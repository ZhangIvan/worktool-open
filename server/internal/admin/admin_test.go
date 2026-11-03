// admin 最小面 httptest 套件（W-M4 DoD：四端点正反例——未授权 401、审批自批
// 拒绝 403 等）。装配与 internal/api/openapi_test.go 同型：真 SQLite + 管线 +
// 只读 SQL 投影，不做接口 mock（审计流/审批决议按真实落库断言）。
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/api"
	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/repo"
	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
)

// fixture 为管理面装配：真 SQLite + scheduler 管线 + 在线探测桩 + httptest 服务。
type fixture struct {
	srv    *httptest.Server
	db     *sqlite.DB
	s      *Server
	token  string // 管理面令牌（IssueAdminToken 签发，仅存 hash）
	now    *time.Time
	online map[string]bool // deviceID → 探测桩结果
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	base := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	now := &base
	machine := statemachine.Default()
	pipeline := scheduler.New(db, machine)
	pipeline.Now = func() time.Time { return *now }
	online := map[string]bool{}
	s := New(Deps{
		DB:       db,
		Pipeline: pipeline,
		Now:      func() time.Time { return *now },
		Log:      testLogger{t},
		Online:   func(target string) bool { return online[target] },
	})
	mux := http.NewServeMux()
	s.Register(mux)
	f := &fixture{srv: httptest.NewServer(mux), db: db, s: s, now: now, online: online}
	t.Cleanup(f.srv.Close)
	f.token, err = s.IssueAdminToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...any) {
	l.t.Logf(format, args...)
}

// call 发起一次管理面请求；auth=false 时不带 Authorization 头。
func (f *fixture) call(t *testing.T, method, path string, body string, auth bool) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body == "" {
		rd = bytes.NewReader(nil)
	} else {
		rd = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// ---- 端点 1：POST /api/v1/admin/pairing-codes ----

func TestPairingCodeIssueAndSingleUse(t *testing.T) {
	f := newFixture(t)
	code, status, body := func() (string, int, map[string]any) {
		st, out := f.call(t, http.MethodPost, "/api/v1/admin/pairing-codes", "{}", true)
		c, _ := out["pairingCode"].(string)
		return c, st, out
	}()
	if status != http.StatusCreated {
		t.Fatalf("签发配对码 status=%d body=%v", status, body)
	}
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
		t.Fatalf("配对码须为 6 位数字，got %q", code)
	}
	if ttl, _ := body["ttlSeconds"].(float64); int(ttl) != 600 {
		t.Fatalf("TTL 须为 600s（spec §5.1.1 冻结 10min），got %v", body["ttlSeconds"])
	}
	// 端到端可消费性（替代 DB 引导的验收实质）：落库 hash 能被 /api/v2/pair
	// 的消费路径原子取用；单次有效（第二次拒绝）。
	// （另：非 JSON 体 → 400，见 TestMethodGuards 后的负例。）
	ctx := context.Background()
	ok, err := f.db.ConsumePairingCode(ctx, sha256HexForTest(code), f.now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("签发的配对码应可消费一次：ok=%v err=%v", ok, err)
	}
	ok, err = f.db.ConsumePairingCode(ctx, sha256HexForTest(code), f.now.Add(time.Minute))
	if err != nil || ok {
		t.Fatalf("配对码须单次有效（第二次消费应拒绝）：ok=%v err=%v", ok, err)
	}
	// 签发入审计（SR-12）。
	rows, err := f.db.QueryDump(ctx, `SELECT COUNT(*) FROM audit_log WHERE kind='admin_pairing_code_issued'`)
	if err != nil || len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("签发须留审计痕：rows=%v err=%v", rows, err)
	}
}

// ---- 端点 2：GET /api/v1/admin/devices ----

func TestDevicesListOnlineStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	onDev, offDev := "device-online-01", "device-offline-02"
	nowT := *f.now
	for _, d := range []sqlite.Device{
		{DeviceID: onDev, RobotID: "robot-a", DeviceName: "在线机", Status: "active", PairedAt: nowT.Add(-time.Hour), UpdatedAt: nowT},
		{DeviceID: offDev, DeviceName: "离线机", Status: "active", PairedAt: nowT, UpdatedAt: nowT},
	} {
		if err := f.db.UpsertDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	f.online[onDev] = true
	status, body := f.call(t, http.MethodGet, "/api/v1/admin/devices", "", true)
	if status != http.StatusOK {
		t.Fatalf("设备列表 status=%d", status)
	}
	devs, _ := body["devices"].([]any)
	if len(devs) != 2 {
		t.Fatalf("应列出 2 台设备，got %v", devs)
	}
	byID := map[string]map[string]any{}
	for _, d := range devs {
		m, _ := d.(map[string]any)
		byID[m["deviceId"].(string)] = m
	}
	if byID[onDev]["online"] != true {
		t.Fatalf("在线探测桩为 true 的设备应报 online=true，got %v", byID[onDev])
	}
	if byID[offDev]["online"] != false {
		t.Fatalf("无活连接设备应报 online=false，got %v", byID[offDev])
	}
	if byID[onDev]["robotId"] != "robot-a" {
		t.Fatalf("robotId 应如实展示，got %v", byID[onDev]["robotId"])
	}
	if byID[offDev]["robotId"] != "" {
		t.Fatalf("未绑定 robot 应为空串（重配对清绑定如实展示），got %v", byID[offDev]["robotId"])
	}
}

// ---- 端点 3：GET /api/v1/admin/commands（审计流 + dryRun 标记） ----

