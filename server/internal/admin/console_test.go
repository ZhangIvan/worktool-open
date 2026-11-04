// 完整管理后台数据面 httptest 套件（W-P0 admin-ui 车道）：概览聚合 / 设备
// 操作（暂停门）/ 指令提交与详情 / 队列操作 / 回调 CRUD 与日志 / 日志查询 /
// 设置（guard·白名单·熔断·加密·令牌·分级）。装配与 admin_test.go 同型：
// 真 SQLite + 真管线，按真实落库断言，不做接口 mock。
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
)

// seedDevice 落一台设备并登记一条会话（供最近活跃时间投影）。
func seedDevice(t *testing.T, f *fixture, id, robotID string) {
	t.Helper()
	ctx := context.Background()
	nowT := *f.now
	if err := f.db.UpsertDevice(ctx, sqlite.Device{
		DeviceID: id, RobotID: robotID, DeviceName: "WorkTool Test Model-X",
		Status: "active", PairedAt: nowT.Add(-time.Hour), UpdatedAt: nowT,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.CreateSession(ctx, sqlite.Session{
		SessionID: "sess-" + id, DeviceID: id, CreatedAt: nowT.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
}

// ---- ① 概览聚合 ----

func TestOverviewAggregate(t *testing.T) {
	f := newFixture(t)
	seedDevice(t, f, "dev-ov-000001", "robot-ov")
	f.online["dev-ov-000001"] = true

	// 干跑一条（同步走完状态机 → succeeded + outbox 结果行）。
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-ov-000001","type":503,"params":{},"dryRun":true}`, true)
	if st != http.StatusCreated {
		t.Fatalf("干跑提交应 201，got %d %v", st, b)
	}
	// 真实排队一条（503=查我的信息 L0，无设备连接即留 queued）。
	*f.now = f.now.Add(time.Second)
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-ov-000001","type":503,"params":{}}`, true)
	if st != http.StatusCreated {
		t.Fatalf("真实提交应 201，got %d %v", st, b)
	}

	st, b = f.call(t, http.MethodGet, "/api/v1/admin/overview", "", true)
	if st != http.StatusOK {
		t.Fatalf("概览 status=%d %v", st, b)
	}
	if b["devicesTotal"].(float64) != 1 || b["devicesOnline"].(float64) != 1 {
		t.Fatalf("设备计数错误：total=%v online=%v", b["devicesTotal"], b["devicesOnline"])
	}
	// 今日指令 = 2（干跑 + 真实）；干跑不计入成功/失败分母（FR-54）。
	if b["todayCommands"].(float64) != 2 {
		t.Fatalf("今日指令应 2，got %v", b["todayCommands"])
	}
	if b["todaySucceeded"].(float64) != 0 || b["todayFailed"].(float64) != 0 || b["todayErrorRate"].(float64) != 0 {
		t.Fatalf("干跑不得污染错误率：succ=%v fail=%v rate=%v", b["todaySucceeded"], b["todayFailed"], b["todayErrorRate"])
	}
	if b["queuedCommands"].(float64) != 1 {
		t.Fatalf("排队应 1（真实未下发），got %v", b["queuedCommands"])
	}
	cbs, _ := b["recentCallbacks"].([]any)
	if len(cbs) != 1 {
		t.Fatalf("最近回调应含干跑结果 1 条，got %v", cbs)
	} else {
		cb := cbs[0].(map[string]any)
		if cb["kind"] != "exec_result" || cb["status"] != "pending" {
			t.Fatalf("干跑结果应落 outbox pending： %v", cb)
		}
	}
	// 无鉴权反例。
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/overview", "", false); st != http.StatusUnauthorized {
		t.Fatalf("无鉴权应 401，got %d", st)
	}
}

// ---- ② 设备操作：暂停门 / 恢复 / 解绑 ----

func TestDeviceActionsAndPauseGate(t *testing.T) {
	f := newFixture(t)
	seedDevice(t, f, "dev-act-000001", "robot-act")

	// 暂停。
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/devices/dev-act-000001/pause", "{}", true)
	if st != http.StatusOK || b["changed"] != true {
		t.Fatalf("暂停应 200 changed=true，got %d %v", st, b)
	}
	// 暂停门：提交被 409 DEVICE_PAUSED 拒收。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-act-000001","type":503,"params":{}}`, true)
	if st != http.StatusConflict || b["error"] != "DEVICE_PAUSED" {
		t.Fatalf("暂停设备提交应 409 DEVICE_PAUSED，got %d %v", st, b)
	}
	// v1:robotId 寻址同样被门拦截（robot 绑定反查）。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"v1:robot-act","type":503,"params":{}}`, true)
	if st != http.StatusConflict || b["error"] != "DEVICE_PAUSED" {
		t.Fatalf("v1 寻址暂停设备提交应 409，got %d %v", st, b)
	}
	// 恢复后可提交。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/devices/dev-act-000001/resume", "{}", true); st != http.StatusOK {
		t.Fatalf("恢复应 200，got %d", st)
	}
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-act-000001","type":503,"params":{}}`, true)
	if st != http.StatusCreated {
		t.Fatalf("恢复后提交应 201，got %d %v", st, b)
	}
	// 解绑：robot 绑定清空（v1 寻址随之失效——解析不到设备行，不再受设备门约束）。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/devices/dev-act-000001/unbind", "{}", true); st != http.StatusOK {
		t.Fatalf("解绑应 200，got %d", st)
	}
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/devices", "", true)
	for _, d := range b["devices"].([]any) {
		m := d.(map[string]any)
		if m["deviceId"] == "dev-act-000001" {
			if m["robotId"] != "" {
				t.Fatalf("解绑后 robotId 应为空，got %v", m["robotId"])
			}
			if m["lastSessionMs"].(float64) <= 0 {
				t.Fatalf("设备列表应含最近会话时间，got %v", m["lastSessionMs"])
			}
		}
	}
	// 操作留审计。
	rows, err := f.db.QueryDump(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE kind IN ('admin_device_paused','admin_device_resumed','admin_device_unbound')`)
	if err != nil || rows[0][0] != "3" {
		t.Fatalf("三项设备操作须各留一条审计，rows=%v err=%v", rows, err)
	}
	// 不存在设备 / 非法动作。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/devices/no-such-device/pause", "{}", true); st != http.StatusNotFound {
		t.Fatalf("不存在设备应 404，got %d", st)
	}
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/devices/dev-act-000001/explode", "{}", true); st != http.StatusBadRequest {
		t.Fatalf("非法动作应 400，got %d", st)
	}
}

// ---- ③a 指令提交：L2 待审批流 + 载荷校验 ----

func TestAdminCommandSubmitApprovalFlow(t *testing.T) {
	f := newFixture(t)
	// L2（219 解散群）无审批提交 → 202 + approvalId（FR-53）。
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-x-00000001","type":219,"params":{"targetRoom":"G1"}}`, true)
	if st != http.StatusAccepted || b["state"] != "awaiting_approval" {
		t.Fatalf("L2 无审批应 202 awaiting_approval，got %d %v", st, b)
	}
	approvalID, _ := b["approvalId"].(string)
	if approvalID == "" {
		t.Fatalf("应携带 approvalId：%v", b)
	}
	// 决议（P0-3：只认 approver 令牌）后携审批号提交 → 201 queued。
	if st, _ := f.callTok(t, http.MethodPost, "/api/v1/admin/approvals/"+approvalID+"/decision",
		`{"approver":"member-b","decision":"approve","reason":"复核通过"}`, f.approverToken); st != http.StatusOK {
		t.Fatalf("决议应 200，got %d", st)
	}
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-x-00000001","type":219,"params":{"targetRoom":"G1"},"approvalId":"`+approvalID+`"}`, true)
	if st != http.StatusCreated || b["state"] != "queued" {
		t.Fatalf("携已批审批提交应 201 queued，got %d %v", st, b)
	}
	// 载荷与类型不一致 → 400（管线静态校验拒绝）。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-x-00000001","type":203,"payload":{"type":204,"targetRoom":"g"}}`, true); st != http.StatusBadRequest {
		t.Fatalf("payload.type 与 type 不一致应 400，got %d", st)
	}
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/commands", `{"type":203}`, true); st != http.StatusBadRequest {
		t.Fatalf("缺 target 应 400，got %d", st)
	}
	// 未知字段 → 400。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"d","type":203,"params":{},"extra":1}`, true); st != http.StatusBadRequest {
		t.Fatalf("未知字段应 400，got %d", st)
	}
}

