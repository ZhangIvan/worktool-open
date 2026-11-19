package gateway

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func ingressCounts(q *ingressQueue) (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count, q.bytes
}

func assertIngressEmpty(t *testing.T, q *ingressQueue) {
	t.Helper()
	if n, b := ingressCounts(q); n != 0 || b != 0 {
		t.Fatalf("leaked reservations: count=%d bytes=%d", n, b)
	}
}

func ingressWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not terminate")
	}
}

func TestIngressQueueKeepsInflightCountAndFIFO(t *testing.T) {
	q := newIngressQueue()
	ctx := context.Background()
	at := time.Unix(100, 0)
	for i := 0; i < MaxQueue; i++ {
		if !q.offer(ctx, received{raw: []byte{byte(i)}, receivedAt: at.Add(time.Duration(i))}) {
			t.Fatal("early rejection")
		}
	}
	first, ok := q.next(ctx)
	if !ok || first.raw[0] != 0 {
		t.Fatal("first item")
	}
	if q.offer(ctx, received{raw: []byte{16}}) {
		t.Fatal("dequeue incorrectly released in-flight reservation")
	}
	if n, b := ingressCounts(q); n != 16 || b != 16 {
		t.Fatalf("in-flight accounting %d/%d", n, b)
	}
	q.done(&first)
	q.done(&first)
	if first.raw != nil || first.reserved || !first.receivedAt.IsZero() {
		t.Fatal("done retained a message reference")
	}
	if !q.offer(ctx, received{raw: []byte{16}, receivedAt: at.Add(16)}) {
		t.Fatal("completed reservation was not reusable")
	}
	for i := 1; i <= 16; i++ {
		v, ok := q.next(ctx)
		if !ok || v.raw[0] != byte(i) || !v.receivedAt.Equal(at.Add(time.Duration(i))) {
			t.Fatal("FIFO/receive timestamp changed")
		}
		q.done(&v)
	}
	assertIngressEmpty(t, q)
}

func TestIngressQueueCountsBackingCapacity(t *testing.T) {
	q := newIngressQueue()
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		if !q.offer(ctx, received{raw: make([]byte, 1, 131072)}) {
			t.Fatal("exact capacity boundary refused")
		}
	}
	v, _ := q.next(ctx)
	if q.offer(ctx, received{raw: []byte{1}}) {
		t.Fatal("capacity/in-flight bytes omitted")
	}
	q.stop()
	q.drain()
	if n, b := ingressCounts(q); n != 1 || b != 131072 {
		t.Fatalf("drain released active message: %d/%d", n, b)
	}
	q.done(&v)
	assertIngressEmpty(t, q)
}

func TestIngressQueueCancellationDiscardsReadyMessage(t *testing.T) {
	for i := 0; i < 200; i++ {
		q := newIngressQueue()
		ctx, cancel := context.WithCancel(context.Background())
		if !q.offer(ctx, received{raw: []byte{1}}) {
			t.Fatal("offer")
		}
		cancel() // Both channel and context are ready; neither may start work.
		if _, ok := q.next(ctx); ok {
			t.Fatal("ready-channel select bypassed cancellation")
		}
		if q.offer(ctx, received{}) {
			t.Fatal("offered after cancellation")
		}
		q.stop()
		q.drain()
		assertIngressEmpty(t, q)
	}
}

func TestIngressQueueStopBeforeAndAfterProducer(t *testing.T) {
	q := newIngressQueue()
	q.stop()
	q.drain() // Failed upgrade: reader never started, channel remains open.
	if q.offer(context.Background(), received{}) {
		t.Fatal("stop before reader startup was forgotten")
	}
	assertIngressEmpty(t, q)
	q = newIngressQueue()
	if !q.offer(context.Background(), received{}) {
		t.Fatal("zero-byte message did not reserve a slot")
	}
	q.stop()
	close(q.messages)
	if _, ok := q.next(context.Background()); ok {
		t.Fatal("stopped queue yielded work")
	}
	q.drain()
	assertIngressEmpty(t, q)
}

func TestIngressQueueConcurrentStopOfferAndRelease(t *testing.T) {
	for i := 0; i < 100; i++ {
		q := newIngressQueue()
		ctx, cancel := context.WithCancel(context.Background())
		ready := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			defer close(q.messages)
			q.offer(ctx, received{raw: []byte{1}})
			close(ready)
			for n := 0; n < 32; n++ {
				if !q.offer(ctx, received{raw: []byte{1}}) {
					return
				}
			}
		}()
		go func() {
			defer workers.Done()
			for {
				v, ok := q.next(ctx)
				if !ok {
					return
				}
				q.done(&v)
			}
		}()
		<-ready
		cancel()
		q.stop()
		workers.Wait()
		q.drain()
		assertIngressEmpty(t, q)
	}
}

type fakeIngressMessage struct {
	kind websocket.MessageType
	raw  []byte
	err  error
}

type fakeIngressReader struct {
	messages chan fakeIngressMessage
	contexts chan context.Context
	reads    atomic.Int64
}

