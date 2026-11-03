package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/httpapi"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/ZhangIvan/worktool-open/backend-go/migrations"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func gatewayGate(t *testing.T) {
	t.Helper()
	allow, o, s := os.Getenv("WORKTOOL_PGTEST_ALLOW"), os.Getenv("WORKTOOL_PGTEST_OWNER_DSN"), os.Getenv("WORKTOOL_PGTEST_STORE_DSN")
	if allow == "" && o == "" && s == "" {
		t.Skip("NOT_RUN: explicit isolated PG plus approved CI loopback required")
	}
	if allow != "isolated-synthetic-schema-only" || o == "" || s == "" {
		t.Fatal("PGTEST incomplete; no socket or connection attempted")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Fatal("gateway integration restricted to approved CI; no socket or PG attempted")
	}
}
func sum(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		t.Fatal("entropy unavailable")
	}
	return hex.EncodeToString(b)
}
func stateCode(e error) string {
	var p *pgconn.PgError
	if errors.As(e, &p) {
		return p.Code
	}
	return "NON_SQL_ERROR"
}

type gwFixture struct {
	ctx              context.Context
	owner            *pgx.Conn
	pool             *pgxpool.Pool
	store            *pg.GatewayStore
	controller       *Controller
	server           *httptest.Server
	client           *http.Client
	schema, appToken string
	commitFault      *atomic.Bool
}

// commitFault is a deterministic driver-error injection AFTER a real committed
// result transaction, not a claim that a TCP fault proxy was exercised.
type faultPool struct {
	base  pg.Pool
	armed *atomic.Bool
}

func (p faultPool) Acquire(ctx context.Context) (pg.Connection, error) {
	c, e := p.base.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	return faultConnection{Connection: c, armed: p.armed}, nil
}

type faultConnection struct {
	pg.Connection
	armed *atomic.Bool
}

func (c faultConnection) BeginTx(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	tx, e := c.Connection.BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	return &faultTx{Tx: tx, armed: c.armed}, nil
}

type faultTx struct {
	pgx.Tx
	armed  *atomic.Bool
	result bool
}

