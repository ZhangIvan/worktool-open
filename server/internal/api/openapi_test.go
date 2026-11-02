package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
)

// openapiFixture 为兼容面冒烟装配：真 SQLite + 管线 + 网关 + /openapi/v1 路由。
type openapiFixture struct {
	srv   *httptest.Server
	db    *sqlite.DB
	api   *Server
	token string
	robot string
	now   *time.Time
}

func newOpenAPIFixture(t *testing.T) *openapiFixture {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	now := &base
	machine := statemachine.Default()
	pipeline := scheduler.New(db, machine)
	pipeline.Now = func() time.Time { return *now }
	hub := gateway.New(db, pipeline, machine, nil)
	srv := NewServer(db, hub, pipeline, machine, nil)
	srv.Now = func() time.Time { return *now }
	f := &openapiFixture{db: db, api: srv, robot: "robot-openapi-01", now: now}
	mux := NewServeMux(Options{Server: srv})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	token, err := srv.IssueOpenAPIToken(context.Background(), f.robot, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	f.token = token
	return f
}

// post 提交一条指令报文（v1 WeworkMessageBean 单条 = 旧面指令报文语义）。
func (f *openapiFixture) post(t *testing.T, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Idempotency-Key", "idem-"+fmt.Sprint(time.Now().UnixNano()))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return resp.StatusCode, env
}

func (f *openapiFixture) get(t *testing.T, commandID string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands/"+commandID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return resp.StatusCode, env
}

func dataMap(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	d, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺 data: %v", env)
	}
	return d
}

// TestOpenAPISmoke203FullChain P0 冒烟：203（发文本，L3）全链
// REST 入 → 管线（E01 queued）→ 干跑结果（FR-54 同路径终态）→ RAW_CONFIRM 查询。
func TestOpenAPISmoke203FullChain(t *testing.T) {
	f := newOpenAPIFixture(t)

	// 1) REST 入（指令报文 = 旧面单条 v1 载荷，候选字段 targetRoom/receivedContent）
	body := `{"type":203,"roomType":1,"targetRoom":"外部群测试群","receivedContent":"您好，WorkTool-Open 协议测试消息","at":[],"atList":[]}`
	status, env := f.post(t, body, map[string]string{"X-WT-Dry-Run": "true"})
	if status != http.StatusOK {
		t.Fatalf("203 提交 HTTP=%d env=%v", status, env)
	}
	if code := int(env["code"].(float64)); code != 0 {
		t.Fatalf("203 提交 code=%d msg=%v", code, env["message"])
	}
	d := dataMap(t, env)
	commandID, _ := d["commandId"].(string)
	if commandID == "" || d["status"] != "succeeded" || d["dryRun"] != true {
		t.Fatalf("203 干跑提交结果异常: %v", d)
	}
	report, ok := d["dryRunReport"].(map[string]any)
	if !ok {
		t.Fatalf("203 缺干跑报告: %v", d)
	}
	// FR-54 报告要素：目标/参数/风险级
	if report["target"] != "v1:"+f.robot || report["riskClass"] != "L0" {
		t.Fatalf("干跑报告要素异常: %v", report)
	}
	params := report["params"].(map[string]any)
	if params["targetRoom"] != "外部群测试群" {
		t.Fatalf("干跑报告参数异常: %v", params)
	}

	// 2) 管线侧：同引擎同路径（E01→E03→E05→E09→E10 全留痕）+ 不进设备下发队列
	cmd, ok2, _ := f.db.GetCommand(context.Background(), commandID)
	if !ok2 || cmd.CommandType != 203 || cmd.State != "succeeded" || !cmd.DryRun {
		t.Fatalf("管线侧指令异常: %+v", cmd)
	}
	events, _ := f.db.Events(context.Background(), commandID)
	if len(events) != 5 {
		t.Fatalf("干跑应恰留 5 条边事件（E01/E03/E05/E09/E10），实际 %d", len(events))
	}
	queued, _ := f.db.QueuedForTarget(context.Background(), cmd.Target, 10)
	if len(queued) != 0 {
		t.Fatalf("干跑指令不得进入设备下发队列")
	}

	// 3) RAW_CONFIRM 结果查询：终态 + errorCode/errorReason/timeCost/successList/failList
	status, env = f.get(t, commandID)
	if status != http.StatusOK || int(env["code"].(float64)) != 0 {
		t.Fatalf("RAW_CONFIRM 查询 HTTP=%d env=%v", status, env)
	}
	d = dataMap(t, env)
	if d["status"] != "succeeded" || d["finalState"] != "succeeded" || d["terminal"] != true {
		t.Fatalf("结果查询状态异常: %v", d)
	}
	if d["errorCode"].(float64) != 0 {
		t.Fatalf("干跑成功 errorCode 应 0: %v", d)
	}
	if _, has := d["successList"]; !has {
		t.Fatalf("结果查询缺 successList（RAW_CONFIRM 语义字段）: %v", d)
	}
}