func (r *fakeIngressReader) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	r.reads.Add(1)
	r.contexts <- ctx
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case m, ok := <-r.messages:
		if !ok {
			return 0, nil, io.EOF
		}
		return m.kind, m.raw, m.err
	}
}

func fakeIngressConnection(clock func() time.Time) (*connection, *fakeIngressReader) {
	return fakeIngressConnectionFromContext(context.Background(), clock)
}

func fakeIngressConnectionFromContext(parent context.Context, clock func() time.Time) (*connection, *fakeIngressReader) {
	ctx, cancel := context.WithCancel(parent)
	readCtx, readCancel := context.WithCancel(parent)
	x := &connection{parent: &Controller{budgetNow: clock}, ctx: ctx, cancel: cancel, readCtx: readCtx, readCancel: readCancel,
		ingress: newIngressQueue()}
	r := &fakeIngressReader{messages: make(chan fakeIngressMessage, 32), contexts: make(chan context.Context, 64)}
	return x, r
}

func joinFakeIngress(t *testing.T, x *connection) {
	t.Helper()
	done := make(chan struct{})
	go func() { x.work.Wait(); close(done) }()
	ingressWait(t, done)
}

func TestIngressReaderBurstWhileHelloProcessorBlocked(t *testing.T) {
	at := time.Unix(100, 0)
	x, r := fakeIngressConnection(func() time.Time { return at })
	defer x.stop(1000, "test cleanup")
	x.startIngress(r)
	r.messages <- fakeIngressMessage{kind: websocket.MessageText, raw: []byte("hello")}
	hello, ok := x.ingress.next(x.ctx)
	if !ok || !hello.receivedAt.Equal(at) {
		t.Fatal("missing HELLO")
	}
	// Hold HELLO exactly as a blocked activation PG call would. No done call
	// until the reader itself rejects the flood and cancels authority.
	for i := 0; i < 16; i++ {
		r.messages <- fakeIngressMessage{kind: websocket.MessageText, raw: []byte{1}}
	}
	ingressWait(t, x.ctx.Done())
	joinFakeIngress(t, x)
	if r.reads.Load() != 17 || x.incoming != 0 || x.outgoing != 0 {
		t.Fatal("reader waited for processor or consumed protocol sequences")
	}
	if n, _ := ingressCounts(x.ingress); n != MaxQueue {
		t.Fatalf("HELLO or queued reservation released before join: %d", n)
	}
	if _, ok := x.ingress.next(x.ctx); ok {
		t.Fatal("queued work started after overflow")
	}
	x.ingress.done(&hello)
	x.ingress.drain()
	assertIngressEmpty(t, x.ingress)
}

func TestIngressReaderQueueOverflowWithRefillingClock(t *testing.T) {
	var ticks atomic.Int64
	x, r := fakeIngressConnection(func() time.Time {
		return time.Unix(100+ticks.Add(1), 0)
	})
	defer x.stop(1000, "test cleanup")
	x.startIngress(r)
	r.messages <- fakeIngressMessage{kind: websocket.MessageText, raw: []byte{1}}
	active, ok := x.ingress.next(x.ctx)
	if !ok {
		t.Fatal("missing active frame")
	}
	for i := 0; i < 16; i++ {
		r.messages <- fakeIngressMessage{kind: websocket.MessageText, raw: []byte{1}}
	}
	ingressWait(t, x.ctx.Done())
	joinFakeIngress(t, x)
	if r.reads.Load() != 17 || ticks.Load() != 18 {
		t.Fatal("queue waited for PG instead of terminating despite available rate tokens")
	}
	x.ingress.done(&active)
	x.ingress.drain()
	assertIngressEmpty(t, x.ingress)
}

func TestIngressReaderEOFDiscardsQueuedButKeepsActive(t *testing.T) {
	x, r := fakeIngressConnection(func() time.Time { return time.Unix(100, 0) })
	defer x.stop(1000, "test cleanup")
	x.startIngress(r)
	r.messages <- fakeIngressMessage{kind: websocket.MessageText, raw: []byte{1}}
	active, ok := x.ingress.next(x.ctx)
	if !ok {
		t.Fatal("missing active item")
	}
	close(r.messages)
	ingressWait(t, x.ctx.Done())
	joinFakeIngress(t, x)
	x.ingress.drain()
	if n, _ := ingressCounts(x.ingress); n != 1 {
		t.Fatal("EOF released a processor-owned item")
	}
	x.ingress.done(&active)
	assertIngressEmpty(t, x.ingress)
}

func TestIngressReaderHelloDeadlineAndCancelJoin(t *testing.T) {
	x, r := fakeIngressConnection(time.Now)
	x.startIngress(r)
	ctx := <-r.contexts
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) < 4*time.Second {
		t.Fatal("HELLO Read lost original five-second deadline")
	}
	x.stop(1001, "shutdown")
	joinFakeIngress(t, x)
	x.finish() // No lease/socket exists; exercises join + drain for pure fake.
	assertIngressEmpty(t, x.ingress)
}