// ---- ③b 指令详情：轨迹 + 结果 JSON ----

func TestCommandDetailTimelineAndResult(t *testing.T) {
	f := newFixture(t)
	// 干跑 503 → 同步走完状态机（E01 入队 + E05/E09 证据链 + E10 终态）+ 结果 outbox 行。
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-d-000000001","type":503,"params":{},"dryRun":true}`, true)
	if st != http.StatusCreated || b["state"] != "succeeded" {
		t.Fatalf("干跑应同步 succeeded，got %d %v", st, b)
	}
	if _, ok := b["dryRunReport"]; !ok {
		t.Fatalf("干跑响应应含 dryRunReport：%v", b)
	}
	commandID, _ := b["commandId"].(string)

	st, b = f.call(t, http.MethodGet, "/api/v1/admin/commands/"+commandID, "", true)
	if st != http.StatusOK {
		t.Fatalf("详情 status=%d", st)
	}
	cmd, _ := b["command"].(map[string]any)
	if cmd["dryRun"] != true || cmd["payload"] == "" {
		t.Fatalf("详情应含载荷与干跑标记：%v", cmd)
	}
	events, _ := b["events"].([]any)
	if len(events) < 2 {
		t.Fatalf("状态轨迹应 ≥2 条边（E01+），got %v", events)
	}
	hasE01 := false
	for _, e := range events {
		if e.(map[string]any)["edge"] == "E01" {
			hasE01 = true
		}
	}
	if !hasE01 {
		t.Fatalf("轨迹应含 E01 入队边：%v", events)
	}
	results, _ := b["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("干跑终态应产生 1 条结果行，got %v", results)
	}
	payload := results[0].(map[string]any)["payload"].(map[string]any)
	if payload["commandId"] != commandID || payload["finalState"] != "succeeded" {
		t.Fatalf("结果 JSON 应绑定指令与终态：%v", payload)
	}
	// 不存在 → 404。
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/commands/no-such-cmd", "", true); st != http.StatusNotFound {
		t.Fatalf("不存在指令应 404，got %d", st)
	}
}

