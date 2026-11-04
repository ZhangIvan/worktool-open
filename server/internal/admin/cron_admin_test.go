// cron 定时任务管理面回归测试（W-ops 审计项①）：CRUD 全链 + 表达式校验 + 预览
// + 启停审计 + L2 审批门 + 空态。装配与 console_test.go 同 fixture（真 SQLite +
// 真管线，按真实落库断言）。
package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCronJobAdminCRUD(t *testing.T) {
	f := newFixture(t)

	// 空态：列表为空数组。
	st, b := f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs", "", true)
	if st != http.StatusOK {
		t.Fatalf("空列表应 200，got %d %v", st, b)
	}
	if n, _ := b["count"].(float64); n != 0 {
		t.Fatalf("初始列表应为空，got %v", b["count"])
	}

	// 创建（L0 类型 503=查我的信息，payload 由 params 合成 {"type":503}）。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/cron-jobs",
		`{"name":"每日巡检","target":"v1:robot-cron","type":503,"params":{},"cronExpr":"30 9 * * *"}`, true)
	if st != http.StatusCreated {
		t.Fatalf("创建应 201，got %d %v", st, b)
	}
	job, _ := b["job"].(map[string]any)
	if job == nil || job["jobId"] == "" {
		t.Fatalf("响应应含 job.jobId：%v", b)
	}
	jobID, _ := job["jobId"].(string)
	// next 触发预览：表达式 30 9 * * *，服务端时钟 2026-11-03 12:00 UTC → 首次为次日 09:30。
	preview, _ := b["previewUtc"].([]any)
	if len(preview) == 0 || !strings.HasPrefix(preview[0].(string), "2026-11-04T09:30") {
		t.Fatalf("next 预览应为 2026-11-04T09:30…，got %v", preview)
	}
	if ms, _ := b["nextFireMs"].(float64); ms <= 0 {
		t.Fatalf("nextFireMs 应 >0，got %v", b["nextFireMs"])
	}
	if en, _ := job["enabled"].(bool); !en {
		t.Fatalf("新任务应默认 enabled，got %v", job["enabled"])
	}

	// 列表投影：enabled/next_fire/last_result。
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs", "", true)
	if st != http.StatusOK {
		t.Fatalf("列表应 200，got %d", st)
	}
	jobs, _ := b["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("列表应 1 条，got %v", b["count"])
	}
	row, _ := jobs[0].(map[string]any)
	if row["lastResult"] != "" || row["lastFireMs"].(float64) != 0 {
		t.Fatalf("未触发任务 lastResult/lastFireMs 应为空/0：%v", row)
	}

	// 停用 → enabled=false；再启用 → true（审计由内核侧落 cron_job_disabled/enabled）。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/cron-jobs/"+jobID+"/enabled", `{"enabled":false}`, true)
	if st != http.StatusOK || b["enabled"] != false {
		t.Fatalf("停用应 200 enabled=false，got %d %v", st, b)
	}
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs", "", true)
	jobs, _ = b["jobs"].([]any)
	if en, _ := jobs[0].(map[string]any)["enabled"].(bool); en {
		t.Fatalf("停用后列表 enabled 应 false")
	}
	st, _ = f.call(t, http.MethodPost, "/api/v1/admin/cron-jobs/"+jobID+"/enabled", `{"enabled":true}`, true)
	if st != http.StatusOK {
		t.Fatalf("启用应 200，got %d", st)
	}
	// 审计留痕核验（SR-12）。
	rows, _ := f.db.QueryDump(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE kind IN ('cron_job_disabled','cron_job_enabled')`)
	if len(rows) != 1 || rows[0][0] != "2" {
		t.Fatalf("启停应各留审计 1 条（共 2），got %v", rows)
	}

	// 非法表达式 → 400；未知字段（riskClass）→ 400（防降级绕审批纪律）。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/cron-jobs",
		`{"name":"x","target":"v1:r","type":503,"params":{},"cronExpr":"99 * * * *"}`, true)
	if st != http.StatusBadRequest {
		t.Fatalf("非法表达式应 400，got %d %v", st, b)
	}
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/cron-jobs",
		`{"name":"x","target":"v1:r","type":503,"params":{},"cronExpr":"* * * * *","riskClass":"L0"}`, true)
	if st != http.StatusBadRequest {
		t.Fatalf("riskClass 应被拒（未定义字段），got %d %v", st, b)
	}

	// L2 类型（219 解散群）不绑常设审批 → 创建被拒（403，非中继态）。
	st, b = f.call(t, http.MethodPost, "/api/v1/admin/cron-jobs",
		`{"name":"高危","target":"v1:r","type":219,"params":{},"cronExpr":"0 12 * * *"}`, true)
	if st != http.StatusForbidden {
		t.Fatalf("L2 无审批创建应 403，got %d %v", st, b)
	}

	// 删除 → 列表空（空态回归）；重复删除 → 404。
	st, b = f.call(t, http.MethodDelete, "/api/v1/admin/cron-jobs/"+jobID, "", true)
	if st != http.StatusOK || b["deleted"] != true {
		t.Fatalf("删除应 200，got %d %v", st, b)
	}
	st, _ = f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs", "", true)
	// （下方再次 call 校验 count；两次请求分开断言避免 body 复用歧义。）
	if st != http.StatusOK {
		t.Fatalf("删除后列表应 200，got %d", st)
	}
	st, b = f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs", "", true)
	if n, _ := b["count"].(float64); n != 0 {
		t.Fatalf("删除后列表应为空，got %v", b)
	}
	st, b = f.call(t, http.MethodDelete, "/api/v1/admin/cron-jobs/"+jobID, "", true)
	if st != http.StatusNotFound {
		t.Fatalf("重复删除应 404，got %d %v", st, b)
	}

	// 无鉴权反例（与全管理面同纪律）。
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs", "", false); st != http.StatusUnauthorized {
		t.Fatalf("无鉴权应 401，got %d", st)
	}
}

func TestCronJobPreviewEndpoint(t *testing.T) {
	f := newFixture(t)
	// 预览端点（表单先验，不落库）。
	st, b := f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs/preview?expr=*/15+*+*+*+*&limit=3", "", true)
	if st != http.StatusOK {
		t.Fatalf("预览应 200，got %d %v", st, b)
	}
	preview, _ := b["previewUtc"].([]any)
	if len(preview) != 3 {
		t.Fatalf("limit=3 应返回 3 个时刻，got %v", preview)
	}
	// 相邻间隔 15 分钟（*/15）。
	for i := 1; i < len(preview); i++ {
		prev := mustRFC3339(t, preview[i-1].(string))
		cur := mustRFC3339(t, preview[i].(string))
		if cur.Sub(prev) != 15*time.Minute {
			t.Fatalf("*/15 相邻触发应差 15 分钟，got %v → %v", prev, cur)
		}
	}
	// 非法表达式 → 400。
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs/preview?expr=not-a-cron", "", true); st != http.StatusBadRequest {
		t.Fatalf("非法表达式应 400")
	}
	// 缺 expr → 400。
	if st, _ := f.call(t, http.MethodGet, "/api/v1/admin/cron-jobs/preview", "", true); st != http.StatusBadRequest {
		t.Fatalf("缺 expr 应 400")
	}
}

func mustRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("时刻 %q 非 RFC3339: %v", s, err)
	}
	return v
}
