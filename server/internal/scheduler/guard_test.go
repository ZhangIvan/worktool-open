package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// guardCode 提取 GuardError.Code（无则失败）。
func guardCode(t *testing.T, err error) string {
	t.Helper()
	var ge *GuardError
	if !errors.As(err, &ge) {
		t.Fatalf("期望 GuardError，实际 %v", err)
	}
	return ge.Code
}

func TestRiskClassify(t *testing.T) {
	cases := map[int]string{
		203: RiskL0, // 已实现普通指令 → L0（docs/01 FR-53 裁决③）
		212: RiskL0, // 通过好友请求（端侧已实现；REST 面不注册但分级为 L0，双侧同级）
		206: RiskL1, // 建群
		207: RiskL1, // 改群（拉人/踢人）
		213: RiskL1, // 手机号加好友（CR-R1-0：与端侧静态表/docs/10 B2 对齐）
		220: RiskL1, // 群内加好友（CR-R1-0：与端侧静态表/docs/10 B2 对齐）
		229: RiskL1, // 批量发送
		230: RiskL1, // 合并发送
		219: RiskL2, // 解散群
		234: RiskL2, // 删联系人
		304: RiskL2, // 清队列
		215: RiskL2, // 未登记（上游未定义）→ L2 fail-closed（裁决②）
		231: RiskL2, // 未登记 → L2 fail-closed
		999: RiskL2, // 未登记 → L2 fail-closed
	}
	for typ, want := range cases {
		got := ClassifyRisk(typ)
		if got != want {
			t.Errorf("type=%d 分级 got=%q want=%q", typ, got, want)
		}
	}
}

// TestUnregisteredTypeFailClosed（docs/01 FR-53 裁决②，CR-R1-0 收尾）：未登记类型
// 默认按 L2 fail-closed——提交即 APPROVAL_REQUIRED（不产生指令）并以「未登记」为
// 理由标记（审计 + 审批行）；独立 approver 决议后携审批提交入队，RiskClass=L2。
func TestUnregisteredTypeFailClosed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const payload = `{"type":215,"targetRoom":"任意群","receivedContent":"x"}`

	res, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 215, Payload: payload,
	})
	if err == nil {
		t.Fatal("未登记类型应 fail-closed 进入审批门")
	} else if code := guardCode(t, err); code != ErrCodeApprovalNeeded {
		t.Fatalf("期望 APPROVAL_REQUIRED，实际 %s", code)
	}
	if res.State != "awaiting_approval" || res.ApprovalID == "" {
		t.Fatalf("应登记待审批: %+v", res)
	}
	if cmds, _ := f.db.CommandsByState(ctx, "queued"); len(cmds) != 0 {
		t.Fatalf("未审批不得入队，实际 %d", len(cmds))
	}
	// 「未登记」标记：审计留痕 + 审批行理由
	rows, err := f.db.QueryDump(ctx, `SELECT kind FROM audit_log WHERE kind='risk_unregistered'`)
	if err != nil || len(rows) != 1 {
		t.Fatalf("缺 risk_unregistered 审计标记: %v %v", rows, err)
	}
	ap, ok, _ := f.svc.GetApprovalRow(ctx, res.ApprovalID)
	if !ok || ap.RiskClass != RiskL2 || !strings.Contains(ap.Reason, "未登记") {
		t.Fatalf("审批行应携带未登记理由且为 L2: %+v", ap)
	}

	// 独立 approver 决议后携审批提交 → 入队，RiskClass=L2（fail-closed 不降级）
	if err := f.svc.DecideApproval(ctx, res.ApprovalID, "approver-a", true, "放行"); err != nil {
		t.Fatal(err)
	}
	r2, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 215, Payload: payload, ApprovalID: res.ApprovalID,
	})
	if err != nil {
		t.Fatalf("决议后提交失败: %v", err)
	}
	if r2.State != "queued" {
		t.Fatalf("应入队，实际 %s", r2.State)
	}
	cmd, _, _ := f.db.GetCommand(ctx, r2.CommandID)
	if cmd.RiskClass != RiskL2 {
		t.Fatalf("未登记类型入队级应为 L2，实际 %s", cmd.RiskClass)
	}
}

