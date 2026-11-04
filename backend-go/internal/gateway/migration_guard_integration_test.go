package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Called by the already CI-gated GW10, not an additional top-level PG test.
// newGW also enforces the gate itself. Local compilation executes no fixture.
func migrationResultGuardCases(t *testing.T) {
	t.Helper()
	t.Run("migration_0008_guards", func(t *testing.T) {
		f := newGW(t)
		cases := []struct{ name, mutation, want string }{
			{"old_check_name_drift", `ALTER TABLE commands RENAME CONSTRAINT commands_result_check TO commands_result_renamed`, "RESULT_CHECK_GUARD_DEFINITION"},
			{"old_check_definition_drift", `ALTER TABLE commands DROP CONSTRAINT commands_result_check; ALTER TABLE commands ADD CONSTRAINT commands_result_check CHECK(result IS NULL OR jsonb_typeof(result::jsonb) IN ('object','array'))`, "RESULT_CHECK_GUARD_DEFINITION"},
			{"old_check_missing", `ALTER TABLE commands DROP CONSTRAINT commands_result_check`, "RESULT_CHECK_GUARD_COUNT"},
			{"result_column_type_drift", `ALTER TABLE commands ALTER COLUMN result TYPE varchar USING result::varchar`, "RESULT_CHECK_GUARD_TYPE"},
			// Expiry does not substitute for explicit revocation at the migration gate.
			{"expired_but_unrevoked_session", `UPDATE device_sessions SET revoked=false,expires=0 WHERE id='session_guard'`, "GATEWAY_EXISTING_SESSION_REQUIRES_REVOCATION"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				schema := resultGuardShadow(t, f)
				tx := resultGuardTx(t, f, schema)
				execOwner(t, tx, f.ctx, tc.mutation)
				if e := tx.Commit(f.ctx); e != nil {
					t.Fatalf("guard mutation commit SQLSTATE=%s", stateCode(e))
				}
				before := resultGuardSnapshot(t, f, schema)
				tx = resultGuardTx(t, f, schema)
				b, e := migrations.Files.ReadFile("0008_gateway_session_result.sql")
				if e != nil {
					t.Fatal("migration missing")
				}
				_, e = tx.Exec(f.ctx, string(b))
				var pgerr *pgconn.PgError
				if !errors.As(e, &pgerr) || pgerr.Code != "P0001" || pgerr.Message != tc.want {
					resultGuardRollback(t, tx)
					t.Fatalf("wrong migration guard: SQLSTATE=%s expected=%s", stateCode(e), tc.want)
				}
				resultGuardRollback(t, tx)
				if after := resultGuardSnapshot(t, f, schema); after != before {
					t.Fatal("failed migration changed catalog or synthetic row graph")
				}
			})
		}
		t.Run("revoked_session_and_result_constraint", func(t *testing.T) {
			schema := resultGuardShadow(t, f)
			tx := resultGuardTx(t, f, schema)
			b, e := migrations.Files.ReadFile("0008_gateway_session_result.sql")
			if e != nil {
				t.Fatal("migration missing")
			}
			execOwner(t, tx, f.ctx, string(b))
			if e = tx.Commit(f.ctx); e != nil {
				t.Fatalf("valid migration commit SQLSTATE=%s", stateCode(e))
			}
			tx = resultGuardTx(t, f, schema)
			var safe bool
			e = tx.QueryRow(f.ctx, `SELECT revoked AND NOT ws_consumed AND ws_connection_id IS NULL AND ws_recv_seq=0 FROM device_sessions WHERE id='session_guard'`).Scan(&safe)
			resultGuardRollback(t, tx)
			if e != nil || !safe {
				t.Fatal("revoked session migration defaults changed identity/authority")
			}
			for _, bad := range []struct{ name, value, code string }{
				{"malformed_json", "{", "22P02"},
				{"json_null", "null", "23514"},
				{"json_array", "[]", "23514"},
				{"json_string", `"synthetic"`, "23514"},
				{"json_number", "42", "23514"},
				{"json_boolean", "true", "23514"},
			} {
				t.Run(bad.name, func(t *testing.T) {
					before := resultGuardSnapshot(t, f, schema)
					tx := resultGuardTx(t, f, schema)
					_, e := tx.Exec(f.ctx, `UPDATE commands SET result=$1 WHERE tenant='tenant_guard' AND id='command_guard'`, bad.value)
					var pgerr *pgconn.PgError
					validFailure := errors.As(e, &pgerr) && pgerr.Code == bad.code
					if validFailure && bad.code == "23514" {
						validFailure = pgerr.ConstraintName == "commands_result_json_object"
					}
					resultGuardRollback(t, tx)
					if !validFailure {
						t.Fatalf("wrong result rejection SQLSTATE=%s", stateCode(e))
					}
					if after := resultGuardSnapshot(t, f, schema); after != before {
						t.Fatal("rejected result altered synthetic row graph")
					}
				})
			}
			nulObject := `{"code":"SYNTHETIC\u0000"}`
			emptyObject := `{}`
			for _, good := range []struct {
				name  string
				value *string
			}{{"sql_null", nil}, {"escaped_nul_object", &nulObject}, {"empty_object", &emptyObject}} {
				t.Run(good.name, func(t *testing.T) {
					tx := resultGuardTx(t, f, schema)
					var value any
					if good.value != nil {
						value = *good.value
					}
					tag, e := tx.Exec(f.ctx, `UPDATE commands SET result=$1 WHERE tenant='tenant_guard' AND id='command_guard'`, value)
					if e != nil || tag.RowsAffected() != 1 {
						resultGuardRollback(t, tx)
						t.Fatalf("allowed result rejected SQLSTATE=%s", stateCode(e))
					}
					if e = tx.Commit(f.ctx); e != nil {
						t.Fatalf("allowed result commit SQLSTATE=%s", stateCode(e))
					}
					tx = resultGuardTx(t, f, schema)
					var stored *string
					e = tx.QueryRow(f.ctx, `SELECT result FROM commands WHERE tenant='tenant_guard' AND id='command_guard'`).Scan(&stored)
					resultGuardRollback(t, tx)
					if e != nil || (stored == nil) != (good.value == nil) || (stored != nil && *stored != *good.value) {
						t.Fatal("allowed result not preserved exactly")
					}
				})
			}
		})
	})
}

