// cron 定时任务管理面（W-ops 审计项①）：把 internal/scheduler 已有 cron 内核
// （CreateCronJob/TickCron/启停）接到管理台——列表/创建（含 cron 表达式校验与
// next 触发预览）/启停/删除。复用 scheduler 公开导出面，不在本包重写调度语义；
// 写操作落审计（cron 内核侧已留痕 cron_job_created/enabled/disabled/deleted）。
//
// 纪律：
//   - 全部端点 requireAdmin（与 console.go 同面）；
//   - 创建走 scheduler.CreateCronJob（L2 须绑定常设审批、表达式 5 字段解析、
//     payload 与单发指令同一校验——全链复用，不旁路）；
//   - 管理面创建**不暴露 riskClass 入参**：CreateCronJob 的 RiskClass 空值 =
//     按类型分级（未登记类型 L2 fail-closed，FR-53 裁决②）——显式传级可把高危
//     类型强制降级绕过审批门，故本面恒空（请求体携带 riskClass 直接拒绝）。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/store/sqlite"
)

// cronPreviewLimitDefault/Max 为触发预览条数（创建响应与独立预览端点共用）。
const (
	cronPreviewLimitDefault = 5
	cronPreviewLimitMax     = 10
)

// adminCronJobRow 为 cron 任务列表投影（lastResult = 最近触发指令的终态投影）。
type adminCronJobRow struct {
	JobID       string `json:"jobId"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	Type        int    `json:"type"`
	CronExpr    string `json:"cronExpr"`
	RiskClass   string `json:"riskClass"`
	ApprovalID  string `json:"approvalId"`
	Enabled     bool   `json:"enabled"`
	LastFireMs  int64  `json:"lastFireMs"` // 0 = 从未触发
	LastCommand string `json:"lastCommandId"`
	// LastResult 为最近触发指令状态（终态名优先，未终态=state 原值；空=从未触发）。
	LastResult  string `json:"lastResult"`
	NextFireMs  int64  `json:"nextFireMs"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// cronJobRow 投影单条（lastResult 经 commands 表回查——cron_jobs 只存
// last_command_id，终态事实源在 commands；行缺失（指令被对账清理）→ 空串）。
func (s *Server) cronJobRow(ctx context.Context, job sqlite.CronJob) adminCronJobRow {
	row := adminCronJobRow{
		JobID: job.JobID, Name: job.Name, Target: job.Target, Type: job.CommandType,
		CronExpr: job.CronExpr, RiskClass: job.RiskClass, ApprovalID: job.ApprovalID,
		Enabled: job.Enabled, LastCommand: job.LastCommandID,
		NextFireMs: job.NextFireAt.UnixMilli(), CreatedAtMs: job.CreatedAt.UnixMilli(),
	}
	if job.LastFireAt.Valid {
		row.LastFireMs = job.LastFireAt.Time.UnixMilli()
	}
	if job.LastCommandID != "" {
		if cmd, ok, err := s.db.GetCommand(ctx, job.LastCommandID); err == nil && ok {
			row.LastResult = cmd.State
			if cmd.TerminalState.Valid && cmd.TerminalState.String != "" {
				row.LastResult = cmd.TerminalState.String
			}
		}
	}
	return row
}

// handleCronJobList GET /api/v1/admin/cron-jobs：全部任务列表（enabled /
// last_result / next_fire 逐条投影）。
func (s *Server) handleCronJobList(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	jobs, err := s.pipeline.CronJobs(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	items := make([]adminCronJobRow, 0, len(jobs))
	for _, j := range jobs {
		items = append(items, s.cronJobRow(r.Context(), j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": items, "count": len(items)})
}

// adminCronJobCreateRequest 创建请求（payload 优先；否则 {type, ...params} 合成，
// 与 handleCommandSubmit 同式；无 riskClass——见包注释纪律）。
type adminCronJobCreateRequest struct {
	Name       string          `json:"name"`
	Target     string          `json:"target"`
	Type       int             `json:"type"`
	Params     map[string]any  `json:"params"`
	Payload    json.RawMessage `json:"payload"`
	CronExpr   string          `json:"cronExpr"`
	ApprovalID string          `json:"approvalId"`
	Actor      string          `json:"actor"`
}

// handleCronJobCreate POST /api/v1/admin/cron-jobs：创建任务。cron 表达式先本地
// 解析（400 快败 + next 触发预览），再走 scheduler.CreateCronJob（L2 审批门/
// payload 校验/审计全链复用）。
func (s *Server) handleCronJobCreate(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	var req adminCronJobCreateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_BODY",
			"请求体解析失败/含未定义字段（须为 {name,target,type,params|payload,cronExpr,approvalId,actor}；不支持 riskClass——恒按类型分级）"}).write(w)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Target = strings.TrimSpace(req.Target)
	req.CronExpr = strings.TrimSpace(req.CronExpr)
	req.ApprovalID = strings.TrimSpace(req.ApprovalID)
	req.Actor = strings.TrimSpace(req.Actor)
	if req.Actor == "" {
		req.Actor = "admin"
	}
	if req.Name == "" || len(req.Name) > 128 {
		(&adminError{http.StatusBadRequest, "INVALID_NAME", "name 必填且 ≤128 字符"}).write(w)
		return
	}
	if req.Target == "" {
		(&adminError{http.StatusBadRequest, "INVALID_TARGET", "target 必填（deviceId 或 v1:robotId）"}).write(w)
		return
	}
	if req.CronExpr == "" {
		(&adminError{http.StatusBadRequest, "INVALID_CRON_EXPR", "cronExpr 必填（5 字段：min hour dom mon dow）"}).write(w)
		return
	}
	now := s.now().UTC()
	preview, err := scheduler.PreviewCronNexts(req.CronExpr, now, cronPreviewLimitDefault)
	if err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_CRON_EXPR", err.Error()}).write(w)
		return
	}
	// 载荷合成：payload 优先（须为对象）；否则 {type:N, ...params}。
	payload := ""
	if len(req.Payload) > 0 {
		payload = string(req.Payload)
	} else {
		obj := map[string]any{"type": req.Type}
		for k, v := range req.Params {
			obj[k] = v
		}
		raw, merr := json.Marshal(obj)
		if merr != nil {
			(&adminError{http.StatusBadRequest, "INVALID_PAYLOAD", "params 序列化失败: " + merr.Error()}).write(w)
			return
		}
		payload = string(raw)
	}
	job, err := s.pipeline.CreateCronJob(r.Context(), scheduler.CronJobInput{
		Name: req.Name, Tenant: "default", Actor: req.Actor, Target: req.Target,
		CommandType: req.Type, Payload: payload, CronExpr: req.CronExpr,
		ApprovalID: req.ApprovalID,
	})
	if err != nil {
		var ge *scheduler.GuardError
		if errors.As(err, &ge) {
			// 创建面语义：缺常设审批/参数指纹不一致=创建被拒（非「待审批」流转——
			// cron 任务没有提交即待审批的中继态），统一 403/403 + 门码。
			status := http.StatusForbidden
			if ge.Code == scheduler.ErrCodeApprovalMismatch {
				status = http.StatusConflict
			}
			(&adminError{status, ge.Code, ge.Msg}).write(w)
			return
		}
		// 表达式已预检；此处余下为 payload/类型校验拒绝（E02 同源）。
		(&adminError{http.StatusBadRequest, "INVALID_CRON_INPUT", err.Error()}).write(w)
		return
	}
	_ = s.db.InsertAudit(r.Context(), nil, "admin_cron_job_created", job.JobID,
		"管理面创建定时任务 name="+req.Name+" expr="+req.CronExpr, now)
	previewMs := make([]int64, 0, len(preview))
	previewUTC := make([]string, 0, len(preview))
	for _, t := range preview {
		previewMs = append(previewMs, t.UnixMilli())
		previewUTC = append(previewUTC, t.UTC().Format(time.RFC3339))
	}
	row := s.cronJobRow(r.Context(), job)
	writeJSON(w, http.StatusCreated, map[string]any{
		"job": row, "nextFireMs": row.NextFireMs,
		"previewMs": previewMs, "previewUtc": previewUTC,
	})
}

// handleCronJobPreview GET /api/v1/admin/cron-jobs/preview?expr=&limit=：表达式
// 预检 + next 触发预览（创建表单先用，不落库）。
func (s *Server) handleCronJobPreview(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	expr := strings.TrimSpace(r.URL.Query().Get("expr"))
	if expr == "" {
		(&adminError{http.StatusBadRequest, "INVALID_CRON_EXPR", "须带 ?expr=（5 字段 cron 表达式）"}).write(w)
		return
	}
	limit := cronPreviewLimitDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= cronPreviewLimitMax {
			limit = n
		}
	}
	preview, err := scheduler.PreviewCronNexts(expr, s.now().UTC(), limit)
	if err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_CRON_EXPR", err.Error()}).write(w)
		return
	}
	nextMs := make([]int64, 0, len(preview))
	nextUTC := make([]string, 0, len(preview))
	for _, t := range preview {
		nextMs = append(nextMs, t.UnixMilli())
		nextUTC = append(nextUTC, t.UTC().Format(time.RFC3339))
	}
	writeJSON(w, http.StatusOK, map[string]any{"cronExpr": expr, "previewMs": nextMs, "previewUtc": nextUTC, "count": len(nextMs)})
}