// TestOpenAPISmoke206L1Chain P0 冒烟：206（建群，L1）全链——白名单内接收人
// 放行 → 干跑 → RAW_CONFIRM 查询；白名单外接收人拒收（FR-53 L1）。
func TestOpenAPISmoke206L1Chain(t *testing.T) {
	f := newOpenAPIFixture(t)
	ctx := context.Background()

	// 白名单未配置：L1 默认策略放行（限速兜底），干跑全链
	status, env := f.post(t, `{"type":206,"roomType":1,"targetRoom":"白名单群","receivedContent":"建群公告"}`,
		map[string]string{"X-WT-Dry-Run": "true"})
	if status != http.StatusOK || int(env["code"].(float64)) != 0 {
		t.Fatalf("206 提交 HTTP=%d env=%v", status, env)
	}
	id1 := dataMap(t, env)["commandId"].(string)
	cmd, _, _ := f.db.GetCommand(ctx, id1)
	if cmd.RiskClass != "L1" {
		t.Fatalf("206 应分级 L1，实际 %s", cmd.RiskClass)
	}
	if s, g := f.get(t, id1); s != http.StatusOK || dataMap(t, g)["status"] != "succeeded" {
		t.Fatalf("206 干跑后查询应 succeeded")
	}

	// 配置白名单后：白名单外 → 101005 RECIPIENT_NOT_WHITELISTED（HTTP 403）
	if err := f.api.Pipeline.AddWhitelist(ctx, cmd.Target, "白名单群", "admin-1"); err != nil {
		t.Fatal(err)
	}
	status, env = f.post(t, `{"type":206,"roomType":1,"targetRoom":"未登记群","receivedContent":"x"}`, nil)
	if status != http.StatusForbidden {
		t.Fatalf("白名单外应 403，实际 %d", status)
	}
	if code := int(env["code"].(float64)); code != 101005 {
		t.Fatalf("白名单外应 101005，实际 %d %v", code, env)
	}
	// 白名单内 → 放行
	status, env = f.post(t, `{"type":206,"roomType":1,"targetRoom":"白名单群","receivedContent":"再建一個"}`, nil)
	if status != http.StatusOK || int(env["code"].(float64)) != 0 {
		t.Fatalf("白名单内应放行: %d %v", status, env)
	}
}

// TestOpenAPIAuth 验证兼容层鉴权（不引入旧面无鉴权缺陷）：
// 无 token/坏 token → 401（501012 语义）；token 与路径 robotId 不匹配 → 403（501011 语义）。
func TestOpenAPIAuth(t *testing.T) {
	f := newOpenAPIFixture(t)
	body := `{"type":203,"targetRoom":"g","receivedContent":"x"}`

	// 无 Authorization
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "k1")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401，实际 %d", resp.StatusCode)
	}

	// 坏 token
	status, env := f.postWithToken(t, body, "Bearer deadbeef", "k2")
	if status != http.StatusUnauthorized || int(env["code"].(float64)) != 501012 {
		t.Fatalf("坏 token 应 401/501012，实际 %d %v", status, env)
	}

	// token 属于其他 robot → 403/501011
	other, _ := f.api.IssueOpenAPIToken(context.Background(), "robot-other", "tenant-a")
	status, env = f.postWithToken(t, body, "Bearer "+other, "k3")
	if status != http.StatusForbidden || int(env["code"].(float64)) != 501011 {
		t.Fatalf("robot 不匹配应 403/501011，实际 %d %v", status, env)
	}
}

