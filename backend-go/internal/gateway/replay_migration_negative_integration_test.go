package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Source-only until explicitly executed in isolated CI. Called by existing GW10;
// no additional top-level PG group. Negative cases roll back their changes;
// the fresh migration/guard positive fixture is committed then schema-cleaned.
func replayMigrationNegativeCases(t *testing.T) {
	t.Helper()
	gatewayGate(t) // Must precede fixtures, connections, and schema work.
	t.Run("replay_migration_negative_matrix", func(t *testing.T) {
		b, e := migrations.Files.ReadFile("0010_callback_replay_receipts.sql")
		if e != nil {
			t.Fatal("replay migration missing")
		}
		migration := string(b)
		type historyCase struct{ name, sql, want string }
		cases := []historyCase{
			{"nonnull_result", `UPDATE commands SET result='{}'`, ""},
			{"cleared_result_ack", `INSERT INTO outbox(tenant,device,command,kind) VALUES('tenant_guard','device_guard','command_guard','result_ack')`, ""},
			{"cleared_result_tombstone", `INSERT INTO tombstones VALUES('tenant_guard','device_guard','command_guard',repeat('0',64),'succeeded',1)`, ""},
			{"cleared_result_callback", `INSERT INTO developer_callbacks(event,tenant,app,command,status,body,next_at) VALUES('event_guard','tenant_guard','app_guard','command_guard','succeeded','{}',0)`, ""},
			{"cleared_result_inbox", `INSERT INTO inbox(tenant,device,event,digest,session,seq,reply) VALUES('tenant_guard','device_guard','event_guard',repeat('0',64),'session_guard',1,'{"kind":"result_ack","body":{"commandId":"command_guard"}}')`, ""},
			{"public_schema", `SET LOCAL search_path TO public,pg_catalog`, "REPLAY_RECEIPT_SCHEMA_GUARD"},
			{"catalog_schema", `SET LOCAL search_path TO pg_catalog`, "REPLAY_RECEIPT_SCHEMA_GUARD"},
			{"empty_current_schema", `SET LOCAL search_path TO wt_replay_intentionally_absent_schema; DO $$ BEGIN IF current_schema() IS NOT NULL THEN RAISE EXCEPTION 'REPLAY_EMPTY_SCHEMA_FIXTURE_INVALID'; END IF; END $$`, "REPLAY_RECEIPT_SCHEMA_GUARD"},
			{"missing_relation", `ALTER TABLE audit RENAME TO replay_missing_audit`, "REPLAY_RECEIPT_RELATION_GUARD"},
			{"public_create_permission", `GRANT CREATE ON SCHEMA REPLAY_TEST_SCHEMA TO PUBLIC`, "REPLAY_RECEIPT_SCHEMA_GUARD"},
		}
		for _, status := range []string{"succeeded", "partially_succeeded", "failed", "cancelled", "expired", "rejected", "execution_unknown"} {
			cases = append(cases, historyCase{"status_" + status, `UPDATE commands SET status='` + status + `'`, ""})
		}
		for _, action := range []string{"command.result", "command.result_conflict"} {
			cases = append(cases, historyCase{"audit_" + action, `INSERT INTO audit VALUES('audit_guard','tenant_guard','device_guard','` + action + `','command_guard','synthetic',0)`, ""})
		}
		for _, event := range []string{"result_success", "result_partial", "result_failure_no_effect", "result_cancel_no_effect", "lost_execution_evidence", "device_reject_before_start", "device_proves_no_effect_cancel", "device_proves_unstarted_expiry", "reconcile_applied", "reconcile_partial", "reconcile_not_applied", "reconcile_unstarted_rejected", "reconcile_unstarted_cancelled", "reconcile_unstarted_expired"} {
			cases = append(cases, historyCase{"event_" + event, `INSERT INTO command_events VALUES('tenant_guard','command_guard','event_guard','fixture','` + event + `','fixture',0)`, ""})
		}
		for _, col := range []string{"source", "target"} {
			for _, status := range []string{"succeeded", "partially_succeeded", "failed", "cancelled", "expired", "rejected", "execution_unknown"} {
				cases = append(cases, historyCase{col + "_" + status, `INSERT INTO command_events VALUES('tenant_guard','command_guard','event_guard','fixture','fixture','fixture',0); UPDATE command_events SET ` + col + `='` + status + `'`, ""})
			}
		}
		for _, kind := range []string{"result", "callback"} {
			for _, state := range []string{"materialized", "purged", "cancelled_retained"} {
				prefix := `INSERT INTO admission_reservations(tenant,reservation_id,command,kind,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES('tenant_guard','reservation_guard','command_guard','result',0,'held',1,196608);`
				if kind == "callback" {
					prefix = replayNegativeCallbackConfig + `INSERT INTO admission_reservations(tenant,reservation_id,command,kind,app,subscription_id,subscription_version,destination_id,destination_policy_version,key_version,max_attempts,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES('tenant_guard','reservation_guard','command_guard','callback','app_guard','sub_guard',1,'dest_guard',1,1,4,0,'held',1,90112);`
				}
				update := `UPDATE admission_reservations SET state='cancelled_retained';`
				if state == "materialized" {
					update = `UPDATE admission_reservations SET state='materialized'`
					if kind == "callback" {
						update += `,delivery_id='delivery_guard',event_id='event_guard'`
					}
					update += `;` // Existing exact-delivery FK is deferred; never commit an orphan.
				} else if state == "purged" {
					update += `UPDATE admission_reservations SET state='purged',released_at=clock_timestamp(),release_reason='synthetic';`
				}
				cases = append(cases, historyCase{kind + "_reservation_" + state, prefix + update, ""})
			}
		}
		// An independent outbox marker is impossible in a valid committed graph.
		// Defer its FKs in this disposable transaction only, leave parents absent,
		// verify this guard independently, and roll back without committing the orphan.
		cases = append(cases, historyCase{"isolated_corrupt_callback_outbox", `DO $$ DECLARE c record; BEGIN FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='callback_outbox'::regclass AND contype='f' LOOP EXECUTE format('ALTER TABLE callback_outbox ALTER CONSTRAINT %I DEFERRABLE INITIALLY DEFERRED',c.conname); END LOOP; END $$;
INSERT INTO callback_outbox(tenant,delivery_id,event_id,destination_id,app,command,device,app_epoch,device_epoch,body_bytes,payload_digest,policy_version,key_version,expires_at,reservation_id,reservation_kind,reservation_state,subscription_id,subscription_version,event_ordinal) VALUES('tenant_guard','delivery_guard','event_guard','dest_guard','app_guard','command_guard','device_guard',0,0,'{}',repeat('0',64),1,1,clock_timestamp()+interval '1 hour','reservation_guard','callback','materialized','sub_guard',1,0)`, ""})
		for _, relation := range []string{"commands", "admission_reservations", "outbox", "tombstones", "command_events", "inbox", "developer_callbacks", "callback_outbox", "audit"} {
			cases = append(cases, historyCase{"temp_shadow_" + relation, `CREATE TEMP TABLE ` + pgx.Identifier{relation}.Sanitize() + `(shadow integer) ON COMMIT DROP`, "REPLAY_RECEIPT_RELATION_GUARD"})
		}
		// Bound each fixture's work without extending its existing 30-second context.
		for start := 0; start < len(cases); start += 12 {
			end := min(start+12, len(cases))
			t.Run(fmt.Sprintf("history_batch_%02d", start/12), func(t *testing.T) {
				f := newGW(t)
				schema := replayNegativeShadow(t, f)
				for _, tc := range cases[start:end] {
					t.Run(tc.name, func(t *testing.T) {
						tx := resultGuardTx(t, f, schema)
						defer resultGuardRollback(t, tx)
						// Explicitly set then clear old result; each remaining source stands alone.
						execOwner(t, tx, f.ctx, `UPDATE commands SET result='{}'; UPDATE commands SET result=NULL`)
						execOwner(t, tx, f.ctx, strings.ReplaceAll(tc.sql, "REPLAY_TEST_SCHEMA", pgx.Identifier{schema}.Sanitize()))
						before := replayNegativeSnapshot(t, f, tx, schema)
						execOwner(t, tx, f.ctx, `SAVEPOINT replay_negative`)
						_, err := tx.Exec(f.ctx, migration)
						want := tc.want
						if want == "" {
							want = "REPLAY_RECEIPT_HISTORY_REQUIRES_RECONCILIATION"
						}
						replayNegativeError(t, err, "P0001", want)
						execOwner(t, tx, f.ctx, `ROLLBACK TO SAVEPOINT replay_negative`)
						if replayNegativeSnapshot(t, f, tx, schema) != before {
							t.Fatal("rejected migration changed synthetic rows or catalog")
						}
					})
				}
			})
		}
		t.Run("non_owner_schema", func(t *testing.T) {
			f := newGW(t)
			snapshot := func() string {
				ownerTx := resultGuardTx(t, f, f.schema)
				defer resultGuardRollback(t, ownerTx)
				return replayNegativeSnapshot(t, f, ownerTx, f.schema)
			}
			before := snapshot()
			tx, e := f.pool.Begin(f.ctx)
			if e != nil {
				t.Fatal("nonowner transaction begin")
			}
			defer resultGuardRollback(t, tx)
			execOwner(t, tx, f.ctx, "SET LOCAL search_path TO "+pgx.Identifier{f.schema}.Sanitize()+",pg_catalog")
			var owner bool
			e = tx.QueryRow(f.ctx, `SELECT n.nspowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user) FROM pg_catalog.pg_namespace n WHERE n.nspname=current_schema()`).Scan(&owner)
			if e != nil || owner {
				t.Fatal("nonowner fixture identity not established")
			}
			_, e = tx.Exec(f.ctx, migration)
			resultGuardRollback(t, tx)
			replayNegativeError(t, e, "P0001", "REPLAY_RECEIPT_SCHEMA_GUARD")
			if snapshot() != before {
				t.Fatal("nonowner migration changed synthetic rows or catalog")
			}
		})
		t.Run("fresh_migration_and_receipt_guards", func(t *testing.T) {
			f := newGW(t)
			schema := replayNegativeShadow(t, f)
			tx := resultGuardTx(t, f, schema)
			defer resultGuardRollback(t, tx)
			execOwner(t, tx, f.ctx, migration)
			var count int
			if e := tx.QueryRow(f.ctx, `SELECT count(*) FROM callback_replay_receipts`).Scan(&count); e != nil || count != 0 {
				t.Fatal("fresh receipt relation missing or nonempty")
			}
			execOwner(t, tx, f.ctx, `UPDATE commands SET result='{}',status='succeeded',effect='applied',attempt='attempt_guard'`)
			replayNegativeReceiptCases(t, f, tx, schema)
			if e := tx.Commit(f.ctx); e != nil {
				t.Fatalf("fresh replay migration/guard fixture commit SQLSTATE=%s", stateCode(e))
			}
		})
	})
}

