package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

const callbackFixtureURL = "https://callbacks.example.com/result"

type callbackWallClock struct{}

func (callbackWallClock) Now() time.Time { return time.Now() }
func (callbackWallClock) Deadline(ctx context.Context, d time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, d)
}

type callbackLocalGate struct{}

func (callbackLocalGate) Check(context.Context, cb.Handle) error { return nil }

type callbackFixtureDNS struct{}

func (callbackFixtureDNS) Resolve(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type callbackFixtureKey struct {
	binding cb.Binding
	key     []byte
}

func (k callbackFixtureKey) Resolve(_ context.Context, b cb.Binding) (cb.Key, error) {
	if b != k.binding {
		return cb.Key{}, cb.ErrDenied
	}
	return cb.Key{Binding: b, Bytes: bytes.Clone(k.key), ExpiresAt: time.Now().Add(time.Minute)}, nil
}

// This exception exists only in this test file and only maps a fixed synthetic
// identity to the receiver created by this test. No environment-provided URL.
type callbackLoopbackTransport struct {
	client *http.Client
	url    string
}

func (t callbackLoopbackTransport) Send(ctx context.Context, r cb.Request) (cb.Response, error) {
	if r.URL != callbackFixtureURL || r.Host != "callbacks.example.com" || r.IP != netip.MustParseAddr("93.184.216.34") || !strings.HasPrefix(t.url, "http://127.0.0.1:") {
		return cb.Response{}, cb.ErrTransportPolicy
	}
	req, e := http.NewRequestWithContext(ctx, "POST", t.url+"/result", bytes.NewReader(r.Body))
	if e != nil {
		return cb.Response{}, e
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WorkTool-Event-Id", r.EventID)
	req.Header.Set("X-WorkTool-Timestamp", r.Timestamp)
	req.Header.Set("X-WorkTool-Signature", r.Signature)
	resp, e := t.client.Do(req)
	if e != nil {
		return cb.Response{}, e
	}
	defer resp.Body.Close()
	return cb.Response{Status: resp.StatusCode}, nil
}
func callbackSyntheticStore(r *pg.Runner, g func(context.Context) error, a pg.RuntimeAuthority) (*pg.GatewayStore, error) {
	return pg.NewSyntheticCallbackGatewayStore(r, g, a, time.Minute)
}
func callbackSetup(t *testing.T, f *gwFixture) (*pg.CallbackStore, cb.Policy) {
	t.Helper()
	execOwner(t, f.owner, f.ctx, `UPDATE developer_apps SET scopes='["commands:write","results:read","callbacks:read"]' WHERE tenant='tenant_demo' AND id='app_demo';INSERT INTO runtime_instances(instance_id,roles,boot_id,recovery_epoch,state) VALUES('worker_demo',ARRAY['worker'],'boot_worker','recovery_demo','active');INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('tenant_demo','dest_demo','app_demo','https://callbacks.example.com/result',1,'key_demo',1,'active');INSERT INTO scope_limits(scope_kind,tenant,scope_id,destination_id,max_in_flight,rate_per_sec,burst,tokens,policy_version,enabled,capacity_jobs,capacity_bytes,admission_enabled) VALUES('destination','tenant_demo','dest_demo','dest_demo',2,8,16,16,1,true,100,20000000,true);INSERT INTO scope_slots(scope_kind,tenant,scope_id,slot_no) VALUES('tenant','tenant_demo','tenant_demo',0),('tenant','tenant_demo','tenant_demo',1),('destination','tenant_demo','dest_demo',0),('destination','tenant_demo','dest_demo',1);INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('tenant_demo','app_demo','sub_demo',1,'dest_demo',1,1,'command.result','bounded-result-v1',1,65536,4)`)
	runtime, e := pg.RuntimeAuthorityFromTrusted("worker_demo", "boot_worker", "recovery_demo")
	if e != nil {
		t.Fatal("worker identity")
	}
	runner, e := pg.NewRunner(pg.PoolAdapter{Pool: f.pool}, pg.Timeouts{Acquire: 2 * time.Second, Begin: 2 * time.Second, Transaction: 5 * time.Second, Commit: 2 * time.Second, Cleanup: time.Second})
	if e != nil {
		t.Fatal("worker runner")
	}
	policy := cb.Policy{Binding: cb.Binding{Tenant: "tenant_demo", Destination: "dest_demo", URL: callbackFixtureURL, KeyRef: "key_demo", PolicyVersion: 1, KeyVersion: 1}, CIDRs: []string{"93.184.216.0/24"}}
	store, e := pg.NewCallbackStore(runner, runtime, func(context.Context) error { return nil }, []cb.Policy{policy})
	if e != nil {
		t.Fatal("worker store")
	}
	return store, policy
}

// CB01 is a REAL-PG/WS/HTTP candidate, not a pure or device test. It skips before
// any resource when the established isolated-CI gate is absent. Not in CI yet.
func TestPGCallbackCB01(t *testing.T) {
	gatewayGate(t)
	f := newGWWithStoreConstructor(t, callbackSyntheticStore)
	store, policy := callbackSetup(t, f)
	key := []byte(randHex(t, 16))
	t.Cleanup(func() { clear(key) })
	var mu sync.Mutex
	seen := map[string]string{}
	effects := 0
	receiver := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/result" || r.Header.Get("Content-Type") != "application/json" || r.ContentLength < 1 || r.ContentLength > 4096 {
			w.WriteHeader(400)
			return
		}
		body, e := io.ReadAll(io.LimitReader(r.Body, 4097))
		if e != nil || len(body) > 4096 {
			w.WriteHeader(400)
			return
		}
		event := r.Header.Get("X-WorkTool-Event-Id")
		for _, h := range []string{"X-WorkTool-Event-Id", "X-WorkTool-Timestamp", "X-WorkTool-Signature"} {
			if len(r.Header.Values(h)) != 1 {
				w.WriteHeader(400)
				return
			}
		}
		if !security.Verify(key, event, r.Header.Get("X-WorkTool-Timestamp"), r.Header.Get("X-WorkTool-Signature"), body, time.Now().UnixMilli()) {
			w.WriteHeader(401)
			return
		}
		var projection map[string]string
		if json.Unmarshal(body, &projection) != nil || len(projection) != 10 || projection["eventId"] != event || projection["mode"] != "synthetic" {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		digest := sum(string(body))
		if prior, ok := seen[event]; ok && prior != digest {
			w.WriteHeader(409)
			return
		}
		if _, ok := seen[event]; !ok {
			if len(seen) >= 4 {
				w.WriteHeader(429)
				return
			}
			seen[event] = digest
			effects++
		}
		w.WriteHeader(204)
	}))
	receiver.Config.ReadHeaderTimeout = time.Second
	receiver.Config.ReadTimeout = 2 * time.Second
	receiver.Config.WriteTimeout = 2 * time.Second
	receiver.Start()
	t.Cleanup(receiver.Close)
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sender, e := cb.New(cb.Dependencies{Store: store, Clock: callbackWallClock{}, Gate: callbackLocalGate{}, DNS: callbackFixtureDNS{}, Keys: callbackFixtureKey{policy.Binding, key}, Transport: callbackLoopbackTransport{client, receiver.URL}}, []cb.Policy{policy})
	if e != nil {
		t.Fatal("sender composition")
	}
	d := f.enroll(t)
	session := f.session(t, d)
	p := f.dial(t, d, session)
	p.hello(t)
	command, message := f.submit(t, d, "callback_vertical_01")
	cmd := commandBody(p.until(t, "command"))
	start(t, p, cmd)
	event := p.send(t, "execution_result", result(cmd, "OK"))
	assertAck(t, p, event, command)
	if f.count(t, `SELECT count(*) FROM callback_outbox WHERE command=$1 AND state='ready'`, command) != 1 {
		t.Fatal("result/outbox atomic materialization")
	}
	outcome := sender.RunOne(f.ctx)
	if !outcome.TransportInvoked || outcome.State != "delivered" || outcome.Reconcile {
		t.Fatalf("callback classification %s", outcome.Code)
	}
	event = p.send(t, "execution_result", result(cmd, "OK"))
	assertAck(t, p, event, command)
	if f.count(t, `SELECT count(*) FROM callback_outbox WHERE command=$1 AND state='delivered'`, command) != 1 || f.count(t, `SELECT count(*) FROM callback_attempts WHERE outcome='delivered'`) != 1 || f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 0 {
		t.Fatal("immutable delivery/settlement")
	}
	status, receipt := f.request(t, "GET", "/api/developer/v1/messages/"+message, f.appToken, "", nil)
	if status != 200 || receipt["children"].([]any)[0].(map[string]any)["status"] != "succeeded" {
		t.Fatal("public result query")
	}
	mu.Lock()
	n := effects
	mu.Unlock()
	if n != 1 {
		t.Fatal("receiver effect count")
	}
	if again := sender.RunOne(f.ctx); again.Claimed || again.TransportInvoked {
		t.Fatal("delivered replay")
	}
}

