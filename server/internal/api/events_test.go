// 事件类回调分发测试（T3.5，W-M6；契约 local-private/parity/b3/events.md）。
// 每类事件 ≥2 例（分发成功 / 开关关闭不分发）+ HMAC 签名验证 + 投递目标路由
// （per-robot type=11 消息回调绑定 > 全局 > pending）+ 管理面开关同源生效。
// 装配与 outbox_test.go 同型：真 SQLite + loopback httptest 接收器，按真实
// 落库断言，不做接口 mock。
package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
)

// newEventsServer 事件面装配：真 SQLite + 兼容面扩展表 + admin_settings 镜像表
// （开关单一事实源；DDL 与 admin 包镜像，幂等共建）。
func newEventsServer(t *testing.T) (*Server, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewServer(db, nil, nil, nil, testLogger{t})
	ctx := context.Background()
	if err := s.ensureOpenAPISchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS admin_settings (
	  key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_ms INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return s, db
}

// setEventSwitches 直写开关 KV（管理面写方的等价落库；跨面端点测试在 admin 包）。
func setEventSwitches(t *testing.T, db *sqlite.DB, pushEvent, pushImage bool) {
	t.Helper()
	raw, _ := json.Marshal(map[string]bool{"pushEventCallback": pushEvent, "pushImage": pushImage})
	if _, err := db.Exec(context.Background(), `
INSERT INTO admin_settings(key, value, updated_ms) VALUES ('event_callback_config', ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_ms=excluded.updated_ms`,
		string(raw), time.Now().UTC().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// bindRobotCallback 登记 per-robot 回调绑定（openapi_callbacks；type=11 消息回调）。
func bindRobotCallback(t *testing.T, db *sqlite.DB, robotID string, callbackType int, url string) {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	if _, err := db.Exec(context.Background(), `
INSERT INTO openapi_callbacks(robot_id, type, callback_url, created_ms, updated_ms) VALUES (?,?,?,?,?)
ON CONFLICT(robot_id, type) DO UPDATE SET callback_url=excluded.callback_url, updated_ms=excluded.updated_ms`,
		robotID, callbackType, url, now, now); err != nil {
		t.Fatal(err)
	}
}

// seedCommand 落一条指令行（query_result 路由反查 commands.target 用）。
func seedCommand(t *testing.T, db *sqlite.DB, commandID, target string) {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	if _, err := db.Exec(context.Background(), `
INSERT INTO commands(command_id, target, command_type, payload, state, created_at)
VALUES (?, ?, 512, '{}', 'succeeded', ?)`, commandID, target, now); err != nil {
		t.Fatal(err)
	}
}

// outboxRowsByKind 取指定 kind 的 outbox 行（任意状态，含 command_id 供路由断言）。
func outboxRowsByKind(t *testing.T, db *sqlite.DB, kind string) []sqlite.OutboxItem {
	t.Helper()
	rows, err := db.QueryDump(context.Background(),
		`SELECT COALESCE(command_id,'') FROM outbox WHERE kind = ? ORDER BY id`, kind)
	if err != nil {
		t.Fatalf("outboxRowsByKind(%s): %v", kind, err)
	}
	out := make([]sqlite.OutboxItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, sqlite.OutboxItem{Kind: kind, CommandID: r[0]})
	}
	return out
}

// deliverAll 手动步进投递（等价 worker 周期行为，确定性更强）。
func deliverAll(t *testing.T, w *OutboxWorker) {
	t.Helper()
	for i := 0; i < 5; i++ {
		if err := w.DeliverDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ---- ① 六类被动事件：分发成功（逐类精确 body + HMAC 验签 + 送达） ----

func TestEventCallbackDispatchPerType(t *testing.T) {
	cases := []struct {
		name string
		item string
		want string // 出向 body（events.md 契约形状；map 键序无关，逐字段断言）
	}{
		{
			name: "101被拉进群（memberList 非空随帧）",
			item: `{"eventType":101,"groupName":"技术交流群","groupRemark":"技术交流群-深圳","roomType":3,"memberList":["张三","李四"]}`,
			want: `{"memberList":["张三","李四"],"groupRemark":"技术交流群-深圳","groupName":"技术交流群","roomType":3,"type":101}`,
		},
		{
			name: "102群名更新（无备注群 remark 空——E2）",
			item: `{"type":102,"groupName":"新群名称","roomType":1}`,
			want: `{"groupRemark":"","groupName":"新群名称","roomType":1,"type":102}`,
		},
		{
			name: "103群解散",
			item: `{"type":103,"groupName":"技术交流群","groupRemark":"","roomType":1}`,
			want: `{"groupRemark":"","groupName":"技术交流群","roomType":1,"type":103}`,
		},
		{
			name: "104被移出群",
			item: `{"type":104,"groupName":"B群","groupRemark":"备注B","roomType":2}`,
			want: `{"groupRemark":"备注B","groupName":"B群","roomType":2,"type":104}`,
		},
		{
			name: "105好友通过（friendRemark 空省略）",
			item: `{"eventType":105,"friendName":"张三","friendRemark":""}`,
			want: `{"friendName":"张三","type":105}`,
		},
		{
			name: "105好友通过（friendRemark 非空随帧）",
			item: `{"type":105,"friendName":"李四","friendRemark":"备注名"}`,
			want: `{"friendName":"李四","friendRemark":"备注名","type":105}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db := newEventsServer(t)
			const secret = "event-secret"
			rcv := newReceiver(t, secret, 0)
			w, err := NewOutboxWorker(db, OutboxConfig{CallbackURL: rcv.srv.URL + "/callback", CallbackSecret: secret}, testLogger{t})
			if err != nil {
				t.Fatal(err)
			}
			ok, err := s.HandleDeviceEvent(context.Background(), "v1:robot-a", json.RawMessage(tc.item))
			if err != nil || !ok {
				t.Fatalf("分发应 enqueue: ok=%v err=%v", ok, err)
			}
			rows := outboxRowsByKind(t, db, "event_callback")
			if len(rows) != 1 {
				t.Fatalf("event_callback 行应 1 条: %d", len(rows))
			}
			deliverAll(t, w)
			if rcv.attempts() != 1 || rcv.badSignSeen {
				t.Fatalf("送达应 1 次且验签通过: attempts=%d badSign=%v", rcv.attempts(), rcv.badSignSeen)
			}
			// body 断言按解码后 JSON 比较（map 键序无关；字段集与值须严格一致）。
			var got, want map[string]any
			if err := json.Unmarshal([]byte(rcv.gotBodies[0]), &got); err != nil {
				t.Fatalf("出向 body 非法 JSON: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("出向 body 契约漂移:\n got %s\nwant %s", rcv.gotBodies[0], tc.want)
			}
			delivered, _ := db.OutboxByStatus(context.Background(), "delivered")
			if len(delivered) != 1 {
				t.Fatalf("行应 delivered: %d", len(delivered))
			}
		})
	}
}

// TestEventCallbackSwitchOffPerType：总门关（pushEventCallback=false）→ 五类
// 事件全不分发（每类 ≥2 例的「开关关闭不分发」半边；开关关闭不审计不留行）。
func TestEventCallbackSwitchOffPerType(t *testing.T) {
	items := []string{
		`{"type":101,"groupName":"g","groupRemark":"","roomType":1,"memberList":["a"]}`,
		`{"type":102,"groupName":"g","groupRemark":"","roomType":1}`,
		`{"type":103,"groupName":"g","groupRemark":"","roomType":1}`,
		`{"type":104,"groupName":"g","groupRemark":"","roomType":1}`,
		`{"type":105,"friendName":"f"}`,
	}
	s, db := newEventsServer(t)
	const secret = "off-secret"
	rcv := newReceiver(t, secret, 0)
	setEventSwitches(t, db, false, false) // 总门关
	for i, item := range items {
		ok, err := s.HandleDeviceEvent(context.Background(), "v1:robot-off", json.RawMessage(item))
		if err != nil || ok {
			t.Fatalf("事件 %d 开关关应不分发: ok=%v err=%v", i, ok, err)
		}
	}
	if rows := outboxRowsByKind(t, db, "event_callback"); len(rows) != 0 {
		t.Fatalf("开关关不得留 outbox 行: %d", len(rows))
	}
	if n, _ := db.QueryDump(context.Background(), `SELECT COUNT(*) FROM outbox`); len(n) != 1 || n[0][0] != "0" {
		t.Fatalf("outbox 应零行: %v", n)
	}
	w, err := NewOutboxWorker(db, OutboxConfig{CallbackURL: rcv.srv.URL + "/callback", CallbackSecret: secret}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	deliverAll(t, w)
	if rcv.attempts() != 0 {
		t.Fatalf("开关关不得投递: %d", rcv.attempts())
	}
	audits, _ := db.QueryDump(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE kind='event_callback_invalid'`)
	if len(audits) == 1 && audits[0][0] != "0" {
		t.Fatalf("开关关为正常配置形态，不应落 invalid 审计: %v", audits)
	}
}

// TestEventCallback101MessageListCollision：type=101 同号消歧——无事件签名字段
// 的 receiveMessageList 消息上报项不拦截（交回 gateway 既有路径）。
func TestEventCallback101MessageListCollision(t *testing.T) {
	s, db := newEventsServer(t)
	for _, item := range []string{
		`{"type":101,"messageId":"m-1","list":[{"textType":1,"content":"hi"}]}`,
		`{"type":101}`,
	} {
		ok, err := s.HandleDeviceEvent(context.Background(), "v1:robot-a", json.RawMessage(item))
		if err != nil || ok {
			t.Fatalf("消息上报形态不得当事件: ok=%v err=%v", ok, err)
		}
	}
	if rows := outboxRowsByKind(t, db, "event_callback"); len(rows) != 0 {
		t.Fatalf("不得留行: %d", len(rows))
	}
}

// TestEventCallbackInvalidSkipped：缺必填字段/roomType 越界 → 跳过分发 + 审计
// event_callback_invalid（出向 body 严格契约，宁缺勿错）。
func TestEventCallbackInvalidSkipped(t *testing.T) {
	s, db := newEventsServer(t)
	for _, item := range []string{
		`{"type":102,"roomType":1}`,                 // 缺 groupName
		`{"type":104,"groupName":"g","roomType":7}`, // roomType 越界
		`{"type":105,"friendRemark":"只有备注"}`,        // 缺 friendName
	} {
		ok, err := s.HandleDeviceEvent(context.Background(), "v1:robot-a", json.RawMessage(item))
		if err != nil || ok {
			t.Fatalf("不合法事件应跳过: ok=%v err=%v", ok, err)
		}
	}
	if rows := outboxRowsByKind(t, db, "event_callback"); len(rows) != 0 {
		t.Fatalf("不合法事件不得留行: %d", len(rows))
	}
	audits, _ := db.QueryDump(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE kind='event_callback_invalid'`)
	if len(audits) != 1 || audits[0][0] != "3" {
		t.Fatalf("invalid 审计应 3 条: %v", audits)
	}
}

// ---- ② 查询结果推送（511/512/513/515） ----

func TestQueryResultDispatchPerType(t *testing.T) {
	cases := []struct {
		name    string
		item    string
		wantSub []string // body 子串断言（保持设备原始字段序的透传段用子串）
	}{
		{
			name:    "511会议（多会议透传 + messageId）",
			item:    `{"type":511,"messageId":"msg-9","date":"2026-11-03","meetingInfoList":[{"meetingName":"周会","meetingTime":"10:00","actualMeetingTime":"10:03","orgName":"A部门"},{"meetingName":"评审","meetingTime":"14:00","actualMeetingTime":"","orgName":"B部门"}]}`,
			wantSub: []string{`"type":511`, `"messageId":"msg-9"`, `"date":"2026-11-03"`, `"meetingName":"周会"`, `"actualMeetingTime":"10:03"`, `"orgName":"B部门"`},
		},
		{
			name:    "512群成员（空成员列表仍推送 + memberCount 兜底 + messageId 增强）",
			item:    `{"type":512,"messageId":"msg-8","groupName":"G","groupRemark":"R","groupOwner":"群主","groupAnnouncement":"","memberList":[]}`,
			wantSub: []string{`"type":512`, `"groupName":"G"`, `"groupOwner":"群主"`, `"memberList":[]`, `"memberCount":0`, `"messageId":"msg-8"`},
		},
		{
			name:    "513设备资源（data 透传）",
			item:    `{"type":513,"data":{"batteryLevel":88,"storageFreeGB":12,"model":"X"}}`,
			wantSub: []string{`"type":513`, `"batteryLevel":88`, `"storageFreeGB":12`, `"model":"X"`},
		},
		{
			name:    "515群名+备注（轻量 + messageId 增强）",
			item:    `{"type":515,"messageId":"msg-7","groupName":"G5","groupRemark":"R5"}`,
			wantSub: []string{`"type":515`, `"groupName":"G5"`, `"groupRemark":"R5"`, `"messageId":"msg-7"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db := newEventsServer(t)
			const secret = "query-secret"
			rcv := newReceiver(t, secret, 0)
			w, err := NewOutboxWorker(db, OutboxConfig{CallbackURL: rcv.srv.URL + "/callback", CallbackSecret: secret}, testLogger{t})
			if err != nil {
				t.Fatal(err)
			}
			ok, err := s.HandleQueryResult(context.Background(), "v1:robot-a", "cmd-q-1", json.RawMessage(tc.item))
			if err != nil || !ok {
				t.Fatalf("查询推送应 enqueue: ok=%v err=%v", ok, err)
			}
			rows := outboxRowsByKind(t, db, "query_result")
			if len(rows) != 1 || rows[0].CommandID != "cmd-q-1" {
				t.Fatalf("query_result 行应 1 条且带 commandId: %+v", rows)
			}
			deliverAll(t, w)
			if rcv.attempts() != 1 || rcv.badSignSeen {
				t.Fatalf("送达应 1 次且验签通过: attempts=%d badSign=%v", rcv.attempts(), rcv.badSignSeen)
			}
			got := rcv.gotBodies[0]
			for _, sub := range tc.wantSub {
				if !strings.Contains(got, sub) {
					t.Fatalf("body 缺 %s:\n%s", sub, got)
				}
			}
		})
	}
}

// TestQueryResult511NoMeetingNoPush：无会议不推送（events.md §五 511 规则）。
func TestQueryResult511NoMeetingNoPush(t *testing.T) {
	s, db := newEventsServer(t)
	for _, item := range []string{
		`{"type":511,"messageId":"m","date":"2026-11-03","meetingInfoList":[]}`,
		`{"type":511,"date":"2026-11-03"}`,
		`{"type":513,"data":"not-object"}`,
		`{"type":512,"groupRemark":"缺群名"}`,
	} {
		ok, err := s.HandleQueryResult(context.Background(), "v1:robot-a", "", json.RawMessage(item))
		if err != nil || ok {
			t.Fatalf("无会议/非法载荷应跳过: item=%s ok=%v err=%v", item, ok, err)
		}
	}
	if rows := outboxRowsByKind(t, db, "query_result"); len(rows) != 0 {
		t.Fatalf("不得留行: %d", len(rows))
	}
	// 回执轨道不受影响（E7 双轨：推送跳过 ≠ 回执消失）——回执行由 scheduler
	// exec_result 路径承载，此处仅断言本轨道未误伤其他 kind。
	if n, _ := db.QueryDump(context.Background(), `SELECT COUNT(*) FROM outbox`); len(n) == 1 && n[0][0] != "0" {
		t.Fatalf("outbox 应零行: %v", n)
	}
}

// TestQueryResultSwitchOff：总门关 → 查询推送跳过（仍回执——回执轨道在
// scheduler，不属本断言域）；恢复总门后同帧照常 enqueue。
func TestQueryResultSwitchOff(t *testing.T) {
	s, db := newEventsServer(t)
	item := json.RawMessage(`{"type":515,"groupName":"G","groupRemark":"R"}`)
	setEventSwitches(t, db, false, false)
	if ok, err := s.HandleQueryResult(context.Background(), "v1:robot-a", "", item); err != nil || ok {
		t.Fatalf("总门关应跳过: ok=%v err=%v", ok, err)
	}
	setEventSwitches(t, db, true, false)
	if ok, err := s.HandleQueryResult(context.Background(), "v1:robot-a", "", item); err != nil || !ok {
		t.Fatalf("总门开应 enqueue: ok=%v err=%v", ok, err)
	}
	if rows := outboxRowsByKind(t, db, "query_result"); len(rows) != 1 {
		t.Fatalf("恢复后应 1 行: %d", len(rows))
	}
}

// ---- ③ 图片回调（pushImage 默认关） ----

func TestImageCallbackDefaultOff(t *testing.T) {
	s, db := newEventsServer(t)
	msg := DeviceImageMessage{ReceivedName: "张三", GroupName: "G", GroupRemark: "R", RoomType: 1, AtMe: "false", TextType: 2, FileBase64: "aGVsbG8="}
	if ok, err := s.HandleImageMessage(context.Background(), "v1:robot-a", msg); err != nil || ok {
		t.Fatalf("默认关不得分发: ok=%v err=%v", ok, err)
	}
	if rows := outboxRowsByKind(t, db, "image_callback"); len(rows) != 0 {
		t.Fatalf("默认关不得留行: %d", len(rows))
	}
	// 开关矩阵另一半：管理面开（写同一 KV 单一事实源）→ 同帧照常分发。
	setEventSwitches(t, db, true, true)
	ok, err := s.HandleImageMessage(context.Background(), "v1:robot-a", msg)
	if err != nil || !ok {
		t.Fatalf("开=应分发: ok=%v err=%v", ok, err)
	}
	if rows := outboxRowsByKind(t, db, "image_callback"); len(rows) != 1 {
		t.Fatalf("开后应 1 行: %d", len(rows))
	}
}

func TestImageCallbackBodyContractAndHMAC(t *testing.T) {
	s, db := newEventsServer(t)
	const secret = "img-secret"
	rcv := newReceiver(t, secret, 0)
	w, err := NewOutboxWorker(db, OutboxConfig{CallbackURL: rcv.srv.URL + "/callback", CallbackSecret: secret}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	setEventSwitches(t, db, true, true)
	// atMe 布尔入向归一（契约：勿按布尔解析——出向恒字符串）。
	msg := DeviceImageMessage{ReceivedName: "李四", GroupName: "技术交流群", GroupRemark: "R", RoomType: 3, AtMe: "true", TextType: 2, FileBase64: "aGVsbG8="}
	if ok, err := s.HandleImageMessage(context.Background(), "v1:robot-a", msg); err != nil || !ok {
		t.Fatalf("应分发: ok=%v err=%v", ok, err)
	}
	deliverAll(t, w)
	if rcv.attempts() != 1 || rcv.badSignSeen {
		t.Fatalf("送达应 1 次且验签通过: attempts=%d badSign=%v", rcv.attempts(), rcv.badSignSeen)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rcv.gotBodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["spoken"] != "" || body["rawSpoken"] != "" {
		t.Fatalf("图片消息 spoken/rawSpoken 恒空串: %v", body)
	}
	if body["atMe"] != "true" {
		t.Fatalf("atMe 须字符串型: %v (%T)", body["atMe"], body["atMe"])
	}
	if body["textType"] != float64(2) || body["fileBase64"] != "aGVsbG8=" || body["roomType"] != float64(3) {
		t.Fatalf("图片契约字段漂移: %v", body)
	}
	// 不合法形态：textType 非 2 / 缺 fileBase64 / atMe 非法 / roomType 越界 / 超限。
	MaxImageCallbackPayloadBytes = 8
	t.Cleanup(func() { MaxImageCallbackPayloadBytes = 16 << 20 })
	for i, bad := range []DeviceImageMessage{
		{TextType: 1, FileBase64: "x", AtMe: "false"},
		{TextType: 2, FileBase64: "", AtMe: "false"},
		{TextType: 2, FileBase64: "x", AtMe: "maybe"},
		{TextType: 2, FileBase64: "x", AtMe: "false", RoomType: 9},
		{TextType: 2, FileBase64: strings.Repeat("x", 9), AtMe: "false"},
	} {
		if ok, err := s.HandleImageMessage(context.Background(), "v1:robot-a", bad); err != nil || ok {
			t.Fatalf("不合法图片 %d 应跳过: ok=%v err=%v", i, ok, err)
		}
	}
	if rows := outboxRowsByKind(t, db, "image_callback"); len(rows) != 1 {
		t.Fatalf("不合法图片不得加行: %d", len(rows))
	}
}

// ---- ④ 投递目标路由（type=11 消息回调绑定 > 全局 > pending） ----

func TestEventCallbackRouting(t *testing.T) {
	s, db := newEventsServer(t)
	ctx := context.Background()
	item := json.RawMessage(`{"type":102,"groupName":"G","groupRemark":"","roomType":1}`)

	// (a) 无任何绑定 → 行保持 pending（禁外网缺省形态）。
	if ok, err := s.HandleDeviceEvent(ctx, "v1:robot-r1", item); err != nil || !ok {
		t.Fatal("应 enqueue")
	}
	wNone, err := NewOutboxWorker(db, OutboxConfig{}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	if err := wNone.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	pending, _ := db.OutboxByStatus(ctx, "pending")
	if len(pending) != 1 {
		t.Fatalf("无绑定应保持 pending: %d", len(pending))
	}

	// (b) per-robot type=11 绑定命中 → 投绑定 URL（非全局）。
	const secret = "route-secret"
	rcv11 := newReceiver(t, secret, 0)
	rcvGlobal := newReceiver(t, secret, 0)
	bindRobotCallback(t, db, "robot-r1", 11, rcv11.srv.URL+"/callback")
	bindRobotCallback(t, db, "robot-other", 11, rcvGlobal.srv.URL+"/callback") // 他 robot 绑定不得串投
	if ok, err := s.HandleDeviceEvent(ctx, "v1:robot-r1", item); err != nil || !ok {
		t.Fatal("应 enqueue")
	}
	w11, err := NewOutboxWorker(db, OutboxConfig{CallbackURL: rcvGlobal.srv.URL + "/callback", CallbackSecret: secret}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	deliverAll(t, w11)
	// 两行（(a) pending 的 + (b) 新 enqueue 的）均属 robot-r1 → 全走 type=11 绑定；
	// 全局 URL 与他 robot 绑定不串投（绑定优先级 > 全局回退）。
	if rcv11.attempts() != 2 || rcvGlobal.attempts() != 0 {
		t.Fatalf("type=11 绑定应优先且不串投: r11=%d global=%d", rcv11.attempts(), rcvGlobal.attempts())
	}

	// (c) query_result 经 commandId 反查 robot → 同一 type=11 路由。
	seedCommand(t, db, "cmd-route-1", "v1:robot-r1")
	if ok, err := s.HandleQueryResult(ctx, "v1:robot-r1", "cmd-route-1",
		json.RawMessage(`{"type":515,"groupName":"G","groupRemark":""}`)); err != nil || !ok {
		t.Fatal("查询推送应 enqueue")
	}
	deliverAll(t, w11)
	if rcv11.attempts() != 3 { // 2 事件行 + 1 查询行，同一 robot 同一绑定
		t.Fatalf("查询推送应走同 robot type=11 绑定: %d", rcv11.attempts())
	}
}

// ---- ⑤ 事件帧旁路分发（gateway 接线入口的注入冒烟） ----

func TestHandleEventFrameItems(t *testing.T) {
	s, db := newEventsServer(t)
	list := json.RawMessage(`[` +
		`{"type":101,"groupName":"G","groupRemark":"","roomType":1,"memberList":["a"]},` +
		`{"type":101,"messageId":"m-1","list":[{"textType":1,"content":"普通消息上报"}]},` +
		`{"type":511,"messageId":"m-2","date":"d","meetingInfoList":[{"meetingName":"周会","meetingTime":"t","actualMeetingTime":"t","orgName":"o"}]},` +
		`{"type":301,"level":"info"},` +
		`{"type":105,"friendName":"f"}]`)
	n, err := s.HandleEventFrameItems(context.Background(), "v1:robot-mix", "cmd-mix", list)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("应 enqueue 3 行（101 事件+511+105；消息上报/日志不动）: %d", n)
	}
	rows, _ := db.QueryDump(context.Background(), `SELECT kind, COUNT(*) FROM outbox GROUP BY kind ORDER BY kind`)
	if len(rows) != 2 || rows[0][0] != "event_callback" || rows[0][1] != "2" || rows[1][0] != "query_result" || rows[1][1] != "1" {
		t.Fatalf("kind 计数漂移: %v", rows)
	}
	// 单对象形态（非数组）容错。
	n, err = s.HandleEventFrameItems(context.Background(), "v1:robot-mix", "", json.RawMessage(`{"type":104,"groupName":"G","roomType":1}`))
	if err != nil || n != 1 {
		t.Fatalf("单对象事件应分发: n=%d err=%v", n, err)
	}
}

// ---- ⑥ 扩展错误码登记（errors-b3.md 全集锁定，防漂移） ----

// TestOpenAPICodeNamesB3FullSet：openapiCodeNames 须与 errors-b3.md 全表
// （29 码 + 成功 0 单列）严格一致——含 APK 独有 101018/201021 与 doc-9564531
// 独有 101016/101017/201017~201020/501xxx；多码少码均视为漂移。
func TestOpenAPICodeNamesB3FullSet(t *testing.T) {
	want := map[int]string{
		101011: "数据格式错误", 101012: "非法操作", 101013: "非法权限", 101014: "指令重复",
		101016: "网络错误", 101017: "客户端异常", 101018: "指令取消",
		201011: "创建群失败", 201012: "群改名失败", 201013: "群拉人失败", 201014: "群踢人失败",
		201015: "改群公告失败", 201016: "改群备注失败", 201017: "改群模板失败",
		201018: "创建群达到上限", 201019: "创建群已存在", 201020: "加好友达到上限", 201021: "群已退出",
		201101: "查找聊天窗失败", 201102: "发送消息失败", 201103: "按钮寻找失败", 201104: "目标寻找失败",
		201105: "转发失败", 201106: "重复添加", 201107: "文件下载异常", 201108: "文件存储异常",
		201109: "二维码获取失败",
		501011: "机器人ID错误", 501012: "机器人Key错误", 501013: "机器人重复登录", 501000: "其他未知错误",
	}
	if len(openapiCodeNames) != len(want) {
		t.Fatalf("错误码映射基数漂移: got %d want %d（errors-b3.md 全集 29+4 服务端码）", len(openapiCodeNames), len(want))
	}
	for code, name := range want {
		if got := openapiCodeNames[code]; got != name {
			t.Fatalf("错误码 %d 名称漂移: got %q want %q", code, got, name)
		}
	}
	// 常量镜像：101018/201021 两枚 APK 独有码须有命名常量（openapi_registry.go）。
	if openapiUpstreamInstrCancelled != 101018 || openapiUpstreamGroupExited != 201021 {
		t.Fatalf("APK 独有码常量漂移: 101018=%d 201021=%d", openapiUpstreamInstrCancelled, openapiUpstreamGroupExited)
	}
	// 101015 为厂商历史跳号（errors-b3.md：APK 与两文档均不存在）——不得登记。
	if _, exists := openapiCodeNames[101015]; exists {
		t.Fatal("101015 为历史跳号，不得登记（errors-b3.md §⑤）")
	}
	// 成功码语义：0 恒成功。
	if openapiCodeSuccess != 0 {
		t.Fatalf("成功码漂移: %d", openapiCodeSuccess)
	}
}