// An isolated pre-0008 schema uses the actual first seven embedded migrations.
// Rows are inert SQL fixtures for constraints, not an authorization/admission test.
func resultGuardShadow(t *testing.T, f *gwFixture) string {
	t.Helper()
	schema := "wt_guard_" + randHex(t, 12)
	qs := pgx.Identifier{schema}.Sanitize()
	tx, e := f.owner.Begin(f.ctx)
	if e != nil {
		t.Fatal("shadow begin")
	}
	defer resultGuardRollback(t, tx)
	execOwner(t, tx, f.ctx, "CREATE SCHEMA "+qs)
	execOwner(t, tx, f.ctx, "REVOKE ALL ON SCHEMA "+qs+" FROM PUBLIC")
	execOwner(t, tx, f.ctx, "SET LOCAL search_path TO "+qs+",pg_catalog")
	firstSeven := []string{"0001_base19.sql", "0002_control7.sql", "0003_admission2.sql", "0004_admission_constraints.sql", "0005_indexes_roles.sql", "0006_commands_payload_json.sql", "0007_gateway_lease_connection.sql"}
	if len(migrations.Order) < 8 || migrations.Order[7] != "0008_gateway_session_result.sql" {
		t.Fatal("guard fixture migration boundary changed")
	}
	for i, name := range firstSeven {
		if migrations.Order[i] != name {
			t.Fatal("guard fixture migration order changed")
		}
		b, e := migrations.Files.ReadFile(name)
		if e != nil {
			t.Fatal("shadow migration missing")
		}
		execOwner(t, tx, f.ctx, string(b))
	}
	execOwner(t, tx, f.ctx, `INSERT INTO tenants VALUES('tenant_guard');
 INSERT INTO users(tenant,id,login,role,password) VALUES('tenant_guard','operator_guard','operator_guard','operator','!disabled');
 INSERT INTO devices(tenant,id,name,key_id,secret,capabilities) VALUES('tenant_guard','device_guard','synthetic','key_guard','synthetic_not_a_live_credential','["message.send_text"]');
 INSERT INTO device_sessions(id,tenant,device,token,epoch,expires,revoked) VALUES('session_guard','tenant_guard','device_guard','synthetic_session_not_a_credential',0,1,true);
 INSERT INTO commands(tenant,id,device,actor,capability,payload,digest,policy,confirm,status,created,expires,result,subscription_snapshot,subscription_snapshot_digest,admission_version)
 VALUES('tenant_guard','command_guard','device_guard','operator_guard','message.send_text',NULL,repeat('0',64),1,false,'queued',0,9000000000000,'{"before":true}','[]',repeat('0',64),'bounded-result-v1');
 INSERT INTO command_events(tenant,command,event_id,source,event,target,created) VALUES('tenant_guard','command_guard','event_guard','fixture','created','queued',0);
 INSERT INTO outbox(tenant,device,command,kind) VALUES('tenant_guard','device_guard','command_guard','command')`)
	if e = tx.Commit(f.ctx); e != nil {
		t.Fatalf("shadow setup commit SQLSTATE=%s", stateCode(e))
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := f.owner.Exec(cleanup, "DROP SCHEMA IF EXISTS "+qs+" CASCADE"); e != nil {
			t.Error("guard shadow cleanup failed")
		}
	})
	return schema
}

