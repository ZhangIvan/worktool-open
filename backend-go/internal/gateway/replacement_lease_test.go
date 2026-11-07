package gateway

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
)

func replacementHandle(fence int64) pg.LeaseHandle {
	return pg.LeaseHandle{Tenant: "tenant_demo", DeviceID: "device_demo", SessionID: fmt.Sprintf("session_%d", fence), OwnerInstance: "gateway_demo", ConnectionID: fmt.Sprintf("connection_%d", fence), RecoveryEpoch: "recovery_demo", Fence: fence, LeaseUntil: time.Now().Add(time.Minute)}
}

func replacementPeer(t *testing.T, c *Controller, h *pg.LeaseHandle) *connection {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	readCtx, readCancel := context.WithCancel(context.Background())
	x := &connection{parent: c, ctx: ctx, cancel: cancel, readCtx: readCtx, readCancel: readCancel, ingress: newIngressQueue(), lease: h}
	c.mu.Lock()
	c.live[x] = struct{}{}
	c.mu.Unlock()
	// These are synthetic registered peers, not running handlers or DB leases.
	// Only cancel their memory contexts. Never call durable cleanup on the fake.
	t.Cleanup(func() { x.stop(1000, "test finished"); c.mu.Lock(); delete(c.live, x); c.mu.Unlock() })
	return x
}

func TestLeaseReplacementOrdersPublishedFences(t *testing.T) {
	for _, order := range []string{"older_first", "newer_first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			for i := 0; i < 32; i++ {
				c := shutdownController(t)
				a, b := replacementPeer(t, c, nil), replacementPeer(t, c, nil)
				older, newer := replacementHandle(1), replacementHandle(2)
				switch order {
				case "older_first":
					if !a.publishLease(older) || !b.publishLease(newer) {
						t.Fatal("ordered publication failed")
					}
				case "newer_first":
					if !b.publishLease(newer) || a.publishLease(older) {
						t.Fatal("delayed lower fence survived")
					}
				case "concurrent":
					start := make(chan struct{})
					var wg sync.WaitGroup
					wg.Add(2)
					go func() { defer wg.Done(); <-start; a.publishLease(older) }()
					go func() { defer wg.Done(); <-start; b.publishLease(newer) }()
					close(start)
					wg.Wait()
				}
				if a.ctx.Err() != context.Canceled || b.ctx.Err() != nil {
					t.Fatal("newest published lease did not survive")
				}
				if a.handle() != older || b.handle() != newer {
					t.Fatal("exact acquired tuple not retained for cleanup")
				}
			}
		})
	}
}

func TestLeaseReplacementKeepsUnorderedPeers(t *testing.T) {
	for _, kind := range []string{"pending", "other_tenant", "other_device", "other_recovery", "equal_fence"} {
		t.Run(kind, func(t *testing.T) {
			c := shutdownController(t)
			older := replacementHandle(1)
			var peer *pg.LeaseHandle = &older
			switch kind {
			case "pending":
				peer = nil
			case "other_tenant":
				older.Tenant = "tenant_other"
			case "other_device":
				older.DeviceID = "device_other"
			case "other_recovery":
				older.RecoveryEpoch = "recovery_other"
			case "equal_fence":
				older.Fence = 2
			}
			y := replacementPeer(t, c, peer)
			x := replacementPeer(t, c, nil)
			if !x.publishLease(replacementHandle(2)) || y.ctx.Err() != nil {
				t.Fatal("unordered peer was retired")
			}
		})
	}
}

func TestLeaseReplacementCanceledAcquisitionRetainsTuple(t *testing.T) {
	c := shutdownController(t)
	old := replacementHandle(1)
	y := replacementPeer(t, c, &old)
	x := replacementPeer(t, c, nil)
	x.stop(1001, "shutdown")
	h := replacementHandle(2)
	if x.publishLease(h) || x.handle() != h {
		t.Fatal("canceled acquisition lost exact cleanup tuple")
	}
	if y.ctx.Err() != nil {
		t.Fatal("canceled publisher retired peer")
	}
}

func TestLeaseReplacementConcurrentRenewalAndStop(t *testing.T) {
	c := shutdownController(t)
	h := replacementHandle(1)
	y := replacementPeer(t, c, &h)
	x := replacementPeer(t, c, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 64; i++ {
			// Renew may update the observed deadline, never the fence/identity.
			renewed := h
			renewed.LeaseUntil = h.LeaseUntil.Add(time.Duration(i) * time.Millisecond)
			y.mu.Lock()
			y.lease = &renewed
			y.mu.Unlock()
		}
	}()
	go func() { defer wg.Done(); <-start; x.publishLease(replacementHandle(2)) }()
	go func() { defer wg.Done(); <-start; y.stop(1000, "peer closed") }()
	close(start)
	wg.Wait()
	if x.ctx.Err() != nil || y.ctx.Err() != context.Canceled {
		t.Fatal("concurrent renewal/stop changed replacement ordering")
	}
}
