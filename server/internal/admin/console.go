// 完整管理后台数据面（W-P0 admin-ui 车道）：在四端点最小面之上补齐日常运维
// 端点——概览聚合 / 设备操作 / 指令提交与详情 / 队列操作 / 回调 CRUD 与日志 /
// 执行与审计日志 / 设置（guard 配置·白名单·熔断·加密开关·令牌列表）。
//
// 纪律：
//   - 全部端点 requireAdmin（Bearer admin scope）；
//   - 写操作一律落审计（SR-12，kind 前缀 admin_）；
//   - 只读投影经 sqlite.QueryDump/既有 store 原语，不落第二事实源；
//   - 本包自治 schema 仅 admin_settings（KV，参照 api 包 openapi_callbacks 先例，
//     经 store 公共原语建表，不动 store 包）。
package admin

import (
	"context"
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

// adminSettingsSchema 为管理面自治 KV 设置表（guard 配置持久化 / FR-43 加密开关）。
const adminSettingsSchema = `
CREATE TABLE IF NOT EXISTS admin_settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_ms INTEGER NOT NULL
);`

// openapiCallbacksSchema 为回调绑定表（物理表与 api 包同一张——单一事实源；
// DDL 镜像 api/openapi_rest.go openapiCallbackSchema，两侧 CREATE IF NOT EXISTS
// 幂等共建。管理面在未装配兼容面的部署（仅 admin）下也能管理回调绑定）。
const openapiCallbacksSchema = `
CREATE TABLE IF NOT EXISTS openapi_callbacks (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  robot_id     TEXT NOT NULL,
  type         INTEGER NOT NULL,
  callback_url TEXT NOT NULL,
  created_ms   INTEGER NOT NULL,
  updated_ms   INTEGER NOT NULL,
  UNIQUE(robot_id, type)
);`

// ensureSchema 幂等建表（New 时调用一次）。
func (s *Server) ensureSchema() error {
	if _, err := s.db.Exec(context.Background(), adminSettingsSchema); err != nil {
		return fmt.Errorf("admin: 建设置表失败: %w", err)
	}
	if _, err := s.db.Exec(context.Background(), openapiCallbacksSchema); err != nil {
		return fmt.Errorf("admin: 建回调绑定表失败: %w", err)
	}
	return nil
}

// ---- 设置 KV 辅助 ----

const (
	settingsKeyGuard      = "guard_config"
	settingsKeyEncryption = "comm_encryption_enabled"
	// settingsKeyEventCallbacks 事件回调开关 KV 键（与 api/events.go 同一单一
	// 事实源：api 包只读、管理面唯一写方；值 = {pushEventCallback, pushImage}
	// JSON。缺省形态 = 上游缺省（events.md §〇）：总门 true、图片 false）。
	settingsKeyEventCallbacks = "event_callback_config"
)

func (s *Server) settingGet(ctx context.Context, key string) (string, bool, error) {
	rows, err := s.db.QueryDump(ctx, `SELECT value FROM admin_settings WHERE key = ?`, key)
	if err != nil {
		return "", false, err
	}
	if len(rows) != 1 {
		return "", false, nil
	}
	return rows[0][0], true, nil
}

func (s *Server) settingPut(ctx context.Context, key, value string) error {
	_, err := s.db.Exec(ctx, `
INSERT INTO admin_settings(key, value, updated_ms) VALUES (?,?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_ms=excluded.updated_ms`,
		key, value, s.now().UTC().UnixMilli())
	return err
}

// ApplyPersistedSettings 把持久化设置重放至进程内服务（启动装配时调用一次；
// 集成步骤见 admin/README「接线」）。失败即返回错误（fail-fast，不静默降级）。
func (s *Server) ApplyPersistedSettings(ctx context.Context) error {
	if s.pipeline == nil {
		return nil
	}
	raw, ok, err := s.settingGet(ctx, settingsKeyGuard)
	if err != nil {
		return err
	}
	if ok {
		var g scheduler.GuardConfig
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			return fmt.Errorf("admin: guard 配置持久化值损坏: %w", err)
		}
		if g.L1Window > 0 && g.L1MaxPerTx > 0 {
			s.pipeline.SetGuardConfig(g)
		}
	}
	return nil
}

// ---- ① 概览聚合：GET /api/v1/admin/overview ----

// handleOverview 概览聚合（在线设备数/今日指令数/错误率/最近回调 + 队列与审批
// 待办）。今日 = 服务端 UTC 当天零点起；错误率分母仅计真实执行终态（干跑不
// 计入，FR-54 防污染风控统计）。
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	now := s.now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()

	one := func(query string, args ...any) string {
		rows, err := s.db.QueryDump(ctx, query, args...)
		if err != nil || len(rows) != 1 {
			return "0"
		}
		return rows[0][0]
	}
	devicesTotal := one(`SELECT COUNT(*) FROM devices`)
	// 在线数：逐台经 Deps.Online 探测（网关活连接；nil 注入时恒 false，诚实降级）。
	online := 0
	if rows, err := s.db.QueryDump(ctx, `SELECT device_id FROM devices`); err == nil {
		for _, row := range rows {
			if s.online(row[0]) {
				online++
			}
		}
	}
	todayCommands := one(`SELECT COUNT(*) FROM commands WHERE created_at >= ?`, todayStart)
	todaySucceeded := one(`SELECT COUNT(*) FROM commands WHERE created_at >= ? AND dry_run = 0 AND terminal_state = 'succeeded'`, todayStart)
	todayFailed := one(`SELECT COUNT(*) FROM commands WHERE created_at >= ? AND dry_run = 0 AND terminal_state = 'failed'`, todayStart)
	succ, _ := strconv.Atoi(todaySucceeded)
	fail, _ := strconv.Atoi(todayFailed)
	errorRate := 0.0
	if succ+fail > 0 {
		errorRate = float64(fail) / float64(succ+fail)
	}
	queued := one(`SELECT COUNT(*) FROM commands WHERE state = 'queued' AND dry_run = 0`)
	pendingApprovals := one(`SELECT COUNT(*) FROM approvals WHERE status = 'pending'`)
	pendingCallbacks := one(`SELECT COUNT(*) FROM outbox WHERE status = 'pending'`)

	// 最近回调（outbox 投递行，最近 10 条）。
	callbacks := []map[string]any{}
	if rows, err := s.db.QueryDump(ctx, `
SELECT id, COALESCE(command_id,''), kind, status, attempts, COALESCE(last_error,''), created_at, updated_at
FROM outbox ORDER BY updated_at DESC, id DESC LIMIT 10`); err == nil {
		for _, row := range rows {
			item := map[string]any{
				"id": row[0], "commandId": row[1], "kind": row[2], "status": row[3],
				"attempts": atoiDefault(row[4]), "lastError": row[5],
				"createdAtMs": atoiDefault(row[6]), "updatedAtMs": atoiDefault(row[7]),
			}
			callbacks = append(callbacks, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAtMs":    now.UnixMilli(),
		"devicesTotal":     atoiDefault(devicesTotal),
		"devicesOnline":    online,
		"todayCommands":    atoiDefault(todayCommands),
		"todaySucceeded":   succ,
		"todayFailed":      fail,
		"todayErrorRate":   errorRate,
		"queuedCommands":   atoiDefault(queued),
		"pendingApprovals": atoiDefault(pendingApprovals),
		"pendingCallbacks": atoiDefault(pendingCallbacks),
		"recentCallbacks":  callbacks,
	})
}

