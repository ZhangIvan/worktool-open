package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type admissionFixture struct {
	f       *pgFixture
	pool    *pgxpool.Pool
	store   *AdmissionStore
	a       AppAuthority
	request AppTextRequest
	pids    []uint32
}

func newAdmissionFixture(t *testing.T, fanout int) *admissionFixture {
	t.Helper()
	f := integrationFixture(t) // explicit opt-in, distinct provisioned identities
	random, e := freshID("synthetic_")
	if e != nil {
		t.Fatal("entropy unavailable")
	}
	fp := hashText(random)
	mustExec(t, f.owner, f.ctx, `UPDATE developer_apps SET token_hash=$1,scopes='["commands:write"]' WHERE tenant='T1' AND id='X'`, fp)
	mustExec(t, f.owner, f.ctx, `UPDATE devices SET capabilities='["message.send_text"]' WHERE tenant='T1' AND id='A';INSERT INTO developer_bindings VALUES('T1','X','A')`)
	mustExec(t, f.owner, f.ctx, `INSERT INTO scope_limits(scope_kind,tenant,scope_id,max_in_flight,rate_per_sec,burst,tokens,refill_at,virtual_finish,policy_version,enabled,capacity_jobs,capacity_bytes,admission_enabled) VALUES('tenant','T1','T1',1,1,1,1,clock_timestamp(),0,1,true,100,10000000,true)`)
	if fanout < 2 {
		mustExec(t, f.owner, f.ctx, `UPDATE callback_subscription_versions SET retired_at=clock_timestamp() WHERE tenant='T1' AND app='X' AND (destination_id='D2' OR $1=0)`, fanout)
	}
	for i := 3; i <= fanout; i++ {
		d := fmt.Sprintf("D%d", i)
		sid := fmt.Sprintf("S%d", i)
		mustExec(t, f.owner, f.ctx, `INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('T1',$1,'X','synthetic:no-network',1,'opaque:not-installed',1,'active')`, d)
		mustExec(t, f.owner, f.ctx, `INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('T1','X',$1,1,$2,1,1,'command.result','bounded-result-v1',1,65536,4)`, sid, d)
	}
	mustExec(t, f.owner, f.ctx, `INSERT INTO scope_limits(scope_kind,tenant,scope_id,destination_id,max_in_flight,rate_per_sec,burst,tokens,refill_at,virtual_finish,policy_version,enabled,capacity_jobs,capacity_bytes,admission_enabled) SELECT 'destination',tenant,id,id,1,1,1,1,clock_timestamp(),0,1,true,100,10000000,true FROM callback_destinations WHERE tenant='T1' AND app='X'`)
	a, e := AppAuthorityFromAuthenticated("T1", "X", "A", fp, 0, 0)
	if e != nil {
		t.Fatal("authority setup")
	}
	// Use only already-validated store config; no new DSN source or default host.
	cfg, e := pgxpool.ParseConfig("")
	if e != nil {
		t.Fatal("pool config failed")
	}
	cfg.ConnConfig = f.store.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{f.schema}.Sanitize() + ",pg_catalog"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "3000"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["application_name"] = f.schema + "_admit"
	cfg.MaxConns = 2
	cfg.MinConns = 0
	pool, e := pgxpool.NewWithConfig(f.ctx, cfg)
	if e != nil {
		t.Fatal("pool setup failed")
	}
	t.Cleanup(pool.Close)
	c1, e := pool.Acquire(f.ctx)
	if e != nil {
		t.Fatal("pool acquire failed")
	}
	c2, e := pool.Acquire(f.ctx)
	if e != nil {
		c1.Release()
		t.Fatal("pool acquire failed")
	}
	pids := []uint32{c1.Conn().PgConn().PID(), c2.Conn().PgConn().PID()}
	c1.Release()
	c2.Release()
	runner, e := NewRunner(PoolAdapter{pool}, Timeouts{5 * time.Second, 2 * time.Second, 10 * time.Second, 2 * time.Second, time.Second})
	if e != nil {
		t.Fatal(e)
	}
	store, e := NewAdmissionStore(runner, func(context.Context) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	return &admissionFixture{f, pool, store, a, AppTextRequest{"synthetic-key-001", "A", "synthetic_demo", "hello", "synthetic", 203, 30}, pids}
}

type admissionResult struct {
	receipt Accepted
	err     error
}

func (a *admissionFixture) start(r AppTextRequest) <-chan admissionResult {
	ch := make(chan admissionResult, 1)
	go func() { v, e := a.store.AdmitAppText(a.f.ctx, a.a, r); ch <- admissionResult{v, e} }()
	return ch
}
func awaitAdmission(t *testing.T, a *admissionFixture, ch <-chan admissionResult) admissionResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-a.f.ctx.Done():
		t.Fatal("admission did not finish within case deadline")
		return admissionResult{}
	}
}
func holdTenant(t *testing.T, a *admissionFixture) pgx.Tx {
	t.Helper()
	tx, e := a.f.owner.Begin(a.f.ctx)
	if e != nil {
		t.Fatal("hold begin")
	}
	mustExec(t, tx, a.f.ctx, `SELECT 1 FROM scope_limits WHERE scope_kind='tenant' AND tenant='T1' AND scope_id='T1' FOR UPDATE`)
	return tx
}
func waitBlocked(t *testing.T, a *admissionFixture, tx pgx.Tx, n int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		e := tx.QueryRow(a.f.ctx, `SELECT count(DISTINCT pid) FROM pg_locks WHERE pid=ANY($1::bigint[]) AND NOT granted`, []int64{int64(a.pids[0]), int64(a.pids[1])}).Scan(&count)
		if e != nil {
			t.Fatal("lock observation failed")
		}
		if count >= n {
			return
		}
		select {
		case <-ticker.C:
		case <-a.f.ctx.Done():
			t.Fatal("expected lock barrier not observed")
		}
	}
}
func commitHold(t *testing.T, a *admissionFixture, tx pgx.Tx) {
	t.Helper()
	if e := tx.Commit(a.f.ctx); e != nil {
		t.Fatal("hold commit failed")
	}
}
func checkLedger(t *testing.T, a *admissionFixture, accepted, fanout int) {
	t.Helper()
	var jobs, bytes, reservations, messages, outboxes, events, commands, audits int64
	if e := a.f.owner.QueryRow(a.f.ctx, `SELECT charged_jobs,charged_bytes FROM scope_limits WHERE scope_kind='tenant' AND tenant='T1'`).Scan(&jobs, &bytes); e != nil {
		t.Fatal("counter query")
	}
	if jobs != int64(accepted*(1+fanout)) || bytes != int64(accepted*(196608+90112*fanout)) {
		t.Fatalf("tenant accounting mismatch jobs=%d bytes=%d", jobs, bytes)
	}
	for q, target := range map[string]*int64{
		`SELECT count(*) FROM commands WHERE left(id,3)='ad_'`:                                          &commands,
		`SELECT count(*) FROM audit WHERE action='developer.message_accepted' AND left(target,3)='ad_'`: &audits,
		`SELECT count(*) FROM admission_reservations WHERE left(command,3)='ad_'`:                       &reservations,
		`SELECT count(*) FROM developer_messages WHERE left(command,3)='ad_'`:                           &messages,
		`SELECT count(*) FROM outbox WHERE kind='command_ready' AND left(command,3)='ad_'`:              &outboxes,
		`SELECT count(*) FROM command_events WHERE left(command,3)='ad_'`:                               &events,
	} {
		if e := a.f.owner.QueryRow(a.f.ctx, q).Scan(target); e != nil {
			t.Fatal("ledger count")
		}
	}
	if commands != int64(accepted) || audits != int64(accepted) || reservations != jobs || messages != int64(accepted) || outboxes != int64(accepted) || events != int64(accepted*2) {
		t.Fatal("partial or duplicate acceptance graph")
	}
	rows, e := a.f.owner.Query(a.f.ctx, `SELECT status,digest,payload,device,policy,confirm,expires FROM commands WHERE left(id,3)='ad_'`)
	if e != nil {
		t.Fatal("command graph query")
	}
	for rows.Next() {
		var state, digest, payload, device string
		var policy, expires int64
		var confirm bool
		if e = rows.Scan(&state, &digest, &payload, &device, &policy, &confirm, &expires); e != nil {
			rows.Close()
			t.Fatal("command scan")
		}
		var p struct{ TargetID, Text string }
		if e = json.Unmarshal([]byte(payload), &p); e != nil {
			rows.Close()
			t.Fatal("stored payload parse")
		}
		expiry, e := wireTime(expires)
		if e != nil {
			rows.Close()
			t.Fatal("stored expiry")
		}
		canonical, e := commandCanonical(AppTextRequest{DeviceID: device, TargetID: p.TargetID, Text: p.Text}, policy, expiry)
		if e != nil || digest != hashText(canonical) || state != "queued" || confirm {
			rows.Close()
			t.Fatal("persisted command status/digest mismatch")
		}
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		t.Fatal("command rows")
	}
	rows.Close()
	var bad int
	if e := a.f.owner.QueryRow(a.f.ctx, `SELECT count(*) FROM scope_limits l WHERE (l.scope_kind='tenant' AND (l.charged_jobs<>(SELECT count(*) FROM admission_reservations r WHERE r.tenant=l.tenant AND r.state<>'purged') OR l.charged_bytes<>COALESCE((SELECT sum(reserved_bytes) FROM admission_reservations r WHERE r.tenant=l.tenant AND r.state<>'purged'),0))) OR (l.scope_kind='destination' AND (l.charged_jobs<>(SELECT count(*) FROM admission_reservations r WHERE r.tenant=l.tenant AND r.destination_id=l.scope_id AND r.state<>'purged') OR l.charged_bytes<>COALESCE((SELECT sum(reserved_bytes) FROM admission_reservations r WHERE r.tenant=l.tenant AND r.destination_id=l.scope_id AND r.state<>'purged'),0)))`).Scan(&bad); e != nil || bad != 0 {
		t.Fatal("scope/reservation aggregate mismatch")
	}
}
func TestPGAdmissionATC01(t *testing.T) {
	a := newAdmissionFixture(t, 2)
	hold := holdTenant(t, a)
	defer rollbackTest(hold)
	one, two := a.start(a.request), a.start(a.request)
	waitBlocked(t, a, hold, 2)
	commitHold(t, a, hold)
	x, y := awaitAdmission(t, a, one), awaitAdmission(t, a, two)
	if x.err != nil || y.err != nil || x.receipt != y.receipt {
		t.Fatal("same key did not converge")
	}
	checkLedger(t, a, 1, 2)
}
func TestPGAdmissionATC02(t *testing.T) {
	a := newAdmissionFixture(t, 2)
	r := a.request
	r.Text = "different"
	hold := holdTenant(t, a)
	defer rollbackTest(hold)
	one, two := a.start(a.request), a.start(r)
	waitBlocked(t, a, hold, 2)
	commitHold(t, a, hold)
	x, y := awaitAdmission(t, a, one), awaitAdmission(t, a, two)
	if !((x.err == nil && fmt.Sprint(y.err) == "IDEMPOTENCY_CONFLICT") || (y.err == nil && fmt.Sprint(x.err) == "IDEMPOTENCY_CONFLICT")) {
		t.Fatal("changed payload race must have one conflict")
	}
	checkLedger(t, a, 1, 2)
}
func TestPGAdmissionATC03(t *testing.T) {
	a := newAdmissionFixture(t, 2)
	mustExec(t, a.f.owner, a.f.ctx, `UPDATE scope_limits SET capacity_jobs=3,capacity_bytes=376832 WHERE scope_kind='tenant'`)
	r := a.request
	r.IdempotencyKey = "synthetic-key-002"
	hold := holdTenant(t, a)
	defer rollbackTest(hold)
	one, two := a.start(a.request), a.start(r)
	waitBlocked(t, a, hold, 2)
	commitHold(t, a, hold)
	x, y := awaitAdmission(t, a, one), awaitAdmission(t, a, two)
	if !((x.err == nil && fmt.Sprint(y.err) == "CAPACITY_EXHAUSTED") || (y.err == nil && fmt.Sprint(x.err) == "CAPACITY_EXHAUSTED")) {
		t.Fatal("last budget race")
	}
	checkLedger(t, a, 1, 2)
}
func TestPGAdmissionATC04(t *testing.T) {
	a := newAdmissionFixture(t, 2)
	mustExec(t, a.f.owner, a.f.ctx, `UPDATE scope_limits SET capacity_jobs=0,capacity_bytes=0 WHERE scope_kind='destination' AND scope_id='D2'`)
	hold := holdTenant(t, a)
	defer rollbackTest(hold)
	one := a.start(a.request)
	waitBlocked(t, a, hold, 1)
	commitHold(t, a, hold)
	x := awaitAdmission(t, a, one)
	if fmt.Sprint(x.err) != "CAPACITY_EXHAUSTED" {
		t.Fatal("missing scope capacity must reject")
	}
	checkLedger(t, a, 0, 2)
	// A separate connection restores the limit; same key now admits one whole graph.
	mustExec(t, a.f.owner, a.f.ctx, `UPDATE scope_limits SET capacity_jobs=100,capacity_bytes=10000000 WHERE scope_kind='destination' AND scope_id='D2'`)
	// Controlled, test-schema-only late failure: audit is written after counters,
	// commands, reservations, message, events and outbox. This proves rollback of
	// completed writes, not merely rejection before any write.
	mustExec(t, a.f.owner, a.f.ctx, `ALTER TABLE audit ADD CONSTRAINT admission_test_late_fault CHECK(action<>'developer.message_accepted')`)
	failed := awaitAdmission(t, a, a.start(a.request))
	if fmt.Sprint(failed.err) != "INTEGRITY_CONFLICT" {
		t.Fatal("late controlled audit fault not reached")
	}
	checkLedger(t, a, 0, 2)
	mustExec(t, a.f.owner, a.f.ctx, `ALTER TABLE audit DROP CONSTRAINT admission_test_late_fault`)
	x = awaitAdmission(t, a, a.start(a.request))
	if x.err != nil {
		t.Fatal("retry after failed unaccepted request")
	}
	checkLedger(t, a, 1, 2)
}
func requireAdmissionEnvironment(t *testing.T) {
	t.Helper()
	if os.Getenv("WORKTOOL_PGTEST_ALLOW") == "" && os.Getenv("WORKTOOL_PGTEST_OWNER_DSN") == "" && os.Getenv("WORKTOOL_PGTEST_STORE_DSN") == "" {
		t.Skip("NOT_RUN: explicit isolated PG integration environment absent")
	}
}
func TestPGAdmissionATC05(t *testing.T) {
	requireAdmissionEnvironment(t)
	t.Run("configuration_first", func(t *testing.T) {
		a := newAdmissionFixture(t, 0)
		// Configuration writer holds the shared gate while inserting the first active
		// subscription (the zero-row phantom case). Admission must wait and see it whole.
		hold := holdTenant(t, a)
		defer rollbackTest(hold)
		one := a.start(a.request)
		waitBlocked(t, a, hold, 1)
		mustExec(t, hold, a.f.ctx, `INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('T1','X','S1',2,'D1',1,1,'command.result','bounded-result-v1',1,65536,4)`)
		commitHold(t, a, hold)
		x := awaitAdmission(t, a, one)
		if x.err != nil {
			t.Fatal("first subscription race")
		}
		checkLedger(t, a, 1, 1)
		// Reverse ordering: already accepted snapshot remains fixed after a new target.
		hold = holdTenant(t, a)
		mustExec(t, hold, a.f.ctx, `INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('T1','X','S2',2,'D2',1,1,'command.result','bounded-result-v1',1,65536,4)`)
		commitHold(t, a, hold)
		y := awaitAdmission(t, a, a.start(a.request))
		if y.err != nil || y.receipt != x.receipt {
			t.Fatal("historical snapshot expanded")
		}
		checkLedger(t, a, 1, 1)
	})
	t.Run("admission_first_empty_set", func(t *testing.T) {
		a := newAdmissionFixture(t, 0)
		blocker, e := a.f.owner.Begin(a.f.ctx)
		if e != nil {
			t.Fatal("auth barrier begin")
		}
		defer rollbackTest(blocker)
		mustExec(t, blocker, a.f.ctx, `SELECT 1 FROM developer_apps WHERE tenant='T1' AND id='X' FOR UPDATE`)
		one := a.start(a.request)
		waitBlocked(t, a, blocker, 1)
		configured := make(chan error, 1)
		go func() {
			tx, e := a.pool.Begin(a.f.ctx)
			if e != nil {
				configured <- e
				return
			}
			defer rollbackTest(tx)
			_, e = tx.Exec(a.f.ctx, `SELECT 1 FROM scope_limits WHERE scope_kind='tenant' AND tenant='T1' AND scope_id='T1' FOR UPDATE`)
			if e == nil {
				_, e = tx.Exec(a.f.ctx, `INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('T1','X','S1',2,'D1',1,1,'command.result','bounded-result-v1',1,65536,4)`)
			}
			if e == nil {
				e = tx.Commit(a.f.ctx)
			}
			configured <- e
		}()
		waitBlocked(t, a, blocker, 2)
		commitHold(t, a, blocker)
		first := awaitAdmission(t, a, one)
		if first.err != nil {
			t.Fatal("admission-first failed")
		}
		select {
		case e = <-configured:
			if e != nil {
				t.Fatal("configuration writer failed")
			}
		case <-a.f.ctx.Done():
			t.Fatal("configuration writer deadline")
		}
		again := awaitAdmission(t, a, a.start(a.request))
		if again.err != nil || again.receipt != first.receipt {
			t.Fatal("new subscription expanded historical empty set")
		}
		checkLedger(t, a, 1, 0)
	})
}
func TestPGAdmissionATC06(t *testing.T) {
	requireAdmissionEnvironment(t)
	t.Run("revocation_first", func(t *testing.T) {
		a := newAdmissionFixture(t, 0)
		hold := holdTenant(t, a)
		defer rollbackTest(hold)
		one := a.start(a.request)
		waitBlocked(t, a, hold, 1)
		mustExec(t, hold, a.f.ctx, `UPDATE developer_apps SET active=false WHERE id='X'`)
		commitHold(t, a, hold)
		if r := awaitAdmission(t, a, one); fmt.Sprint(r.err) != "AUTHORITY_REVOKED_OR_CHANGED" {
			t.Fatal("revoked app admitted")
		}
		checkLedger(t, a, 0, 0)
	})
	t.Run("acceptance_first_then_revocation", func(t *testing.T) {
		a := newAdmissionFixture(t, 0)
		first := awaitAdmission(t, a, a.start(a.request))
		if first.err != nil {
			t.Fatal("initial admission")
		}
		mustExec(t, a.f.owner, a.f.ctx, `UPDATE developer_apps SET active=false WHERE tenant='T1' AND id='X'`)
		again := awaitAdmission(t, a, a.start(a.request))
		if fmt.Sprint(again.err) != "AUTHORITY_REVOKED_OR_CHANGED" {
			t.Fatal("revoked duplicate bypass")
		}
		checkLedger(t, a, 1, 0)
	})
	for _, tc := range []struct{ name, sql, code string }{
		{"credential_epoch", `UPDATE developer_apps SET auth_epoch=auth_epoch+1 WHERE id='X'`, "AUTHORITY_REVOKED_OR_CHANGED"},
		{"credential_fingerprint", `UPDATE developer_apps SET token_hash=repeat('f',64) WHERE id='X'`, "AUTHORITY_REVOKED_OR_CHANGED"},
		{"app_scope", `UPDATE developer_apps SET scopes='[]' WHERE id='X'`, "AUTHORITY_REVOKED_OR_CHANGED"},
		{"device_epoch", `UPDATE devices SET epoch=epoch+1 WHERE id='A'`, "AUTHORITY_REVOKED_OR_CHANGED"},
		{"device_revoked", `UPDATE devices SET state='revoked' WHERE id='A'`, "AUTHORITY_REVOKED_OR_CHANGED"},
		{"device_capability", `UPDATE devices SET capabilities='[]' WHERE id='A'`, "CAPABILITY_UNAVAILABLE"},
		{"device_binding", `DELETE FROM developer_bindings WHERE tenant='T1' AND app='X' AND device='A'`, "AUTHORITY_REVOKED_OR_CHANGED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdmissionFixture(t, 0)
			hold := holdTenant(t, a)
			defer rollbackTest(hold)
			one := a.start(a.request)
			waitBlocked(t, a, hold, 1)
			mustExec(t, hold, a.f.ctx, tc.sql)
			commitHold(t, a, hold)
			r := awaitAdmission(t, a, one)
			if fmt.Sprint(r.err) != tc.code {
				t.Fatal("current authority gate not enforced")
			}
			checkLedger(t, a, 0, 0)
		})
	}
	for _, duplicate := range []bool{false, true} {
		name := "new_expiry_after_wait"
		if duplicate {
			name = "duplicate_expiry_after_wait"
		}
		t.Run(name, func(t *testing.T) {
			a := newAdmissionFixture(t, 0)
			accepted := 0
			if duplicate {
				if r := awaitAdmission(t, a, a.start(a.request)); r.err != nil {
					t.Fatal("initial accept")
				}
				accepted = 1
			}
			var expiry int64
			if e := a.f.owner.QueryRow(a.f.ctx, `UPDATE developer_apps SET token_expires=floor(extract(epoch FROM clock_timestamp())*1000)::bigint+250 WHERE id='X' RETURNING token_expires`).Scan(&expiry); e != nil {
				t.Fatal("expiry setup")
			}
			hold := holdTenant(t, a)
			defer rollbackTest(hold)
			one := a.start(a.request)
			waitBlocked(t, a, hold, 1)
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var now int64
				if e := hold.QueryRow(a.f.ctx, `SELECT floor(extract(epoch FROM clock_timestamp())*1000)::bigint`).Scan(&now); e != nil {
					t.Fatal("clock query")
				}
				if now >= expiry {
					break
				}
				select {
				case <-ticker.C:
				case <-a.f.ctx.Done():
					t.Fatal("expiry barrier")
				}
			}
			commitHold(t, a, hold)
			if r := awaitAdmission(t, a, one); fmt.Sprint(r.err) != "AUTHORITY_REVOKED_OR_CHANGED" {
				t.Fatal("expired credential passed final decision")
			}
			checkLedger(t, a, accepted, 0)
		})
	}
}
func TestPGAdmissionATC07(t *testing.T) {
	a := newAdmissionFixture(t, 2)
	c1, e := a.pool.Acquire(a.f.ctx)
	if e != nil {
		t.Fatal("first connection acquire")
	}
	c2, e := a.pool.Acquire(a.f.ctx)
	if e != nil {
		c1.Release()
		t.Fatal("second connection acquire")
	}
	defer c2.Release()
	c1.Release() // only C1 available to first admission
	first := awaitAdmission(t, a, a.start(a.request))
	if first.err != nil {
		t.Fatal("first commit")
	}
	held, e := a.pool.Acquire(a.f.ctx)
	if e != nil {
		t.Fatal("hold original connection")
	}
	defer held.Release()
	c2.Release() // only C2 available to reconciliation; Release is idempotent
	second := awaitAdmission(t, a, a.start(a.request))
	if second.err != nil || first.receipt != second.receipt {
		t.Fatal("lost application response reconciliation")
	}
	checkLedger(t, a, 1, 2)
}
func TestPGAdmissionATC08(t *testing.T) {
	requireAdmissionEnvironment(t)
	for _, n := range []int{0, 4, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			a := newAdmissionFixture(t, n)
			x := awaitAdmission(t, a, a.start(a.request))
			if n == 5 {
				if fmt.Sprint(x.err) != "SUBSCRIPTION_POLICY_UNAVAILABLE" {
					t.Fatal("fanout5 must not truncate")
				}
				checkLedger(t, a, 0, 0)
				return
			}
			if x.err != nil {
				t.Fatal("fanout boundary")
			}
			hold := holdTenant(t, a)
			mustExec(t, hold, a.f.ctx, `UPDATE scope_limits SET capacity_jobs=charged_jobs,capacity_bytes=charged_bytes,admission_enabled=false;UPDATE callback_subscription_versions SET retired_at=clock_timestamp() WHERE tenant='T1' AND app='X' AND retired_at IS NULL`)
			commitHold(t, a, hold)
			y := awaitAdmission(t, a, a.start(a.request))
			if y.err != nil || x.receipt != y.receipt {
				t.Fatal("duplicate should ignore new-admission closure")
			}
			checkLedger(t, a, 1, n)
			var snapshot string
			if e := a.f.owner.QueryRow(a.f.ctx, `SELECT subscription_snapshot::text FROM commands WHERE id=$1`, x.receipt.CommandID).Scan(&snapshot); e != nil || n == 0 && strings.TrimSpace(snapshot) != "[]" {
				t.Fatal("snapshot boundary")
			}
		})
	}
}