// ---- ③c 队列操作：插队 / 移除 / 清空 ----

func TestQueueOperations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const target = "dev-q-00000001"
	// 三条真实排队指令（503=查我的信息 L0；时钟逐秒推进保证 FIFO 序可断言）。
	ids := []string{}
	for i := 0; i < 3; i++ {
		*f.now = f.now.Add(time.Second)
		st, b := f.call(t, http.MethodPost, "/api/v1/admin/commands",
			`{"target":"`+target+`","type":503,"params":{"receivedContent":"q`+string(rune('A'+i))+`"}}`, true)
		if st != http.StatusCreated {
			t.Fatalf("排队提交 %d 应 201，got %d %v", i, st, b)
		}
		ids = append(ids, b["commandId"].(string))
	}

	// 队列视图：按目标分组序位 1..3。
	st, b := f.call(t, http.MethodGet, "/api/v1/admin/queue?target="+target, "", true)
	if st != http.StatusOK {
		t.Fatalf("队列 status=%d", st)
	}
	q, _ := b["queue"].([]any)
	if len(q) != 3 {
		t.Fatalf("队列应 3 条，got %v", q)
	}
	if q[0].(map[string]any)["commandId"] != ids[0] || q[0].(map[string]any)["position"].(float64) != 1 {
		t.Fatalf("FIFO 序位错误：%v", q[0])
	}

	// 插队：末位指令抬至队首（原序键留痕审计）。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/queue/"+ids[2]+"/priority", "{}", true)
	if st != http.StatusOK || b["adjusted"] != true || b["position"].(float64) != 1 {
		t.Fatalf("插队应 adjusted=true position=1，got %d %v", st, b)
	}
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/queue?target="+target, "", true)
	if b["queue"].([]any)[0].(map[string]any)["commandId"] != ids[2] {
		t.Fatalf("插队后队首应为 %s：%v", ids[2], b["queue"])
	}

	// 移除中位指令 → cancelled + 101018 回执。
	st, b = f.call(t, http.MethodDelete, "/api/v1/admin/queue/"+ids[1], "", true)
	if st != http.StatusOK || b["status"] != "cancelled" {
		t.Fatalf("移除应 cancelled，got %d %v", st, b)
	}
	rows, err := f.db.QueryDump(ctx,
		`SELECT payload FROM outbox WHERE command_id = ? AND kind='exec_result'`, ids[1])
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0][0], "101018") {
		t.Fatalf("被移除指令应落 101018 回执：rows=%v err=%v", rows, err)
	}
	// 非 queued 指令再移除 → 409。
	if st, _ := f.call(t, http.MethodDelete, "/api/v1/admin/queue/"+ids[1], "", true); st != http.StatusConflict {
		t.Fatalf("已取消指令移除应 409，got %d", st)
	}

	// 清空目标队列（余 2 条）。
	st, b = f.call(t, http.MethodDelete, "/api/v1/admin/queue?target="+target, "", true)
	if st != http.StatusOK || b["cleared"].(float64) != 2 {
		t.Fatalf("清空应 cleared=2，got %d %v", st, b)
	}
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/queue?target="+target, "", true)
	if b["count"].(float64) != 0 {
		t.Fatalf("清空后队列应为空：%v", b)
	}
	// 缺 target 清空 → 400。
	if st, _ := f.call(t, http.MethodDelete, "/api/v1/admin/queue", "", true); st != http.StatusBadRequest {
		t.Fatalf("缺 target 清空应 400，got %d", st)
	}
}

