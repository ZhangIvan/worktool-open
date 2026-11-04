/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package gateway_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"cn.worktool.open/server/internal/wire"
)

// TestEventBypassQueryPush（W-M6 接线）：设备 socketType=2 帧 → 网关旁路
// HandleEventFrameItems → 512 查询推进入回调 outbox（kind=query_result）；
// socketType=3 结果帧不触发旁路（分发器自过滤，无双发）。
func TestEventBypassQueryPush(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	c, _ := openV2Session(t, a, m, 2, 0, 0, 1)

	outboxQueryRows := func() int {
		rows, err := a.db.QueryDump(t.Context(), `SELECT COUNT(*) FROM outbox WHERE kind='query_result'`)
		if err != nil || len(rows) != 1 {
			t.Fatalf("outbox count: %v rows=%d", err, len(rows))
		}
		n, _ := strconv.Atoi(rows[0][0])
		return n
	}

	// ① socketType=2 帧：512 群成员推送（合法最小形状）
	push512, _ := json.Marshal(map[string]any{
		"type":       512,
		"groupName":  "交付群",
		"memberList": []string{"张三", "李四"},
	})
	list := json.RawMessage(`[` + string(push512) + `]`)
	c.send(c.buildFrame(wire.SocketMessageList, list, "7e1a1111-2222-3333-4444-555555555551", nil))
	waitFor(t, 3*time.Second, func() (bool, string) {
		if outboxQueryRows() >= 1 {
			return true, ""
		}
		return false, ""
	})

	// ② socketType=3 结果帧：不新增 query_result 行（旁路不消费结果类）
	before := outboxQueryRows()
	resultItem, _ := json.Marshal(map[string]any{
		"type": 203, "errorCode": 0, "errorReason": "",
		"metaJson": `{"dispatchId":"7e1a1111-2222-3333-4444-555555555552"}`,
	})
	c.send(c.buildFrame(wire.SocketRawConfirm, json.RawMessage(`[`+string(resultItem)+`]`), "7e1a1111-2222-3333-4444-555555555552", nil))
	time.Sleep(300 * time.Millisecond)
	if after := outboxQueryRows(); after != before {
		t.Fatalf("结果帧不应触发查询推送 outbox：before=%d after=%d", before, after)
	}
}
