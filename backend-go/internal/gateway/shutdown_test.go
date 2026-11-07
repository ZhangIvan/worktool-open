package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
)

const shutdownIncomplete = "GATEWAY_SHUTDOWN_INCOMPLETE_DO_NOT_CLOSE_POOL"

func shutdownController(t *testing.T) *Controller {
	t.Helper()
	// No runner, database, transport or listener. Invalid SessionAuth below is
	// rejected by the real store before any database access.
	c, err := New(&pg.GatewayStore{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func shutdownCanceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func shutdownJoin(t *testing.T, c *Controller) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Shutdown(ctx); err != nil {
		t.Fatalf("join: %v", err)
	}
}

func shutdownPending(t *testing.T, c *Controller) {
	t.Helper()
	if err := c.Shutdown(shutdownCanceledContext()); err == nil || err.Error() != shutdownIncomplete {
		t.Fatalf("pending join: got %v, want %s", err, shutdownIncomplete)
	}
}

func shutdownWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown test worker did not finish")
	}
}

// Scope the count to Shutdown's own goroutine blocks, not total process
// goroutines. Ignore created-by lines and accept nested closure suffixes. These
// tests are deliberately not parallel; both baseline and patch must positively
// expose a waiter before the bound is checked.
func shutdownWaiterCount(t *testing.T) int {
	t.Helper()
	return shutdownGoroutineCount(t, "github.com/ZhangIvan/worktool-open/backend-go/internal/gateway.(*Controller).Shutdown.func")
}

func shutdownGoroutineCount(t *testing.T, prefix string) int {
	t.Helper()
	for size := 64 << 10; size <= 8<<20; size *= 2 {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			continue
		}
		count := 0
		for _, block := range strings.Split(string(buf[:n]), "\n\n") {
			for _, line := range strings.Split(block, "\n") {
				if strings.HasPrefix(line, prefix) {
					count++
					break
				}
			}
		}
		return count
	}
	t.Fatal("goroutine diagnostic exceeded bounded buffer")
	return -1
}

func shutdownWaiterCountEventually(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := shutdownWaiterCount(t)
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("shutdown waiters = %d, want %d", n, want)
		}
		runtime.Gosched()
	}
}

// Register exactly as Upgrade does under c.mu. The returned release performs
// the same finish/delete/Done order. This models an admitted handler, not a real
// WebSocket or a durable database cleanup result.
func shutdownRegistration(t *testing.T, c *Controller) (*connection, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	readCtx, readCancel := context.WithCancel(context.Background())
	x := &connection{parent: c, ctx: ctx, cancel: cancel, readCtx: readCtx, readCancel: readCancel, ingress: newIngressQueue()}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		cancel()
		readCancel()
		t.Fatal("test registration after closing")
	}
	c.live[x] = struct{}{}
	c.wg.Add(1)
	c.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			x.finish()
			c.mu.Lock()
			delete(c.live, x)
			c.mu.Unlock()
			c.wg.Done()
		})
	}
	t.Cleanup(func() {
		release()
		shutdownJoin(t, c)
		shutdownWaiterCountEventually(t, 0)
	})
	return x, release
}

func TestShutdownBoundedWaiterAndCompletedRetry(t *testing.T) {
	c := shutdownController(t)
	_, release := shutdownRegistration(t, c)
	for i := 0; i < 32; i++ {
		shutdownPending(t, c)
	}
	// A newly launched goroutine also has its own function stack entry before
	// its first scheduling turn, so none of the 32 baseline helpers is omitted.
	count := shutdownWaiterCount(t)
	deadline := time.Now().Add(2 * time.Second)
	for count == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
		count = shutdownWaiterCount(t)
	}
	t.Logf("32 canceled pending calls: %d scoped join helper(s)", count)
	if count != 1 {
		t.Errorf("32 canceled pending calls retained %d join helpers; want exactly 1", count)
	}
	release()
	shutdownJoin(t, c)
	shutdownWaiterCountEventually(t, 0)
	for i := 0; i < 32; i++ {
		if err := c.Shutdown(shutdownCanceledContext()); err != nil {
			t.Errorf("completed join retry %d: %v", i, err)
		}
	}
}

func TestShutdownCompletedJoinWinsCanceledCaller(t *testing.T) {
	c := shutdownController(t)
	shutdownJoin(t, c)
	t.Cleanup(func() { shutdownJoin(t, c); shutdownWaiterCountEventually(t, 0) })
	for i := 0; i < 32; i++ {
		if err := c.Shutdown(shutdownCanceledContext()); err != nil {
			t.Errorf("already observed completed join retry %d: %v", i, err)
		}
	}
}

