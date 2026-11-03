package callback

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
)

type timer struct {
	at     time.Time
	cancel context.CancelFunc
}
type fakeClock struct {
	now    time.Time
	timers []timer
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Deadline(parent context.Context, at time.Time) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	c.timers = append(c.timers, timer{at, cancel})
	if !c.now.Before(at) {
		cancel()
	}
	return ctx, cancel
}
func (c *fakeClock) advance(n time.Duration) {
	c.now = c.now.Add(n)
	for _, timer := range c.timers {
		if !c.now.Before(timer.at) {
			timer.cancel()
		}
	}
}

type fixture struct {
	clock                                                           *fakeClock
	claim                                                           Delivery
	policy                                                          Policy
	log                                                             []string
	key                                                             []byte
	keyBinding                                                      *Binding
	keyExpiry                                                       time.Time
	addresses                                                       []netip.Addr
	claimErr, keyErr, dnsErr, prepareErr, completeErr, transportErr error
	found                                                           bool
	status                                                          int
	gateCalls, failGate                                             int
	gateError                                                       error
	gateHook                                                        func(context.Context)
	keyHook, dnsHook, prepareHook, sendHook                         func(context.Context)
	mutatePermit                                                    func(*Permit)
	prepared                                                        *Prepared
	request                                                         *Request
	completed                                                       []Completion
	committed                                                       []Completion
	currentHandle                                                   *Handle
	preparedConsumed                                                bool
	enforceOnce, staleAtComplete                                    bool
	t                                                               *testing.T
}

func newFixture(t *testing.T) (*fixture, *Sender) {
	t.Helper()
	now := time.Date(2026, 11, 3, 1, 50, 0, 0, time.UTC)
	body := []byte("{\"eventId\":\"callback_fixture\",\"message\":\"中文é😀\"}\n")
	digest := sha256.Sum256(body)
	binding := Binding{Tenant: "tenant_fixture", Destination: "dest_fixture", URL: "https://hooks.example.com/callback", KeyRef: "opaque:test-ref", PolicyVersion: 1, KeyVersion: 3}
	f := &fixture{t: t, clock: &fakeClock{now: now}, found: true, status: 204, key: bytes.Repeat([]byte{0x61}, 32), keyExpiry: now.Add(time.Hour), addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	f.claim = Delivery{Handle: Handle{Tenant: "tenant_fixture", DeliveryID: "delivery_fixture", Owner: "worker_fixture", Recovery: "generation_fixture", Fence: 7, Attempt: 1, MaxAttempts: 4}, Binding: binding, EventID: "callback_fixture", Digest: hex.EncodeToString(digest[:]), Body: body, LeaseUntil: now.Add(10 * time.Second), HardDeadline: now.Add(10 * time.Second), ExpiresAt: now.Add(time.Hour)}
	f.policy = Policy{Binding: binding, CIDRs: []string{"8.8.8.0/24"}}
	s, err := New(Dependencies{Store: f, Clock: f.clock, Gate: f, DNS: f, Keys: fakeKeys{f}, Transport: f}, []Policy{f.policy})
	if err != nil {
		t.Fatal(err)
	}
	return f, s
}

func (f *fixture) Claim(ctx context.Context) (Delivery, bool, error) {
	f.log = append(f.log, "claim")
	return f.claim, f.found, f.claimErr
}
func (f *fixture) Check(ctx context.Context, h Handle) error {
	f.gateCalls++
	f.log = append(f.log, "gate")
	if h != f.claim.Handle {
		f.t.Fatal("wrong local authority identity")
	}
	if f.gateHook != nil {
		f.gateHook(ctx)
	}
	if f.gateCalls == f.failGate {
		if f.gateError != nil {
			return f.gateError
		}
		return ErrDenied
	}
	return ctx.Err()
}

type fakeKeys struct{ f *fixture }

func (k fakeKeys) Resolve(ctx context.Context, b Binding) (Key, error) {
	f := k.f
	f.log = append(f.log, "key")
	if b != f.claim.Binding {
		f.t.Fatal("wrong provider request")
	}
	if f.keyHook != nil {
		f.keyHook(ctx)
	}
	if f.keyBinding != nil {
		b = *f.keyBinding
	}
	return Key{Binding: b, Bytes: f.key, ExpiresAt: f.keyExpiry}, f.keyErr
}
func (f *fixture) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	f.log = append(f.log, "dns")
	if host != "hooks.example.com" {
		f.t.Fatal("unexpected DNS target")
	}
	if f.dnsHook != nil {
		f.dnsHook(ctx)
	}
	return f.addresses, f.dnsErr
}
func (f *fixture) Prepare(ctx context.Context, p Prepared) (Permit, error) {
	f.log = append(f.log, "prepare")
	f.prepared = &p
	if f.prepareHook != nil {
		f.prepareHook(ctx)
	}
	if f.prepareErr != nil {
		return Permit{}, f.prepareErr
	}
	permit := Permit{Prepared: p, Fresh: !f.enforceOnce || !f.preparedConsumed}
	f.preparedConsumed = true
	if f.mutatePermit != nil {
		f.mutatePermit(&permit)
	}
	return permit, nil
}
func (f *fixture) Send(ctx context.Context, r Request) (Response, error) {
	f.log = append(f.log, "send")
	f.request = &r
	if ctx.Err() != nil {
		f.t.Fatal("transport invoked with cancelled context")
	}
	if f.sendHook != nil {
		f.sendHook(ctx)
	}
	return Response{Status: f.status}, f.transportErr
}
func (f *fixture) Complete(ctx context.Context, c Completion) error {
	f.log = append(f.log, "complete")
	f.completed = append(f.completed, c)
	if c.Handle != f.claim.Handle {
		f.t.Fatal("settlement substituted identity")
	}
	if ctx.Err() != nil {
		f.t.Fatal("cleanup inherited cancelled context")
	}
	if f.staleAtComplete {
		return ErrStale
	}
	if f.currentHandle != nil && c.Handle != *f.currentHandle {
		return ErrStale
	}
	if !f.clock.Now().Before(f.claim.LeaseUntil) {
		return ErrStale
	}
	if f.completeErr != nil {
		return f.completeErr
	}
	f.committed = append(f.committed, c)
	return nil
}