func atoiDefault(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ---- ② 设备管理：列表增强 + 暂停/恢复/解绑 ----

// handleDeviceAction POST /api/v1/admin/devices/{deviceId}/pause|resume|unbind。
//
// 语义边界（如实登记，不夸大）：
//   - pause：devices.status='paused'。**强制执行范围 = 管理面指令提交门**
//     （handleCommandSubmit 拒绝向 paused 设备提交，409 DEVICE_PAUSED）。网关
//     连接层/兼容面路径未检查该状态——连接级强制待网关车道接线（dodDeferred，
//     见 admin/README「边界与后续」）。
//   - resume：status='active'。
//   - unbind：robot_id 置空（v1 兼容面绑定解除；与重配对清空同语义，W-M3R）。
func (s *Server) handleDeviceAction(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	deviceID := r.PathValue("deviceId")
	action := r.PathValue("action")
	if deviceID == "" {
		(&adminError{http.StatusBadRequest, "INVALID_DEVICE_ID", "deviceId 不能为空"}).write(w)
		return
	}
	ctx := r.Context()
	dev, found, err := s.db.GetDevice(ctx, deviceID)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	now := s.now().UTC()
	switch action {
	case "pause", "resume":
		if !found {
			(&adminError{http.StatusNotFound, "DEVICE_NOT_FOUND", "设备 " + deviceID + " 不存在"}).write(w)
			return
		}
		status := "paused"
		if action == "resume" {
			status = "active"
		}
		if dev.Status == status {
			writeJSON(w, http.StatusOK, map[string]any{"deviceId": deviceID, "status": status, "changed": false})
			return
		}
		if _, err := s.db.Exec(ctx, `UPDATE devices SET status=?, updated_at=? WHERE device_id=?`,
			status, now.UnixMilli(), deviceID); err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		kind := "admin_device_paused"
		if action == "resume" {
			kind = "admin_device_resumed"
		}
		if err := s.db.InsertAudit(ctx, nil, kind, deviceID, "设备状态 → "+status, now); err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deviceId": deviceID, "status": status, "changed": true})
	case "unbind":
		if !found {
			(&adminError{http.StatusNotFound, "DEVICE_NOT_FOUND", "设备 " + deviceID + " 不存在"}).write(w)
			return
		}
		if dev.RobotID == "" {
			writeJSON(w, http.StatusOK, map[string]any{"deviceId": deviceID, "robotId": "", "changed": false})
			return
		}
		if _, err := s.db.Exec(ctx, `UPDATE devices SET robot_id=NULL, updated_at=? WHERE device_id=?`,
			now.UnixMilli(), deviceID); err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		if err := s.db.InsertAudit(ctx, nil, "admin_device_unbound", deviceID,
			"解除 robot 绑定（原 robotId="+dev.RobotID+"，v1 兼容面寻址与回调路由随之失效）", now); err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deviceId": deviceID, "robotId": "", "changed": true})
	default:
		(&adminError{http.StatusBadRequest, "INVALID_ACTION", "action 须为 pause|resume|unbind"}).write(w)
	}
}

// lastSessionMs 取设备最近一次会话时间（device_sessions 的 opened_at/created_at
// 最大值——最近 hello ≈ 最近心跳证据；无会话行 → 0）。诚实口径：devices 表无
// 心跳列，此处为最近会话痕迹而非逐帧心跳。
func (s *Server) lastSessionMs(ctx context.Context, deviceID string) int64 {
	rows, err := s.db.QueryDump(ctx, `
SELECT MAX(COALESCE(opened_at, created_at)) FROM device_sessions WHERE device_id = ?`, deviceID)
	if err != nil || len(rows) != 1 {
		return 0
	}
	n, _ := strconv.ParseInt(rows[0][0], 10, 64)
	return n
}

// ---- ③a 指令提交：POST /api/v1/admin/commands ----

type adminSubmitRequest struct {
	Target      string          `json:"target"`
	CommandType int             `json:"type"`
	Payload     json.RawMessage `json:"payload"` // 完整载荷对象；缺 type 字段时由 type 注入（CR ADM-2）
	Params      map[string]any  `json:"params"`  // 简写参数（与 type 合成 {"type":N,...}）
	DryRun      bool            `json:"dryRun"`
	ApprovalID  string          `json:"approvalId"`
	Actor       string          `json:"actor"`
	// IdempotencyKey 幂等键（CR ADM-4）：调用方（UI）生成并在重试时复用 → 同键
	// 同参重放返回原 commandId；留空时服务端生成 UUID（每请求唯一，仅登记口径
	// 一致，无跨请求重放保护）。响应回显实际使用的键。
	IdempotencyKey string `json:"idempotencyKey"`
}

// handleCommandSubmit 管理面指令提交：与兼容面同一条统一管线（scheduler.Submit
// ——幂等/熔断/审批/白名单/限速/风险冻结全链生效），非旁路。
func (s *Server) handleCommandSubmit(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req adminSubmitRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体解析失败/含未定义字段（须为 {target,type,params|payload,dryRun,approvalId,actor}）"}).write(w)
		return
	}
	req.Target = strings.TrimSpace(req.Target)
	req.Actor = strings.TrimSpace(req.Actor)
	if req.Actor == "" {
		req.Actor = "admin"
	}
	if req.Target == "" {
		(&adminError{http.StatusBadRequest, "INVALID_TARGET", "target 必填（deviceId 或 v1:robotId）"}).write(w)
		return
	}
	// 载荷合成：payload 优先；否则 {"type":N, ...params}。
	// payload 缺 type 字段时注入 type=req.CommandType（CR ADM-2：无参数指令经 UI
	// 等价载荷 {} 不再 E02「payload 缺 type」；type 显式不一致仍交管线 400 拒绝）。
	payload := ""
	if len(req.Payload) > 0 {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(req.Payload, &probe); err != nil {
			(&adminError{http.StatusBadRequest, "INVALID_PAYLOAD", "payload 须为 JSON 对象"}).write(w)
			return
		}
		if _, hasType := probe["type"]; !hasType {
			probe["type"], _ = json.Marshal(req.CommandType)
			raw, err := json.Marshal(probe)
			if err != nil {
				(&adminError{http.StatusBadRequest, "INVALID_PAYLOAD", "payload 序列化失败: " + err.Error()}).write(w)
				return
			}
			payload = string(raw)
		} else {
			payload = string(req.Payload)
		}
	} else {
		obj := map[string]any{"type": req.CommandType}
		for k, v := range req.Params {
			obj[k] = v
		}
		raw, err := json.Marshal(obj)
		if err != nil {
			(&adminError{http.StatusBadRequest, "INVALID_PAYLOAD", "params 序列化失败: " + err.Error()}).write(w)
			return
		}
		payload = string(raw)
	}
	ctx := r.Context()
	// 管理面设备暂停门（②pause 语义的强制执行点）。
	if dev, ok, err := s.resolveDeviceForTarget(ctx, req.Target); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	} else if ok && dev.Status == "paused" {
		(&adminError{http.StatusConflict, "DEVICE_PAUSED",
			"设备 " + req.Target + " 已被管理面暂停（恢复后再提交；网关层强制待接线，见 admin/README）"}).write(w)
		return
	}
	// 幂等键（CR ADM-4）：与兼容面同一条统一管线在幂等维度对齐——调用方键优先
	// （重试复用 → 重放保护），留空则服务端生成（响应回显，供调用方续用）。
	idemKey := strings.TrimSpace(req.IdempotencyKey)
	if idemKey == "" {
		idemKey = "admin-" + wire.NewUUID()
	}
	res, err := s.pipeline.Submit(ctx, scheduler.SubmitRequest{
		Tenant: "default", Actor: req.Actor, Operation: "admin.submit", Key: idemKey,
		Target: req.Target, CommandType: req.CommandType, Payload: payload,
		DryRun: req.DryRun, ApprovalID: strings.TrimSpace(req.ApprovalID),
	})
	if err != nil {
		s.writeSubmitError(w, res, err)
		return
	}
	now := s.now().UTC()
	_ = s.db.InsertAudit(ctx, nil, "admin_command_submitted", res.CommandID,
		fmt.Sprintf("target=%s type=%d dryRun=%v actor=%s state=%s idemKey=%s", req.Target, req.CommandType, req.DryRun, req.Actor, res.State, idemKey), now)
	status := http.StatusCreated
	state := res.State
	body := map[string]any{"commandId": res.CommandID, "state": state, "replay": res.Replay, "dryRun": req.DryRun, "idempotencyKey": idemKey}
	if state == "awaiting_approval" {
		status = http.StatusAccepted
		body["approvalId"] = res.ApprovalID
		body["message"] = "L2 指令已登记待审批（FR-53）：到「审批」视图决议后携 approvalId 重新提交"
	}
	// 干跑：与兼容面同路径沿同一状态机同步产出报告（FR-54；ExecuteDryRun 幂等
	// 可重入，同键重试从断点收敛，不死键）。
	if req.DryRun && res.CommandID != "" {
		report, derr := s.pipeline.ExecuteDryRun(ctx, res.CommandID)
		if derr != nil {
			(&adminError{http.StatusInternalServerError, "DRYRUN", "干跑执行失败: " + derr.Error()}).write(w)
			return
		}
		body["dryRunReport"] = report
		body["state"] = "succeeded"
	}
	writeJSON(w, status, body)
}