func (f *openapiFixture) postWithToken(t *testing.T, body, auth, idem string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Idempotency-Key", idem)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return resp.StatusCode, env
}

// TestOpenAPIUnknownCommandRejected WTO-004 碰撞拒绝：上报/心跳类 type 与
// 未注册类型不得经兼容面映射为 operation。
func TestOpenAPIUnknownCommandRejected(t *testing.T) {
	f := newOpenAPIFixture(t)
	for _, typ := range []string{"11", "101", "102", "301", "302", "215", "231", "9999"} {
		status, env := f.post(t, `{"type":`+typ+`,"targetRoom":"g","receivedContent":"x"}`, nil)
		if status != http.StatusBadRequest || int(env["code"].(float64)) != 101002 {
			t.Fatalf("type=%s 应 400/101002，实际 %d %v", typ, status, env)
		}
	}
	// 命名空间隔离断言：合法指令的 operation 为 openapi.v1.command.<name>（非裸数字）
	body := `{"type":203,"targetRoom":"g","receivedContent":"x"}`
	if _, env := f.post(t, body, nil); int(env["code"].(float64)) != 0 {
		t.Fatalf("203 应可提交: %v", env)
	}
	rows, _ := f.db.QueryDump(context.Background(),
		`SELECT key FROM idempotency_keys ORDER BY created_at DESC LIMIT 1`)
	if len(rows) == 1 {
		key := rows[0][0]
		if !strings.Contains(key, "openapi.v1.command.send_message") {
			t.Fatalf("幂等键应含命名空间 intent，实际 %q", key)
		}
	}
}

// TestOpenAPIIdempotency 强制 Idempotency-Key；同键同参重放返回原 commandId；
// 同键变参 409（101007）。
func TestOpenAPIIdempotency(t *testing.T) {
	f := newOpenAPIFixture(t)
	body := `{"type":203,"targetRoom":"g","receivedContent":"幂等测试"}`

	// 缺 Idempotency-Key → 400/101003
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, _ := http.DefaultClient.Do(req)
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || int(env["code"].(float64)) != 101003 {
		t.Fatalf("缺幂等键应 400/101003，实际 %d %v", resp.StatusCode, env)
	}

	// 同键同参 → 重放原 commandId
	s1, e1 := f.postWithToken(t, body, "Bearer "+f.token, "same-key")
	s2, e2 := f.postWithToken(t, body, "Bearer "+f.token, "same-key")
	if s1 != 200 || s2 != 200 {
		t.Fatalf("同键同参应均可 200: %d %d", s1, s2)
	}
	if dataMap(t, e1)["commandId"] != dataMap(t, e2)["commandId"] {
		t.Fatalf("同键同参应返回原 commandId: %v vs %v", e1, e2)
	}
	if dataMap(t, e2)["replay"] != true {
		t.Fatalf("重放应标记 replay=true: %v", e2)
	}

	// 同键变参 → 409/101007
	s3, e3 := f.postWithToken(t, `{"type":203,"targetRoom":"g","receivedContent":"变参"}`, "Bearer "+f.token, "same-key")
	if s3 != http.StatusConflict || int(e3["code"].(float64)) != 101007 {
		t.Fatalf("同键变参应 409/101007，实际 %d %v", s3, e3)
	}
}

// TestOpenAPIRateLimit60QPM 验证 60QPM（NFR §2.2 apifox 限制值）：第 61 次
// 请求 429/101004。
func TestOpenAPIRateLimit60QPM(t *testing.T) {
	f := newOpenAPIFixture(t)
	body := `{"type":203,"targetRoom":"g","receivedContent":"x"}`
	var lastStatus int
	var lastEnv map[string]any
	for i := 0; i < 61; i++ {
		lastStatus, lastEnv = f.postWithToken(t, body, "Bearer "+f.token, fmt.Sprintf("rl-%d", i))
		if lastStatus == http.StatusTooManyRequests {
			if i != 60 {
				t.Fatalf("第 %d 次即限速（应第 61 次）", i+1)
			}
			break
		}
	}
	if lastStatus != http.StatusTooManyRequests || int(lastEnv["code"].(float64)) != 101004 {
		t.Fatalf("第 61 次应 429/101004，实际 %d %v", lastStatus, lastEnv)
	}
}