func TestCommandAuditStreamDryRunFlag(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dry, err := f.s.pipeline.Submit(ctx, scheduler.SubmitRequest{
		Tenant: "default", Actor: "ops", Target: "device-audit-01",
		CommandType: 501, Payload: `{"type":501,"receivedContent":"G1"}`, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 干跑同步代执行（与 openapi REST 面同路径：Submit 后 ExecuteDryRun 走完
	// 同一状态机至 succeeded，FR-54）。
	if _, err := f.s.pipeline.ExecuteDryRun(ctx, dry.CommandID); err != nil {
		t.Fatal(err)
	}
	// 推进时钟 1s，保证审计流倒序断言与创建毫秒可区分（fixture 时钟冻结）。
	*f.now = f.now.Add(time.Second)
	real, err := f.s.pipeline.Submit(ctx, scheduler.SubmitRequest{
		Tenant: "default", Actor: "ops", Target: "device-audit-01",
		CommandType: 502, Payload: `{"type":502,"receivedContent":"F1"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := f.call(t, http.MethodGet, "/api/v1/admin/commands?limit=10", "", true)
	if status != http.StatusOK {
		t.Fatalf("审计流 status=%d", status)
	}
	cmds, _ := body["commands"].([]any)
	if len(cmds) != 2 {
		t.Fatalf("应含 2 条指令，got %v", cmds)
	}
	byID := map[string]map[string]any{}
	for _, c := range cmds {
		m, _ := c.(map[string]any)
		byID[m["commandId"].(string)] = m
	}
	if byID[dry.CommandID]["dryRun"] != true {
		t.Fatalf("干跑指令须带 dryRun=true，got %v", byID[dry.CommandID])
	}
	if byID[real.CommandID]["dryRun"] != false {
		t.Fatalf("真实指令须带 dryRun=false，got %v", byID[real.CommandID])
	}
	// 时间倒序（最新在前）。
	if cmds[0].(map[string]any)["commandId"] != real.CommandID {
		t.Fatalf("审计流应按时间倒序，首行=%v", cmds[0])
	}
	// state 过滤与非法 limit 反例。
	if st, b := f.call(t, http.MethodGet, "/api/v1/admin/commands?state=queued", "", true); st != http.StatusOK {
		t.Fatalf("state 过滤 status=%d body=%v", st, b)
	} else if n, _ := b["count"].(float64); int(n) != 1 {
		t.Fatalf("state=queued 应仅 1 条（干跑同步走完状态机），got %v", b["count"])
	}
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/commands?limit=501", "", true); st != http.StatusBadRequest {
		t.Fatalf("limit>500 应 400，got %d", st)
	}
}

// ---- 端点 4：POST /api/v1/admin/approvals/{id}/decision ----

func TestApprovalDecisionFlow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const (
		actor  = "alice"
		target = "device-appr-01"
	)
	ap, err := f.s.pipeline.RequestApproval(ctx, scheduler.ApprovalRequestInput{
		Tenant: "default", Actor: actor, Target: target,
		CommandType: 219, Payload: `{"type":219,"targetRoom":"G1"}`, // 219 解散群 = L2（FR-53 分级表）
		Reason: "L2 危险指令（FR-53）自动登记待审批",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 审批列表（UI 数据面）。
	if st, b := f.call(t, http.MethodGet, "/api/v1/admin/approvals", "", true); st != http.StatusOK {
		t.Fatalf("审批列表 status=%d body=%v", st, b)
	} else if n, _ := b["count"].(float64); int(n) != 1 {
		t.Fatalf("pending 审批应 1 条，got %v", b)
	}
	// 自批拒绝（DoD 反例）：approver == actor → 403 APPROVER_CONFLICT。
	path := "/api/v1/admin/approvals/" + ap.ApprovalID + "/decision"
	st, b := f.call(t, http.MethodPost, path, `{"approver":"alice","decision":"approve","reason":"自批"}`, true)
	if st != http.StatusForbidden || b["error"] != "APPROVER_CONFLICT" {
		t.Fatalf("自批应 403 APPROVER_CONFLICT，got %d %v", st, b)
	}
	// 决议值非法 / 未知字段 / 空 approver → 400。
	if st, _ := f.call(t, http.MethodPost, path, `{"approver":"bob","decision":"maybe"}`, true); st != http.StatusBadRequest {
		t.Fatalf("非法 decision 应 400，got %d", st)
	}
	if st, _ := f.call(t, http.MethodPost, path, `{"approver":"bob","decision":"approve","extra":1}`, true); st != http.StatusBadRequest {
		t.Fatalf("未知字段应 400，got %d", st)
	}
	if st, _ := f.call(t, http.MethodPost, path, `{"decision":"approve"}`, true); st != http.StatusBadRequest {
		t.Fatalf("缺 approver 应 400，got %d", st)
	}
	// 正例：独立 approver 通过。
	st, b = f.call(t, http.MethodPost, path, `{"approver":"bob","decision":"approve","reason":"复核通过"}`, true)
	if st != http.StatusOK || b["status"] != "approved" {
		t.Fatalf("独立 approver 决议应 200 approved，got %d %v", st, b)
	}
	// 重复决议 → 409（仅 pending 可决议）。
	if st, _ := f.call(t, http.MethodPost, path, `{"approver":"carol","decision":"reject"}`, true); st != http.StatusConflict {
		t.Fatalf("重复决议应 409，got %d", st)
	}
	// 不存在 → 404。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/approvals/no-such-id/decision", `{"approver":"bob","decision":"approve"}`, true); st != http.StatusNotFound {
		t.Fatalf("不存在审批应 404，got %d", st)
	}
	// 端到端价值证明：管理面通过的审批能解锁 L2 提交入队（参数指纹一致）。
	res, err := f.s.pipeline.Submit(ctx, scheduler.SubmitRequest{
		Tenant: "default", Actor: actor, Target: target,
		CommandType: 219, Payload: `{"type":219,"targetRoom":"G1"}`, ApprovalID: ap.ApprovalID,
	})
	if err != nil || res.State != "queued" {
		t.Fatalf("已批审批携载提交应入队：state=%v err=%v", res.State, err)
	}
	// 决议入审计（SR-12）。
	rows, err := f.db.QueryDump(ctx, `SELECT COUNT(*) FROM audit_log WHERE kind='approval_decided' AND subject=?`, ap.ApprovalID)
	if err != nil || rows[0][0] != "1" {
		t.Fatalf("决议须留审计痕：rows=%v err=%v", rows, err)
	}
}

func TestApprovalRejectDecision(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ap, err := f.s.pipeline.RequestApproval(ctx, scheduler.ApprovalRequestInput{
		Tenant: "default", Actor: "dave", Target: "device-appr-02",
		CommandType: 234, Payload: `{"type":234}`, Reason: "L2",
	})
	if err != nil {
		t.Fatal(err)
	}
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/approvals/"+ap.ApprovalID+"/decision",
		`{"approver":"erin","decision":"reject","reason":"风险不通过"}`, true)
	if st != http.StatusOK || b["status"] != "rejected" {
		t.Fatalf("拒绝决议应 200 rejected，got %d %v", st, b)
	}
	// 被拒审批不能再解锁 L2 提交（verifyApproval：须 approved）。
	if _, err := f.s.pipeline.Submit(ctx, scheduler.SubmitRequest{
		Tenant: "default", Actor: "dave", Target: "device-appr-02",
		CommandType: 234, Payload: `{"type":234}`, ApprovalID: ap.ApprovalID,
	}); err == nil {
		t.Fatal("被拒审批携载提交必须被管线拒绝")
	}
}

// ---- 鉴权反例（DoD：未授权 401）----

func TestAuthUnauthorized(t *testing.T) {
	f := newFixture(t)
	paths := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/pairing-codes"},
		{http.MethodGet, "/api/v1/admin/devices"},
		{http.MethodGet, "/api/v1/admin/commands"},
		{http.MethodGet, "/api/v1/admin/approvals"},
		{http.MethodPost, "/api/v1/admin/approvals/x/decision"},
	}
	for _, p := range paths {
		if st, b := f.call(t, p.method, p.path, "{}", false); st != http.StatusUnauthorized || b["error"] != "INVALID_TOKEN" {
			t.Fatalf("%s %s 无鉴权应 401 INVALID_TOKEN，got %d %v", p.method, p.path, st, b)
		}
	}
	// 错误令牌 / 非 admin scope（业务 robot 令牌）/ 已吊销 admin 令牌 → 401。
	ctx := context.Background()
	if err := f.db.InsertAPIToken(ctx, sha256HexForTest("wrong-token"), "robot-x", "default", f.now.UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.db.InsertAPIToken(ctx, sha256HexForTest("robot-scope-token"), "robot-openapi-01", "default", f.now.UTC()); err != nil {
		t.Fatal(err)
	}
	revoked, err := f.s.IssueAdminToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE api_tokens SET status='revoked' WHERE token_hash=?`, sha256HexForTest(revoked)); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"错误令牌":   "wrong-token",
		"非admin": "robot-scope-token",
		"已吊销":    revoked,
	} {
		req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/admin/devices", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 令牌应 401，got %d", name, resp.StatusCode)
		}
	}
}

