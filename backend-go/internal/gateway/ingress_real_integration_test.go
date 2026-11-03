package gateway

// Source-only real-transport acceptance candidates. Every TestPG entry gates
// before PostgreSQL or a listener. Local/offline verification must report SKIP.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol/gatewaywire"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
)

const ingressRealLockPhase = 1500 * time.Millisecond

// The first sample initializes newBudget; subsequent samples are successful
// complete Reads, exactly as called by readIngress. No replacement time, logging,
// mutex wait, sleep or unbounded append occurs on the production reader path.
type ingressRealClock struct {
	values   chan time.Time
	overflow atomic.Bool
	history  []time.Time // Only the test goroutine reads/writes history.
}

func newIngressRealClock(f *gwFixture) *ingressRealClock {
	c := &ingressRealClock{values: make(chan time.Time, MaxEvents+2)}
	f.controller.budgetNow = c.now // Before the fixture's first WS connection.
	return c
}

func (c *ingressRealClock) now() time.Time {
	now := time.Now()
	select {
	case c.values <- now:
	default:
		c.overflow.Store(true)
	}
	return now // Keep the exact monotonic time.Now value.
}

func (c *ingressRealClock) samples(t *testing.T) []time.Time {
	t.Helper()
	for {
		select {
		case v := <-c.values:
			c.history = append(c.history, v)
		default:
			if c.overflow.Load() {
				t.Fatal("INCONCLUSIVE: complete-Read clock observation overflow")
			}
			return append([]time.Time(nil), c.history...)
		}
	}
}

func (c *ingressRealClock) beforeFirstPump(t *testing.T) {
	t.Helper()
	s := c.samples(t)
	if len(s) < 2 || time.Since(s[0]) >= 750*time.Millisecond {
		t.Fatal("INCONCLUSIVE: processor lock barrier not established before first pump tick")
	}
}

// Called after the connection has joined, so no further Read sample can appear.
// Lengths include HELLO first, then each attempted WS data message in order.
func (c *ingressRealClock) verifyBudget(t *testing.T, lengths []int, rejected bool) {
	t.Helper()
	s := c.samples(t)
	if len(s) < 2 || len(s)-1 > len(lengths) {
		t.Fatal("INCONCLUSIVE: Read samples do not align with attempted raw messages")
	}
	b := newBudget(s[0])
	failed := -1
	for i, now := range s[1:] {
		if i > 0 && now.Before(s[i]) {
			t.Fatal("INCONCLUSIVE: nonmonotonic observed clock")
		}
		if !b.take(now, lengths[i]) {
			if failed != -1 || i != len(s)-2 {
				t.Fatal("Read continued after a terminal budget rejection")
			}
			failed = i
		}
	}
	if rejected && failed == -1 {
		t.Fatal("INCONCLUSIVE: actual complete-Read times did not establish rate rejection")
	}
	if !rejected && (failed != -1 || len(s)-1 != len(lengths)) {
		t.Fatal("INCONCLUSIVE: queue case was rate-limited or did not complete every expected Read")
	}
	t.Logf("real complete-Read evidence: messages=%d span=%s budgetRejected=%t", len(s)-1, s[len(s)-1].Sub(s[1]), failed != -1)
}

type ingressRealHold struct {
	tx       pgx.Tx
	ctx      context.Context
	cancel   context.CancelFunc
	started  time.Time
	pids     []int64
	ownerPID int64
	released bool
}

