package runtimeassembly

import (
	"context"
	"errors"
	"fmt"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testManifest = `{"version":1,"destinations":[{"tenant":"tenant_cb","app":"app_cb","destination":"destination_a","policy_version":1,"key_version":1,"url":"https://callbacks.example.com/result","cidrs":["93.184.216.0/24"],"signature_profile":"worktool-callback-hmac-sha256-v1","key_ref":"key_cb","provider_revision":{"kind":"exact","id":"revision_001"}}],"budgets":{"runner":{"acquire_ms":1000,"begin_ms":1000,"transaction_ms":3000,"commit_ms":1000,"cleanup_ms":1000},"worker":{"poll_ms":100,"recovery_ms":1000,"run_ms":1000,"shutdown_ms":9000,"join_headroom_ms":1000},"pool":{"max_connections":10,"system_reserve":1,"operational_reserve":1,"roles":[{"role":"worker","replicas":1,"max_conns":2}]}}}`
const testDescriptor = `{"schema":"worktool-destination-descriptor-v1","tenant":"tenant_cb","app":"app_cb","destination":"destination_a","policy_version":1,"key_version":1,"url":"https://callbacks.example.com/result","cidrs":["93.184.216.0/24"],"signature_profile":"worktool-callback-hmac-sha256-v1","key_ref":"key_cb","provider_authority_id":"provider_synthetic","provider_revision":{"kind":"exact","id":"revision_001"}}`

func testConfig(t *testing.T) (composition.Plan, []registration.Descriptor, RuntimeBinding) {
	t.Helper()
	p, e := composition.Parse([]byte(testManifest), "")
	if e != nil {
		t.Fatal(e)
	}
	d, e := registration.Decode([]byte(testDescriptor))
	if e != nil {
		t.Fatal(e)
	}
	return p, []registration.Descriptor{d}, RuntimeBinding{"worker_cb", "boot_cb", "recovery_cb"}
}

type testClock struct{}

func (testClock) Now() time.Time { return time.Now() }
func (testClock) Deadline(ctx context.Context, t time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, t)
}

type testDNS struct{ calls atomic.Int32 }

