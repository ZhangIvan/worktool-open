package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/wire"
)

// ---- 测试辅助 ----

// req 对新扩展面端点发请求（带/不带凭据由 opts 控制）。
func (f *openapiFixture) rest(t *testing.T, method, path, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/"+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Idempotency-Key", fmt.Sprintf("idem-rest-%d", time.Now().UnixNano()))
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

// restNoAuth 不带 Authorization 的请求（鉴权负例）。
func (f *openapiFixture) restNoAuth(t *testing.T, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/"+path,
		bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "idem-noauth")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func codeOf(t *testing.T, env map[string]any) int {
	t.Helper()
	if env == nil {
		t.Fatalf("响应信封为空")
	}
	c, ok := env["code"].(float64)
	if !ok {
		t.Fatalf("响应缺 code: %v", env)
	}
	return int(c)
}

func listOf(t *testing.T, env map[string]any) []any {
	t.Helper()
	d := dataMap(t, env)
	l, ok := d["list"].([]any)
	if !ok {
		t.Fatalf("响应 data 缺 list: %v", d)
	}
	return l
}

// ---- ② 注册表扩容 ----

// TestOpenAPIRegistryFullSurface 注册表全量断言：46 条（39 + 本批 7 条
// 212/236/237/238/239/240/244），命名空间 intent 正确，无重复 type。
func TestOpenAPIRegistryFullSurface(t *testing.T) {
	if len(openapiCommandRegistry) != 46 {
		t.Fatalf("注册表应 46 条，实际 %d", len(openapiCommandRegistry))
	}
	seen := map[int]bool{}
	for _, it := range openapiCommandRegistry {
		if seen[it.Type] {
			t.Fatalf("注册表 type=%d 重复", it.Type)
		}
		seen[it.Type] = true
	}
	want := map[int]string{
		212: "openapi.v1.command.pass_all_friend_request",
		236: "openapi.v1.command.delete_single_directional_contact",
		237: "openapi.v1.command.wework_clean_cache",
		238: "openapi.v1.command.send_batch_text_message",
		239: "openapi.v1.command.create_customer_group_live_qrcode",
		240: "openapi.v1.command.add_group_board_webpage",
		244: "openapi.v1.command.exit_group",
	}
	for typ, intent := range want {
		if got := OpenAPIIntent(typ); got != intent {
			t.Fatalf("type=%d intent 应 %s，实际 %s", typ, intent, got)
		}
	}
	// 上报/心跳与未定义类型仍拒绝（WTO-004 纪律不随扩容放松）
	for _, typ := range []int{11, 101, 102, 301, 302, 215, 216, 217, 231, 232} {
		if got := OpenAPIIntent(typ); got != "" {
			t.Fatalf("type=%d 不得注册（上报/心跳/上游未定义），实际 %s", typ, got)
		}
	}
}

// TestOpenAPIRegistryAllRoutesSmoke 全量路由冒烟：注册表内每个 type 逐一提交，
// 全部被受理（200 入队或 202 待审批），无一 101002 UNKNOWN_COMMAND。
// L2 集 = 分级表 {219,234,304} ∪ 未登记分级表 fail-closed {236,237,238,239,240,244}。
func TestOpenAPIRegistryAllRoutesSmoke(t *testing.T) {
	f := newOpenAPIFixture(t)
	l2 := map[int]bool{219: true, 234: true, 304: true, 236: true, 237: true, 238: true, 239: true, 240: true, 244: true}
	i := 0
	for _, it := range openapiCommandRegistry {
		i++
		body := fmt.Sprintf(`{"type":%d,"targetRoom":"冒烟群-%d","receivedContent":"route-smoke-%d"}`, it.Type, it.Type, i)
		status, env := f.post(t, body, nil)
		code := codeOf(t, env)
		if l2[it.Type] {
			if status != http.StatusAccepted || code != openapiCodeApprovalRequired {
				t.Fatalf("type=%d 应 202/%d（L2 审批门），实际 %d/%d env=%v", it.Type, openapiCodeApprovalRequired, status, code, env)
			}
		} else if status != http.StatusOK || code != openapiCodeSuccess {
			t.Fatalf("type=%d 应 200/0（入队），实际 %d/%d env=%v", it.Type, status, code, env)
		}
		if code == openapiCodeUnknownCommand {
			t.Fatalf("type=%d 意外 101002", it.Type)
		}
	}
}

