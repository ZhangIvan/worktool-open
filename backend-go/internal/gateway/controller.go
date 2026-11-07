// Package gateway connects the reviewed DB boundary to actual bounded WebSockets.
// No Android or external service is executed. All host binding is owned by the
// loopback-only httpapi server/explicitly approved integration fixture.
package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	wire "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol/gatewaywire"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/coder/websocket"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Controller struct {
	store         *pg.GatewayStore
	mu            sync.Mutex
	closing       bool
	live          map[*connection]struct{}
	wg            sync.WaitGroup
	shutdownDone  chan struct{} // Created once under mu, after closing excludes wg.Add.
	cleanupErrors atomic.Int64
	// Private test seam, fixed before any connection; production always time.Now.
	// This clock affects ingress budgets only, never authorization or deadlines.
	budgetNow func() time.Time
}

func New(store *pg.GatewayStore) (*Controller, error) {
	if store == nil {
		return nil, errors.New("store required")
	}
	return &Controller{store: store, live: map[*connection]struct{}{}, budgetNow: time.Now}, nil
}
func (c *Controller) CleanupErrors() int64 { return c.cleanupErrors.Load() }
func (c *Controller) LiveCount() int       { c.mu.Lock(); defer c.mu.Unlock(); return len(c.live) }
func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", errors.New("STORAGE_UNAVAILABLE")
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
func upgradeHeader(r *http.Request) bool {
	if len(r.Header.Values("Origin")) != 0 || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Method != "GET" || r.Header.Get("Sec-WebSocket-Version") != "13" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Protocol") != "" {
		return false
	}
	found := false
	for _, x := range strings.Split(r.Header.Get("Connection"), ",") {
		found = found || strings.EqualFold(strings.TrimSpace(x), "upgrade")
	}
	key, e := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	return found && e == nil && len(key) == 16
}
func httpError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": code, "requestId": "req_gateway", "retryable": status == 503, "details": []string{}}})
}

// captureHijack keeps the exact transport owned by this successful upgrade.
// CloseNow cannot interrupt an already-running Close in the pinned WS library.
// No socket from another connection or failed Hijack is retained.
type captureHijack struct {
	http.ResponseWriter
	hijacker   http.Hijacker
	connection *connection
}

func (w *captureHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffer, err := w.hijacker.Hijack()
	if err == nil && conn != nil {
		w.connection.captureTransport(conn)
	}
	return conn, buffer, err
}
func (w *captureHijack) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func captureUpgradeWriter(w http.ResponseWriter, x *connection) http.ResponseWriter {
	current := w
	for {
		if h, ok := current.(http.Hijacker); ok {
			return &captureHijack{ResponseWriter: w, hijacker: h, connection: x}
		}
		u, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		} // Preserve the library's unsupported-Hijacker rejection.
		current = u.Unwrap()
	}
}
func (x *connection) captureTransport(conn net.Conn) {
	x.mu.Lock()
	x.transport = conn
	x.mu.Unlock()
	if x.ctx.Err() != nil {
		x.hardClose()
	}
}
func (x *connection) hardClose() {
	x.mu.Lock()
	conn := x.transport
	x.mu.Unlock()
	if conn != nil {
		x.transportClose.Do(func() { _ = conn.Close() })
	}
	// Authority is already canceled. Only now release the transport reader;
	// its context must not preempt the close frame on a healthy transport.
	x.cancelIngressRead()
}

