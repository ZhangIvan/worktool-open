// /openapi/v1/* 端点（FR-34 apifox 兼容面 P0 子集；任务卡 T3.2）：
//
//	POST /openapi/v1/robots/{robotId}/commands            提交指令（指令报文 = v1
//	                                                     WeworkMessageBean 单条；
//	                                                     幂等键 Idempotency-Key 强制）
//	GET  /openapi/v1/robots/{robotId}/commands/{commandId} RAW_CONFIRM 结果查询
//
// 响应信封沿用旧面形态 {code, message, data}（code=0 成功；错误码族见
// openapi_registry.go 注释）。干跑：X-WT-Dry-Run: true（FR-54，服务端沿同一
// 状态机产出报告，绝不下发设备）。
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cn.worktool.open/server/internal/scheduler"
)

// openapiMaxBody 兼容面请求体上限（指令报文含 fileBase64 场景，512KiB）。
const openapiMaxBody = 512 << 10

// HandleOpenAPICommand 处理 POST /openapi/v1/robots/{robotId}/commands。
func (s *Server) HandleOpenAPICommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeOpenAPIErr(w, http.StatusMethodNotAllowed, 101001, "仅 POST")
		return
	}
	robotID := r.PathValue("robotId")
	ctx := r.Context()
	now := s.Now().UTC()

	// 60QPM 限流（NFR §2.2，apifox 限制值对齐；先于鉴权计数按 robot 维度）
	if !s.openapiLimiter.allow(robotID, now) {
		writeOpenAPIErr(w, http.StatusTooManyRequests, openapiCodeRateLimited,
			fmt.Sprintf("请求超出限流 %d 次/%s（60QPM，对齐 apifox 限制值）",
				s.openapiCfg.RateLimitPerMinute, s.openapiCfg.RateWindow))
		return
	}
	token, ae := s.authenticate(ctx, r, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}

	// 幂等键强制（docs/06 借鉴②：教训扩展到全部 POST 创建类端点）
	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idemKey == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeIdemKeyRequired,
			"缺少 Idempotency-Key 请求头（全部 POST 创建类端点强制，spec §5.6.1）")
		return
	}

	// 指令报文 = v1 兼容载荷单条（list[0] 原文）；未知顶层字段宽容（同信封纪律）
	body := http.MaxBytesReader(w, r.Body, openapiMaxBody)
	var probe map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&probe); err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeInvalidBody, "请求体须为 JSON 对象（v1 指令报文单条）")
		return
	}
	rawType, ok := probe["type"]
	if !ok {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeInvalidBody, "指令报文缺 type 字段（v1 WeworkMessageBean.type）")
		return
	}
	var commandType int
	if err := json.Unmarshal(rawType, &commandType); err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeInvalidBody, "type 须为整数")
		return
	}
	// WTO-004 碰撞拒绝：仅注册表内的云→端指令可经兼容面提交（上报/心跳类
	// 11/101/102/301/302 与未定义类型一律拒绝，裸数字绝不直接映射 operation）
	intent := OpenAPIIntent(commandType)
	if intent == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeUnknownCommand,
			fmt.Sprintf("指令 type=%d 不在兼容面注册表（上报/心跳/未定义类型不可经 REST 提交；注册表见 openapi_registry.go）", commandType))
		return
	}

	target, ae := s.resolveTarget(ctx, robotID)
	if ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
	dryRun := isDryRunHeader(r.Header.Get("X-WT-Dry-Run"))
	// L2 指令携审批提交（第一次提交返回的 approvalId；FR-53 审批门）
	approvalID := strings.TrimSpace(r.Header.Get("X-WT-Approval-Id"))
	payload, _ := json.Marshal(probe) // 原文回写（键序规范化，语义不变）

	res, err := s.Pipeline.Submit(ctx, scheduler.SubmitRequest{
		Tenant:      token.Tenant,
		Actor:       "openapi:" + robotID,
		Operation:   intent, // 命名空间隔离 intent（非裸数字）
		Key:         idemKey,
		Target:      target,
		CommandType: commandType,
		Payload:     string(payload),
		ApprovalID:  approvalID,
		DryRun:      dryRun,
	})
	if err != nil {
		s.writeSubmitError(w, res, err)
		return
	}

	data := map[string]any{
		"commandId": res.CommandID,
		"status":    res.State,
		"dryRun":    dryRun,
		"replay":    res.Replay,
	}
	// 干跑：沿同一状态机产出"将要做什么"报告（FR-54），随响应返回。
	// ExecuteDryRun 幂等可重入（CR-t32-T3.2-R1 + CR-R1-1）：首次提交走全链；
	// 同键重试按指令现态收敛——succeeded 直接复建报告（零新边），首次链中中断
	// （queued/dispatched/acked/running）从断点续跑到 succeeded，不死键、不 500。
	if dryRun && res.CommandID != "" {
		report, derr := s.Pipeline.ExecuteDryRun(ctx, res.CommandID)
		if derr != nil {
			writeOpenAPIErr(w, http.StatusInternalServerError, 101001, "干跑执行失败: "+derr.Error())
			return
		}
		data["status"] = "succeeded"
		data["dryRunReport"] = report
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, data)
}