const replayNegativeCallbackConfig = `INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('tenant_guard','dest_guard','app_guard','https://callbacks.example.com/result',1,'key_guard',1,'active');
INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('tenant_guard','app_guard','sub_guard',1,'dest_guard',1,1,'command.result','bounded-result-v1',1,65536,4);`

func replayNegativeError(t *testing.T, e error, code, message string) {
	t.Helper()
	var pe *pgconn.PgError
	if !errors.As(e, &pe) || pe.Code != code || (message != "" && pe.Message != message) {
		t.Fatalf("wrong SQL rejection: SQLSTATE=%s expected=%s/%s", stateCode(e), code, message)
	}
}

func replayNegativeShadow(t *testing.T, f *gwFixture) string {
	t.Helper()
	schema := "wt_replay_guard_" + randHex(t, 12)
	qs := pgx.Identifier{schema}.Sanitize()
	tx, e := f.owner.Begin(f.ctx)
	if e != nil {
		t.Fatal("replay shadow begin")
	}
	defer resultGuardRollback(t, tx)
	execOwner(t, tx, f.ctx, "CREATE SCHEMA "+qs+"; REVOKE ALL ON SCHEMA "+qs+" FROM PUBLIC; SET LOCAL search_path TO "+qs+",pg_catalog")
	if len(migrations.Order) < 10 || migrations.Order[9] != "0010_callback_replay_receipts.sql" {
		t.Fatal("replay migration boundary changed")
	}
	for _, name := range migrations.Order[:9] {
		b, e := migrations.Files.ReadFile(name)
		if e != nil {
			t.Fatal("replay shadow migration missing")
		}
		execOwner(t, tx, f.ctx, string(b))
	}
	execOwner(t, tx, f.ctx, `INSERT INTO tenants VALUES('tenant_guard');
INSERT INTO users(tenant,id,login,role,password) VALUES('tenant_guard','operator_guard','operator_guard','operator','!disabled');
INSERT INTO devices(tenant,id,name,key_id,secret,capabilities) VALUES('tenant_guard','device_guard','synthetic','key_guard','synthetic_not_a_live_credential','["message.send_text"]');
INSERT INTO device_sessions(id,tenant,device,token,epoch,expires,revoked) VALUES('session_guard','tenant_guard','device_guard','synthetic_session_not_a_credential',0,1,true);
INSERT INTO developer_apps(tenant,id,creator,name,token_hash,token_expires,scopes) VALUES('tenant_guard','app_guard','operator_guard','synthetic',repeat('0',64),9000000000000,'["commands:write"]');
INSERT INTO commands(tenant,id,device,actor,capability,payload,digest,policy,confirm,status,created,expires,actor_type,app_id,app_epoch,subscription_snapshot,subscription_snapshot_digest,admission_version) VALUES('tenant_guard','command_guard','device_guard','operator_guard','message.send_text','{}',repeat('0',64),1,false,'queued',0,30000,'app','app_guard',0,'[]',repeat('0',64),'bounded-result-v1')`)
	if e := tx.Commit(f.ctx); e != nil {
		t.Fatalf("replay shadow commit SQLSTATE=%s", stateCode(e))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := f.owner.Exec(ctx, "DROP SCHEMA IF EXISTS "+qs+" CASCADE"); e != nil {
			t.Error("replay shadow cleanup failed")
		}
	})
	return schema
}

