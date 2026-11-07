package gateway

import "time"

const (
	MaxConnections  = 16
	MaxEvents       = 256
	MaxSessionBytes = 524288
	MaxFrameBytes   = 65536
	MaxQueue        = 16
	MaxQueueBytes   = 1048576
)

type budget struct {
	frames, bytes           float64
	last                    time.Time
	totalFrames, totalBytes int
}

func newBudget(now time.Time) budget { return budget{frames: 16, bytes: 131072, last: now} }
func (b *budget) take(now time.Time, n int) bool {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	b.last = now
	b.frames = min(16, b.frames+elapsed*8)
	b.bytes = min(131072, b.bytes+elapsed*65536)
	b.totalFrames++
	b.totalBytes += n
	if n < 0 || n > MaxFrameBytes || b.totalFrames > MaxEvents || b.totalBytes > MaxSessionBytes || b.frames < 1 || b.bytes < float64(n) {
		return false
	}
	b.frames--
	b.bytes -= float64(n)
	return true
}
