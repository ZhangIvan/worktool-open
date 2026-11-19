package pg

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Registered as children of the existing lifecycle group. All paths enter the
// unchanged opt-in fixture gate before sockets. These tests are NOT_RUN offline.
func retentionAuditIntegrationCases(t *testing.T) {
	t.Run("RETENTION_AUDIT_supported_shared_event_no_mutation", retentionAuditPGSharedEvent)
	t.Run("RETENTION_AUDIT_repeatable_snapshot_admission", func(t *testing.T) { retentionAuditPGSnapshot(t, false) })
	t.Run("RETENTION_AUDIT_repeatable_snapshot_materialization", func(t *testing.T) { retentionAuditPGSnapshot(t, true) })
	t.Run("RETENTION_AUDIT_caps_and_locked_rows", retentionAuditPGBounds)
	t.Run("RETENTION_AUDIT_drift_missing_scope_legacy", retentionAuditPGConflicts)
	t.Run("RETENTION_AUDIT_nonowner_select_only", retentionAuditPGPrivileges)
}

func newRetentionPGFixture(t *testing.T) (*admissionFixture, *RetentionAuditor, TrustedCapacityTenant) {
	t.Helper()
	a := newAdmissionFixture(t, 2)
	// Never filter or delete the intentionally incomplete legacy T1 fixture.
	// Create a separate complete supported tenant within this random test schema.
	fp := hashText(a.f.schema + "audit_app")
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO tenants VALUES('TA');
INSERT INTO users(tenant,id,login,role,password) VALUES('TA','U','audit_user','admin','!disabled');
INSERT INTO devices(tenant,id,name,key_id,secret,capabilities) VALUES('TA','AuditDevice','synthetic','audit_key','!audit_disabled','["message.send_text"]');`)
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO developer_apps(tenant,id,creator,name,token_hash,token_expires,scopes) VALUES('TA','AuditApp','U','synthetic',$1,9000000000000,'["commands:write"]')`, fp)
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO developer_bindings VALUES('TA','AuditApp','AuditDevice');
INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('TA','AD1','AuditApp','synthetic:no-network',1,'opaque:not-installed',1,'active'),('TA','AD2','AuditApp','synthetic:no-network',1,'opaque:not-installed',1,'active');
INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts)
VALUES('TA','AuditApp','AS1',1,'AD1',1,1,'command.result','bounded-result-v1',1,65536,4),('TA','AuditApp','AS2',1,'AD2',1,1,'command.result','bounded-result-v1',1,65536,4);
INSERT INTO scope_limits(scope_kind,tenant,scope_id,destination_id,max_in_flight,rate_per_sec,burst,tokens,policy_version,enabled,capacity_jobs,capacity_bytes,admission_enabled)
VALUES('tenant','TA','TA',NULL,2048,1,1,1,1,true,100,10000000,true),('destination','TA','AD1','AD1',2048,1,1,1,1,true,100,10000000,true),('destination','TA','AD2','AD2',2048,1,1,1,1,true,100,10000000,true);`)
	var err error
	a.a, err = AppAuthorityFromAuthenticated("TA", "AuditApp", "AuditDevice", fp, 0, 0)
	if err != nil {
		t.Fatal("audit authority fixture")
	}
	a.request.DeviceID = "AuditDevice"
	auditor, err := NewRetentionAuditor(a.store.runner, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := CapacityTenantFromAuthorizedRead("TA")
	if err != nil {
		t.Fatal(err)
	}
	return a, auditor, tenant
}
func retentionPGAdmit(t *testing.T, a *admissionFixture, key string) Accepted {
	t.Helper()
	r := a.request
	r.IdempotencyKey = key
	v, err := a.store.AdmitAppText(a.f.ctx, a.a, r)
	if err != nil {
		t.Fatalf("synthetic audit admission failed: %s", sqlState(err))
	}
	return v
}
func retentionPGRun(t *testing.T, a *admissionFixture, auditor *RetentionAuditor, tenant TrustedCapacityTenant, want CapacityAuditStatus) CapacityAudit {
	t.Helper()
	r, err := auditor.AuditRetainedCapacity(a.f.ctx, tenant)
	if err != nil || r.Status != want || r.ReleasableJobs != 0 || r.ReleasableBytes != 0 {
		t.Fatalf("audit status=%s wanted=%s SQLSTATE=%s issues=%v", r.Status, want, sqlState(err), r.Issues)
	}
	return r
}

// This prepares synthetic result metadata in one transaction, not a device ACK
// or proof of the gateway's full protocol. Real admission above creates charges.
func retentionPGRollback(t *testing.T, tx pgx.Tx) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Error("synthetic audit transaction rollback unconfirmed")
	}
}

func retentionPGMaterialize(t *testing.T, a *admissionFixture, command string) {
	t.Helper()
	tx, err := a.f.owner.Begin(a.f.ctx)
	if err != nil {
		t.Fatal("synthetic materialization begin")
	}
	defer retentionPGRollback(t, tx)
	mustExec(t, tx, a.f.ctx, `UPDATE commands SET status='succeeded',effect='applied',result='{}' WHERE tenant='TA' AND id=$1;
