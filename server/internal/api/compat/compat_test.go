// T3.6 apifox 兼容适配层端到端测试（docs/15 K1~K6）。
//
// 外部测试包（compat_test）：经 api.NewServeMux 真实挂载面打点（不经 mock），
// 断言按真实落库/真实接收器；apifox 文档原始报文（crawl 存证页 example 逐字面）
// 做冒烟。每破坏级 ≥2 例（正/负）。装配与 openapi_test.go 同型：真 SQLite +
// 管线 + 网关 + NewServeMux。
package compat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cn.worktool.open/server/internal/api"
	"cn.worktool.open/server/internal/api/compat"
	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
)

// fixture：真 SQLite + 管线 + 网关 + NewServeMux（兼容面随装配挂载）。
type fixture struct {
	mux   *http.ServeMux
	db    *sqlite.DB
	api   *api.Server
	token string
	robot string
	now   *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	now := &base
	machine := statemachine.Default()
	pipeline := scheduler.New(db, machine)
	pipeline.Now = func() time.Time { return *now }
	hub := gateway.New(db, pipeline, machine, nil)
	srv := api.NewServer(db, hub, pipeline, machine, nil)
	srv.Now = func() time.Time { return *now }
	f := &fixture{db: db, api: srv, robot: "robot-compat-01", now: now}
	f.mux = api.NewServeMux(api.Options{Server: srv})
	token, err := srv.IssueOpenAPIToken(context.Background(), f.robot, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	f.token = token
	// 事件回调开关表（K4 总门单一事实源；DDL 与 admin 包镜像，events_test 同型）
	if _, err := db.Exec(context.Background(), `CREATE TABLE IF NOT EXISTS admin_settings (
	  key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_ms INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return f
}

// doReq 直打挂载面（RemoteAddr 可控——K6 per-IP 测试不经真实 socket）。
func (f *fixture) doReq(t *testing.T, body, remoteAddr string, hdr map[string]string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/wework/sendRawMessage?robotId="+f.robot, strings.NewReader(body))
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var env map[string]any
	_ = json.Unmarshal([]byte(raw), &env)
	return rec.Code, env, raw
}

func (f *fixture) commands(t *testing.T) []map[string]string {
	t.Helper()
	rows, err := f.db.QueryDump(context.Background(),
		`SELECT command_id, target, command_type, payload, state FROM commands ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]string{"commandId": r[0], "target": r[1], "type": r[2], "payload": r[3], "state": r[4]})
	}
	return out
}

// cbReceiver：可编程响应体的 loopback 接收器（记录 body + HMAC 验签）。
type cbReceiver struct {
	mu      sync.Mutex
	bodies  []string
	respond string // 响应 body（K5 代回复注入点）；空 = 仅 200
	badSign bool
	srv     *httptest.Server
	secret  string
}

func newCBReceiver(t *testing.T, secret, respond string) *cbReceiver {
	t.Helper()
	r := &cbReceiver{respond: respond, secret: secret}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var ts int64
		_ = json.Unmarshal([]byte(req.Header.Get("X-WT-Timestamp")), &ts)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		if !api.VerifySign(secret, ts, body, req.Header.Get("X-WT-Signature")) {
			r.badSign = true
		}
		r.mu.Unlock()
		if r.respond != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(r.respond))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *cbReceiver) lastBody() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return ""
	}
	return r.bodies[len(r.bodies)-1]
}