func callbackCompletedCommand(t *testing.T, f *gwFixture) string {
	t.Helper()
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	p.hello(t)
	id, _ := f.submit(t, d, "callback_command")
	cmd := commandBody(p.until(t, "command"))
	start(t, p, cmd)
	event := p.send(t, "execution_result", result(cmd, "OK"))
	assertAck(t, p, event, id)
	return id
}
func callbackPrepared(d cb.Delivery) cb.Prepared {
	return cb.Prepared{Handle: d.Handle, Binding: d.Binding, EventID: d.EventID, Digest: d.Digest, IP: netip.MustParseAddr("93.184.216.34"), Deadline: time.Now().Add(time.Second)}
}
func TestPGCallbackCB02(t *testing.T) {
	gatewayGate(t)
	callbackPrepareEvidenceIntegrationCases(t)
	callbackObservationIntegrationCases(t)
	callbackRetryBoundaryIntegrationCases(t)
	callbackRetryObservationIntegrationCases(t)
	callbackRetryDirectionIntegrationCases(t)
	callbackRetryNaturalLockIntegrationCase(t)
	callbackRetryNaturalReadyObservationIntegrationCase(t)
	callbackReplayReceiptIntegrationCases(t)
	replayReceiptCorruptionCases(t)
	t.Run("same_delivery_single_claim_and_prepare", func(t *testing.T) {
		f := newGWWithStoreConstructor(t, callbackSyntheticStore)
		store, _ := callbackSetup(t, f)
		id := callbackCompletedCommand(t, f)
		type answer struct {
			d  cb.Delivery
			ok bool
			e  error
		}
		ch := make(chan answer, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); d, ok, e := store.Claim(f.ctx); ch <- answer{d, ok, e} }()
		}
		wg.Wait()
		close(ch)
		var won cb.Delivery
		wins := 0
		for a := range ch {
			if a.e != nil {
				t.Fatal("claim failed; redacted")
			}
			if a.ok {
				won = a.d
				wins++
			}
		}
		if wins != 1 {
			t.Fatal("claim duplication")
		}
		p := callbackPrepared(won)
		if permit, e := store.Prepare(f.ctx, p); e != nil || !permit.Fresh {
			t.Fatal("first prepare")
		}
		if _, e := store.Prepare(f.ctx, p); e == nil {
			t.Fatal("prepared twice")
		}
		if e := store.Complete(f.ctx, cb.Completion{Handle: won.Handle, Outcome: "delivered", State: "delivered", Code: "HTTP_ACKNOWLEDGED", Status: 204}); e != nil {
			t.Fatal("completion")
		}
		if e := store.Complete(f.ctx, cb.Completion{Handle: won.Handle, Outcome: "delivered", State: "delivered", Code: "HTTP_ACKNOWLEDGED", Status: 204}); e != nil {
			t.Fatal("idempotent completion")
		}
		if f.count(t, `SELECT count(*) FROM callback_outbox WHERE command=$1 AND state='delivered'`, id) != 1 || f.count(t, `SELECT count(*) FROM callback_attempts`) != 1 {
			t.Fatal("settlement changed graph")
		}
	})
	t.Run("revocation_between_claim_and_prepare", func(t *testing.T) {
		f := newGWWithStoreConstructor(t, callbackSyntheticStore)
		store, _ := callbackSetup(t, f)
		callbackCompletedCommand(t, f)
		d, ok, e := store.Claim(f.ctx)
		if e != nil || !ok {
			t.Fatal("claim")
		}
		execOwner(t, f.owner, f.ctx, `UPDATE developer_apps SET active=false WHERE tenant='tenant_demo' AND id='app_demo'`)
		if _, e = store.Prepare(f.ctx, callbackPrepared(d)); e == nil {
			t.Fatal("revoked prepare")
		}
		if e = store.Complete(f.ctx, cb.Completion{Handle: d.Handle, Outcome: "permanent_failure", State: "cancelled", Code: "AUTHORITY_DENIED"}); e != nil {
			t.Fatal("revoked refusal settlement")
		}
		if f.count(t, `SELECT count(*) FROM callback_attempts WHERE send_prepared_at IS NOT NULL`) != 0 || f.count(t, `SELECT count(*) FROM callback_outbox WHERE state='cancelled'`) != 1 {
			t.Fatal("revocation graph")
		}
	})
}

