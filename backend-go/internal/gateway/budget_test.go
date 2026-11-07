package gateway

import (
	"bufio"
	"context"
	"errors"
	wire "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol/gatewaywire"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngressBudget(t *testing.T) {
	now := time.Unix(100, 0)
	b := newBudget(now)
	for i := 0; i < 16; i++ {
		if !b.take(now, 1) {
			t.Fatal("burst")
		}
	}
	if b.take(now, 1) {
		t.Fatal("unbounded rate")
	}
	if !b.take(now.Add(time.Second), 1) {
		t.Fatal("refill")
	}
	b = newBudget(now)
	if b.take(now, 65537) {
		t.Fatal("oversize")
	}
	b = newBudget(now)
	for i := 0; i < 256; i++ {
		if !b.take(now.Add(time.Duration(i)*time.Second), 1) {
			t.Fatal("lifetime early")
		}
	}
	if b.take(now.Add(300*time.Second), 1) {
		t.Fatal("lifetime unbounded")
	}
}
func TestUnnumberedAckCoalescing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &connection{ctx: ctx, wake: make(chan struct{}, 1)}
	a := outbound{reply: pg.ReceiptReply{Kind: "result_ack", Body: wire.ServerBody{ResultAck: &wire.ResultAck{AckEventID: "evt_first", CommandID: "cmd_demo"}}}}
	if !c.enqueue(a) {
		t.Fatal("queue")
	}
	a.reply.Body.ResultAck = &wire.ResultAck{AckEventID: "evt_second", CommandID: "cmd_demo"}
	if !c.enqueue(a) {
		t.Fatal("coalesce")
	}
	v, ok := c.pop()
	if !ok || v.reply.Body.ResultAck.AckEventID != "evt_second" || c.outgoing != 0 {
		t.Fatal("coalescing allocated sequence or wrong event")
	}
	if _, ok = c.pop(); ok {
		t.Fatal("duplicate queue entry")
	}
}

func TestLifetimeByteBudget(t *testing.T) {
	now := time.Unix(100, 0)
	b := newBudget(now)
	for i := 0; i < 8; i++ {
		if !b.take(now.Add(time.Duration(i)*time.Second), 65536) {
			t.Fatal("byte budget early")
		}
	}
	if b.take(now.Add(9*time.Second), 1) {
		t.Fatal("lifetime byte overflow")
	}
}
func TestQueueCountBoundNoSequenceConsumption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &connection{ctx: ctx, wake: make(chan struct{}, 1)}
	for i := 0; i < MaxQueue; i++ {
		if !c.enqueue(outbound{}) {
			t.Fatal("queue early")
		}
	}
	if c.enqueue(outbound{}) || c.outgoing != 0 {
		t.Fatal("queue unbounded or sequence consumed")
	}
	cancel()
	if c.enqueue(outbound{}) {
		t.Fatal("closed queue accepted")
	}
}

// In-memory transport doubles: no listeners, devices or real network endpoints.
type countedTransport struct {
	net.Conn
	closes atomic.Int64
}

func (c *countedTransport) Close() error { c.closes.Add(1); return nil }

type fakeHijackWriter struct {
	http.ResponseWriter
	conn net.Conn
	err  error
}

func (w fakeHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.conn, nil, w.err }

type unwrapWriter struct{ http.ResponseWriter }

func (w unwrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func TestExactTransportCapture(t *testing.T) {
	t.Run("success_unwrap_idempotent_and_isolated", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		x := &connection{ctx: ctx}
		own, other := &countedTransport{}, &countedTransport{}
		w := captureUpgradeWriter(unwrapWriter{fakeHijackWriter{httptest.NewRecorder(), own, nil}}, x)
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijacker lost")
		}
		got, _, err := h.Hijack()
		if err != nil || got != own {
			t.Fatal("transport identity changed")
		}
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); x.hardClose() }()
		}
		wg.Wait()
		if own.closes.Load() != 1 || other.closes.Load() != 0 {
			t.Fatal("close ownership/idempotency")
		}
	})
	t.Run("unsupported_and_failed_hijack_not_captured", func(t *testing.T) {
		x := &connection{ctx: context.Background()}
		plain := httptest.NewRecorder()
		if captureUpgradeWriter(plain, x) != plain {
			t.Fatal("unsupported writer gained hijacker")
		}
		foreign := &countedTransport{}
		w := captureUpgradeWriter(fakeHijackWriter{plain, foreign, errors.New("synthetic hijack failure")}, x)
		if _, _, err := w.(http.Hijacker).Hijack(); err == nil {
			t.Fatal("failure lost")
		}
		x.hardClose()
		if foreign.closes.Load() != 0 {
			t.Fatal("failed hijack transport was closed")
		}
	})
	t.Run("cancellation_before_capture_still_closes", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		x := &connection{ctx: ctx}
		x.hardClose()
		cancel()
		own := &countedTransport{}
		x.captureTransport(own)
		if own.closes.Load() != 1 {
			t.Fatal("late successful hijack survived cancellation")
		}
	})
}
