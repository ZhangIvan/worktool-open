// /openapi/v1/robots/{robotId}/* 扩展面（apifox 兼容 REST 分组，FR-34 全量面）：
//
//	机器人配置组：GET  .../info .../online .../logs .../corps
//	回调配置组：  POST/GET/PUT/DELETE .../callbacks（回调 CRUD；投递走 outbox worker）
//	历史消息组：  GET  .../history/commands | /history/results | /history/callbacks | /history/messages
//	群管理组：    GET  .../groups
//	队列管理组：  GET/DELETE .../queue、DELETE .../queue/{commandId}、POST .../queue/priority
//
// 纪律（与指令组同源）：每端点 Bearer 鉴权 + 60QPM 限流（openapiLimiter）+ 写操作
// 强制 Idempotency-Key + 统一 {code,message,data} 信封；查询面字段名对齐上游
// apifox 形态（messageId/runTime/rawMsg 等），数据源为统一管线表（commands/
// outbox/device_sessions），不落第二事实源。队列清理/指定取消按上游 304/305 语义
// 为每条被清待执行指令落 101018 回执（outbox exec_result 行，仅回执通道），且经
// L2 审批门（REV-SRV-05：与 304 指令提交同门，见 requireQueueOpApproval）。
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// ---- 兼容面扩展表（本包自治 schema；经 store 公共原语建表，不动 store 包） ----

// openapiCallbackSchema 为回调配置表（FR-35 管理端点化）：robot 维度回调绑定，
// UNIQUE(robot_id,type)——同 type 重复绑定走覆盖语义（上游重复 bind 行为未公布，
// 覆盖为最小惊讶选择，登记于 05 矩阵）。投递凭据不落本表：签名密钥沿用
// OutboxConfig.CallbackSecret（单一密钥源），本表只存 URL。
const openapiCallbackSchema = `
CREATE TABLE IF NOT EXISTS openapi_callbacks (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  robot_id     TEXT NOT NULL,
  type         INTEGER NOT NULL,
  callback_url TEXT NOT NULL,
  created_ms   INTEGER NOT NULL,
  updated_ms   INTEGER NOT NULL,
  UNIQUE(robot_id, type)
);`

// ensureOpenAPISchema 建本包扩展表（幂等；NewServeMux 装配时调用一次）。
func (s *Server) ensureOpenAPISchema(ctx context.Context) error {
	if _, err := s.DB.Exec(ctx, openapiCallbackSchema); err != nil {
		return fmt.Errorf("api/openapi: 建扩展表失败: %w", err)
	}
	return nil
}

// 回调 type 枚举（上游 get 面全集 0/1/5/6/11）。5/6（上线/下线）当前仅登记与
// 查询——触发点在网关会话生命周期（网关车道接线），本面不伪造事件（dodDeferred）。
const (
	openapiCallbackTypeGroupQRCode = 0  // 群二维码回调
	openapiCallbackTypeExecResult  = 1  // 指令结果回调（outbox worker 已接线按 robot 投递）
	openapiCallbackTypeOnline      = 5  // 机器人上线回调
	openapiCallbackTypeOffline     = 6  // 机器人下线回调
	openapiCallbackTypeNewMessage  = 11 // 消息回调
)

var openapiCallbackTypeNames = map[int]string{
	openapiCallbackTypeGroupQRCode: "群二维码回调",
	openapiCallbackTypeExecResult:  "指令结果回调",
	openapiCallbackTypeOnline:      "机器人上线回调",
	openapiCallbackTypeOffline:     "机器人下线回调",
	openapiCallbackTypeNewMessage:  "消息回调",
}

// openapiLimiterKeyCap 单 robot 限流上限内允许的最大队列快照（防全量拉取放大）。
const openapiQueueSnapshotCap = 500

// openapiGuard 兼容面统一前置：Bearer 鉴权（token 归属核对）→ 60QPM 限流 →
// （写操作）Idempotency-Key 强制。返回 false 时已写响应。
// 次序纪律（REV-SRV-04）：限流在鉴权**之后**按已认证 robot 计数——未认证请求
// 不进限流 map（防随机 robotId 路径参数撑爆内存）、不消耗任何 robot 的 60QPM
// 配额（防未认证打穿受害 robot 兼容面）。
func (s *Server) openapiGuard(w http.ResponseWriter, r *http.Request, robotID string, requireIdem bool) (sqlite.APIToken, bool) {
	token, ae := s.authenticate(r.Context(), r, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return sqlite.APIToken{}, false
	}
	if !s.openapiLimiter.allow(robotID, s.Now().UTC()) {
		writeOpenAPIErr(w, http.StatusTooManyRequests, openapiCodeRateLimited,
			fmt.Sprintf("请求超出限流 %d 次/%s（60QPM，对齐 apifox 限制值）",
				s.openapiCfg.RateLimitPerMinute, s.openapiCfg.RateWindow))
		return sqlite.APIToken{}, false
	}
	if requireIdem && strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeIdemKeyRequired,
			"缺少 Idempotency-Key 请求头（全部写操作端点强制，spec §5.6.1）")
		return sqlite.APIToken{}, false
	}
	return token, true
}