// ---- ④ 回调 CRUD + 测试 + 日志 ----

func TestCallbacksCRUDAndLogs(t *testing.T) {
	f := newFixture(t)
	// loopback 探测接收器：200 + application/json（绑定硬校验）。
	probe := newJSONProbe(t)
	defer probe.Close()

	// 新增绑定（探测通过）。
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/callbacks",
		`{"robotId":"robot-cb","type":1,"callBackUrl":"`+probe.URL+`/cb"}`, true)
	if st != http.StatusOK {
		t.Fatalf("绑定应 200（探测通过），got %d %v", st, b)
	}
	// 列表。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/callbacks", "", true)
	if st != http.StatusOK || b["count"].(float64) != 1 {
		t.Fatalf("列表应 1 条，got %d %v", st, b)
	}
	row := b["callbacks"].([]any)[0].(map[string]any)
	if row["typeName"] != "指令结果回调" || row["enabled"] != true {
		t.Fatalf("绑定投影字段错误：%v", row)
	}
	// 编辑（PUT 仅更新已存在）。
	st, _ = f.call(t, http.MethodPut, "/api/v1/admin/callbacks",
		`{"robotId":"robot-cb","type":1,"callBackUrl":"`+probe.URL+`/cb2"}`, true)
	if st != http.StatusOK {
		t.Fatalf("编辑应 200，got %d", st)
	}
	if st, _ := f.call(t, http.MethodPut, "/api/v1/admin/callbacks",
		`{"robotId":"robot-none","type":1,"callBackUrl":"`+probe.URL+`/cb"}`, true); st != http.StatusNotFound {
		t.Fatalf("PUT 不存在绑定应 404，got %d", st)
	}
	// 探测失败目标（200 但 text/plain）不可绑定。
	bad := newTextProbe(t)
	defer bad.Close()
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/callbacks",
		`{"robotId":"robot-cb","type":5,"callBackUrl":"`+bad.URL+`/cb"}`, true); st != http.StatusBadRequest {
		t.Fatalf("探测不通过绑定应 400，got %d", st)
	}
	// 非 loopback 硬拒（SR-09 默认禁外网）。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/callbacks",
		`{"robotId":"robot-cb","type":5,"callBackUrl":"http://example.com/cb"}`, true); st != http.StatusForbidden {
		t.Fatalf("非 loopback 绑定应 403，got %d", st)
	}
	// 保留名硬拒。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/callbacks",
		`{"robotId":"admin","type":1,"callBackUrl":"`+probe.URL+`/cb"}`, true); st != http.StatusBadRequest {
		t.Fatalf("保留名 robotId 应 400，got %d", st)
	}
	// 测试端点：可达 / 不可达。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/callbacks/test",
		`{"callBackUrl":"`+probe.URL+`/cb"}`, true)
	if st != http.StatusOK || b["reachable"] != true {
		t.Fatalf("测试可达端点应 reachable=true，got %d %v", st, b)
	}
	// 回调日志（干跑终态产生 outbox 行）。
	f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-cb-00000001","type":503,"params":{},"dryRun":true}`, true)
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/callbacks/logs?status=pending", "", true)
	if st != http.StatusOK || b["count"].(float64) != 1 {
		t.Fatalf("回调日志应 1 条 pending，got %d %v", st, b)
	}
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/callbacks/logs?status=bogus", "", true); st != http.StatusBadRequest {
		t.Fatalf("非法 status 应 400，got %d", st)
	}
	// 删除。
	st, b = f.call(t, http.MethodDelete, "/api/v1/admin/callbacks?robotId=robot-cb&type=1", "", true)
	if st != http.StatusOK || b["deleted"].(float64) != 1 {
		t.Fatalf("删除应 deleted=1，got %d %v", st, b)
	}
	if st, _ := f.call(t, http.MethodDelete, "/api/v1/admin/callbacks?robotId=robot-cb&type=1", "", true); st != http.StatusNotFound {
		t.Fatalf("重复删除应 404，got %d", st)
	}
}

