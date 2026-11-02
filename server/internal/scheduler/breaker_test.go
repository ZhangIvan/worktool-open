package scheduler

import (
	"context"
	"testing"
	"time"

	"cn.worktool.open/server/internal/wire"
)

// driveTerminal 把一条 queued 指令沿正典路径推到真实终态（成功/失败），
// 为熔断器制造真实执行样本。
func (f *fixture) driveTerminal(t *testing.T, id string, errorCode int) {
	t.Helper()
	ctx := context.Background()
	if err := f.svc.MarkDispatched(ctx, id, 1, 1, wire.NewUUID()); err != nil {
		t.Fatalf("E03: %v", err)
	}
	_ = f.svc.HandleTransportAck(ctx, id, "t")
	_ = f.svc.HandleExecStart(ctx, id, "t")
	if _, err := f.svc.HandleResult(ctx, id, &wire.ExecResult{Type: 203, ErrorCode: errorCode}); err != nil {
		t.Fatalf("结果处理: %v", err)
	}
}

// submitReal 提交一条真实执行指令（非干跑）。
func (f *fixture) submitReal(t *testing.T) string {
	t.Helper()
	res, err := f.svc.Submit(context.Background(), SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 203,
		Payload: `{"type":203,"targetRoom":"g","receivedContent":"x"}`,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return res.CommandID
}

// TestBreakerUnitStates 单元级：closed 样本不足不翻开；达阈值翻开；
// 冷却后 half-open；试探成功闭合 / 试探失败重开。
func TestBreakerUnitStates(t *testing.T) {
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	b := NewBreaker(BreakerConfig{Window: time.Minute, MinSamples: 4, Threshold: 0.5, Cooldown: time.Minute, CloseSamples: 2})
	clock := base
	b.SetClock(func() time.Time { return clock })
	if b.State() != BreakerClosed {
		t.Fatalf("初始应 closed")
	}
	// 3 样本（2 失败）不足 MinSamples=4 → 不翻开
	for i, ok := range []bool{false, false, true} {
		if _, _, tripped := b.Record(base.Add(time.Duration(i)*time.Second), ok); tripped {
			t.Fatal("样本不足不应翻开")
		}
	}
	if b.State() != BreakerClosed {
		t.Fatalf("应仍 closed，实际 %s", b.State())
	}
	// 第 4 样本失败 → 失败率 3/4=75% ≥50% → 翻开
	if _, next, tripped := b.Record(base.Add(4*time.Second), false); !tripped || next != BreakerOpen {
		t.Fatalf("达阈值应翻开，next=%s tripped=%v", next, tripped)
	}
	// open 期间 Allow 拒绝
	clock = base.Add(30 * time.Second)
	if ok, retry := b.Allow(); ok {
		t.Fatal("open 期间应拒绝新提交")
	} else if retry <= 0 {
		t.Fatal("应给出剩余冷却")
	}
	// 冷却期内失败结果不参与判定（不重置冷却）
	if _, _, tripped := b.Record(base.Add(31*time.Second), false); tripped {
		t.Fatal("冷却期内迟到结果不应再次翻开")
	}
	// 冷却到期（openedAt=base+4s + 60s）→ half-open，放行试探
	clock = base.Add(65 * time.Second)
	if ok, _ := b.Allow(); !ok {
		t.Fatal("冷却到期应放行试探")
	}
	if b.State() != BreakerHalfOpen {
		t.Fatalf("冷却后应 half-open，实际 %s", b.State())
	}
	// 试探失败 → 重开
	if _, next, tripped := b.Record(base.Add(66*time.Second), false); !tripped || next != BreakerOpen {
		t.Fatalf("试探失败应重开，next=%s", next)
	}
	// 再次冷却 → 2 连成功 → 闭合
	clock = base.Add(3 * time.Minute)
	b.Allow() // 触发 half-open
	if _, next, _ := b.Record(clock, true); next != BreakerHalfOpen {
		t.Fatalf("第一成功应保持 half-open，实际 %s", next)
	}
	if _, next, _ := b.Record(clock.Add(time.Second), true); next != BreakerClosed {
		t.Fatalf("第二成功应闭合，实际 %s", next)
	}
	// 人工复位
	b.Record(base.Add(4*time.Minute), false) // 保持有样本
	if !b.Reset() && b.State() != BreakerClosed {
		t.Fatal("Reset 语义异常")
	}
}

// TestBreakerServiceIntegration 服务级：错误率超阈值 → 审计 circuit_breaker_open
// + 新提交被拒（自动暂停，FR-53）；冷却 + 连续成功 → 自动恢复 + 审计 closed。
func TestBreakerServiceIntegration(t *testing.T) {
	f := newFixture(t)
	f.svc.breaker = NewBreaker(BreakerConfig{Window: 5 * time.Minute, MinSamples: 4, Threshold: 0.5, Cooldown: time.Minute, CloseSamples: 2})
	f.svc.breaker.SetClock(func() time.Time { return f.svc.Now().UTC() })
	ctx := context.Background()

	// 4 条真实执行：1 成功 3 失败 = 75% ≥ 50% → 翻开
	id1 := f.submitReal(t)
	f.driveTerminal(t, id1, 0)
	for i := 0; i < 3; i++ {
		id := f.submitReal(t)
		f.driveTerminal(t, id, 201011) // 执行错误（20xxx）
	}
	rows, err := f.db.QueryDump(ctx, `SELECT kind, subject FROM audit_log WHERE kind='circuit_breaker_open'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("翻开熔断应落审计事件，实际 %d 行", len(rows))
	}
	// 自动暂停：新提交被拒（E02 仅审计）
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 203,
		Payload: `{"type":203,"targetRoom":"g","receivedContent":"x"}`,
	}); err == nil {
		t.Fatal("熔断期间新提交应被拒绝")
	} else if code := guardCode(t, err); code != ErrCodeCircuitOpen {
		t.Fatalf("期望 CIRCUIT_OPEN，实际 %s", code)
	}

	// 冷却到期 → half-open 放行；2 连真实成功 → 自动闭合 + 审计
	f.advance(2 * time.Minute)
	for i := 0; i < 2; i++ {
		id := f.submitReal(t)
		f.advance(time.Second)
		f.driveTerminal(t, id, 0)
	}
	if f.svc.Breaker().State() != BreakerClosed {
		t.Fatalf("试探成功后应闭合，实际 %s", f.svc.Breaker().State())
	}
	rows, err = f.db.QueryDump(ctx, `SELECT kind FROM audit_log WHERE kind='circuit_breaker_closed'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("自动恢复应落审计事件，实际 %d 行", len(rows))
	}
	// 闭合后新提交恢复
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 203,
		Payload: `{"type":203,"targetRoom":"g","receivedContent":"x"}`,
	}); err != nil {
		t.Fatalf("闭合后应恢复提交: %v", err)
	}
}

// TestBreakerIgnoresDryRun 验证干跑结果不计入熔断样本（FR-54 不污染 FR-53 统计）：
// 大量干跑失败率 100% 也不翻开。
func TestBreakerIgnoresDryRun(t *testing.T) {
	f := newFixture(t)
	f.svc.breaker = NewBreaker(BreakerConfig{Window: time.Minute, MinSamples: 2, Threshold: 0.5, Cooldown: time.Minute, CloseSamples: 2})
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		res, err := f.svc.Submit(ctx, SubmitRequest{
			Actor: "op-1", Target: testTarget, CommandType: 203, DryRun: true,
			Payload: `{"type":203,"targetRoom":"g","receivedContent":"x"}`,
		})
		if err != nil {
			t.Fatalf("干跑提交失败: %v", err)
		}
		if _, err := f.svc.ExecuteDryRun(ctx, res.CommandID); err != nil {
			t.Fatalf("干跑执行失败: %v", err)
		}
	}
	if f.svc.Breaker().State() != BreakerClosed {
		t.Fatalf("干跑样本不应触发熔断，实际 %s", f.svc.Breaker().State())
	}
}
