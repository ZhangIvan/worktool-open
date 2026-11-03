// 事件回调开关管理面测试（T3.5，W-M6）：PUT /api/v1/admin/settings/event-callbacks
// （鉴权/持久化/审计/部分更新）+ 跨面生效（管理面写 → api/events.go 分发内核
// 只读同一 admin_settings 单一事实源 → 图片回调从默认关变为分发）。
// 装配与 console_test.go 同型（fixture 复用），api 侧仅经公开入口注入。
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cn.worktool.open/server/internal/api"
)

// TestAdminEventCallbackSettingsEndpoints：端点正反例——默认值展示（source=
// default）、部分更新（指针字段仅更新提供项）、未知字段 400、无鉴权 401、
// 持久化 + 审计留痕。
func TestAdminEventCallbackSettingsEndpoints(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// 默认形态（events.md §〇上游缺省：总门开、图片关）。
	st, b := f.call(t, http.MethodGet, "/api/v1/admin/settings", "", true)
	if st != http.StatusOK {
		t.Fatalf("设置 status=%d", st)
	}
	ev, _ := b["eventCallbacks"].(map[string]any)
	if ev == nil || ev["pushEventCallback"] != true || ev["pushImage"] != false || ev["source"] != "default" {
		t.Fatalf("默认 eventCallbacks 应 开/关/default: %v", b["eventCallbacks"])
	}

	// 部分更新：只开图片门（总门不随写丢失）。
	st, b = f.call(t, http.MethodPut, "/api/v1/admin/settings/event-callbacks", `{"pushImage":true}`, true)
	if st != http.StatusOK || b["pushImage"] != true || b["pushEventCallback"] != true || b["source"] != "persisted" {
		t.Fatalf("PUT pushImage 应 200 且总门保持开: %d %v", st, b)
	}
	// 关总门（部分更新语义二）。
	st, b = f.call(t, http.MethodPut, "/api/v1/admin/settings/event-callbacks", `{"pushEventCallback":false}`, true)
	if st != http.StatusOK || b["pushEventCallback"] != false || b["pushImage"] != true {
		t.Fatalf("PUT 总门关应保留图片开: %d %v", st, b)
	}
	// 持久化单一事实源（api 内核只读同一行）。
	raw, ok, err := f.s.settingGet(ctx, settingsKeyEventCallbacks)
	if err != nil || !ok {
		t.Fatalf("KV 应持久化: ok=%v err=%v", ok, err)
	}
	var stored map[string]bool
	if json.Unmarshal([]byte(raw), &stored) != nil || !stored["pushImage"] || stored["pushEventCallback"] {
		t.Fatalf("KV 值漂移: %s", raw)
	}
	// 审计留痕（SR-12）。
	audits, _ := f.db.QueryDump(ctx, `SELECT COUNT(*) FROM audit_log WHERE kind='admin_settings_event_callbacks'`)
	if len(audits) != 1 || audits[0][0] != "2" {
		t.Fatalf("开关审计应 2 条: %v", audits)
	}
	// GET 汇总反映 persisted。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/settings", "", true)
	ev, _ = b["eventCallbacks"].(map[string]any)
	if ev["source"] != "persisted" || ev["pushEventCallback"] != false || ev["pushImage"] != true {
		t.Fatalf("GET 应 persisted 关/开: %v", ev)
	}

	// 反例：未知字段 400；空体/非 JSON 400。
	if st, _ := f.call(t, http.MethodPut, "/api/v1/admin/settings/event-callbacks", `{"x":1}`, true); st != http.StatusBadRequest {
		t.Fatalf("未知字段应 400, got %d", st)
	}
	if st, _ := f.call(t, http.MethodPut, "/api/v1/admin/settings/event-callbacks", `not-json`, true); st != http.StatusBadRequest {
		t.Fatalf("非 JSON 应 400, got %d", st)
	}
	// 无鉴权 401。
	if st, _ := f.call(t, http.MethodPut, "/api/v1/admin/settings/event-callbacks", `{"pushImage":true}`, false); st != http.StatusUnauthorized {
		t.Fatalf("无鉴权应 401, got %d", st)
	}
}

