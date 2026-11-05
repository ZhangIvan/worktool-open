// 回调日志 http_status 透出回归（投递记录 http_status 独立字段车道，2026-11-05）：
// GET /api/v1/admin/callbacks/logs 每行透出 httpStatus——有响应 = 数字，无响应
//（NULL）= JSON null。
package admin

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestCallbackLogsExposeHTTPStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	seed := func(commandID, status string, httpStatus any) {
		t.Helper()
		if _, err := f.db.Exec(ctx, `
INSERT INTO outbox(command_id, kind, payload, attempts, next_attempt, status, last_error, created_at, updated_at, http_status)
VALUES (?,'exec_result','{}',1,0,?,'',?,?,?)`, commandID, status, now.UnixMilli(), now.UnixMilli(), httpStatus); err != nil {
			t.Fatal(err)
		}
	}
	seed("cmd-log-201", "delivered", 201)
	seed("cmd-log-null", "pending", nil)

	st, b := f.call(t, http.MethodGet, "/api/v1/admin/callbacks/logs", "", true)
	if st != http.StatusOK {
		t.Fatalf("callbacks/logs 应 200，got %d %v", st, b)
	}
	logs, _ := b["logs"].([]any)
	if len(logs) != 2 {
		t.Fatalf("应 2 行投递日志: %v", logs)
	}
	byID := map[string]map[string]any{}
	for _, raw := range logs {
		m := raw.(map[string]any)
		byID[m["commandId"].(string)] = m
	}
	if got := byID["cmd-log-201"]["httpStatus"]; got != float64(201) {
		t.Fatalf("delivered 行 httpStatus 应为 201: %v", got)
	}
	if got, ok := byID["cmd-log-null"]["httpStatus"]; !ok || got != nil {
		t.Fatalf("NULL 行 httpStatus 应为 JSON null: %v (present=%v)", got, ok)
	}
}