func resultGuardTx(t *testing.T, f *gwFixture, schema string) pgx.Tx {
	t.Helper()
	tx, e := f.owner.Begin(f.ctx)
	if e != nil {
		t.Fatal("guard transaction begin")
	}
	// Also covers t.Fatal in an assertion before the explicit Commit/Rollback.
	t.Cleanup(func() { resultGuardRollback(t, tx) })
	execOwner(t, tx, f.ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize()+",pg_catalog")
	return tx
}
func resultGuardRollback(t *testing.T, tx pgx.Tx) {
	t.Helper()
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := tx.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
		t.Errorf("guard rollback failed SQLSTATE=%s", stateCode(e))
	}
}

// Catalog identity and complete rows for all seeded relations are compared,
// without logging row contents. This also catches partial 0008 columns/trigger
// installation or loss of the original command/session/evidence graph.
func resultGuardSnapshot(t *testing.T, f *gwFixture, schema string) string {
	t.Helper()
	tx := resultGuardTx(t, f, schema)
	defer resultGuardRollback(t, tx)
	var snapshot string
	e := tx.QueryRow(f.ctx, `SELECT jsonb_build_object(
 'tenants',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb) FROM tenants x),
 'users',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.tenant,x.id),'[]'::jsonb) FROM users x),
 'devices',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.tenant,x.id),'[]'::jsonb) FROM devices x),
 'commands',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.tenant,x.id),'[]'::jsonb) FROM commands x),
 'sessions',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb) FROM device_sessions x),
 'events',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.tenant,x.command,x.event_id),'[]'::jsonb) FROM command_events x),
 'outbox',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb) FROM outbox x),
 'columns',(SELECT coalesce(jsonb_agg(jsonb_build_array(c.relname,a.attnum,a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,pg_catalog.pg_get_expr(d.adbin,d.adrelid)) ORDER BY c.relname,a.attnum),'[]'::jsonb)
   FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
   LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
   WHERE n.nspname=current_schema() AND c.relname IN ('commands','device_sessions') AND a.attnum>0 AND NOT a.attisdropped),
 'constraints',(SELECT coalesce(jsonb_agg(jsonb_build_array(c.relname,k.conname,k.contype,pg_catalog.pg_get_constraintdef(k.oid),k.convalidated,k.conislocal,k.coninhcount) ORDER BY c.relname,k.conname),'[]'::jsonb)
   FROM pg_catalog.pg_constraint k JOIN pg_catalog.pg_class c ON c.oid=k.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname=current_schema() AND c.relname IN ('commands','device_sessions')),
 'triggers',(SELECT coalesce(jsonb_agg(pg_catalog.pg_get_triggerdef(g.oid) ORDER BY c.relname,g.tgname),'[]'::jsonb)
   FROM pg_catalog.pg_trigger g JOIN pg_catalog.pg_class c ON c.oid=g.tgrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname=current_schema() AND c.relname IN ('commands','device_sessions') AND NOT g.tgisinternal),
 'ws_functions',(SELECT coalesce(jsonb_agg(pg_catalog.pg_get_functiondef(p.oid) ORDER BY p.oid),'[]'::jsonb)
   FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
   WHERE n.nspname=current_schema() AND p.proname='guard_ws_consumption')
 )::text`).Scan(&snapshot)
	if e != nil {
		t.Fatalf("guard snapshot SQLSTATE=%s", stateCode(e))
	}
	return snapshot
}