func assertZeroKey(t *testing.T, f *fixture) {
	t.Helper()
	if !bytes.Equal(f.key, make([]byte, len(f.key))) {
		t.Fatal("provider key ownership not cleared")
	}
}
func assertNoSend(t *testing.T, f *fixture) {
	t.Helper()
	if f.request != nil {
		t.Fatal("unexpected transport call")
	}
}

func TestOrderedExactBytesAndFence(t *testing.T) {
	f, s := newFixture(t)
	original := append([]byte{}, f.claim.Body...)
	f.keyHook = func(context.Context) { f.claim.Body[0] = '!' } // caller-owned bytes change after snapshot
	r := s.RunOne(context.Background())
	if r.State != "delivered" || r.Reconcile || !r.TransportInvoked {
		t.Fatalf("result %+v", r)
	}
	if !reflect.DeepEqual(f.log, []string{"claim", "gate", "key", "dns", "prepare", "gate", "send", "complete"}) {
		t.Fatal(f.log)
	}
	req := f.request
	if req.Method != "POST" || req.ContentType != "application/json" || req.ContentLength != len(original) || !bytes.Equal(req.Body, original) || req.IP.String() != "8.8.8.8" || req.Host != "hooks.example.com" {
		t.Fatal("request boundary changed")
	}
	if !security.Verify(bytes.Repeat([]byte{0x61}, 32), req.EventID, req.Timestamp, req.Signature, req.Body, f.clock.Now().UnixMilli()) {
		t.Fatal("exact transmitted bytes fail signature")
	}
	if len(f.completed) != 1 || f.completed[0].Handle.Fence != 7 || f.completed[0].Handle.Attempt != 1 || !f.completed[0].NextAt.IsZero() {
		t.Fatal("settlement identity")
	}
	assertZeroKey(t, f)
}