// resolveDeviceForTarget 把提交目标解析为设备行（v2 deviceId 直查；v1:robotId
// 反查 robot 绑定）。无设备行（纯 v1 命名空间机器人）→ ok=false，不拦截。
func (s *Server) resolveDeviceForTarget(ctx context.Context, target string) (deviceRow, bool, error) {
	id := target
	if rid := strings.TrimPrefix(target, "v1:"); rid != target {
		rows, err := s.db.QueryDump(ctx, `SELECT device_id FROM devices WHERE robot_id = ?`, rid)
		if err != nil {
			return deviceRow{}, false, err
		}
		if len(rows) != 1 {
			return deviceRow{}, false, nil
		}
		id = rows[0][0]
	}
	rows, err := s.db.QueryDump(ctx, `SELECT device_id, COALESCE(robot_id,''), status FROM devices WHERE device_id = ?`, id)
	if err != nil {
		return deviceRow{}, false, err
	}
	if len(rows) != 1 {
		return deviceRow{}, false, nil
	}
	return deviceRow{DeviceID: rows[0][0], RobotID: rows[0][1], Status: rows[0][2]}, true, nil
}

type deviceRow struct {
	DeviceID string
	RobotID  string
	Status   string
}

// writeSubmitError 把 Submit 错误映射为 HTTP（与兼容面 openapi.go 映射同型）。
func (s *Server) writeSubmitError(w http.ResponseWriter, res scheduler.SubmitResult, err error) {
	var ge *scheduler.GuardError
	if errors.As(err, &ge) {
		switch ge.Code {
		case scheduler.ErrCodeApprovalNeeded:
			// 待审批不是错误：202 + approvalId（UI 引导决议流）。
			writeJSON(w, http.StatusAccepted, map[string]any{
				"state": "awaiting_approval", "approvalId": res.ApprovalID, "message": ge.Msg,
			})
			return
		case scheduler.ErrCodeCircuitOpen:
			(&adminError{http.StatusServiceUnavailable, ge.Code, ge.Msg}).write(w)
			return
		case scheduler.ErrCodeRateLimited:
			(&adminError{http.StatusTooManyRequests, ge.Code, ge.Msg}).write(w)
			return
		case scheduler.ErrCodeNotWhitelisted, scheduler.ErrCodeApproverConflict:
			(&adminError{http.StatusForbidden, ge.Code, ge.Msg}).write(w)
			return
		case scheduler.ErrCodeApprovalMismatch:
			(&adminError{http.StatusConflict, ge.Code, ge.Msg}).write(w)
			return
		}
		(&adminError{http.StatusForbidden, ge.Code, ge.Msg}).write(w)
		return
	}
	if errors.Is(err, scheduler.ErrIdempotencyConflict) || errors.Is(err, scheduler.ErrRiskFrozen) {
		(&adminError{http.StatusConflict, "SUBMIT_REJECTED", err.Error()}).write(w)
		return
	}
	// 静态校验拒绝（E02）等 → 400。
	(&adminError{http.StatusBadRequest, "SUBMIT_REJECTED", err.Error()}).write(w)
}

// ---- ③b 指令详情：GET /api/v1/admin/commands/{commandId} ----