// ---- ④ 101014 入队去重 ----

// TestOpenAPIQueueDuplicateRejected 同目标同 type 同载荷仍排队中 → 101014；
// 不同载荷/同键重放不受影响。
func TestOpenAPIQueueDuplicateRejected(t *testing.T) {
	f := newOpenAPIFixture(t)
	body := `{"type":203,"targetRoom":"去重群","receivedContent":"同内容"}`
	if status, env := f.post(t, body, nil); status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("首条应入队: %d %v", status, env)
	}
	status, env := f.post(t, body, nil) // 不同幂等键（post 自动生成）同载荷
	if status != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamRepeatInstruct {
		t.Fatalf("重复载荷应 400/101014，实际 %d %v", status, env)
	}
	if d := dataMap(t, env); d["queuedCommandId"] == nil || d["queuedCommandId"] == "" {
		t.Fatalf("101014 应携带 queuedCommandId: %v", d)
	}
	// 不同载荷不受影响
	if status, env := f.post(t, `{"type":203,"targetRoom":"去重群","receivedContent":"不同内容"}`, nil); status != http.StatusOK {
		t.Fatalf("不同载荷应入队: %d %v", status, env)
	}
	// 同键重放不受去重拦截（回归保护；用独立载荷，避免与首条排队指令撞 101014）
	replayBody := `{"type":203,"targetRoom":"去重群","receivedContent":"重放专用"}`
	s1, e1 := f.postWithToken(t, replayBody, "Bearer "+f.token, "dup-replay-key")
	s2, e2 := f.postWithToken(t, replayBody, "Bearer "+f.token, "dup-replay-key")
	if s1 != http.StatusOK || s2 != http.StatusOK {
		t.Fatalf("同键重放应均 200: %d %d", s1, s2)
	}
	if dataMap(t, e1)["commandId"] != dataMap(t, e2)["commandId"] || dataMap(t, e2)["replay"] != true {
		t.Fatalf("同键重放应返回原 commandId 且 replay=true: %v vs %v", e1, e2)
	}
}

// ---- b) 机器人配置组 ----

// TestOpenAPIRobotConfigGroup 配置组正例：info/online/logs/corps。
func TestOpenAPIRobotConfigGroup(t *testing.T) {
	f := newOpenAPIFixture(t)

	status, env := f.rest(t, http.MethodGet, "info", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("info: %d %v", status, env)
	}
	d := dataMap(t, env)
	if d["robotId"] != f.robot || d["paired"] != false {
		t.Fatalf("info 字段异常: %v", d)
	}

	status, env = f.rest(t, http.MethodGet, "online", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("online: %d %v", status, env)
	}
	d = dataMap(t, env)
	if d["online"] != false || d["lastSession"] != nil {
		t.Fatalf("未配对应 online=false 且无会话: %v", d)
	}

	status, env = f.rest(t, http.MethodGet, "logs", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("logs: %d %v", status, env)
	}
	d = dataMap(t, env)
	if total, _ := d["total"].(float64); total != 0 {
		t.Fatalf("未配对 logs 应空: %v", d)
	}

	status, env = f.rest(t, http.MethodGet, "corps", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("corps: %d %v", status, env)
	}
	d = dataMap(t, env)
	if d["snapshot"] != false {
		t.Fatalf("无 506 快照应 snapshot=false（不伪造）: %v", d)
	}
}

