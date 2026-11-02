package pg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// All integration entrypoints call this gate before even parsing a DSN. No
// default localhost discovery; both identities must be provisioned separately.
type pgFixture struct {
	owner, store *pgx.Conn
	schema       string
	ctx          context.Context
}

func integrationFixture(t *testing.T) *pgFixture {
	t.Helper()
	allow, ownerDSN, storeDSN := os.Getenv("WORKTOOL_PGTEST_ALLOW"), os.Getenv("WORKTOOL_PGTEST_OWNER_DSN"), os.Getenv("WORKTOOL_PGTEST_STORE_DSN")
	if allow == "" && ownerDSN == "" && storeDSN == "" {
		t.Skip("NOT_RUN: real PostgreSQL requires explicit isolated approval and two pre-provisioned DSNs")
	}
	if allow != "isolated-synthetic-schema-only" || ownerDSN == "" || storeDSN == "" {
		t.Fatal("PGTEST gate incomplete; no connection attempted")
	}
	ownerCfg, e := pgx.ParseConfig(ownerDSN)
	if e != nil {
		t.Fatal("invalid owner DSN; redacted")
	}
	storeCfg, e := pgx.ParseConfig(storeDSN)
	if e != nil {
		t.Fatal("invalid store DSN; redacted")
	}
	valid := func(c *pgx.ConnConfig) bool {
		return strings.HasPrefix(c.Database, "wt_test_") && len(c.Database) > 8 && (c.Host == "127.0.0.1" || strings.HasPrefix(c.Host, "/")) && len(c.Fallbacks) == 0
	}
	if !valid(ownerCfg) || !valid(storeCfg) || ownerCfg.Host != storeCfg.Host || ownerCfg.Port != storeCfg.Port || ownerCfg.Database != storeCfg.Database || ownerCfg.User == storeCfg.User {
		t.Fatal("PGTEST requires same explicit local wt_test_ database, no fallback, distinct roles")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	b := make([]byte, 12)
	if _, e = rand.Read(b); e != nil {
		t.Fatal("run ID entropy failure")
	}
	schema := "wt_it_" + hex.EncodeToString(b)
	for _, c := range []*pgx.ConnConfig{ownerCfg, storeCfg} {
		c.ConnectTimeout = 2 * time.Second
		c.RuntimeParams["application_name"] = schema
		c.RuntimeParams["statement_timeout"] = "5000"
		c.RuntimeParams["lock_timeout"] = "1000"
		c.RuntimeParams["idle_in_transaction_session_timeout"] = "5000"
	}
	owner, e := pgx.ConnectConfig(ctx, ownerCfg)
	if e != nil {
		t.Fatal("owner connection failed (details redacted)")
	}
	// Cleanup only this random schema and these handles. No external PID kills.
	t.Cleanup(func() {
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_, err := owner.Exec(cleanup, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		if err != nil {
			t.Error("isolated schema cleanup failed; retained for operator cleanup")
		}
		if err = owner.Close(cleanup); err != nil {
			t.Error("owner close failed")
		}
	})
	var storeRole string
	var unsafe bool
	e = owner.QueryRow(ctx, "SELECT rolname, rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls FROM pg_roles WHERE rolname=$1", storeCfg.User).Scan(&storeRole, &unsafe)
	if e != nil || unsafe {
		t.Fatal("store role absent or privileged; refusing integration")
	}
	var member bool
	if e = owner.QueryRow(ctx, "SELECT pg_has_role($1,$2,'MEMBER')", storeCfg.User, ownerCfg.User).Scan(&member); e != nil || member {
		t.Fatal("store must not inherit migration owner")
	}
	mustExec(t, owner, ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	mustExec(t, owner, ctx, "REVOKE ALL ON SCHEMA "+pgx.Identifier{schema}.Sanitize()+" FROM PUBLIC")
	mustExec(t, owner, ctx, "SET search_path TO "+pgx.Identifier{schema}.Sanitize()+",pg_catalog")
	for _, name := range migrations.Order {
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal("missing embedded migration")
		}
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal("migration begin failed")
		}
		_, err = tx.Exec(ctx, string(body))
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			rollbackTest(tx)
		}
		if err != nil {
			t.Fatalf("migration %s failed SQLSTATE=%s (redacted)", name, sqlState(err))
		}
	}
	qs, qr := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{storeRole}.Sanitize()
	mustExec(t, owner, ctx, "GRANT USAGE ON SCHEMA "+qs+" TO "+qr)
	mustExec(t, owner, ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA "+qs+" TO "+qr)
	mustExec(t, owner, ctx, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA "+qs+" TO "+qr)
	mustExec(t, owner, ctx, "REVOKE DELETE ON "+qs+".admission_reservations,"+qs+".callback_subscription_versions FROM "+qr)
	store, e := pgx.ConnectConfig(ctx, storeCfg)
	if e != nil {
		t.Fatal("store connect failed (redacted)")
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := store.Close(c); err != nil {
			t.Error("store close failed")
		}
	})
	mustExec(t, store, ctx, "SET search_path TO "+qs+",pg_catalog")
	var effective string
	if e = store.QueryRow(ctx, "SELECT current_user").Scan(&effective); e != nil || effective != storeRole {
		t.Fatal("unexpected effective store role")
	}
	f := &pgFixture{owner, store, schema, ctx}
	seedFixture(t, f)
	return f
}

type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func mustExec(t *testing.T, c execer, ctx context.Context, q string, args ...any) {
	t.Helper()
	if _, e := c.Exec(ctx, q, args...); e != nil {
		t.Fatalf("fixture SQL failed SQLSTATE=%s (redacted)", sqlState(e))
	}
}
func sqlState(e error) string {
	var pe *pgconn.PgError
	if errors.As(e, &pe) {
		return pe.Code
	}
	return "non_sql_error"
}
func seedFixture(t *testing.T, f *pgFixture) {
	mustExec(t, f.owner, f.ctx, `
INSERT INTO tenants VALUES('T1'),('T2');
INSERT INTO users(tenant,id,login,role,password) VALUES('T1','admin','admin','admin','!disabled'),('T1','operator','operator','operator','!disabled'),('T1','owner','owner','owner','!disabled');
INSERT INTO devices(tenant,id,name,key_id,secret,capabilities) VALUES('T1','A','A','keyA','!disabledA','[]'),('T1','B','B','keyB','!disabledB','[]');
INSERT INTO device_sessions(id,tenant,device,token,epoch,expires) VALUES('sessionA','T1','A','!sessionA',0,9000000000000),('sessionB','T1','B','!sessionB',0,9000000000000);
INSERT INTO runtime_instances(instance_id,roles,boot_id,recovery_epoch,state) VALUES('instanceA',ARRAY['gateway'],'bootA','epoch1','active');
INSERT INTO developer_apps(tenant,id,creator,name,token_hash,token_expires,scopes) VALUES('T1','X','admin','X','!disabledX',9000000000000,'[]'),('T1','Y','admin','Y','!disabledY',9000000000000,'[]');
INSERT INTO commands(tenant,id,device,actor,capability,digest,policy,confirm,status,created,expires,actor_type,app_epoch,app_id,subscription_snapshot,subscription_snapshot_digest,admission_version)
 VALUES('T1','commandA','A','admin','message.send_text','digest',1,false,'queued',1,9000000000000,'app',0,'X','[]',repeat('0',64),'bounded-result-v1'),('T1','commandB','B','admin','message.send_text','digest',1,false,'queued',1,9000000000000,'app',0,'X','[]',repeat('0',64),'bounded-result-v1'),('T1','commandY','A','admin','message.send_text','digest',1,false,'queued',1,9000000000000,'app',0,'Y','[]',repeat('0',64),'bounded-result-v1');
INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('T1','D1','X','synthetic:no-network',1,'opaque:not-installed',1,'active'),('T1','D2','X','synthetic:no-network',1,'opaque:not-installed',1,'active'),('T1','DY','Y','synthetic:no-network',1,'opaque:not-installed',1,'active');
INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts)
 VALUES('T1','X','S1',1,'D1',1,1,'command.result','bounded-result-v1',1,65536,4),('T1','X','S2',1,'D2',1,1,'command.result','bounded-result-v1',1,65536,4),('T1','Y','SY',1,'DY',1,1,'command.result','bounded-result-v1',1,65536,4);
INSERT INTO developer_callbacks(event,tenant,app,command,status,body,next_at) VALUES('eventA','T1','X','commandA','succeeded','{}',1),('eventA2','T1','X','commandA','failed','{}',1),('eventB','T1','X','commandB','succeeded','{}',1),('eventY','T1','Y','commandY','succeeded','{}',1);
`)
}
func reservation(t *testing.T, f *pgFixture, tx pgx.Tx) {
	mustExec(t, tx, f.ctx, `INSERT INTO admission_reservations(tenant,reservation_id,command,kind,app,subscription_id,subscription_version,destination_id,destination_policy_version,key_version,max_attempts,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES('T1','R','commandA','callback','X','S1',1,'D1',1,1,4,0,'held',1,90112)`)
}
func materialize(t *testing.T, f *pgFixture, tx pgx.Tx) {
	reservation(t, f, tx)
	mustExec(t, tx, f.ctx, `UPDATE admission_reservations SET state='materialized',delivery_id='deliveryA',event_id='eventA',materialized_at=clock_timestamp() WHERE reservation_id='R';
INSERT INTO callback_outbox(tenant,delivery_id,event_id,destination_id,app,command,device,app_epoch,device_epoch,body_bytes,payload_digest,policy_version,key_version,expires_at,reservation_id,reservation_kind,reservation_state,subscription_id,subscription_version,event_ordinal)
VALUES('T1','deliveryA','eventA','D1','X','commandA','A',0,0,decode('7b7d','hex'),repeat('0',64),1,1,clock_timestamp()+interval '1 hour','R','callback','materialized','S1',1,0)`)
}
func expectRejected(t *testing.T, f *pgFixture, setup func(pgx.Tx), q string, args ...any) error {
	t.Helper()
	tx, e := f.store.Begin(f.ctx)
	if e != nil {
		t.Fatal("begin failed")
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(c)
	}()
	if setup != nil {
		setup(tx)
	}
	_, e = tx.Exec(f.ctx, q, args...)
	if e == nil {
		e = tx.Commit(f.ctx)
	}
	if e == nil {
		t.Fatal("invalid graph committed")
	}
	s := sqlState(e)
	if s != "23503" && s != "23505" && s != "23514" && s != "42501" {
		t.Fatalf("unexpected SQLSTATE=%s", s)
	}
	return e
}
func TestPGIntegrationSchemaAndDeviceBindings(t *testing.T) {
	f := integrationFixture(t)
	var count int
	if e := f.owner.QueryRow(f.ctx, "SELECT count(*) FROM pg_tables WHERE schemaname=$1", f.schema).Scan(&count); e != nil || count != 28 {
		t.Fatalf("expected28 tables got %d", count)
	}
	var deferred int
	if e := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname=$1 AND c.conname IN ('delivery_reserved_identity','reservation_exact_delivery') AND c.contype='f' AND c.condeferrable AND c.condeferred`, f.schema).Scan(&deferred); e != nil || deferred != 2 {
		t.Fatal("precise bidirectional FKs must both be initially deferred")
	}
	for name, q := range map[string]string{
		"PG-GW-14_wrong_device_outbox":    `INSERT INTO outbox(tenant,device,command,kind) VALUES('T1','B','commandA','command')`,
		"PG-GW-15_wrong_session_inbox":    `INSERT INTO inbox(tenant,device,event,digest,session,seq) VALUES('T1','A','evt','hash','sessionB',1)`,
		"PG-GW-15_wrong_dispatch_session": `UPDATE commands SET dispatch_session='sessionB',dispatch_fence=1,dispatch_owner='instanceA',dispatch_recovery_epoch='epoch1' WHERE id='commandA'`,
		"PG-GW-15_wrong_gateway_session":  `INSERT INTO device_gateway_leases(tenant,device,owner_instance,session_id,recovery_epoch,fence,lease_until) VALUES('T1','A','instanceA','sessionB','epoch1',1,clock_timestamp()+interval '1 minute')`,
		"PG-GW-07_cross_tenant":           `INSERT INTO outbox(tenant,device,command,kind) VALUES('T2','A','commandA','command')`,
	} {
		t.Run(name, func(t *testing.T) { expectRejected(t, f, nil, q) })
	}
}
func TestPGIntegrationReservationBindings(t *testing.T) {
	f := integrationFixture(t)
	tests := []struct {
		name, q string
		setup   bool
	}{
		{"ADMISSION-11_wrong_command", `UPDATE callback_outbox SET command='commandB' WHERE delivery_id='deliveryA'`, true},
		{"ADMISSION-12_cross_app", `INSERT INTO admission_reservations(tenant,reservation_id,command,kind,app,subscription_id,subscription_version,destination_id,destination_policy_version,key_version,max_attempts,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES('T1','RY','commandA','callback','Y','SY',1,'DY',1,1,4,0,'held',1,90112)`, false},
		{"ADMISSION-13_wrong_destination", `INSERT INTO admission_reservations(tenant,reservation_id,command,kind,app,subscription_id,subscription_version,destination_id,destination_policy_version,key_version,max_attempts,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES('T1','RX','commandA','callback','X','S1',1,'D2',1,1,4,0,'held',1,90112)`, false},
		{"ADMISSION-14_double_use", `INSERT INTO callback_outbox SELECT tenant,'deliveryB','eventA2',destination_id,app,command,device,app_epoch,device_epoch,body_bytes,payload_digest,policy_version,key_version,state,available_at,max_attempts,attempt_count,expires_at,claimed_at,hard_deadline_at,lease_owner,lease_recovery_epoch,lease_until,fence,delivered_at,last_error_code,reservation_id,reservation_kind,reservation_state,subscription_id,subscription_version,event_ordinal FROM callback_outbox WHERE delivery_id='deliveryA'`, true},
		{"ADMISSION-16_policy_changed", `UPDATE admission_reservations SET key_version=2 WHERE reservation_id='R'`, true},
		{"ADMISSION-17_reverse_link_changed", `UPDATE admission_reservations SET delivery_id='other' WHERE reservation_id='R'`, true},
		{"ADMISSION-19_invalid_cancel_state", `UPDATE admission_reservations SET state='cancelled_retained',delivery_id=NULL,event_id=NULL WHERE reservation_id='R'`, true},
		{"ADMISSION-20_partial_gc_delete", `DELETE FROM callback_outbox WHERE delivery_id='deliveryA'`, true},
		{"ADMISSION-20_partial_gc_pointer", `UPDATE admission_reservations SET state='purged',delivery_id=NULL,event_id=NULL,released_at=clock_timestamp(),release_reason='gc' WHERE reservation_id='R'`, true},
		{"ADMISSION-21_wrong_event", `UPDATE callback_outbox SET event_id='eventB' WHERE delivery_id='deliveryA'`, true},
		{"ADMISSION-22_rebind", `UPDATE callback_outbox SET reservation_id='other' WHERE delivery_id='deliveryA'`, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			var setup func(pgx.Tx)
			if c.setup {
				setup = func(tx pgx.Tx) { materialize(t, f, tx) }
			}
			err := expectRejected(t, f, setup, c.q)
			if c.name == "ADMISSION-14_double_use" {
				var pe *pgconn.PgError
				if !errors.As(err, &pe) || pe.Code != "23505" || pe.ConstraintName != "delivery_one_reservation" {
					t.Fatal("double use must hit exact reservation unique constraint")
				}
			}
		})
	}
}
func TestPGIntegrationValidMaterializationAndGC(t *testing.T) {
	f := integrationFixture(t)
	tx, e := f.store.Begin(f.ctx)
	if e != nil {
		t.Fatal("begin failed")
	}
	materialize(t, f, tx)
	if e = tx.Commit(f.ctx); e != nil {
		t.Fatalf("valid deferred graph failed SQLSTATE=%s", sqlState(e))
	}
	// Lifecycle B: cancelled delivery retains its materialized reservation.
	mustExec(t, f.store, f.ctx, "UPDATE callback_outbox SET state='cancelled' WHERE delivery_id='deliveryA'")
	var state string
	if e = f.store.QueryRow(f.ctx, "SELECT state FROM admission_reservations WHERE reservation_id='R'").Scan(&state); e != nil {
		t.Fatal("reservation state query failed")
	}
	if state != "materialized" {
		t.Fatal("materialized cancellation lost reservation")
	}
	tx, e = f.store.Begin(f.ctx)
	if e != nil {
		t.Fatal("begin failed")
	}
	mustExec(t, tx, f.ctx, `DELETE FROM callback_outbox WHERE delivery_id='deliveryA'; UPDATE admission_reservations SET state='purged',delivery_id=NULL,event_id=NULL,released_at=clock_timestamp(),release_reason='synthetic_retention_eligible' WHERE reservation_id='R'`)
	if e = tx.Commit(f.ctx); e != nil {
		t.Fatalf("atomic graph GC failed SQLSTATE=%s", sqlState(e))
	}
	expectRejected(t, f, nil, `UPDATE admission_reservations SET state='held',released_at=NULL,release_reason=NULL WHERE reservation_id='R'`)
	expectRejected(t, f, nil, `DELETE FROM admission_reservations WHERE reservation_id='R'`)
}

const insertDelivery = `INSERT INTO callback_outbox(tenant,delivery_id,event_id,destination_id,app,command,device,app_epoch,device_epoch,body_bytes,payload_digest,policy_version,key_version,expires_at,reservation_id,reservation_kind,reservation_state,subscription_id,subscription_version,event_ordinal)
VALUES('T1','deliveryA',$1,$2,$3,$4,$5,0,0,decode('7b7d','hex'),repeat('0',64),1,1,clock_timestamp()+interval '1 hour','R','callback','materialized',$6,1,0)`

func TestPGIntegrationInitialInsertForeignKeys(t *testing.T) {
	f := integrationFixture(t)
	for _, tc := range []struct {
		name               string
		expectedConstraint string
		args               []any
	}{
		{"ADMISSION-11_initial_command", "delivery_reserved_identity", []any{"eventB", "D1", "X", "commandB", "B", "S1"}},
		{"ADMISSION-12_initial_app", "delivery_reserved_identity", []any{"eventY", "DY", "Y", "commandY", "A", "SY"}},
		{"ADMISSION-13_initial_destination", "delivery_reserved_identity", []any{"eventA", "D2", "X", "commandA", "A", "S2"}},
		{"ADMISSION-21_initial_event", "delivery_event_same_command", []any{"eventB", "D1", "X", "commandA", "A", "S1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, e := f.store.Begin(f.ctx)
			if e != nil {
				t.Fatal("begin failed")
			}
			defer rollbackTest(tx)
			reservation(t, f, tx)
			mustExec(t, tx, f.ctx, `UPDATE admission_reservations SET state='materialized',delivery_id='deliveryA',event_id=$1 WHERE reservation_id='R'`, tc.args[0])
			_, e = tx.Exec(f.ctx, insertDelivery, tc.args...)
			if e == nil {
				e = tx.Commit(f.ctx)
			}
			var pe *pgconn.PgError
			if !errors.As(e, &pe) || pe.Code != "23503" || pe.ConstraintName != tc.expectedConstraint {
				t.Fatalf("expected precise FK %s; received SQLSTATE=%s (details redacted)", tc.expectedConstraint, sqlState(e))
			}
		})
	}
	t.Run("ADMISSION-18_held_not_materialized", func(t *testing.T) {
		tx, e := f.store.Begin(f.ctx)
		if e != nil {
			t.Fatal("begin failed")
		}
		defer rollbackTest(tx)
		reservation(t, f, tx)
		_, e = tx.Exec(f.ctx, insertDelivery, "eventA", "D1", "X", "commandA", "A", "S1")
		if e == nil {
			e = tx.Commit(f.ctx)
		}
		var pe *pgconn.PgError
		if !errors.As(e, &pe) || pe.Code != "23503" || pe.ConstraintName != "delivery_reserved_identity" {
			t.Fatal("held reservation must fail precise materialized FK")
		}
	})
	t.Run("ADMISSION-15_result_not_callback", func(t *testing.T) {
		tx, e := f.store.Begin(f.ctx)
		if e != nil {
			t.Fatal("begin failed")
		}
		defer rollbackTest(tx)
		mustExec(t, tx, f.ctx, `INSERT INTO admission_reservations(tenant,reservation_id,command,kind,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES('T1','R','commandA','result',0,'held',1,196608)`)
		_, e = tx.Exec(f.ctx, insertDelivery, "eventA", "D1", "X", "commandA", "A", "S1")
		if e == nil {
			e = tx.Commit(f.ctx)
		}
		var pe *pgconn.PgError
		if !errors.As(e, &pe) || pe.Code != "23503" || pe.ConstraintName != "delivery_reserved_identity" {
			t.Fatal("result reservation must not fund callback")
		}
	})
}
func rollbackTest(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
