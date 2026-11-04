package callback

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrWorkerConfiguration = errors.New("CALLBACK_WORKER_CONFIGURATION_INVALID")
	ErrWorkerDisabled      = errors.New("CALLBACK_WORKER_DISABLED")
	ErrWorkerStarted       = errors.New("CALLBACK_WORKER_ALREADY_STARTED_OR_STOPPED")
	ErrWorkerContext       = errors.New("CALLBACK_WORKER_CONTEXT_CLOSED")
	ErrWorkerJoinPending   = errors.New("CALLBACK_WORKER_JOIN_PENDING")
)

// RecoveryStore must recover at most one expired lease in its current external
// generation. True means a confirmed committed settlement, not a selected row.
// Each operation checks its local generation gate and uses ctx; mutations
// also lock/recheck database runtime authority and fail closed on uncertain
// commits. Selection must be bounded by copied trusted policies.
// It never sends, renews a lease, or recovers a different generation.
type RecoveryStore interface {
	Store
	RecoverOneExpired(context.Context) (bool, error)
}

// WorkerConfig is trusted composition, not request/CLI configuration. The zero
// value is disabled. There is one goroutine, no queue, and no parallel attempts.
// Timeouts bound cooperative work, not arbitrary adapters ignoring cancellation.
// RunTimeout excludes Sender's independent 1s settlement and store cleanup.
type WorkerConfig struct {
	Enabled                                                    bool
	PollInterval, RecoveryTimeout, RunTimeout, ShutdownTimeout time.Duration
}

// WorkerHealth describes lifecycle only, NOT database/generation/UTC readiness.
// It contains no identity, URL, body, key, driver error or adapter-supplied code.
// Active remains true until the real loop has exited, even after Stop times out.
type WorkerHealth struct {
	State, Code                          string
	Active                               bool
	Recovered, Claimed, TransportInvoked uint64
}

// Worker is single-use. Constructing/importing it has no effects. The same store
// behind the concrete Sender supplies recovery; unrelated adapters cannot be
// accidentally paired. Ownership of sender/store must not be shared with another
// loop. The caller retains the pool until Stop confirms the join.
type Worker struct {
	mu      sync.Mutex
	sender  *Sender
	store   RecoveryStore
	config  WorkerConfig
	health  WorkerHealth
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewWorker(sender *Sender, config WorkerConfig) (*Worker, error) {
	w := &Worker{sender: sender, config: config, done: make(chan struct{}), health: WorkerHealth{State: "disabled", Code: "DISABLED"}}
	if !config.Enabled {
		return w, nil
	}
	if sender == nil || config.PollInterval < 100*time.Millisecond || config.PollInterval > time.Minute || config.RecoveryTimeout < 10*time.Millisecond || config.RecoveryTimeout > 10*time.Second || config.RunTimeout < 10*time.Millisecond || config.RunTimeout > 10*time.Second || config.ShutdownTimeout < 10*time.Millisecond || config.ShutdownTimeout > time.Minute {
		return nil, ErrWorkerConfiguration
	}
	store, ok := sender.d.Store.(RecoveryStore)
	if !ok {
		return nil, ErrWorkerConfiguration
	}
	w.store = store
	w.health = WorkerHealth{State: "new", Code: "NOT_STARTED"}
	return w, nil
}

func (w *Worker) Health() WorkerHealth {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.health
}

func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.config.Enabled {
		return ErrWorkerDisabled
	}
	if w.started {
		return ErrWorkerStarted
	}
	if ctx == nil || ctx.Err() != nil {
		return ErrWorkerContext
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.started = true
	w.health.State, w.health.Code, w.health.Active = "running", "RUNNING", true
	go w.run(ctx)
	return nil
}

// Stop cancels and joins the actual loop, including Sender.finish and Runner
// cleanup. A timeout is not a join: Active stays true and the caller must retain
// dependencies and call Stop again. Nil proves only the resource join, not
// durable settlement: inspect Health for latched reconciliation failures. An
// already-issued request may have an unknown effect. Stop is safe before Start
// and concurrently.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.config.Enabled {
		w.mu.Unlock()
		return nil
	}
	if !w.started {
		w.started = true
		w.health.State, w.health.Code = "stopped", "STOPPED"
		close(w.done)
	}
	if w.health.Active {
		w.cancel()
		if w.health.State != "halted" {
			w.health.State, w.health.Code = "stopping", "JOIN_PENDING"
		}
	}
	w.mu.Unlock()
	// A completed join wins even if the caller's context has since expired.
	select {
	case <-w.done:
		return nil
	default:
	}
	if ctx == nil {
		return ErrWorkerJoinPending
	}
	wait, cancel := context.WithTimeout(ctx, w.config.ShutdownTimeout)
	defer cancel()
	select {
	case <-w.done:
		return nil
	case <-wait.Done():
		return ErrWorkerJoinPending
	}
}

func workerIncrement(n *uint64) {
	if *n < ^uint64(0) {
		(*n)++
	}
}

func (w *Worker) halt(code string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.health.State, w.health.Code = "halted", code
}

func (w *Worker) run(ctx context.Context) {
	defer func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.cancel()
		w.health.Active = false
		if w.health.State != "halted" {
			w.health.State, w.health.Code = "stopped", "STOPPED"
		}
		close(w.done)
	}()
	for ctx.Err() == nil {
		recovery, cancel := context.WithTimeout(ctx, w.config.RecoveryTimeout)
		recovered, err := w.store.RecoverOneExpired(recovery)
		cancel()
		if err != nil {
			// Cancellation can hide an uncertain commit; do not claim clean
			// reconciliation simply because shutdown was requested concurrently.
			w.halt("RECOVERY_UNCONFIRMED")
			return
		}
		w.mu.Lock()
		if recovered {
			workerIncrement(&w.health.Recovered)
		}
		w.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		attempt, stop := context.WithTimeout(ctx, w.config.RunTimeout)
		result := w.sender.RunOne(attempt)
		stop()
		w.mu.Lock()
		if result.Claimed {
			workerIncrement(&w.health.Claimed)
		}
		if result.TransportInvoked {
			workerIncrement(&w.health.TransportInvoked)
		}
		w.mu.Unlock()
		if result.Reconcile {
			w.halt("RECONCILIATION_REQUIRED")
			return
		}
		if result.Code == "CLAIM_FAILED" {
			w.halt("CLAIM_UNCONFIRMED")
			return
		}
		if ctx.Err() != nil {
			return
		}
		// Always pace, even after successful work. Recovery cannot drain without
		// giving Claim a turn; Claim retains its existing durable fairness policy.
		timer := time.NewTimer(w.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