// methodGuard 方法守卫（非预期方法 405 + Allow）。
func methodGuard(w http.ResponseWriter, r *http.Request, allow string, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", allow)
	writeOpenAPIErr(w, http.StatusMethodNotAllowed, 101001, "仅 "+allow)
	return false
}

// pageParams 解析分页参数（page 默认 1、size 默认 10、上限 100；对齐上游 list 面）。
func pageParams(r *http.Request) (page, size int) {
	page, size = 1, 10
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && v > 0 {
		size = v
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

// pageEnvelope 构造上游分页信封 data（{pageNum,pageSize,totalPage,total,list}）。
func pageEnvelope(page, size, total int, list any) map[string]any {
	totalPage := 0
	if total > 0 {
		totalPage = int(math.Ceil(float64(total) / float64(size)))
	}
	if list == nil {
		list = []any{}
	}
	return map[string]any{
		"pageNum": page, "pageSize": size, "totalPage": totalPage, "total": total, "list": list,
	}
}

// timeRange 解析 startTime/endTime（毫秒时间戳，选填）。
func timeRange(r *http.Request) (start, end int64, err error) {
	if v := strings.TrimSpace(r.URL.Query().Get("startTime")); v != "" {
		start, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("startTime 须为毫秒时间戳")
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("endTime")); v != "" {
		end, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("endTime 须为毫秒时间戳")
		}
	}
	return start, end, nil
}

// ---- b) 机器人配置组 ----

// HandleOpenAPIRobotInfo GET .../info：机器人信息（配对绑定 + 租户 + 风险基线）。
func (s *Server) HandleOpenAPIRobotInfo(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	dev, paired, err := s.DB.GetDeviceByRobot(ctx, robotID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	data := map[string]any{
		"robotId": robotID,
		"paired":  paired,
	}
	if paired {
		data["deviceId"] = dev.DeviceID
		data["deviceName"] = dev.DeviceName
		data["deviceStatus"] = dev.Status
		data["epoch"] = dev.Epoch
		data["pairedAt"] = dev.PairedAt.UnixMilli()
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, data)
}

// HandleOpenAPIRobotOnline GET .../online：在线状态（网关活连接探测 + 最近会话行）。
func (s *Server) HandleOpenAPIRobotOnline(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	dev, paired, err := s.DB.GetDeviceByRobot(ctx, robotID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	data := map[string]any{"robotId": robotID, "online": false, "lastSession": nil}
	if paired {
		data["deviceId"] = dev.DeviceID
		data["online"] = s.Hub != nil && s.Hub.HasLiveCred(dev.DeviceID)
		if sess, ok2, err2 := s.lastSession(ctx, dev.DeviceID); err2 != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err2.Error())
			return
		} else if ok2 {
			data["lastSession"] = sess
		}
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, data)
}

// lastSession 取设备最近一条会话行（登录日志单条形态）。
func (s *Server) lastSession(ctx context.Context, deviceID string) (map[string]any, bool, error) {
	sess, err := s.sessionsPage(ctx, deviceID, 1, 1)
	if err != nil || len(sess) == 0 {
		return nil, false, err
	}
	return sess[0], true, nil
}

// sessionsPage 分页取设备会话行（created_at 倒序）。
func (s *Server) sessionsPage(ctx context.Context, deviceID string, page, size int) ([]map[string]any, error) {
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT session_id, state, created_at, opened_at, closed_at
FROM device_sessions WHERE device_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		deviceID, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, state string
		var created int64
		var opened, closed sql.NullInt64
		if err := rows.Scan(&id, &state, &created, &opened, &closed); err != nil {
			return nil, err
		}
		item := map[string]any{"sessionId": id, "state": state, "createdAt": created}
		if opened.Valid {
			item["openedAt"] = opened.Int64
		}
		if closed.Valid {
			item["closedAt"] = closed.Int64
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// HandleOpenAPIRobotLogs GET .../logs：登录日志（设备会话开合历史；未配对 v1
// 命名空间机器人无设备会话 → 空列表，不伪造）。
func (s *Server) HandleOpenAPIRobotLogs(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	page, size := pageParams(r)
	list := []map[string]any{}
	total := 0
	if dev, paired, err := s.DB.GetDeviceByRobot(ctx, robotID); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	} else if paired {
		if list, err = s.sessionsPage(ctx, dev.DeviceID, page, size); err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		if rows, err := s.DB.QueryDump(ctx, `SELECT COUNT(*) FROM device_sessions WHERE device_id = ?`, dev.DeviceID); err == nil && len(rows) == 1 {
			total, _ = strconv.Atoi(rows[0][0])
		}
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, pageEnvelope(page, size, total, list))
}

// HandleOpenAPIRobotCorps GET .../corps：企业列表（快照语义：最近一次真实 506
// 指令的设备回执 successList；无快照 → 空列表 + snapshot=false，不伪造数据）。
func (s *Server) HandleOpenAPIRobotCorps(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	s.serveSnapshot(w, r, 506, "corps")
}

// HandleOpenAPIGroups GET .../groups：群列表查询（同快照语义，数据源 508；上游
// 对应 /robot/wework/group/list，其文档已标 deprecated 并推荐消息回调——本面
// 等价提供，05 矩阵登记 deprecated 对齐）。
func (s *Server) HandleOpenAPIGroups(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	s.serveSnapshot(w, r, 508, "groups")
}

// serveSnapshot 快照查询面通用实现（corps=506 / groups=508）。
func (s *Server) serveSnapshot(w http.ResponseWriter, r *http.Request, commandType int, field string) {
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	rows, err := s.DB.QueryDump(ctx, `
SELECT command_id FROM commands
WHERE target = ? AND command_type = ? AND dry_run = 0 AND state IN ('succeeded','failed')
ORDER BY created_at DESC LIMIT 1`, target, commandType)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	data := map[string]any{"robotId": robotID, "snapshot": false, field: []any{}}
	if len(rows) == 0 {
		writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, data)
		return
	}
	commandID := rows[0][0]
	items, err := s.DB.OutboxByCommand(ctx, commandID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	for _, it := range items {
		if it.Kind != "exec_result" {
			continue
		}
		var cb map[string]any
		if err := json.Unmarshal([]byte(it.Payload), &cb); err == nil {
			data["snapshot"] = true
			data["commandId"] = commandID
			data["updatedAt"] = it.CreatedAt.UnixMilli()
			if ec, ok := cb["errorCode"].(float64); ok && ec == 0 {
				if sl, ok := cb["successList"].([]any); ok {
					data[field] = sl
				}
			}
		}
		break
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, data)
}

// ---- c) 回调配置组 ----

// openapiCallbackRow 回调绑定行。
type openapiCallbackRow struct {
	ID       int64  `json:"id"`
	Type     int    `json:"type"`
	URL      string `json:"callbackUrl"`
	TypeName string `json:"typeName"`
}

// HandleOpenAPICallbacks POST/GET/PUT/DELETE .../callbacks（回调 CRUD；type 枚举
// 0/1/5/6/11）。POST=新增或覆盖同 type（上游重复 bind 行为未公布，覆盖语义登记
// 05 矩阵）；PUT=仅更新已存在绑定（不存在 404/101010）；DELETE 按 query type
// 删除并返回剩余绑定列表（上游 deleteByType 形态）。绑定校验：目标接口须以 200 +
// application/json 应答探测（硬性）；默认仅 loopback 接收器（SR-09 同纪律），
// Server.AllowNonLoopbackCallbacks 显式放行后方可绑外网（部署侧强制 TLS，SR-01）。
func (s *Server) HandleOpenAPICallbacks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleCallbacksGet(w, r)
	case http.MethodPost:
		s.handleCallbacksWrite(w, r, "bind")
	case http.MethodPut:
		s.handleCallbacksWrite(w, r, "update")
	case http.MethodDelete:
		s.handleCallbacksDelete(w, r)
	default:
		methodGuard(w, r, "GET, POST, PUT, DELETE",
			http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete)
	}
}

