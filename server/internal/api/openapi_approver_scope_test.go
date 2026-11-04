// P0-3 审批身份车道回归（api 兼容面侧）：保留名 approver 与 admin 同构硬拒——
// 签发侧拒 robot_id=approver，鉴权侧拒 robotId=approver 寻址（防决议令牌经
// v1:approver 倒灌业务管线，scope 隔离）。
package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
)

// TestApproverScopeRejectedOnOpenAPIFace（CR ADM-1 的 approver 镜像）。
func TestApproverScopeRejectedOnOpenAPIFace(t *testing.T) {
	f := newOpenAPIFixture(t)

	// 签发侧硬拒：不得为保留名 approver 签发兼容面令牌（否则同一枚令牌既走
	// 业务管线又过 admin 面决议端点 requireApprover，scope 隔离被击穿）。
	if _, err := f.api.IssueOpenAPIToken(context.Background(), sqlite.ReservedApproverScopeRobotID, "tenant-a"); err == nil {
		t.Fatal("为保留名 approver 签发兼容面令牌必须失败")
	}

	// 鉴权侧硬拒：直插一枚 approver scope 令牌（现实来源 = -issue-approver-token），
	// robotId=approver 寻址恰好命中 t.RobotID == 路径 → 403。
	raw := "e2e-approver-scope-token-raw-value"
	if err := f.db.InsertAPIToken(context.Background(), sha256Hex(raw), sqlite.ReservedApproverScopeRobotID, "default", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	body := `{"type":203,"targetRoom":"g","receivedContent":"x"}`
	req, _ := http.NewRequest(http.MethodPost,
		f.srv.URL+"/openapi/v1/robots/"+sqlite.ReservedApproverScopeRobotID+"/commands",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Idempotency-Key", "approver-face-neg")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("approver 令牌打兼容面应 403（保留名硬拒），got %d", resp.StatusCode)
	}

	// 寻址他 robot：approver 令牌归属不匹配 → 403 ROBOT_MISMATCH（不得借道业务面）。
	req2, _ := http.NewRequest(http.MethodPost,
		f.srv.URL+"/openapi/v1/robots/"+f.robot+"/commands", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer "+raw)
	req2.Header.Set("Idempotency-Key", "approver-face-mismatch")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("approver 令牌寻址他 robot 应 403，got %d", resp2.StatusCode)
	}
}