// TestApprovalGateL2 验证 FR-53 审批门全链：L2 无审批 → 登记待审批不产生指令；
// 自批拒绝；独立 approver 决议后同参提交成功；变参提交被拒；一次性审批消费后失效。
func TestApprovalGateL2(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const payload = `{"type":219,"targetRoom":"待解散群","receivedContent":""}`

	// 1) L2 无审批 → APPROVAL_REQUIRED + 登记 pending（不产生指令）
	res, err := f.svc.Submit(ctx, SubmitRequest{
		Tenant: "tn", Actor: "op-1", Target: testTarget, CommandType: 219, Payload: payload,
	})
	if code := guardCode(t, err); code != ErrCodeApprovalNeeded {
		t.Fatalf("无审批 L2 应返回 APPROVAL_REQUIRED，实际 %s", code)
	}
	if res.ApprovalID == "" || res.State != "awaiting_approval" {
		t.Fatalf("待审批结果异常: %+v", res)
	}
	if cmds, _ := f.db.CommandsByState(ctx, "queued"); len(cmds) != 0 {
		t.Fatalf("待审批不应产生 queued 指令，实际 %d", len(cmds))
	}
	approvalID := res.ApprovalID

	// 2) 同主体自批 → 拒绝（独立 approver）
	if err := f.svc.DecideApproval(ctx, approvalID, "op-1", true, "自批"); err == nil {
		t.Fatal("申请主体自批应被拒绝")
	} else if code := guardCode(t, err); code != ErrCodeApproverConflict {
		t.Fatalf("自批应返回 APPROVER_CONFLICT，实际 %s", code)
	}

	// 3) 独立 approver 决议 → approved
	if err := f.svc.DecideApproval(ctx, approvalID, "approver-a", true, "风险确认放行"); err != nil {
		t.Fatalf("决议失败: %v", err)
	}
	a, ok, _ := f.svc.GetApprovalRow(ctx, approvalID)
	if !ok || a.Status != "approved" || a.Approver != "approver-a" {
		t.Fatalf("审批记录异常: %+v", a)
	}

	// 4) 携审批同参提交 → E01 入队
	ok2, err := f.svc.Submit(ctx, SubmitRequest{
		Tenant: "tn", Actor: "op-1", Target: testTarget, CommandType: 219,
		Payload: payload, ApprovalID: approvalID,
	})
	if err != nil {
		t.Fatalf("审批后提交失败: %v", err)
	}
	if ok2.State != "queued" {
		t.Fatalf("审批后应入队，实际 %s", ok2.State)
	}

	// 5) 一次性审批已消费 → 复用提交被拒
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Tenant: "tn", Actor: "op-1", Target: testTarget, CommandType: 219,
		Payload: payload, ApprovalID: approvalID,
	}); err == nil {
		t.Fatal("一次性审批消费后复用应被拒绝")
	} else if code := guardCode(t, err); code != ErrCodeApprovalMismatch {
		t.Fatalf("消费后复用应返回 APPROVAL_MISMATCH，实际 %s", code)
	}

	// 6) 审批与参数绑定：新审批批准后，变参提交 → APPROVAL_MISMATCH
	res2, _ := f.svc.Submit(ctx, SubmitRequest{
		Tenant: "tn", Actor: "op-1", Target: testTarget, CommandType: 219, Payload: payload,
	})
	if err := f.svc.DecideApproval(ctx, res2.ApprovalID, "approver-a", true, "放行"); err != nil {
		t.Fatalf("决议失败: %v", err)
	}
	tampered := `{"type":219,"targetRoom":"另一个群","receivedContent":""}`
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Tenant: "tn", Actor: "op-1", Target: testTarget, CommandType: 219,
		Payload: tampered, ApprovalID: res2.ApprovalID,
	}); err == nil {
		t.Fatal("审批 A 提交 B 应被拒绝")
	} else if code := guardCode(t, err); code != ErrCodeApprovalMismatch {
		t.Fatalf("变参提交应返回 APPROVAL_MISMATCH，实际 %s", code)
	}

	// 7) 拒绝决议：rejected 后携该审批提交被拒
	res3, _ := f.svc.Submit(ctx, SubmitRequest{
		Tenant: "tn", Actor: "op-1", Target: testTarget, CommandType: 219, Payload: payload,
	})
	if err := f.svc.DecideApproval(ctx, res3.ApprovalID, "approver-b", false, "风险不通过"); err != nil {
		t.Fatalf("决议失败: %v", err)
	}
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Tenant: "tn", Actor: "op-1", Target: testTarget, CommandType: 219,
		Payload: payload, ApprovalID: res3.ApprovalID,
	}); err == nil {
		t.Fatal("rejected 审批提交应被拒绝")
	}
}

