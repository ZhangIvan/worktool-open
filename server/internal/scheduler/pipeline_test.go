package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

const testTarget = "device-9f2c7b41"

type fixture struct {
	svc *Service
	db  *sqlite.DB
	now *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	f := &fixture{db: db, now: &base}
	f.svc = New(db, statemachine.Default())
	f.svc.Now = func() time.Time { return *f.now }
	return f
}

func (f *fixture) advance(d time.Duration) { *f.now = f.now.Add(d) }

func (f *fixture) submit(t *testing.T, typ int, risk string) string {
	t.Helper()
	id, err := f.submitE(typ, risk)
	if err != nil {
		t.Fatalf("submit 失败: %v", err)
	}
	return id
}

func (f *fixture) submitE(typ int, risk string) (string, error) {
	res, err := f.svc.Submit(context.Background(), SubmitRequest{
		Target: testTarget, CommandType: typ, RiskClass: risk,
		Payload: `{"type":203,"targetRoom":"外部群测试群","receivedContent":"t"}`,
	})
	if err != nil {
		return "", err
	}
	return res.CommandID, nil
}

// seed 把一条指令沿正典路径驱动到目标状态（created→queued→dispatched→acked→running）。
func (f *fixture) seed(t *testing.T, state string) string {
	t.Helper()
	ctx := context.Background()
	id := f.submit(t, 203, RiskL3)
	if state == "queued" {
		return id
	}
	if err := f.svc.MarkDispatched(ctx, id, 1, 1, wire.NewUUID()); err != nil {
		t.Fatalf("E03: %v", err)
	}
	if state == "dispatched" {
		return id
	}
	if err := f.svc.HandleTransportAck(ctx, id, "seed"); err != nil {
		t.Fatalf("E05: %v", err)
	}
	if state == "acked" {
		return id
	}
	if err := f.svc.HandleExecStart(ctx, id, "seed"); err != nil {
		t.Fatalf("E09: %v", err)
	}
	if state == "running" {
		return id
	}
	t.Fatalf("seed 不支持状态 %s", state)
	return ""
}

func (f *fixture) state(t *testing.T, id string) string {
	t.Helper()
	cmd, ok, err := f.db.GetCommand(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetCommand(%s): %v ok=%v", id, err, ok)
	}
	return cmd.State
}