// Compare every base-table row plus schema ACL, columns, constraints, triggers,
// and function definitions. Never print snapshot contents or credentials.
func replayNegativeSnapshot(t *testing.T, f *gwFixture, tx pgx.Tx, schema string) string {
	t.Helper()
	var tables []string
	rows, e := tx.Query(f.ctx, `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r' ORDER BY c.relname`, schema)
	if e != nil {
		t.Fatal("replay snapshot relations")
	}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			rows.Close()
			t.Fatal("replay snapshot scan")
		}
		tables = append(tables, name)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		t.Fatal("replay snapshot iteration")
	}
	// One aggregate query for all relation data rather than one round trip per
	// table. Identifiers come from the catalog and are independently quoted.
	projections := make([]string, 0, len(tables))
	for _, table := range tables {
		label := "'" + strings.ReplaceAll(table, "'", "''") + "'"
		projections = append(projections, label+",(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]'::jsonb) FROM "+pgx.Identifier{schema, table}.Sanitize()+" x)")
	}
	var data string
	e = tx.QueryRow(f.ctx, "SELECT jsonb_build_object("+strings.Join(projections, ",")+")::text").Scan(&data)
	if e != nil {
		t.Fatalf("replay aggregate snapshot SQLSTATE=%s", stateCode(e))
	}
	var catalog string
	e = tx.QueryRow(f.ctx, `SELECT jsonb_build_object(
 'schema',(SELECT to_jsonb(n) FROM pg_catalog.pg_namespace n WHERE n.nspname=$1),
 'columns',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.attrelid,a.attnum) FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1),
 'constraints',(SELECT jsonb_agg(to_jsonb(k) ORDER BY k.oid) FROM pg_catalog.pg_constraint k JOIN pg_catalog.pg_namespace n ON n.oid=k.connamespace WHERE n.nspname=$1),
 'triggers',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.oid) FROM pg_catalog.pg_trigger g JOIN pg_catalog.pg_class c ON c.oid=g.tgrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1),
 'functions',(SELECT jsonb_agg(pg_catalog.pg_get_functiondef(p.oid) ORDER BY p.oid) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1))::text`, schema).Scan(&catalog)
	if e != nil {
		t.Fatalf("replay catalog snapshot SQLSTATE=%s", stateCode(e))
	}
	return data + "\n" + catalog
}

