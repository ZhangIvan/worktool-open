package pg

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type leaseFixture struct {
	*admissionFixture
	ls     *LeaseStore
	ga     GatewayAuthority
	worker RuntimeAuthority
}

func newLeaseFixture(t *testing.T) *leaseFixture {
	t.Helper()
	a := newAdmissionFixture(t, 0)
	fp := hashText(a.f.schema + "synthetic_session_fingerprint")
	mustExec(t, a.f.owner, a.f.ctx, `UPDATE device_sessions SET token=$1 WHERE id='sessionA';`, fp)
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO runtime_instances(instance_id,roles,boot_id,recovery_epoch,state) VALUES('workerA',ARRAY['worker'],'workerBoot','epoch1','active')`)
	r, e := RuntimeAuthorityFromTrusted("instanceA", "bootA", "epoch1")
	if e != nil {
		t.Fatal(e)
	}
	ga, e := GatewayAuthorityFromAuthenticated(r, "T1", "A", "sessionA", fp, 0)
	if e != nil {
		t.Fatal(e)
	}
	w, e := RuntimeAuthorityFromTrusted("workerA", "workerBoot", "epoch1")
	if e != nil {
		t.Fatal(e)
	}
	ls, e := NewLeaseStore(a.store.runner, func(context.Context) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	return &leaseFixture{a, ls, ga, w}
}
func (f *leaseFixture) acquire(t *testing.T, conn string, fence int64) LeaseHandle {
	t.Helper()
	h, e := f.ls.AcquireDeviceLease(f.f.ctx, f.ga, AcquireLeaseRequest{conn, fence})
	if e != nil {
		t.Fatal("acquire", e)
	}
	return h
}
func (f *leaseFixture) admit(t *testing.T, key string) Accepted {
	t.Helper()
	r := f.request
	r.IdempotencyKey = key
	v, e := f.store.AdmitAppText(f.f.ctx, f.a, r)
	if e != nil {
		t.Fatal("admit", e)
	}
	return v
}
func (f *leaseFixture) claim(t *testing.T, h LeaseHandle, id string) ClaimedIntent {
	t.Helper()
	v, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{id, 0})
	if e != nil || v.Intent == nil {
		t.Fatalf("claim err=%v reason=%s", e, v.Reason)
	}
	return *v.Intent
}
func (f *leaseFixture) expire(t *testing.T) {
	t.Helper()
	mustExec(t, f.f.owner, f.f.ctx, `UPDATE device_gateway_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant='T1' AND device='A'`)
}
func (f *leaseFixture) rotate(t *testing.T) {
	t.Helper()
	mustExec(t, f.f.owner, f.f.ctx, `UPDATE device_sessions SET revoked=true WHERE id='sessionA'`)
	mustExec(t, f.f.owner, f.f.ctx, `INSERT INTO device_sessions(id,tenant,device,token,epoch,expires) VALUES('sessionNew','T1','A',$1,0,9000000000000)`, hashText("synthetic-new-"+f.f.schema))
	f.ga.session = "sessionNew"
	f.ga.fingerprint = hashText("synthetic-new-" + f.f.schema)
}

type leaseResult struct {
	h LeaseHandle
	e error
}
type claimResult struct {
	o ClaimOutcome
	e error
}

func awaitLease(t *testing.T, f *leaseFixture, ch <-chan leaseResult) leaseResult {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-f.f.ctx.Done():
		t.Fatal("lease deadline")
		return leaseResult{}
	}
}
func awaitClaim(t *testing.T, f *leaseFixture, ch <-chan claimResult) claimResult {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-f.f.ctx.Done():
		t.Fatal("claim deadline")
		return claimResult{}
	}
}
func commandState(t *testing.T, f *leaseFixture, id string) (string, string, int64, int) {
	t.Helper()
	var status string
	var attempt *string
	var version int64
	var events int
	e := f.f.owner.QueryRow(f.f.ctx, `SELECT status,attempt,command_version,(SELECT count(*) FROM command_events e WHERE e.tenant=c.tenant AND e.command=c.id) FROM commands c WHERE tenant='T1' AND id=$1`, id).Scan(&status, &attempt, &version, &events)
	if e != nil {
		t.Fatal("state query")
	}
	a := ""
	if attempt != nil {
		a = *attempt
	}
	return status, a, version, events
}
func assertCommand(t *testing.T, f *leaseFixture, id, status, attempt string, version int64, events int) {
	t.Helper()
	s, a, v, n := commandState(t, f, id)
	if s != status || a != attempt || v != version || n != events {
		t.Fatalf("command graph mismatch status=%s version=%d events=%d", s, v, n)
	}
}
func countInt(t *testing.T, f *leaseFixture, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if e := f.f.owner.QueryRow(f.f.ctx, q, args...).Scan(&n); e != nil {
		t.Fatal("count query")
	}
	return n
}

func TestPGLeaseLF01(t *testing.T) {
	requireAdmissionEnvironment(t)
	f := newLeaseFixture(t)
	hold := holdTenant(t, f.admissionFixture)
	defer hold.Rollback(f.f.ctx)
	ch := make(chan leaseResult, 2)
	for _, conn := range []string{"conn1", "conn2"} {
		go func(c string) {
			h, e := f.ls.AcquireDeviceLease(f.f.ctx, f.ga, AcquireLeaseRequest{c, 0})
			ch <- leaseResult{h, e}
		}(conn)
	}
	waitBlocked(t, f.admissionFixture, hold, 2)
	commitHold(t, f.admissionFixture, hold)
	a, b := awaitLease(t, f, ch), awaitLease(t, f, ch)
	if a.e != nil {
		a, b = b, a
	}
	if a.e != nil || b.e == nil || a.h.Fence != 1 {
		t.Fatal("ownership race did not serialize")
	}
	again, e := f.ls.AcquireDeviceLease(f.f.ctx, f.ga, AcquireLeaseRequest{a.h.ConnectionID, 0})
	if e != nil || !sameHandle(a.h, again) || !a.h.LeaseUntil.Equal(again.LeaseUntil) {
		t.Fatal("nonce retry changed lease")
	}
	if n := countInt(t, f, `SELECT count(*) FROM audit WHERE action='gateway.lease_acquired'`); n != 1 {
		t.Fatal("duplicate acquire audit")
	}
	if n := countInt(t, f, `SELECT count(*) FROM device_sessions WHERE gateway_fence=1 AND gateway_connection=$1`, a.h.ConnectionID); n != 1 {
		t.Fatal("snapshot missing")
	}
}
func TestPGLeaseLF02(t *testing.T) {
	requireAdmissionEnvironment(t)
	t.Run("takeover_command_wait_crosses_session_expiry", leaseTakeoverExpiry)
	for _, terminal := range []bool{true, false} {
		t.Run(fmt.Sprint("terminal_", terminal), func(t *testing.T) {
			f := newLeaseFixture(t)
			a := f.acquire(t, "connA", 0)
			first := f.admit(t, "synthetic-first")
			ci := f.claim(t, a, first.CommandID)
			if terminal {
				mustExec(t, f.f.owner, f.f.ctx, `UPDATE commands SET status='succeeded',result='{"status":"succeeded"}' WHERE id=$1`, first.CommandID)
			}
			f.rotate(t)
			b := f.acquire(t, "connB", 1)
			if b.Fence != 2 || b.SessionID == a.SessionID {
				t.Fatal("takeover identity")
			}
			second := f.admit(t, "synthetic-second")
			if terminal {
				v := f.claim(t, b, second.CommandID)
				old, e := f.ls.CloseExactSession(f.f.ctx, f.ga.runtime, a)
				if e != nil || old.Changed {
					t.Fatal("stale close mutated")
				}
				assertCommand(t, f, second.CommandID, "delivered", v.AttemptID, 1, 3)
			} else {
				assertCommand(t, f, first.CommandID, "execution_unknown", ci.AttemptID, 2, 4)
				v, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, b, ClaimCommandRequest{second.CommandID, 0})
				if e != nil || v.Intent != nil || v.Reason != "DEVICE_RECONCILIATION_REQUIRED" {
					t.Fatal("unknown did not block")
				}
			}
			if n := countInt(t, f, `SELECT count(*) FROM device_sessions WHERE id='sessionA' AND gateway_owner IS NULL AND gateway_connection IS NULL`); n != 1 {
				t.Fatal("old snapshot retained")
			}
		})
	}
}
func TestPGLeaseLF03(t *testing.T) {
	requireAdmissionEnvironment(t)
	f := newLeaseFixture(t)
	old := f.acquire(t, "conn1", 0)
	f.expire(t)
	r, e := f.ls.RecoverExpiredLease(f.f.ctx, f.worker, old)
	if e != nil || !r.Changed {
		t.Fatal("recovery", e)
	}
	h := f.acquire(t, "conn2", 2)
	if h.Fence != 3 {
		t.Fatal("fence monotonic")
	}
	if _, e = f.ls.RenewDeviceLease(f.f.ctx, f.ga, old); e == nil || e.Error() != "STALE_LEASE" {
		t.Fatal("late renewal")
	}
	close, e := f.ls.CloseExactSession(f.f.ctx, f.ga.runtime, old)
	if e != nil || close.Changed {
		t.Fatal("late close")
	}
	for _, q := range []string{`DELETE FROM device_gateway_leases WHERE tenant='T1' AND device='A'`, `UPDATE device_gateway_leases SET fence=1 WHERE tenant='T1' AND device='A'`, `UPDATE device_gateway_leases SET connection_id='forged' WHERE tenant='T1' AND device='A'`} {
		e := expectRejected(t, f.f, nil, q)
		if sqlState(e) != "23514" {
			t.Fatal("DDL guard wrong SQLSTATE")
		}
	}
	renewed, e := f.ls.RenewDeviceLease(f.f.ctx, f.ga, h)
	if e != nil || renewed.Fence != h.Fence || renewed.LeaseUntil.Before(h.LeaseUntil) {
		t.Fatal("valid renewal", e)
	}
	if n := countInt(t, f, `SELECT count(*) FROM device_sessions WHERE id='sessionA' AND gateway_connection='conn2' AND gateway_fence=3`); n != 1 {
		t.Fatal("new snapshot changed")
	}
}
func TestPGLeaseLF04(t *testing.T) {
	requireAdmissionEnvironment(t)
	f := newLeaseFixture(t)
	h := f.acquire(t, "conn1", 0)
	r := f.admit(t, "synthetic-claim")
	hold := holdTenant(t, f.admissionFixture)
	defer hold.Rollback(f.f.ctx)
	ch := make(chan claimResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			o, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{r.CommandID, 0})
			ch <- claimResult{o, e}
		}()
	}
	waitBlocked(t, f.admissionFixture, hold, 2)
	commitHold(t, f.admissionFixture, hold)
	a, b := awaitClaim(t, f, ch), awaitClaim(t, f, ch)
	if a.e != nil {
		a, b = b, a
	}
	if a.e != nil || a.o.Intent == nil || b.e == nil {
		t.Fatal("claim race")
	}
	attempt := a.o.Intent.AttemptID
	assertCommand(t, f, r.CommandID, "delivered", attempt, 1, 3)
	if n := countInt(t, f, `SELECT count(*) FROM outbox WHERE command=$1 AND kind='command_ready' AND state='claimed'`, r.CommandID); n != 1 {
		t.Fatal("claim outbox")
	}
	replay, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{r.CommandID, 1})
	if e != nil || replay.Intent != nil || replay.Reason != "NOT_QUEUED" {
		t.Fatal("lost response replay")
	}
	f.expire(t)
	v, e := f.ls.RecoverExpiredLease(f.f.ctx, f.worker, h)
	if e != nil || !v.Changed {
		t.Fatal("recover claim", e)
	}
	assertCommand(t, f, r.CommandID, "execution_unknown", attempt, 2, 4)
}
func TestPGLeaseLF05(t *testing.T) {
	requireAdmissionEnvironment(t)
	t.Run("outbox_wait_crosses_token_expiry", leaseClaimOutboxExpiry)
	cases := []struct{ name, sql, reason string }{
		{"app_revoked", `UPDATE developer_apps SET active=false WHERE tenant='T1' AND id='X'`, "POLICY_DENIED"},
		{"app_epoch", `UPDATE developer_apps SET auth_epoch=1 WHERE tenant='T1' AND id='X'`, "POLICY_DENIED"},
		{"app_scope", `UPDATE developer_apps SET scopes='[]' WHERE tenant='T1' AND id='X'`, "POLICY_DENIED"},
		{"binding", `DELETE FROM developer_bindings WHERE tenant='T1' AND app='X' AND device='A'`, "POLICY_DENIED"},
		{"device_policy", `UPDATE devices SET policy=2 WHERE tenant='T1' AND id='A'`, "POLICY_DENIED"},
		{"device_epoch", `UPDATE devices SET epoch=1 WHERE tenant='T1' AND id='A'`, "AUTHORITY_REVOKED_OR_CHANGED"},
		{"cancel", `UPDATE commands SET cancel_requested=true WHERE left(id,3)='ad_'`, "CANCEL_REQUESTED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLeaseFixture(t)
			h := f.acquire(t, "conn1", 0)
			r := f.admit(t, "synthetic-policy")
			hold := holdTenant(t, f.admissionFixture)
			defer hold.Rollback(f.f.ctx)
			ch := make(chan claimResult, 1)
			go func() {
				v, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{r.CommandID, 0})
				ch <- claimResult{v, e}
			}()
			waitBlocked(t, f.admissionFixture, hold, 1)
			mustExec(t, hold, f.f.ctx, c.sql)
			commitHold(t, f.admissionFixture, hold)
			v := awaitClaim(t, f, ch)
			reason := v.o.Reason
			if v.e != nil {
				reason = v.e.Error()
			}
			if reason != c.reason || v.o.Intent != nil {
				t.Fatalf("policy rejection %s", reason)
			}
			assertCommand(t, f, r.CommandID, "queued", "", 0, 2)
		})
	}
	for _, kind := range []string{"token", "session", "lease"} {
		t.Run("expiry_"+kind, func(t *testing.T) {
			f := newLeaseFixture(t)
			h := f.acquire(t, "conn1", 0)
			r := f.admit(t, "synthetic-expiry")
			hold := holdTenant(t, f.admissionFixture)
			defer hold.Rollback(f.f.ctx)
			var target time.Time
			if e := hold.QueryRow(f.f.ctx, `SELECT clock_timestamp()+interval '150 milliseconds'`).Scan(&target); e != nil {
				t.Fatal("clock target")
			}
			switch kind {
			case "token":
				mustExec(t, hold, f.f.ctx, `UPDATE developer_apps SET token_expires=$1 WHERE tenant='T1' AND id='X'`, target.UnixMilli())
			case "session":
				mustExec(t, hold, f.f.ctx, `UPDATE device_sessions SET expires=$1 WHERE id='sessionA'`, target.UnixMilli())
			case "lease":
				mustExec(t, hold, f.f.ctx, `UPDATE device_gateway_leases SET lease_until=$1 WHERE tenant='T1' AND device='A'`, target)
			}
			ch := make(chan claimResult, 1)
			go func() {
				v, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{r.CommandID, 0})
				ch <- claimResult{v, e}
			}()
			waitBlocked(t, f.admissionFixture, hold, 1)
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var reached bool
				if e := hold.QueryRow(f.f.ctx, `SELECT clock_timestamp()>=$1`, target).Scan(&reached); e != nil {
					t.Fatal("expiry observation")
				}
				if reached {
					break
				}
				select {
				case <-ticker.C:
				case <-f.f.ctx.Done():
					t.Fatal("expiry barrier deadline")
				}
			}
			commitHold(t, f.admissionFixture, hold)
			v := awaitClaim(t, f, ch)
			if v.o.Intent != nil || v.e == nil && v.o.Reason != "POLICY_DENIED" {
				t.Fatal("expired authority dispatched")
			}
			assertCommand(t, f, r.CommandID, "queued", "", 0, 2)
		})
	}
	t.Run("decision_before_revoke", func(t *testing.T) {
		f := newLeaseFixture(t)
		h := f.acquire(t, "conn1", 0)
		r := f.admit(t, "synthetic-before")
		v := f.claim(t, h, r.CommandID)
		mustExec(t, f.f.owner, f.f.ctx, `UPDATE developer_apps SET active=false WHERE tenant='T1' AND id='X'`)
		assertCommand(t, f, r.CommandID, "delivered", v.AttemptID, 1, 3)
	})
}
func TestPGLeaseLF06(t *testing.T) {
	requireAdmissionEnvironment(t)
	f := newLeaseFixture(t)
	h := f.acquire(t, "conn1", 0)
	r := f.admit(t, "synthetic-target")
	cases := []struct {
		name   string
		mutate func(*LeaseHandle)
	}{
		{"tenant", func(v *LeaseHandle) { v.Tenant = "T2" }}, {"device", func(v *LeaseHandle) { v.DeviceID = "B" }}, {"session", func(v *LeaseHandle) { v.SessionID = "sessionB" }},
		{"owner", func(v *LeaseHandle) { v.OwnerInstance = "workerA" }}, {"connection", func(v *LeaseHandle) { v.ConnectionID = "other" }}, {"epoch", func(v *LeaseHandle) { v.RecoveryEpoch = "other" }}, {"fence", func(v *LeaseHandle) { v.Fence++ }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := h
			c.mutate(&bad)
			v, e := f.ls.CloseExactSession(f.f.ctx, f.ga.runtime, bad)
			if e == nil && v.Changed {
				t.Fatal("wrong close changed")
			}
			o, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, bad, ClaimCommandRequest{r.CommandID, 0})
			if e == nil && o.Intent != nil {
				t.Fatal("wrong handle claimed")
			}
			assertCommand(t, f, r.CommandID, "queued", "", 0, 2)
		})
	}
	for _, q := range []ClaimCommandRequest{{"commandB", 0}, {r.CommandID, 1}} {
		v, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, q)
		if e == nil || v.Intent != nil {
			t.Fatal("wrong command/version accepted")
		}
	}
	t.Run("unsupported_user", func(t *testing.T) {
		// commandA is already referenced by eventA/eventA2 through the exact
		// same-app FK. Do not rewrite that immutable fixture identity merely to
		// exercise unsupported user actors; construct a separate valid user row.
		mustExec(t, f.f.owner, f.f.ctx, `INSERT INTO commands(tenant,id,device,actor,capability,digest,policy,confirm,status,created,expires,actor_type,subscription_snapshot,subscription_snapshot_digest,admission_version)
 VALUES('T1','synthetic_user_command','A','operator','message.send_text','synthetic_unused_digest',1,false,'queued',1,9000000000000,'user','[]',repeat('0',64),'bounded-result-v1')`)
		v, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{"synthetic_user_command", 0})
		if e != nil || v.Reason != "UNSUPPORTED_COMMAND" {
			t.Fatal("user actor not unsupported")
		}
		if v.Intent != nil {
			t.Fatal("unsupported user actor produced intent")
		}
		assertCommand(t, f, "synthetic_user_command", "queued", "", 0, 0)
		if n := countInt(t, f, `SELECT count(*) FROM developer_callbacks cb JOIN commands c ON c.tenant=cb.tenant AND c.app_id=cb.app AND c.id=cb.command WHERE c.tenant='T1' AND c.id='commandA' AND c.actor_type='app' AND c.app_id='X' AND cb.event IN ('eventA','eventA2')`); n != 2 {
			t.Fatal("original same-app callback fixture was changed")
		}
	})
	t.Run("unsupported_export", func(t *testing.T) {
		mustExec(t, f.f.owner, f.f.ctx, `UPDATE commands SET capability='file.export' WHERE id='commandY'`)
		v, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{"commandY", 0})
		if e != nil || v.Reason != "UNSUPPORTED_COMMAND" {
			t.Fatal("export not unsupported")
		}
	})
	t.Run("runtime_role", func(t *testing.T) {
		mustExec(t, f.f.owner, f.f.ctx, `UPDATE runtime_instances SET roles=ARRAY['api'] WHERE instance_id='instanceA'`)
		v, e := f.ls.RenewDeviceLease(f.f.ctx, f.ga, h)
		if e == nil || v.Fence != 0 {
			t.Fatal("wrong runtime accepted")
		}
		mustExec(t, f.f.owner, f.f.ctx, `UPDATE runtime_instances SET roles=ARRAY['gateway'] WHERE instance_id='instanceA'`)
	})
	assertCommand(t, f, r.CommandID, "queued", "", 0, 2)
	// The API never accepts an attempt supplied by an untrusted request; only the
	// locked row's attempt enters retirement CAS. Prove another session cannot retire it.
	ci := f.claim(t, h, r.CommandID)
	bad := h
	bad.SessionID = "sessionB"
	v, e := f.ls.RecoverExpiredLease(f.f.ctx, f.worker, bad)
	if e == nil && v.Changed {
		t.Fatal("wrong attempt owner retired")
	}
	assertCommand(t, f, r.CommandID, "delivered", ci.AttemptID, 1, 3)
}
func TestPGLeaseLF07(t *testing.T) {
	requireAdmissionEnvironment(t)
	t.Run("late_claim_failure", func(t *testing.T) {
		f := newLeaseFixture(t)
		h := f.acquire(t, "conn1", 0)
		r := f.admit(t, "synthetic-rollback")
		mustExec(t, f.f.owner, f.f.ctx, `ALTER TABLE audit ADD CONSTRAINT synthetic_late_failure CHECK(action<>'command.dispatch')`)
		v, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{r.CommandID, 0})
		if e == nil || v.Intent != nil {
			t.Fatal("late failure committed")
		}
		assertCommand(t, f, r.CommandID, "queued", "", 0, 2)
		if n := countInt(t, f, `SELECT count(*) FROM outbox WHERE command=$1 AND state='pending' AND kind='command_ready'`, r.CommandID); n != 1 {
			t.Fatal("outbox not rolled back")
		}
		if n := countInt(t, f, `SELECT count(*) FROM audit WHERE action='command.dispatch'`); n != 0 {
			t.Fatal("audit partial")
		}
		mustExec(t, f.f.owner, f.f.ctx, `ALTER TABLE audit DROP CONSTRAINT synthetic_late_failure`)
		f.claim(t, h, r.CommandID)
	})
	t.Run("late_retire_failure", func(t *testing.T) {
		f := newLeaseFixture(t)
		h := f.acquire(t, "conn1", 0)
		r := f.admit(t, "synthetic-retire")
		ci := f.claim(t, h, r.CommandID)
		mustExec(t, f.f.owner, f.f.ctx, `ALTER TABLE audit ADD CONSTRAINT synthetic_late_failure CHECK(action<>'gateway.lease_retired')`)
		v, e := f.ls.CloseExactSession(f.f.ctx, f.ga.runtime, h)
		if e == nil || v.Changed {
			t.Fatal("late retire committed")
		}
		assertCommand(t, f, r.CommandID, "delivered", ci.AttemptID, 1, 3)
		if n := countInt(t, f, `SELECT count(*) FROM device_gateway_leases WHERE state='active' AND fence=1 AND connection_id='conn1'`); n != 1 {
			t.Fatal("lease rollback")
		}
		if n := countInt(t, f, `SELECT count(*) FROM device_sessions WHERE gateway_fence=1 AND gateway_connection='conn1'`); n != 1 {
			t.Fatal("snapshot rollback")
		}
		if n := countInt(t, f, `SELECT count(*) FROM outbox WHERE kind='command_state_changed'`); n != 0 {
			t.Fatal("notification rollback")
		}
	})
	t.Run("lease_lock_timeout", func(t *testing.T) {
		f := newLeaseFixture(t)
		h := f.acquire(t, "conn1", 0)
		r := f.admit(t, "synthetic-timeout")
		hold, e := f.f.owner.Begin(f.f.ctx)
		if e != nil {
			t.Fatal("begin")
		}
		defer hold.Rollback(f.f.ctx)
		mustExec(t, hold, f.f.ctx, `SELECT 1 FROM device_gateway_leases WHERE tenant='T1' AND device='A' FOR UPDATE`)
		ctx, cancel := context.WithTimeout(f.f.ctx, 300*time.Millisecond)
		defer cancel()
		ch := make(chan claimResult, 1)
		go func() {
			v, e := f.ls.ClaimReadyCommand(ctx, f.ga, h, ClaimCommandRequest{r.CommandID, 0})
			ch <- claimResult{v, e}
		}()
		waitBlocked(t, f.admissionFixture, hold, 1)
		v := awaitClaim(t, f, ch)
		if v.e == nil || v.e.Error() != "STORE_TIMEOUT" || v.o.Intent != nil {
			t.Fatal("bounded timeout")
		}
		commitHold(t, f.admissionFixture, hold)
		assertCommand(t, f, r.CommandID, "queued", "", 0, 2)
	})
}
func TestPGLeaseLF08(t *testing.T) {
	requireAdmissionEnvironment(t)
	t.Run("recovery_claim_race", leaseRecoveryRace)
	for _, state := range []string{"delivered", "accepted", "running", "execution_unknown", "unknown_result", "succeeded", "contradictory_result"} {
		t.Run(state, func(t *testing.T) {
			f := newLeaseFixture(t)
			h := f.acquire(t, "conn1", 0)
			r := f.admit(t, "synthetic-recovery")
			ci := f.claim(t, h, r.CommandID)
			switch state {
			case "unknown_result":
				mustExec(t, f.f.owner, f.f.ctx, `UPDATE commands SET status='execution_unknown',result='{"status":"execution_unknown"}' WHERE id=$1`, r.CommandID)
			case "contradictory_result":
				mustExec(t, f.f.owner, f.f.ctx, `UPDATE commands SET result='{"status":"succeeded"}' WHERE id=$1`, r.CommandID)
			case "succeeded":
				mustExec(t, f.f.owner, f.f.ctx, `UPDATE commands SET status='succeeded',result='{"status":"succeeded"}' WHERE id=$1`, r.CommandID)
			default:
				mustExec(t, f.f.owner, f.f.ctx, `UPDATE commands SET status=$2 WHERE id=$1`, r.CommandID, state)
			}
			f.expire(t)
			jobs := countInt(t, f, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant' AND tenant='T1'`)
			v, e := f.ls.RecoverExpiredLease(f.f.ctx, f.worker, h)
			if state == "contradictory_result" {
				if e == nil || e.Error() != "INTEGRITY_CONFLICT" || v.Changed {
					t.Fatal("contradictory result overwritten")
				}
				assertCommand(t, f, r.CommandID, "delivered", ci.AttemptID, 1, 3)
				return
			}
			if e != nil || !v.Changed {
				t.Fatal("recovery failed", e)
			}
			expected := state
			version, events := int64(1), 3
			if state == "delivered" || state == "accepted" || state == "running" {
				expected = "execution_unknown"
				version = 2
				events = 4
			}
			if state == "unknown_result" {
				expected = "execution_unknown"
			}
			assertCommand(t, f, r.CommandID, expected, ci.AttemptID, version, events)
			again, e := f.ls.RecoverExpiredLease(f.f.ctx, f.worker, h)
			if e != nil || again.Changed {
				t.Fatal("repeat recovery")
			}
			next := f.acquire(t, "conn2", 2)
			second := f.admit(t, "synthetic-next")
			o, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, next, ClaimCommandRequest{second.CommandID, 0})
			if e != nil {
				t.Fatal(e)
			}
			if expected == "execution_unknown" && (o.Intent != nil || o.Reason != "DEVICE_RECONCILIATION_REQUIRED") {
				t.Fatal("unknown replay")
			}
			if expected == "succeeded" && o.Intent == nil {
				t.Fatal("terminal blocked control")
			}
			if n := countInt(t, f, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant' AND tenant='T1'`); n != jobs+1 {
				t.Fatal("unknown released quota")
			}
			if strings.Contains(state, "result") || state == "succeeded" {
				if n := countInt(t, f, `SELECT count(*) FROM commands WHERE id=$1 AND result IS NOT NULL`, r.CommandID); n != 1 {
					t.Fatal("durable result lost")
				}
			}
		})
	}
}

