// cron 定时任务（FR-38：定时触发指令/群发；任务卡 T3.2）。
//
// 定时调度属服务端（docs/02 §5.2 事实源分工；端侧仅事件触发规则与模式 A 本地
// cron）。任务到点经统一命令管线 Submit 入队（E01），复用全部投递/幂等/风控语义：
//   - 幂等：每次触发的幂等键 = "cron-<jobId>-<计划触发时刻>"（崩溃重启后重复
//     Tick 同一计划时刻命中幂等重放，不重复副作用）；
//   - L2 任务：创建时须绑定常设审批（Recurring=true），每次触发复验审批仍
//     approved，失效即跳过 + 审计；
//   - 堆积保护：上次触发指令尚未终态（离线设备积压）时跳过本次触发 + 审计，
//     next_fire 照常推进。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// CronJobInput 为创建定时任务的输入。
type CronJobInput struct {
	Name        string
	Tenant      string
	Actor       string
	Target      string // 统一目标键（v2 deviceId / v1:robotId）
	CommandType int
	Payload     string // list[0] 原文 JSON（与单发指令同一校验）
	CronExpr    string // 5 字段
	RiskClass   string // 空 = 按类型分级
	ApprovalID  string // L2 任务必填（常设审批）
}

// CreateCronJob 创建任务（表达式解析 + 首次 next_fire 计算 + L2 审批复验）。
func (s *Service) CreateCronJob(ctx context.Context, in CronJobInput) (sqlite.CronJob, error) {
	expr, err := ParseCron(in.CronExpr)
	if err != nil {
		return sqlite.CronJob{}, err
	}
	if err := validatePayload(in.CommandType, in.Payload); err != nil {
		return sqlite.CronJob{}, err
	}
	risk := in.RiskClass
	if risk == "" {
		risk = ClassifyRisk(in.CommandType) // 恒 L0/L1/L2（未登记→L2 fail-closed，docs/01 FR-53 裁决②）
	}
	now := s.Now().UTC()
	if risk == RiskL2 {
		if in.ApprovalID == "" {
			return sqlite.CronJob{}, &GuardError{Code: ErrCodeApprovalNeeded,
				Msg: "L2 定时任务须绑定常设审批（Recurring=true）后创建（FR-53）"}
		}
		a, ok, err := s.db.GetApproval(ctx, in.ApprovalID)
		if err != nil {
			return sqlite.CronJob{}, err
		}
		if !ok || a.Status != "approved" || !a.Recurring {
			return sqlite.CronJob{}, &GuardError{Code: ErrCodeApprovalMismatch,
				Msg: fmt.Sprintf("绑定审批 %s 须为 approved 且常设（Recurring）", in.ApprovalID)}
		}
		if a.ParamsDigest != ApprovalDigest(in.Target, in.CommandType, in.Payload) {
			return sqlite.CronJob{}, &GuardError{Code: ErrCodeApprovalMismatch,
				Msg: "绑定审批参数指纹与任务参数不一致"}
		}
	}
	next, ok := expr.Next(now)
	if !ok {
		return sqlite.CronJob{}, fmt.Errorf("scheduler/cron: 表达式 %q 在 4 年内无可触发时刻", in.CronExpr)
	}
	job := sqlite.CronJob{
		JobID:       s.newID(),
		Name:        in.Name,
		Tenant:      in.Tenant,
		Actor:       in.Actor,
		Target:      in.Target,
		CommandType: in.CommandType,
		Payload:     in.Payload,
		CronExpr:    in.CronExpr,
		RiskClass:   risk,
		ApprovalID:  in.ApprovalID,
		Enabled:     true,
		NextFireAt:  next,
		CreatedAt:   now,
	}
	if err := s.db.InsertCronJob(ctx, job); err != nil {
		return sqlite.CronJob{}, err
	}
	if err := s.db.InsertAudit(ctx, nil, "cron_job_created", job.JobID,
		fmt.Sprintf("name=%s target=%s type=%d expr=%s risk=%s next=%s", in.Name, in.Target, in.CommandType, in.CronExpr, risk, next.UTC().Format(time.RFC3339)), now); err != nil {
		return sqlite.CronJob{}, err
	}
	return job, nil
}

// CronJobs 列出全部任务（运维/测试）。
func (s *Service) CronJobs(ctx context.Context) ([]sqlite.CronJob, error) { return s.db.CronJobs(ctx) }