func TestNoClaimPermissionOnFailure(t *testing.T) {
	for _, err := range []error{ErrCommitUnknown, errors.New("PRIVATE token database error")} {
		f, s := newFixture(t)
		f.claimErr = err
		r := s.RunOne(context.Background())
		if r.Claimed || r.TransportInvoked || r.Reconcile != errors.Is(err, ErrCommitUnknown) || len(f.log) != 1 {
			t.Fatalf("bad claim failure %+v", r)
		}
	}
	f, s := newFixture(t)
	f.found = false
	if r := s.RunOne(context.Background()); r.Claimed || r.Code != "NO_READY_DELIVERY" {
		t.Fatal(r)
	}
	if r := s.RunOne(nil); r.Code != "CONTEXT_CLOSED" {
		t.Fatal(r)
	}
}

func TestInvalidImmutableSnapshotNeverPrepares(t *testing.T) {
	cases := map[string]func(*Delivery){
		"changed_digest":    func(d *Delivery) { d.Digest = strings.Repeat("0", 64) },
		"body_limit":        func(d *Delivery) { d.Body = make([]byte, 4097) },
		"attempt_zero":      func(d *Delivery) { d.Handle.Attempt = 0 },
		"attempt_exhausted": func(d *Delivery) { d.Handle.Attempt = 5 },
		"attempt_limit":     func(d *Delivery) { d.Handle.MaxAttempts = 5 },
		"fence_zero":        func(d *Delivery) { d.Handle.Fence = 0 },
		"cross_tenant":      func(d *Delivery) { d.Binding.Tenant = "other" },
		"lease_after_hard":  func(d *Delivery) { d.LeaseUntil = d.HardDeadline.Add(time.Second) },
		"hard_after_expiry": func(d *Delivery) { d.HardDeadline = d.ExpiresAt.Add(time.Second) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f, s := newFixture(t)
			change(&f.claim)
			r := s.RunOne(context.Background())
			if !r.Reconcile || r.Code != "INVALID_SNAPSHOT" || len(f.log) != 1 {
				t.Fatal(r, f.log)
			}
		})
	}
}

func TestPolicyAndAllDNSAnswersGate(t *testing.T) {
	for _, mode := range []string{"version", "url", "keyref", "private_answer", "unapproved_public", "empty_answers"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newFixture(t)
			switch mode {
			case "version":
				f.claim.Binding.PolicyVersion++
			case "url":
				f.claim.Binding.URL = "https://other.example.com/callback"
			case "keyref":
				f.claim.Binding.KeyRef = "other-ref"
			case "private_answer":
				f.addresses = append(f.addresses, netip.MustParseAddr("169.254.169.254"))
			case "unapproved_public":
				f.addresses = append(f.addresses, netip.MustParseAddr("1.1.1.1"))
			case "empty_answers":
				f.addresses = nil
			}
			r := s.RunOne(context.Background())
			assertNoSend(t, f)
			if f.prepared != nil || r.State != "dead_letter" || len(f.completed) != 1 {
				t.Fatal(r)
			}
		})
	}
}

func TestProviderNeverFallsBackAndClears(t *testing.T) {
	for _, mode := range []string{"error", "error_with_key", "wrong_version", "wrong_ref", "short_key", "expired"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newFixture(t)
			switch mode {
			case "error":
				f.keyErr = errors.New("PRIVATE provider password")
				f.key = nil
			case "error_with_key":
				f.keyErr = errors.New("PRIVATE provider password")
			case "wrong_version":
				b := f.claim.Binding
				b.KeyVersion++
				f.keyBinding = &b
			case "wrong_ref":
				b := f.claim.Binding
				b.KeyRef = "other"
				f.keyBinding = &b
			case "short_key":
				f.key = f.key[:31]
			case "expired":
				f.keyExpiry = f.clock.Now()
			}
			r := s.RunOne(context.Background())
			assertNoSend(t, f)
			assertZeroKey(t, f)
			if f.prepared != nil || len(f.completed) != 1 || strings.Contains(fmt.Sprint(r), "PRIVATE") {
				t.Fatal(r)
			}
		})
	}
}