func (s *Server) handleCallbacksGet(w http.ResponseWriter, r *http.Request) {
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	list, err := s.callbacksList(r.Context(), robotID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{"list": list})
}

// callbacksList 取 robot 绑定列表（type 升序；typeName 注解）。
func (s *Server) callbacksList(ctx context.Context, robotID string) ([]openapiCallbackRow, error) {
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx,
		`SELECT id, type, callback_url FROM openapi_callbacks WHERE robot_id = ? ORDER BY type`, robotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []openapiCallbackRow{}
	for rows.Next() {
		var it openapiCallbackRow
		if err := rows.Scan(&it.ID, &it.Type, &it.URL); err != nil {
			return nil, err
		}
		it.TypeName = openapiCallbackTypeNames[it.Type]
		out = append(out, it)
	}
	return out, rows.Err()
}

// openapiCallbackBinding 绑定写请求体。
type openapiCallbackBinding struct {
	Type        int    `json:"type"`
	CallbackURL string `json:"callBackUrl"`
}

func (s *Server) handleCallbacksWrite(w http.ResponseWriter, r *http.Request, op string) {
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, true); !ok {
		return
	}
	ctx := r.Context()
	var body openapiCallbackBinding
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeInvalidBody, "请求体须为 JSON 对象 {type, callBackUrl}")
		return
	}
	if _, known := openapiCallbackTypeNames[body.Type]; !known {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData,
			fmt.Sprintf("回调 type=%d 不在枚举 0/1/5/6/11（0 群二维码、1 指令结果、5 上线、6 下线、11 消息）", body.Type))
		return
	}
	u, err := url.Parse(strings.TrimSpace(body.CallbackURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "callBackUrl 须为合法 http(s) URL")
		return
	}
	if !s.AllowNonLoopbackCallbacks && !isLoopbackHostURL(u) {
		writeOpenAPIErr(w, http.StatusForbidden, openapiCodeNotWhitelisted,
			"默认禁外网：回调 URL 仅允许 loopback 接收器（SR-09）；外网接收器须部署侧显式放行并强制 TLS（SR-01）")
		return
	}
	// 绑定校验（硬性，上游同款）：目标接口须 200 + application/json 应答
	if !s.probeCallbackURL(r.Context(), u) {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalOp,
			"回调地址校验未通过（目标接口须以 200 + application/json 应答）——无法绑定")
		return
	}
	if op == "update" {
		existing, err := s.DB.QueryDump(ctx,
			`SELECT id FROM openapi_callbacks WHERE robot_id = ? AND type = ?`, robotID, body.Type)
		if err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		if len(existing) == 0 {
			writeOpenAPIErr(w, http.StatusNotFound, openapiCodeNotFound,
				fmt.Sprintf("type=%d 未登记绑定（PUT 仅更新已存在；新绑定用 POST）", body.Type))
			return
		}
	}
	now := s.Now().UTC().UnixMilli()
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO openapi_callbacks(robot_id, type, callback_url, created_ms, updated_ms)
VALUES (?,?,?,?,?)
ON CONFLICT(robot_id, type) DO UPDATE SET callback_url=excluded.callback_url, updated_ms=excluded.updated_ms`,
		robotID, body.Type, body.CallbackURL, now, now)
	if err != nil {
		_ = tx.Rollback()
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"op": op, "type": body.Type, "url": body.CallbackURL})
	_ = s.DB.InsertAudit(ctx, nil, auditKindOpenAPI+"_callback_bind", robotID, string(detail), s.Now().UTC())
	list, err := s.callbacksList(ctx, robotID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{"list": list})
}

func (s *Server) handleCallbacksDelete(w http.ResponseWriter, r *http.Request) {
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, true); !ok {
		return
	}
	ctx := r.Context()
	// type 来源：query ?type=N 或 DELETE body {"type":N}（上游 deleteByType body 形态；
	// type=0 群二维码为合法值，指针判空区分「未提供」与「0」）
	typeStr := strings.TrimSpace(r.URL.Query().Get("type"))
	if typeStr == "" && r.Body != nil {
		var body struct {
			Type *int `json:"type"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err == nil && body.Type != nil {
			typeStr = strconv.Itoa(*body.Type)
		}
	}
	if typeStr == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "缺 type（query ?type=N 或 body {\"type\":N}）")
		return
	}
	typ, err := strconv.Atoi(typeStr)
	if err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "type 须为整数")
		return
	}
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM openapi_callbacks WHERE robot_id = ? AND type = ?`, robotID, typ)
	if err != nil {
		_ = tx.Rollback()
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	deleted, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	if deleted == 0 {
		writeOpenAPIErr(w, http.StatusNotFound, openapiCodeNotFound,
			fmt.Sprintf("type=%d 未登记绑定", typ))
		return
	}
	_ = s.DB.InsertAudit(ctx, nil, auditKindOpenAPI+"_callback_delete", robotID,
		fmt.Sprintf("type=%d", typ), s.Now().UTC())
	list, err := s.callbacksList(ctx, robotID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	// 上游形态：data=删除后剩余绑定列表
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{"list": list})
}

// probeCallbackURL 绑定校验探测：目标须以 2xx + application/json 应答（GET，
// 3s 超时——上游 3s 超时放弃口径）。探测失败=校验不通过，不绑定。
func (s *Server) probeCallbackURL(ctx context.Context, u *url.URL) bool {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	resp, err := s.callbackProbeClient().Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	ct := resp.Header.Get("Content-Type")
	return resp.StatusCode == http.StatusOK && strings.HasPrefix(ct, "application/json")
}

// callbackProbeClient 探测客户端（3s 超时；禁跟随重定向）。
// 重定向纪律（REV-SRV-01 / SR-09）：探测绝不跟随 3xx——loopback URL 返回 30x
// 指向外网时，跟随会以外网终态 200+JSON 误判「校验通过」，击穿默认禁外网承诺。
// 禁随后 30x 终态非 200，探测即失败，绑定被拒。
func (s *Server) callbackProbeClient() *http.Client {
	if s.callbackProbe == nil {
		s.callbackProbe = &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return s.callbackProbe
}

// ---- d) 历史消息组 ----

// historyFilters 指令/结果历史共用过滤（messageId=commandId 精确、type、时间窗）。
func historyFilters(r *http.Request) (conds []string, args []any, err error) {
	q := r.URL.Query()
	if v := strings.TrimSpace(q.Get("messageId")); v != "" {
		conds = append(conds, "c.command_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("type")); v != "" {
		t, perr := strconv.Atoi(v)
		if perr != nil {
			return nil, nil, fmt.Errorf("type 须为整数")
		}
		conds = append(conds, "c.command_type = ?")
		args = append(args, t)
	}
	start, end, terr := timeRange(r)
	if terr != nil {
		return nil, nil, terr
	}
	if start > 0 {
		conds = append(conds, "c.created_at >= ?")
		args = append(args, start)
	}
	if end > 0 {
		conds = append(conds, "c.created_at <= ?")
		args = append(args, end)
	}
	return conds, args, nil
}

// HandleOpenAPIHistoryCommands GET .../history/commands：指令调用查询
// （上游 listRawMessage 等价面；字段对齐统一管线：commandId/type/status/riskClass）。
func (s *Server) HandleOpenAPIHistoryCommands(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	filters, args, err := historyFilters(r)
	if err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, err.Error())
		return
	}
	if status != "" {
		filters = append(filters, "c.state = ?")
		args = append(args, status)
	}
	page, size := pageParams(r)
	where := " WHERE c.target = ?"
	for _, f := range filters {
		where += " AND " + f
	}
	list := []map[string]any{}
	total := 0
	tx, txErr := s.DB.Tx(ctx)
	if txErr != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, txErr.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()
	all := append([]any{target}, args...)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM commands c`+where, all...).Scan(&total); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	rows, err := tx.QueryContext(ctx, `
SELECT c.command_id, c.command_type, c.state, c.risk_class, c.dry_run, c.created_at, c.terminal_state
FROM commands c`+where+` ORDER BY c.created_at DESC LIMIT ? OFFSET ?`,
		append(all, size, (page-1)*size)...)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	for rows.Next() {
		var id string
		var typ int
		var state, risk string
		var dryRun int64
		var created int64
		var terminal sql.NullString
		if err := rows.Scan(&id, &typ, &state, &risk, &dryRun, &created, &terminal); err != nil {
			rows.Close()
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		item := map[string]any{
			"messageId": id, "commandId": id, "type": typ, "status": state,
			"riskClass": risk, "dryRun": dryRun != 0, "createTime": created,
		}
		if terminal.Valid {
			item["finalState"] = terminal.String
		}
		list = append(list, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, pageEnvelope(page, size, total, list))
}

// HandleOpenAPIHistoryResults GET .../history/results：指令执行结果查询
// （上游 rawMsg/list 等价面：messageId 闭环查询；errorCode 透传 + codeName 注解）。
func (s *Server) HandleOpenAPIHistoryResults(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	filters, args, err := historyFilters(r)
	if err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, err.Error())
		return
	}
	page, size := pageParams(r)
	where := " WHERE c.target = ?"
	for _, f := range filters {
		where += " AND " + f
	}
	where += " AND EXISTS (SELECT 1 FROM outbox o WHERE o.command_id = c.command_id AND o.kind = 'exec_result')"
	all := append([]any{target}, args...)
	list := []map[string]any{}
	total := 0
	tx, txErr := s.DB.Tx(ctx)
	if txErr != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, txErr.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM commands c`+where, all...).Scan(&total); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	rows, err := tx.QueryContext(ctx, `