// The LF08 recovery/claim interleaving is DB-only: neither winner sends a frame.
func leaseRecoveryRace(t *testing.T) {
	f := newLeaseFixture(t)
	h := f.acquire(t, "conn1", 0)
	first := f.admit(t, "synthetic-old-race")
	ci := f.claim(t, h, first.CommandID)
	next := f.admit(t, "synthetic-next-race")
	f.expire(t)
	hold := holdTenant(t, f.admissionFixture)
	defer hold.Rollback(f.f.ctx)
	cc := make(chan claimResult, 1)
	rc := make(chan error, 1)
	go func() {
		o, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{next.CommandID, 0})
		cc <- claimResult{o, e}
	}()
	go func() { _, e := f.ls.RecoverExpiredLease(f.f.ctx, f.worker, h); rc <- e }()
	waitBlocked(t, f.admissionFixture, hold, 2)
	commitHold(t, f.admissionFixture, hold)
	v := awaitClaim(t, f, cc)
	if v.o.Intent != nil {
		t.Fatal("recovery race produced new attempt")
	}
	select {
	case e := <-rc:
		if e != nil {
			t.Fatal("recover race", e)
		}
	case <-f.f.ctx.Done():
		t.Fatal("recovery race deadline")
	}
	assertCommand(t, f, first.CommandID, "execution_unknown", ci.AttemptID, 2, 4)
	assertCommand(t, f, next.CommandID, "queued", "", 0, 2)
}