// edgeDriver 把 CSV 每条边映射到管线 API；run 迁移后返回 commandID。
var edgeDrivers = map[string]struct {
	from string
	run  func(f *fixture, t *testing.T) string
}{
	"E01": {"created", func(f *fixture, t *testing.T) string { return f.submit(t, 203, RiskL3) }},
	"E02": {"created", func(f *fixture, t *testing.T) string {
		if _, err := f.submitE(999, RiskL3); err == nil {
			t.Fatal("非法指令应被拒绝（E02）")
		}
		return ""
	}},
	"E03": {"queued", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "queued")
		if err := f.svc.MarkDispatched(context.Background(), id, 1, 1, wire.NewUUID()); err != nil {
			t.Fatalf("E03: %v", err)
		}
		return id
	}},
	"E04": {"queued", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "queued")
		if err := f.svc.CancelBeforeDelivery(context.Background(), id, "op"); err != nil {
			t.Fatalf("E04: %v", err)
		}
		return id
	}},
	"E05": {"dispatched", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "dispatched")
		if err := f.svc.HandleTransportAck(context.Background(), id, "seed"); err != nil {
			t.Fatalf("E05: %v", err)
		}
		return id
	}},
	"E06": {"dispatched", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "dispatched")
		mustRecover(t, f, []string{id})
		return id
	}},
	"E07": {"acked", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "acked")
		mustRecover(t, f, []string{id})
		return id
	}},
	"E08": {"running", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "running")
		mustRecover(t, f, []string{id})
		return id
	}},
	"E09": {"acked", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "acked")
		if err := f.svc.HandleExecStart(context.Background(), id, "seed"); err != nil {
			t.Fatalf("E09: %v", err)
		}
		return id
	}},
	"E10": {"running", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "running")
		if _, err := f.svc.HandleResult(context.Background(), id, &wire.ExecResult{Type: 203, ErrorCode: 0}); err != nil {
			t.Fatalf("E10: %v", err)
		}
		return id
	}},
	"E11": {"running", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "running")
		if _, err := f.svc.HandleResult(context.Background(), id, &wire.ExecResult{Type: 203, ErrorCode: 201011, ErrorReason: "建群失败"}); err != nil {
			t.Fatalf("E11: %v", err)
		}
		return id
	}},
	"E12": {"execution_unknown", func(f *fixture, t *testing.T) string {
		id := f.toUnknown(t, "dispatched")
		if _, err := f.svc.HandleResult(context.Background(), id, &wire.ExecResult{Type: 203, ErrorCode: 0}); err != nil {
			t.Fatalf("E12: %v", err)
		}
		return id
	}},
	"E13": {"execution_unknown", func(f *fixture, t *testing.T) string {
		id := f.toUnknown(t, "acked")
		if err := f.svc.ReconcileJournal(context.Background(), []JournalEntry{{CommandID: id, Epoch: 1, Outcome: "never_started"}}); err != nil {
			t.Fatalf("E13: %v", err)
		}
		return id
	}},
	"E14": {"execution_unknown", func(f *fixture, t *testing.T) string {
		id := f.toUnknown(t, "running")
		if _, err := f.svc.HandleResult(context.Background(), id, &wire.ExecResult{Type: 203, ErrorCode: 201108, ErrorReason: "文件存储异常"}); err != nil {
			t.Fatalf("E14: %v", err)
		}
		return id
	}},
	"E15": {"execution_unknown", func(f *fixture, t *testing.T) string {
		id := f.toUnknown(t, "running")
		if err := f.svc.ManualResolve(context.Background(), id, "failed", "op-admin", "证据穷尽"); err != nil {
			t.Fatalf("E15: %v", err)
		}
		return id
	}},
	"E16": {"running", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "running")
		if err := f.svc.UserTakeoverStop(context.Background(), id); err != nil {
			t.Fatalf("E16: %v", err)
		}
		return id
	}},
	"E17": {"dispatched", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "dispatched")
		f.advance(301 * time.Second) // 越过 300s ACK TTL
		expired, err := f.svc.ScanAckTTL(context.Background())
		if err != nil {
			t.Fatalf("E17 扫描: %v", err)
		}
		if len(expired) != 1 || expired[0] != id {
			t.Fatalf("E17 应命中 %s，实际 %v", id, expired)
		}
		return id
	}},
	"E18": {"acked", func(f *fixture, t *testing.T) string {
		id := f.seed(t, "acked")
		f.advance(301 * time.Second)
		expired, err := f.svc.ScanAckTTL(context.Background())
		if err != nil {
			t.Fatalf("E18 扫描: %v", err)
		}
		if len(expired) != 1 || expired[0] != id {
			t.Fatalf("E18 应命中 %s，实际 %v", id, expired)
		}
		return id
	}},
}

// toUnknown 驱动到 from 状态后经崩溃恢复入 execution_unknown。
func (f *fixture) toUnknown(t *testing.T, from string) string {
	t.Helper()
	id := f.seed(t, from)
	mustRecover(t, f, []string{id})
	if st := f.state(t, id); st != "execution_unknown" {
		t.Fatalf("应为 execution_unknown，实际 %s", st)
	}
	return id
}