// newJSONProbe 起 200 + application/json 的 loopback 探测接收器。
func newJSONProbe(t *testing.T) *httptest.Server { return newProbe(t, "application/json") }

// newTextProbe 起 200 + text/plain 的接收器（探测应不通过）。
func newTextProbe(t *testing.T) *httptest.Server { return newProbe(t, "text/plain") }

// newProbe 起一个以指定 Content-Type 应答 200 的 loopback 接收器。
func newProbe(t *testing.T, contentType string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ---- ⑤ 日志查询：执行日志 + 审计日志 ----

func TestLogQueryEndpoints(t *testing.T) {
	f := newFixture(t)
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-lq-00000001","type":503,"params":{},"dryRun":true}`, true)
	if st != http.StatusCreated {
		t.Fatalf("干跑提交应 201，got %d", st)
	}
	commandID := b["commandId"].(string)

	// 执行日志：全局时间线含该指令的 E01（及后续边）。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/logs/executions?commandId="+commandID, "", true)
	if st != http.StatusOK {
		t.Fatalf("执行日志 status=%d", st)
	}
	events, _ := b["events"].([]any)
	if len(events) < 2 {
		t.Fatalf("干跑指令应有 ≥2 条边留痕，got %v", events)
	}
	ev0 := events[len(events)-1].(map[string]any) // 时间线倒序，最旧在末尾
	if ev0["edge"] != "E01" || ev0["target"] != "dev-lq-00000001" {
		t.Fatalf("最旧事件应为 E01 且带目标：%v", ev0)
	}
	// target 过滤。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/logs/executions?target=dev-lq-00000001", "", true)
	if st != http.StatusOK || b["count"].(float64) < 2 {
		t.Fatalf("target 过滤应命中干跑边链，got %d %v", st, b)
	}

	// 审计日志：管理面提交留痕（kind=admin_command_submitted）。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/logs/audit?kind=admin_command_submitted", "", true)
	if st != http.StatusOK || b["count"].(float64) != 1 {
		t.Fatalf("审计过滤应 1 条管理面提交，got %d %v", st, b)
	}
	entry := b["entries"].([]any)[0].(map[string]any)
	if entry["subject"] != commandID {
		t.Fatalf("审计 subject 应为 commandId：%v", entry)
	}
	// 无鉴权反例。
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/logs/audit", "", false); st != http.StatusUnauthorized {
		t.Fatalf("无鉴权应 401，got %d", st)
	}
}