func TestShutdownStopsAllAndRetainsDependenciesUntilJoin(t *testing.T) {
	c := shutdownController(t)
	store := c.store
	x, releaseX := shutdownRegistration(t, c)
	y, releaseY := shutdownRegistration(t, c)
	// Release both before either registration cleanup joins on a fatal path.
	t.Cleanup(func() { releaseX(); releaseY() })
	shutdownPending(t, c)
	for _, conn := range []*connection{x, y} {
		if conn.ctx.Err() != context.Canceled || conn.readCtx.Err() != context.Canceled {
			t.Fatal("shutdown did not stop every registered connection")
		}
		if conn.ingress.offer(context.Background(), received{raw: []byte("pending")}) {
			t.Fatal("stopped connection accepted ingress")
		}
	}
	if c.LiveCount() != 2 || c.store != store {
		t.Fatal("pending join prematurely released live registration or store")
	}
	releaseX()
	shutdownPending(t, c)
	if c.LiveCount() != 1 || c.store != store {
		t.Fatal("one joined connection released the other registration or store")
	}
	releaseY()
	shutdownJoin(t, c)
	if c.LiveCount() != 0 || c.store != store {
		t.Fatal("joined controller changed dependency ownership")
	}
}

func TestShutdownWaitsForConnectionWorkersAndRegistrationDone(t *testing.T) {
	c := shutdownController(t)
	x, release := shutdownRegistration(t, c)
	x.work.Add(1)
	workerRelease := make(chan struct{})
	workerDone := make(chan struct{})
	var workerOnce sync.Once
	unblock := func() { workerOnce.Do(func() { close(workerRelease) }) }
	go func() { defer close(workerDone); <-workerRelease; x.work.Done() }()
	finished := make(chan struct{})
	go func() { defer close(finished); release() }()
	// Registered after the registration cleanup so unblock/join runs first,
	// including fatal paths; no gated test worker is left behind.
	t.Cleanup(func() { unblock(); shutdownWait(t, workerDone); shutdownWait(t, finished) })
	shutdownPending(t, c)
	select {
	case <-finished:
		t.Fatal("connection finished before its worker joined")
	default:
	}
	if c.LiveCount() != 1 {
		t.Fatal("connection removed while worker still pending")
	}
	unblock()
	shutdownWait(t, workerDone)
	shutdownWait(t, finished)
	shutdownJoin(t, c)

	// A live-map snapshot is not proof that the registered handler has returned:
	// model the narrow interval after delete(c.live, x), before wg.Done().
	d := shutdownController(t)
	d.wg.Add(1)
	var doneOnce sync.Once
	done := func() { doneOnce.Do(d.wg.Done) }
	t.Cleanup(func() { done(); shutdownJoin(t, d); shutdownWaiterCountEventually(t, 0) })
	shutdownPending(t, d)
	if d.LiveCount() != 0 {
		t.Fatal("expected empty live map")
	}
	done()
	shutdownJoin(t, d)
}

func TestShutdownConcurrentJoinAndIndependentCancellation(t *testing.T) {
	c := shutdownController(t)
	_, release := shutdownRegistration(t, c)
	const callers = 16
	results := make(chan error, callers)
	var calls sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := 0; i < callers; i++ {
		calls.Add(1)
		go func() { defer calls.Done(); results <- c.Shutdown(ctx) }()
	}
	allDone := make(chan struct{})
	go func() { calls.Wait(); close(allDone) }()
	t.Cleanup(func() { release(); cancel(); shutdownWait(t, allDone) })
	deadline := time.Now().Add(time.Second)
	for {
		n := shutdownGoroutineCount(t, "github.com/ZhangIvan/worktool-open/backend-go/internal/gateway.(*Controller).Shutdown(")
		if n == callers {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d callers entered pending Shutdown", n, callers)
		}
		runtime.Gosched()
	}
	shutdownPending(t, c)
	select {
	case err := <-results:
		t.Fatalf("independent caller returned before actual join: %v", err)
	default:
	}
	release()
	shutdownWait(t, allDone)
	for i := 0; i < callers; i++ {
		if err := <-results; err != nil {
			t.Errorf("successful concurrent join: %v", err)
		}
	}
}

func shutdownUpgradeRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://localhost/ws", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return r
}

func TestShutdownRejectsUpgradeBeforeStoreAccess(t *testing.T) {
	c := shutdownController(t)
	shutdownJoin(t, c)
	t.Cleanup(func() { shutdownJoin(t, c); shutdownWaiterCountEventually(t, 0) })
	// The control verifies valid headers reach admission/auth without shutdown.
	control := shutdownController(t)
	w := httptest.NewRecorder()
	control.Upgrade(w, shutdownUpgradeRequest(), pg.SessionAuth{})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("open admission control: %d", w.Code)
	}
	shutdownJoin(t, control)
	for i := 0; i < 32; i++ {
		w := httptest.NewRecorder()
		c.Upgrade(w, shutdownUpgradeRequest(), pg.SessionAuth{})
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "RESOURCE_LIMIT") {
			t.Fatalf("upgrade after closing: %d %s", w.Code, w.Body.String())
		}
	}
	if c.LiveCount() != 0 {
		t.Fatal("upgrade after closing registered a connection")
	}
}

type shutdownBlockedResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release <-chan struct{}
}

func (w *shutdownBlockedResponse) WriteHeader(code int) {
	close(w.entered)
	<-w.release
	w.ResponseRecorder.WriteHeader(code)
}

func TestShutdownRacesUpgradeAdmission(t *testing.T) {
	// Actual Upgrade and Shutdown race the shared registration mutex. Empty
	// SessionAuth prevents database access and WebSocket acceptance. A response
	// barrier positively holds one real registered Upgrade throughout Shutdown;
	// this is not merely a race where every request might arrive after closing.
	for iteration := 0; iteration < 32; iteration++ {
		c := shutdownController(t)
		heldRelease := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(heldRelease) }) }
		held := &shutdownBlockedResponse{httptest.NewRecorder(), make(chan struct{}), heldRelease}
		heldDone := make(chan struct{})
		go func() { defer close(heldDone); c.Upgrade(held, shutdownUpgradeRequest(), pg.SessionAuth{}) }()
		t.Cleanup(func() { unblock(); shutdownWait(t, heldDone); shutdownJoin(t, c) })
		shutdownWait(t, held.entered)
		if c.LiveCount() != 1 {
			t.Fatal("control Upgrade was not registered at response barrier")
		}
		start := make(chan struct{})
		results := make(chan int, 16)
		var upgrades sync.WaitGroup
		for i := 0; i < cap(results); i++ {
			upgrades.Add(1)
			go func() {
				defer upgrades.Done()
				<-start
				w := httptest.NewRecorder()
				c.Upgrade(w, shutdownUpgradeRequest(), pg.SessionAuth{})
				results <- w.Code
			}()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		joined := make(chan error, 1)
		shutdownReturned := make(chan struct{})
		go func() { defer close(shutdownReturned); <-start; joined <- c.Shutdown(ctx) }()
		upgradesDone := make(chan struct{})
		go func() { upgrades.Wait(); close(upgradesDone) }()
		t.Cleanup(func() {
			unblock()
			cancel()
			shutdownWait(t, upgradesDone)
			shutdownWait(t, shutdownReturned)
		})
		close(start)
		deadline := time.Now().Add(time.Second)
		for {
			c.mu.Lock()
			closing := c.closing
			c.mu.Unlock()
			if closing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("concurrent Shutdown did not close admission")
			}
			runtime.Gosched()
		}
		select {
		case err := <-joined:
			t.Errorf("join returned before real Upgrade deferred cleanup: %v", err)
			// Return the result so every launched caller is consumed below.
			joined <- err
		default:
		}
		unblock()
		shutdownWait(t, heldDone)
		shutdownWait(t, upgradesDone)
		select {
		case err := <-joined:
			cancel()
			if err != nil {
				t.Fatalf("iteration %d: %v", iteration, err)
			}
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("concurrent Shutdown did not return")
		}
		shutdownWait(t, shutdownReturned)
		if held.Code != http.StatusUnauthorized {
			t.Fatalf("registered Upgrade control: %d", held.Code)
		}
		for i := 0; i < cap(results); i++ {
			if code := <-results; code != http.StatusUnauthorized && code != http.StatusServiceUnavailable {
				t.Fatalf("unexpected upgrade response %d", code)
			}
		}
		if c.LiveCount() != 0 {
			t.Fatal("join left a registration live")
		}
	}
	shutdownWaiterCountEventually(t, 0)
}

func TestShutdownDeadlineLeavesJoinAvailable(t *testing.T) {
	c := shutdownController(t)
	_, release := shutdownRegistration(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := c.Shutdown(ctx); err == nil || err.Error() != shutdownIncomplete {
		t.Fatalf("deadline with pending registration: %v", err)
	}
	if ctx.Err() != context.DeadlineExceeded || c.LiveCount() != 1 {
		t.Fatal("deadline did not leave the registration pending")
	}
	release()
	shutdownJoin(t, c)
}

func TestShutdownJoinDoesNotEraseCleanupErrors(t *testing.T) {
	c := shutdownController(t)
	// Synthetic accounting only: no claim that a durable close was attempted.
	c.cleanupErrors.Add(1)
	shutdownJoin(t, c)
	if c.CleanupErrors() != 1 {
		t.Fatal("join erased cleanup failure evidence")
	}
	shutdownWaiterCountEventually(t, 0)
}