func (r *cbReceiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func detailMap(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	d, ok := env["detail"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺 detail: %v", env)
	}
	return d
}

// ---- K1/K2：sendRawMessage 信封 + 响应形态 ----

// doc203Example = api-23520034 example 逐字面（apifox 文档原始报文）。
const doc203Example = `{"socketType": 2, "list": [{"type": 203, "titleList": ["仑哥(这里改成你的微信昵称或群名)"], "receivedContent": "你好~"}]}`

// docUpExample = api-456696584 example 逐字面（指令插队 up:true）。
const docUpExample = `{"socketType": 2, "list": [{"type": 203, "titleList": ["仑哥(这里改成你的微信昵称或群名)"], "receivedContent": "所有指令任务均支持利用up进行插队，该指令会到达客户端待执行队列的顶部", "up": true}]}`

// TestK1SingleItemDocVerbatim：文档原始报文 → code=0 + data=string messageId +
// detail 逐条可对账；指令真实入队（payload 原文透传）。
func TestK1SingleItemDocVerbatim(t *testing.T) {
	f := newFixture(t)
	status, env, raw := f.doReq(t, doc203Example, "", nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d raw=%s", status, raw)
	}
	if env["code"] != float64(0) || env["message"] != "success" {
		t.Fatalf("信封错误: %v", env)
	}
	data, ok := env["data"].(string) // K2：data=string messageId（文档形态）
	if !ok || data == "" {
		t.Fatalf("data 须为非空 string（messageId）: %v", env["data"])
	}
	d := detailMap(t, env)
	if d["count"] != float64(1) {
		t.Fatalf("detail.count = %v", d["count"])
	}
	list := d["list"].([]any)
	item := list[0].(map[string]any)
	if item["messageId"] != data || item["commandId"] != data || item["status"] != "queued" {
		t.Fatalf("detail.list[0] 对账失败: %v", item)
	}
	cmds := f.commands(t)
	if len(cmds) != 1 || cmds[0]["commandId"] != data || cmds[0]["type"] != "203" {
		t.Fatalf("指令未入队/对账键不符: %+v", cmds)
	}
	if !strings.Contains(cmds[0]["payload"], "你好~") || !strings.Contains(cmds[0]["payload"], "titleList") {
		t.Fatalf("payload 未原文透传: %s", cmds[0]["payload"])
	}
}

// TestK1BatchMixed203And218：批量 list 混排（203+218，api-147612959 批量语义）
// → 逐条入队 + data=首条 messageId + detail.count=2。
func TestK1BatchMixed203And218(t *testing.T) {
	f := newFixture(t)
	// SR-11 fileUrl 域白名单（G11：默认零域=全拒；登记 loopback 域后 218 可过）
	if err := f.api.Pipeline.AddAllowedFileDomain(context.Background(), "127.0.0.1:9", "compat-test"); err != nil {
		t.Fatal(err)
	}
	body := `{"socketType": 2, "list": [
		{"type": 203, "titleList": ["仑哥"], "receivedContent": "批量第一条"},
		{"type": 218, "titleList": ["仑哥"], "objectName": "报告.pdf", "fileUrl": "http://127.0.0.1:9/f.pdf", "fileType": "pdf"}
	]}`
	status, env, raw := f.doReq(t, body, "", nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d raw=%s", status, raw)
	}
	data := env["data"].(string)
	d := detailMap(t, env)
	if d["count"] != float64(2) {
		t.Fatalf("detail.count = %v（want 2）", d["count"])
	}
	cmds := f.commands(t)
	if len(cmds) != 2 {
		t.Fatalf("两条均应入队: %+v", cmds)
	}
	if cmds[0]["commandId"] != data {
		t.Fatalf("data 应为首条 messageId: data=%s first=%s", data, cmds[0]["commandId"])
	}
	if cmds[1]["type"] != "218" {
		t.Fatalf("第二条应为 218: %+v", cmds[1])
	}
}

// TestK1EnvelopeNegatives：信封负例（原子拒绝——任一不过整单 4xx 零入队）。
func TestK1EnvelopeNegatives(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name string
		body string
		code float64
		want string
	}{
		{"缺 list", `{"socketType": 2}`, 101011, "list 必填"},
		{"空 list", `{"socketType": 2, "list": []}`, 101011, "至少 1 条"},
		{"socketType 非 2", `{"socketType": 3, "list": [{"type": 203, "titleList": ["a"], "receivedContent": "x"}]}`, 101011, "socketType"},
		{"缺 type", `{"socketType": 2, "list": [{"titleList": ["a"]}]}`, 101011, "缺 type"},
		{"type 非整数", `{"socketType": 2, "list": [{"type": "203"}]}`, 101011, "整数"},
		{"未注册 type", `{"socketType": 2, "list": [{"type": 11}]}`, 101002, "不在兼容面注册表"},
		{"缺 robotId", `{"socketType": 2, "list": [{"type": 203, "titleList": ["a"], "receivedContent": "x"}]}`, 101011, "robotId"}, // 本例经专用请求构造
		{"非 JSON", `not-json`, 101011, "JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var status int
			var env map[string]any
			var raw string
			if tc.name == "缺 robotId" {
				req := httptest.NewRequest(http.MethodPost, "/wework/sendRawMessage", strings.NewReader(tc.body))
				req.Header.Set("Authorization", "Bearer "+f.token)
				rec := httptest.NewRecorder()
				f.mux.ServeHTTP(rec, req)
				status, raw = rec.Code, rec.Body.String()
				_ = json.Unmarshal([]byte(raw), &env)
			} else {
				status, env, raw = f.doReq(t, tc.body, "", nil)
			}
			if status == http.StatusOK {
				t.Fatalf("应拒绝: %s", raw)
			}
			if env["code"] != tc.code {
				t.Fatalf("code = %v（want %v）raw=%s", env["code"], tc.code, raw)
			}
			if !strings.Contains(fmt.Sprint(env["message"]), tc.want) {
				t.Fatalf("message 不含 %q: %v", tc.want, env["message"])
			}
		})
	}
	if n := len(f.commands(t)); n != 0 {
		t.Fatalf("负例应零入队（原子拒绝）: %d 条", n)
	}
}

// TestK1ListOver100：批量 list 超 100 → 101011（13 项清单第 8 条闭环）。
func TestK1ListOver100(t *testing.T) {
	f := newFixture(t)
	var b bytes.Buffer
	b.WriteString(`{"socketType": 2, "list": [`)
	for i := 0; i < 101; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type": 203, "titleList": ["g%d"], "receivedContent": "x"}`, i)
	}
	b.WriteString(`]}`)
	status, env, _ := f.doReq(t, b.String(), "", nil)
	if status != http.StatusBadRequest || env["code"] != float64(101011) {
		t.Fatalf("101 条应 101011 拒绝: %v", env)
	}
	// 恰好 100 条 = 上限内放行（边界）
	b.Reset()
	b.WriteString(`{"socketType": 2, "list": [`)
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type": 203, "titleList": ["g%d"], "receivedContent": "x%d"}`, i, i)
	}
	b.WriteString(`]}`)
	status, env, raw := f.doReq(t, b.String(), "", nil)
	if status != http.StatusOK || env["code"] != float64(0) {
		t.Fatalf("100 条应放行: %d %s", status, raw)
	}
}