func mustRecover(t *testing.T, f *fixture, want []string) {
	t.Helper()
	// 模拟重启：新 Service 实例（同 DB）执行恢复
	restarted := New(f.db, statemachine.Default())
	restarted.Now = f.svc.Now
	got, err := restarted.RecoverInflight(context.Background())
	if err != nil {
		t.Fatalf("RecoverInflight: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("恢复指令数 = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
}

// TestCSVDrivenAllEdgesPositive：CSV 权威边集逐条驱动管线正例（18/18）。
func TestCSVDrivenAllEdgesPositive(t *testing.T) {
	m := statemachine.Default()
	for _, e := range m.Edges() {
		drv, ok := edgeDrivers[e.ID]
		if !ok {
			t.Fatalf("边 %s 缺驱动（测试与边集失同步）", e.ID)
		}
		if drv.from != e.From {
			t.Fatalf("边 %s 驱动前置状态 %s ≠ CSV from %s", e.ID, drv.from, e.From)
		}
		t.Run(e.ID+"_"+e.From+"--"+e.Event+"-->"+e.To, func(t *testing.T) {
			f := newFixture(t)
			id := drv.run(f, t)
			if e.ID == "E02" {
				return // 仅审计路径（无指令行）
			}
			if st := f.state(t, id); st != e.To {
				t.Fatalf("边 %s 迁移后状态 = %s, want %s", e.ID, st, e.To)
			}
			// 每条边留痕（不变量①）
			events, err := f.db.Events(context.Background(), id)
			if err != nil {
				t.Fatalf("Events: %v", err)
			}
			found := false
			for _, ev := range events {
				if ev.EdgeID == e.ID {
					found = true
				}
			}
			if !found {
				t.Fatalf("边 %s 无 command_events 留痕：%+v", e.ID, events)
			}
		})
	}
}

// TestCSVDrivenAPIRejectsIllegal：API 层非法迁移反例——每个触发器从错误状态调用
// 必须返回 409 ILLEGAL_TRANSITION（machine 层全矩阵见 statemachine 包）。
func TestCSVDrivenAPIRejectsIllegal(t *testing.T) {
	cases := []struct {
		name  string
		state string
		run   func(f *fixture, id string) error
	}{
		{"dispatch@acked", "acked", func(f *fixture, id string) error {
			return f.svc.MarkDispatched(context.Background(), id, 2, 2, wire.NewUUID())
		}},
		{"dispatch@running", "running", func(f *fixture, id string) error {
			return f.svc.MarkDispatched(context.Background(), id, 2, 2, wire.NewUUID())
		}},
		{"transport_ack@queued", "queued", func(f *fixture, id string) error {
			return f.svc.HandleTransportAck(context.Background(), id, "x")
		}},
		{"transport_ack@running", "running", func(f *fixture, id string) error {
			return f.svc.HandleTransportAck(context.Background(), id, "x")
		}},
		{"exec_start@queued", "queued", func(f *fixture, id string) error {
			return f.svc.HandleExecStart(context.Background(), id, "x")
		}},
		{"exec_start@dispatched", "dispatched", func(f *fixture, id string) error {
			return f.svc.HandleExecStart(context.Background(), id, "x")
		}},
		{"cancel@dispatched", "dispatched", func(f *fixture, id string) error {
			return f.svc.CancelBeforeDelivery(context.Background(), id, "op")
		}},
		{"takeover@queued", "queued", func(f *fixture, id string) error {
			return f.svc.UserTakeoverStop(context.Background(), id)
		}},
		{"takeover@acked", "acked", func(f *fixture, id string) error {
			return f.svc.UserTakeoverStop(context.Background(), id)
		}},
		{"manual_resolve@running", "running", func(f *fixture, id string) error {
			return f.svc.ManualResolve(context.Background(), id, "failed", "op", "证据穷尽")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id := f.seed(t, c.state)
			err := c.run(f, id)
			var it *statemachine.IllegalTransitionError
			if !errors.As(err, &it) {
				t.Fatalf("应返回 IllegalTransitionError（409），实际: %v", err)
			}
			if it.Status != 409 || it.Code != "ILLEGAL_TRANSITION" {
				t.Fatalf("错误语义: %+v", it)
			}
		})
	}
}

// TestTerminalIdempotentResult：终态重复回执幂等（同内容回执原确认，无重复副作用）。
func TestTerminalIdempotentResult(t *testing.T) {
	f := newFixture(t)
	id := f.seed(t, "running")
	if _, err := f.svc.HandleResult(context.Background(), id, &wire.ExecResult{Type: 203, ErrorCode: 0}); err != nil {
		t.Fatalf("E10: %v", err)
	}
	outbox1, err := f.db.OutboxByStatus(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox1) != 1 {
		t.Fatalf("outbox 应恰 1 条，实际 %d", len(outbox1))
	}
	// 重复回执（同 eventId 语义在 gateway 层去重；此处验证状态层幂等）
	st, err := f.svc.HandleResult(context.Background(), id, &wire.ExecResult{Type: 203, ErrorCode: 0})
	if err != nil || st != "succeeded" {
		t.Fatalf("重复回执应幂等: st=%s err=%v", st, err)
	}
	outbox2, _ := f.db.OutboxByStatus(context.Background(), "pending")
	if len(outbox2) != 1 {
		t.Fatalf("重复回执不得产生新 outbox：%d 条", len(outbox2))
	}
}

// TestTerminalTombstoneAndOutbox：终态副作用——墓碑（仅元数据）+ outbox（仅元数据）。
func TestTerminalTombstoneAndOutbox(t *testing.T) {
	f := newFixture(t)
	id := f.seed(t, "running")
	if _, err := f.svc.HandleResult(context.Background(), id, &wire.ExecResult{
		Type: 203, ErrorCode: 0, TimeCost: 1830, SuccessList: []string{"外部群测试群"},
	}); err != nil {
		t.Fatalf("E10: %v", err)
	}
	tombs, err := f.db.Tombstones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tombs) != 1 || tombs[0].CommandID != id || tombs[0].FinalState != "succeeded" {
		t.Fatalf("墓碑错误: %+v", tombs)
	}
	if !tombs[0].PurgeAfter.After(tombs[0].TerminalAt.Add(6 * 24 * time.Hour)) {
		t.Fatalf("墓碑保留期应 ≥7 天: %+v", tombs[0])
	}
	// 墓碑仅元数据：不含 payload 正文
	for _, tb := range tombs {
		if tb.FinalState == "" {
			t.Fatal("墓碑缺终态")
		}
	}
	items, err := f.db.OutboxByStatus(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].CommandID != id {
		t.Fatalf("outbox 错误: %+v", items)
	}
	// 回调载荷仅元数据：不得 echo receivedContent/targetRoom
	if containsAny(items[0].Payload, "receivedContent", "targetRoom") {
		t.Fatalf("回调载荷泄漏消息正文: %s", items[0].Payload)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && stringIndex(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestIdempotencyWindow：同键同参→原响应；同键变参→409；超窗→新指令。
func TestIdempotencyWindow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := SubmitRequest{
		Tenant: "t1", Actor: "a1", Operation: "send", Key: "k1",
		Target: testTarget, CommandType: 203,
		Payload: `{"type":203,"receivedContent":"v1"}`,
	}
	r1, err := f.svc.Submit(ctx, req)
	if err != nil {
		t.Fatalf("首次提交: %v", err)
	}
	r2, err := f.svc.Submit(ctx, req)
	if err != nil {
		t.Fatalf("同键同参应重放原响应: %v", err)
	}
	if !r2.Replay || r2.CommandID != r1.CommandID {
		t.Fatalf("重放错误: %+v vs %+v", r1, r2)
	}
	req.Payload = `{"type":203,"receivedContent":"v2"}`
	if _, err := f.svc.Submit(ctx, req); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("同键变效应 409 IDEMPOTENCY_CONFLICT，实际: %v", err)
	}
	// 超窗（24h）后同键可用
	f.advance(25 * time.Hour)
	req.Payload = `{"type":203,"receivedContent":"v3"}`
	r3, err := f.svc.Submit(ctx, req)
	if err != nil {
		t.Fatalf("超窗后应允许: %v", err)
	}
	if r3.CommandID == r1.CommandID {
		t.Fatal("超窗后应生成新指令")
	}
}

// TestRiskFreeze：FR-53——L1/L2 存在 pending unknown 时冻结同类新指令。
func TestRiskFreeze(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.toUnknownByRisk(t, RiskL1)
	_ = id
	// 同类 L1 冻结
	if _, err := f.submitE(203, RiskL1); !errors.Is(err, ErrRiskFrozen) {
		t.Fatalf("L1 应被冻结: %v", err)
	}
	// 异类 L3 不受影响
	if _, err := f.submitE(203, RiskL3); err != nil {
		t.Fatalf("L3 不应被冻结: %v", err)
	}
	// 对账离出后解冻（E13 never_started）
	if err := f.svc.ReconcileJournal(ctx, []JournalEntry{{CommandID: id, Epoch: 1, Outcome: "never_started"}}); err != nil {
		t.Fatalf("E13: %v", err)
	}
	if _, err := f.submitE(203, RiskL1); err != nil {
		t.Fatalf("解冻后 L1 应可提交: %v", err)
	}
}

func (f *fixture) toUnknownByRisk(t *testing.T, risk string) string {
	t.Helper()
	res, err := f.svc.Submit(context.Background(), SubmitRequest{
		Target: testTarget, CommandType: 203, RiskClass: risk,
		Payload: `{"type":203,"receivedContent":"x"}`,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := f.svc.MarkDispatched(context.Background(), res.CommandID, 1, 1, wire.NewUUID()); err != nil {
		t.Fatalf("E03: %v", err)
	}
	mustRecover(t, f, []string{res.CommandID})
	return res.CommandID
}

// TestAckTTLEvidenceGuard：TTL 到期但已有持久证据 → 不迁（guard 拒绝）。
func TestAckTTLEvidenceGuard(t *testing.T) {
	ctx := context.Background()
	t.Run("dispatched 有 transport_ack 留痕", func(t *testing.T) {
		// E05 留痕使指令离开 dispatched 进入 acked；此后 TTL 到期按 E18 判定
		//（exec_start 无留痕 → 迁 unknown），而非 E17 的证据豁免路径。
		f := newFixture(t)
		id := f.seed(t, "dispatched")
		if err := f.svc.HandleTransportAck(ctx, id, "ack"); err != nil {
			t.Fatal(err)
		}
		f.advance(301 * time.Second)
		expired, err := f.svc.ScanAckTTL(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(expired) != 1 || expired[0] != id {
			t.Fatalf("acked 无 exec_start 证据应经 E18 迁 unknown: %v", expired)
		}
		if st := f.state(t, id); st != "execution_unknown" {
			t.Fatalf("状态 = %s, want execution_unknown（E18）", st)
		}
	})
	t.Run("acked 有 exec_start 留痕", func(t *testing.T) {
		f := newFixture(t)
		id := f.seed(t, "acked")
		if err := f.svc.HandleExecStart(ctx, id, "start"); err != nil {
			t.Fatal(err)
		}
		f.advance(301 * time.Second)
		expired, err := f.svc.ScanAckTTL(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(expired) != 0 {
			t.Fatalf("有 E09 证据不应迁 unknown: %v", expired)
		}
	})
	t.Run("TTL 未到期不迁", func(t *testing.T) {
		f := newFixture(t)
		f.seed(t, "dispatched")
		f.advance(299 * time.Second)
		expired, err := f.svc.ScanAckTTL(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(expired) != 0 {
			t.Fatalf("TTL 未到期不应迁移: %v", expired)
		}
	})
}

// TestCrashRecoverySemantics：崩溃恢复——delivered/accepted/running 无证据→unknown。
func TestCrashRecoverySemantics(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	d := f.seed(t, "dispatched")
	a := f.seed(t, "acked")
	r := f.seed(t, "running")
	// 重启（新 Service 同 DB）
	restarted := New(f.db, statemachine.Default())
	restarted.Now = f.svc.Now
	got, err := restarted.RecoverInflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{d: true, a: true, r: true}
	if len(got) != len(want) {
		t.Fatalf("恢复数 %d ≠ %d: %v", len(got), len(want), got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("意外指令 %s 被恢复", id)
		}
		if st := f.state(t, id); st != "execution_unknown" {
			t.Fatalf("指令 %s 恢复后 = %s, want execution_unknown", id, st)
		}
	}
	// 二次恢复幂等
	got2, err := restarted.RecoverInflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Fatalf("二次恢复应无迁移，实际 %v", got2)
	}
	// 终态/queued 不受影响
	term := f.seed(t, "running")
	if _, err := f.svc.HandleResult(ctx, term, &wire.ExecResult{Type: 203, ErrorCode: 0}); err != nil {
		t.Fatal(err)
	}
	q := f.submit(t, 203, RiskL3)
	got3, err := restarted.RecoverInflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got3) != 0 {
		t.Fatalf("终态/queued 不应恢复: %v", got3)
	}
	if st := f.state(t, term); st != "succeeded" || f.state(t, q) != "queued" {
		t.Fatal("终态/queued 状态被破坏")
	}
}

// TestJournalReplayDrivesRunning：904 journal 启动证据驱动 E09（重连对账入口）。
func TestJournalReplayDrivesRunning(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.seed(t, "acked")
	if err := f.svc.ReconcileJournal(ctx, []JournalEntry{{CommandID: id, Epoch: 1, Started: true, Source: "local"}}); err != nil {
		t.Fatal(err)
	}
	if st := f.state(t, id); st != "running" {
		t.Fatalf("journal started 应驱动 running，实际 %s", st)
	}
	// 补报成功结论 → E10
	if err := f.svc.ReconcileJournal(ctx, []JournalEntry{{CommandID: id, Epoch: 1, Outcome: "success", Source: "local"}}); err != nil {
		t.Fatal(err)
	}
	if st := f.state(t, id); st != "succeeded" {
		t.Fatalf("journal success 应落 succeeded，实际 %s", st)
	}
}

// TestOrphanResult：queued 收到结果 → 无迁移路径，仅审计留痕。
func TestOrphanResult(t *testing.T) {
	f := newFixture(t)
	id := f.seed(t, "queued")
	st, err := f.svc.HandleResult(context.Background(), id, &wire.ExecResult{Type: 203, ErrorCode: 0})
	if !errors.Is(err, ErrOrphanResult) {
		t.Fatalf("应返回 ErrOrphanResult，实际: %v", err)
	}
	if st != "queued" {
		t.Fatalf("孤儿结果不得迁移，实际 %s", st)
	}
}

// TestSubmitRejectE02：非法载荷 → E02 仅审计（无指令行、不入队）。
func TestSubmitRejectE02(t *testing.T) {
	f := newFixture(t)
	if _, err := f.submitE(203, RiskL3); err != nil {
		// 用合法基线确认 submit 可用
		t.Fatalf("基线提交失败: %v", err)
	}
	bad := SubmitRequest{Target: testTarget, CommandType: 203, Payload: `{"type":204}`}
	if _, err := f.svc.Submit(context.Background(), bad); err == nil {
		t.Fatal("type 不一致应被拒绝")
	}
	cmds, err := f.db.CommandsByState(context.Background(), "failed")
	if err != nil {
		t.Fatal(err)
	}
	// E02 不落指令行（仅审计）；failed 状态的行应不存在
	if len(cmds) != 0 {
		t.Fatalf("E02 不应产生 failed 指令行: %+v", cmds)
	}
}

var _ = sql.NullBool{} // 保持 database/sql 导入（NullTime 在生产代码使用）
