// H-1（安全审计 TOP5）pair 面防爆破回归：per-IP 限速（5 次/分钟）、连续失败
// 封禁（20 次 → 15 分钟）、成功清零，及 v1 兼容面默认不挂载（H-2）。
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pairBody 构造形状合法的 pair 请求体（码可控：非登记码 → 403 语义失败路径）。
func pairBody(code string) map[string]string {
	return map[string]string{
		"pairingCode":              code,
		"deviceId":                 "device-guard-001",
		"deviceName":               "guard-test",
		"ed25519PublicKey":         strings.Repeat("ab", 32),
		"x25519EphemeralPublicKey": strings.Repeat("cd", 32),
	}
}

// pairPost 向 /api/v2/pair 发一次请求（httptest 回环 → 全部请求同 IP 键，
// 正好复现单 IP 连发形态）。
func (f *openapiFixture) pairPost(t *testing.T, body map[string]string) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(f.srv.URL+"/api/v2/pair", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestPairRateLimitPerIP（H-1）：窗口内 5 次放行、第 6 次 429 RATE_LIMITED；
// 窗口滑过（注入时钟 +61s）恢复放行。
func TestPairRateLimitPerIP(t *testing.T) {
	f := newOpenAPIFixture(t)
	base := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	*f.now = base

	for k := 0; k < PairRatePerMin; k++ {
		st, body := f.pairPost(t, pairBody("00000"+string(rune('0'+k))))
		if st != http.StatusForbidden {
			t.Fatalf("窗口内第 %d 次应 403（限速内放行）: %d %s", k+1, st, body)
		}
	}
	st, body := f.pairPost(t, pairBody("999999"))
	if st != http.StatusTooManyRequests || !strings.Contains(body, "RATE_LIMITED") {
		t.Fatalf("窗口内第 %d 次应 429 RATE_LIMITED: %d %s", PairRatePerMin+1, st, body)
	}
	// 时钟滑过滚动窗口 → 限速恢复（仍 403：码无效是语义失败，非限速）
	*f.now = base.Add(PairRateWindow + time.Second)
	if st, _ := f.pairPost(t, pairBody("999999")); st != http.StatusForbidden {
		t.Fatalf("窗口滑过后应恢复放行（403）: %d", st)
	}
}

// TestPairFailBanAfterConsecutiveFailures（H-1）：连续 20 次失败（时钟逐窗口
// 步进，隔离限速路径）→ 封禁期内即使限速窗口全新也 429；15 分钟冷却后恢复。
func TestPairFailBanAfterConsecutiveFailures(t *testing.T) {
	f := newOpenAPIFixture(t)
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	*f.now = base
	step := PairRateWindow + time.Second

	// 前 PairFailBanThreshold-1 次连续失败 → 均 403（未封禁）
	for k := 0; k < PairFailBanThreshold-1; k++ {
		*f.now = base.Add(time.Duration(k+1) * step)
		if st, _ := f.pairPost(t, pairBody("999999")); st != http.StatusForbidden {
			t.Fatalf("连续失败第 %d 次应 403 未封禁: %d", k+1, st)
		}
	}
	// 第 20 次失败触发封禁（本次响应仍是 403：失败先记账，闸在下一请求生效）
	*f.now = base.Add(time.Duration(PairFailBanThreshold) * step)
	if st, _ := f.pairPost(t, pairBody("999999")); st != http.StatusForbidden {
		t.Fatalf("第 %d 次失败本身应 403: %d", PairFailBanThreshold, st)
	}
	// 封禁期内（14 分钟后）：限速窗口早已全新，429 只能来自失败封禁
	banStart := base.Add(time.Duration(PairFailBanThreshold) * step)
	*f.now = banStart.Add(14 * time.Minute)
	raw, _ := json.Marshal(pairBody("999999"))
	resp, err := http.Post(f.srv.URL+"/api/v2/pair", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(b), "RATE_LIMITED") {
		t.Fatalf("封禁期内应 429 RATE_LIMITED: %d %s", resp.StatusCode, b)
	}
	if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || ra <= 0 {
		t.Fatalf("封禁 429 应携带 Retry-After > 0，实际 %q (%v)", resp.Header.Get("Retry-After"), err)
	}
	// 冷却结束（16 分钟后）恢复放行（403：码无效）
	*f.now = banStart.Add(16 * time.Minute)
	if st, _ := f.pairPost(t, pairBody("999999")); st != http.StatusForbidden {
		t.Fatalf("冷却结束应恢复放行（403）: %d", st)
	}
}

// TestPairFailBanSuccessResetsCounter（H-1）：配对成功（201）清零连续失败计数——
// 19 失败 + 1 成功 + 19 失败不得封禁（若无清零，第 20 次失败即封禁）。
func TestPairFailBanSuccessResetsCounter(t *testing.T) {
	f := newOpenAPIFixture(t)
	base := time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC)
	*f.now = base
	step := PairRateWindow + time.Second

	for k := 0; k < PairFailBanThreshold-1; k++ {
		*f.now = base.Add(time.Duration(k+1) * step)
		if st, _ := f.pairPost(t, pairBody("999999")); st != http.StatusForbidden {
			t.Fatalf("失败第 %d 次应 403: %d", k+1, st)
		}
	}
	code, err := f.api.CreatePairingCode()
	if err != nil {
		t.Fatal(err)
	}
	*f.now = base.Add(time.Duration(PairFailBanThreshold) * step)
	if st, body := f.pairPost(t, pairBody(code)); st != http.StatusCreated {
		t.Fatalf("有效码应 201: %d %s", st, body)
	}
	// 计数已清零：再 19 次失败（时钟步进）→ 不得封禁
	for k := 0; k < PairFailBanThreshold-1; k++ {
		*f.now = base.Add(time.Duration(PairFailBanThreshold+1+k) * step)
		if st, _ := f.pairPost(t, pairBody("999999")); st != http.StatusForbidden {
			t.Fatalf("成功清零后失败第 %d 次应 403 未封禁（429 = 清零失效）: %d", k+1, st)
		}
	}
}

// TestV1CompatRouteNotMountedByDefault（H-2）：默认装配（Options 不带
// EnableV1Compat）下 /webserver/wework/{robotId} 不可达（404）。
func TestV1CompatRouteNotMountedByDefault(t *testing.T) {
	f := newOpenAPIFixture(t)
	resp, err := http.Get(f.srv.URL + "/webserver/wework/robot-x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("v1 兼容面默认不挂载（H-2），应 404: %d", resp.StatusCode)
	}
}
