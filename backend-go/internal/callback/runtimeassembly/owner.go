package runtimeassembly

import (
	"context"
	"errors"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"sync"
	"time"
)

var (
	ErrConfiguration = errors.New("CALLBACK_ASSEMBLY_CONFIGURATION_INVALID")
	ErrBlocked       = errors.New("CALLBACK_ASSEMBLY_BLOCKED")
	ErrConsumed      = errors.New("CALLBACK_ASSEMBLY_CONSUMED")
	ErrPending       = errors.New("CALLBACK_ASSEMBLY_JOIN_PENDING")
	ErrRelease       = errors.New("CALLBACK_ASSEMBLY_RELEASE_UNCONFIRMED")
	ErrRecord        = errors.New("CALLBACK_ASSEMBLY_RECORD_UNCONFIRMED")
	ErrKey           = errors.New("CALLBACK_ASSEMBLY_EXACT_KEY_UNCONFIRMED")
)

// Dependencies transfer exclusive ownership only on successful construction.
// Runner/pool budgets and adapter authenticity remain unverified prerequisites.
type Dependencies struct {
	Runner    *pg.Runner
	Clock     cb.Clock
	DNS       cb.DNS
	Keys      ExactKeyResolver
	Transport cb.Transport
	Release   func(context.Context) error
}

// launchCapability has no production issuer. Its callbacks are test model
// contracts only, not persistence, authenticated acquisition, or reconciliation.
type incidentRecord struct{ Identity, Code string }

type launchCapability struct {
	lease    *generationLease
	identity string
	claim    func() error
	record   func(context.Context, incidentRecord) (incidentRecord, error)
}
type Health struct {
	State, Code                            string
	Active, JoinPending, ResourcesReleased bool
}
type Owner struct{ *ownerState }
type ownerState struct {
	mu                                             sync.Mutex
	worker                                         *cb.Worker
	lease                                          *generationLease
	capability                                     *launchCapability
	snapshot                                       *bindingSnapshot
	release                                        func(context.Context) error
	cancel                                         context.CancelFunc
	consumed, operation, closing, joined, released bool
	workerStarted                                  bool
	releaseAttempted, recordAttempted              bool
	releaseFailed, recordFailed                    bool
	halt                                           string
}

