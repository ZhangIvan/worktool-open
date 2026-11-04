package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Included under the existing gated schema group. No new CI selector, resource,
// connection target or listener. These cases are authored, locally NOT_RUN.
func callbackDestinationVersionCases(t *testing.T, f *pgFixture) {
	t.Run("destination_versions", func(t *testing.T) {
		for _, tc := range []struct{ name, setup, query, message string }{
			{"same_policy_url", "", `UPDATE callback_destinations SET canonical_url='synthetic:changed' WHERE id='D1'`, "DESTINATION_ROTATION_REQUIRES_NEW_VERSION"},
			{"same_key_ref", "", `UPDATE callback_destinations SET key_ref='opaque:changed' WHERE id='D1'`, "DESTINATION_ROTATION_REQUIRES_NEW_VERSION"},
			{"url_requires_policy_even_with_new_key", "", `UPDATE callback_destinations SET canonical_url='synthetic:changed',key_version=2 WHERE id='D1'`, "DESTINATION_ROTATION_REQUIRES_NEW_VERSION"},
			{"key_requires_version_even_with_new_policy", "", `UPDATE callback_destinations SET key_ref='opaque:changed',policy_version=2 WHERE id='D1'`, "DESTINATION_ROTATION_REQUIRES_NEW_VERSION"},
			{"no_policy_rollback", `UPDATE callback_destinations SET policy_version=2 WHERE id='D1'`, `UPDATE callback_destinations SET policy_version=1,key_version=2 WHERE id='D1'`, "DESTINATION_ROTATION_REQUIRES_NEW_VERSION"},
			{"no_key_rollback", `UPDATE callback_destinations SET key_version=2 WHERE id='D1'`, `UPDATE callback_destinations SET key_version=1,policy_version=2 WHERE id='D1'`, "DESTINATION_ROTATION_REQUIRES_NEW_VERSION"},
			{"no_app_reassignment", "", `UPDATE callback_destinations SET app='Y' WHERE id='D1'`, "IMMUTABLE_DESTINATION_IDENTITY"},
			{"no_current_delete", "", `DELETE FROM callback_destinations WHERE id='D1'`, "DESTINATION_IDENTITY_RETAINED"},
			{"historical_url_immutable", "", `UPDATE callback_destination_versions SET canonical_url='synthetic:changed' WHERE destination_id='D1'`, "IMMUTABLE_DESTINATION_VERSION"},
			{"historical_key_immutable", "", `UPDATE callback_destination_versions SET key_ref='opaque:changed' WHERE destination_id='D1'`, "IMMUTABLE_DESTINATION_VERSION"},
			{"historical_identity_immutable", "", `UPDATE callback_destination_versions SET key_version=2 WHERE destination_id='D1'`, "IMMUTABLE_DESTINATION_VERSION"},
			{"historical_delete_denied", "", `DELETE FROM callback_destination_versions WHERE destination_id='D1'`, "DESTINATION_VERSION_HISTORY_RETAINED"},
			{"detached_version_insert", "", `INSERT INTO callback_destination_versions(tenant,app,destination_id,policy_version,key_version,canonical_url,key_ref) VALUES('T1','X','D1',2,1,'synthetic:no-network','opaque:not-installed')`, "DESTINATION_VERSION_NOT_CURRENT"},
			{"cross_app_version_insert", "", `INSERT INTO callback_destination_versions(tenant,app,destination_id,policy_version,key_version,canonical_url,key_ref) VALUES('T1','Y','D1',1,1,'synthetic:no-network','opaque:not-installed')`, "DESTINATION_VERSION_NOT_CURRENT"},
			{"cross_tenant_version_insert", "", `INSERT INTO callback_destination_versions(tenant,app,destination_id,policy_version,key_version,canonical_url,key_ref) VALUES('T2','X','D1',1,1,'synthetic:no-network','opaque:not-installed')`, "DESTINATION_VERSION_NOT_CURRENT"},
			{"revoked_destination_cannot_restart", `UPDATE callback_destinations SET state='revoked' WHERE id='D1'`, `UPDATE callback_destinations SET state='active',policy_version=2 WHERE id='D1'`, "DESTINATION_REVOCATION_TERMINAL"},
			{"version_revoke_cannot_clear", `UPDATE callback_destination_versions SET revoked_at=clock_timestamp() WHERE destination_id='D1'`, `UPDATE callback_destination_versions SET revoked_at=NULL WHERE destination_id='D1'`, "IMMUTABLE_DESTINATION_VERSION"},
			{"version_revoke_cannot_change_time", `UPDATE callback_destination_versions SET revoked_at=clock_timestamp() WHERE destination_id='D1'`, `UPDATE callback_destination_versions SET revoked_at=revoked_at+interval '1 second' WHERE destination_id='D1'`, "IMMUTABLE_DESTINATION_VERSION"},
			{"revoked_version_cannot_reactivate", `UPDATE callback_destination_versions SET revoked_at=clock_timestamp() WHERE destination_id='D1'`, `UPDATE callback_destinations SET state='active' WHERE id='D1'`, "DESTINATION_VERSION_REVOKED"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tx, e := f.owner.Begin(f.ctx)
				if e != nil {
					t.Fatal("version test begin")
				}
				defer rollbackTest(tx)
				if tc.setup != "" {
					mustExec(t, tx, f.ctx, tc.setup)
				}
				_, e = tx.Exec(f.ctx, tc.query)
				requireDestinationError(t, e, "23514", tc.message, "")
			})
		}
		t.Run("rotation_preserves_frozen_delivery", func(t *testing.T) {
			tx, e := f.owner.Begin(f.ctx)
			if e != nil {
				t.Fatal("rotation begin")
			}
			defer rollbackTest(tx)
			materialize(t, f, tx)
			mustExec(t, tx, f.ctx, `UPDATE callback_destinations SET canonical_url='synthetic:next',policy_version=2,key_ref='opaque:next',key_version=2 WHERE id='D1'; SET CONSTRAINTS ALL IMMEDIATE`)
			var valid bool
			e = tx.QueryRow(f.ctx, `SELECT o.policy_version=1 AND o.key_version=1 AND o.state='ready' AND v.canonical_url='synthetic:no-network' AND v.key_ref='opaque:not-installed' AND v.revoked_at IS NULL AND d.policy_version=2 AND d.key_version=2 AND d.canonical_url='synthetic:next' AND n.revoked_at IS NULL FROM callback_outbox o JOIN callback_destination_versions v ON v.tenant=o.tenant AND v.app=o.app AND v.destination_id=o.destination_id AND v.policy_version=o.policy_version AND v.key_version=o.key_version JOIN callback_destinations d ON d.tenant=o.tenant AND d.id=o.destination_id JOIN callback_destination_versions n ON n.tenant=d.tenant AND n.app=d.app AND n.destination_id=d.id AND n.policy_version=d.policy_version AND n.key_version=d.key_version WHERE o.delivery_id='deliveryA'`).Scan(&valid)
			if e != nil || !valid {
				t.Fatal("rotation retargeted history or revoked old identity")
			}
			// Rotation requires new subscriptions for new work; retirement stays terminal.
			mustExec(t, tx, f.ctx, `UPDATE callback_subscription_versions SET retired_at=clock_timestamp() WHERE subscription_id='S1'; INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('T1','X','S1',2,'D1',2,2,'command.result','bounded-result-v1',1,65536,4)`)
		})
		t.Run("revoke_is_terminal_and_idempotent", func(t *testing.T) {
			tx, e := f.owner.Begin(f.ctx)
			if e != nil {
				t.Fatal("revoke begin")
			}
			defer rollbackTest(tx)
			mustExec(t, tx, f.ctx, `UPDATE callback_destinations SET state='revoked' WHERE id='D1'; UPDATE callback_destinations SET state='revoked' WHERE id='D1'; UPDATE callback_destination_versions SET revoked_at=revoked_at WHERE destination_id='D1'; SET CONSTRAINTS ALL IMMEDIATE`)
			var revoked bool
			e = tx.QueryRow(f.ctx, `SELECT revoked_at IS NOT NULL FROM callback_destination_versions WHERE destination_id='D1'`).Scan(&revoked)
			if e != nil || !revoked {
				t.Fatal("destination revoke did not revoke its exact version")
			}
		})
		t.Run("exact_version_revoke_allows_new_identity", func(t *testing.T) {
			tx, e := f.owner.Begin(f.ctx)
			if e != nil {
				t.Fatal("version revoke begin")
			}
			defer rollbackTest(tx)
			mustExec(t, tx, f.ctx, `UPDATE callback_destination_versions SET revoked_at=clock_timestamp() WHERE destination_id='D1'; UPDATE callback_destinations SET policy_version=2,key_version=2 WHERE id='D1'; SET CONSTRAINTS ALL IMMEDIATE`)
			var valid bool
			e = tx.QueryRow(f.ctx, `SELECT count(*)=2 AND count(*) FILTER(WHERE revoked_at IS NOT NULL)=1 FROM callback_destination_versions WHERE destination_id='D1'`).Scan(&valid)
			if e != nil || !valid {
				t.Fatal("version-specific revocation scope changed")
			}
		})
		t.Run("subscription_exact_tuple", func(t *testing.T) {
			for _, pair := range [][2]int64{{2, 1}, {1, 2}} {
				tx, e := f.owner.Begin(f.ctx)
				if e != nil {
					t.Fatal("subscription begin")
				}
				mustExec(t, tx, f.ctx, `UPDATE callback_subscription_versions SET retired_at=clock_timestamp() WHERE subscription_id='S1'`)
				_, e = tx.Exec(f.ctx, `INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('T1','X','S1',2,'D1',$1,$2,'command.result','bounded-result-v1',1,65536,4)`, pair[0], pair[1])
				rollbackTest(tx)
				requireDestinationError(t, e, "23503", "", "subscription_exact_destination_version")
			}
		})
		t.Run("temporary_table_cannot_shadow_version_guard", func(t *testing.T) {
			// Store TEMP is intentionally denied by CI; do not broaden its grants.
			tx, e := f.owner.Begin(f.ctx)
			if e != nil {
				t.Fatal("shadow begin")
			}
			defer rollbackTest(tx)
			mustExec(t, tx, f.ctx, `CREATE TEMP TABLE callback_destinations(tenant text,app text,id text,policy_version bigint,key_version bigint,canonical_url text,key_ref text,state text) ON COMMIT DROP; INSERT INTO pg_temp.callback_destinations VALUES('T1','X','D1',99,1,'synthetic:injected','opaque:injected','active')`)
			mustExec(t, tx, f.ctx, "SET LOCAL search_path TO pg_temp,"+pgx.Identifier{f.schema}.Sanitize()+",pg_catalog")
			_, e = tx.Exec(f.ctx, "INSERT INTO "+pgx.Identifier{f.schema, "callback_destination_versions"}.Sanitize()+`(tenant,app,destination_id,policy_version,key_version,canonical_url,key_ref) VALUES('T1','X','D1',99,1,'synthetic:injected','opaque:injected')`)
			requireDestinationError(t, e, "23514", "DESTINATION_VERSION_NOT_CURRENT", "")
		})
		t.Run("historical_revoke_conflicts_with_authority_lock", func(t *testing.T) {
			hold, e := f.store.Begin(f.ctx)
			if e != nil {
				t.Fatal("authority lock begin")
			}
			defer rollbackTest(hold)
			var url, ref string
			var valid bool
			if e = hold.QueryRow(f.ctx, callbackReadDestinationVersion, "T1", "X", "D1", int64(1), int64(1)).Scan(&url, &ref, &valid); e != nil || !valid {
				t.Fatal("version share lock")
			}
			revoke, e := f.owner.Begin(f.ctx)
			if e != nil {
				t.Fatal("revoke lock begin")
			}
			defer rollbackTest(revoke)
			mustExec(t, revoke, f.ctx, `SET LOCAL lock_timeout='100ms'`)
			_, e = revoke.Exec(f.ctx, `UPDATE callback_destination_versions SET revoked_at=clock_timestamp() WHERE destination_id='D1'`)
			requireDestinationError(t, e, "55P03", "", "")
		})
	})
}