// ---- ⑥ 设置：guard / 加密 / 熔断 / 白名单 / 分级 / 令牌列表 ----

func TestSettingsEndpoints(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// guard：默认值展示（source=default）。
	st, b := f.call(t, http.MethodGet, "/api/v1/admin/settings", "", true)
	if st != http.StatusOK {
		t.Fatalf("设置 status=%d", st)
	}
	guard, _ := b["guard"].(map[string]any)
	if guard["source"] != "default" || guard["l1MaxPerTx"].(float64) != 10 {
		t.Fatalf("默认 guard 应 10/min default：%v", guard)
	}
	// 更新（即时应用 + 持久化）。
	st, b = f.call(t, http.MethodPut, "/api/v1/admin/settings/guard",
		`{"l1WindowSeconds":120,"l1MaxPerTx":5,"whitelistDefaultDeny":true}`, true)
	if st != http.StatusOK || b["applied"] != true {
		t.Fatalf("guard 更新应 200 applied，got %d %v", st, b)
	}
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/settings", "", true)
	guard, _ = b["guard"].(map[string]any)
	if guard["source"] != "persisted" || guard["l1MaxPerTx"].(float64) != 5 || guard["whitelistDefaultDeny"] != true {
		t.Fatalf("更新后 guard 应 persisted 5/min deny：%v", guard)
	}
	// ApplyPersistedSettings 重放（装配步骤的行为验证）。
	if err := f.s.ApplyPersistedSettings(ctx); err != nil {
		t.Fatalf("重放持久化设置失败：%v", err)
	}
	// 越界值反例。
	if st, _ := f.call(t, http.MethodPut, "/api/v1/admin/settings/guard",
		`{"l1WindowSeconds":0,"l1MaxPerTx":5}`, true); st != http.StatusBadRequest {
		t.Fatalf("越界 guard 应 400，got %d", st)
	}

	// 加密开关：默认关（FR-43）→ 开 → 持久化。
	if enc, _ := b["encryption"].(map[string]any); enc["enabled"] != false {
		t.Fatalf("加密默认应关：%v", b["encryption"])
	}
	st, b = f.call(t, http.MethodPut, "/api/v1/admin/settings/encryption", `{"enabled":true}`, true)
	if st != http.StatusOK || b["enabled"] != true {
		t.Fatalf("加密开关应更新为开，got %d %v", st, b)
	}
	raw, ok, err := f.s.settingGet(ctx, settingsKeyEncryption)
	if err != nil || !ok || raw != "1" {
		t.Fatalf("加密开关应持久化 =1：raw=%q ok=%v err=%v", raw, ok, err)
	}
	if st, _ := f.call(t, http.MethodPut, "/api/v1/admin/settings/encryption", `{"enabled":true,"x":1}`, true); st != http.StatusBadRequest {
		t.Fatalf("加密开关未知字段应 400，got %d", st)
	}

	// 熔断：设置面应含 breaker（重新 GET——上一响应是加密开关的 PUT 体）。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/settings", "", true)
	if _, ok := b["breaker"].(map[string]any); !ok {
		t.Fatalf("设置应含 breaker：%v", b)
	}
	if b["breaker"].(map[string]any)["state"] != "closed" {
		t.Fatalf("初始熔断应 closed：%v", b["breaker"])
	}
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/breaker/reset", "{}", true)
	if st != http.StatusOK || b["state"] != "closed" {
		t.Fatalf("熔断复位应 200 closed，got %d %v", st, b)
	}

	// 分级查询：219→L2、203→L0、9999→L2（未登记 fail-closed）。
	for typ, want := range map[int]string{219: "L2", 203: "L0", 9999: "L2", 229: "L1"} {
		st, b = f.call(t, http.MethodGet, "/api/v1/admin/risk/"+strconv.Itoa(typ), "", true)
		if st != http.StatusOK || b["riskClass"] != want {
			t.Fatalf("分级 type=%d 应 %s，got %d %v", typ, want, st, b)
		}
	}
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/risk/abc", "", true); st != http.StatusBadRequest {
		t.Fatalf("非法分级类型应 400，got %d", st)
	}

	// 白名单 CRUD（经 scheduler 导出面，配置即强制）。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/whitelist",
		`{"target":"dev-wl-00000001","recipient":"room-1"}`, true)
	if st != http.StatusCreated {
		t.Fatalf("白名单新增应 201，got %d %v", st, b)
	}
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/whitelist?target=dev-wl-00000001", "", true)
	if st != http.StatusOK || b["count"].(float64) != 1 {
		t.Fatalf("白名单查询应 1 条，got %d %v", st, b)
	}
	if st, _ := f.call(t, http.MethodDelete, "/api/v1/admin/whitelist?target=dev-wl-00000001&recipient=room-1", "", true); st != http.StatusOK {
		t.Fatalf("白名单删除应 200，got %d", st)
	}

	// 令牌列表：hash 前 8 位 + scope 标注；原始值永不出现。
	rawTok, err := f.s.IssueAdminToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/tokens", "", true)
	if st != http.StatusOK {
		t.Fatalf("令牌列表 status=%d", st)
	}
	toks, _ := b["tokens"].([]any)
	if len(toks) != 3 { // fixture 签发（管理+决议，P0-3）+ 本枚
		t.Fatalf("令牌应 3 枚，got %v", toks)
	}
	foundAdmin, foundApprover := false, false
	for _, tk := range toks {
		m := tk.(map[string]any)
		if m["scope"] == "admin（管理面）" {
			foundAdmin = true
		}
		if m["scope"] == "approver（决议面，P0-3）" {
			foundApprover = true
		}
		if len(m["hashPrefix8"].(string)) != 8 {
			t.Fatalf("hash 前缀应 8 位：%v", m)
		}
	}
	if !foundAdmin {
		t.Fatalf("令牌列表应标注 admin scope：%v", toks)
	}
	if !foundApprover {
		t.Fatalf("令牌列表应标注 approver scope（P0-3）：%v", toks)
	}
	// 响应不得含任何令牌原始值（仅 hash 投影）。
	rawJSON, _ := json.Marshal(b)
	if strings.Contains(string(rawJSON), rawTok) {
		t.Fatal("令牌列表响应不得包含原始值")
	}
}