func (t *faultTx) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(q, "UPDATE commands SET status=$4,effect=$5,result=$6") {
		t.result = true
	}
	return t.Tx.Exec(ctx, q, args...)
}
func (t *faultTx) Commit(ctx context.Context) error {
	e := t.Tx.Commit(ctx)
	if e == nil && t.result && t.armed.CompareAndSwap(true, false) {
		return errors.New("synthetic post-commit outcome uncertainty")
	}
	return e
}
func newGW(t *testing.T) *gwFixture {
	t.Helper()
	gatewayGate(t)
	oc, e := pgx.ParseConfig(os.Getenv("WORKTOOL_PGTEST_OWNER_DSN"))
	if e != nil {
		t.Fatal("owner DSN invalid")
	}
	sc, e := pgx.ParseConfig(os.Getenv("WORKTOOL_PGTEST_STORE_DSN"))
	if e != nil {
		t.Fatal("store DSN invalid")
	}
	valid := func(c *pgx.ConnConfig) bool {
		return strings.HasPrefix(c.Database, "wt_test_") && len(c.Database) > 8 && (c.Host == "127.0.0.1" || strings.HasPrefix(c.Host, "/")) && len(c.Fallbacks) == 0
	}
	if !valid(oc) || !valid(sc) || oc.Host != sc.Host || oc.Port != sc.Port || oc.Database != sc.Database || oc.User == sc.User {
		t.Fatal("explicit same local synthetic DB/distinct roles/no fallbacks required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	schema := "wt_it_" + randHex(t, 12)
	oc.ConnectTimeout = 2 * time.Second
	sc.ConnectTimeout = 2 * time.Second
	for _, c := range []*pgx.ConnConfig{oc, sc} {
		c.RuntimeParams["application_name"] = schema
		c.RuntimeParams["statement_timeout"] = "5000"
		c.RuntimeParams["lock_timeout"] = "2000"
		c.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	}
	owner, e := pgx.ConnectConfig(ctx, oc)
	if e != nil {
		t.Fatal("owner connection failed; redacted")
	}
	qs := pgx.Identifier{schema}.Sanitize()
	storageSafeToClose := true
	t.Cleanup(func() {
		if !storageSafeToClose {
			t.Error("storage cleanup withheld: live gateway; isolated CI must terminate its resource")
			return
		}
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if _, e := owner.Exec(cleanup, "DROP SCHEMA IF EXISTS "+qs+" CASCADE"); e != nil {
			t.Error("synthetic schema cleanup failed")
		}
		_ = owner.Close(cleanup)
	})
	var unsafe, member, owns bool
	e = owner.QueryRow(ctx, `SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls FROM pg_roles WHERE rolname=$1`, sc.User).Scan(&unsafe)
	if e != nil || unsafe {
		t.Fatal("unsafe/absent store role")
	}
	e = owner.QueryRow(ctx, `SELECT pg_has_role($1,$2,'MEMBER')`, sc.User, oc.User).Scan(&member)
	if e != nil || member {
		t.Fatal("store inherits owner")
	}
	e = owner.QueryRow(ctx, `SELECT NOT r.rolsuper AND d.datdba=r.oid FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user`).Scan(&owns)
	if e != nil || !owns {
		t.Fatal("owner must be nonsuper database owner")
	}
	execOwner(t, owner, ctx, "CREATE SCHEMA "+qs)
	execOwner(t, owner, ctx, "REVOKE ALL ON SCHEMA "+qs+" FROM PUBLIC")
	execOwner(t, owner, ctx, "SET search_path TO "+qs+",pg_catalog")
	for _, name := range migrations.Order {
		b, e := migrations.Files.ReadFile(name)
		if e != nil {
			t.Fatal("migration missing")
		}
		tx, e := owner.Begin(ctx)
		if e != nil {
			t.Fatal("begin migration")
		}
		_, e = tx.Exec(ctx, string(b))
		if e == nil {
			e = tx.Commit(ctx)
		} else {
			rollback(ctx, tx)
		}
		if e != nil {
			t.Fatalf("migration %s SQLSTATE=%s", name, stateCode(e))
		}
	}
	qr := pgx.Identifier{sc.User}.Sanitize()
	execOwner(t, owner, ctx, "GRANT USAGE ON SCHEMA "+qs+" TO "+qr)
	execOwner(t, owner, ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA "+qs+" TO "+qr)
	execOwner(t, owner, ctx, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA "+qs+" TO "+qr)
	execOwner(t, owner, ctx, "REVOKE DELETE ON admission_reservations,callback_subscription_versions FROM "+qr)
	pc, e := pgxpool.ParseConfig("")
	if e != nil {
		t.Fatal("pool config")
	}
	pc.ConnConfig = sc.Copy()
	pc.ConnConfig.RuntimeParams["search_path"] = qs + ",pg_catalog"
	pc.MaxConns = 2
	pc.MinConns = 0
	pool, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		t.Fatal("pool")
	}
	t.Cleanup(func() {
		if storageSafeToClose {
			pool.Close()
		}
	})
	var who string
	e = pool.QueryRow(ctx, `SELECT current_user`).Scan(&who)
	if e != nil || who != sc.User {
		t.Fatal("effective store identity")
	}
	token := randHex(t, 32)
	execOwner(t, owner, ctx, `INSERT INTO tenants VALUES('tenant_demo');INSERT INTO users(tenant,id,login,role,password) VALUES('tenant_demo','admin_demo','admin_demo','admin','!disabled'),('tenant_demo','owner_demo','owner_demo','owner','!disabled');INSERT INTO runtime_instances(instance_id,roles,boot_id,recovery_epoch,state) VALUES('gateway_demo',ARRAY['gateway'],'boot_demo','recovery_demo','active');INSERT INTO scope_limits(scope_kind,tenant,scope_id,max_in_flight,rate_per_sec,burst,tokens,policy_version,enabled,capacity_jobs,capacity_bytes,admission_enabled) VALUES('tenant','tenant_demo','tenant_demo',2,8,16,16,1,true,100,20000000,true)`)
	execOwner(t, owner, ctx, `INSERT INTO developer_apps(tenant,id,creator,name,token_hash,token_expires,scopes) VALUES('tenant_demo','app_demo','admin_demo','synthetic',$1,9000000000000,'["commands:write","results:read"]')`, sum(token))
	fault := &atomic.Bool{}
	runner, e := pg.NewRunner(faultPool{base: pg.PoolAdapter{Pool: pool}, armed: fault}, pg.Timeouts{Acquire: 2 * time.Second, Begin: 2 * time.Second, Transaction: 5 * time.Second, Commit: 2 * time.Second, Cleanup: time.Second})
	if e != nil {
		t.Fatal("runner")
	}
	runtime, e := pg.RuntimeAuthorityFromTrusted("gateway_demo", "boot_demo", "recovery_demo")
	if e != nil {
		t.Fatal("runtime")
	}
	store, e := pg.NewGatewayStore(runner, func(context.Context) error { return nil }, runtime)
	if e != nil {
		t.Fatal("store")
	}
	controller, e := New(store)
	if e != nil {
		t.Fatal("controller")
	}
	router, e := httpapi.NewRouter(httpapi.Options{Store: store, WebSocket: controller})
	if e != nil {
		t.Fatal("router")
	}
	// First network listener is reached only after explicit gate and isolated PG checks.
	server := httptest.NewUnstartedServer(router)
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Config.ReadTimeout = 10 * time.Second
	server.Config.WriteTimeout = 10 * time.Second
	server.Start()
	storageSafeToClose = false
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() {
		cleanup, c := context.WithTimeout(context.Background(), 7*time.Second)
		defer c()
		if e := controller.Shutdown(cleanup); e != nil {
			t.Error("gateway shutdown incomplete; pool must remain available until callbacks stop")
		} else {
			storageSafeToClose = true
		}
		server.Close()
		if controller.CleanupErrors() != 0 {
			t.Error("late cleanup DB failures")
		}
	})
	return &gwFixture{ctx, owner, pool, store, controller, server, client, schema, token, fault}
}
func rollback(_ context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func execOwner(t *testing.T, c interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, ctx context.Context, q string, args ...any) {
	t.Helper()
	if _, e := c.Exec(ctx, q, args...); e != nil {
		t.Fatalf("fixture SQLSTATE=%s", stateCode(e))
	}
}
func (f *gwFixture) count(t *testing.T, q string, args ...any) int64 {
	t.Helper()
	var v int64
	if e := f.owner.QueryRow(f.ctx, q, args...).Scan(&v); e != nil {
		t.Fatal("count query")
	}
	return v
}
func (f *gwFixture) request(t *testing.T, method, path, token, key string, body any) (int, map[string]any) {
	t.Helper()
	var data []byte
	if body != nil {
		var e error
		data, e = json.Marshal(body)
		if e != nil {
			t.Fatal("encode fixture")
		}
	}
	req, e := http.NewRequestWithContext(f.ctx, method, f.server.URL+path, strings.NewReader(string(data)))
	if e != nil {
		t.Fatal("request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, e := f.client.Do(req)
	if e != nil {
		t.Fatal("HTTP exchange failed; redacted")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if e != nil || len(raw) > 65536 {
		t.Fatal("response bound")
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		t.Fatal("response not JSON")
	}
	return resp.StatusCode, out
}
func (f *gwFixture) grant(t *testing.T) pg.EnrollmentGrant {
	t.Helper()
	a, e := pg.TrustedAdminFromControl("tenant_demo", "admin_demo")
	if e != nil {
		t.Fatal("admin context")
	}
	g, e := f.store.ProvisionEnrollment(f.ctx, a, pg.EnrollmentRequest{DeviceLabel: "synthetic device", Capabilities: []string{"message.send_text", "file.export"}, ExpiresInSeconds: 300})
	if e != nil {
		t.Fatal("provision", e)
	}
	return g
}
func (f *gwFixture) enroll(t *testing.T) pg.DeviceCredentials {
	t.Helper()
	g := f.grant(t)
	status, v := f.request(t, "POST", "/api/v1/devices/enroll", "", "", map[string]any{"enrollmentId": g.EnrollmentID, "enrollmentToken": g.EnrollmentToken, "deviceLabel": "synthetic", "localConfirmed": true})
	if status != 201 {
		t.Fatalf("enroll status=%d", status)
	}
	d := pg.DeviceCredentials{DeviceID: v["deviceId"].(string), KeyID: v["keyId"].(string), DeviceSecret: v["deviceSecret"].(string), PolicyVersion: 1}
	execOwner(t, f.owner, f.ctx, `INSERT INTO developer_bindings VALUES('tenant_demo','app_demo',$1)`, d.DeviceID)
	return d
}
func (f *gwFixture) session(t *testing.T, d pg.DeviceCredentials) pg.SessionGrant {
	t.Helper()
	status, v := f.request(t, "POST", "/api/v1/device-sessions", d.DeviceSecret, "", map[string]any{"deviceId": d.DeviceID, "keyId": d.KeyID})
	if status != 201 {
		t.Fatalf("session status=%d", status)
	}
	return pg.SessionGrant{SessionID: v["sessionId"].(string), AccessToken: v["accessToken"].(string), ExpiresAt: v["expiresAt"].(string), RevocationEpoch: int64(v["revocationEpoch"].(float64))}
}

type peer struct {
	fixture         *gwFixture
	ws              *websocket.Conn
	device, session string
	seq, received   int64
}

func (f *gwFixture) dial(t *testing.T, d pg.DeviceCredentials, s pg.SessionGrant) *peer {
	t.Helper()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+s.AccessToken)
	ws, _, e := websocket.Dial(f.ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/api/v1/device-sessions", &websocket.DialOptions{HTTPHeader: h, CompressionMode: websocket.CompressionDisabled})
	if e != nil {
		t.Fatal("WS dial failed; redacted")
	}
	ws.SetReadLimit(65536)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return &peer{fixture: f, ws: ws, device: d.DeviceID, session: s.SessionID}
}
func (p *peer) hello(t *testing.T) {
	t.Helper()
	p.writeJSON(t, map[string]any{"kind": "hello", "supportedVersions": []string{"1.0"}, "mode": "synthetic", "capabilities": []string{"message.send_text", "file.export"}})
	v := p.read(t)
	if len(v) != 10 || v["kind"] != "hello_ack" || v["version"] != "1.0" || v["sessionId"] != p.session || v["heartbeatIntervalSec"] != float64(15) || v["maxFrameBytes"] != float64(65536) {
		t.Fatal("hello ack shape")
	}
}
func (p *peer) writeJSON(t *testing.T, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal("encode frame")
	}
	ctx, c := context.WithTimeout(p.fixture.ctx, 3*time.Second)
	defer c()
	if e = p.ws.Write(ctx, websocket.MessageText, b); e != nil {
		t.Fatal("WS write failed; redacted")
	}
}
func (p *peer) read(t *testing.T) map[string]any {
	t.Helper()
	ctx, c := context.WithTimeout(p.fixture.ctx, 7*time.Second)
	defer c()
	kind, b, e := p.ws.Read(ctx)
	if e != nil || kind != websocket.MessageText {
		t.Fatal("WS read failed; redacted")
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		t.Fatal("invalid server JSON")
	}
	if v["kind"] != "hello_ack" {
		n, ok := v["seq"].(float64)
		if !ok || int64(n) != p.received+1 {
			t.Fatal("noncontiguous server sequence")
		}
		p.received = int64(n)
	}
	return v
}
func (p *peer) until(t *testing.T, kind string) map[string]any {
	t.Helper()
	for i := 0; i < 16; i++ {
		v := p.read(t)
		if v["kind"] == kind {
			return v
		}
	}
	t.Fatal("expected kind not received within bound")
	return nil
}
func (p *peer) send(t *testing.T, kind string, body any) string {
	t.Helper()
	p.seq++
	event := "evt_" + randHex(t, 16)
	p.writeJSON(t, map[string]any{"version": "1.0", "kind": kind, "eventId": event, "tenantId": "tenant_demo", "deviceId": p.device, "sessionId": p.session, "seq": p.seq, "sentAt": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), "mode": "synthetic", "body": body})
	return event
}
func (f *gwFixture) submit(t *testing.T, d pg.DeviceCredentials, key string) (string, string) {
	t.Helper()
	status, v := f.request(t, "POST", "/api/developer/v1/messages", f.appToken, key, map[string]any{"type": 203, "deviceId": d.DeviceID, "targetId": "synthetic_demo", "text": "synthetic-only", "expiresInSeconds": 30})
	if status != 202 {
		t.Fatalf("submit status=%d", status)
	}
	children := v["children"].([]any)
	if len(children) != 1 {
		t.Fatal("child count")
	}
	return children[0].(map[string]any)["commandId"].(string), v["messageId"].(string)
}
func commandBody(v map[string]any) map[string]any { return v["body"].(map[string]any) }
func start(t *testing.T, p *peer, cmd map[string]any) {
	t.Helper()
	ref := map[string]any{"commandId": cmd["commandId"], "attemptId": cmd["attemptId"]}
	p.send(t, "accepted", ref)
	p.until(t, "transport_ack")
	p.send(t, "started", ref)
	p.until(t, "transport_ack")
}
func result(cmd map[string]any, code string) map[string]any {
	return map[string]any{"commandId": cmd["commandId"], "attemptId": cmd["attemptId"], "status": "succeeded", "effectState": "applied", "code": code, "evidenceRefs": []string{"journal_" + cmd["commandId"].(string)}}
}
func assertAck(t *testing.T, p *peer, event, command string) {
	t.Helper()
	b := commandBody(p.until(t, "result_ack"))
	if b["ackEventId"] != event || b["commandId"] != command {
		t.Fatal("ACK not bound to current result event")
	}
}
func waitCondition(t *testing.T, f *gwFixture, fn func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("condition deadline")
		case <-f.ctx.Done():
			t.Fatal("fixture deadline")
		}
	}
}

// Durable synthetic fake-device journal: atomic rename across fake process
// recreation. This is not an Android SQLite/power-loss durability claim.
type fakeJournal struct{ path string }

func (j fakeJournal) finish(t *testing.T, cmd map[string]any) map[string]any {
	t.Helper()
	records := map[string]map[string]any{}
	if b, e := os.ReadFile(j.path); e == nil {
		if json.Unmarshal(b, &records) != nil {
			t.Fatal("journal decode")
		}
	}
	id := cmd["commandId"].(string)
	if old, ok := records[id]; ok {
		if old["attemptId"] != cmd["attemptId"] {
			t.Fatal("fake journal attempt conflict")
		}
		return old
	}
	r := result(cmd, "SYNTHETIC_APPLIED")
	records[id] = r
	b, e := json.Marshal(records)
	if e != nil {
		t.Fatal("journal encode")
	}
	if e = os.WriteFile(j.path+".tmp", b, 0600); e != nil {
		t.Fatal("journal write")
	}
	if e = os.Rename(j.path+".tmp", j.path); e != nil {
		t.Fatal("journal commit")
	}
	return r
}
func (j fakeJournal) count(t *testing.T) int {
	t.Helper()
	b, e := os.ReadFile(j.path)
	if e != nil {
		t.Fatal("journal read")
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		t.Fatal("journal decode")
	}
	return len(v)
}

func TestPGGatewayGW01(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	g := f.grant(t)
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _ := f.request(t, "POST", "/api/v1/devices/enroll", "", "", map[string]any{"enrollmentId": g.EnrollmentID, "enrollmentToken": g.EnrollmentToken, "deviceLabel": "synthetic", "localConfirmed": true})
			statuses <- s
		}()
	}
	wg.Wait()
	close(statuses)
	accepted, denied := 0, 0
	for s := range statuses {
		if s == 201 {
			accepted++
		}
		if s == 401 {
			denied++
		}
	}
	if accepted != 1 || denied != 1 || f.count(t, `SELECT count(*) FROM devices`) != 1 {
		t.Fatal("enrollment CAS")
	}
	s, _ := f.request(t, "POST", "/api/v1/devices/enroll", "", "", map[string]any{"enrollmentId": g.EnrollmentID, "enrollmentToken": g.EnrollmentToken, "deviceLabel": "synthetic", "localConfirmed": true})
	if s != 401 {
		t.Fatal("one-time secret replay")
	}
	owner, _ := pg.TrustedAdminFromControl("tenant_demo", "owner_demo")
	if _, e := f.store.ProvisionEnrollment(f.ctx, owner, pg.EnrollmentRequest{DeviceLabel: "synthetic", Capabilities: []string{"message.send_text"}, ExpiresInSeconds: 30}); e == nil || e.Error() != "ROLE_DENIED" {
		t.Fatal("owner inherited admin")
	}
}
func TestPGGatewayGW02(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	d := f.enroll(t)
	s, _ := f.request(t, "POST", "/api/v1/device-sessions", randHex(t, 32), "", map[string]any{"deviceId": d.DeviceID, "keyId": d.KeyID})
	if s != 401 {
		t.Fatal("wrong secret accepted")
	}
	sg := f.session(t, d)
	execOwner(t, f.owner, f.ctx, `UPDATE devices SET epoch=epoch+1 WHERE id=$1`, d.DeviceID)
	h := http.Header{}
	h.Set("Authorization", "Bearer "+sg.AccessToken)
	_, resp, e := websocket.Dial(f.ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/api/v1/device-sessions", &websocket.DialOptions{HTTPHeader: h})
	if e == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatal("old epoch WS accepted")
	}
	execOwner(t, f.owner, f.ctx, `UPDATE devices SET state='revoked' WHERE id=$1`, d.DeviceID)
	s, _ = f.request(t, "POST", "/api/v1/device-sessions", d.DeviceSecret, "", map[string]any{"deviceId": d.DeviceID, "keyId": d.KeyID})
	if s != 401 || f.count(t, `SELECT count(*) FROM device_gateway_leases`) != 0 {
		t.Fatal("revoked device/session lease")
	}
}
func TestPGGatewayGW03(t *testing.T) {
	gatewayGate(t)
	t.Run("legacy_two_caps", func(t *testing.T) {
		f := newGW(t)
		d := f.enroll(t)
		p := f.dial(t, d, f.session(t, d))
		p.hello(t)
		p.send(t, "heartbeat", map[string]any{"policyVersion": 1, "lastReceivedSeq": 0})
		p.until(t, "transport_ack")
		if f.count(t, `SELECT count(*) FROM inbox`) != 0 {
			t.Fatal("heartbeat inflated inbox")
		}
	})
	t.Run("explicit_11_no_fallback", func(t *testing.T) {
		f := newGW(t)
		d := f.enroll(t)
		sg := f.session(t, d)
		p := f.dial(t, d, sg)
		p.writeJSON(t, map[string]any{"kind": "hello", "supportedVersions": []string{"1.1"}, "mode": "synthetic", "capabilities": []string{"message.send_text", "file.export", "fixture.ui.submit_text"}, "drivers": []any{map[string]any{"driverId": "fixture.ui", "driverVersion": 1, "targetPackage": "io.worktool.open.prototype", "targetActivity": "io.worktool.open.prototype.ui.FixtureActivity"}}})
		ctx, c := context.WithTimeout(f.ctx, 3*time.Second)
		defer c()
		_, _, e := p.ws.Read(ctx)
		if e == nil || websocket.CloseStatus(e) != 1008 {
			t.Fatal("1.1 was not explicitly rejected")
		}
		waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
		headers := http.Header{}
		headers.Set("Authorization", "Bearer "+sg.AccessToken)
		_, resp, e := websocket.Dial(f.ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/api/v1/device-sessions", &websocket.DialOptions{HTTPHeader: headers})
		if e == nil || resp == nil || resp.StatusCode != 409 {
			t.Fatal("consumed session reactivated")
		}
	})
}
func TestPGGatewayGW04(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	p.hello(t)
	id, message := f.submit(t, d, "synthetic-happy")
	cmd := commandBody(p.until(t, "command"))
	if cmd["commandId"] != id {
		t.Fatal("wrong command")
	}
	start(t, p, cmd)
	j := fakeJournal{filepath.Join(t.TempDir(), "journal.json")}
	r := j.finish(t, cmd)
	event := p.send(t, "execution_result", r)
	assertAck(t, p, event, id)
	s, v := f.request(t, "GET", "/api/developer/v1/messages/"+message, f.appToken, "", nil)
	if s != 200 || v["children"].([]any)[0].(map[string]any)["status"] != "succeeded" || j.count(t) != 1 {
		t.Fatal("vertical completion")
	}
	if f.count(t, `SELECT count(*) FROM admission_reservations WHERE kind='result' AND state='materialized'`) != 1 || f.count(t, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant'`) != 1 || f.count(t, `SELECT count(*) FROM inbox`) != 3 {
		t.Fatal("result reservation/inbox graph")
	}
}
func TestPGGatewayGW05(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	d := f.enroll(t)
	id, _ := f.submit(t, d, "synthetic-key")
	again, _ := f.submit(t, d, "synthetic-key")
	if again != id || f.count(t, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant'`) != 1 {
		t.Fatal("duplicate charged")
	}
	s, _ := f.request(t, "POST", "/api/developer/v1/messages", f.appToken, "synthetic-key", map[string]any{"type": 203, "deviceId": d.DeviceID, "targetId": "synthetic_demo", "text": "different", "expiresInSeconds": 30})
	if s != 409 {
		t.Fatal("changed idempotency payload")
	}
	hold, e := f.owner.Begin(f.ctx)
	if e != nil {
		t.Fatal("config begin")
	}
	defer rollback(f.ctx, hold)
	execOwner(t, hold, f.ctx, `SELECT 1 FROM scope_limits WHERE scope_kind='tenant' FOR UPDATE`)
	ch := make(chan int, 1)
	go func() {
		status, _ := f.request(t, "POST", "/api/developer/v1/messages", f.appToken, "synthetic-config-race", map[string]any{"type": 203, "deviceId": d.DeviceID, "targetId": "synthetic_demo", "text": "new", "expiresInSeconds": 30})
		ch <- status
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var n int
		if e = hold.QueryRow(f.ctx, `SELECT count(*) FROM pg_locks WHERE NOT granted AND pid IN(SELECT pid FROM pg_stat_activity WHERE application_name=$1)`, f.schema).Scan(&n); e != nil {
			t.Fatal("barrier query")
		}
		if n > 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			t.Fatal("barrier deadline")
		}
	}
	execOwner(t, hold, f.ctx, `INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('tenant_demo','dest_demo','app_demo','synthetic:no-network',1,'opaque_test',1,'active');INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('tenant_demo','app_demo','sub_demo',1,'dest_demo',1,1,'command.result','bounded-result-v1',1,65536,4)`)
	if e = hold.Commit(f.ctx); e != nil {
		t.Fatal("config commit")
	}
	select {
	case s = <-ch:
	case <-f.ctx.Done():
		t.Fatal("request deadline")
	}
	if s != 422 || f.count(t, `SELECT count(*) FROM commands`) != 1 || f.count(t, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant'`) != 1 {
		t.Fatal("nonzero fanout accepted/charged")
	}
	again, _ = f.submit(t, d, "synthetic-key")
	if again != id {
		t.Fatal("historical empty snapshot expanded")
	}
}
func TestPGGatewayGW06(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	p.hello(t)
	id, _ := f.submit(t, d, "synthetic-disconnect")
	cmd := commandBody(p.until(t, "command"))
	start(t, p, cmd)
	path := filepath.Join(t.TempDir(), "journal.json")
	j := fakeJournal{path}
	r := j.finish(t, cmd)
	_ = p.ws.CloseNow()
	waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
	if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND status='execution_unknown'`, id) != 1 {
		t.Fatal("disconnect not unknown")
	}
	restarted := fakeJournal{path}
	r = restarted.finish(t, cmd)
	p2 := f.dial(t, d, f.session(t, d))
	p2.hello(t)
	event := p2.send(t, "execution_result", r)
	assertAck(t, p2, event, id)
	if restarted.count(t) != 1 || f.count(t, `SELECT count(*) FROM command_events WHERE command=$1 AND event='dispatch'`, id) != 1 {
		t.Fatal("effect/attempt replay")
	}
}
func TestPGGatewayGW07(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	d := f.enroll(t)
	p := f.dial(t, d, f.session(t, d))
	p.hello(t)
	id, _ := f.submit(t, d, "synthetic-ack-drop")
	cmd := commandBody(p.until(t, "command"))
	start(t, p, cmd)
	r := result(cmd, "SYNTHETIC_APPLIED")
	p.send(t, "execution_result", r)
	waitCondition(t, f, func() bool {
		return f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND result IS NOT NULL`, id) == 1
	})
	_ = p.ws.CloseNow()
	waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
	for i := 0; i < 4; i++ {
		next := f.dial(t, d, f.session(t, d))
		next.hello(t)
		event := next.send(t, "execution_result", r)
		assertAck(t, next, event, id)
		_ = next.ws.CloseNow()
		waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
	}
	if f.count(t, `SELECT count(*) FROM inbox`) != 3 || f.count(t, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant'`) != 1 {
		t.Fatal("result retry grew ledger")
	}
	next := f.dial(t, d, f.session(t, d))
	next.hello(t)
	changed := result(cmd, "CONFLICT_CHANGED")
	next.send(t, "execution_result", changed)
	ctx, c := context.WithTimeout(f.ctx, 3*time.Second)
	defer c()
	if _, _, e := next.ws.Read(ctx); e == nil {
		t.Fatal("conflicting result ACKed")
	}
	waitCondition(t, f, func() bool {
		return f.count(t, `SELECT count(*) FROM audit WHERE action='command.result_conflict' AND target=$1`, id) == 1
	})
	if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND result LIKE '%CONFLICT_CHANGED%'`, id) != 0 {
		t.Fatal("result overwritten")
	}
}
func TestPGGatewayGW08(t *testing.T) {
	gatewayGate(t)
	t.Run("private_consumed_nonce_cas", privateNonceCAS)
	t.Run("private_late_command_lock_expiry", privateAcquireExpiry)
	f := newGW(t)
	d := f.enroll(t)
	old := f.dial(t, d, f.session(t, d))
	old.hello(t)
	id, _ := f.submit(t, d, "synthetic-replaced")
	cmd := commandBody(old.until(t, "command"))
	start(t, old, cmd)
	// Keep the old peer alive/not reading the close while the replacement upgrades.
	next := f.dial(t, d, f.session(t, d))
	next.hello(t)
	event := next.send(t, "execution_result", result(cmd, "SYNTHETIC_APPLIED"))
	assertAck(t, next, event, id)
	_ = old.ws.CloseNow()
	waitCondition(t, f, func() bool { return f.controller.LiveCount() == 1 })
	id2, _ := f.submit(t, d, "synthetic-after-replaced")
	cmd2 := commandBody(next.until(t, "command"))
	if cmd2["commandId"] != id2 || cmd2["attemptId"] == cmd["attemptId"] {
		t.Fatal("replacement wrong attempt")
	}
	if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND status='delivered' AND dispatch_session=$2`, id2, next.session) != 1 {
		t.Fatal("old close damaged new attempt")
	}
}
func TestPGGatewayGW09(t *testing.T) {
	gatewayGate(t)
	for _, change := range []string{"app_revoked", "token_expired", "device_policy"} {
		t.Run(change, func(t *testing.T) {
			f := newGW(t)
			d := f.enroll(t)
			p := f.dial(t, d, f.session(t, d))
			p.hello(t)
			id, _ := f.submit(t, d, "synthetic-revoked-evidence")
			cmd := commandBody(p.until(t, "command"))
			start(t, p, cmd)
			switch change {
			case "app_revoked":
				execOwner(t, f.owner, f.ctx, `UPDATE developer_apps SET active=false WHERE id='app_demo'`)
			case "token_expired":
				execOwner(t, f.owner, f.ctx, `UPDATE developer_apps SET token_expires=1 WHERE id='app_demo'`)
			case "device_policy":
				execOwner(t, f.owner, f.ctx, `UPDATE devices SET policy=policy+1 WHERE id=$1`, d.DeviceID)
			}
			event := p.send(t, "execution_result", result(cmd, "SYNTHETIC_APPLIED"))
			assertAck(t, p, event, id)
			if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND status='succeeded'`, id) != 1 {
				t.Fatal("revoked app lost evidence")
			}
		})
	}
	t.Run("device_revoked_no_ack", func(t *testing.T) {
		f := newGW(t)
		d := f.enroll(t)
		p := f.dial(t, d, f.session(t, d))
		p.hello(t)
		id, _ := f.submit(t, d, "synthetic-device-revoked")
		cmd := commandBody(p.until(t, "command"))
		start(t, p, cmd)
		execOwner(t, f.owner, f.ctx, `UPDATE devices SET state='revoked' WHERE id=$1`, d.DeviceID)
		p.send(t, "execution_result", result(cmd, "SYNTHETIC_APPLIED"))
		ctx, c := context.WithTimeout(f.ctx, 3*time.Second)
		defer c()
		if _, _, e := p.ws.Read(ctx); e == nil {
			t.Fatal("revoked device ACKed")
		}
		if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND result IS NOT NULL`, id) != 0 {
			t.Fatal("untrusted result persisted")
		}
	})
}
func TestPGGatewayGW10(t *testing.T) {
	gatewayGate(t)
	migrationResultGuardCases(t)
	for _, invalid := range []string{"cross_tenant", "wrong_session", "sequence_gap", "stale_timestamp"} {
		t.Run(invalid, func(t *testing.T) {
			f := newGW(t)
			d := f.enroll(t)
			p := f.dial(t, d, f.session(t, d))
			p.hello(t)
			v := map[string]any{"version": "1.0", "kind": "heartbeat", "eventId": "evt_synthetic", "tenantId": "tenant_demo", "deviceId": p.device, "sessionId": p.session, "seq": 1, "sentAt": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), "mode": "synthetic", "body": map[string]any{"policyVersion": 1, "lastReceivedSeq": 0}}
			switch invalid {
			case "cross_tenant":
				v["tenantId"] = "tenant_other"
			case "wrong_session":
				v["sessionId"] = "session_other"
			case "sequence_gap":
				v["seq"] = 2
			case "stale_timestamp":
				v["sentAt"] = time.Now().Add(-time.Minute).UTC().Truncate(time.Second).Format(time.RFC3339)
			}
			p.writeJSON(t, v)
			ctx, c := context.WithTimeout(f.ctx, 3*time.Second)
			defer c()
			if _, _, e := p.ws.Read(ctx); e == nil {
				t.Fatal("invalid frame ACKed")
			}
			if f.count(t, `SELECT count(*) FROM inbox`) != 0 {
				t.Fatal("invalid frame persisted")
			}
		})
	}
	t.Run("nul_result_and_wrong_attempt", func(t *testing.T) {
		f := newGW(t)
		d := f.enroll(t)
		p := f.dial(t, d, f.session(t, d))
		p.hello(t)
		id, _ := f.submit(t, d, "synthetic-nul")
		cmd := commandBody(p.until(t, "command"))
		start(t, p, cmd)
		event := p.send(t, "execution_result", result(cmd, "SYNTHETIC\x00😀"))
		assertAck(t, p, event, id)
		var raw string
		if e := f.owner.QueryRow(f.ctx, `SELECT result FROM commands WHERE id=$1`, id).Scan(&raw); e != nil || !strings.Contains(raw, `\u0000`) || strings.ContainsRune(raw, 0) {
			t.Fatal("NUL result bytes not preserved")
		}
		bad := result(cmd, "SYNTHETIC\x00😀")
		bad["attemptId"] = "attempt_wrong"
		p.send(t, "execution_result", bad)
		ctx, c := context.WithTimeout(f.ctx, 3*time.Second)
		defer c()
		if _, _, e := p.ws.Read(ctx); e == nil {
			t.Fatal("wrong attempt ACKed")
		}
	})
	t.Run("rate_bound", func(t *testing.T) {
		f := newGW(t)
		d := f.enroll(t)
		p := f.dial(t, d, f.session(t, d))
		p.hello(t)
		for i := 0; i < 24; i++ {
			p.seq++
			v := map[string]any{"version": "1.0", "kind": "heartbeat", "eventId": "evt_" + randHex(t, 16), "tenantId": "tenant_demo", "deviceId": p.device, "sessionId": p.session, "seq": p.seq, "sentAt": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), "mode": "synthetic", "body": map[string]any{"policyVersion": 1, "lastReceivedSeq": 0}}
			b, _ := json.Marshal(v)
			if e := p.ws.Write(f.ctx, websocket.MessageText, b); e != nil {
				break
			}
		}
		waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
		if f.count(t, `SELECT count(*) FROM inbox`) != 0 {
			t.Fatal("flood grew ledger")
		}
	})
}
func TestPGGatewayGW11(t *testing.T) {
	gatewayGate(t)
	for _, fault := range []string{"late_audit", "commit_unknown"} {
		t.Run(fault, func(t *testing.T) {
			f := newGW(t)
			d := f.enroll(t)
			p := f.dial(t, d, f.session(t, d))
			p.hello(t)
			id, _ := f.submit(t, d, "synthetic-result-fault")
			cmd := commandBody(p.until(t, "command"))
			start(t, p, cmd)
			if fault == "late_audit" {
				execOwner(t, f.owner, f.ctx, `ALTER TABLE audit ADD CONSTRAINT synthetic_result_failure CHECK(action<>'command.result')`)
			} else {
				f.commitFault.Store(true)
			}
			r := result(cmd, "SYNTHETIC_APPLIED")
			p.send(t, "execution_result", r)
			ctx, c := context.WithTimeout(f.ctx, 3*time.Second)
			defer c()
			if _, _, e := p.ws.Read(ctx); e == nil {
				t.Fatal("uncertain/failed commit ACKed")
			}
			waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
			if fault == "late_audit" {
				if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND result IS NOT NULL`, id) != 0 || f.count(t, `SELECT count(*) FROM admission_reservations WHERE state='materialized'`) != 0 || f.count(t, `SELECT count(*) FROM outbox WHERE kind='result_ack'`) != 0 {
					t.Fatal("late failure partial graph")
				}
				execOwner(t, f.owner, f.ctx, `ALTER TABLE audit DROP CONSTRAINT synthetic_result_failure`)
			} else if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND result IS NOT NULL`, id) != 1 {
				t.Fatal("postcommit injection did not commit")
			}
			next := f.dial(t, d, f.session(t, d))
			next.hello(t)
			event := next.send(t, "execution_result", r)
			assertAck(t, next, event, id)
			if f.count(t, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant'`) != 1 {
				t.Fatal("fault retry charged")
			}
		})
	}
}
func TestPGGatewayGW12(t *testing.T) {
	gatewayGate(t)
	f := newGW(t)
	d := f.enroll(t)
	old := f.dial(t, d, f.session(t, d))
	old.hello(t)
	id, _ := f.submit(t, d, "synthetic-shutdown")
	cmd := commandBody(old.until(t, "command"))
	start(t, old, cmd)
	next := f.dial(t, d, f.session(t, d))
	next.hello(t)
	ctx, cancel := context.WithTimeout(f.ctx, 6*time.Second)
	defer cancel()
	if e := f.controller.Shutdown(ctx); e != nil {
		t.Fatal("shutdown did not join all sockets")
	}
	if f.controller.LiveCount() != 0 || f.controller.CleanupErrors() != 0 {
		t.Fatal("late close/pool lifecycle")
	}
	if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND status='execution_unknown' AND attempt=$2`, id, cmd["attemptId"]) != 1 {
		t.Fatal("shutdown replayed/lost attempt")
	}
	if f.count(t, `SELECT count(*) FROM device_sessions WHERE ws_consumed`) != 2 {
		t.Fatal("consumed sessions reset")
	}
}

// Capture both exact store backends before taking the observer transaction.
// pg_stat_activity can cache a transaction snapshot before a lazy second pool
// connection exists; the lock barrier must observe the actual contender PIDs.
func storeBackendPIDs(t *testing.T, f *gwFixture) []int64 {
	t.Helper()
	first, e := f.pool.Acquire(f.ctx)
	if e != nil {
		t.Fatal("first barrier connection")
	}
	defer first.Release()
	second, e := f.pool.Acquire(f.ctx)
	if e != nil {
		t.Fatal("second barrier connection")
	}
	defer second.Release()
	pids := []int64{int64(first.Conn().PgConn().PID()), int64(second.Conn().PgConn().PID())}
	if pids[0] == pids[1] {
		t.Fatal("barrier requires distinct backends")
	}
	return pids
}
func waitBlockedPG(t *testing.T, f *gwFixture, tx pgx.Tx, pids []int64, n int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var v int
		e := tx.QueryRow(f.ctx, `SELECT count(DISTINCT pid) FROM pg_locks WHERE NOT granted AND pid=ANY($1::bigint[])`, pids).Scan(&v)
		if e != nil {
			t.Fatal("blocked lock query")
		}
		if v >= n {
			return
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			t.Fatal("lock barrier deadline")
		}
	}
}
func consumeFixture(t *testing.T, f *gwFixture, d pg.DeviceCredentials, nonce string) (pg.SessionAuth, pg.SessionGrant) {
	t.Helper()
	sg := f.session(t, d)
	a, e := f.store.AuthenticateSession(f.ctx, sg.AccessToken)
	if e != nil {
		t.Fatal("authenticate fixture session")
	}
	if _, e = f.store.AcquireConnection(f.ctx, a); e == nil {
		t.Fatal("unconsumed authority acquired lease")
	}
	a, e = f.store.ConsumeWebSocketSession(f.ctx, a, nonce)
	if e != nil {
		t.Fatal("consume fixture session")
	}
	return a, sg
}
func privateNonceCAS(t *testing.T) {
	f := newGW(t)
	d := f.enroll(t)
	a, sg := consumeFixture(t, f, d, "conn_same")
	pids := storeBackendPIDs(t, f)
	hold, e := f.owner.Begin(f.ctx)
	if e != nil {
		t.Fatal("gate begin")
	}
	defer rollback(f.ctx, hold)
	execOwner(t, hold, f.ctx, `SELECT 1 FROM scope_limits WHERE scope_kind='tenant' FOR UPDATE`)
	type acquired struct {
		h pg.LeaseHandle
		e error
	}
	ch := make(chan acquired, 2)
	for i := 0; i < 2; i++ {
		go func() { h, e := f.store.AcquireConnection(f.ctx, a); ch <- acquired{h, e} }()
	}
	waitBlockedPG(t, f, hold, pids, 2)
	if e = hold.Commit(f.ctx); e != nil {
		t.Fatal("gate commit")
	}
	first, second := <-ch, <-ch
	if first.e != nil || second.e != nil || first.h.Fence != 1 || second.h.Fence != 1 || !first.h.LeaseUntil.Equal(second.h.LeaseUntil) {
		t.Fatal("same nonce changed lease or deadline")
	}
	if f.count(t, `SELECT count(*) FROM audit WHERE action='gateway.lease_acquired'`) != 1 {
		t.Fatal("same nonce duplicate audit")
	}
	auth, e := f.store.AuthenticateSession(f.ctx, sg.AccessToken)
	if e != nil {
		t.Fatal("session lookup")
	}
	if _, e = f.store.ConsumeWebSocketSession(f.ctx, auth, "conn_other"); e == nil || e.Error() != "SESSION_ALREADY_USED" {
		t.Fatal("different nonce consumed same session")
	}
	next, _ := consumeFixture(t, f, d, "conn_new")
	nextHandle, e := f.store.AcquireConnection(f.ctx, next)
	if e != nil || nextHandle.Fence != 2 {
		t.Fatal("new authorized session takeover")
	}
	if _, e = f.store.AcquireConnection(f.ctx, a); e == nil {
		t.Fatal("revoked old session reacquired")
	}
	oldClose, e := f.store.CloseConnection(f.ctx, a, first.h)
	if e != nil || oldClose.Changed {
		t.Fatal("old exact close changed new lease")
	}
	if f.count(t, `SELECT count(*) FROM device_gateway_leases WHERE fence=2 AND connection_id='conn_new'`) != 1 {
		t.Fatal("old close reset fence")
	}
	// Old exact close before the successor acquires must not make its saved
	// expected fence stale: the private consumed-nonce path reads under lock.
	third, _ := consumeFixture(t, f, d, "conn_third")
	closed, e := f.store.CloseConnection(f.ctx, next, nextHandle)
	if e != nil || !closed.Changed {
		t.Fatal("exact old close before takeover")
	}
	thirdHandle, e := f.store.AcquireConnection(f.ctx, third)
	if e != nil || thirdHandle.Fence != 4 {
		t.Fatal("close-before takeover fence")
	}
	closed, e = f.store.CloseConnection(f.ctx, third, thirdHandle)
	if e != nil || !closed.Changed {
		t.Fatal("exact close")
	}
	beforeRetired := f.count(t, `SELECT count(*) FROM audit`)
	if _, e = f.store.AcquireConnection(f.ctx, third); e == nil || e.Error() != "STALE_LEASE" {
		t.Fatal("same consumed nonce revived retired lease")
	}
	if f.count(t, `SELECT count(*) FROM audit`) != beforeRetired || f.count(t, `SELECT count(*) FROM device_gateway_leases WHERE fence=5 AND state='retired'`) != 1 {
		t.Fatal("retired retry changed graph")
	}
	tx, e := f.pool.Begin(f.ctx)
	if e != nil {
		t.Fatal("guard tx")
	}
	_, e = tx.Exec(f.ctx, `UPDATE device_sessions SET ws_connection_id='conn_spoofed' WHERE id=$1`, sg.SessionID)
	rollback(f.ctx, tx)
	if stateCode(e) != "23514" {
		t.Fatal("consumed nonce rewrite not rejected")
	}
}
func privateAcquireExpiry(t *testing.T) {
	f := newGW(t)
	d := f.enroll(t)
	a, _ := consumeFixture(t, f, d, "conn_old")
	h, e := f.store.AcquireConnection(f.ctx, a)
	if e != nil {
		t.Fatal("old acquire")
	}
	p, e := f.store.AuthenticateApp(f.ctx, f.appToken, "commands:write")
	if e != nil {
		t.Fatal("app auth")
	}
	accepted, e := f.store.AdmitText(f.ctx, p, pg.AppTextRequest{IdempotencyKey: "synthetic-private-expiry", DeviceID: d.DeviceID, TargetID: "synthetic_demo", Text: "synthetic", Mode: "synthetic", Type: 203, ExpiresInSeconds: 30})
	if e != nil {
		t.Fatal("admit")
	}
	claim, e := f.store.Claim(f.ctx, a, h, pg.ClaimCommandRequest{CommandID: accepted.CommandID, ExpectedCommandVersion: 0})
	if e != nil || claim.Intent == nil {
		t.Fatal("claim")
	}
	next, sg := consumeFixture(t, f, d, "conn_new")
	before := f.count(t, `SELECT count(*) FROM audit`)
	pids := storeBackendPIDs(t, f)
	hold, e := f.owner.Begin(f.ctx)
	if e != nil {
		t.Fatal("command hold")
	}
	defer rollback(f.ctx, hold)
	execOwner(t, hold, f.ctx, `SELECT 1 FROM commands WHERE id=$1 FOR UPDATE`, accepted.CommandID)
	var cutoff time.Time
	e = f.pool.QueryRow(f.ctx, `UPDATE device_sessions SET expires=floor(extract(epoch FROM clock_timestamp()+interval '1 second')*1000)::bigint WHERE id=$1 RETURNING to_timestamp(expires::double precision/1000)`, sg.SessionID).Scan(&cutoff)
	if e != nil {
		t.Fatal("cutoff update")
	}
	ch := make(chan error, 1)
	go func() { _, e := f.store.AcquireConnection(f.ctx, next); ch <- e }()
	waitBlockedPG(t, f, hold, pids, 1)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var passed bool
		if e = hold.QueryRow(f.ctx, `SELECT clock_timestamp()>=$1`, cutoff).Scan(&passed); e != nil {
			t.Fatal("clock observation")
		}
		if passed {
			break
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			t.Fatal("cutoff deadline")
		}
	}
	if e = hold.Commit(f.ctx); e != nil {
		t.Fatal("release command lock")
	}
	e = <-ch
	if e == nil || e.Error() != "SESSION_NOT_CURRENT" {
		t.Fatal("private takeover returned expired success")
	}
	if f.count(t, `SELECT count(*) FROM commands WHERE id=$1 AND status='delivered' AND command_version=1 AND attempt=$2`, accepted.CommandID, claim.Intent.AttemptID) != 1 || f.count(t, `SELECT count(*) FROM audit`) != before || f.count(t, `SELECT count(*) FROM outbox WHERE kind='command_state_changed'`) != 0 || f.count(t, `SELECT count(*) FROM device_gateway_leases WHERE fence=1 AND connection_id='conn_old'`) != 1 {
		t.Fatal("late expiry partially retired graph")
	}
}