// ---- 方法守卫与 UI 伺服 ----

func TestMethodGuards(t *testing.T) {
	f := newFixture(t)
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/pairing-codes", "", true); st != http.StatusMethodNotAllowed {
		t.Fatalf("GET pairing-codes 应 405，got %d", st)
	}
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/devices", "{}", true); st != http.StatusMethodNotAllowed {
		t.Fatalf("POST devices 应 405，got %d", st)
	}
	// 空体与合法空对象放行（上方正例覆盖 {}）；非 JSON / 含未定义字段 → 400。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/pairing-codes", "", true); st != http.StatusCreated {
		t.Fatalf("空体签发应 201，got %d", st)
	}
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/pairing-codes", "not-json", true); st != http.StatusBadRequest {
		t.Fatalf("非 JSON 体应 400，got %d", st)
	}
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/pairing-codes", `{"ttlSeconds":60}`, true); st != http.StatusBadRequest {
		t.Fatalf("含未定义字段应 400（TTL 冻结不可配），got %d", st)
	}
}

func TestUIServing(t *testing.T) {
	root, err := repo.Root()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	now := &base
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pipeline := scheduler.New(db, statemachine.Default())
	pipeline.Now = func() time.Time { return *now }
	s := New(Deps{DB: db, Pipeline: pipeline, Now: func() time.Time { return *now }, Log: testLogger{t}, UIDir: filepath.Join(root, "admin")})
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/ 应 200，got %d", resp.StatusCode)
	}
	pageBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageBytes)
	for _, marker := range []string{
		"WorkTool-Open", "pairing-codes", "approvals",
		// W-P0 完整管理后台视图与 API 面对齐（概览/队列/回调/日志/设置）。
		"admin/overview", "admin/queue", "admin/callbacks", "admin/whitelist",
		"logs/executions", "logs/audit", "settings/guard", "settings/encryption",
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("index.html 应含 %q（UI 与 API 面对齐）", marker)
		}
	}
	// 零外部资源纪律（G3/G4）：无 CDN/外链字体/图片引用。
	for _, banned := range []string{"http://cdn", "https://cdn", "googleapis", "jsdelivr", "unpkg", "<img ", "background-image:url(http"} {
		if strings.Contains(page, banned) {
			t.Fatalf("index.html 不得含外部资源引用 %q（零第三方资源纪律）", banned)
		}
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type 应为 text/html，got %q", ct)
	}
	// /admin → /admin/ 重定向（不跟随跳转判定）。
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp2, err := noRedirect.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusFound {
		t.Fatalf("GET /admin 应 302，got %d", resp2.StatusCode)
	}
	// 不存在的静态资源 → 404（FileServer 默认行为，防目录穿越由其保证）。
	resp3, err := http.Get(srv.URL + "/admin/no-such-file.js")
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在资源应 404，got %d", resp3.StatusCode)
	}
}

