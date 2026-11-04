// /readyz 增强回归测试（W-ops 审计项②）：liveness/readiness 区分 + 三个后台
// 循环（派发泵/回调 worker/cron 循环）活性探针——未启动 not_started → 503；
// 启动后 alive → 200；停摆超 3×周期 stalled → 503。纯函数与集成各一。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// loopProbe 纯函数表测：三态判定与 3×周期阈值。
func TestLoopProbeStates(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).UnixMilli()
	cases := []struct {
		name       string
		lastTickMs int64
		periodMs   int64
		wantState  string
		wantOK     bool
	}{
		{"循环未启动", 0, 0, "not_started", false},
		{"已配置未唤醒", 0, 250, "not_started", false},
		{"窗口内 alive", now - 1000, 1000, "alive", true},
		{"恰在 3×周期边界 alive", now - 3000, 1000, "alive", true},
		{"超 3×周期 stalled", now - 3001, 1000, "stalled", false},
	}
	for _, c := range cases {
		got := loopProbe(now, c.lastTickMs, c.periodMs)
		if got["state"] != c.wantState || got["ok"] != c.wantOK {
			t.Fatalf("%s: loopProbe(%d,%d,%d) = %v，want state=%s ok=%v", c.name, now, c.lastTickMs, c.periodMs, got, c.wantState, c.wantOK)
		}
	}
}

// getJSON 发起一次 GET 并解 JSON（返回状态码 + 解码体）。
func (f *openapiFixture) getJSON(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// waitReadyz 轮询 /readyz 直到命中期望状态码或超时（循环唤醒为异步事件）。
func waitReadyz(t *testing.T, f *openapiFixture, wantCode int) (int, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, body := f.getJSON(t, "/readyz")
		if st == wantCode {
			return st, body
		}
		if time.Now().After(deadline) {
			return st, body
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestReadyzLoopLiveness 集成：三循环未启动 → 503/dead；启动（20ms 小周期）→
// 200/alive；停摆超 3×周期 → 503/stalled。探针缺位（Outbox 未装配）不出现字段。
func TestReadyzLoopLiveness(t *testing.T) {
	f := newOpenAPIFixture(t)

	// ① 循环未启动：dispatch_pump / cron_loop = not_started（Hub/Pipeline 已装配）
	// → liveness=dead → 503；callback_worker 不出现（Outbox 未装配，不误判）。
	st, body := f.getJSON(t, "/readyz")
	if st != http.StatusServiceUnavailable {
		t.Fatalf("循环未启动应 503，got %d %v", st, body)
	}
	if body["liveness"] != "dead" || body["readiness"] != "not_ready" || body["status"] != "not_ready" {
		t.Fatalf("状态应 dead/not_ready：%v", body)
	}
	checks, _ := body["checks"].(map[string]any)
	if checks == nil {
		t.Fatalf("应含 checks：%v", body)
	}
	if c, ok := checks["callback_worker"]; ok {
		t.Fatalf("Outbox 未装配不应出现 callback_worker 探针：%v", c)
	}
	for _, key := range []string{"dispatch_pump", "cron_loop"} {
		c, _ := checks[key].(map[string]any)
		if c == nil || c["state"] != "not_started" {
			t.Fatalf("%s 应为 not_started：%v", key, c)
		}
	}
	if c, _ := checks["db"].(map[string]any); c == nil || c["ok"] != true {
		t.Fatalf("db 探针应 ok：%v", checks["db"])
	}

	// ② 装配 worker 并启动三个循环（20ms 小周期，加快唤醒观测）。
	worker, err := NewOutboxWorker(f.db, OutboxConfig{PollInterval: 20 * time.Millisecond, CallbackSecret: "readyz-test-secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.api.Outbox = worker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.api.Hub.RunDispatchPump(ctx, 20*time.Millisecond) }()
	go func() { _ = worker.Run(ctx) }()
	go func() { _ = f.api.Pipeline.RunCronScheduler(ctx, 20*time.Millisecond) }()

	st, body = waitReadyz(t, f, http.StatusOK)
	if st != http.StatusOK {
		t.Fatalf("循环启动后应 200，got %d %v", st, body)
	}
	if body["liveness"] != "alive" || body["readiness"] != "ready" {
		t.Fatalf("状态应 alive/ready：%v", body)
	}
	checks, _ = body["checks"].(map[string]any)
	for _, key := range []string{"dispatch_pump", "callback_worker", "cron_loop"} {
		c, _ := checks[key].(map[string]any)
		if c == nil || c["state"] != "alive" {
			t.Fatalf("%s 应为 alive：%v", key, c)
		}
	}

	// ③ 停摆：取消循环，超 3×周期（60ms）后探针 stalled → 503/dead。
	cancel()
	time.Sleep(300 * time.Millisecond)
	st, body = waitReadyz(t, f, http.StatusServiceUnavailable)
	if st != http.StatusServiceUnavailable {
		t.Fatalf("停摆后应 503，got %d %v", st, body)
	}
	if body["liveness"] != "dead" {
		t.Fatalf("停摆后 liveness 应 dead：%v", body)
	}
	checks, _ = body["checks"].(map[string]any)
	stalled := 0
	for _, key := range []string{"dispatch_pump", "callback_worker", "cron_loop"} {
		c, _ := checks[key].(map[string]any)
		if c != nil && c["state"] == "stalled" {
			stalled++
		}
	}
	if stalled != 3 {
		t.Fatalf("三个循环探针均应 stalled，实际 %d：%v", stalled, checks)
	}
}