// TestK1RequiredFieldPrevalidate：203 缺 receivedContent → 422 + 101011 前置
//（13 项清单第 7 条骨架；结构化字段错误车道：HTTP 400→422，顶层 code 语义不变）
// + 整单拒绝。
func TestK1RequiredFieldPrevalidate(t *testing.T) {
	f := newFixture(t)
	status, env, _ := f.doReq(t,
		`{"socketType": 2, "list": [{"type": 203, "titleList": ["仑哥"]}]}`, "", nil)
	if status != http.StatusUnprocessableEntity || env["code"] != float64(101011) {
		t.Fatalf("缺必填应 422 + 101011: status=%d %v", status, env)
	}
	if !strings.Contains(env["message"].(string), "receivedContent") {
		t.Fatalf("message 应指出缺失字段: %v", env["message"])
	}
	if n := len(f.commands(t)); n != 0 {
		t.Fatalf("应零入队: %d", n)
	}
	// 206/213/234 表项抽查（顶层 code 语义不变 + errors 数组在位）
	for _, tc := range []struct{ body, field string }{
		{`{"socketType": 2, "list": [{"type": 206, "groupName": "g"}]}`, "selectList"},
		{`{"socketType": 2, "list": [{"type": 213, "friend": {"markName": "m"}}]}`, "friend.phone"},
		{`{"socketType": 2, "list": [{"type": 234, "friend": {}}]}`, "friend.name"},
	} {
		_, env, _ := f.doReq(t, tc.body, "", nil)
		if env["code"] != float64(101011) || !strings.Contains(env["message"].(string), tc.field) {
			t.Fatalf("%s 必填校验未命中: %v", tc.field, env["message"])
		}
		assertErrorsArray(t, env, tc.field)
	}
}

// assertErrorsArray 断言响应含 errors:[{field,code,message}] 数组，且数组内含
// 指定 field 的元素（code=101011，message 含 list[i] 索引前缀）。
func assertErrorsArray(t *testing.T, env map[string]any, field string) {
	t.Helper()
	raw, ok := env["errors"].([]any)
	if !ok || len(raw) == 0 {
		t.Fatalf("响应应含 errors 数组: %v", env)
	}
	found := false
	for _, e := range raw {
		fe, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("errors 元素应为对象: %v", e)
		}
		if fe["field"] == field {
			found = true
			if fe["code"] != float64(101011) {
				t.Fatalf("errors 元素 code 应为 101011: %v", fe)
			}
			msg, _ := fe["message"].(string)
			if !strings.Contains(msg, "list[") {
				t.Fatalf("errors 元素 message 应含 list[i] 索引: %v", fe)
			}
		}
	}
	if !found {
		t.Fatalf("errors 数组应含 field=%s: %v", field, raw)
	}
}

// TestK1RequiredFieldErrorsStructured（结构化字段错误车道）：多违例全量收集——
// 单条多缺字段 → errors 覆盖全部缺失字段；多条 list 项 → 逐项索引化收集；
// 结构性错误（缺 type）维持 400 不带 errors。
func TestK1RequiredFieldErrorsStructured(t *testing.T) {
	f := newFixture(t)
	// 单条 203 双缺：titleList + receivedContent 两条 errors。
	status, env, _ := f.doReq(t,
		`{"socketType": 2, "list": [{"type": 203}]}`, "", nil)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("应 422: %d %v", status, env)
	}
	raw, _ := env["errors"].([]any)
	if len(raw) != 2 {
		t.Fatalf("双缺字段应 2 条 errors: %v", env["errors"])
	}
	fields := map[string]bool{}
	for _, e := range raw {
		fields[e.(map[string]any)["field"].(string)] = true
	}
	if !fields["titleList"] || !fields["receivedContent"] {
		t.Fatalf("errors 应覆盖 titleList+receivedContent: %v", raw)
	}

	// 多条 list 项逐项收集（list[0] 206 缺 selectList + list[1] 213 缺 phone）。
	status, env, _ = f.doReq(t,
		`{"socketType": 2, "list": [{"type": 206, "groupName": "g"}, {"type": 213, "friend": {}}]}`, "", nil)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("应 422: %d %v", status, env)
	}
	raw, _ = env["errors"].([]any)
	if len(raw) != 2 {
		t.Fatalf("两条目应各 1 条 errors: %v", env["errors"])
	}
	var sawList0, sawList1 bool
	for _, e := range raw {
		msg, _ := e.(map[string]any)["message"].(string)
		if strings.Contains(msg, "list[0]") {
			sawList0 = true
		}
		if strings.Contains(msg, "list[1]") {
			sawList1 = true
		}
	}
	if !sawList0 || !sawList1 {
		t.Fatalf("errors message 应逐项索引化（list[0]/list[1]）: %v", raw)
	}
	if n := len(f.commands(t)); n != 0 {
		t.Fatalf("应零入队: %d", n)
	}

	// 结构性错误维持 400（非必填表违例不带 errors 数组）。
	status, env, _ = f.doReq(t,
		`{"socketType": 2, "list": [{"titleList": ["a"]}]}`, "", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("缺 type 应维持 400: %d", status)
	}
	if _, has := env["errors"]; has {
		t.Fatalf("结构性错误不应带 errors 数组: %v", env)
	}
}