// SetCronJobEnabled 启停任务（审计留痕）。
func (s *Service) SetCronJobEnabled(ctx context.Context, jobID string, enabled bool, by string) error {
	if err := s.db.SetCronJobEnabled(ctx, jobID, enabled); err != nil {
		return err
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	return s.db.InsertAudit(ctx, nil, "cron_job_"+state, jobID, "by="+by, s.Now().UTC())
}

// TickCron 执行一次到期扫描：到期任务逐条触发（Submit 入队）并推进 next_fire。
// 返回本次触发产生的 commandId 列表。可重入（幂等键防重复副作用）。
func (s *Service) TickCron(ctx context.Context) ([]string, error) {
	now := s.Now().UTC()
	due, err := s.db.DueCronJobs(ctx, now, 64)
	if err != nil {
		return nil, err
	}
	exprs := map[string]*CronExpr{}
	var fired []string
	for _, job := range due {
		expr, ok := exprs[job.CronExpr]
		if !ok {
			parsed, err := ParseCron(job.CronExpr)
			if err != nil {
				return fired, fmt.Errorf("scheduler/cron: 任务 %s 表达式损坏 %q: %w", job.JobID, job.CronExpr, err)
			}
			exprs[job.CronExpr] = parsed
			expr = parsed
		}
		commandID, err := s.fireCronJob(ctx, job, expr, now)
		if err != nil {
			return fired, err
		}
		if commandID != "" {
			fired = append(fired, commandID)
		}
	}
	return fired, nil
}

// fireCronJob 触发单个任务（堆积保护 → L2 审批复验 → Submit → 推进 next_fire）。
func (s *Service) fireCronJob(ctx context.Context, job sqlite.CronJob, expr *CronExpr, now time.Time) (string, error) {
	// 堆积保护：上次触发指令尚未终态（设备离线积压）→ 跳过本次，next_fire 照常推进
	if job.LastCommandID != "" {
		prev, ok, err := s.db.GetCommand(ctx, job.LastCommandID)
		if err != nil {
			return "", err
		}
		if ok && !s.m.IsTerminal(prev.State) {
			next, has := expr.Next(now)
			if !has {
				return "", nil
			}
			if err := s.db.UpdateCronJobFire(ctx, job.JobID, job.LastCommandID, prev.CreatedAt, next); err != nil {
				return "", err
			}
			return "", s.db.InsertAudit(ctx, nil, "cron_skip_inflight", job.JobID,
				fmt.Sprintf("上次触发 %s 仍处 %s（未终态），跳过本次触发（堆积保护）", prev.CommandID, prev.State), now)
		}
	}
	risk := job.RiskClass
	if risk == "" {
		risk = ClassifyRisk(job.CommandType) // 恒 L0/L1/L2（未登记→L2 fail-closed）
	}
	// 幂等键绑定计划触发时刻：崩溃重启后重复 Tick 命中同键同参重放
	idemKey := fmt.Sprintf("cron-%s-%d", job.JobID, job.NextFireAt.UnixMilli())
	res, err := s.Submit(ctx, SubmitRequest{
		Tenant:            job.Tenant,
		Actor:             job.Actor,
		Operation:         "cron.fire",
		Key:               idemKey,
		Target:            job.Target,
		CommandType:       job.CommandType,
		Payload:           job.Payload,
		RiskClass:         risk,
		ApprovalID:        job.ApprovalID,
		ParamsFingerprint: idemKey, // 同键重试安全：指纹取幂等键本身（参数=模板+计划时刻）
	})
	next, has := expr.Next(now)
	if !has {
		return "", fmt.Errorf("scheduler/cron: 任务 %s 表达式 %q 无后续触发时刻", job.JobID, job.CronExpr)
	}
	commandID := res.CommandID
	if err != nil {
		// 触发失败（熔断/风控/审批失效等）：审计留痕，next_fire 照常推进（不反复撞同一刻）
		if auditErr := s.db.InsertAudit(ctx, nil, "cron_fire_rejected", job.JobID,
			fmt.Sprintf("submit 失败: %v（next 推进至 %s）", err, next.UTC().Format(time.RFC3339)), now); auditErr != nil {
			return "", auditErr
		}
		commandID = ""
	}
	if err := s.db.UpdateCronJobFire(ctx, job.JobID, commandID, now, next); err != nil {
		return "", err
	}
	return commandID, nil
}

// RunCronScheduler 周期扫描入口（main 挂 goroutine；间隔默认 15s，最小粒度=1 分钟）。
func (s *Service) RunCronScheduler(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := s.TickCron(ctx); err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				// 单任务失败不终止调度循环（审计已留痕）
				continue
			}
		}
	}
}

// newID 生成业务行 ID（复用 wire UUID 生成器）。
func (s *Service) newID() string { return wire.NewUUID() }