func sha256HexForTest(s string) string {
	return sha256Hex(s)
}

// TestAdminTokenRejectedOnOpenAPIFace（CR ADM-1 反例）：admin scope（api_tokens.
// robot_id='admin'）只对 /api/v1/admin/* 有效——openapi 兼容面在签发与鉴权两侧
// 硬拒保留名：admin 令牌打 /openapi/v1/robots/admin/commands 的 t.RobotID ==
// 路径寻址**恰好匹配**（非「寻址不匹配无意义」），不硬拒即可经 resolveTarget
// 进入业务管线。两包共享常量 sqlite.ReservedAdminScopeRobotID 防语义漂移。
func TestAdminTokenRejectedOnOpenAPIFace(t *testing.T) {
	f := newFixture(t)
	machine := statemachine.Default()
	pipeline := scheduler.New(f.db, machine)
	pipeline.Now = func() time.Time { return *f.now }
	hub := gateway.New(f.db, pipeline, machine, testLogger{t})
	apiSrv := api.NewServer(f.db, hub, pipeline, machine, testLogger{t})
	apiSrv.Now = func() time.Time { return *f.now }
	mux := api.NewServeMux(api.Options{Server: apiSrv})
	openapiSrv := httptest.NewServer(mux)
	t.Cleanup(openapiSrv.Close)

	// 签发侧硬拒：不得为保留名 admin 签发兼容面令牌（否则静默铸出第二个
	// admin-scope 令牌）
	if _, err := apiSrv.IssueOpenAPIToken(context.Background(), sqlite.ReservedAdminScopeRobotID, "default"); err == nil {
		t.Fatal("为保留名 admin 签发兼容面令牌必须失败")
	}

	// 鉴权侧硬拒：管理面签发的 admin 令牌 + robotId=admin 寻址 → 403
	body := `{"type":203,"targetRoom":"g","receivedContent":"x"}`
	req, _ := http.NewRequest(http.MethodPost,
		openapiSrv.URL+"/openapi/v1/robots/"+sqlite.ReservedAdminScopeRobotID+"/commands",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Idempotency-Key", "admin-face-neg")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin 令牌打兼容面应 403（保留名硬拒），got %d", resp.StatusCode)
	}

	// 对照：同一 admin 令牌打管理面仍正常（硬拒只影响兼容面）
	if st, b := f.call(t, http.MethodGet, "/api/v1/admin/devices", "", true); st != http.StatusOK {
		t.Fatalf("admin 令牌打管理面应 200，got %d %v", st, b)
	}
}

