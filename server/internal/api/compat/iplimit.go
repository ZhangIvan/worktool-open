// K6 per-IP 限流器（T3.6；docs/15 破坏级 #6）。
//
// 口径（FAQ 20QPS；60QPM 全局口径与 per-robot 限流并存互补——文档两页口径矛盾
// 已在字段级比对报告 §2.3 如实登记，本层取 FAQ 20QPS 滚动窗口）：
//   - 键 = r.RemoteAddr host（net.SplitHostPort；X-Forwarded-For 不信——无反代
//     假设，部署侧显式配置才启用，v1 不做）；
//   - 速率：PerIPRateQPS（默认 20）次 / PerIPWindow（默认 1s）滚动窗口；
//   - 封禁：窗口内**连续**第 PerIPBanThreshold（默认 2）次超限 → 该 IP 进入
//     PerIPBanDuration（默认 60s）冷却，期内直接 429（docs/15 K6「连续超限
//     60s 冷却」）；放行一次即清零连续超限计数；
//   - 内存纪律：对齐 openapiLimiter 的 keys-cap fail-closed（4096 基数上限，
//     防未认证 IP 洪泛撑爆 map——本层在鉴权**之前**计数，cap 为必要纵深）；
//     entry 空窗回收（窗口内零命中即删 entry）。
//
// 顺序纪律（REV-SRV-04 保持）：per-IP（认证前，防刷）→ authenticate →
// per-robot 60QPM（认证后，未认证请求不消耗 robot 配额）。1 个 HTTP 请求计
// 1 次，list 条数不计（对齐上游「批量规避限流」官方定位，api-147612959）。
package compat

import (
	"net"
	"sync"
	"time"
)

// K6 参数（var 供测试注入；默认值 = 上游 FAQ 口径）。
var (
	// PerIPRateQPS 单 IP 滚动窗口内允许的请求数（FAQ：20QPS）。
	PerIPRateQPS = 20
	// PerIPWindow 滚动窗口长度。
	PerIPWindow = time.Second
	// PerIPBanThreshold 窗口内连续超限第 N 次触发封禁（docs/15：连续超限）。
	PerIPBanThreshold = 2
	// PerIPBanDuration 封禁冷却时长（docs/15 K6：60s）。
	PerIPBanDuration = 60 * time.Second
	// PerIPKeysCap entry 基数上限（fail-closed：超限对新 IP 直接拒绝，保旧）。
	PerIPKeysCap = 4096
)

// ipLimiter 为 per-IP 滚动窗口限流器 + 连续超限封禁。
type ipLimiter struct {
	mu      sync.Mutex
	entries map[string]*ipEntry
}

type ipEntry struct {
	hits        []time.Time // 窗口内放行时间戳
	consecOver  int         // 连续超限计数（放行一次即清零）
	bannedUntil time.Time   // 封禁截止（零值 = 未封）
}

func newIPLimiter() *ipLimiter { return &ipLimiter{entries: map[string]*ipEntry{}} }

// ipKey 从 RemoteAddr 提取 host（无端口原样返回；解析失败返回整串——fail-safe
// 归并为单键，不影响放行判定）。
func ipKey(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil && host != "" {
		return host
	}
	return remoteAddr
}

// allow 判定本请求是否放行。返回 (allowed, banned)：
//   - banned=true：封禁期内（冷却未过，直接拒绝，不计数）；
//   - allowed=false 且 banned=false：超限拒绝（计入连续超限计数，达到阈值
//     即启动封禁）。
func (l *ipLimiter) allow(ip string, now time.Time) (allowed, banned bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if ok && now.Before(e.bannedUntil) {
		return false, true // 冷却期内：直接拒绝
	}
	if !ok {
		if len(l.entries) >= PerIPKeysCap {
			return false, false // keys-cap fail-closed（拒新保旧）
		}
		e = &ipEntry{}
		l.entries[ip] = e
	}
	// 封禁已过或首次：清窗口与计数（冷却结束 = 干净重来）
	if !e.bannedUntil.IsZero() {
		e.bannedUntil = time.Time{}
		e.hits = nil
		e.consecOver = 0
	}
	cutoff := now.Add(-PerIPWindow)
	kept := e.hits[:0]
	for _, t := range e.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 && len(e.hits) > 0 {
		delete(l.entries, ip)                     // 空窗回收（防每 IP 一条永不释放的空 entry）
		return l.allowLockedFresh(ip, now), false // cap 满 → fail-closed 拒新
	}
	e.hits = kept
	if len(kept) >= PerIPRateQPS {
		e.consecOver++
		if e.consecOver >= PerIPBanThreshold {
			e.bannedUntil = now.Add(PerIPBanDuration)
			e.consecOver = 0
		}
		return false, false
	}
	e.hits = append(e.hits, now)
	e.consecOver = 0
	return true, false
}

// allowLockedFresh 空窗回收后重新建 entry（等价首请求路径；cap 满时 fail-closed）。
func (l *ipLimiter) allowLockedFresh(ip string, now time.Time) bool {
	if len(l.entries) >= PerIPKeysCap {
		return false // keys-cap fail-closed：拒新保旧
	}
	l.entries[ip] = &ipEntry{hits: []time.Time{now}}
	return true
}
