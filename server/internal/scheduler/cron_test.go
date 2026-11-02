package scheduler

import (
	"context"
	"testing"
	"time"

	"cn.worktool.open/server/internal/wire"
)

// finish 把指令沿正典路径驱动到 succeeded（测试辅助）。
func (f *fixture) finish(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	if err := f.svc.MarkDispatched(ctx, id, 1, 1, wire.NewUUID()); err != nil {
		t.Fatalf("E03: %v", err)
	}
	if err := f.svc.HandleTransportAck(ctx, id, "t"); err != nil {
		t.Fatalf("E05: %v", err)
	}
	if err := f.svc.HandleExecStart(ctx, id, "t"); err != nil {
		t.Fatalf("E09: %v", err)
	}
	if _, err := f.svc.HandleResult(ctx, id, &wire.ExecResult{Type: 203, ErrorCode: 0}); err != nil {
		t.Fatalf("E10: %v", err)
	}
}

// TestCronJobFireAtDueTime 验证 FR-38：表达式到点触发 → 经统一管线入队（E01），
// next_fire 推进；未到点不触发。
func TestCronJobFireAtDueTime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.CreateCronJob(ctx, CronJobInput{
		Name: "整点报时", Tenant: "tn1", Actor: "op-1", Target: testTarget,
		CommandType: 203, Payload: `{"type":203,"targetRoom":"外部群测试群","receivedContent":"整点报时"}`,
		CronExpr: "30 9 * * *", // 每天 09:30
	}); err != nil {
		t.Fatalf("创建 cron 任务失败: %v", err)
	}
	// 当前时刻 2026-11-02 10:00 → 未到点（下一个 09:30 在明日）
	fired, err := f.svc.TickCron(ctx)
	if err != nil {
		t.Fatalf("TickCron: %v", err)
	}
	if len(fired) != 0 {
		t.Fatalf("未到点不应触发，实际触发 %d 条", len(fired))
	}
	// 推进到 11-03 09:29:59（仍差 1 秒）
	*f.now = time.Date(2026, 11, 3, 9, 29, 59, 0, time.UTC)
	if again, _ := f.svc.TickCron(ctx); len(again) != 0 {
		t.Fatalf("差 1 秒不应触发")
	}
	// 到点
	*f.now = time.Date(2026, 11, 3, 9, 30, 1, 0, time.UTC)
	fired, err = f.svc.TickCron(ctx)
	if err != nil {
		t.Fatalf("TickCron: %v", err)
	}
	if len(fired) != 1 {
		t.Fatalf("到点应触发 1 条，实际 %d", len(fired))
	}
	cmd, ok, err := f.db.GetCommand(ctx, fired[0])
	if err != nil || !ok {
		t.Fatalf("GetCommand: %v ok=%v", err, ok)
	}
	if cmd.Target != testTarget || cmd.CommandType != 203 || cmd.State != "queued" {
		t.Fatalf("触发指令字段异常: %+v", cmd)
	}
	// next_fire 推进到次日 09:30
	jobs, _ := f.svc.CronJobs(ctx)
	if !jobs[0].NextFireAt.Equal(time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("next_fire 未推进: %s", jobs[0].NextFireAt)
	}
	// 崩溃重放保护：先把上次触发指令推到终态（排除堆积保护路径），再回拨
	// next_fire 到同一计划时刻重复 Tick —— 幂等键命中，重放同 commandId
	f.finish(t, fired[0])
	snapshot := jobs[0]
	if err := f.db.UpdateCronJobFire(ctx, snapshot.JobID, snapshot.LastCommandID, snapshot.LastFireAt.Time,
		time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("回拨 next_fire 失败: %v", err)
	}
	refired, rerr := f.svc.TickCron(ctx)
	if rerr != nil {
		t.Fatalf("重复 Tick 失败: %v", rerr)
	}
	if len(refired) != 1 || refired[0] != fired[0] {
		t.Fatalf("同计划时刻重复触发应幂等重放同 commandId=%s，实际 %v", fired[0], refired)
	}
	queued, _ := f.db.CommandsByState(ctx, "queued")
	if len(queued) != 0 {
		t.Fatalf("幂等重放不应新增 queued 指令，实际 %d", len(queued))
	}
}