func TestPGCallbackCB03(t *testing.T) {
	gatewayGate(t)
	callbackRecoveryEvidenceIntegrationCases(t)
	for _, prepared := range []bool{false, true} {
		name := "unprepared"
		if prepared {
			name = "prepared_unknown"
		}
		t.Run(name, func(t *testing.T) {
			f := newGWWithStoreConstructor(t, callbackSyntheticStore)
			store, _ := callbackSetup(t, f)
			callbackCompletedCommand(t, f)
			d, ok, e := store.Claim(f.ctx)
			if e != nil || !ok {
				t.Fatal("claim")
			}
			if prepared {
				if _, e = store.Prepare(f.ctx, callbackPrepared(d)); e != nil {
					t.Fatal("prepare")
				}
			}
			// Controlled DB expiry injection, NOT a wall-clock timeout claim. No HTTP is
			// issued. Capture one timestamp so both exact slot leases match the delivery.
			execOwner(t, f.owner, f.ctx, `WITH n AS (SELECT clock_timestamp()-interval '1 second' AS expired), u AS (UPDATE callback_outbox SET claimed_at=n.expired-interval '1 second',hard_deadline_at=n.expired,lease_until=n.expired FROM n WHERE tenant='tenant_demo' AND delivery_id=$1 RETURNING lease_until) UPDATE scope_slots SET lease_until=u.lease_until FROM u WHERE tenant='tenant_demo' AND delivery_id=$1`, d.Handle.DeliveryID)
			if e = store.RecoverExpired(f.ctx, d.Handle.Tenant, d.Handle.DeliveryID); e != nil {
				t.Fatal("expired recovery")
			}
			expected := "lease_lost"
			if prepared {
				expected = "delivery_unknown"
			}
			if f.count(t, `SELECT count(*) FROM callback_attempts WHERE outcome=$1`, expected) != 1 || f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 0 {
				t.Fatal("recovery evidence")
			}
			execOwner(t, f.owner, f.ctx, `UPDATE callback_outbox SET available_at=clock_timestamp() WHERE tenant='tenant_demo' AND delivery_id=$1 AND state='ready'`, d.Handle.DeliveryID)
			next, ok, e := store.Claim(f.ctx)
			if e != nil || !ok || next.Handle.Fence != d.Handle.Fence+1 || next.Handle.Attempt != 2 {
				t.Fatal("successor claim")
			}
			if e = store.Complete(f.ctx, cb.Completion{Handle: d.Handle, Outcome: "delivered", State: "delivered", Code: "HTTP_ACKNOWLEDGED", Status: 204}); e == nil {
				t.Fatal("stale completion overwrote successor")
			}
			if f.count(t, `SELECT count(*) FROM callback_outbox WHERE state='leased' AND fence=$1`, next.Handle.Fence) != 1 {
				t.Fatal("successor modified")
			}
		})
	}
	callbackWorkerIntegrationCases(t)
}
func TestPGCallbackCB04(t *testing.T) {
	gatewayGate(t)
	f := newGWWithStoreConstructor(t, callbackSyntheticStore)
	_, _ = callbackSetup(t, f)
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	p.hello(t)
	id, _ := f.submit(t, d, "callback_rollback")
	cmd := commandBody(p.until(t, "command"))
	start(t, p, cmd)
	execOwner(t, f.owner, f.ctx, `CREATE FUNCTION fail_callback_result_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='command.result' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SYNTHETIC_RESULT_AUDIT_FAILURE'; END IF; RETURN NEW; END $$; CREATE TRIGGER synthetic_callback_rollback BEFORE INSERT ON audit FOR EACH ROW EXECUTE FUNCTION fail_callback_result_audit()`)
	p.send(t, "execution_result", result(cmd, "OK"))
	readCtx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	_, _, e := p.ws.Read(readCtx)
	cancel()
	if e == nil {
		t.Fatal("failed transaction ACKed")
	}
	waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
	if f.count(t, `SELECT count(*) FROM callback_replay_receipts WHERE command=$1`, id) != 0 || f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND result IS NOT NULL`, id) != 0 || f.count(t, `SELECT count(*) FROM callback_outbox`) != 0 || f.count(t, `SELECT count(*) FROM developer_callbacks`) != 0 || f.count(t, `SELECT count(*) FROM admission_reservations WHERE command=$1 AND state='held'`, id) != 2 || f.count(t, `SELECT count(*) FROM outbox WHERE command=$1 AND kind='result_ack'`, id) != 0 {
		t.Fatal("partial result graph survived rollback")
	}
	execOwner(t, f.owner, f.ctx, `DROP TRIGGER synthetic_callback_rollback ON audit; DROP FUNCTION fail_callback_result_audit()`)
	next := f.dial(t, d, f.session(t, d))
	next.hello(t)
	event := next.send(t, "execution_result", result(cmd, "OK"))
	assertAck(t, next, event, id)
	if f.count(t, `SELECT count(*) FROM callback_replay_receipts WHERE command=$1`, id) != 1 || f.count(t, `SELECT count(*) FROM callback_outbox WHERE command=$1`, id) != 1 || f.count(t, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant' AND tenant='tenant_demo' AND scope_id='tenant_demo'`) != 2 {
		t.Fatal("recovery duplicate quota/graph")
	}
}

