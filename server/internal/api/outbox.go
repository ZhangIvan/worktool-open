// 回调 outbox worker（docs/02-ARCHITECTURE.md §4 api 行；FR-35）：
// 结果落库同事务 enqueue（scheduler 侧已保证）→ 本 worker 投递：
//   - HMAC-SHA256 签名（X-WT-Signature: hex(HMAC(secret, "ts." + body))，防重放时间戳）；
//   - 重试 1s/2s/4s（默认），第 4 次尝试失败 → 死信（status=dead + 审计）；
//   - 默认禁外网：回调 URL 仅允许 loopback 接收器（SR-09 同型断言），
//     显式 allowNonLoopback=true 才放开（部署侧须 TLS，SR-01）；
//   - T3.6 apifox 兼容（docs/15）：K3 exec_result 分发 body 双名别名
//     （messageId/type:int/rawMsg/runTime，落库 payload 零迁移）；K5 投递响应
//     type:5000 同步代回复（3s 窗口/单次不重试，走 FR-53 门链）。
package api

import (
	"bytes"
	"log"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// DefaultRetryDelays 默认重试间隔（1/2/4s；首次立即，共 4 次尝试，第 4 次失败死信）。
var DefaultRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// OutboxConfig 为回调分发配置。
type OutboxConfig struct {
	CallbackURL      string // 空 = 不投递（outbox 行保持 pending）
	CallbackSecret   string // HMAC 密钥
	AllowNonLoopback bool   // 默认 false：仅 loopback 接收器
	RetryDelays      []time.Duration
	PollInterval     time.Duration // 轮询间隔（默认 250ms）
	Client           *http.Client  // 测试注入
	// CompatAliases 为 K3 回调 body 双名开关（T3.6）：true = exec_result 分发
	// body 注入文档 ExecCallbackBean 别名字段（messageId/type:int/rawMsg/runTime/
	// groupName/qrCode）；false = 旧裸形态（测试/显式 opt-out）。**nil = true**
	//（默认开——指针形态兼顾 Go 零值与「默认 true」两义；docs/15 破坏级 #3）。
	CompatAliases *bool
}

// OutboxWorker 为回调分发工作器。
type OutboxWorker struct {
	db     *sqlite.DB
	cfg    OutboxConfig
	client *http.Client
	now    func() time.Time
	log    interface{ Printf(string, ...any) }
	// reply 为 K5 type:5000 代回复的指令管线引用（nil = 能力关闭；由
	// Server.wireOutboxCompat 装配注入——worker 与 Server 独立构造，装配序 =
	// main 先挂 srv.Outbox 再 NewServeMux）。
	reply *scheduler.Service
	// warnedNoReceiver：无接收器一次性告警（状态翻转重复告警抑制）。
	warnedNoReceiver bool
}

// SetReplyPipeline 注入代回复指令管线（K5；不绕过 FR-53——复用同一管线实例，
// 熔断/分级/审批门全部生效）。
func (w *OutboxWorker) SetReplyPipeline(p *scheduler.Service) { w.reply = p }

// compatAliasesOn 报告 K3 双名开关（nil = 默认 true）。
func (w *OutboxWorker) compatAliasesOn() bool {
	return w.cfg.CompatAliases == nil || *w.cfg.CompatAliases
}

// NewOutboxWorker 创建 worker；URL 非 loopback 且未显式放行 → 返回禁用错误
// （启动即拒绝式样，与 config SR-09 断言同型）。
func NewOutboxWorker(db *sqlite.DB, cfg OutboxConfig, logger interface{ Printf(string, ...any) }) (*OutboxWorker, error) {
	if logger == nil {
		logger = log.Default() // 测试直构容错（nil logger 不再 panic）
	}
	if cfg.RetryDelays == nil {
		cfg.RetryDelays = DefaultRetryDelays
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.CallbackURL != "" {
		u, err := url.Parse(cfg.CallbackURL)
		if err != nil {
			return nil, fmt.Errorf("api/outbox: 回调 URL 非法: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("api/outbox: 回调 URL 仅支持 http(s): %s", u.Scheme)
		}
		if !cfg.AllowNonLoopback && !isLoopbackHostURL(u) {
			return nil, fmt.Errorf("api/outbox: 默认禁外网——回调 URL %s 非 loopback 接收器；如确需外网须显式 allowNonLoopback（部署侧强制 TLS，SR-01/09）", u.Host)
		}
	}
	client := cfg.Client
	if client == nil {
		// 重定向纪律（REV-SRV-01 / SR-09）：投递绝不跟随 3xx——loopback 接收器
		// 返回 307/308 指向外网时，Go 默认跟随会把 POST body+HMAC 签名原样重放
		// 到外网地址。禁随后 30x 终态按非 2xx 处理（retry → 死信），body 不出 loopback。
		client = &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &OutboxWorker{db: db, cfg: cfg, client: client, now: time.Now, log: logger}, nil
}

func isLoopbackHostURL(u *url.URL) bool {
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Sign 计算 X-WT-Signature：hex(HMAC-SHA256(secret, "<tsMilli>." + body))。
func Sign(secret string, tsMillis int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", tsMillis)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySign 供接收器校验（loopback 测试接收器复用）。
func VerifySign(secret string, tsMillis int64, body []byte, signatureHex string) bool {
	return hmac.Equal([]byte(Sign(secret, tsMillis, body)), []byte(signatureHex))
}

// errCallbackSecretMissing 投递启用前置未满足（空签名密钥）——哨兵错误：行保持
// pending，部署侧配置 -callback-secret 后自动恢复（不终态化任何行）。
var errCallbackSecretMissing = errors.New("api/outbox: 签名密钥未配置——拒绝投递（行保持 pending；经 -callback-secret 配置后恢复）")

// Run 启动投递循环（周期 claim 待投递行 → POST → 状态机：delivered/retry/dead）。
func (w *OutboxWorker) Run(ctx context.Context) error {
	w.log.Printf("api/outbox: worker 启动 poll=%v global_url=%q", w.cfg.PollInterval, w.cfg.CallbackURL)
	tk := time.NewTicker(w.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
			if err := w.DeliverDue(ctx); err != nil {
				w.log.Printf("api/outbox: %v", err)
			}
		}
	}
}

// DeliverDue 投递一批到期行（幂等可重入；测试可直接调用）。
// 投递目标解析优先级：per-robot 回调绑定（openapi_callbacks type=1 指令结果，
// apifox 兼容面 CRUD 登记处）> 部署级全局 CallbackURL；两者皆空 → 行保持
// pending（禁外网缺省形态不变）。
func (w *OutboxWorker) DeliverDue(ctx context.Context) error {
	if w.cfg.CallbackURL == "" {
		has, err := w.hasRobotCallbacks(ctx)
		if err != nil {
			return err // 查询失败：本轮放弃判定（行不动，错误可见）
		}
		if !has {
			if !w.warnedNoReceiver {
				w.warnedNoReceiver = true
				w.log.Printf("api/outbox: 无任何回调接收器（全局 URL 空且 openapi_callbacks 空）——行保持 pending（一次性告警）")
			}
			return nil // 未配置任何接收器：行保持 pending（禁外网缺省形态）
		}
	}
	w.warnedNoReceiver = false
	// 补充审计（2026-11-04）：投递启用前置——签名密钥必须有效（空密钥=未鉴权
	// 明文投递，拒绝；行保持 pending，部署侧经 -callback-secret 配置后自动恢复）。
	if w.cfg.CallbackSecret == "" {
		return errCallbackSecretMissing
	}
	now := w.now().UTC()
	items, err := w.db.DueOutbox(ctx, now, 32)
	if err != nil {
		return err
	}
	if len(items) > 0 {
		w.log.Printf("api/outbox: claim %d 行待投递", len(items))
	}
	for _, it := range items {
		target, terr := w.callbackURLFor(ctx, it)
		if terr != nil {
			// 解析链查询失败（补充审计：不得静默终态化）——本行保持 pending 下轮重试
			w.log.Printf("api/outbox: 行 id=%d kind=%s 目标解析失败（保持 pending）: %v", it.ID, it.Kind, terr)
			continue
		}
		if target == "" {
			// W-M6 回调闭环根因修复：无投递目标（该 kind 对应回调域未配置且无全局
			// URL）→ **终态 skipped** 而非永久 pending——原语义下存量行按 id 序
			// 占满 claim 窗口（LIMIT 32）饿死后续可投递行（S1 实证：exec_result
			// 无 type=1 绑定的存量行 pending 至今，179/181 query_result 永不被
			// claim）。E7「未配置=仅回执不推送」按行终态化承接。
			it.Status = "skipped"
			it.LastError = "no_receiver: 该 kind 对应回调类型未配置且全局 URL 空"
			it.UpdatedAt = now
			if err := w.db.UpdateOutbox(ctx, it); err != nil {
				return err
			}
			w.log.Printf("api/outbox: 行 id=%d kind=%s 无接收器 → skipped（终态，E7 仅回执不推送）", it.ID, it.Kind)
			continue
		}
		if err := w.deliverOne(ctx, it, now); err != nil {
			return err
		}
	}
	return nil
}

// hasRobotCallbacks 是否存在任意 per-robot 回调绑定（全局 URL 未配置时的
// 唯一投递来源探测；一次 COUNT 查询）。
func (w *OutboxWorker) hasRobotCallbacks(ctx context.Context) (bool, error) {
	rows, err := w.db.QueryDump(ctx, `SELECT COUNT(*) FROM openapi_callbacks`)
	if err != nil {
		// 分流（补充审计）：缺表=老库结构性未装配兼容面（确定无绑定，非故障）；
		// 其余查询失败≠未配置——上抛由 Run 记日志（行不动，不静默降级）。
		if strings.Contains(err.Error(), "no such table") {
			return false, nil
		}
		return false, fmt.Errorf("api/outbox: 接收器探测查询失败: %w", err)
	}
	if len(rows) != 1 {
		return false, nil // 表未建（老库未装配兼容面）→ 视为无绑定
	}
	n, _ := strconv.Atoi(rows[0][0])
	return n > 0, nil
}

// callbackURLFor 解析本行的投递目标（按 kind 分域；T3.5 事件面扩展）：
//   - exec_result 行：其 robot 登记了 type=1 指令结果回调绑定 → 该 URL；
//   - query_result 行（511~515 查询结果推送，events.md §五「机器人配置的消息回调
//     地址」）：commandID 反查 robot → type=11 消息回调绑定（qaUrl 语义）；
//   - event_callback / image_callback / message_push 行（101~105 被动事件 /
//     图片回调 / type=11 文本消息推送 T3.6 K4）：CommandID=管线目标键 → robot
//     → type=11 消息回调绑定；
//   - 各域绑定未命中或无 robot → 部署级全局 URL（可能为空 = 本行不投递，
//     行保持 pending；E7 未配置=仅回执不推送的双轨语义）。
func (w *OutboxWorker) callbackURLFor(ctx context.Context, it sqlite.OutboxItem) (string, error) {
	// 错误=解析链查询失败（调用方保持 pending 重试）；空串=查询成功且确定无绑定
	// （skipped 终态语义只对后者成立——补充审计分流）。
	switch it.Kind {
	case "exec_result":
		if it.CommandID == "" {
			return w.cfg.CallbackURL, nil
		}
		robotID, err := w.robotIDForCommand(ctx, it.CommandID)
		if err != nil {
			return "", err
		}
		if u, err := w.robotCallbackURL(ctx, robotID, 1); err == nil && u != "" {
			return u, nil
		} else if err != nil {
			return "", err
		}
		return w.cfg.CallbackURL, nil
	case outboxKindQueryResult:
		if it.CommandID == "" {
			return w.cfg.CallbackURL, nil
		}
		robotID, err := w.robotIDForCommand(ctx, it.CommandID)
		if err != nil {
			return "", err
		}
		if u, err := w.robotCallbackURL(ctx, robotID, 11); err == nil && u != "" {
			return u, nil
		} else if err != nil {
			return "", err
		}
		return w.cfg.CallbackURL, nil
	case outboxKindEventCallback, outboxKindImageCallback, outboxKindMessagePush:
		// CommandID=管线目标键（"v1:<robotId>" 或 deviceId；无指令行可反查）
		if it.CommandID == "" {
			return w.cfg.CallbackURL, nil
		}
		robotID, err := w.robotIDForTarget(ctx, it.CommandID)
		if err != nil {
			return "", err
		}
		if u, err := w.robotCallbackURL(ctx, robotID, 11); err == nil && u != "" {
			return u, nil
		} else if err != nil {
			return "", err
		}
		return w.cfg.CallbackURL, nil
	default:
		return w.cfg.CallbackURL, nil
	}
}

// robotIDForCommand 由指令号反查 robot（commands.target 为 deviceId →
// devices.robot_id，或 v1:robotId）；不可解析返回空。
func (w *OutboxWorker) robotIDForCommand(ctx context.Context, commandID string) (string, error) {
	rows, err := w.db.QueryDump(ctx, `SELECT target FROM commands WHERE command_id = ?`, commandID)
	if err != nil {
		return "", fmt.Errorf("commandID→target 查询失败: %w", err)
	}
	if len(rows) != 1 {
		return "", nil // 指令行缺失=确定不可路由（非错误）
	}
	return w.robotIDForTarget(ctx, rows[0][0])
}

// robotIDForTarget 由管线目标键解析 robot（"v1:<robotId>" 直读；v2 deviceId 反查
// devices.robot_id）。
func (w *OutboxWorker) robotIDForTarget(ctx context.Context, target string) (string, error) {
	robotID := strings.TrimPrefix(target, "v1:")
	if robotID != target {
		return robotID, nil
	}
	drows, derr := w.db.QueryDump(ctx, `SELECT COALESCE(robot_id,'') FROM devices WHERE device_id = ?`, target)
	if derr != nil {
		return "", fmt.Errorf("deviceId→robot 反查失败: %w", derr)
	}
	if len(drows) == 1 && drows[0][0] != "" {
		return drows[0][0], nil
	}
	// E2E 发现（2026-11-04 管理台闭环）：管理面提交可直接以 robotId 为 target
	//（无 v1: 前缀、非 deviceId）——target 本身命中 devices.robot_id 即视为该 robot。
	rrows, rerr := w.db.QueryDump(ctx, `SELECT COUNT(*) FROM devices WHERE robot_id = ?`, target)
	if rerr != nil {
		return "", fmt.Errorf("robotId 直查失败: %w", rerr)
	}
	if len(rrows) == 1 && rrows[0][0] != "0" {
		return target, nil
	}
	return "", nil // 设备缺失/未绑=确定不可路由（非错误）
}

// robotCallbackURL 取 robot 登记的指定 type 回调绑定 URL（未登记/空返回空串）。
func (w *OutboxWorker) robotCallbackURL(ctx context.Context, robotID string, callbackType int) (string, error) {
	if robotID == "" {
		return "", nil
	}
	crows, cerr := w.db.QueryDump(ctx,
		`SELECT callback_url FROM openapi_callbacks WHERE robot_id = ? AND type = ? LIMIT 1`, robotID, callbackType)
	if cerr != nil {
		return "", fmt.Errorf("robot→回调绑定查询失败: %w", cerr)
	}
	if len(crows) != 1 || crows[0][0] == "" {
		return "", nil // 未绑定该类型=确定无目标（非错误）
	}
	return crows[0][0], nil
}

func (w *OutboxWorker) deliverOne(ctx context.Context, it sqlite.OutboxItem, now time.Time) error {
	target, terr := w.callbackURLFor(ctx, it)
	if terr != nil {
		return terr // 解析链失败：保持 pending（调用方 DeliverDue 已记日志路径）
	}
	if target == "" {
		return nil // 本行无投递目标（确定无绑定且全局未配置）：保持 pending（DeliverDue 层已 skipped 化）
	}
	ts := now.UnixMilli()
	// K3（T3.6）：exec_result 行分发 body 注入文档 ExecCallbackBean 双名别名
	// （messageId/type:int/rawMsg/runTime/...）——仅分发形态，落库 payload 不变
	// （存量表零迁移；HandleOpenAPIResult 回填逻辑读原字段不受影响）。
	// HMAC 签名作用于别名后 body（接收器按文档验内容）。
	body := []byte(it.Payload)
	if w.compatAliasesOn() && it.Kind == "exec_result" {
		body = w.compatExecResultBody(ctx, it)
	}
	// K5：message_push 行投递请求超时收敛为 3s（docs/10 §C：回调 3 秒内须响应，
	// 否则放弃；行本身的投递重试节奏不变，仅代回复单次语义在此窗口内判定）。
	reqCtx := ctx
	if it.Kind == outboxKindMessagePush {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, replyBehalfWindow)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WT-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-WT-Signature", Sign(w.cfg.CallbackSecret, ts, body))
	req.Header.Set("X-WT-Delivery-Attempt", strconv.Itoa(it.Attempts+1))

	resp, err := w.client.Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
	}
	delivered := err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300

	it.Attempts++
	it.UpdatedAt = now
	if delivered {
		it.Status = "delivered"
		it.LastError = ""
		if err := w.db.UpdateOutbox(ctx, it); err != nil {
			return err
		}
		// K5（T3.6）：投递成功后解析响应 body 的 type:5000 同步代回复（单次——
		// 行转 delivered 仅发生一次，天然不重复；docs/15 破坏级 #5）。
		if w.reply != nil && (it.Kind == outboxKindMessagePush || it.Kind == "exec_result") {
			w.maybeReplyOnBehalf(ctx, it, body, resp)
		}
		return nil
	}
	errMsg := "transport error"
	if err == nil {
		errMsg = fmt.Sprintf("http %d", resp.StatusCode)
	}
	it.LastError = errMsg
	if it.Attempts >= len(w.cfg.RetryDelays)+1 {
		// 第 4 次（=1 次首发 + 3 次重试）仍失败 → 死信 + 审计（docs/06 ⑤）
		it.Status = "dead"
		if err := w.db.UpdateOutbox(ctx, it); err != nil {
			return err
		}
		tx, txErr := w.db.Tx(ctx)
		if txErr == nil {
			_ = w.db.InsertAudit(ctx, tx, "outbox_dead_letter", it.CommandID,
				fmt.Sprintf("id=%d attempts=%d lastError=%s", it.ID, it.Attempts, errMsg), now)
			_ = tx.Commit()
		}
		return nil
	}
	delay := w.cfg.RetryDelays[it.Attempts-1]
	it.NextAttempt = now.Add(delay)
	return w.db.UpdateOutbox(ctx, it)
}

// ---- K3：exec_result 分发 body 双名别名（docs/15 破坏级 #3） ----

// compatExecResultBody 把落库 exec_result payload 投影为文档 ExecCallbackBean
// 形态（api-44952776）：注入 messageId（=commandId，与提交响应 data 关联的对账
// 键）、type（int 指令类型；原 "exec_result" 字符串字段改存别名 kind——存量
// 消费者迁移注记）、runTime（=终态时间 at，毫秒）、rawMsg（指令原文，commands
// payload 回查——零 schema 变更）；groupName/qrCode 仅载荷携带时透传（type=0
// 群二维码回调本身 dodDeferred，字段位先留、有值才发）。解析失败防御性回落
// 原始 payload（不因别名层失败丢投递）。
func (w *OutboxWorker) compatExecResultBody(ctx context.Context, it sqlite.OutboxItem) []byte {
	var payload map[string]any
	if err := json.Unmarshal([]byte(it.Payload), &payload); err != nil || payload == nil {
		return []byte(it.Payload)
	}
	if t, ok := payload["type"]; ok {
		payload["kind"] = t // 存量字符串 type 字段迁移注记（WTO：双名过渡）
	}
	if ct, ok := payload["commandType"].(float64); ok {
		payload["type"] = int(ct) // 文档语义：int 指令类型（如 203）
	} else {
		delete(payload, "type") // 无指令类型可投影（防御性：宁缺勿错型）
	}
	payload["messageId"] = it.CommandID
	if at, ok := payload["at"]; ok {
		payload["runTime"] = at // 终态时间（毫秒）
	}
	if raw := w.commandPayload(ctx, it.CommandID); raw != "" {
		payload["rawMsg"] = raw // 指令原文（含 titleList/receivedContent 等）
	}
	// groupName/qrCode：payload 携带才透传（successList/failList/errorCode/
	// errorReason/timeCost 原名原值——文档字段名一致，无需别名）。
	out, err := json.Marshal(payload)
	if err != nil {
		return []byte(it.Payload)
	}
	return out
}

// commandPayload 回查指令原文（commands.payload；零 schema 变更——rawMsg 来源
// 取「payload 回查」而非 Submit 时冗余，方案 K3 二选一定案）。
func (w *OutboxWorker) commandPayload(ctx context.Context, commandID string) string {
	rows, err := w.db.QueryDump(ctx, `SELECT payload FROM commands WHERE command_id = ?`, commandID)
	if err != nil || len(rows) != 1 {
		return ""
	}
	return rows[0][0]
}

// ---- K5：回调响应 type:5000 同步代回复（docs/15 破坏级 #5；FR-35 Demo 协议） ----

// replyBehalfWindow 为代回复语义窗口（docs/10 §C：回调须 3s 内响应，否则放弃）。
const replyBehalfWindow = 3 * time.Second

// replyBehalfMaxResponse 为代回复解析的响应体读取上限（防接收器超大响应撑内存）。
const replyBehalfMaxResponse = 64 << 10

// maybeReplyOnBehalf 解析投递响应 body：{code:0, data:{type:5000, info:{text}}}
// 命中 → 生成 203 代回复指令入队（Pipeline.Submit，FR-53 门链全量生效——不
// 绕过熔断/分级/审批）；单次不重试（行 delivered 仅一次，解析失败/未命中即
// 放弃；放弃落审计 reply_on_behalf_dropped，不留悬挂重试）。会话定向字段以
// 本行分发 body 的 groupName/receivedName 为准（无群名走私聊 receivedName）。
func (w *OutboxWorker) maybeReplyOnBehalf(ctx context.Context, it sqlite.OutboxItem, sentBody []byte, resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, replyBehalfMaxResponse))
	if err != nil {
		w.log.Printf("api/outbox: 代回复响应读取失败 id=%d: %v", it.ID, err)
		return
	}
	var rb struct {
		Code int `json:"code"`
		Data *struct {
			Type int `json:"type"`
			Info *struct {
				Text string `json:"text"`
			} `json:"info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &rb); err != nil || rb.Code != 0 || rb.Data == nil || rb.Data.Type != 5000 {
		return // 非 5000 形态 = 接收器未请求代回复（常见态，静默）
	}
	now := w.now().UTC()
	drop := func(reason string) {
		w.log.Printf("api/outbox: 代回复放弃 id=%d: %s", it.ID, reason)
		tx, txErr := w.db.Tx(ctx)
		if txErr != nil {
			return
		}
		defer func() { _ = tx.Rollback() }()
		_ = w.db.InsertAudit(ctx, tx, "reply_on_behalf_dropped", it.CommandID,
			fmt.Sprintf("id=%d reason=%s", it.ID, reason), now)
		_ = tx.Commit()
	}
	if rb.Data.Info == nil || rb.Data.Info.Text == "" {
		drop("type:5000 缺 info.text（回答文本为空）")
		return
	}
	// 会话定向：分发 body 的 groupName 优先，无群名走私聊 receivedName；
	// exec_result 行 body 无会话字段（ExecCallbackBean 无此二者）→ 回退该指令
	// 载荷 titleList[0]（结果回调的代回复定向到指令原目标会话）。
	var conv struct {
		GroupName    string `json:"groupName"`
		ReceivedName string `json:"receivedName"`
	}
	_ = json.Unmarshal(sentBody, &conv)
	name := conv.GroupName
	if name == "" {
		name = conv.ReceivedName
	}
	if name == "" && it.Kind == "exec_result" {
		var cmd struct {
			TitleList []string `json:"titleList"`
		}
		if raw := w.commandPayload(ctx, it.CommandID); raw != "" {
			_ = json.Unmarshal([]byte(raw), &cmd)
			if len(cmd.TitleList) > 0 {
				name = cmd.TitleList[0]
			}
		}
	}
	if name == "" {
		drop("分发 body 无 groupName/receivedName（且指令无可回退 titleList），代回复无法定向会话")
		return
	}
	// robot 反查（exec_result 行=指令号反查；message_push 行=管线目标键直读）；
	// 查询失败=本轮放弃代回复（非确定不可路由，错误分流口径同投递链）。
	var robotID string
	if it.Kind == "exec_result" {
		r, err := w.robotIDForCommand(ctx, it.CommandID)
		if err != nil {
			drop("robot 反查查询失败（本轮放弃）")
			return
		}
		robotID = r
	} else {
		r, err := w.robotIDForTarget(ctx, it.CommandID)
		if err != nil {
			drop("robot 反查查询失败（本轮放弃）")
			return
		}
		robotID = r
	}
	if robotID == "" {
		drop("无法反查 robot（指令/目标键已不存在）")
		return
	}
	target := w.resolveTarget(ctx, robotID)
	if target == "" {
		drop("robot 目标键解析失败")
		return
	}
	key, err := wire.RandomHex(16)
	if err != nil {
		drop("幂等键生成失败: " + err.Error())
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"type": 203, "titleList": []string{name}, "receivedContent": rb.Data.Info.Text,
	})
	if _, err := w.reply.Submit(ctx, scheduler.SubmitRequest{
		Tenant:      "default",
		Actor:       "compat:reply:5000",
		Operation:   OpenAPIIntent(203),
		Key:         key,
		Target:      target,
		CommandType: 203,
		Payload:     string(payload),
	}); err != nil {
		drop("代回复指令提交失败（FR-53 门链拦截或管线错误）: " + err.Error())
		return
	}
	w.log.Printf("api/outbox: 代回复已入队 robot=%s 会话=%q（type:5000，单次）", robotID, name)
}

// resolveTarget 把 robotId 解析为管线目标键（与 api.Server.resolveTarget 同语义：
// 已配对设备优先 deviceId；否则 v1:robotId 命名空间）。
func (w *OutboxWorker) resolveTarget(ctx context.Context, robotID string) string {
	dev, ok, err := w.db.GetDeviceByRobot(ctx, robotID)
	if err != nil || !ok {
		return "v1:" + robotID
	}
	return dev.DeviceID
}

// ErrDisabled 为未配置回调时的显式语义（内部哨兵，不出现在公开 API）。
var ErrDisabled = errors.New("api/outbox: 未配置回调 URL")