`, command)
	mustExec(t, tx, a.f.ctx, `UPDATE admission_reservations SET state='materialized' WHERE tenant='TA' AND command=$1 AND kind='result'`, command)
	mustExec(t, tx, a.f.ctx, `INSERT INTO developer_callbacks(event,tenant,app,command,status,body,state,next_at) VALUES($1,'TA','AuditApp',$2,'succeeded','{}','managed_outbox',1)`, "audit_event_"+command, command)
	mustExec(t, tx, a.f.ctx, `UPDATE admission_reservations SET state='materialized',delivery_id='audit_delivery_'||reservation_id,event_id=$2,materialized_at=clock_timestamp() WHERE tenant='TA' AND command=$1 AND kind='callback'`, command, "audit_event_"+command)
	mustExec(t, tx, a.f.ctx, `INSERT INTO callback_outbox(tenant,delivery_id,event_id,destination_id,app,command,device,app_epoch,device_epoch,body_bytes,payload_digest,policy_version,key_version,expires_at,max_attempts,reservation_id,reservation_kind,reservation_state,subscription_id,subscription_version,event_ordinal)
SELECT tenant,delivery_id,event_id,destination_id,app,command,'AuditDevice',0,0,decode('7b7d','hex'),repeat('0',64),destination_policy_version,key_version,clock_timestamp()+interval '1 hour',max_attempts,reservation_id,'callback','materialized',subscription_id,subscription_version,event_ordinal FROM admission_reservations WHERE tenant='TA' AND command=$1 AND kind='callback'`, command)
	mustExec(t, tx, a.f.ctx, `INSERT INTO outbox(tenant,device,command,kind) VALUES('TA','AuditDevice',$1,'result_ack')`, command)
	if err = tx.Commit(a.f.ctx); err != nil {
		t.Fatalf("synthetic graph commit SQLSTATE=%s", sqlState(err))
	}
}

// Test-only complete row fingerprints stay in memory and are never logged.
func retentionPGFingerprint(t *testing.T, a *admissionFixture) string {
	t.Helper()
	out := ""
	for _, table := range []string{"scope_limits", "admission_reservations", "commands", "callback_outbox", "developer_callbacks", "callback_attempts", "scope_slots", "outbox"} {
		var hash string
		q := `SELECT md5(COALESCE(string_agg(row_to_json(x)::text,E'\n' ORDER BY row_to_json(x)::text),'')) FROM ` + table + ` x WHERE tenant='TA'`
		if err := a.f.owner.QueryRow(a.f.ctx, q).Scan(&hash); err != nil {
			t.Fatal("synthetic before/after fingerprint")
		}
		out += hash
	}
	return out
}
func retentionAuditPGSharedEvent(t *testing.T) {
	a, auditor, tenant := newRetentionPGFixture(t)
	c := retentionPGAdmit(t, a, "audit-shared-001")
	retentionPGMaterialize(t, a, c.CommandID)
	// Exercise nonempty attempt, occupied expired-slot and nullable time decoders.
	mustExec(t, a.f.owner, a.f.ctx, `UPDATE callback_outbox SET fence=1,attempt_count=1 WHERE tenant='TA' AND destination_id='AD1';