func TestPGCallbackCB05(t *testing.T) {
	gatewayGate(t)
	callbackObservationCommitIntegrationCases(t)
	callbackRetryCommitIntegrationCases(t)
	f := newGWWithStoreConstructor(t, callbackSyntheticStore)
	store, policy := callbackSetup(t, f)
	callbackCompletedCommand(t, f)
	key := []byte(randHex(t, 16))
	t.Cleanup(func() { clear(key) })
	var mu sync.Mutex
	calls, effects := 0, 0
	var firstEvent, firstDigest, firstTimestamp string
	receiver := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/result" || r.ContentLength < 1 || r.ContentLength > 4096 {
			w.WriteHeader(400)
			return
		}
		body, e := io.ReadAll(io.LimitReader(r.Body, 4097))
		event, timestamp := r.Header.Get("X-WorkTool-Event-Id"), r.Header.Get("X-WorkTool-Timestamp")
		if e != nil || len(body) > 4096 || !security.Verify(key, event, timestamp, r.Header.Get("X-WorkTool-Signature"), body, time.Now().UnixMilli()) {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			firstEvent, firstDigest, firstTimestamp = event, sum(string(body)), timestamp
			effects++
			h, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(500)
				return
			}
			conn, _, e := h.Hijack()
			if e == nil {
				_ = conn.Close()
			}
			return
		}
		if event != firstEvent || sum(string(body)) != firstDigest || timestamp == firstTimestamp {
			w.WriteHeader(409)
			return
		}
		w.WriteHeader(204)
	}))
	receiver.Config.ReadHeaderTimeout = time.Second
	receiver.Config.ReadTimeout = 2 * time.Second
	receiver.Config.WriteTimeout = 2 * time.Second
	receiver.Start()
	t.Cleanup(receiver.Close)
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sender, e := cb.New(cb.Dependencies{Store: store, Clock: callbackWallClock{}, Gate: callbackLocalGate{}, DNS: callbackFixtureDNS{}, Keys: callbackFixtureKey{policy.Binding, key}, Transport: callbackLoopbackTransport{client, receiver.URL}}, []cb.Policy{policy})
	if e != nil {
		t.Fatal("sender")
	}
	first := sender.RunOne(f.ctx)
	if !first.TransportInvoked || first.Reconcile || first.Outcome != "delivery_unknown" || first.State != "ready" {
		t.Fatal("lost response not retained unknown")
	}
	// Real authoritative retry delay, not a frozen clock or SQL availability edit.
	waitCondition(t, f, func() bool {
		return f.count(t, `SELECT count(*) FROM callback_outbox WHERE state='ready' AND available_at<=clock_timestamp()`) == 1
	})
	second := sender.RunOne(f.ctx)
	if second.Reconcile || second.State != "delivered" {
		t.Fatal("retry settlement")
	}
	mu.Lock()
	n, k := calls, effects
	mu.Unlock()
	if n != 2 || k != 1 || f.count(t, `SELECT count(*) FROM callback_attempts`) != 2 || f.count(t, `SELECT count(*) FROM callback_attempts WHERE outcome='delivery_unknown'`) != 1 {
		t.Fatal("stable identity/receiver dedupe")
	}
}
