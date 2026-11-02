package scheduler

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"cn.worktool.open/server/internal/wire"
)

// TestDryRunIdempotentRetryReturnsOriginalReport（CR-t32-T3.2-R1）：
// 干跑指令同键同参重试必须短路复用首次报告（spec §5.6.1 重试安全）——
// ExecuteDryRun 幂等可重入：终态重调只复建报告（零新边），不产生 500。
func TestDryRunIdempotentRetryReturnsOriginalReport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := SubmitRequest{
		Actor: "op-1", Key: "k-dry", Target: testTarget, CommandType: 206, DryRun: true,
		Payload: `{"type":206,"targetRoom":"干跑重试群","receivedContent":"公告"}`,
	}

	first, err := f.svc.Submit(ctx, req)
	if err != nil {
		t.Fatalf("首次干跑提交失败: %v", err)
	}
	rep1, err := f.svc.ExecuteDryRun(ctx, first.CommandID)
	if err != nil {
		t.Fatalf("首次干跑执行失败: %v", err)
	}

	retry, err := f.svc.Submit(ctx, req) // 同键同参重试
	if err != nil {
		t.Fatalf("同键同参重试不得报错: %v", err)
	}
	if !retry.Replay || retry.CommandID != first.CommandID {
		t.Fatalf("重试应重放原 commandId: %+v vs %+v", retry, first)
	}
	rep2, err := f.svc.ExecuteDryRun(ctx, retry.CommandID) // 幂等重入：终态复建报告
	if err != nil {
		t.Fatalf("终态重调应幂等复用报告（不得 500）: %v", err)
	}
	if !reflect.DeepEqual(rep1, rep2) {
		t.Fatalf("重建报告应与首次一致:\n%+v\n%+v", rep1, rep2)
	}
	// 重试不得产生第二条指令或新边事件
	cmds, _ := f.db.CommandsByState(ctx, "succeeded")
	if len(cmds) != 1 {
		t.Fatalf("重试不得新增指令，succeeded=%d", len(cmds))
	}
}

// TestDryRunResumeAfterInterruptedExecution（CR-R1-1）：首次干跑在边链中途
// 中断（停于 dispatched/acked/running）时，重调 ExecuteDryRun 从断点续跑收敛到
// succeeded 且报告完整——同幂等键不死键。每个中断点逐一验证。
func TestDryRunResumeAfterInterruptedExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		// interrupt 把 queued 干跑指令推进到该中断态（模拟链中崩溃）
		interrupt func(t *testing.T, f *fixture, id string)
	}{
		{"queued（提交后未起跑）", func(t *testing.T, f *fixture, id string) {}},
		{"dispatched（E03 后中断）", func(t *testing.T, f *fixture, id string) {
			if err := f.svc.MarkDispatched(context.Background(), id, 0, 0, wire.NewUUID()); err != nil {
				t.Fatalf("E03: %v", err)
			}
		}},
		{"acked（E05 后中断）", func(t *testing.T, f *fixture, id string) {
			if err := f.svc.MarkDispatched(context.Background(), id, 0, 0, wire.NewUUID()); err != nil {
				t.Fatalf("E03: %v", err)
			}
			if err := f.svc.HandleTransportAck(context.Background(), id, "dry-run"); err != nil {
				t.Fatalf("E05: %v", err)
			}
		}},
		{"running（E09 后中断）", func(t *testing.T, f *fixture, id string) {
			if err := f.svc.MarkDispatched(context.Background(), id, 0, 0, wire.NewUUID()); err != nil {
				t.Fatalf("E03: %v", err)
			}
			if err := f.svc.HandleTransportAck(context.Background(), id, "dry-run"); err != nil {
				t.Fatalf("E05: %v", err)
			}
			if err := f.svc.HandleExecStart(context.Background(), id, "dry-run"); err != nil {
				t.Fatalf("E09: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			res, err := f.svc.Submit(ctx, SubmitRequest{
				Actor: "op-1", Target: testTarget, CommandType: 206, DryRun: true,
				Payload: `{"type":206,"targetRoom":"干跑断点续跑群","receivedContent":"公告"}`,
			})
			if err != nil {
				t.Fatalf("提交失败: %v", err)
			}
			tc.interrupt(t, f, res.CommandID)

			report, err := f.svc.ExecuteDryRun(ctx, res.CommandID) // 同键重试的续跑路径
			if err != nil {
				t.Fatalf("断点续跑应收敛: %v", err)
			}
			if !report.DryRun || report.CommandType != 206 || report.RiskClass != RiskL1 {
				t.Fatalf("报告要素异常: %+v", report)
			}
			if st := f.state(t, res.CommandID); st != "succeeded" {
				t.Fatalf("续跑后应 succeeded，实际 %s", st)
			}
			// 全边留痕且无重复（E01/E03/E05/E09/E10 各恰一条）
			events, _ := f.db.Events(ctx, res.CommandID)
			got := map[string]int{}
			for _, e := range events {
				got[e.EdgeID]++
			}
			for _, want := range []string{"E01", "E03", "E05", "E09", "E10"} {
				if got[want] != 1 {
					t.Fatalf("边 %s 出现 %d 次（期望恰 1）：%v", want, got[want], got)
				}
			}
			// 终态幂等重入不新增边
			if _, err := f.svc.ExecuteDryRun(ctx, res.CommandID); err != nil {
				t.Fatalf("终态重调应幂等: %v", err)
			}
			events2, _ := f.db.Events(ctx, res.CommandID)
			if len(events2) != len(events) {
				t.Fatalf("终态重调不得新增边: %d → %d", len(events), len(events2))
			}
		})
	}
}

