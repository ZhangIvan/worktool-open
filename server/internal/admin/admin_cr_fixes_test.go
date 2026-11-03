// CR 修复回归（ADM-1/ADM-2/ADM-4）：令牌签发默认租户回落、管理面提交载荷
// type 注入与幂等键重放保护。
package admin

import (
	"context"
	"net/http"
	"testing"
)

// TestAdminTokenIssueDefaultTenant（CR ADM-1）：{} 体 / tenant 空白 → 201 回落
// default（修复前 {} 体被 400 拒绝——UI 留空占位符「default」误导用户）；仅体
// 解析失败才 400。
func TestAdminTokenIssueDefaultTenant(t *testing.T) {
	f := newFixture(t)
	// UI 等价形态①：tenant 留空时 UI 发送 {} 体
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/robots/robot-adm-1/openapi-tokens", `{}`, true)
	if st != http.StatusCreated {
		t.Fatalf("{} 体应 201（tenant 回落 default），got %d %v", st, b)
	}
	if tenant, _ := b["tenant"].(string); tenant != "default" {
		t.Fatalf("tenant 应回落 default，got %q", tenant)
	}
	// UI 等价形态②：tenant 空白串
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/robots/robot-adm-1/openapi-tokens", `{"tenant":"   "}`, true)
	if st != http.StatusCreated || b["tenant"] != "default" {
		t.Fatalf("空白 tenant 应 201/default，got %d %v", st, b)
	}
	// 显式 tenant 仍生效
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/robots/robot-adm-2/openapi-tokens", `{"tenant":"t2"}`, true)
	if st != http.StatusCreated || b["tenant"] != "t2" {
		t.Fatalf("显式 tenant 应生效，got %d %v", st, b)
	}
	// 仅解析失败才 400
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/robots/robot-adm-1/openapi-tokens", `not-json`, true); st != http.StatusBadRequest {
		t.Fatalf("非 JSON 体应 400，got %d", st)
	}
}

// TestAdminSubmitPayloadTypeInjectionAndIdempotency（CR ADM-2 + ADM-4）：
//   - ADM-2 负转正：payload 缺 type（UI 无参数指令等价载荷 {}）→ 服务端注入
//     type 后受理（修复前 E02「payload 缺 type 字段」必败）；
//   - ADM-2 边界：payload 显式 type 不一致仍 400（管线静态校验不放松）；
//   - ADM-4：同 idempotencyKey 同参重放 → replay=true 同 commandId，不重复入队；
//     未携带键时服务端生成并回显。
func TestAdminSubmitPayloadTypeInjectionAndIdempotency(t *testing.T) {
	f := newFixture(t)

	// ADM-2：payload={}（无参数指令 UI 等价载荷）+ dryRun → 注入 type 受理
	st, b := f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-adm-000001","type":503,"payload":{},"dryRun":true,"idempotencyKey":"adm-idem-fix-1"}`, true)
	if st != http.StatusCreated || b["state"] != "succeeded" {
		t.Fatalf("payload 无 type 应注入后受理（201/succeeded），got %d %v", st, b)
	}
	firstID, _ := b["commandId"].(string)
	if firstID == "" {
		t.Fatalf("应携带 commandId：%v", b)
	}
	if key, _ := b["idempotencyKey"].(string); key != "adm-idem-fix-1" {
		t.Fatalf("应回显调用方幂等键，got %q", key)
	}

	// ADM-4：同键同参重放 → 原指令返回，不重复入队
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-adm-000001","type":503,"payload":{},"dryRun":true,"idempotencyKey":"adm-idem-fix-1"}`, true)
	if st != http.StatusCreated || b["replay"] != true || b["commandId"] != firstID {
		t.Fatalf("同键重放应返回原指令（replay=true），got %d %v", st, b)
	}
	rows, err := f.db.QueryDump(context.Background(),
		`SELECT COUNT(*) FROM commands WHERE command_id = ?`, firstID)
	if err != nil || len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("同键重放不得产生第二条指令行: rows=%v err=%v", rows, err)
	}

	// ADM-4：未携带键 → 服务端生成并回显（每请求唯一，仅口径一致）
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-adm-000001","type":503,"params":{},"dryRun":true}`, true)
	if st != http.StatusCreated {
		t.Fatalf("无键提交应 201，got %d %v", st, b)
	}
	if key, _ := b["idempotencyKey"].(string); key == "" {
		t.Fatalf("服务端应生成并回显幂等键：%v", b)
	}

	// ADM-2 边界：payload 显式 type 与 type 不一致 → 仍 400（既有行为不放松）
	if st, _ := f.call(t, http.MethodPost, "/api/v1/admin/commands",
		`{"target":"dev-adm-000001","type":203,"payload":{"type":204,"targetRoom":"g"},"dryRun":true}`, true); st != http.StatusBadRequest {
		t.Fatalf("type 不一致仍应 400，got %d", st)
	}
}