func (Owner) String() string        { return "CallbackOwner(blocked-by-construction,redacted)" }
func (Owner) GoString() string      { return "CallbackOwner(blocked-by-construction,redacted)" }
func (*Owner) Unresolved() []string { return (composition.Plan{}).Unresolved() }
func NewClosed(p composition.Plan, ds []registration.Descriptor, r RuntimeBinding, d Dependencies) (*Owner, error) {
	return newOwner(p, ds, r, d, nil)
}
func newOwner(p composition.Plan, ds []registration.Descriptor, r RuntimeBinding, d Dependencies, c *launchCapability) (*Owner, error) {
	s, err := compileBindings(p, ds, r)
	if err != nil {
		return nil, err
	}
	if d.Runner == nil || d.Clock == nil || d.DNS == nil || d.Keys == nil || d.Transport == nil || d.Release == nil {
		return nil, ErrConfiguration
	}
	lease := &generationLease{snapshot: s}
	if c != nil {
		if c.lease == nil || c.lease.snapshot == nil || c.identity != s.identity || c.lease.snapshot.identity != s.identity || c.claim == nil || c.record == nil {
			return nil, ErrConfiguration
		}
		lease = c.lease
	}
	runtime, err := pg.RuntimeAuthorityFromTrusted(r.Instance, r.Boot, r.Epoch)
	if err != nil {
		return nil, ErrConfiguration
	}
	store, err := pg.NewCallbackStore(d.Runner, runtime, lease.check, s.policies)
	if err != nil {
		return nil, ErrConfiguration
	}
	sender, err := cb.New(cb.Dependencies{Store: store, Clock: d.Clock, Gate: lease, DNS: d.DNS, Keys: exactKeys{s, d.Keys}, Transport: d.Transport}, s.policies)
	if err != nil {
		return nil, ErrConfiguration
	}
	b := p.Budgets().Worker
	ms := time.Millisecond
	worker, err := cb.NewWorker(sender, cb.WorkerConfig{Enabled: true, PollInterval: time.Duration(b.PollMS) * ms, RecoveryTimeout: time.Duration(b.RecoveryMS) * ms, RunTimeout: time.Duration(b.RunMS) * ms, ShutdownTimeout: time.Duration(b.ShutdownMS) * ms})
	if err != nil {
		return nil, ErrConfiguration
	}
	return &Owner{ownerState: &ownerState{worker: worker, lease: lease, capability: c, snapshot: s, release: d.Release}}, nil
}
func (o *Owner) Start(ctx context.Context) error {
	if o == nil || o.ownerState == nil {
		return ErrBlocked
	}
	o.mu.Lock()
	if o.consumed || o.closing {
		o.mu.Unlock()
		return ErrConsumed
	}
	if o.capability == nil || o.worker == nil || o.snapshot == nil {
		o.mu.Unlock()
		return ErrBlocked
	}
	o.consumed = true
	o.operation = true
	parent := ctx
	if parent == nil {
		parent = context.Background()
	}
	run, cancel := context.WithCancel(parent)
	o.cancel = cancel
	c := o.capability
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.operation = false; o.mu.Unlock() }()
	if ctx == nil || ctx.Err() != nil || c.lease.check(ctx) != nil || c.identity != o.snapshot.identity {
		cancel()
		o.lease.close()
		return ErrBlocked
	}
	if safeCall(c.claim) != nil {
		o.mu.Lock()
		o.halt = "ACQUISITION_UNCONFIRMED"
		o.mu.Unlock()
		o.lease.close()
		o.cancelRun()
		return ErrBlocked
	}
	o.mu.Lock()
	closed := o.closing
	o.mu.Unlock()
	if closed || c.lease.check(ctx) != nil {
		o.mu.Lock()
		o.halt = "START_INTERRUPTED"
		o.mu.Unlock()
		o.lease.close()
		o.cancelRun()
		return ErrBlocked
	}
	if err := o.worker.Start(run); err != nil {
		o.mu.Lock()
		o.halt = "START_UNCONFIRMED"
		o.mu.Unlock()
		o.lease.close()
		o.cancelRun()
		return ErrBlocked
	}
	o.mu.Lock()
	o.workerStarted = true
	closed = o.closing
	o.mu.Unlock()
	if closed {
		o.cancelRun()
		return ErrPending
	}
	return nil
}
func (o *Owner) cancelRun() {
	o.mu.Lock()
	cancel := o.cancel
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func safeCall(f func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRecord
		}
	}()
	return f()
}
func (o *Owner) Stop(ctx context.Context) error {
	if o == nil || o.ownerState == nil {
		return nil
	}
	o.mu.Lock()
	o.closing = true
	o.consumed = true
	o.lease.close()
	if o.operation {
		o.mu.Unlock()
		o.cancelRun()
		return ErrPending
	}
	if o.releaseFailed {
		o.mu.Unlock()
		return ErrRelease
	}
	if o.joined && (o.released || o.release == nil) {
		failed := o.recordFailed
		o.mu.Unlock()
		if failed {
			return ErrRecord
		}
		return nil
	}
	o.operation = true
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.operation = false; o.mu.Unlock() }()
	o.cancelRun()
	if o.worker != nil {
		if o.worker.Stop(ctx) != nil {
			return ErrPending
		}
	}
	o.mu.Lock()
	o.joined = true
	o.mu.Unlock()
	if o.worker != nil {
		h := o.worker.Health()
		if h.State == "halted" {
			o.mu.Lock()
			o.halt = h.Code
			o.mu.Unlock()
		}
	}
	o.mu.Lock()
	record := o.capability != nil && !o.recordAttempted
	o.recordAttempted = true
	halt := o.halt
	o.mu.Unlock()
	if record {
		code := halt
		if code == "" {
			code = "STOPPED_BEFORE_START"
			o.mu.Lock()
			if o.workerStarted {
				code = "JOINED"
			}
			o.mu.Unlock()
		}
		recordCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		intent := incidentRecord{Identity: o.snapshot.identity, Code: code}
		var receipt incidentRecord
		recordErr := safeCall(func() error { var err error; receipt, err = o.capability.record(recordCtx, intent); return err })
		deadline, _ := recordCtx.Deadline()
		confirmed := recordErr == nil && recordCtx.Err() == nil && time.Now().Before(deadline) && receipt == intent
		cancel()
		if !confirmed {
			o.mu.Lock()
			o.recordFailed = true
			o.mu.Unlock()
		}
	}
	o.mu.Lock()
	release := o.release != nil && !o.releaseAttempted
	o.releaseAttempted = true
	o.mu.Unlock()
	if release {
		if ctx == nil {
			ctx = context.Background()
		}
		releaseCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := safeCall(func() error { return o.release(releaseCtx) })
		deadline, _ := releaseCtx.Deadline()
		confirmed := err == nil && releaseCtx.Err() == nil && time.Now().Before(deadline)
		cancel()
		o.mu.Lock()
		o.releaseFailed = !confirmed
		o.released = confirmed
		o.mu.Unlock()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.releaseFailed {
		return ErrRelease
	}
	if o.recordFailed {
		return ErrRecord
	}
	return nil
}
func (o *Owner) Health() Health {
	if o == nil || o.ownerState == nil {
		return Health{State: "blocked", Code: "BLOCKED"}
	}
	// The private worker pointer is immutable after construction. Never hold the
	// owner mutex while calling Worker, including its bounded health accessor.
	workerHealth := cb.WorkerHealth{}
	if o.worker != nil {
		workerHealth = o.worker.Health()
	}
	active := workerHealth.Active
	o.mu.Lock()
	defer o.mu.Unlock()
	h := Health{State: "blocked", Code: "BLOCKED", Active: active, ResourcesReleased: o.released}
	if o.consumed {
		h.State = "consumed"
		h.Code = "NOT_RUNNING"
	}
	if o.workerStarted && active {
		h.State = "running"
		h.Code = "RUNNING"
	}
	if o.closing {
		h.State = "closing"
		h.Code = "JOIN_PENDING"
		h.JoinPending = !o.joined || o.operation
	}
	if o.joined {
		// Joined is monotone and supersedes a Worker snapshot sampled before Stop.
		h.Active = false
		h.State = "joined"
		h.Code = "JOINED"
	}
	if workerHealth.State == "halted" {
		h.State = "halted"
		h.Code = workerHealth.Code
	}
	if o.halt != "" {
		h.State = "halted"
		h.Code = o.halt
	}
	if o.recordFailed {
		h.Code = "RECORD_UNCONFIRMED"
	}
	if o.releaseFailed {
		h.Code = "RELEASE_UNCONFIRMED"
	}
	return h
}