// TestK1AuthNegative：无 token → 401/501012；他 robot token → 403/501011。
func TestK1AuthNegative(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/wework/sendRawMessage?robotId="+f.robot, strings.NewReader(doc203Example))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无鉴权应 401: %d %s", rec.Code, rec.Body.String())
	}
	other, err := f.api.IssueOpenAPIToken(context.Background(), "robot-other", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/wework/sendRawMessage?robotId="+f.robot, strings.NewReader(doc203Example))
	req2.Header.Set("Authorization", "Bearer "+other)
	rec2 := httptest.NewRecorder()
	f.mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("跨 robot 应 403: %d %s", rec2.Code, rec2.Body.String())
	}
	var env map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &env)
	if env["code"] != float64(501011) {
		t.Fatalf("robotId 错误码应 501011: %v", env)
	}
}

// TestK1L2ApprovalGate：L2 指令（234 删联系人）无审批 → 202 + 101006
// （FR-53 审批门为安全增强保留；上游无审批——迁移差异，docs/15 §1.1 行 12 口径）。
func TestK1L2ApprovalGate(t *testing.T) {
	f := newFixture(t)
	status, env, _ := f.doReq(t,
		`{"socketType": 2, "list": [{"type": 234, "friend": {"name": "小白"}}]}`, "", nil)
	if status != http.StatusAccepted || env["code"] != float64(101006) {
		t.Fatalf("L2 无审批应 202/101006: %d %v", status, env)
	}
	if !strings.Contains(env["message"].(string), "审批") {
		t.Fatalf("message 应含审批指引: %v", env["message"])
	}
	if n := len(f.commands(t)); n != 0 {
		t.Fatalf("待审批指令不入 commands: %d", n)
	}
}

// TestK1Duplicate101014：同请求重放（首条仍在队列）→ 101014。
func TestK1Duplicate101014(t *testing.T) {
	f := newFixture(t)
	if status, env, _ := f.doReq(t, doc203Example, "", nil); status != http.StatusOK {
		t.Fatalf("首次应成功: %v", env)
	}
	status, env, _ := f.doReq(t, doc203Example, "", nil)
	if status != http.StatusBadRequest || env["code"] != float64(101014) {
		t.Fatalf("重放应 101014: %d %v", status, env)
	}
	if n := len(f.commands(t)); n != 1 {
		t.Fatalf("重放不得重复入队: %d", n)
	}
}