// TestOpenAPIL2ApprovalViaREST L2（219 解散群）经 REST：先 202 待审批
// （101006 + approvalId），独立 approver 决议后携审批提交入队。
func TestOpenAPIL2ApprovalViaREST(t *testing.T) {
	f := newOpenAPIFixture(t)
	ctx := context.Background()
	body := `{"type":219,"targetRoom":"待解散群","receivedContent":""}`

	status, env := f.postWithToken(t, body, "Bearer "+f.token, "l2-1")
	if status != http.StatusAccepted || int(env["code"].(float64)) != 101006 {
		t.Fatalf("L2 无审批应 202/101006，实际 %d %v", status, env)
	}
	approvalID, _ := dataMap(t, env)["approvalId"].(string)
	if approvalID == "" {
		t.Fatalf("应携带 approvalId: %v", env)
	}
	// 独立 approver 决议（自批拒绝路径见 scheduler 包单测）
	if err := f.api.Pipeline.DecideApproval(ctx, approvalID, "approver-a", true, "放行"); err != nil {
		t.Fatal(err)
	}
	// 携审批提交（X-WT-Approval-Id；新幂等键）
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Idempotency-Key", "l2-2-"+approvalID)
	req.Header.Set("X-WT-Approval-Id", approvalID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var env2 map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env2)
	resp.Body.Close()
	status, env = resp.StatusCode, env2
	if status != http.StatusOK || int(env["code"].(float64)) != 0 {
		t.Fatalf("审批后 L2 提交失败: %d %v", status, env)
	}
	cmdID := dataMap(t, env)["commandId"].(string)
	cmd, _, _ := f.db.GetCommand(ctx, cmdID)
	if cmd.RiskClass != "L2" || cmd.State != "queued" {
		t.Fatalf("L2 审批后应入队: %+v", cmd)
	}
}

// TestOpenAPIResultQueryNotFound 查询不存在的指令 / 跨 robot 指令 → 404/101010。
func TestOpenAPIResultQueryNotFound(t *testing.T) {
	f := newOpenAPIFixture(t)
	if status, env := f.get(t, "00000000-0000-4000-8000-000000000000"); status != http.StatusNotFound || int(env["code"].(float64)) != 101010 {
		t.Fatalf("不存在指令应 404/101010，实际 %d %v", status, env)
	}
}

// TestOpenAPIDryRunIdempotentRetry（CR-t32-T3.2-R1）：干跑提交同幂等键重试必须返回
// 原响应（同 commandId + 首次 dryRunReport，replay=true），不得 500——旧实现重试路径
// 无条件再调 ExecuteDryRun，因指令已终态报「干跑执行失败」。
func TestOpenAPIDryRunIdempotentRetry(t *testing.T) {
	f := newOpenAPIFixture(t)
	ctx := context.Background()
	body := `{"type":203,"targetRoom":"外部群测试群","receivedContent":"干跑重试"}`

	postDryRun := func(idem string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Idempotency-Key", idem)
		req.Header.Set("X-WT-Dry-Run", "true")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var env map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&env)
		return resp.StatusCode, env
	}

	s1, e1 := postDryRun("dry-retry-key")
	if s1 != http.StatusOK {
		t.Fatalf("首次干跑提交 HTTP=%d env=%v", s1, e1)
	}
	d1 := dataMap(t, e1)
	if d1["status"] != "succeeded" {
		t.Fatalf("首次干跑应 succeeded: %v", d1)
	}
	r1, ok := d1["dryRunReport"].(map[string]any)
	if !ok {
		t.Fatalf("首次响应缺 dryRunReport: %v", d1)
	}
	commandID, _ := d1["commandId"].(string)

	s2, e2 := postDryRun("dry-retry-key")
	if s2 != http.StatusOK {
		t.Fatalf("同键干跑重试应 200（原响应），实际 HTTP=%d env=%v", s2, e2)
	}
	d2 := dataMap(t, e2)
	if d2["commandId"] != commandID || d2["replay"] != true || d2["status"] != "succeeded" {
		t.Fatalf("重试响应应重放原结果: %v", d2)
	}
	r2, ok := d2["dryRunReport"].(map[string]any)
	if !ok {
		t.Fatalf("重试响应缺 dryRunReport（应复用首次报告）: %v", d2)
	}
	if r1["commandId"] != r2["commandId"] || r1["plannedAction"] != r2["plannedAction"] ||
		r1["riskClass"] != r2["riskClass"] || r1["target"] != r2["target"] {
		t.Fatalf("重试报告要素应与首次一致:\n%v\n%v", r1, r2)
	}

	// 重试不得再次代执行：边事件仍恰为首次的 5 条（E01/E03/E05/E09/E10）
	events, _ := f.db.Events(ctx, commandID)
	if len(events) != 5 {
		t.Fatalf("重试后事件应仍为 5 条（不得重复代执行），实际 %d", len(events))
	}
}