// handleCommandDetail 单条指令全量视图：指令行 + 载荷 + E01→E18 事件轨迹 +
// 结果 JSON（outbox exec_result 载荷）。载荷/结果仅在单条详情面展示（授权
// admin 专属），审计流列表仍仅元数据（docs/02 §6.1 口径，见 admin/README）。
func (s *Server) handleCommandDetail(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	commandID := r.PathValue("commandId")
	ctx := r.Context()
	rows, err := s.db.QueryDump(ctx, `
SELECT command_id, target, command_type, payload, state, risk_class, dry_run, created_at,
COALESCE(dispatched_at,0), COALESCE(ack_deadline,0), COALESCE(terminal_at,0), COALESCE(terminal_state,''), epoch, seq
FROM commands WHERE command_id = ?`, commandID)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if len(rows) != 1 {
		(&adminError{http.StatusNotFound, "COMMAND_NOT_FOUND", "指令 " + commandID + " 不存在"}).write(w)
		return
	}
	row := rows[0]
	cmd := map[string]any{
		"commandId": row[0], "target": row[1], "type": atoiDefault(row[2]), "payload": row[3],
		"state": row[4], "riskClass": row[5], "dryRun": row[6] == "1",
		"createdAtMs": atoiDefault(row[7]), "dispatchedAtMs": atoiDefault(row[8]),
		"ackDeadlineMs": atoiDefault(row[9]), "terminalAtMs": atoiDefault(row[10]),
		"terminalState": row[11], "epoch": atoiDefault(row[12]), "seq": atoiDefault(row[13]),
	}
	events := []map[string]any{}
	if erows, err := s.db.QueryDump(ctx, `
SELECT edge_id, from_state, to_state, event, detail, at FROM command_events WHERE command_id = ? ORDER BY id`, commandID); err == nil {
		for _, e := range erows {
			events = append(events, map[string]any{
				"edge": e[0], "fromState": e[1], "toState": e[2], "event": e[3],
				"detail": e[4], "atMs": atoiDefault(e[5]),
			})
		}
	}
	results := []map[string]any{}
	if orows, err := s.db.QueryDump(ctx, `
SELECT id, kind, status, attempts, COALESCE(last_error,''), payload, created_at FROM outbox WHERE command_id = ? ORDER BY id`, commandID); err == nil {
		for _, o := range orows {
			var parsed any
			if json.Unmarshal([]byte(o[5]), &parsed) == nil {
				results = append(results, map[string]any{
					"id": atoiDefault(o[0]), "kind": o[1], "status": o[2], "attempts": atoiDefault(o[3]),
					"lastError": o[4], "payload": parsed, "createdAtMs": atoiDefault(o[6]),
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"command": cmd, "events": events, "results": results})
}

// ---- ③c 队列视图：GET/DELETE /api/v1/admin/queue(+/{commandId}/priority) ----

// handleQueueList 当前排队指令（queued 且非干跑；FIFO 序 = created_at）。
// ?target= 过滤；默认全部目标（每目标内按提交序）。
func (s *Server) handleQueueList(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	query := `
SELECT command_id, target, command_type, risk_class, dry_run, created_at
FROM commands WHERE state = 'queued' AND dry_run = 0`
	var args []any
	if tg := strings.TrimSpace(r.URL.Query().Get("target")); tg != "" {
		query += ` AND target = ?`
		args = append(args, tg)
	}
	query += ` ORDER BY target, created_at`
	rows, err := s.db.QueryDump(ctx, query, args...)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	position := map[string]int{}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		position[row[1]]++
		items = append(items, map[string]any{
			"commandId": row[0], "target": row[1], "type": atoiDefault(row[2]),
			"riskClass": row[3], "position": position[row[1]], "createdAtMs": atoiDefault(row[5]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": items, "count": len(items)})
}

// handleQueueRemove DELETE /api/v1/admin/queue/{commandId}（移除单条 = 上游 305
// 指定清除语义：仅 queued 可清；被清指令落 101018 取消回执）。
// L2 审批豁免登记（REV-SRV-05）：admin scope 即授权决议面（requireAdmin）且操作
// 落审计——兼容面同语义操作须走 L2 审批门（api requireQueueOpApproval），本面
// 豁免已在 docs/05 矩阵登记（两面差异显式化，不静默）。
func (s *Server) handleQueueRemove(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	commandID := r.PathValue("commandId")
	ctx := r.Context()
	cmd, ok, err := s.db.GetCommand(ctx, commandID)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if !ok {
		(&adminError{http.StatusNotFound, "COMMAND_NOT_FOUND", "指令 " + commandID + " 不存在"}).write(w)
		return
	}
	if cmd.State != "queued" {
		(&adminError{http.StatusConflict, "NOT_QUEUED",
			fmt.Sprintf("指令不在队列中（state=%s；仅 queued 待执行指令可移除）", cmd.State)}).write(w)
		return
	}
	if err := s.pipeline.CancelBeforeDelivery(ctx, commandID, "admin"); err != nil {
		(&adminError{http.StatusInternalServerError, "CANCEL", err.Error()}).write(w)
		return
	}
	if err := s.enqueueAdminCancelReceipt(ctx, cmd); err != nil {
		(&adminError{http.StatusInternalServerError, "RECEIPT", err.Error()}).write(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commandId": commandID, "status": "cancelled"})
}

// handleQueueClear DELETE /api/v1/admin/queue?target=X（清空目标队列 = 上游 304
// 语义；逐条取消 + 101018 回执）。L2 审批豁免登记同 handleQueueRemove（REV-SRV-05）。
func (s *Server) handleQueueClear(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		(&adminError{http.StatusBadRequest, "INVALID_TARGET", "清空须指定 ?target=（deviceId 或 v1:robotId）"}).write(w)
		return
	}
	ctx := r.Context()
	queued, err := s.pipeline.QueuedForTarget(ctx, target, 500)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	cleared := []string{}
	for _, c := range queued {
		if err := s.pipeline.CancelBeforeDelivery(ctx, c.CommandID, "admin"); err != nil {
			(&adminError{http.StatusInternalServerError, "CANCEL",
				fmt.Sprintf("清空中断（已清 %d 条）：%v", len(cleared), err)}).write(w)
			return
		}
		if err := s.enqueueAdminCancelReceipt(ctx, c); err != nil {
			(&adminError{http.StatusInternalServerError, "RECEIPT", err.Error()}).write(w)
			return
		}
		cleared = append(cleared, c.CommandID)
	}
	now := s.now().UTC()
	_ = s.db.InsertAudit(ctx, nil, "admin_queue_cleared", target,
		fmt.Sprintf("清空待执行队列 %d 条（101018 逐条回执）", len(cleared)), now)
	writeJSON(w, http.StatusOK, map[string]any{"target": target, "cleared": len(cleared), "commandIds": cleared})
}

// handleQueuePriority POST /api/v1/admin/queue/{commandId}/priority（插队：把
// queued 指令的队列序键抬到队首之前——与兼容面 queue/priority 同实现口径）。
func (s *Server) handleQueuePriority(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	commandID := r.PathValue("commandId")
	ctx := r.Context()
	cmd, ok, err := s.db.GetCommand(ctx, commandID)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if !ok || cmd.State != "queued" {
		(&adminError{http.StatusNotFound, "NOT_QUEUED", "指令不存在或不在队列中（仅 queued 可插队）"}).write(w)
		return
	}
	queued, err := s.pipeline.QueuedForTarget(ctx, cmd.Target, 500)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	prev, origMS := 0, cmd.CreatedAt.UnixMilli()
	for i, c := range queued {
		if c.CommandID == commandID {
			prev = i + 1
			break
		}
	}
	if prev == 0 {
		(&adminError{http.StatusNotFound, "NOT_QUEUED", "指令不在待执行队列中"}).write(w)
		return
	}
	adjusted := false
	if prev > 1 {
		head := queued[0].CreatedAt.UnixMilli()
		if _, err := s.db.Exec(ctx,
			`UPDATE commands SET created_at = ? WHERE command_id = ? AND state = 'queued'`, head-1, commandID); err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		adjusted = true
		now := s.now().UTC()
		_ = s.db.InsertAudit(ctx, nil, "admin_queue_priority", commandID,
			fmt.Sprintf("position %d→1（队列序键抬至队首前，原序键 %d 留痕于此）", prev, origMS), now)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"commandId": commandID, "previousPosition": prev, "position": 1, "adjusted": adjusted,
	})
}

// enqueueAdminCancelReceipt 为被清/被移除指令落 101018 执行回执（与兼容面
// 304/305 逐条回执同形态；等待结果的回调接收方据此感知取消）。
func (s *Server) enqueueAdminCancelReceipt(ctx context.Context, cmd sqlite.Command) error {
	payload := map[string]any{
		"type": "exec_result", "commandId": cmd.CommandID, "commandType": cmd.CommandType,
		"errorCode": 101018, "errorReason": "待执行指令被管理面队列操作清理（clear/remove）",
		"timeCost": 0, "finalState": "cancelled", "dryRun": cmd.DryRun,
		"at": s.now().UTC().UnixMilli(),
	}
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return err
	}
	if err := s.db.EnqueueOutbox(ctx, tx, "exec_result", cmd.CommandID, payload, s.now().UTC()); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- ④ 回调管理：/api/v1/admin/callbacks（CRUD + 测试）与 /callbacks/logs ----

// adminCallbackRow 回调绑定投影（openapi_callbacks 表 = 兼容面同源单一事实；
// 管理面不落第二张配置表）。启用状态如实映射：绑定即启用，删除即停用（表无
// 独立 enabled 列——新增列会造成兼容面无视该列的假停用，不做）。
type adminCallbackRow struct {
	RobotID  string `json:"robotId"`
	Target   string `json:"target"` // 兼容面寻址 robot（v1）
	Type     int    `json:"type"`
	TypeName string `json:"typeName"`
	URL      string `json:"callbackUrl"`
	Enabled  bool   `json:"enabled"`
}

// 回调 type 枚举（与 api/openapi_rest.go 同源：0/1/5/6/11；管理面只读复用，
// 不另立枚举）。
var adminCallbackTypeNames = map[int]string{
	0: "群二维码回调", 1: "指令结果回调", 5: "机器人上线回调", 6: "机器人下线回调", 11: "消息回调",
}

// handleCallbacks 面板路由（方法守卫内派发）。
func (s *Server) handleCallbacks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleCallbacksList(w, r)
	case http.MethodPost:
		s.handleCallbacksBind(w, r, "bind")
	case http.MethodPut:
		s.handleCallbacksBind(w, r, "update")
	case http.MethodDelete:
		s.handleCallbacksDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, PUT, DELETE")
		(&adminError{http.StatusMethodNotAllowed, "METHOD", "仅 GET/POST/PUT/DELETE"}).write(w)
	}
}

func (s *Server) handleCallbacksList(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	rows, err := s.db.QueryDump(ctx, `SELECT robot_id, type, callback_url FROM openapi_callbacks ORDER BY robot_id, type`)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]adminCallbackRow, 0, len(rows))
	for _, row := range rows {
		t := atoiDefault(row[1])
		items = append(items, adminCallbackRow{
			RobotID: row[0], Type: t, TypeName: adminCallbackTypeNames[t],
			URL: row[2], Enabled: true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"callbacks": items, "count": len(items)})
}

type adminCallbackBindRequest struct {
	RobotID     string `json:"robotId"`
	Type        int    `json:"type"`
	CallbackURL string `json:"callBackUrl"`
}

// handleCallbacksBind POST（新增/覆盖同 type）与 PUT（仅更新已存在）。
// 绑定校验与兼容面同纪律：默认仅 loopback 接收器（SR-09），目标须以 200 +
// application/json 应答探测（硬性）；Deps.AllowNonLoopbackCallbacks 显式放行
// 后可绑外网（部署侧强制 TLS，SR-01）。
func (s *Server) handleCallbacksBind(w http.ResponseWriter, r *http.Request, op string) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req adminCallbackBindRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为 JSON 对象 {robotId, type, callBackUrl}"}).write(w)
		return
	}
	req.RobotID = strings.TrimSpace(req.RobotID)
	req.CallbackURL = strings.TrimSpace(req.CallbackURL)
	if req.RobotID == "" || req.RobotID == adminScopeRobotID {
		(&adminError{http.StatusBadRequest, "INVALID_ROBOT", "robotId 必填且不得为管理面保留名 admin"}).write(w)
		return
	}
	if _, known := adminCallbackTypeNames[req.Type]; !known {
		(&adminError{http.StatusBadRequest, "INVALID_TYPE", "回调 type 不在枚举 0/1/5/6/11"}).write(w)
		return
	}
	u, err := url.Parse(req.CallbackURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		(&adminError{http.StatusBadRequest, "INVALID_URL", "callBackUrl 须为合法 http(s) URL"}).write(w)
		return
	}
	if !s.allowNonLoopbackCallbacks && !isLoopbackURL(u) {
		(&adminError{http.StatusForbidden, "NOT_LOOPBACK",
			"默认禁外网：回调 URL 仅允许 loopback 接收器（SR-09）；外网须装配侧 AllowNonLoopbackCallbacks 放行并强制 TLS（SR-01）"}).write(w)
		return
	}
	ctx := r.Context()
	if !s.probeCallback(ctx, u) {
		(&adminError{http.StatusBadRequest, "PROBE_FAILED",
			"回调地址校验未通过（目标接口须以 200 + application/json 应答，3s 超时）"}).write(w)
		return
	}
	if op == "update" {
		existing, err := s.db.QueryDump(ctx,
			`SELECT id FROM openapi_callbacks WHERE robot_id = ? AND type = ?`, req.RobotID, req.Type)
		if err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		if len(existing) == 0 {
			(&adminError{http.StatusNotFound, "NOT_FOUND",
				fmt.Sprintf("robot=%s type=%d 未登记绑定（PUT 仅更新；新绑定用 POST）", req.RobotID, req.Type)}).write(w)
			return
		}
	}
	now := s.now().UTC()
	if _, err := s.db.Exec(ctx, `
INSERT INTO openapi_callbacks(robot_id, type, callback_url, created_ms, updated_ms) VALUES (?,?,?,?,?)
ON CONFLICT(robot_id, type) DO UPDATE SET callback_url=excluded.callback_url, updated_ms=excluded.updated_ms`,
		req.RobotID, req.Type, req.CallbackURL, now.UnixMilli(), now.UnixMilli()); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	_ = s.db.InsertAudit(ctx, nil, "admin_callback_"+op, req.RobotID,
		fmt.Sprintf("type=%d url=%s", req.Type, req.CallbackURL), now)
	writeJSON(w, http.StatusOK, map[string]any{"robotId": req.RobotID, "type": req.Type, "callbackUrl": req.CallbackURL, "op": op})
}

func (s *Server) handleCallbacksDelete(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	q := r.URL.Query()
	robotID := strings.TrimSpace(q.Get("robotId"))
	typStr := strings.TrimSpace(q.Get("type"))
	if robotID == "" || typStr == "" {
		(&adminError{http.StatusBadRequest, "INVALID_QUERY", "须带 ?robotId=&type="}).write(w)
		return
	}
	typ, err := strconv.Atoi(typStr)
	if err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_TYPE", "type 须为整数"}).write(w)
		return
	}
	ctx := r.Context()
	res, err := s.db.Exec(ctx, `DELETE FROM openapi_callbacks WHERE robot_id = ? AND type = ?`, robotID, typ)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	deleted, _ := res.RowsAffected()
	if deleted == 0 {
		(&adminError{http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("robot=%s type=%d 未登记绑定", robotID, typ)}).write(w)
		return
	}
	_ = s.db.InsertAudit(ctx, nil, "admin_callback_delete", robotID, fmt.Sprintf("type=%d", typ), s.now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"robotId": robotID, "type": typ, "deleted": deleted})
}