// writeSubmitError 把管线/门错误映射到旧面错误码信封。
func (s *Server) writeSubmitError(w http.ResponseWriter, res scheduler.SubmitResult, err error) {
	var ge *scheduler.GuardError
	switch {
	case errors.As(err, &ge):
		switch ge.Code {
		case scheduler.ErrCodeApprovalNeeded:
			// 202 语义：指令未入队，待独立 approver 决议（FR-53）
			writeOpenAPIErrData(w, http.StatusAccepted, openapiCodeApprovalRequired, ge.Msg, map[string]any{
				"approvalId": res.ApprovalID,
				"status":     "awaiting_approval",
			})
		case scheduler.ErrCodeRateLimited:
			writeOpenAPIErr(w, http.StatusTooManyRequests, openapiCodeRateLimited, ge.Msg)
		case scheduler.ErrCodeNotWhitelisted:
			writeOpenAPIErr(w, http.StatusForbidden, openapiCodeNotWhitelisted, ge.Msg)
		case scheduler.ErrCodeCircuitOpen:
			writeOpenAPIErr(w, http.StatusServiceUnavailable, openapiCodeCircuitOpen, ge.Msg)
		case scheduler.ErrCodeApprovalMismatch, scheduler.ErrCodeApproverConflict:
			writeOpenAPIErr(w, http.StatusForbidden, openapiCodeApprovalRequired, ge.Msg)
		default:
			writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeInvalidBody, ge.Msg)
		}
	case errors.Is(err, scheduler.ErrIdempotencyConflict):
		writeOpenAPIErr(w, http.StatusConflict, openapiCodeIdemConflict,
			"同幂等键变参（409 IDEMPOTENCY_CONFLICT，spec §5.6.1）："+err.Error())
	case errors.Is(err, scheduler.ErrRiskFrozen):
		writeOpenAPIErr(w, http.StatusForbidden, openapiCodeRiskFrozen, err.Error())
	default:
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeInvalidBody, err.Error())
	}
}

// HandleOpenAPIResult 处理 GET /openapi/v1/robots/{robotId}/commands/{commandId}
// （RAW_CONFIRM 结果查询：终态指令回传 socketType=3 ExecCallbackBean 语义字段）。
func (s *Server) HandleOpenAPIResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeOpenAPIErr(w, http.StatusMethodNotAllowed, 101001, "仅 GET")
		return
	}
	robotID := r.PathValue("robotId")
	commandID := r.PathValue("commandId")
	ctx := r.Context()
	now := s.Now().UTC()

	if !s.openapiLimiter.allow(robotID, now) {
		writeOpenAPIErr(w, http.StatusTooManyRequests, openapiCodeRateLimited, "请求超出限流（60QPM）")
		return
	}
	if _, ae := s.authenticate(ctx, r, robotID); ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return
	}
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
	data := map[string]any{
		"commandId": cmd.CommandID,
		"type":      cmd.CommandType,
		"status":    cmd.State,
		"dryRun":    cmd.DryRun,
		"riskClass": cmd.RiskClass,
		"createdAt": cmd.CreatedAt.UnixMilli(),
		"terminal":  s.Machine.IsTerminal(cmd.State),
	}
	if cmd.TerminalState.Valid {
		data["finalState"] = cmd.TerminalState.String
	}
	// 终态：从 exec_result 出站行回填 RAW_CONFIRM 字段（与服务端回调同源）
	if s.Machine.IsTerminal(cmd.State) {
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
				data["errorCode"] = cb["errorCode"]
				data["errorReason"] = cb["errorReason"]
				data["timeCost"] = cb["timeCost"]
				data["successList"] = cb["successList"]
				data["failList"] = cb["failList"]
			}
			break
		}
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, data)
}

// ---- 信封与辅助 ----

type openapiEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func writeOpenAPIData(w http.ResponseWriter, status, code int, data any) {
	writeJSON(w, status, openapiEnvelope{Code: code, Message: "success", Data: data})
}

func writeOpenAPIErr(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, openapiEnvelope{Code: code, Message: msg})
}

// writeOpenAPIErrData 为带 data 载荷的错误信封（如 APPROVAL_REQUIRED 携带审批号）。
func writeOpenAPIErrData(w http.ResponseWriter, status, code int, msg string, data any) {
	writeJSON(w, status, openapiEnvelope{Code: code, Message: msg, Data: data})
}

// openapiHTTPCode 把内部 apiError 的机读码映射到兼容面错误码段。
func openapiHTTPCode(ae *apiError) int {
	switch ae.Code {
	case "INVALID_TOKEN":
		return openapiCodeInvalidToken
	case "ROBOT_MISMATCH":
		return openapiCodeRobotMismatch
	default:
		return openapiCodeInvalidBody
	}
}

func isDryRunHeader(v string) bool {
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return b
}