func (d *testDNS) Resolve(context.Context, string) ([]netip.Addr, error) {
	d.calls.Add(1)
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type testKeys struct {
	calls  atomic.Int32
	bytes  []byte
	mutate func(*ExactKey)
	fail   bool
}

func (k *testKeys) ResolveExact(_ context.Context, v registration.Value) (ExactKey, error) {
	k.calls.Add(1)
	k.bytes = make([]byte, 32)
	for i := range k.bytes {
		k.bytes[i] = byte(i + 1)
	}
	x := ExactKey{Descriptor: v, Key: cb.Key{Binding: cb.Binding{Tenant: v.Tenant, Destination: v.Destination, URL: v.URL, KeyRef: v.KeyRef, PolicyVersion: v.PolicyVersion, KeyVersion: v.KeyVersion}, Bytes: k.bytes, ExpiresAt: time.Now().Add(time.Minute)}}
	if k.mutate != nil {
		k.mutate(&x)
	}
	if k.fail {
		return x, errors.New("PRIVATE secret URI")
	}
	return x, nil
}

type testTransport struct{ calls atomic.Int32 }

func (x *testTransport) Send(context.Context, cb.Request) (cb.Response, error) {
	x.calls.Add(1)
	return cb.Response{Status: 503}, nil
}

type noPool struct{ calls atomic.Int32 }

func (p *noPool) Acquire(context.Context) (pg.Connection, error) {
	p.calls.Add(1)
	return nil, errors.New("unexpected fixture database")
}
func testDeps(t *testing.T, p pg.Pool) (Dependencies, *testKeys, *testTransport) {
	t.Helper()
	r, e := pg.NewRunner(p, pg.Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: 3 * time.Second, Commit: time.Second, Cleanup: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	k := &testKeys{}
	x := &testTransport{}
	return Dependencies{Runner: r, Clock: testClock{}, DNS: &testDNS{}, Keys: k, Transport: x, Release: func(context.Context) error { return nil }}, k, x
}

// One stable scope, not indexed by plan/epoch/owner. This is not durable storage.
type testJournal struct {
	mu       sync.Mutex
	occupied bool
	records  int
	fail     bool
}

func (j *testJournal) claim() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.occupied {
		return ErrBlocked
	}
	j.occupied = true
	return nil
}
func (j *testJournal) record(_ context.Context, intent incidentRecord) (incidentRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records++
	if j.fail {
		return incidentRecord{}, errors.New("PRIVATE persistence")
	}
	return intent, nil
}
func testCapability(t *testing.T, p composition.Plan, ds []registration.Descriptor, r RuntimeBinding, j *testJournal) *launchCapability {
	t.Helper()
	s, e := compileBindings(p, ds, r)
	if e != nil {
		t.Fatal(e)
	}
	g := &generationLease{snapshot: s}
	g.live.Store(true)
	return &launchCapability{lease: g, identity: s.identity, claim: j.claim, record: j.record}
}
func TestClosedOwnerAndZeroFailClosed(t *testing.T) {
	var zero Owner
	if zero.Start(context.Background()) != ErrBlocked || zero.Stop(nil) != nil || zero.Health().ResourcesReleased {
		t.Fatal("zero lifecycle")
	}
	p, ds, r := testConfig(t)
	pool := &noPool{}
	d, k, x := testDeps(t, pool)
	var releases atomic.Int32
	d.Release = func(context.Context) error { releases.Add(1); return nil }
	o, e := NewClosed(p, ds, r, d)
	if e != nil {
		t.Fatal(e)
	}
	if o.Start(context.Background()) != ErrBlocked || pool.calls.Load() != 0 || k.calls.Load() != 0 || x.calls.Load() != 0 || len(o.Unresolved()) != 9 {
		t.Fatal("closed owner did work")
	}
	if o.Stop(context.Background()) != nil || o.Stop(nil) != nil || releases.Load() != 1 || !o.Health().ResourcesReleased || o.Start(context.Background()) != ErrConsumed {
		t.Fatal("stop before start")
	}
	for _, v := range []any{o, *o, r, ExactKey{}} {
		s := fmt.Sprintf("%v %#v", v, v)
		if strings.Contains(s, "callbacks.example") || strings.Contains(s, "key_cb") {
			t.Fatal("unredacted formatting")
		}
	}
}
func TestOwnerReleaseFailureIsPermanent(t *testing.T) {
	for _, panicMode := range []bool{false, true} {
		t.Run(fmt.Sprint(panicMode), func(t *testing.T) {
			p, ds, r := testConfig(t)
			d, _, _ := testDeps(t, &noPool{})
			calls := 0
			d.Release = func(context.Context) error {
				calls++
				if panicMode {
					panic("PRIVATE")
				}
				return errors.New("PRIVATE")
			}
			o, e := NewClosed(p, ds, r, d)
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 3; i++ {
				if o.Stop(context.Background()) != ErrRelease {
					t.Fatal("release failure lost")
				}
			}
			if calls != 1 || o.Health().ResourcesReleased || o.Health().Code != "RELEASE_UNCONFIRMED" {
				t.Fatal("release retried")
			}
		})
	}
}
func TestOwnerReentrantReleaseAndClaim(t *testing.T) {
	p, ds, r := testConfig(t)
	d, _, _ := testDeps(t, &noPool{})
	var o *Owner
	var nested error
	d.Release = func(context.Context) error { nested = o.Stop(context.Background()); return nil }
	var e error
	o, e = NewClosed(p, ds, r, d)
	if e != nil {
		t.Fatal(e)
	}
	if o.Stop(context.Background()) != nil || nested != ErrPending {
		t.Fatal("reentrant release")
	}
	d, _, _ = testDeps(t, &noPool{})
	c := testCapability(t, p, ds, r, &testJournal{})
	c.claim = func() error { nested = o.Stop(context.Background()); return nil }
	o, e = newOwner(p, ds, r, d, c)
	if e != nil {
		t.Fatal(e)
	}
	if o.Start(context.Background()) != ErrBlocked || nested != ErrPending || o.Stop(context.Background()) != nil {
		t.Fatal("reentrant start")
	}
}
func TestOwnerConcurrentStartStopReservation(t *testing.T) {
	p, ds, r := testConfig(t)
	pool := &noPool{}
	d, _, _ := testDeps(t, pool)
	c := testCapability(t, p, ds, r, &testJournal{})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	c.claim = func() error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-time.After(3 * time.Second):
			return ErrBlocked
		}
	}
	o, e := newOwner(p, ds, r, d, c)
	if e != nil {
		t.Fatal(e)
	}
	var result error
	t.Cleanup(func() {
		unblock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("start did not finish after barrier release")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if o.Stop(ctx) != nil {
			t.Error("concurrent fixture failed to join")
		}
	})
	go func() { result = o.Start(context.Background()); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("acquisition barrier not reached")
	}
	if o.Start(context.Background()) != ErrConsumed || o.Stop(context.Background()) != ErrPending {
		t.Error("reservation bypass")
	}
	unblock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("start remained pending")
	}
	if result != ErrBlocked {
		t.Error("start crossed stop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if o.Stop(ctx) != nil || pool.calls.Load() != 0 {
		t.Fatal("unexpected work")
	}
}
func TestBindingAndExactKeyMetadata(t *testing.T) {
	p, ds, r := testConfig(t)
	s, e := compileBindings(p, ds, r)
	if e != nil {
		t.Fatal(e)
	}
	b := s.policies[0].Binding
	for _, field := range []string{"app", "authority", "revision", "binding", "provider_error"} {
		t.Run(field, func(t *testing.T) {
			k := &testKeys{mutate: func(x *ExactKey) {
				switch field {
				case "app":
					x.Descriptor.App = "other"
				case "authority":
					x.Descriptor.ProviderAuthorityID = "other"
				case "revision":
					x.Descriptor.ProviderRevision.ID = "other"
				case "binding":
					x.Key.Binding.KeyVersion++
				}
			}}
			k.fail = field == "provider_error"
			key, e := (exactKeys{s, k}).Resolve(context.Background(), b)
			if e != ErrKey || len(key.Bytes) != 32 || &key.Bytes[0] != &k.bytes[0] {
				t.Fatal("lost ownership or metadata check")
			}
			clear(key.Bytes)
		})
	}
	bad, e := registration.Decode([]byte(strings.Replace(testDescriptor, "app_cb", "other_app", 1)))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = compileBindings(p, []registration.Descriptor{bad}, r); e != ErrConfiguration {
		t.Fatal("app downgraded")
	}
}