func TestPrepareCommitUnknownNeverSends(t *testing.T) {
	for _, err := range []error{ErrCommitUnknown, ErrStale, errors.New("PRIVATE prepare details")} {
		f, s := newFixture(t)
		f.prepareErr = err
		r := s.RunOne(context.Background())
		assertNoSend(t, f)
		assertZeroKey(t, f)
		if !r.Reconcile || r.TransportInvoked || len(f.completed) != 0 {
			t.Fatal(r)
		}
	}
}

func TestPermitCannotReplayOrChangeAuthority(t *testing.T) {
	for _, mode := range []string{"replay", "fence", "owner", "generation", "event", "digest", "ip", "deadline", "key"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newFixture(t)
			f.mutatePermit = func(p *Permit) {
				switch mode {
				case "replay":
					p.Fresh = false
				case "fence":
					p.Prepared.Handle.Fence++
				case "owner":
					p.Prepared.Handle.Owner = "other"
				case "generation":
					p.Prepared.Handle.Recovery = "new"
				case "event":
					p.Prepared.EventID = "other_event"
				case "digest":
					p.Prepared.Digest = strings.Repeat("0", 64)
				case "ip":
					p.Prepared.IP = netip.MustParseAddr("1.1.1.1")
				case "deadline":
					p.Prepared.Deadline = p.Prepared.Deadline.Add(time.Second)
				case "key":
					p.Prepared.Binding.KeyVersion++
				}
			}
			r := s.RunOne(context.Background())
			assertNoSend(t, f)
			assertZeroKey(t, f)
			if !r.Reconcile || len(f.completed) != 0 {
				t.Fatal(r)
			}
		})
	}
	f, s := newFixture(t)
	f.enforceOnce = true
	if r := s.RunOne(context.Background()); !r.TransportInvoked {
		t.Fatal(r)
	}
	f.key = bytes.Repeat([]byte{0x61}, 32)
	f.request = nil
	if r := s.RunOne(context.Background()); r.TransportInvoked || !r.Reconcile {
		t.Fatal(r)
	}
	assertNoSend(t, f)
}

func TestRevocationBeforeSend(t *testing.T) {
	for _, where := range []string{"initial_gate", "prepare", "final_gate"} {
		t.Run(where, func(t *testing.T) {
			f, s := newFixture(t)
			switch where {
			case "initial_gate":
				f.failGate = 1
			case "prepare":
				f.prepareErr = ErrDenied
			case "final_gate":
				f.failGate = 2
			}
			r := s.RunOne(context.Background())
			assertNoSend(t, f)
			if r.State != "cancelled" || len(f.completed) != 1 {
				t.Fatal(r)
			}
			if r.Outcome != "permanent_failure" || f.completed[0].Outcome != "permanent_failure" {
				t.Fatal("cancelled delivery emitted unsupported SQL attempt outcome")
			}
		})
	}
}

func TestBudgetIncludesDNSPrepareAndFinalCheck(t *testing.T) {
	for _, stage := range []string{"key_hard", "dns", "prepare", "key_expiry"} {
		t.Run(stage, func(t *testing.T) {
			f, s := newFixture(t)
			switch stage {
			case "key_hard":
				f.keyHook = func(ctx context.Context) {
					f.clock.advance(10 * time.Second)
					if ctx.Err() == nil {
						t.Fatal("claim deadline not propagated")
					}
				}
			case "dns":
				f.dnsHook = func(ctx context.Context) {
					f.clock.advance(2 * time.Second)
					if ctx.Err() == nil {
						t.Fatal("DNS deadline not propagated")
					}
				}
			case "prepare":
				f.prepareHook = func(ctx context.Context) {
					f.clock.advance(2 * time.Second)
					if ctx.Err() == nil {
						t.Fatal("prepare deadline not propagated")
					}
				}
			case "key_expiry":
				f.keyExpiry = f.clock.Now().Add(time.Millisecond)
				f.prepareHook = func(context.Context) { f.clock.advance(time.Millisecond) }
			}
			r := s.RunOne(context.Background())
			assertNoSend(t, f)
			assertZeroKey(t, f)
			if !r.Claimed || len(f.completed) != 1 {
				t.Fatal(r)
			}
		})
	}
	// Key lookup precedes DNS and may consume the separate claim budget.
	f, s := newFixture(t)
	f.keyHook = func(context.Context) { f.clock.advance(3 * time.Second) }
	if r := s.RunOne(context.Background()); r.State != "delivered" {
		t.Fatal(r)
	}
	if !f.prepared.Deadline.Equal(f.clock.Now().Add(2 * time.Second)) {
		t.Fatal("network budget starts before key lookup")
	}
}

