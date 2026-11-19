package runtimeassembly

import (
	"context"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"sync/atomic"
	"time"
)

// Only tests currently initialize live=true, before construction. There is no
// production issuer or reopen operation. Closing never erases captured identity.
type generationLease struct {
	live     atomic.Bool
	snapshot *bindingSnapshot
}

func (g *generationLease) close() {
	if g != nil {
		g.live.Store(false)
	}
}
func (g *generationLease) check(ctx context.Context) error {
	if g == nil || g.snapshot == nil || !g.live.Load() || ctx == nil || ctx.Err() != nil {
		return cb.ErrStale
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return cb.ErrStale
	}
	return nil
}
func (g *generationLease) Check(ctx context.Context, h cb.Handle) error {
	if err := g.check(ctx); err != nil {
		return err
	}
	if h.Owner != g.snapshot.runtime.Instance || h.Recovery != g.snapshot.runtime.Epoch {
		return cb.ErrStale
	}
	for b := range g.snapshot.exact {
		if b.Tenant == h.Tenant {
			return nil
		}
	}
	return cb.ErrStale
}