// TestOpenAPISnapshotFromExecResult corps/groups 快照正例：506/508 真实回执
// successList 成为快照数据源。
func TestOpenAPISnapshotFromExecResult(t *testing.T) {
	f := newOpenAPIFixture(t)
	ctx := context.Background()

	// 提交 506（非干跑）→ 模拟设备成功回执（successList=企业清单）
	status, env := f.post(t, `{"type":506,"receivedContent":"查询企业列表"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("506 提交失败: %d %v", status, env)
	}
	commandID := dataMap(t, env)["commandId"].(string)
	// 走完整链：dispatch → ack → exec_start → result（HandleResult 蕴含前序证据边）
	if err := f.api.Pipeline.MarkDispatched(ctx, commandID, 1, 1, "evt-506"); err != nil {
		t.Fatal(err)
	}
	if err := f.api.Pipeline.HandleTransportAck(ctx, commandID, "ack"); err != nil {
		t.Fatal(err)
	}
	if err := f.api.Pipeline.HandleExecStart(ctx, commandID, "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.Pipeline.HandleResult(ctx, commandID, &wire.ExecResult{
		Type: 506, ErrorCode: 0, SuccessList: []string{"企业A", "企业B"},
	}); err != nil {
		t.Fatal(err)
	}

	status, env = f.rest(t, http.MethodGet, "corps", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("corps: %d %v", status, env)
	}
	d := dataMap(t, env)
	if d["snapshot"] != true {
		t.Fatalf("有 506 回执应 snapshot=true: %v", d)
	}
	corps, _ := d["corps"].([]any)
	if len(corps) != 2 || corps[0] != "企业A" {
		t.Fatalf("corps 快照内容异常: %v", d)
	}
	// 508 无回执 → groups 仍 snapshot=false（互不串扰）
	_, env = f.rest(t, http.MethodGet, "groups", "", nil)
	if dataMap(t, env)["snapshot"] != false {
		t.Fatalf("groups 无 508 回执应 snapshot=false: %v", env)
	}
}

// ---- c) 回调配置组 ----

// TestOpenAPICallbacksCRUD 回调 CRUD 全流程：bind 校验（200+JSON 硬性）→
// 列表 → PUT 更新 → 404 语义 → 非法 type 101011 → 校验失败 101012 →
// loopback 纪律 → DELETE 剩余列表 → 再删 404。
func TestOpenAPICallbacksCRUD(t *testing.T) {
	f := newOpenAPIFixture(t)

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(ok.Close)
	badCT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(badCT.Close)

	// bind type=1 → 成功，列表含 1 条
	status, env := f.rest(t, http.MethodPost, "callbacks",
		`{"type":1,"callBackUrl":"`+ok.URL+`/exec"}`, nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("bind: %d %v", status, env)
	}
	list := listOf(t, env)
	if len(list) != 1 {
		t.Fatalf("bind 后应 1 条: %v", list)
	}
	row := list[0].(map[string]any)
	if row["type"].(float64) != 1 || row["callbackUrl"] != ok.URL+"/exec" || row["typeName"] != "指令结果回调" {
		t.Fatalf("绑定行异常: %v", row)
	}

	// GET 列表
	status, env = f.rest(t, http.MethodGet, "callbacks", "", nil)
	if status != http.StatusOK || len(listOf(t, env)) != 1 {
		t.Fatalf("GET callbacks: %d %v", status, env)
	}

	// PUT 更新 URL
	status, env = f.rest(t, http.MethodPut, "callbacks",
		`{"type":1,"callBackUrl":"`+ok.URL+`/exec2"}`, nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("PUT update: %d %v", status, env)
	}
	if got := listOf(t, env)[0].(map[string]any)["callbackUrl"]; got != ok.URL+"/exec2" {
		t.Fatalf("PUT 后 URL 应更新: %v", got)
	}

	// PUT 未登记 type → 404/101010
	status, env = f.rest(t, http.MethodPut, "callbacks", `{"type":11,"callBackUrl":"`+ok.URL+`/msg"}`, nil)
	if status != http.StatusNotFound || codeOf(t, env) != openapiCodeNotFound {
		t.Fatalf("PUT 未登记应 404/101010: %d %v", status, env)
	}

	// POST 非法 type → 400/101011
	status, env = f.rest(t, http.MethodPost, "callbacks", `{"type":99,"callBackUrl":"`+ok.URL+`/x"}`, nil)
	if status != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamIllegalData {
		t.Fatalf("非法 type 应 400/101011: %d %v", status, env)
	}

	// POST 目标非 JSON → 400/101012（绑定校验硬性）
	status, env = f.rest(t, http.MethodPost, "callbacks",
		`{"type":5,"callBackUrl":"`+badCT.URL+`/cb"}`, nil)
	if status != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamIllegalOp {
		t.Fatalf("非 JSON 目标应 400/101012: %d %v", status, env)
	}

	// 非法 URL → 101011
	status, env = f.rest(t, http.MethodPost, "callbacks", `{"type":1,"callBackUrl":"ftp://x"}`, nil)
	if status != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamIllegalData {
		t.Fatalf("非法 URL 应 400/101011: %d %v", status, env)
	}

	// 外网（非 loopback）URL → 403（默认禁外网，SR-09）
	status, env = f.rest(t, http.MethodPost, "callbacks",
		`{"type":1,"callBackUrl":"https://cb.example.com/exec"}`, nil)
	if status != http.StatusForbidden || codeOf(t, env) != openapiCodeNotWhitelisted {
		t.Fatalf("外网回调应 403: %d %v", status, env)
	}

	// DELETE（query type）→ 剩余列表（空）
	status, env = f.rest(t, http.MethodDelete, "callbacks?type=1", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("DELETE: %d %v", status, env)
	}
	if l := listOf(t, env); len(l) != 0 {
		t.Fatalf("删除后剩余列表应空: %v", l)
	}
	// 再删 → 404
	status, env = f.rest(t, http.MethodDelete, "callbacks?type=1", "", nil)
	if status != http.StatusNotFound || codeOf(t, env) != openapiCodeNotFound {
		t.Fatalf("重复 DELETE 应 404: %d %v", status, env)
	}
	// DELETE body 形态 + type=0（合法枚举边界）
	status, env = f.rest(t, http.MethodPost, "callbacks", `{"type":0,"callBackUrl":"`+ok.URL+`/qr"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("bind type=0 应成功: %d %v", status, env)
	}
	status, env = f.rest(t, http.MethodDelete, "callbacks", `{"type":0}`, nil)
	if status != http.StatusOK || len(listOf(t, env)) != 0 {
		t.Fatalf("DELETE body {type:0} 应删除 type=0 绑定: %d %v", status, env)
	}
}

// TestOpenAPICallbacksWriteRequiresIdemKey 回调写操作缺 Idempotency-Key → 400/101003。
func TestOpenAPICallbacksWriteRequiresIdemKey(t *testing.T) {
	f := newOpenAPIFixture(t)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/callbacks",
		strings.NewReader(`{"type":1,"callBackUrl":"http://127.0.0.1:9/x"}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, _ := http.DefaultClient.Do(req)
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || codeOf(t, env) != openapiCodeIdemKeyRequired {
		t.Fatalf("缺幂等键应 400/101003: %d %v", resp.StatusCode, env)
	}
}

// TestOpenAPICallbackDeliveryPerRobot 回调投递按 robot 绑定路由：全局未配置
// CallbackURL，仅 type=1 绑定存在 → outbox 投递到该 URL 且带 HMAC 签名头。
func TestOpenAPICallbackDeliveryPerRobot(t *testing.T) {
	f := newOpenAPIFixture(t)

	received := make(chan map[string]any, 4)
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body) // GET 探测空 body → 保持空 map
		body["_sig"] = r.Header.Get("X-WT-Signature")
		body["_ct"] = r.Header.Get("Content-Type")
		body["_method"] = r.Method
		received <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(rcv.Close)

	// bind type=1（探测走 GET，投递走 POST）
	if status, env := f.rest(t, http.MethodPost, "callbacks",
		`{"type":1,"callBackUrl":"`+rcv.URL+`/exec"}`, nil); status != http.StatusOK {
		t.Fatalf("bind 失败: %d %v", status, env)
	}
	// 干跑提交 → 终态 exec_result outbox 行
	if status, env := f.post(t, `{"type":203,"targetRoom":"回调群","receivedContent":"投递"}`,
		map[string]string{"X-WT-Dry-Run": "true"}); status != http.StatusOK {
		t.Fatalf("干跑提交失败: %d %v", status, env)
	}
	// outbox worker：全局 URL 空 + per-robot 绑定 → 投递到绑定 URL
	worker, err := NewOutboxWorker(f.db, OutboxConfig{CallbackSecret: "sec-per-robot"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 通道里首条是绑定探测 GET，取首条 POST 投递
	var body map[string]any
	deadline := time.After(2 * time.Second)
	for {
		select {
		case body = <-received:
			if body["_method"] == http.MethodPost {
				goto delivered
			}
		case <-deadline:
			t.Fatal("per-robot 回调未投递")
		}
	}
delivered:
	if body["type"] != "exec_result" || body["errorCode"].(float64) != 0 {
		t.Fatalf("回执载荷异常: %v", body)
	}
	if body["_sig"] == "" || body["_ct"] != "application/json" {
		t.Fatalf("投递应带签名头: %v", body)
	}
}

// ---- d) 历史消息组 ----

// TestOpenAPIHistoryGroup 历史组正例：commands / results / callbacks / messages。
func TestOpenAPIHistoryGroup(t *testing.T) {
	f := newOpenAPIFixture(t)

	// 两条 203：一条干跑（终态+exec_result）、一条非干跑（留队）
	_, env := f.post(t, `{"type":203,"targetRoom":"历史群","receivedContent":"干跑记录"}`,
		map[string]string{"X-WT-Dry-Run": "true"})
	dryID := dataMap(t, env)["commandId"].(string)
	_, env = f.post(t, `{"type":203,"targetRoom":"历史群","receivedContent":"排队记录"}`, nil)
	queuedID := dataMap(t, env)["commandId"].(string)

	// history/commands：total=2；type 过滤；messageId 精确；status 过滤
	status, env := f.rest(t, http.MethodGet, "history/commands", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("history/commands: %d %v", status, env)
	}
	d := dataMap(t, env)
	if d["total"].(float64) != 2 {
		t.Fatalf("total 应 2: %v", d)
	}
	status, env = f.rest(t, http.MethodGet, "history/commands?status=queued", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status 过滤: %d", status)
	}
	if l := listOf(t, env); len(l) != 1 || l[0].(map[string]any)["commandId"] != queuedID {
		t.Fatalf("status=queued 应只剩排队指令: %v", l)
	}
	status, env = f.rest(t, http.MethodGet, "history/commands?messageId="+dryID, "", nil)
	if status != http.StatusOK || dataMap(t, env)["total"].(float64) != 1 {
		t.Fatalf("messageId 精确过滤: %v", env)
	}
	// 非法 type → 101011
	status, env = f.rest(t, http.MethodGet, "history/commands?type=abc", "", nil)
	if status != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamIllegalData {
		t.Fatalf("非法 type 应 101011: %d %v", status, env)
	}

	// history/results：仅干跑指令有 exec_result（rawMsg/rawSuccess/时间）
	status, env = f.rest(t, http.MethodGet, "history/results", "", nil)
	if status != http.StatusOK {
		t.Fatalf("history/results: %d %v", status, env)
	}
	d = dataMap(t, env)
	if d["total"].(float64) != 1 {
		t.Fatalf("results total 应 1: %v", d)
	}
	item := listOf(t, env)[0].(map[string]any)
	if item["messageId"] != dryID || item["rawSuccess"].(float64) != 0 {
		t.Fatalf("results 行异常: %v", item)
	}
	if !strings.Contains(item["rawMsg"].(string), "干跑记录") {
		t.Fatalf("rawMsg 应为原始指令 JSON: %v", item)
	}

	// history/callbacks：≥1 行（干跑 exec_result outbox）
	status, env = f.rest(t, http.MethodGet, "history/callbacks", "", nil)
	if status != http.StatusOK || dataMap(t, env)["total"].(float64) < 1 {
		t.Fatalf("history/callbacks: %v", env)
	}

	// history/messages：未配对设备 → 空列表 + deprecated 标注
	status, env = f.rest(t, http.MethodGet, "history/messages", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("history/messages: %d %v", status, env)
	}
	d = dataMap(t, env)
	if d["deprecated"] != true {
		t.Fatalf("messages 面应标 deprecated: %v", d)
	}
}

// ---- f) 队列管理组 ----

// TestOpenAPIQueueGroup 队列组全流程：查询 → 插队 → 指定清除（101018 回执）→
// 清空（101018 回执）→ 404/409 语义。
func TestOpenAPIQueueGroup(t *testing.T) {
	f := newOpenAPIFixture(t)
	ctx := context.Background()

	var ids []string
	for i, content := range []string{"q1", "q2", "q3"} {
		_ = i
		_, env := f.post(t, `{"type":203,"targetRoom":"队列群","receivedContent":"`+content+`"}`, nil)
		ids = append(ids, dataMap(t, env)["commandId"].(string))
	}

	// GET 队列：3 条，position 1..3
	status, env := f.rest(t, http.MethodGet, "queue", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("GET queue: %d %v", status, env)
	}
	d := dataMap(t, env)
	if d["count"].(float64) != 3 {
		t.Fatalf("队列应 3 条: %v", d)
	}
	list := d["list"].([]any)
	if list[0].(map[string]any)["commandId"] != ids[0] || list[2].(map[string]any)["position"].(float64) != 3 {
		t.Fatalf("队列顺序异常: %v", list)
	}

	// POST priority：第 3 条插队 → position 1
	status, env = f.rest(t, http.MethodPost, "queue/priority", `{"commandId":"`+ids[2]+`"}`, nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("priority: %d %v", status, env)
	}
	d = dataMap(t, env)
	if d["previousPosition"].(float64) != 3 || d["position"].(float64) != 1 || d["adjusted"] != true {
		t.Fatalf("priority 结果异常: %v", d)
	}
	if d["originalCreateTime"].(float64) <= 0 {
		t.Fatalf("priority 应携带原序键时间: %v", d)
	}
	_, env = f.rest(t, http.MethodGet, "queue", "", nil)
	if got := dataMap(t, env)["list"].([]any)[0].(map[string]any)["commandId"]; got != ids[2] {
		t.Fatalf("插队后队首应为 ids[2]: %v", got)
	}
	// 已在队首再插队 → adjusted=false（幂等）
	_, env = f.rest(t, http.MethodPost, "queue/priority", `{"commandId":"`+ids[2]+`"}`, nil)
	if dataMap(t, env)["adjusted"] != false {
		t.Fatalf("队首再插队应 adjusted=false: %v", env)
	}
	// 不存在的指令 → 404
	status, env = f.rest(t, http.MethodPost, "queue/priority", `{"commandId":"no-such-id"}`, nil)
	if status != http.StatusNotFound || codeOf(t, env) != openapiCodeNotFound {
		t.Fatalf("priority 未知指令应 404: %d %v", status, env)
	}

	// DELETE queue/{id}：未携审批 → 202 登记待审批（REV-SRV-05：305 同 304 L2 门）
	status, env = f.rest(t, http.MethodDelete, "queue/"+ids[1], "", nil)
	if status != http.StatusAccepted || codeOf(t, env) != openapiCodeApprovalRequired {
		t.Fatalf("指定取消未携审批应 202/%d: %d %v", openapiCodeApprovalRequired, status, env)
	}
	cancelApproval := f.approveQueueOp(t, env)
	// 携已批审批号：指定清除 ids[1] → cancelled + 101018 回执
	status, env = f.queueOpWithApproval(t, http.MethodDelete, "queue/"+ids[1], cancelApproval)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("DELETE queue/{id}: %d %v", status, env)
	}
	d = dataMap(t, env)
	if d["status"] != "cancelled" || d["errorCode"].(float64) != openapiUpstreamInstrCancelled ||
		d["errorCodeName"] != "指令取消" {
		t.Fatalf("指定清除应 101018: %v", d)
	}
	// 回执行落 outbox（101018 → history/results 可查）
	items, _ := f.db.OutboxByCommand(ctx, ids[1])
	found101018 := false
	for _, it := range items {
		if it.Kind == "exec_result" && strings.Contains(it.Payload, `"errorCode":101018`) {
			found101018 = true
		}
	}
	if !found101018 {
		t.Fatalf("指定清除应落 101018 回执行: %v", items)
	}
	// 已取消再清 → 409/101012（仅 queued 可清；状态先于审批门）
	status, env = f.rest(t, http.MethodDelete, "queue/"+ids[1], "", nil)
	if status != http.StatusConflict || codeOf(t, env) != openapiUpstreamIllegalOp {
		t.Fatalf("重复清除应 409/101012: %d %v", status, env)
	}
	// 未知指令 → 404
	status, env = f.rest(t, http.MethodDelete, "queue/unknown-id", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("未知指令应 404: %d", status)
	}

	// DELETE queue：未携审批 → 202；决议后清空剩余 2 条 → 101018 × 2
	status, env = f.rest(t, http.MethodDelete, "queue", "", nil)
	if status != http.StatusAccepted || codeOf(t, env) != openapiCodeApprovalRequired {
		t.Fatalf("清空未携审批应 202/%d: %d %v", openapiCodeApprovalRequired, status, env)
	}
	clearApproval := f.approveQueueOp(t, env)
	status, env = f.queueOpWithApproval(t, http.MethodDelete, "queue", clearApproval)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("DELETE queue: %d %v", status, env)
	}
	d = dataMap(t, env)
	if d["cleared"].(float64) != 2 || len(d["commandIds"].([]any)) != 2 {
		t.Fatalf("清空应 2 条: %v", d)
	}
	_, env = f.rest(t, http.MethodGet, "queue", "", nil)
	if dataMap(t, env)["count"].(float64) != 0 {
		t.Fatalf("清空后队列应空: %v", env)
	}
	// 每条被清指令均有 101018 回执
	for _, id := range []string{ids[0], ids[2]} {
		items, _ := f.db.OutboxByCommand(ctx, id)
		hit := false
		for _, it := range items {
			if strings.Contains(it.Payload, `"errorCode":101018`) {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("被清指令 %s 缺 101018 回执", id)
		}
	}
	// 清空后再清（一次性审批已消费，须走新审批）→ 0 条（幂等成功）
	status, env = f.rest(t, http.MethodDelete, "queue", "", nil)
	if status != http.StatusAccepted {
		t.Fatalf("已消费审批后再清须重新登记审批（202）: %d %v", status, env)
	}
	clearApproval2 := f.approveQueueOp(t, env)
	_, env = f.queueOpWithApproval(t, http.MethodDelete, "queue", clearApproval2)
	if dataMap(t, env)["cleared"].(float64) != 0 {
		t.Fatalf("空队列清空应 0: %v", env)
	}
}

// TestOpenAPIQueueWritesRequireIdemKey 队列写操作缺 Idempotency-Key → 101003。
func TestOpenAPIQueueWritesRequireIdemKey(t *testing.T) {
	f := newOpenAPIFixture(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodDelete, "queue"},
		{http.MethodPost, "queue/priority"},
	} {
		req, _ := http.NewRequest(c.method, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/"+c.path, nil)
		req.Header.Set("Authorization", "Bearer "+f.token)
		resp, _ := http.DefaultClient.Do(req)
		var env map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&env)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || codeOf(t, env) != openapiCodeIdemKeyRequired {
			t.Fatalf("%s %s 缺幂等键应 400/101003: %d %v", c.method, c.path, resp.StatusCode, env)
		}
	}
}

// ---- ④ 扩展错误码族 ----

// TestOpenAPIExtendedErrorCodes 扩展码族完整性：ask 指定的 101014/101016/101017/
// 101018/201017~201020/201021 全部登记且注解可达。
func TestOpenAPIExtendedErrorCodes(t *testing.T) {
	required := []int{
		openapiUpstreamRepeatInstruct, openapiUpstreamNetwork, openapiUpstreamException,
		openapiUpstreamInstrCancelled, openapiUpstreamGroupTemplate, openapiUpstreamCreateGroupLimit,
		openapiUpstreamGroupExists, openapiUpstreamAddFriendLimit, openapiUpstreamGroupExited,
	}
	for _, c := range required {
		if c == 0 {
			t.Fatalf("扩展码常量未定义: %d", c)
		}
		if OpenAPICodeName(c) == "" {
			t.Fatalf("扩展码 %d 缺中文名注解", c)
		}
	}
	// 分层纪律：101018 只在回执面出现（exec_result 行），不出现在 REST 信封码段
	if _, isEnvelope := map[int]bool{
		openapiCodeSuccess: true, openapiCodeInvalidBody: true, openapiCodeUnknownCommand: true,
		openapiCodeIdemKeyRequired: true, openapiCodeRateLimited: true, openapiCodeNotWhitelisted: true,
		openapiCodeApprovalRequired: true, openapiCodeIdemConflict: true, openapiCodeRiskFrozen: true,
		openapiCodeCircuitOpen: true, openapiCodeNotFound: true,
	}[openapiUpstreamInstrCancelled]; isEnvelope {
		t.Fatal("101018 不得进入服务端自分配信封段")
	}
}

// ---- ⑤ 鉴权负例全覆盖 ----

// TestOpenAPIRestAuthNegatives 每个新端点 ≥1 鉴权负例：无 Authorization → 401。
func TestOpenAPIRestAuthNegatives(t *testing.T) {
	f := newOpenAPIFixture(t)
	cases := []struct{ method, path, body string }{
		{http.MethodGet, "info", ""},
		{http.MethodGet, "online", ""},
		{http.MethodGet, "logs", ""},
		{http.MethodGet, "corps", ""},
		{http.MethodGet, "groups", ""},
		{http.MethodGet, "callbacks", ""},
		{http.MethodPost, "callbacks", `{"type":1,"callBackUrl":"http://127.0.0.1:9/x"}`},
		{http.MethodPut, "callbacks", `{"type":1,"callBackUrl":"http://127.0.0.1:9/x"}`},
		{http.MethodDelete, "callbacks?type=1", ""},
		{http.MethodGet, "queue", ""},
		{http.MethodDelete, "queue", ""},
		{http.MethodDelete, "queue/some-id", ""},
		{http.MethodPost, "queue/priority", `{"commandId":"x"}`},
		{http.MethodGet, "history/commands", ""},
		{http.MethodGet, "history/results", ""},
		{http.MethodGet, "history/callbacks", ""},
		{http.MethodGet, "history/messages", ""},
	}
	for _, c := range cases {
		if got := f.restNoAuth(t, c.method, c.path, c.body); got != http.StatusUnauthorized {
			t.Fatalf("%s %s 无鉴权应 401，实际 %d", c.method, c.path, got)
		}
	}
	// 坏 token 同样 401/501012（抽查扩展面）
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/info", nil)
	req.Header.Set("Authorization", "Bearer deadbeef")
	resp, _ := http.DefaultClient.Do(req)
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || codeOf(t, env) != openapiCodeInvalidToken {
		t.Fatalf("坏 token 应 401/501012: %d %v", resp.StatusCode, env)
	}
	// robot 不匹配 → 403/501011（抽查）
	other, _ := f.api.IssueOpenAPIToken(context.Background(), "robot-rest-other", "tenant-a")
	req, _ = http.NewRequest(http.MethodGet, f.srv.URL+"/openapi/v1/robots/"+f.robot+"/online", nil)
	req.Header.Set("Authorization", "Bearer "+other)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || codeOf(t, env) != openapiCodeRobotMismatch {
		t.Fatalf("他 robot token 应 403/501011: %d %v", resp.StatusCode, env)
	}
}