// TestApprovalAuditTrail 验证审批全程审计留痕（SR-12）。
func TestApprovalAuditTrail(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 234,
		Payload: `{"type":234,"friend":{"phone":"13800000000","name":"张三"}}`,
	})
	if err == nil {
		t.Fatal("L2 无审批应报错")
	}
	if err := f.svc.DecideApproval(ctx, res.ApprovalID, "approver-a", true, "ok"); err != nil {
		t.Fatalf("决议失败: %v", err)
	}
	rows, err := f.db.QueryDump(ctx, `SELECT kind FROM audit_log WHERE subject=? ORDER BY id`, res.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, r := range rows {
		kinds = append(kinds, r[0])
	}
	want := []string{"approval_requested", "approval_decided"}
	if len(kinds) != len(want) {
		t.Fatalf("审计留痕 %v，期望 %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("审计留痕 %v，期望 %v", kinds, want)
		}
	}
}

// TestL1Whitelist 验证 FR-53 L1 接收人白名单：配置即强制；未配置不拦截；
// WhitelistDefaultDeny 开启时未配置默认拒收。
func TestL1Whitelist(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const group = "白名单群"
	payload := `{"type":229,"targetRoom":"` + group + `","receivedContent":"批量内容"}`

	// 未配置白名单：放行（默认策略）
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 229, Payload: payload,
	}); err != nil {
		t.Fatalf("未配置白名单不应拦截: %v", err)
	}

	// 配置白名单后：接收人在列放行
	if err := f.svc.AddWhitelist(ctx, testTarget, group, "admin-1"); err != nil {
		t.Fatalf("登记白名单失败: %v", err)
	}
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 229, Payload: payload,
	}); err != nil {
		t.Fatalf("白名单内接收人应放行: %v", err)
	}

	// 接收人不在列 → RECIPIENT_NOT_WHITELISTED
	other := `{"type":229,"targetRoom":"未登记群","receivedContent":"x"}`
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 229, Payload: other,
	}); err == nil {
		t.Fatal("白名单外接收人应被拒绝")
	} else if code := guardCode(t, err); code != ErrCodeNotWhitelisted {
		t.Fatalf("期望 RECIPIENT_NOT_WHITELISTED，实际 %s", code)
	}

	// L0 指令不受白名单约束（203 发普通消息）
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 203,
		Payload: `{"type":203,"targetRoom":"未登记群","receivedContent":"普通消息不受白名单约束"}`,
	}); err != nil {
		t.Fatalf("L3 不应受白名单约束: %v", err)
	}

	// WhitelistDefaultDeny=true：另一目标未配置白名单 → 默认拒收
	f.svc.SetGuardConfig(GuardConfig{L1Window: time.Minute, L1MaxPerTx: 100, WhitelistDefaultDeny: true})
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: "device-no-whitelist", CommandType: 229, Payload: payload,
	}); err == nil {
		t.Fatal("WhitelistDefaultDeny 下未配置白名单应拒收")
	} else if code := guardCode(t, err); code != ErrCodeNotWhitelisted {
		t.Fatalf("期望 RECIPIENT_NOT_WHITELISTED，实际 %s", code)
	}
}

// TestL1WhitelistArrayRecipients（CR-t32-T3.2-R5）：批量 L1 指令接收人以数组承载时
// 逐元素判定（任一不在白名单即拒）；形态不可识别默认拒收（fail-closed，不给批量
// 收件人留逃逸通道）；字段缺失仍走审计跳过（既有语义）。
func TestL1WhitelistArrayRecipients(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.svc.AddWhitelist(ctx, testTarget, "群甲", "admin-1"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AddWhitelist(ctx, testTarget, "群乙", "admin-1"); err != nil {
		t.Fatal(err)
	}

	// 数组收件人全部在列 → 放行
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 229,
		Payload: `{"type":229,"targetRoom":["群甲","群乙"],"receivedContent":"批量"}`,
	}); err != nil {
		t.Fatalf("数组收件人全在白名单应放行: %v", err)
	}

	// 数组任一不在列 → 拒（旧实现 json.Unmarshal 报错→""→整条放行）
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 229,
		Payload: `{"type":229,"targetRoom":["群甲","未登记群"],"receivedContent":"批量"}`,
	}); err == nil {
		t.Fatal("数组含白名单外接收人应被拒绝")
	} else if code := guardCode(t, err); code != ErrCodeNotWhitelisted {
		t.Fatalf("期望 RECIPIENT_NOT_WHITELISTED，实际 %s", code)
	}

	// 形态不可识别（对象/嵌套）→ 默认拒收（fail-closed）
	for i, payload := range []string{
		`{"type":229,"targetRoom":{"room":"群甲"},"receivedContent":"x"}`,
		`{"type":229,"targetRoom":[["群甲"]],"receivedContent":"x"}`,
	} {
		if _, err := f.svc.Submit(ctx, SubmitRequest{
			Actor: "op-1", Target: testTarget, CommandType: 229, Payload: payload,
		}); err == nil {
			t.Fatalf("形态不可识别 payload[%d] 应默认拒收", i)
		} else if code := guardCode(t, err); code != ErrCodeNotWhitelisted {
			t.Fatalf("payload[%d] 期望 RECIPIENT_NOT_WHITELISTED，实际 %s", i, code)
		}
	}

	// 字段缺失 → 审计跳过不拦截（既有语义不变）
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 229,
		Payload: `{"type":229,"receivedContent":"无接收人字段"}`,
	}); err != nil {
		t.Fatalf("无接收人字段不应拦截: %v", err)
	}
}