INSERT INTO callback_attempts(tenant,delivery_id,fence,attempt_no,recovery_epoch,owner_instance,finished_at,outcome)
SELECT tenant,delivery_id,1,1,'epoch1','instanceA',clock_timestamp(),'delivery_unknown' FROM callback_outbox WHERE tenant='TA' AND destination_id='AD1';
INSERT INTO scope_slots(scope_kind,tenant,scope_id,slot_no,owner_instance,delivery_id,delivery_fence,recovery_epoch,slot_fence,lease_until)
SELECT 'destination',tenant,destination_id,0,'instanceA',delivery_id,1,'epoch1',1,clock_timestamp()-interval '1 second' FROM callback_outbox WHERE tenant='TA' AND destination_id='AD1';
UPDATE admission_reservations SET retain_until=clock_timestamp()-interval '1 second' WHERE tenant='TA' AND kind='result';`)
	before := retentionPGFingerprint(t, a)
	r := retentionPGRun(t, a, auditor, tenant, CapacityConserved)
	after := retentionPGFingerprint(t, a)
	if before != after {
		t.Fatal("audit mutated ledger/graph")
	}
	for _, h := range []string{"SHARED_EVENT_COHORT", "RETENTION_DEADLINE_UNSET", "OUTBOX_SETTLEMENT_NOT_PROVEN", "CALLBACK_PENDING_OR_LEASED", "CALLBACK_ATTEMPT_UNRESOLVED", "CALLBACK_SLOT_REFERENCE"} {
		if !slices.Contains(r.Holds, h) {
			t.Fatal("missing conservative hold", h)
		}
	}
	for _, s := range r.Scopes {
		if s.Kind == "tenant" && (s.ExpectedJobs != 3 || s.ExpectedBytes != 376832) {
			t.Fatal("shared-body charge wrong")
		}
	}
}

type capacityBarrierPool struct {
	Pool
	reached, resume chan struct{}
	once            sync.Once
}

func (p *capacityBarrierPool) Acquire(ctx context.Context) (Connection, error) {
	c, e := p.Pool.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	return &capacityBarrierConnection{Connection: c, p: p}, nil
}

type capacityBarrierConnection struct {
	Connection
	p *capacityBarrierPool
}

func (c *capacityBarrierConnection) BeginTx(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	tx, e := c.Connection.BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	return &capacityBarrierTx{Tx: tx, p: c.p}, nil
}

type capacityBarrierTx struct {
	pgx.Tx
	p *capacityBarrierPool
}

func (tx *capacityBarrierTx) Query(ctx context.Context, q string, args ...any) (pgx.Rows, error) {
	// Barrier before the second table, after the first has closed; snapshot and
	// counters have already been read. Only this test wrapper waits.
	if q == capacityAuditQueries[1] {
		tx.p.once.Do(func() {
			close(tx.p.reached)
			select {
			case <-tx.p.resume:
			case <-ctx.Done():
			}
		})
	}
	return tx.Tx.Query(ctx, q, args...)
}
func retentionAuditPGSnapshot(t *testing.T, materialization bool) {
	a, _, tenant := newRetentionPGFixture(t)
	c := retentionPGAdmit(t, a, "audit-snapshot-001")
	p := &capacityBarrierPool{Pool: PoolAdapter{a.pool}, reached: make(chan struct{}), resume: make(chan struct{})}
	runner, err := NewRunner(p, Timeouts{2 * time.Second, time.Second, 10 * time.Second, time.Second, time.Second})
	if err != nil {
		t.Fatal(err)
	}
	auditor, err := NewRetentionAuditor(runner, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		r CapacityAudit
		e error
	}
	done := make(chan outcome, 1)
	auditCtx, cancelAudit := context.WithCancel(a.f.ctx)
	go func() { r, e := auditor.AuditRetainedCapacity(auditCtx, tenant); done <- outcome{r, e} }()
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(p.resume) }) }
	joined := false
	// Own the worker on success and every Fatal path, before fixture cleanup.
	defer func() {
		cancelAudit()
		resume()
		if !joined {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("audit worker cleanup unconfirmed before fixture teardown")
			}
		}
	}()
	select {
	case <-p.reached:
	case <-a.f.ctx.Done():
		t.Fatal("audit barrier missing")
	}
	// This writer must complete while the audit remains paused: no row/gate lock.
	if materialization {
		retentionPGMaterialize(t, a, c.CommandID)
	} else {
		retentionPGAdmit(t, a, "audit-snapshot-002")
	}
	resume()
	var first CapacityAudit
	select {
	case o := <-done:
		joined = true
		if o.e != nil || o.r.Status != CapacityConserved {
			t.Fatal("inconsistent repeatable snapshot")
		}
		first = o.r
	case <-a.f.ctx.Done():
		t.Fatal("audit did not finish")
	}
	for _, s := range first.Scopes {
		if s.Kind == "tenant" && s.ExpectedJobs != 3 {
			t.Fatal("snapshot mixed new reservations with old counters")
		}
	}
	if !slices.Contains(first.Holds, "HELD_RESERVATION") || slices.Contains(first.Holds, "SHARED_EVENT_COHORT") {
		t.Fatal("snapshot mixed terminal materialization")
	}
	next := retentionPGRun(t, a, auditor, tenant, CapacityConserved)
	if materialization {
		if !slices.Contains(next.Holds, "SHARED_EVENT_COHORT") {
			t.Fatal("fresh audit omitted committed result graph")
		}
	} else {
		for _, s := range next.Scopes {
			if s.Kind == "tenant" && s.ExpectedJobs != 6 {
				t.Fatal("fresh audit omitted committed admission")
			}
		}
	}
}
func retentionAuditPGBounds(t *testing.T) {
	a, auditor, tenant := newRetentionPGFixture(t)
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO scope_slots(scope_kind,tenant,scope_id,slot_no) SELECT 'tenant','TA','TA',i FROM generate_series(0,1023) i`)
	// Empty slots count toward the cap, and row locks cannot hide them.
	hold, err := a.f.owner.Begin(a.f.ctx)
	if err != nil {
		t.Fatal("slot lock begin")
	}
	defer retentionPGRollback(t, hold)
	mustExec(t, hold, a.f.ctx, `SELECT slot_no FROM scope_slots WHERE tenant='TA' FOR UPDATE`)
	retentionPGRun(t, a, auditor, tenant, CapacityConserved)
	retentionPGRollback(t, hold)
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO scope_slots(scope_kind,tenant,scope_id,slot_no) VALUES('tenant','TA','TA',1024)`)
	r := retentionPGRun(t, a, auditor, tenant, CapacityIncomplete)
	if len(r.Scopes) != 0 || !r.ObservedAt.IsZero() {
		t.Fatal("capped input reported totals")
	}
}
func retentionAuditPGConflicts(t *testing.T) {
	a, auditor, tenant := newRetentionPGFixture(t)
	retentionPGAdmit(t, a, "audit-drift-001")
	legacy, _ := CapacityTenantFromAuthorizedRead("T1")
	retentionPGRun(t, a, auditor, legacy, CapacityUnsupported)
	mustExec(t, a.f.owner, a.f.ctx, `UPDATE scope_limits SET charged_bytes=charged_bytes-1 WHERE tenant='TA' AND scope_kind='tenant'`)
	retentionPGRun(t, a, auditor, tenant, CapacityDrift)
	// A new supported command without a result
	// reservation is a DDL-permitted missing graph, which the auditor must reject.
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO commands(tenant,id,device,actor,capability,digest,policy,confirm,status,created,expires,actor_type,app_epoch,app_id,subscription_snapshot,subscription_snapshot_digest,admission_version)
VALUES('TA','audit_missing_result','AuditDevice','U','message.send_text',repeat('0',64),1,false,'queued',1,9000000000000,'app',0,'AuditApp','[]',repeat('0',64),'bounded-result-v1')`)
	retentionPGRun(t, a, auditor, tenant, CapacityIntegrityConflict)
	// New immutable callback identities without a destination scope exercise
	// the DDL-permitted missing-scope case without deleting or repairing anything.
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('TA','AD3','AuditApp','synthetic:no-network',1,'opaque:not-installed',1,'active');
INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('TA','AuditApp','AS3',1,'AD3',1,1,'command.result','bounded-result-v1',1,65536,4);
INSERT INTO commands(tenant,id,device,actor,capability,digest,policy,confirm,status,created,expires,actor_type,app_epoch,app_id,subscription_snapshot,subscription_snapshot_digest,admission_version)
VALUES('TA','audit_missing_scope','AuditDevice','U','message.send_text',repeat('0',64),1,false,'queued',1,9000000000000,'app',0,'AuditApp','[{"destinationId":"AD3"}]',repeat('0',64),'bounded-result-v1');
INSERT INTO admission_reservations(tenant,reservation_id,command,kind,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES('TA','audit_missing_scope_result','audit_missing_scope','result',0,'held',1,196608);
INSERT INTO admission_reservations(tenant,reservation_id,command,kind,app,subscription_id,subscription_version,destination_id,destination_policy_version,key_version,max_attempts,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES('TA','audit_missing_scope_callback','audit_missing_scope','callback','AuditApp','AS3',1,'AD3',1,1,4,0,'held',1,90112);`)
	conflict := retentionPGRun(t, a, auditor, tenant, CapacityIntegrityConflict)
	if !slices.Contains(conflict.Issues, "MISSING_DESTINATION_SCOPE") {
		t.Fatal("missing destination scope hidden")
	}
	// A separately seeded empty tenant with no scope exercises missing tenant scope.
	mustExec(t, a.f.owner, a.f.ctx, `INSERT INTO tenants VALUES('TA_missing')`)
	missing, _ := CapacityTenantFromAuthorizedRead("TA_missing")
	r := retentionPGRun(t, a, auditor, missing, CapacityIntegrityConflict)
	if !slices.Contains(r.Issues, "MISSING_TENANT_SCOPE") {
		t.Fatal("missing tenant scope not detected")
	}
}
func retentionAuditPGPrivileges(t *testing.T) {
	a, auditor, tenant := newRetentionPGFixture(t)
	retentionPGAdmit(t, a, "audit-role-001")
	var owner bool
	if err := a.f.store.QueryRow(a.f.ctx, `SELECT c.relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_class c WHERE c.oid='scope_limits'::regclass`).Scan(&owner); err != nil || owner {
		t.Fatal("audit fixture must be nonowner")
	}
	// This role still has UPDATE here, so only read-only transaction semantics
	// can reject the no-op mutation. Do not conflate it with a permission denial.
	err := a.store.runner.Run(a.f.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE scope_limits SET charged_jobs=charged_jobs WHERE tenant='TA'`)
		return e
	})
	if err == nil || sqlState(err) != "25006" {
		t.Fatal("read-only mutation was not rejected with 25006")
	}
	role := pgx.Identifier{a.f.store.Config().User}.Sanitize()
	// Revoke only random-fixture-schema table grants, never change role attributes.
	for _, table := range []string{"scope_limits", "admission_reservations", "commands", "callback_outbox", "developer_callbacks", "callback_attempts", "scope_slots", "outbox"} {
		mustExec(t, a.f.owner, a.f.ctx, "REVOKE INSERT,UPDATE,DELETE ON "+pgx.Identifier{a.f.schema, table}.Sanitize()+" FROM "+role)
	}
	retentionPGRun(t, a, auditor, tenant, CapacityConserved)
	mustExec(t, a.f.owner, a.f.ctx, "REVOKE SELECT ON "+pgx.Identifier{a.f.schema, "outbox"}.Sanitize()+" FROM "+role)
	r, err := auditor.AuditRetainedCapacity(a.f.ctx, tenant)
	if err == nil || r.Status != CapacityUnavailable || len(r.Scopes) > 0 || sqlState(err) != "42501" || errors.Is(err, errCapacityAuditBound) {
		t.Fatal("missing read privilege did not fail closed")
	}
}
