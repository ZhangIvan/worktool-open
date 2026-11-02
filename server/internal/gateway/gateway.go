// Package gateway 实现 WS 网关（docs/02-ARCHITECTURE.md §4 gateway 行；任务卡 T2.3）：
//
//   - v2 安全会话：POST /api/v2/device-sessions 建 session → WS /api/v2/device-sessions/{id}
//     （hello 协商 / 逐帧 HMAC 验签 / seq 严格递增 / eventId 去重 / encryptType=2 GCM）；
//   - v1 兼容路径：/webserver/wework/{robotId}（信封解码，透传统一命令管线；
//     robotId 即身份的弱鉴权是已知事实，spec §2/audit-02 §A.2——仅兼容迁移面）；
//   - 凭据管理：deviceToken 服务端仅存 SHA-256 hash（spec §5.1.3）；原始 token
//     仅存活于进程内存的 LiveCred 注册表——重启后凭据失忆，设备经 §5.5.1
//     Ed25519 重协商签名换发新凭据（本实现将该机制同时用作重启恢复路径）。
package gateway

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// Logger 为最小日志接口（标准 log 与测试桩皆可注入；零遥测，G3）。
type Logger interface {
	Printf(format string, args ...any)
}

// Hub 为网关中枢：会话注册表 + 活动凭据 + 调度泵。
type Hub struct {
	DB       *sqlite.DB
	Pipeline *scheduler.Service
	Machine  *statemachine.Machine
	Now      func() time.Time
	Log      Logger

	mu      sync.Mutex
	creds   map[string]*LiveCred  // deviceID → 活动凭据（原始 token 仅内存）
	v2      map[string]*v2Session // sessionID → 会话
	v2ByDev map[string]*v2Session // deviceID → 当前活动会话（一设备一会话）
	v1      map[string]*v1Conn    // robotId → v1 兼容连接

	// flushMu 串行化“取 queued 批→发送→E03 留痕”整段（hello 冲刷/调度泵/
	// 显式 DispatchQueued 并发时防止同一指令被取走两次——双发帧 + 第二次
	// MarkDispatched 409；T3.2 修复的既有竞态）。
	flushMu sync.Mutex

	upgrader websocket.Upgrader
}

// LiveCred 为进程内活动凭据（原始 deviceToken 绝不落库，spec §5.1.3）。
type LiveCred struct {
	DeviceID     string
	TokenHex     string
	TokenVersion int64
}