SELECT c.command_id, c.command_type, c.payload, c.created_at, c.terminal_at, c.terminal_state, o.payload
FROM commands c JOIN outbox o ON o.command_id = c.command_id AND o.kind = 'exec_result'`+where+`
ORDER BY c.created_at DESC LIMIT ? OFFSET ?`, append(all, size, (page-1)*size)...)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	for rows.Next() {
		var id, payload string
		var typ int
		var created int64
		var terminalAt sql.NullInt64
		var terminalState sql.NullString
		var outboxPayload string
		if err := rows.Scan(&id, &typ, &payload, &created, &terminalAt, &terminalState, &outboxPayload); err != nil {
			rows.Close()
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		item := map[string]any{
			"messageId": id, "robotId": robotID, "type": typ, "rawMsg": payload,
			"apiSend": 1, "runTime": created, "finalState": "",
		}
		if terminalAt.Valid {
			item["runTime"] = terminalAt.Int64
		}
		if terminalState.Valid {
			item["finalState"] = terminalState.String
		}
		var cb map[string]any
		if json.Unmarshal([]byte(outboxPayload), &cb) == nil {
			if ec, ok := cb["errorCode"].(float64); ok {
				item["rawSuccess"] = int(ec)
				if name := OpenAPICodeName(int(ec)); name != "" {
					item["errorCodeName"] = name
				}
			}
			item["errorReason"] = cb["errorReason"]
			item["timeCost"] = cb["timeCost"]
			item["successList"] = cb["successList"]
			item["failList"] = cb["failList"]
		}
		list = append(list, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, pageEnvelope(page, size, total, list))
}

// HandleOpenAPIHistoryCallbacks GET .../history/callbacks：回调日志查询
// （outbox 投递行：状态/尝试次数/最近错误；上游 qaLog 面 data schema 未公布——
// 本面以投递日志等价提供，05 矩阵登记）。
func (s *Server) HandleOpenAPIHistoryCallbacks(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	page, size := pageParams(r)
	list := []map[string]any{}
	total := 0
	tx, txErr := s.DB.Tx(ctx)
	if txErr != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, txErr.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM outbox o JOIN commands c ON c.command_id = o.command_id WHERE c.target = ?`,
		target).Scan(&total); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	rows, err := tx.QueryContext(ctx, `
SELECT o.id, o.command_id, o.kind, o.status, o.attempts, o.last_error, o.created_at, o.updated_at
FROM outbox o JOIN commands c ON c.command_id = o.command_id
WHERE c.target = ? ORDER BY o.created_at DESC LIMIT ? OFFSET ?`,
		target, size, (page-1)*size)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	for rows.Next() {
		var id int64
		var commandID, kind, status, lastErr string
		var attempts int
		var createdAt, updatedAt int64
		if err := rows.Scan(&id, &commandID, &kind, &status, &attempts, &lastErr, &createdAt, &updatedAt); err != nil {
			rows.Close()
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		list = append(list, map[string]any{
			"id": id, "messageId": commandID, "kind": kind, "status": status,
			"attempts": attempts, "lastError": lastErr, "startTime": createdAt, "updatedAt": updatedAt,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, pageEnvelope(page, size, total, list))
}

// HandleOpenAPIHistoryMessages GET .../history/messages：历史消息查询
// （上游 wework/message 面，其文档已标 deprecated 并推荐消息回调）。本面提供
// 入向事件元数据（inbound_events：eventId/at/digest）——消息正文按 §6.1
// 不落库不回显，字段面如实标注 deprecated + contentRetained=false。
func (s *Server) HandleOpenAPIHistoryMessages(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET", http.MethodGet) {
		return
	}
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	page, size := pageParams(r)
	list := []map[string]any{}
	total := 0
	if dev, paired, err := s.DB.GetDeviceByRobot(ctx, robotID); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	} else if paired {
		tx, txErr := s.DB.Tx(ctx)
		if txErr != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, txErr.Error())
			return
		}
		defer func() { _ = tx.Rollback() }()
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM inbound_events WHERE device_id = ?`, dev.DeviceID).Scan(&total); err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		rows, err := tx.QueryContext(ctx, `
SELECT event_id, payload_digest, at FROM inbound_events WHERE device_id = ?
ORDER BY at DESC LIMIT ? OFFSET ?`, dev.DeviceID, size, (page-1)*size)
		if err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		for rows.Next() {
			var eventID, digest string
			var at int64
			if err := rows.Scan(&eventID, &digest, &at); err != nil {
				rows.Close()
				writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
				return
			}
			list = append(list, map[string]any{"eventId": eventID, "digest": digest, "createTime": at})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{
		"deprecated": true, "note": "上游已标 deprecated（推荐消息回调）；本面仅入向事件元数据，消息正文不落库不回显（§6.1）",
		"page": pageEnvelope(page, size, total, list),
	})
}

// ---- f) 队列管理组 ----

// 队列操作审批参数指纹载荷（REV-SRV-05）：与指令提交 ApprovalDigest 同式绑定
// （target|type|payload）——清空绑定目标级操作，指定取消绑定到具体 commandID，
// 防「审批 A 清 B」。
const (
	queueClearPayload  = "openapi:queue:clear"
	queueCancelPrefix  = "openapi:queue:cancel:"
	queueTypeClear     = 304 // 上游清空队列语义
	queueTypeCancelOne = 305 // 上游清除指定指令语义
)

// requireQueueOpApproval 队列清理/指定取消的 L2 风险门（REV-SRV-05）：与 304 经
// /commands 提交同门（FR-53：清队列=L2 独立 approver）——此前 DELETE …/queue 与
// DELETE …/queue/{commandId} 仅 Bearer+Idempotency-Key 即可静默清队，绕过 FR-53
// 对同语义操作设的 L2 门；本门消除两面孔语义漂移。管理面（admin scope）豁免：
// admin 即决议面且操作落审计（05 矩阵登记）。
//
// 消费次序说明：一次性审批在操作执行**前**消费（条件 UPDATE 原子占位）——并发
// 同审批双删仅一方放行（防破坏性操作重放）；若操作中途失败审批已烧，重试登记
// 新审批（留痕可审计，取舍登记于此）。
func (s *Server) requireQueueOpApproval(w http.ResponseWriter, r *http.Request, token sqlite.APIToken, robotID, target string, commandType int, payload string) bool {
	if scheduler.ClassifyRisk(commandType) != scheduler.RiskL2 {
		return true // 护栏：分级表调整后非 L2 类型不设门（当前 304=表内 L2、305=未登记 fail-closed L2）
	}
	actor := "openapi:" + robotID
	approvalID := strings.TrimSpace(r.Header.Get("X-WT-Approval-Id"))
	ctx := r.Context()
	if approvalID == "" {
		a, err := s.Pipeline.RequestApproval(ctx, scheduler.ApprovalRequestInput{
			Tenant: token.Tenant, Actor: actor, Target: target,
			CommandType: commandType, Payload: payload,
			Reason: fmt.Sprintf("队列清理/取消操作（type=%d）按 304 同门 L2 审批（FR-53；REV-SRV-05 补齐）", commandType),
		})
		if err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return false
		}
		writeOpenAPIErrData(w, http.StatusAccepted, openapiCodeApprovalRequired,
			fmt.Sprintf("队列操作须独立 approver 确认（FR-53，与 304 指令同门）：审批 %s 已登记待决议", a.ApprovalID),
			map[string]any{"approvalId": a.ApprovalID, "status": "awaiting_approval", "commandType": commandType})
		return false
	}
	a, ok, err := s.DB.GetApproval(ctx, approvalID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return false
	}
	if !ok || a.Status != "approved" {
		writeOpenAPIErr(w, http.StatusForbidden, openapiCodeApprovalRequired,
			fmt.Sprintf("审批 %s 不存在或未 approved（决议后携 X-WT-Approval-Id 重试）", approvalID))
		return false
	}
	if a.Approver == "" || a.Approver == a.Actor {
		writeOpenAPIErr(w, http.StatusForbidden, openapiCodeApprovalRequired, "审批记录缺独立 approver（FR-53 自批拒绝）")
		return false
	}
	if digest := scheduler.ApprovalDigest(target, commandType, payload); digest != a.ParamsDigest {
		writeOpenAPIErr(w, http.StatusForbidden, openapiCodeApprovalRequired,
			"审批参数指纹与队列操作不一致（审批与操作参数绑定，防审批 A 清 B）")
		return false
	}
	if !a.Recurring {
		tx, txErr := s.DB.Tx(ctx)
		if txErr != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, txErr.Error())
			return false
		}
		consumed, cerr := s.DB.ConsumeApprovalTx(ctx, tx, approvalID, s.Now().UTC())
		if cerr != nil {
			_ = tx.Rollback()
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, cerr.Error())
			return false
		}
		if !consumed {
			_ = tx.Rollback()
			writeOpenAPIErr(w, http.StatusForbidden, openapiCodeApprovalRequired,
				fmt.Sprintf("审批 %s 已被消费（FR-53 一次性审批与单次操作绑定；重复操作须新审批）", approvalID))
			return false
		}
		if err := tx.Commit(); err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return false
		}
	}
	return true
}

// HandleOpenAPIQueue GET .../queue（查询）/ DELETE .../queue（清空）。
// 清空 = 上游 304 清队列语义：每条被清待执行指令落 101018 取消回执
// （errorReason 携操作指令 id），操作本身落审计。
func (s *Server) HandleOpenAPIQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleQueueGet(w, r)
	case http.MethodDelete:
		s.handleQueueClear(w, r)
	default:
		methodGuard(w, r, "GET, DELETE", http.MethodGet, http.MethodDelete)
	}
}

func (s *Server) handleQueueGet(w http.ResponseWriter, r *http.Request) {
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, false); !ok {
		return
	}
	ctx := r.Context()
	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= openapiQueueSnapshotCap {
		limit = v
	}
	queued, err := s.Pipeline.QueuedForTarget(ctx, target, limit)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	list := []map[string]any{}
	for i, c := range queued {
		list = append(list, map[string]any{
			"commandId": c.CommandID, "type": c.CommandType, "status": c.State,
			"riskClass": c.RiskClass, "position": i + 1, "createTime": c.CreatedAt.UnixMilli(),
		})
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess,
		map[string]any{"robotId": robotID, "target": target, "count": len(list), "list": list})
}

// handleQueueClear 清空队列（上游 304 语义；逐条取消 + 101018 回执）。
// L2 审批门（REV-SRV-05）：与 304 经 /commands 提交同门——未携审批号回 202
// 登记待审批，携已决议审批号方执行。
func (s *Server) handleQueueClear(w http.ResponseWriter, r *http.Request) {
	robotID := r.PathValue("robotId")
	token, ok := s.openapiGuard(w, r, robotID, true)
	if !ok {
		return
	}
	ctx := r.Context()
	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	if !s.requireQueueOpApproval(w, r, token, robotID, target, queueTypeClear, queueClearPayload) {
		return
	}
	queued, err := s.Pipeline.QueuedForTarget(ctx, target, openapiQueueSnapshotCap)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	opID := wire.NewUUID()
	cleared := []string{}
	for _, c := range queued {
		if err := s.Pipeline.CancelBeforeDelivery(ctx, c.CommandID, "openapi:"+robotID); err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001,
				fmt.Sprintf("清空中断（已清 %d 条）：%v", len(cleared), err))
			return
		}
		if err := s.enqueueCancelReceipt(ctx, c, "待执行指令被清空队列指令清理，取消指令messageId="+opID); err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		cleared = append(cleared, c.CommandID)
	}
	detail, _ := json.Marshal(map[string]any{"operationId": opID, "cleared": len(cleared)})
	_ = s.DB.InsertAudit(ctx, nil, auditKindOpenAPI+"_queue_clear", robotID, string(detail), s.Now().UTC())
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{
		"operationId": opID, "cleared": len(cleared), "commandIds": cleared,
	})
}

// enqueueCancelReceipt 为被清/被取消指令落 101018 执行回执（outbox exec_result；
// 与终态迁移同源形态，走 type=1 回调投递链路——上游 304/305 逐条回执语义）。
func (s *Server) enqueueCancelReceipt(ctx context.Context, c sqlite.Command, reason string) error {
	payload := map[string]any{
		"type": "exec_result", "commandId": c.CommandID, "commandType": c.CommandType,
		"errorCode": openapiUpstreamInstrCancelled, "errorReason": reason,
		"timeCost": 0, "finalState": "cancelled", "dryRun": c.DryRun,
		"at": s.Now().UTC().UnixMilli(),
	}
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		return err
	}
	if err := s.DB.EnqueueOutbox(ctx, tx, "exec_result", c.CommandID, payload, s.Now().UTC()); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// HandleOpenAPIQueueItem DELETE .../queue/{commandId}（指定清除 = 上游 305 语义；
// 仅 queued 可清，被清指令落 101018 回执）。L2 审批门（REV-SRV-05）：305 未登记
// 分级表按 L2 fail-closed，与 304 同门——审批指纹绑定到具体 commandID。
func (s *Server) HandleOpenAPIQueueItem(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "DELETE", http.MethodDelete) {
		return
	}
	robotID := r.PathValue("robotId")
	commandID := r.PathValue("commandId")
	token, ok := s.openapiGuard(w, r, robotID, true)
	if !ok {
		return
	}
	ctx := r.Context()
	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	cmd, ok, err := s.DB.GetCommand(ctx, commandID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	if !ok || cmd.Target != target {
		writeOpenAPIErr(w, http.StatusNotFound, openapiCodeNotFound, "指令不存在或不属于该 robot")
		return
	}
	if cmd.State != "queued" {
		writeOpenAPIErr(w, http.StatusConflict, openapiUpstreamIllegalOp,
			fmt.Sprintf("指令不在队列中（state=%s；仅 queued 待执行指令可清除）", cmd.State))
		return
	}
	if !s.requireQueueOpApproval(w, r, token, robotID, target, queueTypeCancelOne, queueCancelPrefix+commandID) {
		return
	}
	if err := s.Pipeline.CancelBeforeDelivery(ctx, commandID, "openapi:"+robotID); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	opID := wire.NewUUID()
	if err := s.enqueueCancelReceipt(ctx, cmd, "待执行指令被指定取消，取消指令messageId="+opID); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	_ = s.DB.InsertAudit(ctx, nil, auditKindOpenAPI+"_queue_cancel", robotID,
		fmt.Sprintf("commandId=%s op=%s", commandID, opID), s.Now().UTC())
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{
		"commandId": commandID, "status": "cancelled", "errorCode": openapiUpstreamInstrCancelled,
		"errorCodeName": OpenAPICodeName(openapiUpstreamInstrCancelled), "operationId": opID,
	})
}

// HandleOpenAPIQueuePriority POST .../queue/priority（插队；body {commandId}）：
// 把 queued 指令的队列序键（commands.created_at）抬到队首之前——下发泵
// （gateway FIFO）即先发它。真实提交时间以 command_events E01 行与审计留痕为
// 准（响应 originalCreateTime 同步携带原序键值；插队后指令行 created_at 即队列
// 序键，历史面 createTime 语义随之，此为插队副作用、留痕可审计）。
func (s *Server) HandleOpenAPIQueuePriority(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "POST", http.MethodPost) {
		return
	}
	robotID := r.PathValue("robotId")
	if _, ok := s.openapiGuard(w, r, robotID, true); !ok {
		return
	}
	ctx := r.Context()
	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	var body struct {
		CommandID string `json:"commandId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || strings.TrimSpace(body.CommandID) == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "请求体须为 JSON 对象 {commandId}")
		return
	}
	queued, err := s.Pipeline.QueuedForTarget(ctx, target, openapiQueueSnapshotCap)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	prev := 0
	found := false
	origMS := int64(0)
	for i, c := range queued {
		if c.CommandID == body.CommandID {
			prev, found, origMS = i+1, true, c.CreatedAt.UnixMilli()
			break
		}
	}
	if !found {
		writeOpenAPIErr(w, http.StatusNotFound, openapiCodeNotFound, "指令不存在、不属于该 robot 或不在队列中")
		return
	}
	adjusted := false
	if prev > 1 {
		head := queued[0].CreatedAt.UnixMilli()
		tx, txErr := s.DB.Tx(ctx)
		if txErr != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, txErr.Error())
			return
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE commands SET created_at = ? WHERE command_id = ? AND state = 'queued'`, head-1, body.CommandID); err != nil {
			_ = tx.Rollback()
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		if err := tx.Commit(); err != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
			return
		}
		adjusted = true
		_ = s.DB.InsertAudit(ctx, nil, auditKindOpenAPI+"_queue_priority", robotID,
			fmt.Sprintf("commandId=%s position %d→1（队列序键抬至队首前，原序键 %d 留痕于此）", body.CommandID, prev, origMS), s.Now().UTC())
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{
		"commandId": body.CommandID, "previousPosition": prev, "position": 1, "adjusted": adjusted,
		"originalCreateTime": origMS,
	})
}

// ---- 指令提交去重（101014）----

// queuedDuplicate 查同目标同 type 同载荷的排队指令（排除本键重放目标）。
// 命中返回该指令 id，未命中返回空串。
func (s *Server) queuedDuplicate(ctx context.Context, target string, commandType int, payload, excludeCommandID string) (string, error) {
	rows, err := s.DB.QueryDump(ctx, `
SELECT command_id FROM commands
WHERE target = ? AND command_type = ? AND payload = ? AND state = 'queued' AND dry_run = 0
LIMIT 1`, target, commandType, payload)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row[0] != excludeCommandID {
			return row[0], nil
		}
	}
	return "", nil
}