// TestCronPileupSkip 验证堆积保护：上次触发指令未终态（queued，设备离线）时
// 跳过本次触发，next_fire 照常推进。
func TestCronPileupSkip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	*f.now = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	_, err := f.svc.CreateCronJob(ctx, CronJobInput{
		Name: "每分钟", Target: testTarget, CommandType: 203,
		Payload:  `{"type":203,"targetRoom":"g","receivedContent":"m"}`,
		CronExpr: "* * * * *",
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	f.advance(time.Minute + time.Second) // 09:01:01 → 首个计划时刻 09:01:00 到点
	fired, _ := f.svc.TickCron(ctx)
	if len(fired) != 1 {
		t.Fatalf("首次应触发")
	}
	f.advance(time.Minute) // 09:02:01（上次指令仍 queued）
	fired, _ = f.svc.TickCron(ctx)
	if len(fired) != 0 {
		t.Fatalf("上次指令未终态应跳过（堆积保护），实际触发 %d", len(fired))
	}
	jobs, _ := f.svc.CronJobs(ctx)
	if !jobs[0].NextFireAt.Equal(time.Date(2026, 11, 2, 9, 3, 0, 0, time.UTC)) {
		t.Fatalf("跳过时 next_fire 应推进至 09:03，实际 %s", jobs[0].NextFireAt)
	}
}

// TestCronL2RequiresStandingApproval 验证 L2 cron 任务须绑定常设审批（FR-53）。
func TestCronL2RequiresStandingApproval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	payload := `{"type":219,"targetRoom":"待解散群","receivedContent":""}`
	// 无审批 → 拒绝创建
	if _, err := f.svc.CreateCronJob(ctx, CronJobInput{
		Target: testTarget, CommandType: 219, Payload: payload, CronExpr: "0 3 * * *",
	}); err == nil {
		t.Fatal("L2 cron 无审批应拒绝创建")
	}
	// 建一次性审批（非 recurring）→ 仍拒绝
	oneShot, err := f.svc.RequestApproval(ctx, ApprovalRequestInput{
		Actor: "op-1", Target: testTarget, CommandType: 219, Payload: payload,
	})
	if err != nil {
		t.Fatalf("登记审批失败: %v", err)
	}
	if err := f.svc.DecideApproval(ctx, oneShot.ApprovalID, "approver-a", true, "放行"); err != nil {
		t.Fatalf("决议失败: %v", err)
	}
	if _, err := f.svc.CreateCronJob(ctx, CronJobInput{
		Target: testTarget, CommandType: 219, Payload: payload, CronExpr: "0 3 * * *",
		ApprovalID: oneShot.ApprovalID,
	}); err == nil {
		t.Fatal("一次性审批绑定周期任务应拒绝（须常设）")
	}
	// 常设审批 → 创建成功并按计划触发
	standing, err := f.svc.RequestApproval(ctx, ApprovalRequestInput{
		Actor: "op-1", Target: testTarget, CommandType: 219, Payload: payload, Recurring: true,
	})
	if err != nil {
		t.Fatalf("登记常设审批失败: %v", err)
	}
	if err := f.svc.DecideApproval(ctx, standing.ApprovalID, "approver-a", true, "常设放行"); err != nil {
		t.Fatalf("决议失败: %v", err)
	}
	job, err := f.svc.CreateCronJob(ctx, CronJobInput{
		Target: testTarget, CommandType: 219, Payload: payload, CronExpr: "* * * * *",
		ApprovalID: standing.ApprovalID,
	})
	if err != nil {
		t.Fatalf("常设审批创建失败: %v", err)
	}
	f.advance(time.Minute + time.Second)
	fired, _ := f.svc.TickCron(ctx)
	if len(fired) != 1 {
		t.Fatalf("L2 任务到点应触发入队，实际 %d", len(fired))
	}
	cmd, _, _ := f.db.GetCommand(ctx, fired[0])
	if cmd.RiskClass != RiskL2 {
		t.Fatalf("219 应分级 L2，实际 %s", cmd.RiskClass)
	}
	_ = job
}

// TestCronDisabled 验证停用任务不触发。
func TestCronDisabled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.CreateCronJob(ctx, CronJobInput{
		Target: testTarget, CommandType: 203,
		Payload:  `{"type":203,"targetRoom":"g","receivedContent":"m"}`,
		CronExpr: "* * * * *",
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := f.svc.SetCronJobEnabled(ctx, (func() string {
		jobs, _ := f.svc.CronJobs(ctx)
		return jobs[0].JobID
	})(), false, "op-1"); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	f.advance(time.Minute)
	if fired, _ := f.svc.TickCron(ctx); len(fired) != 0 {
		t.Fatalf("停用任务不应触发")
	}
}