// handleCronJobEnabled POST /api/v1/admin/cron-jobs/{jobId}/enabled：启停
//（body {enabled: bool}；审计由 scheduler.SetCronJobEnabled 落 cron_job_enabled/
// disabled）。
func (s *Server) handleCronJobEnabled(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	jobID := r.PathValue("jobId")
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Enabled == nil {
		(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为 {enabled: true|false}"}).write(w)
		return
	}
	ctx := r.Context()
	if _, ok, err := s.pipeline.GetCronJob(ctx, jobID); err != nil {
		s.storeError(w, err)
		return
	} else if !ok {
		(&adminError{http.StatusNotFound, "CRON_JOB_NOT_FOUND", "定时任务 " + jobID + " 不存在"}).write(w)
		return
	}
	if err := s.pipeline.SetCronJobEnabled(ctx, jobID, *req.Enabled, "admin"); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": jobID, "enabled": *req.Enabled})
}

// handleCronJobDelete DELETE /api/v1/admin/cron-jobs/{jobId}：删除任务（审计
// cron_job_deleted；历史触发指令行保留）。
func (s *Server) handleCronJobDelete(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	jobID := r.PathValue("jobId")
	if err := s.pipeline.DeleteCronJob(r.Context(), jobID, "admin"); err != nil {
		if errors.Is(err, sqlite.ErrNotFound) {
			(&adminError{http.StatusNotFound, "CRON_JOB_NOT_FOUND", "定时任务 " + jobID + " 不存在"}).write(w)
			return
		}
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": jobID, "deleted": true})
}