// TestIdempotentRetrySkipsGates（CR-t32-T3.2-R2）：幂等重放判定先于 FR-53 门链——
// 同键同参的合法重试直接返回原响应：不消费一次性审批（①）、不消耗 L1 限速额度（②）、
// 不被白名单收紧拦截（③）；同键变参仍 409（④）。
func TestIdempotentRetrySkipsGates(t *testing.T) {
	ctx := context.Background()

	t.Run("L2 一次性审批已消费后重试不 403", func(t *testing.T) {
		f := newFixture(t)
		const payload = `{"type":219,"targetRoom":"待解散群","receivedContent":""}`
		ap, err := f.svc.RequestApproval(ctx, ApprovalRequestInput{
			Actor: "op-1", Target: testTarget, CommandType: 219, Payload: payload, Reason: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.svc.DecideApproval(ctx, ap.ApprovalID, "approver-a", true, ""); err != nil {
			t.Fatal(err)
		}
		req := SubmitRequest{Actor: "op-1", Key: "k-l2", Target: testTarget,
			CommandType: 219, Payload: payload, ApprovalID: ap.ApprovalID}
		first, err := f.svc.Submit(ctx, req)
		if err != nil {
			t.Fatalf("首提交失败: %v", err)
		}
		second, err := f.svc.Submit(ctx, req) // 审批已 consumed，无重放短路则 APPROVAL_MISMATCH
		if err != nil {
			t.Fatalf("同键同参重试被门链拦截（应重放原响应）: %v", err)
		}
		if !second.Replay || second.CommandID != first.CommandID {
			t.Fatalf("重试应重放: %+v vs %+v", second, first)
		}
	})

	t.Run("L1 限速窗口打满后重试不计数", func(t *testing.T) {
		f := newFixture(t)
		f.svc.SetGuardConfig(guardConfigOf(1)) // 1 条/分钟
		payload := `{"type":229,"targetRoom":"限速群","receivedContent":"x"}`
		req := SubmitRequest{Actor: "op-1", Key: "k-l1", Target: testTarget, CommandType: 229, Payload: payload}
		if _, err := f.svc.Submit(ctx, req); err != nil {
			t.Fatalf("首提交失败: %v", err)
		}
		if _, err := f.svc.Submit(ctx, req); err != nil { // 重试：不得因额度耗尽 429
			t.Fatalf("同键同参重试被限速拦截: %v", err)
		}
		if _, err := f.svc.Submit(ctx, SubmitRequest{Actor: "op-1", Key: "k-l1-b", Target: testTarget,
			CommandType: 229, Payload: payload}); err == nil {
			t.Fatal("窗口内新提交应被限速（限速语义未被重试破坏）")
		} else if code := guardCode(t, err); code != ErrCodeRateLimited {
			t.Fatalf("期望 RATE_LIMITED，实际 %s", code)
		}
	})

	t.Run("白名单收紧后重试不被 403", func(t *testing.T) {
		f := newFixture(t)
		const room = "白名单群"
		payload := `{"type":229,"targetRoom":"` + room + `","receivedContent":"x"}`
		req := SubmitRequest{Actor: "op-1", Key: "k-wl", Target: testTarget, CommandType: 229, Payload: payload}
		if _, err := f.svc.Submit(ctx, req); err != nil { // 未配置白名单：放行
			t.Fatalf("首提交失败: %v", err)
		}
		if err := f.svc.AddWhitelist(ctx, testTarget, "另一个群", "admin-1"); err != nil { // 收紧且不含原接收人
			t.Fatal(err)
		}
		if _, err := f.svc.Submit(ctx, req); err != nil { // 重试：不得因收紧被拒
			t.Fatalf("同键同参重试被白名单收紧拦截: %v", err)
		}
	})

	t.Run("同键变参仍 409（先于门链）", func(t *testing.T) {
		f := newFixture(t)
		req := SubmitRequest{Actor: "op-1", Key: "k-x", Target: testTarget, CommandType: 203,
			Payload: `{"type":203,"targetRoom":"g","receivedContent":"a"}`}
		if _, err := f.svc.Submit(ctx, req); err != nil {
			t.Fatal(err)
		}
		req.Payload = `{"type":203,"targetRoom":"g","receivedContent":"b"}`
		if _, err := f.svc.Submit(ctx, req); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("同键变适应 ErrIdempotencyConflict，实际 %v", err)
		}
	})
}

// TestApprovalConsumeAtomicWithEnqueue（CR-t32-T3.2-R3）：一次性审批消费与 E01 入队
// 同事务——入队前的失败（风险冻结）不得烧掉审批；冻结解除后同一审批可完成提交并被消费。
func TestApprovalConsumeAtomicWithEnqueue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const payload = `{"type":219,"targetRoom":"待解散群","receivedContent":""}`

	// 冻结 L2：同风险级 pending execution_unknown（L2 入队需审批，seed 走全链）
	seedApproval := func(payload string) string {
		t.Helper()
		ap, err := f.svc.RequestApproval(ctx, ApprovalRequestInput{
			Actor: "op-1", Target: testTarget, CommandType: 219, Payload: payload, Reason: "seed"})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.svc.DecideApproval(ctx, ap.ApprovalID, "approver-seed", true, ""); err != nil {
			t.Fatal(err)
		}
		return ap.ApprovalID
	}
	seedPayload := `{"type":219,"targetRoom":"冻结种子群","receivedContent":""}`
	seedRes, err := f.svc.Submit(ctx, SubmitRequest{Actor: "op-1", Target: testTarget,
		CommandType: 219, Payload: seedPayload, ApprovalID: seedApproval(seedPayload)})
	if err != nil {
		t.Fatalf("种子提交失败: %v", err)
	}
	if err := f.svc.MarkDispatched(ctx, seedRes.CommandID, 1, 1, wire.NewUUID()); err != nil {
		t.Fatalf("种子 E03: %v", err)
	}
	mustRecover(t, f, []string{seedRes.CommandID})
	unknownID := seedRes.CommandID

	ap, err := f.svc.RequestApproval(ctx, ApprovalRequestInput{
		Actor: "op-1", Target: testTarget, CommandType: 219, Payload: payload, Reason: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DecideApproval(ctx, ap.ApprovalID, "approver-a", true, ""); err != nil {
		t.Fatal(err)
	}

	// 提交在审批校验之后、入队之前被风险冻结拒绝 → 审批必须仍是 approved（未被烧掉）
	if _, err := f.svc.Submit(ctx, SubmitRequest{Actor: "op-1", Target: testTarget,
		CommandType: 219, Payload: payload, ApprovalID: ap.ApprovalID}); !errors.Is(err, ErrRiskFrozen) {
		t.Fatalf("期望 ErrRiskFrozen，实际 %v", err)
	}
	row, ok, _ := f.svc.GetApprovalRow(ctx, ap.ApprovalID)
	if !ok || row.Status != "approved" {
		t.Fatalf("入队失败时审批不得被消费（应 approved），实际 %+v", row)
	}

	// 冻结解除（unknown 经 E12 离出）后，同一审批完成提交：入队成功才消费
	if _, err := f.svc.HandleResult(ctx, unknownID, &wire.ExecResult{Type: 219, ErrorCode: 0}); err != nil {
		t.Fatalf("解除冻结失败: %v", err)
	}
	res, err := f.svc.Submit(ctx, SubmitRequest{Actor: "op-1", Target: testTarget,
		CommandType: 219, Payload: payload, ApprovalID: ap.ApprovalID})
	if err != nil {
		t.Fatalf("解冻后提交失败: %v", err)
	}
	if res.State != "queued" {
		t.Fatalf("应入队，实际 %s", res.State)
	}
	row, _, _ = f.svc.GetApprovalRow(ctx, ap.ApprovalID)
	if row.Status != "consumed" {
		t.Fatalf("入队成功后一次性审批应 consumed，实际 %s", row.Status)
	}
}

func guardConfigOf(max int) (cfg GuardConfig) {
	cfg = DefaultGuardConfig()
	cfg.L1MaxPerTx = max
	return cfg
}