func TestTransportTimeoutIsUnknownAndNeverRetriesInline(t *testing.T) {
	for _, mode := range []string{"timeout", "error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.sendHook = func(ctx context.Context) {
				switch mode {
				case "timeout":
					f.clock.advance(2 * time.Second)
				case "cancel":
					cancel()
				}
			}
			if mode == "error" {
				f.transportErr = errors.New("PRIVATE remote response secret")
			}
			r := s.RunOne(ctx)
			if !r.TransportInvoked || r.Outcome != "delivery_unknown" || r.State != "ready" || len(f.completed) != 1 || strings.Contains(fmt.Sprint(r), "PRIVATE") {
				t.Fatal(r)
			}
			count := 0
			for _, step := range f.log {
				if step == "send" {
					count++
				}
			}
			if count != 1 {
				t.Fatal("hidden retry")
			}
		})
	}
}

func TestHTTPClassificationAndAttemptCap(t *testing.T) {
	for _, tc := range []struct {
		status         int
		outcome, state string
	}{{200, "delivered", "delivered"}, {299, "delivered", "delivered"}, {302, "permanent_failure", "dead_letter"}, {307, "permanent_failure", "dead_letter"}, {400, "permanent_failure", "dead_letter"}, {401, "permanent_failure", "dead_letter"}, {408, "retryable_failure", "ready"}, {425, "retryable_failure", "ready"}, {429, "retryable_failure", "ready"}, {503, "retryable_failure", "ready"}, {101, "delivery_unknown", "ready"}, {0, "delivery_unknown", "ready"}} {
		t.Run(strconvStatus(tc.status), func(t *testing.T) {
			f, s := newFixture(t)
			f.status = tc.status
			r := s.RunOne(context.Background())
			if r.Outcome != tc.outcome || r.State != tc.state || len(f.completed) != 1 {
				t.Fatal(r)
			}
		})
	}
	for attempt := 1; attempt <= 4; attempt++ {
		f, s := newFixture(t)
		f.claim.Handle.Attempt = attempt
		f.status = 503
		r := s.RunOne(context.Background())
		if attempt == 4 {
			if r.State != "dead_letter" || !f.completed[0].NextAt.IsZero() {
				t.Fatal(r)
			}
		} else if r.State != "ready" || !f.completed[0].NextAt.Equal(f.clock.Now().Add(time.Second<<(attempt-1))) {
			t.Fatal("bounded delay", r)
		}
	}
	f, s := newFixture(t)
	f.status = 503
	f.claim.LeaseUntil = f.clock.Now().Add(500 * time.Millisecond)
	f.claim.HardDeadline = f.claim.LeaseUntil
	f.claim.ExpiresAt = f.clock.Now().Add(time.Second)
	if r := s.RunOne(context.Background()); r.State != "dead_letter" {
		t.Fatal("retry beyond expiry", r)
	}
	f, s = newFixture(t)
	f.transportErr = ErrTransportPolicy
	if r := s.RunOne(context.Background()); r.State != "dead_letter" || r.Outcome != "permanent_failure" {
		t.Fatal(r)
	}
}

func strconvStatus(n int) string { return fmt.Sprintf("status_%d", n) }