// LF02: post-retirement decision must reject a session that expired while
// waiting for the old command. Any transient unknown transition is rolled back.
func leaseTakeoverExpiry(t *testing.T) {
	f := newLeaseFixture(t)
	old := f.acquire(t, "connA", 0)
	r := f.admit(t, "synthetic-takeover-expiry")
	ci := f.claim(t, old, r.CommandID)
	f.rotate(t)
	audits := countInt(t, f, `SELECT count(*) FROM audit`)
	hold, e := f.f.owner.Begin(f.f.ctx)
	if e != nil {
		t.Fatal("holder begin")
	}
	defer hold.Rollback(f.f.ctx)
	mustExec(t, hold, f.f.ctx, `SELECT 1 FROM commands WHERE tenant='T1' AND id=$1 FOR UPDATE`, r.CommandID)
	// Use the already validated second fixture connection so the session expiry
	// update commits without holding the session lock until after the barrier.
	var cutoff time.Time
	if e = f.f.store.QueryRow(f.f.ctx, `UPDATE device_sessions SET expires=floor(extract(epoch FROM clock_timestamp()+interval '1 second')*1000)::bigint WHERE id='sessionNew' RETURNING to_timestamp(expires::double precision/1000)`).Scan(&cutoff); e != nil {
		t.Fatal("session cutoff setup")
	}
	ch := make(chan leaseResult, 1)
	go func() {
		h, e := f.ls.AcquireDeviceLease(f.f.ctx, f.ga, AcquireLeaseRequest{"connB", 1})
		ch <- leaseResult{h, e}
	}()
	waitBlocked(t, f.admissionFixture, hold, 1)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var reached bool
		if e = hold.QueryRow(f.f.ctx, `SELECT clock_timestamp()>=$1`, cutoff).Scan(&reached); e != nil {
			t.Fatal("cutoff observation")
		}
		if reached {
			break
		}
		select {
		case <-ticker.C:
		case <-f.f.ctx.Done():
			t.Fatal("cutoff deadline")
		}
	}
	commitHold(t, f.admissionFixture, hold)
	v := awaitLease(t, f, ch)
	if v.e == nil || v.e.Error() != "SESSION_NOT_CURRENT" || v.h.Fence != 0 {
		t.Fatal("expired takeover returned handle")
	}
	assertCommand(t, f, r.CommandID, "delivered", ci.AttemptID, 1, 3)
	if n := countInt(t, f, `SELECT count(*) FROM audit`); n != audits {
		t.Fatal("retirement audit was not rolled back")
	}
	if n := countInt(t, f, `SELECT count(*) FROM device_gateway_leases WHERE tenant='T1' AND device='A' AND state='active' AND fence=1 AND session_id='sessionA' AND connection_id='connA' AND lease_until=$1`, old.LeaseUntil); n != 1 {
		t.Fatal("old lease changed")
	}
	if n := countInt(t, f, `SELECT count(*) FROM device_sessions WHERE id='sessionA' AND gateway_connection='connA' AND gateway_fence=1`); n != 1 {
		t.Fatal("old snapshot changed")
	}
	if n := countInt(t, f, `SELECT count(*) FROM device_sessions WHERE id='sessionNew' AND gateway_owner IS NULL AND gateway_connection IS NULL`); n != 1 {
		t.Fatal("new snapshot incorrectly installed")
	}
	if n := countInt(t, f, `SELECT count(*) FROM outbox WHERE kind='command_state_changed'`); n != 0 {
		t.Fatal("retirement outbox was not rolled back")
	}
}