// New 创建网关中枢。
func New(db *sqlite.DB, pipeline *scheduler.Service, m *statemachine.Machine, logger Logger) *Hub {
	if logger == nil {
		logger = log.Default()
	}
	return &Hub{
		DB:       db,
		Pipeline: pipeline,
		Machine:  m,
		Now:      time.Now,
		Log:      logger,
		creds:    map[string]*LiveCred{},
		v2:       map[string]*v2Session{},
		v2ByDev:  map[string]*v2Session{},
		v1:       map[string]*v1Conn{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4 << 10,
			WriteBufferSize: 4 << 10,
			// 明文 loopback 原型 + TLS 对外部署（SR-09）；设备鉴权由逐帧 HMAC
			// （v2）与后续 admin 白名单（v1，T3.x）承担，WS 层不做 Origin 校验。
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// ---- 凭据注册表 ----

func (h *Hub) putCred(c *LiveCred) {
	h.mu.Lock()
	h.creds[c.DeviceID] = c
	h.mu.Unlock()
}

// PutCred 登记/覆盖活动凭据（配对/重协商换发后由 api 层调用）。
func (h *Hub) PutCred(c *LiveCred) { h.putCred(c) }

func (h *Hub) getCred(deviceID string) *LiveCred {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.creds[deviceID]
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// DropCred 使凭据失忆（撤销/测试用）。
func (h *Hub) DropCred(deviceID string) {
	h.mu.Lock()
	delete(h.creds, deviceID)
	h.mu.Unlock()
}

// HasLiveCred 报告设备当前是否有活动凭据（测试/诊断）。
func (h *Hub) HasLiveCred(deviceID string) bool { return h.getCred(deviceID) != nil }

// ---- 调度泵：把 queued 指令推到在线端点 ----

// DispatchQueued 对目标端点做一次调度：v2 会话在线走加密鉴权通道，v1 连接在线
// 走兼容信封；均不在线则指令留队（会话建立时补发）。
func (h *Hub) DispatchQueued(ctx context.Context, target string) (int, error) {
	h.mu.Lock()
	s2 := h.v2ByDev[target]
	c1 := h.v1[target]
	if c1 == nil {
		if robotID, ok := v1RobotID(target); ok {
			c1 = h.v1[robotID] // v1 连接按 robotId 登记，目标键为 "v1:robotId"
		}
	}
	h.mu.Unlock()
	if s2 != nil {
		return s2.flushQueued(ctx, h)
	}
	if c1 != nil {
		return c1.flushQueued(ctx)
	}
	return 0, nil
}

// v1RobotID 从统一目标键还原 robotId（"v1:robotId"）。
func v1RobotID(target string) (string, bool) {
	if rest, ok := strings.CutPrefix(target, "v1:"); ok {
		return rest, true
	}
	return "", false
}

// DispatchAllQueued 扫描全部在线端点（周期泵入口）。
func (h *Hub) DispatchAllQueued(ctx context.Context) (int, error) {
	h.mu.Lock()
	targets := make(map[string]bool, len(h.v2ByDev)+len(h.v1))
	for t := range h.v2ByDev {
		targets[t] = true
	}
	for t := range h.v1 {
		targets[t] = true
	}
	h.mu.Unlock()
	total := 0
	for t := range targets {
		n, err := h.DispatchQueued(ctx, t)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// RunDispatchPump 周期调度（提交后最迟一个周期内下发；会话建立时即时冲刷）。
func (h *Hub) RunDispatchPump(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := h.DispatchAllQueued(ctx); err != nil {
				h.Log.Printf("gateway: dispatch pump: %v", err)
			}
		}
	}
}

// ---- v2 出站签名帧构造 ----

// buildSignedFrame 构造一帧已签名的 v2 出站信封；seq 在此处严格递增分配。
// 设备能力 encryptTypeMax≥2 且载荷非空时自动走 AES-256-GCM（spec §5.3）。
func (s *v2Session) buildSignedFrame(h *Hub, socketType int, list json.RawMessage, commandID string) (*wire.Envelope, *wire.FrameMeta, error) {
	now := h.Now().UTC()
	s.OutSeq++
	seq := s.OutSeq
	eventID := wire.NewUUID()
	nonce, err := wire.RandomHex(16)
	if err != nil {
		return nil, nil, err
	}
	env := &wire.Envelope{
		SocketType:  socketType,
		MessageID:   wire.BuildMessageID(now.UnixMilli(), eventID),
		APISend:     intPtr(0),
		EncryptType: 0,
	}
	var digest string
	if s.EncryptMax >= 2 && len(list) > 0 {
		key, err := s.sessionKey()
		if err != nil {
			return nil, nil, err
		}
		sealed, err := wire.SealList(key, wire.GCMNonce(eventID), wire.GCMAAD(seq, eventID, now.UnixMilli()), normalizeList(list))
		if err != nil {
			return nil, nil, err
		}
		env.EncryptType = 2
		env.EncryptedList = sealed
		digest, err = wire.PayloadDigest(2, nil, sealed)
		if err != nil {
			return nil, nil, err
		}
	} else {
		list = normalizeList(list)
		env.List = list
		digest, err = wire.PayloadDigest(0, list, "")
		if err != nil {
			return nil, nil, err
		}
	}
	meta := wire.FrameMeta{
		Ver: 2, Ts: now.UnixMilli(), Nonce: nonce,
		Seq: seq, EventID: eventID, CommandID: commandID, TokenVersion: s.TokenVersion,
	}
	mac, err := wire.ComputeHMAC(s.TokenHex, commandID, eventID, seq, meta.Ts, nonce, digest)
	if err != nil {
		return nil, nil, err
	}
	meta.HMAC = mac
	env.MetaRaw = mustJSON(meta)
	return env, &meta, nil
}

// normalizeList 确保出站 list 为规范 JSON 数组文本。
func normalizeList(list json.RawMessage) json.RawMessage {
	if len(list) == 0 {
		return json.RawMessage(`[]`)
	}
	return list
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func intPtr(i int) *int { return &i }

// closeWith 以 v2 关闭码关闭（spec §5.2.5）。
func closeWith(mu *sync.Mutex, conn *websocket.Conn, code int, reason string) {
	mu.Lock()
	defer mu.Unlock()
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
}

// wsWriteDeadline 单帧写上限（CR-t32-T3.2-R4）：卡死/恶意客户端 TCP 零窗口停读时
// WriteMessage 可无限期阻塞——写锁（会话 writeMu/Hub flushMu）被长期持有会放大为
// 全部设备的调度泵停摆。超时即判连接失效（写错误走既有关闭路径）。
// var 而非 const：慢客户端回归测试注入短值，避免真等 10s。
var wsWriteDeadline = 10 * time.Second

// writeRaw 线程安全写原始文本帧（写前设 deadline，见 [wsWriteDeadline]）。
func writeRaw(mu *sync.Mutex, conn *websocket.Conn, data []byte) error {
	mu.Lock()
	defer mu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
	return conn.WriteMessage(websocket.TextMessage, data)
}
