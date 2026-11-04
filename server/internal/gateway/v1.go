// v1 兼容路径 /webserver/wework/{robotId}（spec §3/§4，上游 v2.8.1 报文互通）：
// 信封解码后透传统一命令管线。鉴权薄弱（robotId 即身份）是上游已知事实，
// 本路径仅用于兼容与迁移（spec §2）；v1 心跳 5s、裸 {"type":11}、首帧 {"td":ms}
// 均须接受（上游还原）。
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// v1Target 由 robotId 构造统一管线目标键。
func v1Target(robotID string) string { return "v1:" + robotID }

// v1Conn 为一条 v1 兼容连接。
type v1Conn struct {
	hub     *Hub
	robotID string
	target  string

	mu      sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex
	pending map[string]pendingRef // messageId → 下发引用（结果/确认回配）
	ordered []string              // 已下发 messageId 顺序（结果回配兜底）
	closed  bool
}

// pendingRef 为 v1 下发指令的服务端侧引用（v1 报文无 commandId 字段可还原，
// 服务端以 messageId 回配；结果回执兜底按指令类型 + 下发顺序匹配）。
type pendingRef struct {
	commandID   string
	commandType int
}

// HandleV1 处理 GET /webserver/wework/{robotId}（v1 兼容 WS）。
func (h *Hub) HandleV1(w http.ResponseWriter, r *http.Request) {
	robotID := r.PathValue("robotId")
	if robotID == "" || strings.ContainsAny(robotID, "/\\") {
		http.Error(w, `{"error":"INVALID_ROBOT_ID"}`, http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	target := v1Target(robotID)
	// v1 设备自动登记（robotId 即身份；无配对面）。v2 设备携 robot_id 的以
	// 配对身份关联（device 行合并语义：优先既有行）。
	dev, ok, err := h.DB.GetDeviceByRobot(ctx, robotID)
	if err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	if !ok {
		now := h.Now().UTC()
		if err := h.DB.UpsertDevice(ctx, sqlite.Device{
			DeviceID: target, RobotID: robotID, DeviceName: "v1:" + robotID,
			Status: "active", PairedAt: now, UpdatedAt: now,
		}); err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}
	} else if dev.DeviceID != target {
		// 已配对设备的 robotId：目标键统一指向配对身份（v1/v2 同管线同设备）
		target = dev.DeviceID
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(wsReadLimit) // H-2 OOM 面：升级后即设单帧读上限（1 MiB）
	c := &v1Conn{
		hub: h, robotID: robotID, target: target,
		conn: conn, pending: map[string]pendingRef{},
	}
	// 同 robotId 旧连接被顶替（上游 501013 多端互踢语义；v1 面无服务端实现可还原，
	// 此处以“新连接顶旧连接”承载，spec §3 错误码职责行）
	h.mu.Lock()
	if old := h.v1[robotID]; old != nil && old != c {
		old.closeLocal()
	}
	h.v1[robotID] = c
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.v1[robotID] == c {
			delete(h.v1, robotID)
		}
		h.mu.Unlock()
	}()

	// 建连即冲刷积压指令（设备不在线时留队的在此补发）
	if _, err := c.flushQueued(ctx); err != nil {
		h.Log.Printf("gateway: v1 flushQueued: %v", err)
	}

	for {
		// v1 心跳 5s；无上游服务端超时语义可还原——仅设宽松读超时防悬挂
		_ = conn.SetReadDeadline(h.Now().Add(10 * time.Minute))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		if stop := c.handleFrame(ctx, data); stop {
			// 版本混用等终止性错误：显式关闭后退出读循环
			closeWith(&c.writeMu, conn, websocket.CloseNormalClosure, "v1 session terminated")
			return
		}
	}
}

// handleFrame 分发一帧 v1 消息；返回 true 表示连接应终止。
func (c *v1Conn) handleFrame(ctx context.Context, data []byte) bool {
	var peek map[string]json.RawMessage
	if err := json.Unmarshal(data, &peek); err != nil {
		c.hub.Log.Printf("gateway: v1 JSON 解析失败 robot=%s: %v", c.robotID, err)
		return false // 上游噪音不撕裂连接
	}
	if td, ok := peek["td"]; ok && len(peek) == 1 {
		_ = td // onOpen 时间戳（上游还原）：接受，无应答语义可还原
		return false
	}
	if len(peek) == 1 {
		if _, isType := peek["type"]; isType {
			return false // 裸 {"type":11} 心跳（spec §4.2：须接受）
		}
	}
	env, err := wire.DecodeEnvelope(data)
	if err != nil {
		c.hub.Log.Printf("gateway: v1 信封解析失败 robot=%s: %v", c.robotID, err)
		return false
	}
	if env.IsV2() {
		c.hub.Log.Printf("gateway: v1 路径拒绝 v2 帧（meta 对象）robot=%s", c.robotID)
		return true // 同一设备会话只能是一种版本（spec §2）
	}
	if err := env.ValidateV1(); err != nil {
		c.hub.Log.Printf("gateway: v1 帧校验失败 robot=%s: %v", c.robotID, err)
		return false // 兼容面宽容：坏帧丢弃不撕裂
	}
	now := c.hub.Now().UTC()
	switch env.SocketType {
	case wire.SocketHeartbeat:
		return false
	case wire.SocketConfirm:
		// 消息确认：messageId 回配（v1 确认幂等 30s 窗口在上游客户端，服务端按幂等边处理）
		if cmdID := c.pendingCommand(env.MessageID); cmdID != "" {
			if err := c.hub.Pipeline.HandleTransportAck(ctx, cmdID, "v1-confirm:"+env.MessageID); err != nil {
				c.hub.Log.Printf("gateway: v1 confirm %s: %v", cmdID, err)
			}
		}
		return false
	case wire.SocketRawConfirm:
		c.handleResult(ctx, env, now)
		return false
	case wire.SocketMessageList:
		items, err := env.ListItems()
		if err != nil {
			c.hub.Log.Printf("gateway: v1 list 解析失败: %v", err)
			return false
		}
		for _, item := range items {
			c.handleReportItem(ctx, item, now)
		}
		return false
	default:
		return false
	}
}

// handleResult 处理 socketType=3 执行结果（RAW_CONFIRM）。
func (c *v1Conn) handleResult(ctx context.Context, env *wire.Envelope, now time.Time) {
	items, err := (&wire.Envelope{List: env.List}).ListItems()
	if err != nil || len(items) == 0 {
		c.hub.Log.Printf("gateway: v1 RAW_CONFIRM 无载荷 robot=%s", c.robotID)
		return
	}
	for _, item := range items {
		if !wire.IsExecResult(item) {
			continue
		}
		r, err := wire.DecodeExecResult(item)
		if err != nil {
			continue
		}
		commandID := dispatchIDFromMetaJSON(r.MetaJSON)
		if commandID == "" {
			commandID = c.pendingCommand(env.MessageID)
		}
		if commandID == "" {
			// 兜底：该连接最早未决的同类型指令（v1 报文无 commandId 字段可还原）
			commandID = c.oldestPendingOfType(r.Type)
		}
		if commandID == "" {
			c.hub.Log.Printf("gateway: v1 结果无法回配指令 robot=%s type=%d", c.robotID, r.Type)
			continue
		}
		if _, err := c.hub.Pipeline.HandleResult(ctx, commandID, r); err != nil {
			c.hub.Log.Printf("gateway: v1 结果处理 %s: %v", commandID, err)
		}
		c.forgetPending(commandID)
	}
	_ = now
}

// handleReportItem 处理上行上报（101/301/302；审计 + outbox 仅元数据）。
func (c *v1Conn) handleReportItem(ctx context.Context, item json.RawMessage, now time.Time) {
	switch t := wire.ListItemType(item); t {
	case wire.TypeReceiveMessageList, wire.TypeRobotLog, wire.TypeRobotErrorLog:
		kind := "report"
		if t != wire.TypeReceiveMessageList {
			kind = "device_log"
		}
		tx, txErr := c.hub.DB.Tx(ctx)
		if txErr == nil {
			_ = c.hub.DB.InsertAudit(ctx, tx, kind, c.target, string(truncateForAudit(item, 512)), now)
			_ = c.hub.DB.EnqueueOutbox(ctx, tx, kind, "", map[string]any{
				"kind": kind, "robotId": c.robotID, "reportType": t, "at": now.UnixMilli(),
			}, now)
			_ = tx.Commit()
		}
	default:
		// 其余上行类型：T2.3 不消费（指令下发/结果回收为主链路）
	}
}

// flushQueued 把目标 queued 指令以 v1 信封下发并落 E03（取批→发送→留痕整段
// 持 Hub 级 flush 锁，防并发双发，见 Hub.flushMu 注释）。
func (c *v1Conn) flushQueued(ctx context.Context) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, nil
	}
	c.hub.flushMu.Lock()
	defer c.hub.flushMu.Unlock()
	cmds, err := c.hub.Pipeline.QueuedForTarget(ctx, c.target, 64)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, cmd := range cmds {
		epoch, err := c.hub.DB.BumpEpoch(ctx, c.target)
		if err != nil {
			return n, err
		}
		eventID := wire.NewUUID()
		messageID := wire.BuildMessageID(c.hub.Now().UTC().UnixMilli(), eventID)
		env := &wire.Envelope{
			SocketType:  wire.SocketMessageList,
			MessageID:   messageID,
			MetaRaw:     mustJSON(""),
			APISend:     intPtr(1),
			EncryptType: 0,
			List:        json.RawMessage(`[` + cmd.Payload + `]`),
		}
		if err := writeRaw(&c.writeMu, c.conn, mustJSON(env)); err != nil {
			return n, err
		}
		if err := c.hub.Pipeline.MarkDispatched(ctx, cmd.CommandID, epoch, 0, eventID); err != nil {
			return n, err
		}
		c.mu.Lock()
		c.pending[messageID] = pendingRef{commandID: cmd.CommandID, commandType: cmd.CommandType}
		c.ordered = append(c.ordered, messageID)
		c.mu.Unlock()
		n++
	}
	return n, nil
}

func (c *v1Conn) pendingCommand(messageID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ref, ok := c.pending[messageID]; ok {
		return ref.commandID
	}
	return ""
}

// oldestPendingOfType 返回最早仍 pending 且类型匹配的指令（v1 结果回配兜底）。
func (c *v1Conn) oldestPendingOfType(commandType int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, mid := range c.ordered {
		if ref, ok := c.pending[mid]; ok && ref.commandType == commandType {
			return ref.commandID
		}
	}
	return ""
}

func (c *v1Conn) forgetPending(commandID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for mid, ref := range c.pending {
		if ref.commandID == commandID {
			delete(c.pending, mid)
		}
	}
}

func (c *v1Conn) closeLocal() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	closeWith(&c.writeMu, c.conn, websocket.CloseNormalClosure, "superseded")
}