// TestOpenAPIDryRunRetryConvergesAfterInterruptedFirstRun（CR-R1-1）：首次干跑请求
// 在 Submit 落库后、边链完成前中断（停于 dispatched），同键重试不得 500/死键——
// Replay 分支经幂等可重入 ExecuteDryRun 从断点续跑收敛到 succeeded + 报告。
func TestOpenAPIDryRunRetryConvergesAfterInterruptedFirstRun(t *testing.T) {
	f := newOpenAPIFixture(t)
	ctx := context.Background()

	// 与 handler 相同的载荷规范化（json.Marshal 对 map 按键序确定），保证同键同参命中重放
	probe := map[string]json.RawMessage{
		"type":            json.RawMessage(`203`),
		"targetRoom":      json.RawMessage(`"外部群测试群"`),
		"receivedContent": json.RawMessage(`"干跑断点续跑"`),
	}
	payload, _ := json.Marshal(probe)
	res, err := f.api.Pipeline.Submit(ctx, scheduler.SubmitRequest{
		Tenant: "tenant-a", Actor: "openapi:" + f.robot, Operation: OpenAPIIntent(203),
		Key: "dry-resume-key", Target: "v1:" + f.robot, CommandType: 203,
		Payload: string(payload), DryRun: true,
	})
	if err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	// 模拟首次请求在 E03 后、链完成前中断（进程崩溃 / DB 错误）
	if err := f.api.Pipeline.MarkDispatched(ctx, res.CommandID, 0, 0, "evt-dry-resume-1"); err != nil {
		t.Fatalf("模拟中断（E03）失败: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands",
		strings.NewReader(`{"type":203,"targetRoom":"外部群测试群","receivedContent":"干跑断点续跑"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Idempotency-Key", "dry-resume-key")
	req.Header.Set("X-WT-Dry-Run", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("中断后同键重试应收敛 200（不得 500/死键），实际 HTTP=%d env=%v", resp.StatusCode, env)
	}
	d := dataMap(t, env)
	if d["commandId"] != res.CommandID || d["replay"] != true || d["status"] != "succeeded" {
		t.Fatalf("重试应重放并收敛: %v", d)
	}
	if _, ok := d["dryRunReport"].(map[string]any); !ok {
		t.Fatalf("重试响应缺 dryRunReport: %v", d)
	}
	cmd, _, _ := f.db.GetCommand(ctx, res.CommandID)
	if cmd.State != "succeeded" {
		t.Fatalf("指令应收敛 succeeded，实际 %s", cmd.State)
	}
}

// TestOpenAPINonDryRunQueued 非干跑 203：真实链路（无设备在线）指令留队
// （E01 queued），RAW_CONFIRM 查询返回非终态状态（在线设备下发由 gateway 泵承担）。
func TestOpenAPINonDryRunQueued(t *testing.T) {
	f := newOpenAPIFixture(t)
	status, env := f.post(t, `{"type":203,"targetRoom":"g","receivedContent":"真实发送"}`, nil)
	if status != http.StatusOK || int(env["code"].(float64)) != 0 {
		t.Fatalf("非干跑提交失败: %d %v", status, env)
	}
	d := dataMap(t, env)
	if d["status"] != "queued" || d["dryRun"] != false {
		t.Fatalf("非干跑应留队: %v", d)
	}
	if s, g := f.get(t, d["commandId"].(string)); s != http.StatusOK || dataMap(t, g)["terminal"] != false {
		t.Fatalf("非终态查询异常: %v", g)
	}
}
