// P0-3 审批身份车道回归（approver 独立 scope 令牌）：
//   - 决议只认 approver 令牌：admin 令牌决议=403 APPROVER_SCOPE_REQUIRED（结构性
//     消灭自批）、无令牌/坏令牌=401、approver 令牌决议=200（留痕身份=令牌派生）；
//   - scope 隔离另一向：approver 令牌提交指令=403 ADMIN_SCOPE_REQUIRED；
//   - 保留名 reservation：兼容面签发/吊销/寻址三侧硬拒 robot_id=approver
//     （与 CR ADM-1 的 admin 同构）。
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/api"
	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
)

// newApprovalDirect 经管线直登一条 pending 审批（绕开提交端点，聚焦决议面）。
func newApprovalDirect(t *testing.T, f *fixture, actor, target string) sqlite.Approval {
	t.Helper()
	ap, err := f.s.pipeline.RequestApproval(context.Background(), scheduler.ApprovalRequestInput{
		Tenant: "default", Actor: actor, Target: target,
		CommandType: 219, Payload: `{"type":219,"targetRoom":"G1"}`, // 219 解散群 = L2（FR-53）
		Reason: "P0-3 回归：L2 待审批",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

// TestScopedIdentityFormatting 身份串单元回归：前缀 + hash 前 16 hex；自由文本
// 仅作展示后缀；畸形短 hash 不 panic（取全长）。
func TestScopedIdentityFormatting(t *testing.T) {
	long := strings.Repeat("ab", 32) // 64 hex（sha256Hex 口径）
	if got := scopedIdentity("approver-token:", long, "member-b"); got != "approver-token:abababababababab (member-b)" {
		t.Fatalf("带后缀身份串不符，got %q", got)
	}
	if got := scopedIdentity("admin-token:", long, ""); got != "admin-token:abababababababab" {
		t.Fatalf("无后缀身份串不符，got %q", got)
	}
	if got := scopedIdentity("approver-token:", "0123456789abcdef", ""); got != "approver-token:0123456789abcdef" {
		t.Fatalf("短 hash 应取全长，got %q", got)
	}
}

// TestP03DecisionScopeDoD 任务卡 DoD 四态：admin token 决议→403；无 approver
// token→401；approver token 决议→200；决议留痕身份 = "approver-token:" + hash
// 前 16 hex（自由文本仅展示后缀，可留空）。
func TestP03DecisionScopeDoD(t *testing.T) {
	f := newFixture(t)
	ap := newApprovalDirect(t, f, "member-a", "device-p03-000001")
	path := "/api/v1/admin/approvals/" + ap.ApprovalID + "/decision"

	// ① admin token 决议 → 403 APPROVER_SCOPE_REQUIRED（自批结构性消灭）。
	if st, b := f.call(t, http.MethodPost, path, `{"approver":"member-a","decision":"approve"}`, true); st != http.StatusForbidden || b["error"] != "APPROVER_SCOPE_REQUIRED" {
		t.Fatalf("admin 令牌决议应 403 APPROVER_SCOPE_REQUIRED，got %d %v", st, b)
	}
	// 审批行不得被 admin 令牌决议改动（仍 pending）。
	if a, ok, _ := f.db.GetApproval(context.Background(), ap.ApprovalID); !ok || a.Status != "pending" {
		t.Fatalf("admin 令牌决议后审批须仍 pending，got %v", a.Status)
	}

	// ② 无 approver token → 401 INVALID_TOKEN。
	if st, b := f.callTok(t, http.MethodPost, path, `{"decision":"approve"}`, ""); st != http.StatusUnauthorized || b["error"] != "INVALID_TOKEN" {
		t.Fatalf("无令牌决议应 401 INVALID_TOKEN，got %d %v", st, b)
	}
	// 坏令牌同样 401。
	if st, _ := f.callTok(t, http.MethodPost, path, `{"decision":"approve"}`, "not-a-token"); st != http.StatusUnauthorized {
		t.Fatalf("坏令牌决议应 401，got %d", st)
	}

	// ③ approver token 决议 → 200；留痕身份 = approver-token:<hash16> + 展示后缀。
	st, b := f.callTok(t, http.MethodPost, path, `{"approver":"member-b","decision":"approve","reason":"复核通过"}`, f.approverToken)
	if st != http.StatusOK || b["status"] != "approved" {
		t.Fatalf("approver 令牌决议应 200 approved，got %d %v", st, b)
	}
	wantID := "approver-token:" + sha256Hex(f.approverToken)[:16]
	if got, _ := b["approver"].(string); got != wantID+" (member-b)" {
		t.Fatalf("决议留痕身份须为 %q，got %q", wantID+" (member-b)", b["approver"])
	}
	a, ok, err := f.db.GetApproval(context.Background(), ap.ApprovalID)
	if !ok || err != nil {
		t.Fatalf("审批行读取失败：ok=%v err=%v", ok, err)
	}
	if a.Approver != wantID+" (member-b)" {
		t.Fatalf("落库 approver 须为令牌派生身份，got %q", a.Approver)
	}
	// approver ≠ actor（FR-53 文本比较在令牌身份下天然成立）。
	if a.Approver == a.Actor {
		t.Fatal("决议身份不得与申请人相同（FR-53 独立 approver）")
	}

	// ④ 自由文本留空 = 仅以令牌身份留痕（身份来自令牌，非请求体声明字段）。
	ap2 := newApprovalDirect(t, f, "member-a", "device-p03-000002")
	path2 := "/api/v1/admin/approvals/" + ap2.ApprovalID + "/decision"
	if st, b := f.callTok(t, http.MethodPost, path2, `{"decision":"reject"}`, f.approverToken); st != http.StatusOK || b["status"] != "rejected" {
		t.Fatalf("无自由文本决议应 200 rejected，got %d %v", st, b)
	}
	if a, ok, _ := f.db.GetApproval(context.Background(), ap2.ApprovalID); !ok || a.Approver != wantID {
		t.Fatalf("留空自由文本时留痕须恰为 %q，got %q", wantID, a.Approver)
	}
}

// TestP03ApproverTokenCannotSubmitCommands scope 隔离另一向：approver 令牌打
// 提交端点 → 403 ADMIN_SCOPE_REQUIRED（不落指令行）；业务 robot 令牌维持 401
// 旧口径；admin 令牌不受影响。
func TestP03ApproverTokenCannotSubmitCommands(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	body := `{"target":"dev-p03-000003","type":503,"params":{},"dryRun":true}`

	if st, b := f.callTok(t, http.MethodPost, "/api/v1/admin/commands", body, f.approverToken); st != http.StatusForbidden || b["error"] != "ADMIN_SCOPE_REQUIRED" {
		t.Fatalf("approver 令牌提交应 403 ADMIN_SCOPE_REQUIRED，got %d %v", st, b)
	}
	rows, err := f.db.QueryDump(ctx, `SELECT COUNT(*) FROM commands`)
	if err != nil || rows[0][0] != "0" {
		t.Fatalf("approver 令牌提交不得落指令行：rows=%v err=%v", rows, err)
	}

	// 业务 robot 令牌 → 401（CR ADM-1 防探测口径不变，仅保留 scope 互相 403）。
	if err := f.db.InsertAPIToken(ctx, sha256Hex("robot-p03-token"), "robot-p03", "default", f.now.UTC()); err != nil {
		t.Fatal(err)
	}
	if st, b := f.callTok(t, http.MethodPost, "/api/v1/admin/commands", body, "robot-p03-token"); st != http.StatusUnauthorized || b["error"] != "INVALID_TOKEN" {
		t.Fatalf("robot 令牌提交应 401 INVALID_TOKEN（旧口径），got %d %v", st, b)
	}

	// 对照：admin 令牌提交同体 → 201（提交面完好）。
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/commands", body, true); st != http.StatusCreated {
		t.Fatalf("admin 令牌提交应 201，got %d", st)
	}
}

// TestP03ApproverReservation 保留名 approver 与 admin 同构硬拒：管理面签发/吊销
// 兼容面令牌拒 approver；兼容面签发（api.IssueOpenAPIToken）与寻址
// （robotId=approver）同样硬拒——同一枚令牌不得双面行走（scope 隔离）。
func TestP03ApproverReservation(t *testing.T) {
	f := newFixture(t)
	// 管理面签发侧：robots/approver/openapi-tokens → 403 ROBOT_FORBIDDEN。
	if st, b := f.call(t, http.MethodPost, "/api/v1/admin/robots/approver/openapi-tokens", `{}`, true); st != http.StatusForbidden || b["error"] != "ROBOT_FORBIDDEN" {
		t.Fatalf("保留名 approver 签发应 403 ROBOT_FORBIDDEN，got %d %v", st, b)
	}
	// 管理面吊销侧同样 403。
	if st, _ := f.call(t, http.MethodDelete, "/api/v1/admin/robots/approver/openapi-tokens", "", true); st != http.StatusForbidden {
		t.Fatalf("保留名 approver 吊销应 403，got %d", st)
	}

	// 兼容面签发侧（api 包）：IssueOpenAPIToken("approver") 必须失败。
	machine := statemachine.Default()
	pipeline := scheduler.New(f.db, machine)
	pipeline.Now = func() time.Time { return *f.now }
	hub := gateway.New(f.db, pipeline, machine, testLogger{t})
	apiSrv := api.NewServer(f.db, hub, pipeline, machine, testLogger{t})
	apiSrv.Now = func() time.Time { return *f.now }
	mux := api.NewServeMux(api.Options{Server: apiSrv})
	openapiSrv := httptest.NewServer(mux)
	t.Cleanup(openapiSrv.Close)

	if _, err := apiSrv.IssueOpenAPIToken(context.Background(), "approver", "default"); err == nil {
		t.Fatal("为保留名 approver 签发兼容面令牌必须失败")
	}

	// 兼容面寻址侧：approver 令牌 + robotId=approver 寻址 → 403（不硬拒即可经
	// resolveTarget 倒灌业务管线——与 admin 的 CR ADM-1 向量同构）。
	body := `{"type":203,"targetRoom":"g","receivedContent":"x"}`
	req, _ := http.NewRequest(http.MethodPost, openapiSrv.URL+"/openapi/v1/robots/approver/commands", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.approverToken)
	req.Header.Set("Idempotency-Key", "p03-reserve")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("approver 令牌打兼容面寻址应 403（保留名硬拒），got %d", resp.StatusCode)
	}

	// 即便寻址普通 robot：approver 令牌归属不匹配 → 403（不得借道业务面）。
	req2, _ := http.NewRequest(http.MethodPost, openapiSrv.URL+"/openapi/v1/robots/robot-p03/commands", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer "+f.approverToken)
	req2.Header.Set("Idempotency-Key", "p03-reserve-2")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("approver 令牌寻址他 robot 应 403 ROBOT_MISMATCH，got %d", resp2.StatusCode)
	}
}

// TestP03ApproverTokenLifecycle 决议令牌生命周期：签发即可决议；吊销后决议
// 401；管理面令牌列表以 approver（决议面）标注 scope。
func TestP03ApproverTokenLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// 另签一枚（与 fixture 预置分持），用于旋转语义验证。
	fresh, err := f.s.IssueApproverToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ap := newApprovalDirect(t, f, "member-a", "device-p03-000004")
	path := "/api/v1/admin/approvals/" + ap.ApprovalID + "/decision"
	if st, _ := f.callTok(t, http.MethodPost, path, `{"decision":"approve"}`, fresh); st != http.StatusOK {
		t.Fatalf("新签决议令牌应可决议 200，got %d", st)
	}

	// 吊销 fresh（SQL 旋转路径，scope 令牌不走兼容面吊销端点）。
	if _, err := f.db.Exec(ctx, `UPDATE api_tokens SET status='revoked' WHERE token_hash=?`, sha256Hex(fresh)); err != nil {
		t.Fatal(err)
	}
	ap2 := newApprovalDirect(t, f, "member-a", "device-p03-000005")
	if st, _ := f.callTok(t, http.MethodPost, "/api/v1/admin/approvals/"+ap2.ApprovalID+"/decision", `{"decision":"approve"}`, fresh); st != http.StatusUnauthorized {
		t.Fatalf("吊销后决议令牌应 401，got %d", st)
	}

	// 令牌列表（管理面只读投影）：approver scope 显式标注。
	st, b := f.call(t, http.MethodGet, "/api/v1/admin/tokens", "", true)
	if st != http.StatusOK {
		t.Fatalf("令牌列表应 200，got %d", st)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "approver（决议面，P0-3）") {
		t.Fatalf("令牌列表须标注 approver scope，got %s", raw)
	}
}