// handleCallbackTest POST /api/v1/admin/callbacks/test（探测回调地址可用性；
// 不落库）。校验口径与绑定一致：200 + application/json，3s 超时。
func (s *Server) handleCallbackTest(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req struct {
		CallbackURL string `json:"callBackUrl"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.CallbackURL) == "" {
		(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为 JSON 对象 {callBackUrl}"}).write(w)
		return
	}
	u, err := url.Parse(strings.TrimSpace(req.CallbackURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		(&adminError{http.StatusBadRequest, "INVALID_URL", "callBackUrl 须为合法 http(s) URL"}).write(w)
		return
	}
	if !s.allowNonLoopbackCallbacks && !isLoopbackURL(u) {
		(&adminError{http.StatusForbidden, "NOT_LOOPBACK", "默认禁外网：仅允许探测 loopback 接收器（SR-09）"}).write(w)
		return
	}
	ok := s.probeCallback(r.Context(), u)
	writeJSON(w, http.StatusOK, map[string]any{"url": u.String(), "reachable": ok,
		"message": map[bool]string{true: "目标以 200 + application/json 应答（可绑定）", false: "目标未以 200 + application/json 应答（3s 超时或格式不符）"}[ok]})
}

// handleCallbackLogs GET /api/v1/admin/callbacks/logs（outbox 投递日志：成功/
// 失败/重试次数；?status=pending|delivered|dead 过滤，分页）。
func (s *Server) handleCallbackLogs(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	limit := pageLimit(r, 100, 500)
	offset := pageOffset(r)
	query := `SELECT id, COALESCE(command_id,''), kind, status, attempts, COALESCE(last_error,''), next_attempt, created_at, updated_at FROM outbox`
	var args []any
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		switch st {
		case "pending", "delivered", "dead":
			query += ` WHERE status = ?`
			args = append(args, st)
		default:
			(&adminError{http.StatusBadRequest, "INVALID_STATUS", "status 须为 pending|delivered|dead"}).write(w)
			return
		}
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryDump(ctx, query, args...)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"id": atoiDefault(row[0]), "commandId": row[1], "kind": row[2], "status": row[3],
			"attempts": atoiDefault(row[4]), "lastError": row[5],
			"nextAttemptMs": atoiDefault(row[6]), "createdAtMs": atoiDefault(row[7]), "updatedAtMs": atoiDefault(row[8]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": items, "count": len(items), "limit": limit, "offset": offset})
}

// isLoopbackURL 判定 loopback 接收器（localhost 或回环 IP）。
func isLoopbackURL(u *url.URL) bool {
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// callbackProbeClient 探测客户端（3s 超时；禁跟随重定向——REV-SRV-01/SR-09：
// loopback 目标 30x 跳外网时跟随会误判「校验通过」；禁随后 30x 非 200 即失败，
// 与 api 包 callbackProbeClient 同纪律）。
var callbackProbeClient = &http.Client{
	Timeout: 3 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// probeCallback 探测目标接口（GET，3s 超时，须 200 + application/json）。
func (s *Server) probeCallback(ctx context.Context, u *url.URL) bool {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	resp, err := callbackProbeClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	ct := resp.Header.Get("Content-Type")
	return resp.StatusCode == http.StatusOK && strings.HasPrefix(ct, "application/json")
}

// ---- ⑤ 日志查询 ----

// handleExecutionLogs GET /api/v1/admin/logs/executions（指令执行日志时间线：
// command_events 全局流，含 E01→E18 边轨迹；?commandId= 定位单条，?target= 过滤）。
func (s *Server) handleExecutionLogs(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	limit := pageLimit(r, 100, 500)
	offset := pageOffset(r)
	query := `
SELECT e.id, e.command_id, COALESCE(c.target,''), COALESCE(c.command_type,0), c.risk_class,
e.edge_id, e.from_state, e.to_state, e.event, e.detail, e.at
FROM command_events e LEFT JOIN commands c ON c.command_id = e.command_id`
	var conds []string
	var args []any
	if cid := strings.TrimSpace(r.URL.Query().Get("commandId")); cid != "" {
		conds = append(conds, "e.command_id = ?")
		args = append(args, cid)
	}
	if tg := strings.TrimSpace(r.URL.Query().Get("target")); tg != "" {
		conds = append(conds, "c.target = ?")
		args = append(args, tg)
	}
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY e.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryDump(ctx, query, args...)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"id": atoiDefault(row[0]), "commandId": row[1], "target": row[2], "type": atoiDefault(row[3]),
			"riskClass": row[4], "edge": row[5], "fromState": row[6], "toState": row[7],
			"event": row[8], "detail": row[9], "atMs": atoiDefault(row[10]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": items, "count": len(items), "limit": limit, "offset": offset})
}

// handleAuditLogs GET /api/v1/admin/logs/audit（系统审计日志：谁在什么时候做了
// 什么——kind/subject/detail；?kind= 过滤，分页）。
func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	limit := pageLimit(r, 100, 500)
	offset := pageOffset(r)
	query := `SELECT id, kind, subject, detail, at FROM audit_log`
	var args []any
	if kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind != "" {
		query += ` WHERE kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryDump(ctx, query, args...)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"id": atoiDefault(row[0]), "kind": row[1], "subject": row[2], "detail": row[3], "atMs": atoiDefault(row[4]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": items, "count": len(items), "limit": limit, "offset": offset})
}

// ---- ⑥ 设置 ----

// handleSettings GET /api/v1/admin/settings：机器人配置汇总（guard 限流/白名单
// 策略、熔断状态、加密开关、令牌概览计数）。熔断参数为进程内配置且 scheduler
// 未导出读回/写入口——此处展示默认值并如实标注（参数调整 dodDeferred）。
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	// guard：持久化管理面值（source=persisted）或管线默认（source=default）。
	guard := map[string]any{
		"l1WindowSeconds":      60,
		"l1MaxPerTx":           10,
		"whitelistDefaultDeny": false,
		"source":               "default",
	}
	if raw, ok, err := s.settingGet(ctx, settingsKeyGuard); err == nil && ok {
		var g scheduler.GuardConfig
		if json.Unmarshal([]byte(raw), &g) == nil && g.L1Window > 0 && g.L1MaxPerTx > 0 {
			guard = map[string]any{
				"l1WindowSeconds":      int(g.L1Window.Seconds()),
				"l1MaxPerTx":           g.L1MaxPerTx,
				"whitelistDefaultDeny": g.WhitelistDefaultDeny,
				"source":               "persisted",
			}
		}
	}
	def := scheduler.DefaultBreakerConfig()
	breaker := map[string]any{
		"state":           string(s.pipeline.Breaker().State()),
		"windowSeconds":   int(def.Window.Seconds()),
		"minSamples":      def.MinSamples,
		"threshold":       def.Threshold,
		"cooldownSeconds": int(def.Cooldown.Seconds()),
		"closeSamples":    def.CloseSamples,
		"note":            "状态为进程内实时值（重启回 closed）；参数为默认值展示，scheduler 未导出参数读回/调整入口（dodDeferred）",
	}
	encEnabled := false
	if raw, ok, err := s.settingGet(ctx, settingsKeyEncryption); err == nil && ok {
		encEnabled = raw == "1"
	}
	encryption := map[string]any{
		"enabled": encEnabled,
		"note":    "FR-43 通讯加密开关（默认关）。本开关持久化管理面意图并落审计；网关握手协商的强制接线属网关车道（dodDeferred，见 admin/README）",
	}
	// 事件回调开关（T3.5）：api/events.go 分发内核逐事件只读同源 KV，改即生效。
	pushEvent, pushImage, evSource := s.eventCallbackSwitches(ctx)
	eventCallbacks := map[string]any{
		"pushEventCallback": pushEvent, "pushImage": pushImage, "source": evSource,
		"note": "事件类回调开关（T3.5）：pushEventCallback 总门（101 被拉群/102 群名更新/103 群解散/104 被踢/105 好友通过 + 511/512/513/515 查询推送，默认开）；pushImage 图片 fileBase64 回调独立门（默认关）。改即生效（分发内核逐事件读 KV）",
	}
	tokens := map[string]any{}
	if rows, err := s.db.QueryDump(ctx, `SELECT COUNT(*) FROM api_tokens WHERE status='active'`); err == nil && len(rows) == 1 {
		tokens["activeTokens"] = atoiDefault(rows[0][0])
	}
	if rows, err := s.db.QueryDump(ctx, `SELECT COUNT(*) FROM recipient_whitelist`); err == nil && len(rows) == 1 {
		tokens["whitelistEntries"] = atoiDefault(rows[0][0])
	}
	writeJSON(w, http.StatusOK, map[string]any{"guard": guard, "breaker": breaker, "encryption": encryption, "eventCallbacks": eventCallbacks, "counts": tokens})
}

// handleGuardSettings PUT /api/v1/admin/settings/guard（限流/白名单策略：实时
// 应用 pipeline.SetGuardConfig + 持久化 admin_settings + 审计；启动重放经
// ApplyPersistedSettings）。
func (s *Server) handleGuardSettings(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req struct {
		L1WindowSeconds      int  `json:"l1WindowSeconds"`
		L1MaxPerTx           int  `json:"l1MaxPerTx"`
		WhitelistDefaultDeny bool `json:"whitelistDefaultDeny"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为 {l1WindowSeconds, l1MaxPerTx, whitelistDefaultDeny}"}).write(w)
		return
	}
	if req.L1WindowSeconds < 1 || req.L1WindowSeconds > 3600 || req.L1MaxPerTx < 1 || req.L1MaxPerTx > 1000 {
		(&adminError{http.StatusBadRequest, "INVALID_RANGE", "l1WindowSeconds 1..3600；l1MaxPerTx 1..1000"}).write(w)
		return
	}
	cfg := scheduler.GuardConfig{
		L1Window:             time.Duration(req.L1WindowSeconds) * time.Second,
		L1MaxPerTx:           req.L1MaxPerTx,
		WhitelistDefaultDeny: req.WhitelistDefaultDeny,
	}
	s.pipeline.SetGuardConfig(cfg)
	raw, _ := json.Marshal(cfg)
	ctx := r.Context()
	if err := s.settingPut(ctx, settingsKeyGuard, string(raw)); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	_ = s.db.InsertAudit(ctx, nil, "admin_settings_guard", "", string(raw), s.now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"applied": true, "guard": map[string]any{
		"l1WindowSeconds": req.L1WindowSeconds, "l1MaxPerTx": req.L1MaxPerTx,
		"whitelistDefaultDeny": req.WhitelistDefaultDeny, "source": "persisted",
	}})
}

// handleEncryptionSettings PUT /api/v1/admin/settings/encryption（FR-43 开关；
// 持久化 + 审计。强制执行边界见 handleSettings 注释，如实不夸大）。
func (s *Server) handleEncryptionSettings(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Enabled == nil {
		(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为 {enabled: true|false}"}).write(w)
		return
	}
	value := "0"
	if *req.Enabled {
		value = "1"
	}
	ctx := r.Context()
	if err := s.settingPut(ctx, settingsKeyEncryption, value); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	_ = s.db.InsertAudit(ctx, nil, "admin_settings_encryption", "",
		"FR-43 通讯加密开关 → "+map[bool]string{true: "开", false: "关"}[*req.Enabled]+"（网关强制待接线）", s.now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"enabled": *req.Enabled})
}

// ---- ⑦ 事件回调开关（T3.5，W-M6；契约 local-private/parity/b3/events.md） ----

// adminEventCallbackSwitches 为开关 KV 值的本包镜像结构（与 api/events.go
// EventCallbackConfig 同字段同名 JSON 键——单一事实源为 admin_settings KV 行，
// 结构两侧镜像不互 import，同 openapi_callbacks DDL 镜像先例）。
type adminEventCallbackSwitches struct {
	PushEventCallback bool `json:"pushEventCallback"` // 总门：101~105 + 511~515（缺省 true，e2/a.java:209）
	PushImage         bool `json:"pushImage"`         // 图片回调独立门（缺省 false，doc-861677）
}

// eventCallbackSwitches 读当前开关（KV 缺失/损坏 → 缺省形态：总门开、图片关，
// 与 api/events.go 读路径同一缺省策略——上游缺省，events.md §〇）。
func (s *Server) eventCallbackSwitches(ctx context.Context) (pushEvent, pushImage bool, source string) {
	pushEvent, pushImage, source = true, false, "default"
	raw, ok, err := s.settingGet(ctx, settingsKeyEventCallbacks)
	if err != nil || !ok {
		return
	}
	var cfg adminEventCallbackSwitches
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return // 损坏值回落缺省
	}
	return cfg.PushEventCallback, cfg.PushImage, "persisted"
}

// handleEventCallbackSettings PUT /api/v1/admin/settings/event-callbacks（T3.5
// 开关面：六类被动事件 101~105 + 查询推送 511~515 总门 pushEventCallback（缺省
// 开，e2/a.java:209）与图片 fileBase64 回调独立门 pushImage（缺省关，doc-861677）。
// 指针字段=仅更新提供项；持久化 admin_settings（api 分发内核只读同源）+ 审计）。
func (s *Server) handleEventCallbackSettings(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req struct {
		PushEventCallback *bool `json:"pushEventCallback"`
		PushImage         *bool `json:"pushImage"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为 {pushEventCallback?: bool, pushImage?: bool}"}).write(w)
		return
	}
	ctx := r.Context()
	pushEvent, pushImage, _ := s.eventCallbackSwitches(ctx)
	if req.PushEventCallback != nil {
		pushEvent = *req.PushEventCallback
	}
	if req.PushImage != nil {
		pushImage = *req.PushImage
	}
	raw, err := json.Marshal(adminEventCallbackSwitches{PushEventCallback: pushEvent, PushImage: pushImage})
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if err := s.settingPut(ctx, settingsKeyEventCallbacks, string(raw)); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	_ = s.db.InsertAudit(ctx, nil, "admin_settings_event_callbacks", "", string(raw), s.now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"pushEventCallback": pushEvent, "pushImage": pushImage, "source": "persisted"})
}