// Simulate delayed context timer delivery: Err remains nil after Deadline.
type testDelayedContext struct {
	context.Context
	ns atomic.Int64
}

func (c *testDelayedContext) Deadline() (time.Time, bool) { return time.Unix(0, c.ns.Load()), true }
func TestLateAcquisitionAndFormatting(t *testing.T) {
	p, ds, r := testConfig(t)
	pool := &noPool{}
	d, _, _ := testDeps(t, pool)
	j := &testJournal{}
	cap := testCapability(t, p, ds, r, j)
	ctx := &testDelayedContext{Context: context.Background()}
	ctx.ns.Store(time.Now().Add(time.Minute).UnixNano())
	cap.claim = func() error {
		if e := j.claim(); e != nil {
			return e
		}
		ctx.ns.Store(time.Now().Add(-time.Second).UnixNano())
		return nil
	}
	o, e := newOwner(p, ds, r, d, cap)
	if e != nil {
		t.Fatal(e)
	}
	if o.Start(ctx) != ErrBlocked || pool.calls.Load() != 0 || o.Health().State != "halted" || o.Health().Code != "START_INTERRUPTED" {
		t.Fatal("late acquisition claimed running")
	}
	if o.Stop(context.Background()) != nil || !j.occupied {
		t.Fatal("late acquisition erased occupancy")
	}
	for _, v := range []any{o, *o} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			s := fmt.Sprintf(format, v)
			if strings.Contains(s, "callbacks.example") || strings.Contains(s, "key_cb") || strings.Contains(s, "worker_cb") || strings.Contains(s, "provider_synthetic") {
				t.Fatal("value/pointer format leaked binding")
			}
		}
	}
}

func TestExactKeyCopyIsolationUnknownBindingAndPanic(t *testing.T) {
	p, ds, r := testConfig(t)
	s, e := compileBindings(p, ds, r)
	if e != nil {
		t.Fatal(e)
	}
	b := s.policies[0].Binding
	k := &testKeys{mutate: func(x *ExactKey) { x.Descriptor.CIDRs[0] = "8.8.8.0/24" }}
	key, e := (exactKeys{s, k}).Resolve(context.Background(), b)
	if e != ErrKey || s.exact[b].CIDRs[0] != "93.184.216.0/24" || len(key.Bytes) != 32 {
		t.Fatal("provider mutation changed snapshot or lost bytes")
	}
	clear(key.Bytes)
	k = &testKeys{}
	unknown := b
	unknown.KeyVersion++
	if _, e = (exactKeys{s, k}).Resolve(context.Background(), unknown); e != ErrKey || k.calls.Load() != 0 {
		t.Fatal("unknown binding resolved")
	}
	k = &testKeys{mutate: func(*ExactKey) { panic("PRIVATE provider URI") }}
	if _, e = (exactKeys{s, k}).Resolve(context.Background(), b); e != ErrKey {
		t.Fatal("provider panic not redacted")
	}
	// Panic prevented ownership transfer; provider still owns and clears its bytes.
	clear(k.bytes)
	pool := &noPool{}
	d, _, _ := testDeps(t, pool)
	released := false
	d.Release = func(context.Context) error { released = true; return nil }
	if o, e := NewClosed(p, nil, r, d); o != nil || e != ErrConfiguration || released || pool.calls.Load() != 0 {
		t.Fatal("failed construction transferred resources")
	}
}