func TestUnconfirmedSettlementPreservesPossibleSend(t *testing.T) {
	for _, err := range []error{ErrCommitUnknown, ErrStale, errors.New("PRIVATE SQL details")} {
		f, s := newFixture(t)
		f.completeErr = err
		r := s.RunOne(context.Background())
		if !r.TransportInvoked || !r.Reconcile || r.State != "" || r.Code != "SETTLEMENT_UNCONFIRMED" || len(f.completed) != 1 {
			t.Fatal(r)
		}
		if strings.Contains(fmt.Sprint(r), "PRIVATE") {
			t.Fatal("raw cause leaked")
		}
	}
}

func TestAdapterFailureClassifications(t *testing.T) {
	f, s := newFixture(t)
	f.dnsErr = errors.New("PRIVATE resolver details")
	r := s.RunOne(context.Background())
	assertNoSend(t, f)
	if r.Code != "DNS_UNAVAILABLE" || r.State != "ready" {
		t.Fatal(r)
	}
	f, s = newFixture(t)
	f.clock.advance(10 * time.Second)
	r = s.RunOne(context.Background())
	assertNoSend(t, f)
	if r.Code != "SETTLEMENT_UNCONFIRMED" || !r.Reconcile || r.State != "" || len(f.committed) != 0 {
		t.Fatal(r)
	}
}

func TestTemporaryGateClosureDoesNotCancelDelivery(t *testing.T) {
	for _, stage := range []int{1, 2} {
		for _, mode := range []string{"temporary", "cancel", "stale_generation"} {
			t.Run(fmt.Sprintf("stage_%d_%s", stage, mode), func(t *testing.T) {
				f, s := newFixture(t)
				f.failGate = stage
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				switch mode {
				case "temporary":
					f.gateError = errors.New("PRIVATE local gate temporarily unavailable")
				case "cancel":
					f.gateHook = func(context.Context) {
						if f.gateCalls == stage {
							cancel()
						}
					}
				case "stale_generation":
					f.gateError = ErrStale
				}
				r := s.RunOne(ctx)
				assertNoSend(t, f)
				if mode == "stale_generation" {
					if !r.Reconcile || len(f.completed) != 0 {
						t.Fatal(r)
					}
				} else if r.State != "ready" || r.Outcome != "retryable_failure" || len(f.committed) != 1 {
					t.Fatal(r)
				}
			})
		}
	}
}

func TestSuccessorLeaseCannotBeSettledByOldAttempt(t *testing.T) {
	f, s := newFixture(t)
	f.sendHook = func(context.Context) { h := f.claim.Handle; h.Fence++; h.Owner = "successor"; f.currentHandle = &h }
	r := s.RunOne(context.Background())
	if !r.TransportInvoked || !r.Reconcile || r.State != "" || len(f.completed) != 1 || len(f.committed) != 0 {
		t.Fatal(r)
	}
}

func TestGenerationUncertaintyDominatesSimultaneousDeadline(t *testing.T) {
	for _, stage := range []int{1, 2} {
		for _, err := range []error{ErrStale, ErrCommitUnknown} {
			f, s := newFixture(t)
			f.failGate, f.gateError = stage, err
			f.gateHook = func(context.Context) {
				if f.gateCalls == stage {
					f.clock.advance(10 * time.Second)
				}
			}
			r := s.RunOne(context.Background())
			assertNoSend(t, f)
			if !r.Reconcile || r.Code != "LOCAL_GENERATION_UNCONFIRMED" || len(f.completed) != 0 {
				t.Fatal("generation uncertainty incorrectly attempted settlement", r)
			}
		}
	}
}

func TestConstructionFailsClosed(t *testing.T) {
	f, _ := newFixture(t)
	d := Dependencies{Store: f, Clock: f.clock, Gate: f, DNS: f, Keys: fakeKeys{f}, Transport: f}
	if _, err := New(d, nil); err == nil {
		t.Fatal("no reviewed policies")
	}
	if _, err := New(d, []Policy{f.policy, f.policy}); err == nil {
		t.Fatal("duplicate policy")
	}
	d.Keys = nil
	if _, err := New(d, []Policy{f.policy}); err == nil {
		t.Fatal("missing provider")
	}
}