// handleBreakerReset POST /api/v1/admin/breaker/reset（人工复位熔断为 closed；
// 复位留审计——与自动翻开/闭合事件同为 SR-12 审计面）。
func (s *Server) handleBreakerReset(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	changed := s.pipeline.Breaker().Reset()
	_ = s.db.InsertAudit(r.Context(), nil, "admin_breaker_reset", "",
		fmt.Sprintf("人工复位熔断（changed=%v：非 closed 态复位生效）", changed), s.now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"state": string(s.pipeline.Breaker().State()), "changed": changed})
}

// handleRiskQuery GET /api/v1/admin/risk/{type}（实时分级查询——单一事实源
// scheduler.ClassifyRisk，未登记类型 L2 fail-closed）。
func (s *Server) handleRiskQuery(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	typ, err := strconv.Atoi(r.PathValue("type"))
	if err != nil || typ < 1 || typ > 99999 {
		(&adminError{http.StatusBadRequest, "INVALID_TYPE", "type 须为 1..99999 整数（提交面另行受 11..999 约束）"}).write(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": typ, "riskClass": scheduler.ClassifyRisk(typ)})
}

// handleWhitelist 白名单管理（L1 接收人白名单，FR-53；scheduler 导出面直通）。
func (s *Server) handleWhitelist(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		target := strings.TrimSpace(r.URL.Query().Get("target"))
		if target == "" {
			(&adminError{http.StatusBadRequest, "INVALID_TARGET", "须带 ?target="}).write(w)
			return
		}
		list, err := s.pipeline.WhitelistFor(ctx, target)
		if err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"target": target, "recipients": list, "count": len(list)})
	case http.MethodPost:
		var req struct {
			Target    string `json:"target"`
			Recipient string `json:"recipient"`
			AddedBy   string `json:"addedBy"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为 {target, recipient, addedBy}"}).write(w)
			return
		}
		req.Target, req.Recipient = strings.TrimSpace(req.Target), strings.TrimSpace(req.Recipient)
		if req.Target == "" || req.Recipient == "" || len(req.Recipient) > 128 {
			(&adminError{http.StatusBadRequest, "INVALID_INPUT", "target/recipient 必填，recipient ≤128 字符"}).write(w)
			return
		}
		if req.AddedBy == "" {
			req.AddedBy = "admin"
		}
		if err := s.pipeline.AddWhitelist(ctx, req.Target, req.Recipient, req.AddedBy); err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		_ = s.db.InsertAudit(ctx, nil, "admin_whitelist_add", req.Target,
			"recipient="+req.Recipient+" by="+req.AddedBy, s.now().UTC())
		writeJSON(w, http.StatusCreated, map[string]any{"target": req.Target, "recipient": req.Recipient})
	case http.MethodDelete:
		target := strings.TrimSpace(r.URL.Query().Get("target"))
		recipient := strings.TrimSpace(r.URL.Query().Get("recipient"))
		if target == "" || recipient == "" {
			(&adminError{http.StatusBadRequest, "INVALID_QUERY", "须带 ?target=&recipient="}).write(w)
			return
		}
		if err := s.pipeline.RemoveWhitelist(ctx, target, recipient); err != nil {
			(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
			return
		}
		_ = s.db.InsertAudit(ctx, nil, "admin_whitelist_remove", target, "recipient="+recipient, s.now().UTC())
		writeJSON(w, http.StatusOK, map[string]any{"target": target, "recipient": recipient, "removed": true})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		(&adminError{http.StatusMethodNotAllowed, "METHOD", "仅 GET/POST/DELETE"}).write(w)
	}
}

// handleTokensList GET /api/v1/admin/tokens（全部令牌只读投影：hash 前 8 位 +
// scope（admin 保留名 → 管理面）/租户/状态/时间；原始值不可再生，永不出现在
// 响应）。签发/吊销走既有 /robots/{robotId}/openapi-tokens 与 IssueAdminToken。
func (s *Server) handleTokensList(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	rows, err := s.db.QueryDump(r.Context(), `
SELECT substr(token_hash,1,8), robot_id, tenant, status, created_ms, COALESCE(last_used_ms,0)
FROM api_tokens ORDER BY created_ms DESC LIMIT 500`)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		scope := "robot:" + row[1]
		if row[1] == adminScopeRobotID {
			scope = "admin（管理面）"
		}
		item := map[string]any{
			"hashPrefix8": row[0], "robotId": row[1], "scope": scope, "tenant": row[2],
			"status": row[3], "createdMs": atoiDefault(row[4]),
		}
		if used := atoiDefault(row[5]); used > 0 {
			item["lastUsedMs"] = used
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": items, "count": len(items)})
}

// ---- 分页辅助 ----

// pageLimit 解析 ?limit=（def 默认，max 上限）。
func pageLimit(r *http.Request, def, max int) int {
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= max {
			return n
		}
	}
	return def
}

// pageOffset 解析 ?offset=（≥0，非法回落 0）。
func pageOffset(r *http.Request) int {
	if v := strings.TrimSpace(r.URL.Query().Get("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}