func holdIngressTenant(t *testing.T, f *gwFixture) *ingressRealHold {
	t.Helper()
	pids := storeBackendPIDs(t, f) // Existing bounded pool, no extra connection.
	started := time.Now()
	ctx, cancel := context.WithTimeout(f.ctx, ingressRealLockPhase)
	tx, err := f.owner.Begin(ctx)
	if err != nil {
		cancel()
		t.Fatal("INCONCLUSIVE: tenant hold begin")
	}
	h := &ingressRealHold{tx: tx, ctx: ctx, cancel: cancel, started: started, pids: pids, ownerPID: int64(f.owner.PgConn().PID())}
	t.Cleanup(func() {
		if err := h.release(); err != nil {
			t.Error("synthetic tenant hold cleanup failed; isolated run cleanup required")
		}
	})
	var marker int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM scope_limits WHERE scope_kind='tenant' AND tenant='tenant_demo' AND scope_id='tenant_demo' FOR UPDATE`).Scan(&marker); err != nil || marker != 1 {
		t.Fatal("INCONCLUSIVE: exact tenant hold unavailable")
	}
	return h
}

func (h *ingressRealHold) release() error {
	defer h.cancel()
	if h.released {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := h.tx.Rollback(ctx)
	if err == nil || errors.Is(err, pgx.ErrTxClosed) {
		h.released = true
		return nil
	}
	return err
}

func releaseIngressTenant(t *testing.T, h *ingressRealHold) {
	t.Helper()
	if err := h.release(); err != nil {
		t.Fatal("synthetic tenant hold rollback failed")
	}
}

func ingressRealUntil(t *testing.T, ctx context.Context, condition string, fn func() bool) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			t.Fatalf("INCONCLUSIVE: %s exceeded bounded phase", condition)
		}
		if fn() {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("INCONCLUSIVE: %s exceeded bounded phase", condition)
		}
	}
}

func ingressRealQueue(x *connection) (count, queued, retained int, stopped bool) {
	x.ingress.mu.Lock()
	defer x.ingress.mu.Unlock()
	return x.ingress.count, len(x.ingress.messages), x.ingress.bytes, x.ingress.stopped
}

func ingressRealConnection(t *testing.T, f *gwFixture, p *peer) *connection {
	t.Helper()
	f.controller.mu.Lock()
	defer f.controller.mu.Unlock()
	for x := range f.controller.live {
		_, device, session, _ := x.auth.Identity()
		if device == p.device && session == p.session {
			return x
		}
	}
	t.Fatal("matching upgraded connection absent")
	return nil
}

func waitIngressProcessorBlocked(t *testing.T, h *ingressRealHold, x *connection, clock *ingressRealClock) {
	t.Helper()
	ingressRealUntil(t, h.ctx, "exact processor PG lock barrier", func() bool {
		var blocked int
		err := h.tx.QueryRow(h.ctx, `SELECT count(DISTINCT pid) FROM pg_locks WHERE NOT granted AND pid=ANY($1::bigint[]) AND $2::integer=ANY(pg_blocking_pids(pid))`, h.pids, h.ownerPID).Scan(&blocked)
		if err != nil {
			t.Fatal("INCONCLUSIVE: exact blocking-owner observation unavailable")
		}
		n, queued, _, stopped := ingressRealQueue(x)
		return blocked > 0 && n == 1 && queued == 0 && !stopped && x.ctx.Err() == nil
	})
	// HELLO has no pump yet; for post-HELLO cases this additionally excludes a
	// pump transaction from being mistaken for the blocked serial processor.
	clock.beforeFirstPump(t)
}

func waitIngressCount(t *testing.T, h *ingressRealHold, x *connection, want int) {
	t.Helper()
	ingressRealUntil(t, h.ctx, "complete-Read outstanding reservations", func() bool {
		n, queued, retained, stopped := ingressRealQueue(x)
		if stopped || x.ctx.Err() != nil {
			t.Fatal("INCONCLUSIVE: connection stopped before required admission state")
		}
		if n > MaxQueue || retained > MaxQueueBytes {
			t.Fatal("ingress reservation exceeded production bound")
		}
		return n == want && queued == want-1
	})
}

func waitIngressCanceledWhileHeld(t *testing.T, h *ingressRealHold, x *connection) {
	t.Helper()
	select {
	case <-x.ctx.Done():
	case <-h.ctx.Done():
		t.Fatal("INCONCLUSIVE: no ingress cancellation before lock-phase deadline")
	}
	if h.released || h.ctx.Err() != nil || time.Since(h.started) >= ingressRealLockPhase {
		t.Fatal("INCONCLUSIVE: cancellation not proven before unlock/PG lock timeout")
	}
	ingressRealUntil(t, h.ctx, "synchronous ingress admission stop", func() bool {
		_, _, _, stopped := ingressRealQueue(x)
		return stopped
	})
}

func ingressRealHelloRaw() []byte {
	return []byte(`{"kind":"hello","supportedVersions":["1.0"],"mode":"synthetic","capabilities":["message.send_text","file.export"]}`)
}

func ingressRealHeartbeatRaw(p *peer, seq int, full bool) ([]byte, string) {
	event := fmt.Sprintf("evt_ingress_%d", seq)
	b, err := json.Marshal(map[string]any{"version": "1.0", "kind": "heartbeat", "eventId": event, "tenantId": "tenant_demo", "deviceId": p.device, "sessionId": p.session, "seq": seq, "sentAt": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), "mode": "synthetic", "body": map[string]any{"policyVersion": 1, "lastReceivedSeq": 0}})
	if err != nil {
		panic("fixed synthetic fixture JSON cannot encode")
	}
	if full && len(b) <= MaxFrameBytes {
		b = append(b, bytes.Repeat([]byte{' '}, MaxFrameBytes-len(b))...)
	}
	return b, event
}

func writeIngressReal(t *testing.T, ctx context.Context, p *peer, raw []byte) {
	t.Helper()
	if err := p.ws.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatal("synthetic bounded WS write failed; redacted")
	}
}

func assertIngressJoined(t *testing.T, f *gwFixture, x *connection) {
	t.Helper()
	waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 }) // Existing 5s bound.
	n, queued, retained, _ := ingressRealQueue(x)
	if n != 0 || queued != 0 || retained != 0 || x.readCtx.Err() == nil || f.controller.CleanupErrors() != 0 {
		t.Fatal("connection/reader/reservations/storage cleanup did not join")
	}
}

func assertIngressNoDataReply(t *testing.T, f *gwFixture, p *peer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	if _, _, err := p.ws.Read(ctx); err == nil {
		t.Fatal("data reply after admission was canceled behind PG lock")
	}
}

func TestIngressRealFixturePayloadAndObservationBound(t *testing.T) {
	if _, err := wire.DecodeHello(ingressRealHelloRaw()); err != nil {
		t.Fatal("HELLO fixture invalid")
	}
	p := &peer{device: "device_demo", session: "session_demo"}
	for _, full := range []bool{false, true} {
		raw, _ := ingressRealHeartbeatRaw(p, 1, full)
		if full && len(raw) != MaxFrameBytes {
			t.Fatal("full fixture not exactly 65536 bytes")
		}
		if _, err := wire.DecodeDeviceFrame(raw); err != nil {
			t.Fatal("heartbeat JSON/trailing whitespace fixture invalid")
		}
	}
	c := &ingressRealClock{values: make(chan time.Time, MaxEvents+2)}
	before := time.Now()
	returned := c.now()
	observed := <-c.values
	if returned != observed || returned.Before(before) || returned.After(time.Now()) {
		t.Fatal("clock observation changed returned time")
	}
	for i := 0; i < MaxEvents+3; i++ {
		c.now()
	}
	if !c.overflow.Load() || len(c.values) != MaxEvents+2 {
		t.Fatal("clock observation is not bounded/nonblocking")
	}
}

func TestPGGatewayGW13(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	clock := newIngressRealClock(f)
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	x := ingressRealConnection(t, f, p)
	h := holdIngressTenant(t, f)
	defer h.release()
	hello := ingressRealHelloRaw()
	lengths := []int{len(hello)}
	writeIngressReal(t, h.ctx, p, hello)
	waitIngressProcessorBlocked(t, h, x, clock)
	for i := 1; i <= 4 && x.ctx.Err() == nil; i++ {
		raw, _ := ingressRealHeartbeatRaw(p, i, true)
		lengths = append(lengths, len(raw))
		if err := p.ws.Write(h.ctx, websocket.MessageText, raw); err != nil {
			break // Expected rejection can stop writes; not itself pass evidence.
		}
	}
	waitIngressCanceledWhileHeld(t, h, x)
	var leases int
	if err := h.tx.QueryRow(h.ctx, `SELECT count(*) FROM device_gateway_leases WHERE tenant='tenant_demo' AND device=$1`, d.DeviceID).Scan(&leases); err != nil || leases != 0 {
		t.Fatal("HELLO activation escaped the held first PG gate")
	}
	releaseIngressTenant(t, h) // Release before joining storage cleanup.
	assertIngressJoined(t, f, x)
	clock.verifyBudget(t, lengths, true)
	assertIngressNoDataReply(t, f, p)
	if f.count(t, `SELECT count(*) FROM device_gateway_leases WHERE tenant='tenant_demo' AND device=$1`, d.DeviceID) != 0 || f.count(t, `SELECT count(*) FROM inbox`) != 0 {
		t.Fatal("canceled HELLO activated/appended after unlock")
	}
}

func TestPGGatewayGW14(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	clock := newIngressRealClock(f)
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	hello := ingressRealHelloRaw()
	writeIngressReal(t, f.ctx, p, hello)
	if p.read(t)["kind"] != "hello_ack" {
		t.Fatal("HELLO activation missing")
	}
	x := ingressRealConnection(t, f, p)
	h := holdIngressTenant(t, f)
	defer h.release()
	lengths := []int{len(hello)}
	for i := 1; i <= 15; i++ {
		raw, _ := ingressRealHeartbeatRaw(p, i, false)
		lengths = append(lengths, len(raw))
		writeIngressReal(t, h.ctx, p, raw)
		if i == 1 {
			waitIngressProcessorBlocked(t, h, x, clock)
		}
	}
	waitIngressCount(t, h, x, 15)
	for i := 16; i <= 17; i++ {
		// Pace from an already-observed successful admission, not client Write.
		timer := time.NewTimer(125 * time.Millisecond)
		select {
		case <-timer.C:
		case <-h.ctx.Done():
			timer.Stop()
			t.Fatal("INCONCLUSIVE: refill pacing exceeded lock-phase deadline")
		}
		raw, _ := ingressRealHeartbeatRaw(p, i, false)
		lengths = append(lengths, len(raw))
		writeIngressReal(t, h.ctx, p, raw)
		if i == 16 {
			waitIngressCount(t, h, x, 16)
		}
	}
	waitIngressCanceledWhileHeld(t, h, x)
	releaseIngressTenant(t, h)
	assertIngressJoined(t, f, x)
	clock.verifyBudget(t, lengths, false) // All 18 data Reads, including HELLO.
	assertIngressNoDataReply(t, f, p)
	if f.count(t, `SELECT count(*) FROM inbox`) != 0 {
		t.Fatal("advisory flood appended durable inbox")
	}
}

func TestPGGatewayGW15(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	p.hello(t)
	x := ingressRealConnection(t, f, p)
	// No further peer Read, CloseRead, Ping or close response until server join.
	// This is a non-reading close peer, not a proven saturated TCP send buffer.
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	var shutdownErr error
	started := time.Now()
	go func() { shutdownErr = f.controller.Shutdown(ctx); close(done) }()
	defer func() { cancel(); <-done }() // Join even if an assertion fails.
	select {
	case <-x.ctx.Done():
	case <-ctx.Done():
		t.Fatal("authority did not cancel during bounded shutdown")
	}
	if x.readCtx.Err() != nil {
		t.Fatal("INCONCLUSIVE: transport grace period was not observed")
	}
	select {
	case <-x.readCtx.Done():
	case <-ctx.Done():
		t.Fatal("non-reading peer prevented transport cancellation")
	}
	transportElapsed := time.Since(started)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("non-reading peer prevented bounded shutdown join")
	}
	if shutdownErr != nil {
		t.Fatal("bounded gateway shutdown failed")
	}
	assertIngressJoined(t, f, x)
	t.Logf("non-reading close peer: observed stop-to-read-cancel=%s; not an exact network deadline claim", transportElapsed)
}

func TestPGGatewayGW16(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	clock := newIngressRealClock(f)
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	hello := ingressRealHelloRaw()
	writeIngressReal(t, f.ctx, p, hello)
	if p.read(t)["kind"] != "hello_ack" {
		t.Fatal("HELLO activation missing")
	}
	x := ingressRealConnection(t, f, p)
	h := holdIngressTenant(t, f)
	defer h.release()
	lengths := []int{len(hello)}
	events := []string{}
	for i := 1; i <= 2; i++ {
		raw, event := ingressRealHeartbeatRaw(p, i, false)
		lengths = append(lengths, len(raw))
		events = append(events, event)
		writeIngressReal(t, h.ctx, p, raw)
		if i == 1 {
			waitIngressProcessorBlocked(t, h, x, clock)
		}
	}
	waitIngressCount(t, h, x, 2)
	releaseIngressTenant(t, h)
	for i, event := range events {
		frame := p.read(t) // Existing helper checks contiguous server sequences.
		if frame["kind"] != "transport_ack" || commandBody(frame)["ackEventId"] != event || p.received != int64(i+1) {
			t.Fatal("PG release changed FIFO receipt/ACK sequence order")
		}
	}
	_ = p.ws.CloseNow()
	assertIngressJoined(t, f, x)
	clock.verifyBudget(t, lengths, false)
	if f.count(t, `SELECT count(*) FROM inbox`) != 0 {
		t.Fatal("advisory ordered frames appended durable inbox")
	}
}
