package scheduler

import (
	"context"
	"encoding/json"
	"testing"
)

// TestExecuteDryRunCanonicalPath 验证 FR-54：干跑与真实执行同引擎同路径——
// E01→E03→E05→E09→E10 全边留痕（事件表可查），终态 succeeded，报告含
// 目标/参数/风险级；指令绝不下发设备（queued 拉取为空）。
func TestExecuteDryRunCanonicalPath(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 206, DryRun: true,
		Payload: `{"type":206,"targetRoom":"干跑建群目标","receivedContent":"公告内容"}`,
	})
	if err != nil {
		t.Fatalf("干跑提交失败: %v", err)
	}
	report, err := f.svc.ExecuteDryRun(ctx, res.CommandID)
	if err != nil {
		t.Fatalf("干跑执行失败: %v", err)
	}
	if !report.DryRun || report.Target != testTarget || report.CommandType != 206 {
		t.Fatalf("报告要素异常: %+v", report)
	}
	if report.RiskClass != RiskL1 {
		t.Fatalf("206 干跑报告风险级应 L1，实际 %s", report.RiskClass)
	}
	if report.PlannedAction == "" || report.Params["targetRoom"] != "干跑建群目标" {
		t.Fatalf("报告缺计划动作/参数: %+v", report)
	}
	// 终态 + 全边留痕（同引擎同路径）
	cmd, _, _ := f.db.GetCommand(ctx, res.CommandID)
	if cmd.State != "succeeded" || !cmd.DryRun {
		t.Fatalf("干跑终态异常: %+v", cmd)
	}
	events, _ := f.db.Events(ctx, res.CommandID)
	gotEdges := map[string]bool{}
	for _, e := range events {
		gotEdges[e.EdgeID] = true
	}
	for _, want := range []string{"E01", "E03", "E05", "E09", "E10"} {
		if !gotEdges[want] {
			t.Fatalf("干跑缺边 %s（事件：%v）", want, gotEdges)
		}
	}
	// 绝不下发设备：目标 queued 拉取不含干跑指令
	queued, _ := f.db.QueuedForTarget(ctx, testTarget, 64)
	if len(queued) != 0 {
		t.Fatalf("干跑指令不得进入设备下发队列，实际 %d 条", len(queued))
	}
	// 结果经 outbox（回调面同源，携带 dryRun 标记）
	items, _ := f.db.OutboxByCommand(ctx, res.CommandID)
	if len(items) != 1 || items[0].Kind != "exec_result" {
		t.Fatalf("干跑结果应入 outbox，实际 %+v", items)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(items[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["dryRun"] != true {
		t.Fatalf("outbox 载荷应携带 dryRun=true: %v", payload)
	}
}

// TestDryRunRejectsNonDryRun 验证非干跑指令拒绝服务端代执行（防真实指令被
// 服务端伪造结果）。
func TestDryRunRejectsNonDryRun(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.submit(t, 203, RiskL0)
	if _, err := f.svc.ExecuteDryRun(ctx, id); err == nil {
		t.Fatal("非干跑指令应拒绝服务端代执行")
	}
}

// TestPlannedActionNamesWMS5Coverage（S7 回归，2026-11-04 review）：W-M5 进入
// openapi 注册表的六类（236/237/238/239/240/244）此前缺中文名——干跑报告退化为
// 「指令 type=236」。锁名字已补齐（与 admin/catalog.go 对齐）；分级另由
// TestRiskClassify 锁 L2（补名 ≠ 降级）。
func TestPlannedActionNamesWMS5Coverage(t *testing.T) {
	want := map[int]string{
		236: "删除单向联系人", 237: "清理企微缓存", 238: "批量发送文本",
		239: "创建客户群活码", 240: "添加群公告网页", 244: "退出群",
	}
	for typ, name := range want {
		if got := plannedAction(typ); got != name {
			t.Fatalf("type=%d plannedAction 应 %q，实际 %q", typ, name, got)
		}
	}
}
