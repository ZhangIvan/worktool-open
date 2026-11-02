package gateway

import (
	"context"
	wire "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol/gatewaywire"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
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