// TestAdminEventCallbackSwitchFeedsDispatch：跨面生效闭环——管理面端点写开关
// → api 分发内核（同一 DB）逐事件读同源 KV → 图片回调从默认不分发变为分发
// （出向含 fileBase64/atMe 字符串/textType=2）；总门关 → 101 事件不分发。
func TestAdminEventCallbackSwitchFeedsDispatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// openapi_callbacks / admin_settings 表由 admin.New ensureSchema 幂等共建
	//（DDL 镜像 api 包，物理表同一张）——api 侧免建表直读同一单一事实源。
	srv := api.NewServer(f.db, nil, nil, nil, testLogger{t})

	// 缺省形态：图片回调默认关（doc-861677）。
	img := api.DeviceImageMessage{
		ReceivedName: "张三", GroupName: "技术交流群", GroupRemark: "R",
		RoomType: 1, AtMe: "false", TextType: 2,
		FileBase64: strings.Repeat("aGk=", 4),
	}
	if ok, err := srv.HandleImageMessage(ctx, "v1:robot-ev", img); err != nil || ok {
		t.Fatalf("默认关不得分发: ok=%v err=%v", ok, err)
	}
	// 管理面打开图片门。
	st, b := f.call(t, http.MethodPut, "/api/v1/admin/settings/event-callbacks", `{"pushImage":true}`, true)
	if st != http.StatusOK || b["pushImage"] != true {
		t.Fatalf("PUT 应开图片门: %d %v", st, b)
	}
	if ok, err := srv.HandleImageMessage(ctx, "v1:robot-ev", img); err != nil || !ok {
		t.Fatalf("管理面开后应分发: ok=%v err=%v", ok, err)
	}
	rows, err := f.db.QueryDump(ctx, `SELECT COALESCE(command_id,''), kind FROM outbox WHERE kind='image_callback'`)
	if err != nil || len(rows) != 1 || rows[0][0] != "v1:robot-ev" {
		t.Fatalf("图片回调行应 1 条且 CommandID=目标键: %v %v", rows, err)
	}
	// 出向载荷契约字段（outbox 行即 POST body 原文）。
	payload, err := f.db.QueryDump(ctx, `SELECT payload FROM outbox WHERE kind='image_callback'`)
	if err != nil || len(payload) != 1 {
		t.Fatalf("载荷查询失败: %v", err)
	}
	var body map[string]any
	if json.Unmarshal([]byte(payload[0][0]), &body) != nil {
		t.Fatalf("载荷非法 JSON: %s", payload[0][0])
	}
	if body["atMe"] != "false" || body["textType"] != float64(2) || body["spoken"] != "" || body["fileBase64"] == "" {
		t.Fatalf("图片出向契约漂移: %v", body)
	}

	// 管理面关总门 → 101 被拉进群不分发（每类 ≥2 例的管理面开关半边）。
	if st, b := f.call(t, http.MethodPut, "/api/v1/admin/settings/event-callbacks", `{"pushEventCallback":false}`, true); st != http.StatusOK {
		t.Fatalf("PUT 关总门应 200: %d %v", st, b)
	}
	if ok, err := srv.HandleDeviceEvent(ctx, "v1:robot-ev",
		json.RawMessage(`{"type":101,"groupName":"G","groupRemark":"","roomType":1,"memberList":["a"]}`)); err != nil || ok {
		t.Fatalf("总门关不得分发: ok=%v err=%v", ok, err)
	}
	if n, _ := f.db.QueryDump(ctx, `SELECT COUNT(*) FROM outbox WHERE kind='event_callback'`); len(n) == 1 && n[0][0] != "0" {
		t.Fatalf("总门关不得留行: %v", n)
	}
}