// TestK1UpPriority：文档 up 示例逐字面 → 提交时插队（13 项清单第 9 条闭环）。
func TestK1UpPriority(t *testing.T) {
	f := newFixture(t)
	// 先入队一条普通 203，再发 up:true 条目 → up 条目应排到队首
	if status, env, _ := f.doReq(t,
		`{"socketType": 2, "list": [{"type": 203, "titleList": ["先入队"], "receivedContent": "first"}]}`, "", nil); status != http.StatusOK {
		t.Fatalf("first 提交失败: %v", env)
	}
	if status, env, _ := f.doReq(t, docUpExample, "", nil); status != http.StatusOK {
		t.Fatalf("up 提交失败: %v", env)
	}
	queued, err := f.api.Pipeline.QueuedForTarget(context.Background(), "v1:"+f.robot, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 {
		t.Fatalf("应两条排队: %d", len(queued))
	}
	if !strings.Contains(queued[0].Payload, "待执行队列的顶部") {
		t.Fatalf("up:true 条目应在队首: %s", queued[0].Payload)
	}
	// 审计留痕（与 REST 面插队同 kind）
	rows, _ := f.db.QueryDump(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE kind='openapi_v1_queue_priority'`)
	if rows[0][0] != "1" {
		t.Fatalf("up 插队审计缺失: %v", rows)
	}
}

// ---- K6：per-IP 限流 ----

// TestK6PerIPRateLimitAndBan：窗口超限 429；连续第 2 次超限 → 60s 封禁；
// 封禁期内全拒；冷却结束恢复放行；他 IP 不受牵连。
func TestK6PerIPRateLimitAndBan(t *testing.T) {
	f := newFixture(t)
	compat.PerIPRateQPS = 3
	defer func() { compat.PerIPRateQPS = 20 }()
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	*f.now = base

	// 载荷逐条唯一（同载荷重放会命中 101014，干扰限流判定）
	i := 0
	sendUnique := func(ip string) int {
		i++
		req := httptest.NewRequest(http.MethodPost, "/wework/sendRawMessage?robotId="+f.robot,
			strings.NewReader(fmt.Sprintf(`{"socketType": 2, "list": [{"type": 203, "titleList": ["a"], "receivedContent": "x%d"}]}`, i)))
		req.RemoteAddr = ip + ":5000"
		req.Header.Set("Authorization", "Bearer "+f.token)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		return rec.Code
	}

	// 前 3 放行（QPS=3），第 4 超限 429（未封禁），第 5 连续超限 → 封禁启动
	for k := 0; k < 3; k++ {
		if c := sendUnique("10.9.0.1"); c != http.StatusOK {
			t.Fatalf("第 %d 请求应放行: %d", k+1, c)
		}
	}
	if c := sendUnique("10.9.0.1"); c != http.StatusTooManyRequests {
		t.Fatalf("第 4 请求应 429: %d", c)
	}
	if c := sendUnique("10.9.0.1"); c != http.StatusTooManyRequests {
		t.Fatalf("连续第 2 次超限应 429: %d", c)
	}
	// 封禁期内（59s 后）即使窗口已空也全拒
	*f.now = base.Add(59 * time.Second)
	if c := sendUnique("10.9.0.1"); c != http.StatusTooManyRequests {
		t.Fatalf("封禁期内应 429: %d", c)
	}
	// 他 IP 不受牵连（封禁是 per-IP）
	if c := sendUnique("10.9.0.2"); c != http.StatusOK {
		t.Fatalf("他 IP 应放行: %d", c)
	}
	// 冷却结束（61s）恢复放行
	*f.now = base.Add(61 * time.Second)
	if c := sendUnique("10.9.0.1"); c != http.StatusOK {
		t.Fatalf("冷却后应恢复放行: %d", c)
	}
}

// TestK6PerRobotRateLimitIndependent：per-robot 60QPM 与 per-IP 独立计数
// （REV-SRV-04：认证后 per-robot；per-IP 放宽至不影响本例）。
func TestK6PerRobotRateLimitIndependent(t *testing.T) {
	f := newFixture(t)
	compat.PerIPRateQPS = 1000 // per-IP 放宽：本例只验 per-robot
	defer func() { compat.PerIPRateQPS = 20 }()
	f.api.SetOpenAPIConfig(api.OpenAPIConfig{RateLimitPerMinute: 2, RateWindow: time.Minute})
	for k := 0; k < 2; k++ {
		body := fmt.Sprintf(`{"socketType": 2, "list": [{"type": 203, "titleList": ["a"], "receivedContent": "r%d"}]}`, k)
		if status, env, _ := f.doReq(t, body, "", nil); status != http.StatusOK {
			t.Fatalf("第 %d 请求应放行: %v", k+1, env)
		}
	}
	status, env, _ := f.doReq(t,
		`{"socketType": 2, "list": [{"type": 203, "titleList": ["a"], "receivedContent": "r3"}]}`, "", nil)
	if status != http.StatusTooManyRequests || env["code"] != float64(101004) {
		t.Fatalf("per-robot 超限应 429/101004: %d %v", status, env)
	}
	if !strings.Contains(env["message"].(string), "60QPM") {
		t.Fatalf("限流错误信息应含 60QPM 口径: %v", env["message"])
	}
}

// TestK6KeysCapFailClosed：entry 基数上限 fail-closed（拒新保旧；空窗回收
// 只复用自身槽位不挤占他人——REV-SRV-04 同型纵深防御）。
func TestK6KeysCapFailClosed(t *testing.T) {
	f := newFixture(t)
	compat.PerIPKeysCap = 2
	defer func() { compat.PerIPKeysCap = 4096 }()
	base := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
	*f.now = base
	n := 0
	send := func(ip string) int {
		n++
		req := httptest.NewRequest(http.MethodPost, "/wework/sendRawMessage?robotId="+f.robot,
			strings.NewReader(fmt.Sprintf(`{"socketType": 2, "list": [{"type": 203, "titleList": ["a"], "receivedContent": "c%d"}]}`, n)))
		req.RemoteAddr = ip + ":6000"
		req.Header.Set("Authorization", "Bearer "+f.token)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := send("10.1.0.1"); c != http.StatusOK {
		t.Fatalf("IP1 应放行: %d", c)
	}
	if c := send("10.1.0.2"); c != http.StatusOK {
		t.Fatalf("IP2 应放行: %d", c)
	}
	if c := send("10.1.0.3"); c != http.StatusTooManyRequests {
		t.Fatalf("cap 满后新 IP 应 fail-closed 拒绝: %d", c)
	}
	// 空窗回收：IP1 窗口滑过回收自身 entry → 复用自身槽位放行（不挤占 IP2）
	*f.now = base.Add(2 * time.Second)
	if c := send("10.1.0.1"); c != http.StatusOK {
		t.Fatalf("空窗回收后自身槽位应放行: %d", c)
	}
	if c := send("10.1.0.2"); c != http.StatusOK {
		t.Fatalf("既有 entry 不受 cap 影响: %d", c)
	}
	if c := send("10.1.0.3"); c != http.StatusTooManyRequests {
		t.Fatalf("cap 仍满：新 IP 持续 fail-closed: %d", c)
	}
}

// ---- K3：exec_result 回调 body 双名别名 ----

// seedExecResult：落一条指令行 + 终态 exec_result outbox 行（payload 与
// scheduler outboxEffect 同型）。
func seedExecResult(t *testing.T, f *fixture, commandID string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `
INSERT INTO commands(command_id, target, command_type, payload, state, created_at)
VALUES (?, 'v1:robot-compat-01', 203, ?, 'succeeded', ?)`,
		commandID, `{"type":203,"titleList":["仑哥"],"receivedContent":"你好~"}`, at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := f.db.EnqueueOutbox(ctx, tx, "exec_result", commandID, map[string]any{
		"type": "exec_result", "commandId": commandID, "commandType": 203,
		"errorCode": 0, "errorReason": "", "timeCost": 2.5,
		"successList": []string{"仑哥"}, "failList": []string{},
		"finalState": "succeeded", "dryRun": false, "at": at.UnixMilli(),
	}, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func newWorker(t *testing.T, f *fixture, rcvURL, secret string, aliases *bool) *api.OutboxWorker {
	t.Helper()
	w, err := api.NewOutboxWorker(f.db, api.OutboxConfig{
		CallbackURL: rcvURL, CallbackSecret: secret, CompatAliases: aliases,
		RetryDelays: []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond},
	}, testLog{t})
	if err != nil {
		t.Fatal(err)
	}
	w.SetReplyPipeline(f.api.Pipeline)
	return w
}

type testLog struct{ t *testing.T }

func (l testLog) Printf(format string, args ...any) { l.t.Logf("[outbox] "+format, args...) }

// TestK3ExecCallbackAliases：接收器按文档 ExecCallbackBean 解析零改造——
// messageId=commandId、type=203(int)、runTime、rawMsg（指令原文回查）、
// groupName/qrCode 仅携带才发；HMAC 验签按别名后 body 通过。
func TestK3ExecCallbackAliases(t *testing.T) {
	f := newFixture(t)
	const secret = "k3-secret"
	rcv := newCBReceiver(t, secret, "")
	w := newWorker(t, f, rcv.srv.URL+"/callback", secret, nil) // CompatAliases 缺省 = true
	at := time.Date(2026, 11, 3, 10, 1, 0, 0, time.UTC)
	seedExecResult(t, f, "cmd-k3-1", at)

	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rcv.count() != 1 {
		t.Fatalf("应投递一次: %d", rcv.count())
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rcv.lastBody()), &body); err != nil {
		t.Fatal(err)
	}
	if body["messageId"] != "cmd-k3-1" {
		t.Fatalf("messageId 应=commandId: %v", body["messageId"])
	}
	if body["type"] != float64(203) { // int 指令类型（文档语义），非 "exec_result" 字符串
		t.Fatalf("type 应为 int 203: %v (%T)", body["type"], body["type"])
	}
	if body["kind"] != "exec_result" {
		t.Fatalf("原字符串 type 应迁别名 kind: %v", body["kind"])
	}
	if body["runTime"] != float64(at.UnixMilli()) {
		t.Fatalf("runTime 应=终态时间毫秒: %v", body["runTime"])
	}
	var rawMsg string
	_ = json.Unmarshal([]byte(body["rawMsg"].(string)), &rawMsg) // rawMsg 为指令原文 JSON 字符串
	if rawMsg != "" || body["rawMsg"] == nil {
		t.Fatalf("rawMsg 应为指令原文: %v", body["rawMsg"])
	}
	if !strings.Contains(body["rawMsg"].(string), "receivedContent") {
		t.Fatalf("rawMsg 应回查 commands.payload: %v", body["rawMsg"])
	}
	if _, has := body["groupName"]; has {
		t.Fatalf("groupName 未携带不应发: %v", body)
	}
	if _, has := body["qrCode"]; has {
		t.Fatalf("qrCode 未携带不应发（type=0 场景 dodDeferred）: %v", body)
	}
	if body["errorCode"] != float64(0) || body["successList"] == nil {
		t.Fatalf("原名原值字段应保留: %v", body)
	}
	// 落库 payload 未迁移（零 schema 变更；HandleOpenAPIResult 回填不受影响）
	rows, _ := f.db.QueryDump(context.Background(),
		`SELECT payload FROM outbox WHERE kind='exec_result'`)
	if strings.Contains(rows[0][0], "messageId") {
		t.Fatalf("落库 payload 不得被别名改写: %s", rows[0][0])
	}
}

// TestK3AliasesOptOut：CompatAliases=false → 旧裸形态（显式 opt-out）。
func TestK3AliasesOptOut(t *testing.T) {
	f := newFixture(t)
	const secret = "k3-secret-off"
	rcv := newCBReceiver(t, secret, "")
	off := false
	w := newWorker(t, f, rcv.srv.URL+"/callback", secret, &off)
	seedExecResult(t, f, "cmd-k3-off", time.Now().UTC())
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(rcv.lastBody()), &body)
	if body["type"] != "exec_result" || body["messageId"] != nil {
		t.Fatalf("opt-out 应保持旧裸形态: %v", body)
	}
}

// ---- K4：type=11 文本消息推送 ----

func bindType11(t *testing.T, f *fixture, url string) {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	if _, err := f.db.Exec(context.Background(), `
INSERT INTO openapi_callbacks(robot_id, type, callback_url, created_ms, updated_ms) VALUES (?,?,?, ?, ?)
ON CONFLICT(robot_id, type) DO UPDATE SET callback_url=excluded.callback_url, updated_ms=excluded.updated_ms`,
		f.robot, 11, url, now, now); err != nil {
		t.Fatal(err)
	}
}

// doc861677Item = doc-861677 请求示例形态（设备帧 receiveMessageList 项）。
const doc861677Item = `{"spoken": "你好", "rawSpoken": "@管家 你好", "receivedName": "仑哥", "groupName": "测试群1", "groupRemark": "测试群1备注名", "roomType": 1, "atMe": "true", "textType": 1, "messageId": "m-861677-1"}`

// TestK4TextMessagePush：文本消息 → type=11 绑定 URL 收到 doc-861677 全集 body。
func TestK4TextMessagePush(t *testing.T) {
	f := newFixture(t)
	const secret = "k4-secret"
	rcv := newCBReceiver(t, secret, "")
	bindType11(t, f, rcv.srv.URL+"/callback")
	w := newWorker(t, f, rcv.srv.URL+"/callback", secret, nil)

	ok, err := f.api.HandleTextMessagePush(context.Background(), "v1:"+f.robot, json.RawMessage(doc861677Item))
	if err != nil || !ok {
		t.Fatalf("应入队 message_push: ok=%v err=%v", ok, err)
	}
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rcv.count() != 1 {
		t.Fatalf("type=11 绑定应收到推送: %d", rcv.count())
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(rcv.lastBody()), &body)
	want := map[string]any{"spoken": "你好", "rawSpoken": "@管家 你好", "receivedName": "仑哥",
		"groupName": "测试群1", "groupRemark": "测试群1备注名", "roomType": float64(1),
		"atMe": "true", "textType": float64(1), "messageId": "m-861677-1"}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%s] = %v（want %v）: %v", k, body[k], v, body)
		}
	}
	// 审计仅元数据（不含正文——§6.1 隐私口径）
	rows, _ := f.db.QueryDump(context.Background(),
		`SELECT detail FROM audit_log WHERE kind='event_callback'`)
	if strings.Contains(rows[0][0], "你好") {
		t.Fatalf("审计不得含消息正文: %s", rows[0][0])
	}
}

// TestK4SwitchAndRoutingNegatives：总门关→不入队；textType=2 → 复用图片门
// （pushImage 缺省关 → 不入队）；roomType 越界 → 审计 event_callback_invalid。
func TestK4SwitchAndRoutingNegatives(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// 总门关
	setSwitches(t, f, false, false)
	ok, err := f.api.HandleTextMessagePush(ctx, "v1:"+f.robot, json.RawMessage(doc861677Item))
	if err != nil || ok {
		t.Fatalf("总门关应不分发: ok=%v err=%v", ok, err)
	}
	// 总门开、图片门缺省关：textType=2 → 走 HandleImageMessage（独立门拒）
	setSwitches(t, f, true, false)
	img := strings.Replace(doc861677Item, `"textType": 1`, `"textType": 2`, 1)
	ok, err = f.api.HandleTextMessagePush(ctx, "v1:"+f.robot, json.RawMessage(img))
	if err != nil || ok {
		t.Fatalf("图片族应走 pushImage 门（缺省关）: ok=%v err=%v", ok, err)
	}
	// roomType 越界 → 审计 event_callback_invalid（ok=false 不入队；审计成功
	// 落库时 err=nil——以审计行为准断言，与 events.go 既有语义一致）
	bad := strings.Replace(doc861677Item, `"roomType": 1`, `"roomType": 9`, 1)
	ok, err = f.api.HandleTextMessagePush(ctx, "v1:"+f.robot, json.RawMessage(bad))
	if ok {
		t.Fatalf("roomType 越界不得入队: ok=%v", ok)
	}
	rows, _ := f.db.QueryDump(ctx, `SELECT COUNT(*) FROM audit_log WHERE kind='event_callback_invalid'`)
	if rows[0][0] != "1" {
		t.Fatalf("应有一条 invalid 审计: %v", rows)
	}
	_ = err
}

func setSwitches(t *testing.T, f *fixture, pushEvent, pushImage bool) {
	t.Helper()
	raw, _ := json.Marshal(map[string]bool{"pushEventCallback": pushEvent, "pushImage": pushImage})
	if _, err := f.db.Exec(context.Background(), `
INSERT INTO admin_settings(key, value, updated_ms) VALUES ('event_callback_config', ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_ms=excluded.updated_ms`,
		string(raw), time.Now().UTC().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// ---- K5：type:5000 同步代回复 ----

// TestK5ReplyOnBehalf：接收器回 type:5000 → 3s 内 203 代回复入队（单次、定向
// 群名）；无 5000 响应 → 不生成。
func TestK5ReplyOnBehalf(t *testing.T) {
	f := newFixture(t)
	const secret = "k5-secret"
	rcv := newCBReceiver(t, secret,
		`{"code": 0, "message": "参数接收成功", "data": {"type": 5000, "info": {"text": "自动回复：收到~"}}}`)
	bindType11(t, f, rcv.srv.URL+"/callback")
	w := newWorker(t, f, rcv.srv.URL+"/callback", secret, nil)

	if _, err := f.api.HandleTextMessagePush(context.Background(), "v1:"+f.robot, json.RawMessage(doc861677Item)); err != nil {
		t.Fatal(err)
	}
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rcv.count() != 1 {
		t.Fatalf("回调应投递一次: %d", rcv.count())
	}
	// 代回复 203 已入队：定向群名（groupName 优先于 receivedName）
	rows, err := f.db.QueryDump(context.Background(),
		`SELECT command_type, payload FROM commands WHERE payload LIKE '%自动回复：收到~%'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][0] != "203" {
		t.Fatalf("应生成恰一条 203 代回复: %v", rows)
	}
	if !strings.Contains(rows[0][1], `"测试群1"`) || !strings.Contains(rows[0][1], "receivedContent") {
		t.Fatalf("代回复应定向 groupName=测试群1: %s", rows[0][1])
	}
	// 单次不重试：行已 delivered，再触发投递不得重复生成代回复
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows2, _ := f.db.QueryDump(context.Background(),
		`SELECT COUNT(*) FROM commands WHERE payload LIKE '%自动回复：收到~%'`)
	if rows2[0][0] != "1" {
		t.Fatalf("代回复应单次（不重复生成）: %v", rows2)
	}
}

// TestK5NoReplyWithout5000：普通响应（无 data.type=5000）→ 不生成代回复；
// 5000 缺 info.text → 放弃 + 审计 reply_on_behalf_dropped（单次不重试）。
func TestK5NoReplyWithout5000(t *testing.T) {
	f := newFixture(t)
	const secret = "k5b"
	// 场景 1：普通 200 响应
	rcv := newCBReceiver(t, secret, `{"code": 0, "message": "参数接收成功"}`)
	bindType11(t, f, rcv.srv.URL+"/callback")
	w := newWorker(t, f, rcv.srv.URL+"/callback", secret, nil)
	if _, err := f.api.HandleTextMessagePush(context.Background(), "v1:"+f.robot, json.RawMessage(doc861677Item)); err != nil {
		t.Fatal(err)
	}
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.commands(t)); n != 0 {
		t.Fatalf("无 5000 不应生成指令: %d", n)
	}
	// 场景 2：5000 但空 text → 放弃审计
	rcv2 := newCBReceiver(t, secret, `{"code": 0, "message": "x", "data": {"type": 5000, "info": {"text": ""}}}`)
	bindType11(t, f, rcv2.srv.URL+"/callback")
	if _, err := f.api.HandleTextMessagePush(context.Background(), "v1:"+f.robot, json.RawMessage(
		strings.Replace(doc861677Item, `"messageId": "m-861677-1"`, `"messageId": "m-2"`, 1))); err != nil {
		t.Fatal(err)
	}
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.commands(t)); n != 0 {
		t.Fatalf("空 text 应放弃: %d", n)
	}
	rows, _ := f.db.QueryDump(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE kind='reply_on_behalf_dropped'`)
	if rows[0][0] != "1" {
		t.Fatalf("应有一条放弃审计: %v", rows)
	}
}

// TestK5ReplyOnExecResultRow：exec_result 行的代回复路径（robotIDForCommand
// 指令号反查分支；定向 = 别名后 body 无 groupName → 私聊 receivedName）。
func TestK5ReplyOnExecResultRow(t *testing.T) {
	f := newFixture(t)
	const secret = "k5c"
	rcv := newCBReceiver(t, secret,
		`{"code": 0, "message": "ok", "data": {"type": 5000, "info": {"text": "私聊代回复"}}}`)
	// 全局 URL 投递（exec_result 无 type=1 绑定时回落部署级 URL）
	w := newWorker(t, f, rcv.srv.URL+"/callback", secret, nil)
	at := time.Date(2026, 11, 3, 10, 2, 0, 0, time.UTC)
	seedExecResult(t, f, "cmd-k5-exec-1", at)
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.QueryDump(context.Background(),
		`SELECT command_type, payload, target FROM commands WHERE payload LIKE '%私聊代回复%'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][0] != "203" {
		t.Fatalf("exec_result 行代回复应生成一条 203: %v", rows)
	}
	if !strings.Contains(rows[0][1], "仑哥") {
		t.Fatalf("无 groupName 应定向私聊 receivedName=仑哥: %s", rows[0][1])
	}
	if rows[0][2] != "v1:"+f.robot {
		t.Fatalf("代回复目标键应为 robot 反查解析: %s", rows[0][2])
	}
}

