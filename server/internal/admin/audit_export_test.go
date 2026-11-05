// 审计导出端点回归（审计导出车道，2026-11-05）：GET /api/v1/admin/audit/export
// ?format=jsonl|csv&limit=N——admin scope 鉴权、分页流式（keyset 翻页逐页
// flush）、Content-Disposition 附件头、无审计行空流、limit 上限 10000。
package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// clearAuditRows 清空 fixture 签发令牌落下的审计行（导出计数确定性）。
func clearAuditRows(t *testing.T, f *fixture) {
	t.Helper()
	if _, err := f.db.Exec(context.Background(), `DELETE FROM audit_log`); err != nil {
		t.Fatal(err)
	}
}

func seedAudit(t *testing.T, f *fixture, kind, subject, detail string) {
	t.Helper()
	if err := f.db.InsertAudit(context.Background(), nil, kind, subject, detail, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func exportReq(t *testing.T, f *fixture, query string, auth bool) (int, http.Header, string) {
	t.Helper()
	url := f.srv.URL + "/api/v1/admin/audit/export"
	if query != "" {
		url += "?" + query
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if auth {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

// TestAuditExportEmptyStream：无审计行 → 200 空流（jsonl/csv 均空 body），
// Content-Disposition 附件头在位。
func TestAuditExportEmptyStream(t *testing.T) {
	f := newFixture(t)
	clearAuditRows(t, f)
	for _, q := range []string{"format=jsonl", "format=csv"} {
		st, h, body := exportReq(t, f, q, true)
		if st != http.StatusOK {
			t.Fatalf("%s 应 200，got %d %s", q, st, body)
		}
		if body != "" {
			t.Fatalf("%s 无审计行应空流，got %q", q, body)
		}
		cd := h.Get("Content-Disposition")
		if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "audit-export-") {
			t.Fatalf("%s Content-Disposition 应为 audit-export 附件: %q", q, cd)
		}
		wantExt := "jsonl"
		if q == "format=csv" {
			wantExt = "csv"
		}
		if !strings.Contains(cd, "."+wantExt) {
			t.Fatalf("%s 文件名应带 .%s 后缀: %q", q, wantExt, cd)
		}
	}
}

// TestAuditExportJSONL：jsonl 逐行对象（id/kind/subject/detail/atMs），按 id 升序。
func TestAuditExportJSONL(t *testing.T) {
	f := newFixture(t)
	clearAuditRows(t, f)
	seedAudit(t, f, "admin_pairing_code_issued", "admin", "甲签发配对码")
	seedAudit(t, f, "outbox_dead_purged", "", "count=2（dead 行保留期）")

	st, h, body := exportReq(t, f, "format=jsonl", true)
	if st != http.StatusOK {
		t.Fatalf("应 200，got %d", st)
	}
	if ct := h.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("jsonl Content-Type 应为 application/x-ndjson: %q", ct)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("应 2 行 jsonl: %q", body)
	}
	prevID := float64(0)
	for _, ln := range lines {
		var e map[string]any
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("行应为合法 JSON: %q err=%v", ln, err)
		}
		id := e["id"].(float64)
		if id <= prevID {
			t.Fatalf("应按 id 升序: %v", e)
		}
		prevID = id
		if _, ok := e["atMs"].(float64); !ok {
			t.Fatalf("应含 atMs: %v", e)
		}
		if e["kind"] == "" {
			t.Fatalf("应含 kind: %v", e)
		}
	}
	if !strings.Contains(body, "甲签发配对码") {
		t.Fatalf("detail 应原文导出: %q", body)
	}
}

// TestAuditExportCSV：csv 表头 + 逐行转义（含逗号/引号/换行的 detail 正确引用）。
func TestAuditExportCSV(t *testing.T) {
	f := newFixture(t)
	clearAuditRows(t, f)
	seedAudit(t, f, "kind-a", "subj,1", `detail "quoted", with,commas
and newline`)

	st, h, body := exportReq(t, f, "format=csv", true)
	if st != http.StatusOK {
		t.Fatalf("应 200，got %d", st)
	}
	if ct := h.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("csv Content-Type 应为 text/csv: %q", ct)
	}
	rd := csv.NewReader(strings.NewReader(body))
	records, err := rd.ReadAll()
	if err != nil {
		t.Fatalf("csv 解析失败: %v body=%q", err, body)
	}
	if len(records) != 2 {
		t.Fatalf("应 1 表头 + 1 行: %v", records)
	}
	if strings.Join(records[0], ",") != "id,kind,subject,detail,atMs" {
		t.Fatalf("表头不符: %v", records[0])
	}
	if records[1][1] != "kind-a" || records[1][2] != "subj,1" || !strings.Contains(records[1][3], "with,commas") {
		t.Fatalf("转义错误: %v", records[1])
	}
}

// TestAuditExportLimitAndPaging：limit 截断 + >单页 500 行分页流式全量导出。
func TestAuditExportLimitAndPaging(t *testing.T) {
	f := newFixture(t)
	clearAuditRows(t, f)
	for i := 0; i < 3; i++ {
		seedAudit(t, f, fmt.Sprintf("kind-%d", i), "s", "d")
	}
	// limit 截断。
	st, _, body := exportReq(t, f, "format=jsonl&limit=2", true)
	if st != http.StatusOK || strings.Count(body, "\n") != 2 {
		t.Fatalf("limit=2 应导出 2 行: st=%d body=%q", st, body)
	}
	// 超上限拒绝。
	st, _, _ = exportReq(t, f, "format=jsonl&limit=10001", true)
	if st != http.StatusBadRequest {
		t.Fatalf("limit=10001 应 400，got %d", st)
	}
	st, _, _ = exportReq(t, f, "format=jsonl&limit=0", true)
	if st != http.StatusBadRequest {
		t.Fatalf("limit=0 应 400，got %d", st)
	}
	st, _, _ = exportReq(t, f, "format=xml", true)
	if st != http.StatusBadRequest {
		t.Fatalf("format=xml 应 400，got %d", st)
	}

	// 分页流式：501 行（> 单页 500）→ 全量 501 行导出（keyset 翻页无漏）。
	clearAuditRows(t, f)
	for i := 0; i < 501; i++ {
		seedAudit(t, f, "bulk", "s", fmt.Sprintf("d-%d", i))
	}
	st, _, body = exportReq(t, f, "format=jsonl", true)
	if st != http.StatusOK {
		t.Fatalf("应 200，got %d", st)
	}
	if got := strings.Count(body, "\n"); got != 501 {
		t.Fatalf("501 行应全量导出（分页无漏），got %d", got)
	}
	if !strings.Contains(body, "d-500") {
		t.Fatal("末页行（d-500）应在导出中")
	}
}

// TestAuditExportAuthAndFilename：未授权 401；文件名带 UTC 时间戳形态。
func TestAuditExportAuthAndFilename(t *testing.T) {
	f := newFixture(t)
	st, _, _ := exportReq(t, f, "format=jsonl", false)
	if st != http.StatusUnauthorized {
		t.Fatalf("未授权应 401，got %d", st)
	}
	st, h, _ := exportReq(t, f, "", true)
	if st != http.StatusOK {
		t.Fatalf("缺 format 应回落 jsonl 放行，got %d", st)
	}
	wantRe := regexp.MustCompile(`attachment; filename="audit-export-\d{8}T\d{6}Z\.jsonl"`)
	if !wantRe.MatchString(h.Get("Content-Disposition")) {
		t.Fatalf("文件名应带 UTC 时间戳: %q", h.Get("Content-Disposition"))
	}
}