func leaseClaimOutboxExpiry(t *testing.T) {
	f := newLeaseFixture(t)
	h := f.acquire(t, "conn1", 0)
	r := f.admit(t, "synthetic-outbox-expiry")
	audits := countInt(t, f, `SELECT count(*) FROM audit`)
	hold, e := f.f.owner.Begin(f.f.ctx)
	if e != nil {
		t.Fatal("outbox holder begin")
	}
	defer hold.Rollback(f.f.ctx)
	mustExec(t, hold, f.f.ctx, `SELECT id FROM outbox WHERE tenant='T1' AND command=$1 AND kind='command_ready' FOR UPDATE`, r.CommandID)
	var cutoff time.Time
	if e = f.f.store.QueryRow(f.f.ctx, `UPDATE developer_apps SET token_expires=floor(extract(epoch FROM clock_timestamp()+interval '1 second')*1000)::bigint WHERE tenant='T1' AND id='X' RETURNING to_timestamp(token_expires::double precision/1000)`).Scan(&cutoff); e != nil {
		t.Fatal("token cutoff")
	}
	ch := make(chan claimResult, 1)
	go func() {
		o, e := f.ls.ClaimReadyCommand(f.f.ctx, f.ga, h, ClaimCommandRequest{r.CommandID, 0})
		ch <- claimResult{o, e}
	}()
	waitBlocked(t, f.admissionFixture, hold, 1)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var reached bool
		if e = hold.QueryRow(f.f.ctx, `SELECT clock_timestamp()>=$1`, cutoff).Scan(&reached); e != nil {
			t.Fatal("cutoff query")
		}
		if reached {
			break
		}
		select {
		case <-ticker.C:
		case <-f.f.ctx.Done():
			t.Fatal("outbox cutoff deadline")
		}
	}
	commitHold(t, f.admissionFixture, hold)
	v := awaitClaim(t, f, ch)
	if v.e != nil || v.o.Intent != nil || v.o.Reason != "POLICY_DENIED" {
		t.Fatal("outbox wait bypassed final expiry")
	}
	assertCommand(t, f, r.CommandID, "queued", "", 0, 2)
	if n := countInt(t, f, `SELECT count(*) FROM audit`); n != audits {
		t.Fatal("unexpected dispatch audit")
	}
	if n := countInt(t, f, `SELECT count(*) FROM outbox WHERE tenant='T1' AND command=$1 AND kind='command_ready' AND state='pending'`, r.CommandID); n != 1 {
		t.Fatal("outbox changed")
	}
	if n := countInt(t, f, `SELECT count(*) FROM device_gateway_leases WHERE fence=1 AND connection_id='conn1' AND lease_until=$1`, h.LeaseUntil); n != 1 {
		t.Fatal("lease changed")
	}
	if n := countInt(t, f, `SELECT count(*) FROM device_sessions WHERE id='sessionA' AND gateway_fence=1 AND gateway_connection='conn1'`); n != 1 {
		t.Fatal("snapshot changed")
	}
	if n := countInt(t, f, `SELECT charged_jobs FROM scope_limits WHERE scope_kind='tenant' AND tenant='T1'`); n != 1 {
		t.Fatal("quota changed")
	}
}