// TestOpenAPITokenLifecycleViaAdmin（W-M5 日常使用闭环）：管理面签发兼容面令牌
// → 令牌可打 /openapi/v1 全量面 → 列表只读（hash 前缀，无原始值）→ 吊销后
// 兼容面 401。保留名 admin 签发硬拒（CR ADM-1）；无管理令牌 401。
func TestOpenAPITokenLifecycleViaAdmin(t *testing.T) {
	f := newFixture(t)
	machine := statemachine.Default()
	pipeline := scheduler.New(f.db, machine)
	pipeline.Now = func() time.Time { return *f.now }
	hub := gateway.New(f.db, pipeline, machine, testLogger{t})
	apiSrv := api.NewServer(f.db, hub, pipeline, machine, testLogger{t})
	apiSrv.Now = func() time.Time { return *f.now }
	mux := api.NewServeMux(api.Options{Server: apiSrv})
	openapiSrv := httptest.NewServer(mux)
	t.Cleanup(openapiSrv.Close)

	// 1) 签发（tenant 可选参数）
	st, body := f.call(t, http.MethodPost, "/api/v1/admin/robots/robot-daily/openapi-tokens",
		`{"tenant":"t1"}`, true)
	if st != http.StatusCreated {
		t.Fatalf("签发应 201，got %d %v", st, body)
	}
	token, _ := body["token"].(string)
	if len(token) != 64 { // 256-bit hex
		t.Fatalf("令牌应为 64 hex，got %q", token)
	}

	// 2) 集成：该令牌打兼容面 info → 200（鉴权归属核对通过）
	req, _ := http.NewRequest(http.MethodGet, openapiSrv.URL+"/openapi/v1/robots/robot-daily/info", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("管理面签发令牌应可打兼容面，got %d", resp.StatusCode)
	}

	// 3) 列表只读：hash 前缀 + 状态；原始值不得再现
	st, body = f.call(t, http.MethodGet, "/api/v1/admin/robots/robot-daily/openapi-tokens", "", true)
	if st != http.StatusOK {
		t.Fatalf("列表应 200，got %d %v", st, body)
	}
	if count, _ := body["count"].(float64); count != 1 {
		t.Fatalf("应 1 枚令牌: %v", body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), token) {
		t.Fatal("列表响应不得包含令牌原始值")
	}

	// 4) 吊销 → 兼容面即 401（旋转路径生效）
	st, body = f.call(t, http.MethodDelete, "/api/v1/admin/robots/robot-daily/openapi-tokens", "", true)
	if st != http.StatusOK || body["revoked"].(float64) != 1 {
		t.Fatalf("吊销应 revoked=1: %d %v", st, body)
	}
	req, _ = http.NewRequest(http.MethodGet, openapiSrv.URL+"/openapi/v1/robots/robot-daily/info", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("吊销后兼容面应 401，got %d", resp.StatusCode)
	}

	// 5) 保留名硬拒：为 admin 签发兼容面令牌 → 403（CR ADM-1）
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/robots/"+sqlite.ReservedAdminScopeRobotID+"/openapi-tokens", "", true); st != http.StatusForbidden {
		t.Fatalf("保留名签发应 403，got %d", st)
	}

	// 6) 鉴权负例：无管理令牌 → 401
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/robots/robot-daily/openapi-tokens", "", false); st != http.StatusUnauthorized {
		t.Fatalf("无鉴权应 401，got %d", st)
	}
}