// ---- 指令历史分页/过滤增强 ----

func TestCommandsPaginationAndTypeFilter(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		*f.now = f.now.Add(time.Second)
		if st, b := f.call(t, http.MethodPost, "/api/v1/admin/commands",
			`{"target":"dev-pg-00000001","type":203,"params":{"targetRoom":"g","receivedContent":"m`+strconv.Itoa(i)+`"}}`, true); st != http.StatusCreated {
			t.Fatalf("排队提交 %d 应 201，got %d %v", i, st, b)
		}
	}
	// type 过滤 + total。
	st, b := f.call(t, http.MethodGet, "/api/v1/admin/commands?type=203&limit=2&offset=0", "", true)
	if st != http.StatusOK || b["total"].(float64) != 3 || b["count"].(float64) != 2 {
		t.Fatalf("type 过滤分页应 total=3 count=2，got %d %v", st, b)
	}
	// 翻页。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/commands?type=203&limit=2&offset=2", "", true)
	if b["count"].(float64) != 1 {
		t.Fatalf("第二页应 1 条，got %v", b)
	}
	// state 过滤组合。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/commands?type=203&state=queued", "", true)
	if b["count"].(float64) != 3 {
		t.Fatalf("state=queued 应 3 条，got %v", b)
	}
	// 非法 type / offset 反例。
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/commands?type=abc", "", true); st != http.StatusBadRequest {
		t.Fatalf("非法 type 应 400，got %d", st)
	}
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/commands?offset=-1", "", true); st != http.StatusBadRequest {
		t.Fatalf("负 offset 应 400，got %d", st)
	}
}