func requireDestinationError(t *testing.T, err error, code, message, constraint string) {
	t.Helper()
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != code || (message != "" && pe.Message != message) || (constraint != "" && pe.ConstraintName != constraint) {
		t.Fatalf("wrong destination guard SQLSTATE=%s", sqlState(err))
	}
}

func callbackDestinationMigrationCases(t *testing.T, f *pgFixture) {
	t.Run("migration_0009_history_gate", func(t *testing.T) {
		schema := f.schema + "_dv"
		qs := pgx.Identifier{schema}.Sanitize()
		tx, e := f.owner.Begin(f.ctx)
		if e != nil {
			t.Fatal("migration shadow begin")
		}
		defer rollbackTest(tx)
		mustExec(t, tx, f.ctx, "CREATE SCHEMA "+qs+"; REVOKE ALL ON SCHEMA "+qs+" FROM PUBLIC; SET LOCAL search_path TO "+qs+",pg_catalog")
		if len(migrations.Order) < 9 || migrations.Order[8] != "0009_callback_destination_versions.sql" {
			t.Fatal("migration boundary changed")
		}
		for _, name := range migrations.Order[:8] {
			body, e := migrations.Files.ReadFile(name)
			if e != nil {
				t.Fatal("migration missing")
			}
			mustExec(t, tx, f.ctx, string(body))
		}
		mustExec(t, tx, f.ctx, `INSERT INTO tenants VALUES('T1');
 INSERT INTO users(tenant,id,login,role,password) VALUES('T1','admin','admin','admin','!disabled');
 INSERT INTO devices(tenant,id,name,key_id,secret,capabilities) VALUES('T1','A','synthetic','keyA','!disabled','[]');
 INSERT INTO developer_apps(tenant,id,creator,name,token_hash,token_expires,scopes) VALUES('T1','X','admin','synthetic','!disabled',9000000000000,'[]');
 INSERT INTO commands(tenant,id,device,actor,capability,digest,policy,confirm,status,created,expires,actor_type,app_epoch,app_id,subscription_snapshot,subscription_snapshot_digest,admission_version) VALUES('T1','commandA','A','admin','message.send_text','digest',1,false,'queued',1,9000000000000,'app',0,'X','[]',repeat('0',64),'bounded-result-v1');
 INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('T1','D1','X','synthetic:no-network',1,'opaque:not-installed',1,'active'),('T1','D2','X','synthetic:no-network',1,'opaque:not-installed',1,'pending_review'),('T1','D3','X','synthetic:no-network',1,'opaque:not-installed',1,'revoked')`)
		if e = tx.Commit(f.ctx); e != nil {
			t.Fatal("migration shadow commit")
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, e := f.owner.Exec(ctx, "DROP SCHEMA "+qs+" CASCADE"); e != nil {
				t.Error("migration shadow cleanup")
			}
		})
		body, e := migrations.Files.ReadFile(migrations.Order[8])
		if e != nil {
			t.Fatal("migration missing")
		}
		subscription := `INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('T1','X','S1',1,'D1',1,1,'command.result','bounded-result-v1',1,65536,4)`
		event := `INSERT INTO developer_callbacks(event,tenant,app,command,status,body,next_at) VALUES('eventA','T1','X','commandA','succeeded','{}',1)`
		for _, name := range []string{"destination_only_config", "subscription_history", "legacy_event_history", "held_reservation_history", "materialized_delivery_history", "command_snapshot_history"} {
			t.Run(name, func(t *testing.T) {
				tx, e := f.owner.Begin(f.ctx)
				if e != nil {
					t.Fatal("migration case begin")
				}
				defer rollbackTest(tx)
				mustExec(t, tx, f.ctx, "SET LOCAL search_path TO "+qs+",pg_catalog")
				switch name {
				case "subscription_history":
					mustExec(t, tx, f.ctx, subscription)
				case "legacy_event_history":
					mustExec(t, tx, f.ctx, event)
				case "held_reservation_history":
					mustExec(t, tx, f.ctx, subscription)
					reservation(t, f, tx)
				case "materialized_delivery_history":
					mustExec(t, tx, f.ctx, subscription)
					mustExec(t, tx, f.ctx, event)
					materialize(t, f, tx)
					mustExec(t, tx, f.ctx, "SET CONSTRAINTS ALL IMMEDIATE")
				case "command_snapshot_history":
					mustExec(t, tx, f.ctx, `INSERT INTO commands(tenant,id,device,actor,capability,digest,policy,confirm,status,created,expires,actor_type,app_epoch,app_id,subscription_snapshot,subscription_snapshot_digest,admission_version) VALUES('T1','commandFrozen','A','admin','message.send_text','digest',1,false,'queued',1,9000000000000,'app',0,'X','[{"subscriptionId":"S_legacy"}]',repeat('0',64),'bounded-result-v1')`)
				}
				before := callbackMigrationRows(t, f, tx)
				mustExec(t, tx, f.ctx, "SAVEPOINT migration_attempt")
				_, e = tx.Exec(f.ctx, string(body))
				if name == "destination_only_config" {
					if e != nil {
						t.Fatalf("safe config-only migration SQLSTATE=%s", sqlState(e))
					}
					var valid bool
					e = tx.QueryRow(f.ctx, `SELECT count(*)=3 AND count(*) FILTER(WHERE revoked_at IS NOT NULL)=1 AND bool_and(canonical_url='synthetic:no-network' AND key_ref='opaque:not-installed') FROM callback_destination_versions`).Scan(&valid)
					if e != nil || !valid {
						t.Fatal("safe config freeze changed identity/revocation")
					}
					if after := callbackMigrationRows(t, f, tx); after != before {
						t.Fatal("migration changed existing rows")
					}
					mustExec(t, tx, f.ctx, "SET CONSTRAINTS ALL IMMEDIATE")
				} else {
					requireDestinationError(t, e, "P0001", "CALLBACK_LEGACY_HISTORY_REQUIRES_RECONCILIATION", "")
					mustExec(t, tx, f.ctx, "ROLLBACK TO SAVEPOINT migration_attempt")
					if after := callbackMigrationRows(t, f, tx); after != before {
						t.Fatal("failed migration altered old rows")
					}
					var absent bool
					e = tx.QueryRow(f.ctx, `SELECT to_regclass('callback_destination_versions') IS NULL`).Scan(&absent)
					if e != nil || !absent {
						t.Fatal("failed migration installed partial history")
					}
				}
			})
		}
	})
}

func callbackMigrationRows(t *testing.T, f *pgFixture, tx pgx.Tx) string {
	t.Helper()
	var snapshot string
	e := tx.QueryRow(f.ctx, `SELECT jsonb_build_array(
 (SELECT coalesce(jsonb_agg(to_jsonb(d) ORDER BY id),'[]') FROM callback_destinations d),
 (SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY subscription_id,version),'[]') FROM callback_subscription_versions s),
 (SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY reservation_id),'[]') FROM admission_reservations r),
 (SELECT coalesce(jsonb_agg(to_jsonb(o) ORDER BY delivery_id),'[]') FROM callback_outbox o),
 (SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY event),'[]') FROM developer_callbacks e),
 (SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY id),'[]') FROM commands c))::text`).Scan(&snapshot)
	if e != nil {
		t.Fatalf("migration snapshot SQLSTATE=%s", sqlState(e))
	}
	return snapshot
}
