// 全局熔断器（FR-53：错误率阈值触发自动暂停 + 审计事件；任务卡 T3.2）。
//
// 语义：
//   - closed（闭合/正常）：窗口内真实执行（非干跑）终态样本 ≥ MinSamples 且
//     失败率 ≥ Threshold → 翻开（open），审计 circuit_breaker_open（SR-12）；
//   - open（熔断/自动暂停）：Allow() 拒绝全部新提交（E02 仅审计），持续 Cooldown；
//   - half-open（试探）：Cooldown 到期后放行试探提交；CloseSamples 条连续成功 →
//     闭合（审计 circuit_breaker_closed）；任一失败 → 重新翻开（审计 reopen）。
//
// 样本仅计真实执行结果（DryRun 指令不计入，FR-54 干跑不污染风控统计）。
// 进程内状态（重启回 closed，审计留痕持久）；结果样本本身持久于 commands 表。
package scheduler

import (
	"sync"
	"time"
)

// BreakerConfig 为熔断参数（默认见 DefaultBreakerConfig；FR-53"阈值触发"可配）。
type BreakerConfig struct {
	Window       time.Duration // 滚动样本窗口
	MinSamples   int           // 判定最少样本数（不足不翻开）
	Threshold    float64       // 失败率阈值 [0,1]
	Cooldown     time.Duration // open → half-open 冷却
	CloseSamples int           // half-open 连续成功样本数 → closed
}

// DefaultBreakerConfig 默认：5 分钟窗口 ≥8 样本、失败率 ≥50% 翻开，冷却 60s，
// 试探 3 连成功闭合。
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		Window:       5 * time.Minute,
		MinSamples:   8,
		Threshold:    0.5,
		Cooldown:     60 * time.Second,
		CloseSamples: 3,
	}
}

// BreakerState 为熔断状态。
type BreakerState string

const (
	BreakerClosed   BreakerState = "closed"
	BreakerOpen     BreakerState = "open"
	BreakerHalfOpen BreakerState = "half_open"
)

type breakerSample struct {
	at time.Time
	ok bool
}

// Breaker 为全局熔断器（纯内存判定 + 状态迁移回调落审计）。
type Breaker struct {
	mu       sync.Mutex
	cfg      BreakerConfig
	state    BreakerState
	openedAt time.Time
	samples  []breakerSample // closed 态滚动窗口
	// half-open 试探期结果（连续成功计数 / 任一失败）
	trialOK int
	// now 为可注入时钟（Service 装配复用同一 Now；同包测试注入）
	now func() time.Time
}

// NewBreaker 创建熔断器（cfg 零值回落默认）。
func NewBreaker(cfg BreakerConfig) *Breaker {
	if cfg.Window <= 0 || cfg.MinSamples <= 0 || cfg.Threshold <= 0 || cfg.Cooldown <= 0 || cfg.CloseSamples <= 0 {
		cfg = DefaultBreakerConfig()
	}
	return &Breaker{cfg: cfg, state: BreakerClosed, now: time.Now}
}

// SetClock 注入时钟（Service 装配时复用同一 Now；测试用）。
func (b *Breaker) SetClock(now func() time.Time) {
	if now != nil {
		b.now = now
	}
}

// State 返回当前状态。
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	// open 到期惰性转 half-open（免定时器）
	if b.state == BreakerOpen && b.now().Sub(b.openedAt) >= b.cfg.Cooldown {
		b.state = BreakerHalfOpen
		b.trialOK = 0
	}
	return b.state
}

// Allow 判定当前是否放行新提交（open=false 时拒绝并给出剩余冷却）。
func (b *Breaker) Allow() (ok bool, retryAfter time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerOpen:
		if elapsed := b.now().Sub(b.openedAt); elapsed < b.cfg.Cooldown {
			return false, b.cfg.Cooldown - elapsed
		}
		b.state = BreakerHalfOpen
		b.trialOK = 0
		return true, 0
	default: // closed / half_open
		return true, 0
	}
}

// Record 登记一条真实执行终态样本并评估状态迁移。
// 返回 (prevState, newState, tripped)——tripped=true 表示本次判定翻开熔断
// （调用方据此落审计事件 circuit_breaker_open / circuit_breaker_closed）。
func (b *Breaker) Record(at time.Time, ok bool) (BreakerState, BreakerState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	prev := b.state
	if prev == BreakerOpen && at.Sub(b.openedAt) < b.cfg.Cooldown {
		// 冷却期内到达的迟到结果不参与判定（不重置 openedAt）
		return prev, prev, false
	}
	if prev == BreakerOpen {
		b.state = BreakerHalfOpen
		b.trialOK = 0
		prev = BreakerHalfOpen
	}
	switch b.state {
	case BreakerHalfOpen:
		if ok {
			b.trialOK++
			if b.trialOK >= b.cfg.CloseSamples {
				b.state = BreakerClosed
				b.samples = nil
				return prev, BreakerClosed, false
			}
			return prev, BreakerHalfOpen, false
		}
		b.state = BreakerOpen
		b.openedAt = at
		b.samples = nil
		return prev, BreakerOpen, true // reopen（half-open 试探失败）
	default: // closed
		b.samples = append(b.samples, breakerSample{at: at, ok: ok})
		cutoff := at.Add(-b.cfg.Window)
		kept := b.samples[:0]
		for _, s := range b.samples {
			if s.at.After(cutoff) {
				kept = append(kept, s)
			}
		}
		b.samples = kept
		if len(b.samples) >= b.cfg.MinSamples {
			fails := 0
			for _, s := range b.samples {
				if !s.ok {
					fails++
				}
			}
			if float64(fails)/float64(len(b.samples)) >= b.cfg.Threshold {
				b.state = BreakerOpen
				b.openedAt = at
				b.samples = nil
				return prev, BreakerOpen, true
			}
		}
		return prev, BreakerClosed, false
	}
}

// Reset 人工复位为 closed（运维干预；调用方落审计）。返回是否发生状态变化。
func (b *Breaker) Reset() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == BreakerClosed {
		return false
	}
	b.state = BreakerClosed
	b.samples = nil
	b.trialOK = 0
	return true
}