func TestIngressReaderDoesNotQueueBinaryOrPartialError(t *testing.T) {
	for _, m := range []fakeIngressMessage{
		{kind: websocket.MessageBinary, raw: []byte{1}},
		{kind: websocket.MessageText, raw: []byte{1}, err: errors.New("synthetic partial Read")},
	} {
		x, r := fakeIngressConnection(time.Now)
		x.startIngress(r)
		r.messages <- m
		ingressWait(t, x.ctx.Done())
		joinFakeIngress(t, x)
		x.ingress.drain()
		assertIngressEmpty(t, x.ingress)
	}
}

func TestIngressCanceledProcessorNeverTouchesStore(t *testing.T) {
	x, _ := fakeIngressConnection(time.Now)
	x.stop(1001, "shutdown")
	if _, _, active := x.activate([]byte("not decoded")); active {
		t.Fatal("activation after cancellation")
	}
	if x.applyIngress([]byte("not decoded")) {
		t.Fatal("frame processing after cancellation")
	}
	// A nil store/lease here would panic if either canceled path reached PG.
}

// transportOrder records whether hardClose canceled the read BEFORE closing
// the exact transport. The approved order must keep read alive until Close.
type transportOrder struct {
	countedTransport
	readCtx     context.Context
	earlyCancel atomic.Bool
}

func (c *transportOrder) Close() error {
	c.earlyCancel.Store(c.readCtx.Err() != nil)
	return c.countedTransport.Close()
}

func TestIngressAuthorityCancelPreservesReadUntilExactClose(t *testing.T) {
	x, r := fakeIngressConnection(time.Now)
	defer x.stop(1000, "test cleanup")
	own := &transportOrder{readCtx: x.readCtx}
	x.captureTransport(own)
	x.startIngress(r)
	read := <-r.contexts
	// This is the first stage of stop. With an actual ws, its close worker
	// gets a bounded opportunity to write the policy close before hardClose.
	x.cancel()
	x.ingress.stop()
	if x.ctx.Err() == nil || read.Err() != nil {
		t.Fatal("authority/read lifetimes were not separated")
	}
	if x.ingress.offer(x.ctx, received{raw: []byte{1}}) {
		t.Fatal("transport grace period granted execution authority")
	}
	closed := make(chan struct{})
	timer := time.AfterFunc(200*time.Millisecond, func() { x.hardClose(); close(closed) })
	defer timer.Stop()
	ingressWait(t, closed) // Join the fired callback before checking its state.
	joinFakeIngress(t, x)
	if own.closes.Load() != 1 || own.earlyCancel.Load() || x.readCtx.Err() == nil {
		t.Fatal("read cancellation did not follow the exact transport close")
	}
	x.hardClose()
	if own.closes.Load() != 1 {
		t.Fatal("exact transport close lost once-only ownership")
	}
	assertIngressEmpty(t, x.ingress)
}

func TestIngressCompletedReadRechecksRevokedAuthority(t *testing.T) {
	x, r := fakeIngressConnection(time.Now)
	defer x.stop(1000, "test cleanup")
	x.startIngress(r)
	<-r.contexts
	x.cancel()
	x.ingress.stop()
	if x.readCtx.Err() != nil {
		t.Fatal("authority cancellation reached the read sibling")
	}
	r.messages <- fakeIngressMessage{kind: websocket.MessageText, raw: []byte{1}}
	joinFakeIngress(t, x)
	assertIngressEmpty(t, x.ingress)
	if r.reads.Load() != 1 {
		t.Fatal("started another Read after authority revocation")
	}
}

func TestIngressStopBeforeReaderStartCancelsBothContexts(t *testing.T) {
	x, r := fakeIngressConnection(time.Now)
	x.stop(1001, "shutdown")
	if x.ctx.Err() == nil || x.readCtx.Err() == nil {
		t.Fatal("stop without a socket left a context alive")
	}
	x.startIngress(r)
	joinFakeIngress(t, x)
	x.finish()
	if r.reads.Load() != 0 {
		t.Fatal("reader started after stop")
	}
	assertIngressEmpty(t, x.ingress)
}

func TestIngressFinishWithoutSocketCancelsAndJoinsReader(t *testing.T) {
	x, r := fakeIngressConnection(time.Now)
	x.startIngress(r)
	<-r.contexts
	done := make(chan struct{})
	go func() { x.finish(); close(done) }()
	ingressWait(t, done)
	if x.ctx.Err() == nil || x.readCtx.Err() == nil {
		t.Fatal("finish did not cancel both lifetimes")
	}
	assertIngressEmpty(t, x.ingress)
}

func TestIngressRequestCancelReachesBothSiblingContexts(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	x, r := fakeIngressConnectionFromContext(parent, time.Now)
	x.startIngress(r)
	<-r.contexts
	cancel()
	joinFakeIngress(t, x)
	x.finish()
	if x.ctx.Err() == nil || x.readCtx.Err() == nil {
		t.Fatal("external request cancellation was detached")
	}
	assertIngressEmpty(t, x.ingress)
}