func replayNegativeReceiptCases(t *testing.T, f *gwFixture, tx pgx.Tx, schema string) {
	t.Helper()
	digest := sha256.Sum256([]byte("{}"))
	fields := map[string]any{"schema": "callback-result-replay-v1", "tenant": "tenant_guard", "device": "device_guard", "command": "command_guard", "app": "app_guard", "appEpoch": 0, "attempt": "attempt_guard", "commandDigest": strings.Repeat("0", 64), "resultDigest": hex.EncodeToString(digest[:]), "status": "succeeded", "effect": "applied"}
	encode := func(m map[string]any) string {
		b, e := json.Marshal(m)
		if e != nil {
			t.Fatal("synthetic receipt encoding")
		}
		return string(b)
	}
	copyFields := func() map[string]any {
		m := map[string]any{}
		for k, v := range fields {
			m[k] = v
		}
		return m
	}
	valid := encode(fields)
	type receiptCase struct {
		name          string
		value         any
		code, message string
	}
	cases := []receiptCase{
		{"sql_null", nil, "23514", "REPLAY_RECEIPT_BOUNDS"},
		{"malformed", "{", "22P02", ""},
		{"json_null", "null", "23514", "REPLAY_RECEIPT_OBJECT"},
		{"array", "[]", "23514", "REPLAY_RECEIPT_OBJECT"},
		{"string", `"x"`, "23514", "REPLAY_RECEIPT_OBJECT"},
		{"number", "1", "23514", "REPLAY_RECEIPT_OBJECT"},
		{"boolean", "true", "23514", "REPLAY_RECEIPT_OBJECT"},
		{"duplicate_field", `{"schema":"x",` + valid[1:], "23514", "REPLAY_RECEIPT_DUPLICATE_FIELD"},
		{"epoch_decimal", strings.Replace(valid, `"appEpoch":0`, `"appEpoch":0.0`, 1), "23514", "REPLAY_RECEIPT_COMMAND_BINDING"},
		{"epoch_exponent", strings.Replace(valid, `"appEpoch":0`, `"appEpoch":0e0`, 1), "23514", "REPLAY_RECEIPT_COMMAND_BINDING"},
	}
	for _, key := range []string{"schema", "tenant", "device", "command", "app", "appEpoch", "attempt", "commandDigest", "resultDigest", "status", "effect"} {
		m := copyFields()
		delete(m, key)
		cases = append(cases, receiptCase{"missing_" + key, encode(m), "23514", "REPLAY_RECEIPT_FIELD_TYPE"})
		m = copyFields()
		m[key] = nil
		cases = append(cases, receiptCase{"null_" + key, encode(m), "23514", "REPLAY_RECEIPT_FIELD_TYPE"})
		m = copyFields()
		m[key] = false
		cases = append(cases, receiptCase{"wrong_type_" + key, encode(m), "23514", "REPLAY_RECEIPT_FIELD_TYPE"})
		m = copyFields()
		if key == "appEpoch" {
			m[key] = 1
		} else {
			m[key] = "mismatch"
		}
		cases = append(cases, receiptCase{"mismatch_" + key, encode(m), "23514", "REPLAY_RECEIPT_COMMAND_BINDING"})
	}
	// SQL intentionally validates only the documented top-level contract. A pad
	// field tests storage byte bounds, not validity under the Go replay schema.
	padded := func(size int, multibyte bool) string {
		m := copyFields()
		m["pad"] = ""
		base := encode(m)
		n := size - len(base)
		if multibyte {
			m["pad"] = strings.Repeat("界", n/3) + strings.Repeat("x", n%3)
		} else {
			m["pad"] = strings.Repeat("x", n)
		}
		value := encode(m)
		if len(value) != size {
			t.Fatal("incorrect synthetic byte boundary")
		}
		return value
	}
	cases = append(cases, receiptCase{"oversize_ascii", padded(16385, false), "23514", "REPLAY_RECEIPT_BOUNDS"}, receiptCase{"oversize_utf8", padded(16385, true), "23514", "REPLAY_RECEIPT_BOUNDS"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replayNegativeStatement(t, f, tx, schema, `INSERT INTO callback_replay_receipts VALUES('tenant_guard','command_guard',$1)`, []any{tc.value}, tc.code, tc.message)
		})
	}
	for _, tc := range []struct{ name, value string }{{"valid_binding", valid}, {"exact_byte_limit_ascii", padded(16384, false)}, {"exact_byte_limit_utf8", padded(16384, true)}} {
		t.Run(tc.name, func(t *testing.T) {
			before := replayNegativeSnapshot(t, f, tx, schema)
			execOwner(t, tx, f.ctx, `SAVEPOINT replay_positive`)
			execOwner(t, tx, f.ctx, `INSERT INTO callback_replay_receipts VALUES('tenant_guard','command_guard',$1)`, tc.value)
			var stored string
			if e := tx.QueryRow(f.ctx, `SELECT receipt FROM callback_replay_receipts`).Scan(&stored); e != nil || stored != tc.value {
				t.Fatal("accepted receipt bytes changed")
			}
			execOwner(t, tx, f.ctx, `ROLLBACK TO SAVEPOINT replay_positive`)
			if replayNegativeSnapshot(t, f, tx, schema) != before {
				t.Fatal("positive-case rollback changed rows or catalog")
			}
		})
	}
	t.Run("missing_command", func(t *testing.T) {
		replayNegativeStatement(t, f, tx, schema, `INSERT INTO callback_replay_receipts VALUES('tenant_guard','missing',$1)`, []any{valid}, "23514", "REPLAY_RECEIPT_COMMAND_BINDING")
	})
	// A separate unfinished command proves that first-result absence is checked.
	execOwner(t, tx, f.ctx, `INSERT INTO commands(tenant,id,device,actor,capability,payload,digest,policy,confirm,status,created,expires,actor_type,app_id,app_epoch,attempt,subscription_snapshot,subscription_snapshot_digest,admission_version) SELECT tenant,'unfinished_guard',device,actor,capability,payload,digest,policy,confirm,'running',created,expires,actor_type,app_id,app_epoch,attempt,subscription_snapshot,subscription_snapshot_digest,admission_version FROM commands WHERE id='command_guard'`)
	m := copyFields()
	m["command"] = "unfinished_guard"
	m["status"] = "running"
	m["effect"] = "not_started"
	t.Run("missing_result", func(t *testing.T) {
		replayNegativeStatement(t, f, tx, schema, `INSERT INTO callback_replay_receipts VALUES('tenant_guard','unfinished_guard',$1)`, []any{encode(m)}, "23514", "REPLAY_RECEIPT_COMMAND_BINDING")
	})
	execOwner(t, tx, f.ctx, `INSERT INTO callback_replay_receipts VALUES('tenant_guard','command_guard',$1)`, valid)
	for _, tc := range []struct{ name, sql, message string }{
		{"receipt_noop", `UPDATE callback_replay_receipts SET receipt=receipt`, "REPLAY_RECEIPT_IMMUTABLE"},
		{"receipt_rewrite", `UPDATE callback_replay_receipts SET receipt='{}'`, "REPLAY_RECEIPT_IMMUTABLE"},
		{"receipt_delete", `DELETE FROM callback_replay_receipts`, "REPLAY_RECEIPT_IMMUTABLE"},
		{"result_clear", `UPDATE commands SET result=NULL WHERE id='command_guard'`, "IMMUTABLE_RESULT_IDENTITY"},
		{"result_replace", `UPDATE commands SET result='{"changed":true}' WHERE id='command_guard'`, "IMMUTABLE_RESULT_IDENTITY"},
		{"result_command_delete", `DELETE FROM commands WHERE id='command_guard'`, "RESULT_IDENTITY_RETAINED"},
		{"attempt_change", `UPDATE commands SET attempt='other' WHERE id='command_guard'`, "IMMUTABLE_RESULT_IDENTITY"},
		{"status_change", `UPDATE commands SET status='failed' WHERE id='command_guard'`, "IMMUTABLE_RESULT_IDENTITY"},
		{"effect_change", `UPDATE commands SET effect='not_applied' WHERE id='command_guard'`, "IMMUTABLE_RESULT_IDENTITY"},
		{"device_change", `UPDATE commands SET device='missing' WHERE id='command_guard'`, "IMMUTABLE_RESULT_IDENTITY"},
		{"app_epoch_change", `UPDATE commands SET app_epoch=1 WHERE id='command_guard'`, "IMMUTABLE_RESULT_IDENTITY"},
		{"dispatch_session_change", `UPDATE commands SET dispatch_session='session_guard' WHERE id='command_guard'`, "IMMUTABLE_RESULT_IDENTITY"},
	} {
		t.Run(tc.name, func(t *testing.T) { replayNegativeStatement(t, f, tx, schema, tc.sql, nil, "23514", tc.message) })
	}
	t.Run("duplicate_receipt", func(t *testing.T) {
		replayNegativeStatement(t, f, tx, schema, `INSERT INTO callback_replay_receipts VALUES('tenant_guard','command_guard',$1)`, []any{valid}, "23505", "")
	})
	execOwner(t, tx, f.ctx, `UPDATE commands SET cancel_requested=true,command_version=command_version+1 WHERE id='command_guard'`)
}

func replayNegativeStatement(t *testing.T, f *gwFixture, tx pgx.Tx, schema, sql string, args []any, code, message string) {
	t.Helper()
	before := replayNegativeSnapshot(t, f, tx, schema)
	execOwner(t, tx, f.ctx, `SAVEPOINT replay_statement`)
	_, e := tx.Exec(f.ctx, sql, args...)
	// Restore the transaction even if the assertion fails, so parent cleanup works.
	_, rollbackErr := tx.Exec(f.ctx, `ROLLBACK TO SAVEPOINT replay_statement`)
	if rollbackErr != nil {
		t.Fatalf("replay savepoint rollback SQLSTATE=%s", stateCode(rollbackErr))
	}
	replayNegativeError(t, e, code, message)
	if replayNegativeSnapshot(t, f, tx, schema) != before {
		t.Fatal("rejected SQL changed synthetic rows or catalog")
	}
}