// TestL1RateLimit 验证 FR-53 L1 限速：窗口内超限 → RATE_LIMITED；窗口滑动恢复。
func TestL1RateLimit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	payload := `{"type":229,"targetRoom":"限速群","receivedContent":"x"}`
	submit := func() error {
		_, err := f.svc.Submit(ctx, SubmitRequest{
			Actor: "op-1", Target: testTarget, CommandType: 229, Payload: payload,
		})
		return err
	}
	// 默认 10 条/分钟：前 10 条放行
	for i := 0; i < 10; i++ {
		if err := submit(); err != nil {
			t.Fatalf("第 %d 条不应限速: %v", i+1, err)
		}
	}
	if err := submit(); err == nil {
		t.Fatal("第 11 条应被限速")
	} else if code := guardCode(t, err); code != ErrCodeRateLimited {
		t.Fatalf("期望 RATE_LIMITED，实际 %s", code)
	}
	// 窗口滑动：推进超过窗口后恢复
	f.advance(61 * time.Second)
	if err := submit(); err != nil {
		t.Fatalf("窗口滑动后应恢复: %v", err)
	}
	// L0 指令不受 L1 限速约束
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 203,
		Payload: `{"type":203,"targetRoom":"g","receivedContent":"x"}`,
	}); err != nil {
		t.Fatalf("L0 不应被 L1 限速拦截: %v", err)
	}
}

// TestL1WhitelistGroupMembers W-M3 CR 收口 B1：206/207 的成员名单字段（groupMembers/addMembers）
// 同样受 L1 接收人白名单约束——被拉入者不在白名单即拒；形态不可识别 fail-closed。
func TestL1WhitelistGroupMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.svc.AddWhitelist(ctx, testTarget, "白名单群", "admin-1"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AddWhitelist(ctx, testTarget, "张三", "admin-1"); err != nil {
		t.Fatal(err)
	}

	// 目标群在白名单 + addMembers 全在白名单 → 放行
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 207,
		Payload: `{"type":207,"targetRoom":"白名单群","addMembers":["张三"],"receivedContent":"拉人"}`,
	}); err != nil {
		t.Fatalf("成员全在白名单应放行: %v", err)
	}

	// 目标群在白名单但 addMembers 含白名单外人 → 拒（B1 修复前此载荷可过门）
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 207,
		Payload: `{"type":207,"targetRoom":"白名单群","addMembers":["张三","李四"],"receivedContent":"拉人"}`,
	}); err == nil {
		t.Fatal("addMembers 含白名单外成员应被拒绝（B1）")
	} else if code := guardCode(t, err); code != ErrCodeNotWhitelisted {
		t.Fatalf("期望 RECIPIENT_NOT_WHITELISTED，实际 %s", code)
	}

	// 206 groupMembers 全在白名单 → 放行
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 206,
		Payload: `{"type":206,"targetRoom":"白名单群","groupMembers":["张三"],"receivedContent":"建群"}`,
	}); err != nil {
		t.Fatalf("groupMembers 全在白名单应放行: %v", err)
	}

	// 成员字段形态不可识别 → fail-closed 拒收
	if _, err := f.svc.Submit(ctx, SubmitRequest{
		Actor: "op-1", Target: testTarget, CommandType: 207,
		Payload: `{"type":207,"targetRoom":"白名单群","addMembers":{"name":"张三"},"receivedContent":"x"}`,
	}); err == nil {
		t.Fatal("addMembers 形态不可识别应默认拒收")
	} else if code := guardCode(t, err); code != ErrCodeNotWhitelisted {
		t.Fatalf("期望 RECIPIENT_NOT_WHITELISTED，实际 %s", code)
	}
}
