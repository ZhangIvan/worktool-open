package gateway_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"cn.worktool.open/server/internal/scheduler"
)

// 管理面暂停门（派发侧收口）：devices.status='paused' 的设备，flushQueued
// 不得投递已排队指令——此前管理面只拦新提交（409 DEVICE_PAUSED），已排队
// 指令仍被派发泵/hello 冲刷照发。resume 后恢复派发。
// 审计防刷屏：pause_skip_dispatch 每设备每轮至多一条（跳过即终止本轮冲刷）。
func TestV2FlushSkipsPausedDevice(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	c, _ := openV2Session(t, a, m, 0, 0, 0, 1)

	ctx := context.Background()
	setDeviceStatus := func(status string) {
		t.Helper()
		if _, err := a.db.Exec(ctx, `UPDATE devices SET status=?, updated_at=? WHERE device_id=?`,
			status, time.Now().UTC().UnixMilli(), m.deviceID); err != nil {
			t.Fatalf("set devices status=%s: %v", status, err)
		}
	}
	auditCount := func() int {
		t.Helper()
		rows, err := a.db.QueryDump(ctx,
			`SELECT COUNT(*) FROM audit_log WHERE kind='pause_skip_dispatch' AND subject=?`, m.deviceID)
		if err != nil || len(rows) == 0 {
			t.Fatalf("audit 查询失败: %v", err)
		}
		n, _ := strconv.Atoi(rows[0][0])
		return n
	}

	// paused + 空队列：无可跳过对象，不落审计（防刷屏）
	setDeviceStatus("paused")
	if n, err := a.hub.DispatchQueued(ctx, m.deviceID); err != nil || n != 0 {
		t.Fatalf("paused 空队列冲刷应 no-op: n=%d err=%v", n, err)
	}
	if got := auditCount(); got != 0 {
		t.Fatalf("paused 空队列不应落 pause_skip_dispatch 审计, got %d", got)
	}

	// 入队 2 条 → paused 冲刷零派发（无 E03、指令留队）
	ids := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		r, err := a.pipeline.Submit(ctx, scheduler.SubmitRequest{
			Target: m.deviceID, CommandType: 203,
			Payload: `{"type":203,"targetRoom":"外部群测试群","receivedContent":"paused` + strconv.Itoa(i) + `"}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.CommandID)
	}
	if n, err := a.hub.DispatchQueued(ctx, m.deviceID); err != nil || n != 0 {
		t.Fatalf("paused 冲刷应零派发: n=%d err=%v", n, err)
	}
	for _, id := range ids {
		cmd, ok, _ := a.db.GetCommand(ctx, id)
		if !ok || cmd.State != "queued" {
			t.Fatalf("paused 设备指令 %s 状态 = %v（应留队 queued）", id, cmd.State)
		}
		assertNoE03(t, a, id)
	}
	// 每设备每轮至多一条审计：2 条 queued 单轮只落 1 条（非逐指令刷屏）
	if got := auditCount(); got != 1 {
		t.Fatalf("2 条 queued 单轮冲刷应恰好 1 条 pause_skip_dispatch, got %d", got)
	}

	// 第二轮冲刷（仍 paused）→ 再落 1 条（每轮一条），指令仍留队
	if n, err := a.hub.DispatchQueued(ctx, m.deviceID); err != nil || n != 0 {
		t.Fatalf("paused 第二轮冲刷应零派发: n=%d err=%v", n, err)
	}
	if got := auditCount(); got != 2 {
		t.Fatalf("两轮冲刷应共 2 条审计（每轮至多一条）, got %d", got)
	}
	for _, id := range ids {
		assertNoE03(t, a, id)
	}

	// resume → 恢复派发（2 条全部下发 + E03 留痕）
	setDeviceStatus("active")
	if n, err := a.hub.DispatchQueued(ctx, m.deviceID); err != nil || n != 2 {
		t.Fatalf("resume 后应恢复派发 2 条: n=%d err=%v", n, err)
	}
	for i, id := range ids {
		cmd, ok, _ := a.db.GetCommand(ctx, id)
		if !ok || cmd.State != "dispatched" {
			t.Fatalf("resume 后指令 %s 状态 = %v（应 dispatched）", id, cmd.State)
		}
		events, _ := a.db.Events(ctx, id)
		if len(events) == 0 {
			t.Fatalf("resume 后指令 %s 应有 E03 事件", id)
		}
		// 设备侧帧可读（hello_ack=1，指令帧自 seq=2 起连续）
		env := c.readFrame()
		if meta, _ := env.V2Meta(); meta.Seq != int64(i+2) {
			t.Fatalf("指令帧 seq = %d, want %d", meta.Seq, i+2)
		}
		plain := c.verifyServerFrame(env, "")
		var items []json.RawMessage
		if err := json.Unmarshal(plain, &items); err != nil || len(items) != 1 {
			t.Fatalf("指令载荷应为单元数组: %s", plain)
		}
	}
}

// assertNoE03 断言指令事件流无 dispatch 边（E01 入队边来自 Submit 本身，为基线）。
func assertNoE03(t *testing.T, a *testApp, commandID string) {
	t.Helper()
	events, err := a.db.Events(context.Background(), commandID)
	if err != nil {
		t.Fatalf("事件流查询: %v", err)
	}
	for _, e := range events {
		if e.EdgeID == "E03" {
			t.Fatalf("paused 设备指令 %s 不得产生 E03 dispatch 留痕，事件流: %+v", commandID, events)
		}
	}
}