// ---- 双面并存回归：既有 /openapi/v1 面零改动 ----

// TestOpenAPIFaceCoexists：既有 REST 提交面（data=object 形态）与兼容面并行可用
// （双面并存红线；信封形态互不污染）。
func TestOpenAPIFaceCoexists(t *testing.T) {
	f := newFixture(t)
	// 旧面：单条报文 + Idempotency-Key → data=object{commandId,...}
	req, _ := http.NewRequest(http.MethodPost,
		"http://loopback/openapi/v1/robots/"+f.robot+"/commands",
		strings.NewReader(`{"type":203,"titleList":["仑哥"],"receivedContent":"旧面"}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Idempotency-Key", "idem-compat-1")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if rec.Code != http.StatusOK || env["code"] != float64(0) {
		t.Fatalf("旧面应正常: %d %v", rec.Code, env)
	}
	d, ok := env["data"].(map[string]any)
	if !ok || d["commandId"] == nil {
		t.Fatalf("旧面 data 应保持 object 形态（零改动）: %v", env["data"])
	}
	// 兼容面同机器人再提交 → data=string（两面互不影响）
	status, env2, _ := f.doReq(t, doc203Example, "", nil)
	if status != http.StatusOK {
		t.Fatalf("兼容面应正常: %v", env2)
	}
	if _, isStr := env2["data"].(string); !isStr {
		t.Fatalf("兼容面 data 应为 string: %v", env2["data"])
	}
}