func (c *Controller) Upgrade(w http.ResponseWriter, r *http.Request, a pg.SessionAuth) {
	if !upgradeHeader(r) {
		httpError(w, 400, "SCHEMA_INVALID")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	readCtx, readCancel := context.WithCancel(r.Context()) // Sibling, not authority child.
	x := &connection{parent: c, ctx: ctx, cancel: cancel, readCtx: readCtx, readCancel: readCancel, wake: make(chan struct{}, 1), ingress: newIngressQueue(), seen: map[string]seenEvent{}, stops: map[string]bool{}}
	c.mu.Lock()
	if c.closing || len(c.live) >= MaxConnections {
		c.mu.Unlock()
		cancel()
		readCancel()
		httpError(w, 503, "RESOURCE_LIMIT")
		return
	}
	c.live[x] = struct{}{}
	c.wg.Add(1)
	c.mu.Unlock()
	defer func() { x.finish(); c.mu.Lock(); delete(c.live, x); c.mu.Unlock(); c.wg.Done() }()
	id, e := randomID("conn_")
	if e != nil {
		httpError(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	a, e = c.store.ConsumeWebSocketSession(ctx, a, id)
	if e != nil {
		code := "UNAUTHENTICATED"
		status := 401
		if e.Error() == "SESSION_ALREADY_USED" {
			code = "SESSION_ALREADY_USED"
			status = 409
		}
		if e.Error() == "COMMIT_OUTCOME_UNKNOWN" || e.Error() == "STORE_TIMEOUT" {
			code = e.Error()
			status = 503
		}
		httpError(w, status, code)
		return
	}
	c.mu.Lock()
	x.auth = a
	c.mu.Unlock()
	if ctx.Err() != nil {
		httpError(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	ws, e := websocket.Accept(captureUpgradeWriter(w, x), r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if e != nil {
		return
	}
	ws.SetReadLimit(MaxFrameBytes)
	x.mu.Lock()
	x.ws = ws
	x.mu.Unlock()
	if ctx.Err() != nil {
		_ = ws.CloseNow()
		return
	}
	// Existing physical sockets remain in live until cleanup; the DB session
	// rotation already revoked them. Only their captured tuples are retired.
	tenant, device, session, _ := a.Identity()
	c.mu.Lock()
	old := []*connection{}
	for y := range c.live {
		if y == x {
			continue
		}
		yt, yd, ys, _ := y.auth.Identity()
		if yt == tenant && yd == device && ys != session {
			old = append(old, y)
		}
	}
	c.mu.Unlock()
	for _, y := range old {
		y.stop(1000, "replaced")
	}
	x.run()
}

// Shutdown stops admission and joins all registered connection cleanup. ctx must
// be non-nil. Cancellation bounds only this caller's wait, not the shared join;
// an incomplete join requires retaining the store/pool for a later retry.
// A nil result confirms the join, not durable cleanup success: check CleanupErrors.
func (c *Controller) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	c.closing = true
	if c.shutdownDone == nil {
		done := make(chan struct{})
		c.shutdownDone = done
		// Upgrade registers under the same mutex and cannot Add after closing.
		// Wait for actual Done, including finish, even if live is already empty.
		go func() { c.wg.Wait(); close(done) }()
	}
	done := c.shutdownDone
	all := make([]*connection, 0, len(c.live))
	for x := range c.live {
		all = append(all, x)
	}
	c.mu.Unlock()
	for _, x := range all {
		x.stop(1001, "shutdown")
	}
	// A previously observed completed join wins over a canceled retry context.
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		select {
		case <-done:
			return nil
		default:
			return errors.New("GATEWAY_SHUTDOWN_INCOMPLETE_DO_NOT_CLOSE_POOL")
		}
	}
}

type seenEvent struct {
	seq    int64
	digest string
}
type outbound struct {
	reply   pg.ReceiptReply
	command *pg.ClaimedIntent
	size    int
}
type connection struct {
	parent             *Controller
	ctx                context.Context
	cancel             context.CancelFunc
	readCtx            context.Context
	readCancel         context.CancelFunc
	auth               pg.SessionAuth
	mu                 sync.Mutex
	ws                 *websocket.Conn
	transport          net.Conn
	transportClose     sync.Once
	lease              *pg.LeaseHandle
	queue              []outbound
	queueBytes         int
	wake               chan struct{}
	work               sync.WaitGroup
	once               sync.Once
	seen               map[string]seenEvent
	stops              map[string]bool
	incoming, outgoing int64
	last               atomic.Int64
	ingress            *ingressQueue
}

func (x *connection) stop(code int, reason string) {
	x.once.Do(func() {
		x.cancel()
		if x.ingress != nil {
			x.ingress.stop()
		}
		x.mu.Lock()
		ws := x.ws
		x.mu.Unlock()
		if ws != nil {
			x.work.Add(1)
			go func() {
				defer x.work.Done()
				defer x.cancelIngressRead()
				deadlineDone := make(chan struct{})
				timer := time.AfterFunc(200*time.Millisecond, func() { x.hardClose(); close(deadlineDone) })
				_ = ws.Close(websocket.StatusCode(code), reason)
				if !timer.Stop() {
					<-deadlineDone
				} // Join an already-fired transport closer.
				x.hardClose()
				_ = ws.CloseNow()
			}()
		} else {
			x.cancelIngressRead() // No socket/close worker can unblock a reader.
		}
	})
}
func (x *connection) finish() {
	x.stop(1000, "closed")
	x.mu.Lock()
	ws := x.ws
	x.mu.Unlock()
	// stop guarantees a read-cancel path: immediate without ws, otherwise
	// normal close completion or the joined 200ms exact-transport closer.
	x.work.Wait()
	x.cancelIngressRead() // Idempotent final cleanup, including failed upgrades.
	if x.ingress != nil {
		x.ingress.drain()
	}
	x.hardClose()
	// stop synchronously registers its bounded close worker. Give that worker
	// the opportunity to send the policy close frame before the final hard close.
	// Authorization cancellation is still immediate; the worker has a 200ms cap.
	if ws != nil {
		_ = ws.CloseNow()
	}
	x.mu.Lock()
	h := x.lease
	x.mu.Unlock()
	if h != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, e := x.parent.store.CloseConnection(ctx, x.auth, *h); e != nil {
			x.parent.cleanupErrors.Add(1)
		}
	}
}
func (x *connection) handle() pg.LeaseHandle { x.mu.Lock(); defer x.mu.Unlock(); return *x.lease }
func (x *connection) run() {
	// Start reading before HELLO decoding, activation PG work or HELLO ACK.
	// The handler remains the one processor; every worker is registered before
	// launch, and finish calls stop before joining the complete work group.
	x.startIngress(x.ws)
	hello, ok := x.ingress.next(x.ctx)
	if !ok {
		return
	}
	capable, policy, active := x.activate(hello.raw)
	x.ingress.done(&hello)
	if !active || x.ctx.Err() != nil {
		return
	}
	x.last.Store(time.Now().UnixNano())
	x.work.Add(2)
	go func() { defer x.work.Done(); x.writer() }()
	go func() { defer x.work.Done(); x.pump(capable, policy) }()
	for {
		frame, ok := x.ingress.next(x.ctx)
		if !ok {
			return
		}
		accepted := x.applyIngress(frame.raw)
		x.ingress.done(&frame)
		if !accepted {
			return
		}
	}
}

func (x *connection) activate(raw []byte) (bool, int64, bool) {
	if x.ctx.Err() != nil {
		return false, 0, false
	}
	hello, e := wire.DecodeHello(raw)
	if e != nil {
		if errors.Is(e, wire.ErrUnsupportedVersion) {
			x.stop(1008, "UNSUPPORTED_VERSION")
		} else {
			x.stop(1002, "PROTOCOL_ERROR")
		}
		return false, 0, false
	}
	if x.ctx.Err() != nil {
		return false, 0, false
	}
	h, e := x.parent.store.AcquireConnection(x.ctx, x.auth)
	if e != nil {
		x.stop(1008, "LEASE_UNAVAILABLE")
		return false, 0, false
	}
	x.mu.Lock()
	x.lease = &h
	x.mu.Unlock()
	if x.ctx.Err() != nil {
		return false, 0, false
	}
	state, e := x.parent.store.ConnectionState(x.ctx, x.auth, h)
	if e != nil {
		x.stop(1008, "UNAUTHENTICATED")
		return false, 0, false
	}
	ack, e := wire.EncodeHelloAck(wire.HelloAck{TenantID: h.Tenant, DeviceID: h.DeviceID, SessionID: h.SessionID, PolicyVersion: state.PolicyVersion, ServerTime: time.UnixMilli(state.ServerTimeMillis).UTC().Truncate(time.Second)})
	if e != nil {
		x.stop(1002, "PROTOCOL_ERROR")
		return false, 0, false
	}
	if e = x.write(ack); e != nil {
		x.stop(1008, "WRITE_FAILED")
		return false, 0, false
	}
	capable := false
	for _, v := range hello.Capabilities {
		capable = capable || v == "message.send_text"
	}
	return capable, state.PolicyVersion, x.ctx.Err() == nil
}

// applyIngress is called only by the serial handler. Returning drops decoded
// references before its caller releases the in-flight raw-message reservation.
func (x *connection) applyIngress(data []byte) bool {
	if x.ctx.Err() != nil {
		return false
	}
	f, e := wire.DecodeDeviceFrame(data)
	if e != nil {
		x.stop(1002, "PROTOCOL_ERROR")
		return false
	}
	prior, duplicate := x.seen[f.Header.EventID]
	if duplicate {
		if prior.seq != f.Header.Seq || prior.digest != f.FrameDigest {
			x.stop(1008, "IDEMPOTENCY_CONFLICT")
			return false
		}
	} else if f.Header.Seq != x.incoming+1 {
		x.stop(1008, "ILLEGAL_SEQUENCE")
		return false
	}
	if x.ctx.Err() != nil {
		return false
	}
	response, e := x.parent.store.ApplyDeviceFrame(x.ctx, x.auth, x.handle(), data)
	if e != nil {
		x.stop(1008, "FRAME_REJECTED")
		return false
	}
	x.last.Store(time.Now().UnixNano())
	if !duplicate {
		x.incoming = f.Header.Seq
		x.seen[f.Header.EventID] = seenEvent{f.Header.Seq, f.FrameDigest}
	}
	if x.ctx.Err() != nil {
		return false
	}
	if response.Kind != "" && !x.enqueue(outbound{reply: response}) {
		x.stop(1008, "RESOURCE_LIMIT")
		return false
	}
	return true
}
func (x *connection) write(b []byte) error {
	ctx, cancel := context.WithTimeout(x.ctx, 2*time.Second)
	defer cancel()
	return x.ws.Write(ctx, websocket.MessageText, b)
}
func key(o outbound) string {
	switch o.reply.Kind {
	case "result_ack":
		return "result:" + o.reply.Body.ResultAck.CommandID
	case "stop_request":
		return "stop:" + o.reply.Body.Stop.CommandID
	case "transport_ack":
		return "transport:" + o.reply.Body.TransportAck.AckEventID
	case "config_notification":
		return "config"
	}
	return ""
}
func (x *connection) enqueue(o outbound) bool {
	b, e := json.Marshal(o.reply.Body)
	if e != nil {
		return false
	}
	o.size = len(b) + 512
	if o.command != nil {
		o.size += len(o.command.CanonicalPayload) + 512
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.ctx.Err() != nil {
		return false
	}
	k := key(o)
	if k != "" {
		for i, old := range x.queue {
			if key(old) == k {
				if x.queueBytes-old.size+o.size > MaxQueueBytes {
					return false
				}
				x.queueBytes += o.size - old.size
				x.queue[i] = o
				return true
			}
		}
	}
	if len(x.queue) >= MaxQueue || x.queueBytes+o.size > MaxQueueBytes {
		return false
	}
	x.queue = append(x.queue, o)
	x.queueBytes += o.size
	select {
	case x.wake <- struct{}{}:
	default:
	}
	return true
}
func (x *connection) pop() (outbound, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.queue) == 0 {
		return outbound{}, false
	}
	v := x.queue[0]
	x.queue = x.queue[1:]
	x.queueBytes -= v.size
	return v, true
}
func (x *connection) writer() {
	for {
		v, ok := x.pop()
		if !ok {
			select {
			case <-x.ctx.Done():
				return
			case <-x.wake:
			}
			continue
		}
		if x.ctx.Err() != nil {
			return
		}
		h := x.handle()
		if v.command != nil {
			if e := x.parent.store.AuthorizeSend(x.ctx, x.auth, h, *v.command); e != nil {
				x.stop(1008, "SEND_NOT_AUTHORIZED")
				return
			}
		}
		if v.command == nil {
			if _, e := x.parent.store.ConnectionState(x.ctx, x.auth, h); e != nil {
				x.stop(1008, "SEND_NOT_AUTHORIZED")
				return
			}
		}
		id, e := randomID("evt_")
		if e != nil {
			x.stop(1011, "STORAGE_UNAVAILABLE")
			return
		}
		x.outgoing++
		frame := wire.ServerFrame{Header: wire.Header{Version: "1.0", Kind: v.reply.Kind, EventID: id, TenantID: h.Tenant, DeviceID: h.DeviceID, SessionID: h.SessionID, Mode: "synthetic", Seq: x.outgoing, SentAt: time.Now().UTC().Truncate(time.Second)}, Body: v.reply.Body}
		b, e := wire.EncodeServerFrame(frame)
		if e != nil {
			x.stop(1002, "PROTOCOL_ERROR")
			return
		}
		if e = x.write(b); e != nil {
			x.stop(1008, "WRITE_FAILED")
			return
		}
	}
}
func (x *connection) pump(capable bool, initialPolicy int64) {
	policyNotified := false
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	renew := time.NewTicker(15 * time.Second)
	defer renew.Stop()
	for {
		select {
		case <-x.ctx.Done():
			return
		case <-renew.C:
			h, e := x.parent.store.RenewConnection(x.ctx, x.auth, x.handle())
			if e != nil {
				x.stop(1008, "SESSION_EXPIRED")
				return
			}
			x.mu.Lock()
			x.lease = &h
			x.mu.Unlock()
		case <-ticker.C:
			if time.Since(time.Unix(0, x.last.Load())) > 45*time.Second {
				x.stop(1008, "HEARTBEAT_TIMEOUT")
				return
			}
			h := x.handle()
			state, e := x.parent.store.ConnectionState(x.ctx, x.auth, h)
			if e != nil {
				x.stop(1008, "SESSION_EXPIRED")
				return
			}
			stop, e := x.parent.store.PolicyStop(x.ctx, x.auth, h)
			if e != nil {
				x.stop(1008, "POLICY_CHECK_FAILED")
				return
			}
			if stop.Kind != "" {
				k := key(outbound{reply: stop})
				if !x.stops[k] {
					x.stops[k] = true
					if !x.enqueue(outbound{reply: stop}) {
						x.stop(1008, "RESOURCE_LIMIT")
						return
					}
				}
			}
			if state.PolicyVersion != initialPolicy {
				if !policyNotified {
					if !x.enqueue(outbound{reply: pg.ReceiptReply{Kind: "config_notification", Body: wire.ServerBody{Config: &wire.ConfigBody{PolicyVersion: state.PolicyVersion, Revoked: true}}}}) {
						x.stop(1008, "RESOURCE_LIMIT")
						return
					}
					policyNotified = true
				}
				capable = false
				continue // keep heartbeat/session/lease bounds alive; no further dispatch
			}
			if !capable {
				continue
			}
			hint, e := x.parent.store.NextCommand(x.ctx, x.auth)
			if e != nil {
				x.stop(1011, "STORAGE_UNAVAILABLE")
				return
			}
			if hint == nil {
				continue
			}
			claim, e := x.parent.store.Claim(x.ctx, x.auth, h, pg.ClaimCommandRequest{CommandID: hint.ID, ExpectedCommandVersion: hint.Version})
			if e != nil {
				x.stop(1008, "CLAIM_REJECTED")
				return
			}
			if claim.Intent == nil {
				continue
			}
			ci := claim.Intent
			var p struct {
				TargetID string `json:"targetId"`
				Text     string `json:"text"`
			}
			if json.Unmarshal([]byte(ci.CanonicalPayload), &p) != nil {
				x.stop(1011, "INTEGRITY_CONFLICT")
				return
			}
			expires, e := time.Parse(time.RFC3339, ci.ExpiresAt)
			if e != nil {
				x.stop(1011, "INTEGRITY_CONFLICT")
				return
			}
			body := wire.TextCommandBody{CommandID: ci.CommandID, AttemptID: ci.AttemptID, PayloadDigest: ci.PayloadDigest, ExpiresAt: expires, PolicyVersion: ci.PolicyVersion, RequiresLocalConfirmation: false, Payload: wire.TextPayload{TargetID: p.TargetID, Text: p.Text}}
			if !x.enqueue(outbound{reply: pg.ReceiptReply{Kind: "command", Body: wire.ServerBody{Command: &body}}, command: ci}) {
				x.stop(1008, "RESOURCE_LIMIT")
				return
			}
		}
	}
}
