package gateway

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
)

// Children of CB03's unchanged isolated-PG gate. These are authored real-SQL
// cases, NOT_RUN locally. Owner DML injects representable contradictions without
// changing constraints, triggers or privileges; recovery uses the store role.
func callbackRecoveryEvidenceIntegrationCases(t *testing.T) {
	gatewayGate(t)
	for _, path := range []string{"explicit", "selector"} {
		for _, mode := range []string{"zero_start", "start_before_claim", "start_after_now", "prepare_before_start", "prepare_after_now", "status_present", "error_present", "empty_error_present", "leased_delivered"} {
			t.Run("recovery_evidence_"+path+"_"+mode, func(t *testing.T) {
				f, store, d := callbackRecoveryEvidenceFixture(t, false)
				callbackRecoveryEvidenceExpire(t, f, d)
				q := ""
				switch mode {
				case "zero_start":
					q = `UPDATE callback_attempts SET started_at='0001-01-01 00:00:00+00'::timestamptz WHERE tenant=$1 AND delivery_id=$2`
				case "start_before_claim":
					q = `UPDATE callback_attempts a SET started_at=o.claimed_at-interval '1 microsecond' FROM callback_outbox o WHERE a.tenant=$1 AND a.delivery_id=$2 AND o.tenant=a.tenant AND o.delivery_id=a.delivery_id`
				case "start_after_now":
					q = `UPDATE callback_attempts SET started_at=clock_timestamp()+interval '1 hour' WHERE tenant=$1 AND delivery_id=$2`
				case "prepare_before_start":
					q = `UPDATE callback_attempts SET send_prepared_at=started_at-interval '1 microsecond' WHERE tenant=$1 AND delivery_id=$2`
				case "prepare_after_now":
					q = `UPDATE callback_attempts SET send_prepared_at=clock_timestamp()+interval '1 hour' WHERE tenant=$1 AND delivery_id=$2`
				case "status_present":
					q = `UPDATE callback_attempts SET status_code=204 WHERE tenant=$1 AND delivery_id=$2`
				case "error_present":
					q = `UPDATE callback_attempts SET error_code='SYNTHETIC_PRIOR_EVIDENCE' WHERE tenant=$1 AND delivery_id=$2`
				case "empty_error_present":
					q = `UPDATE callback_attempts SET error_code='' WHERE tenant=$1 AND delivery_id=$2`
				case "leased_delivered":
					q = `UPDATE callback_outbox SET delivered_at=clock_timestamp() WHERE tenant=$1 AND delivery_id=$2`
				}
				callbackRecoveryEvidenceUpdate(t, f, q, d)
				callbackRecoveryEvidenceEligible(t, f, d, true)
				before := callbackRecoveryEvidenceSnapshot(t, f)
				callbackRecoveryEvidenceReject(t, f, store, d, path)
				if callbackRecoveryEvidenceSnapshot(t, f) != before {
					t.Fatal("rejected recovery changed durable evidence, slots, audit or charges")
				}
			})
		}
		for _, prepared := range []bool{false, true} {
			mode := "unprepared_start_equals_claim"
			if prepared {
				mode = "prepared_equals_start"
			}
			t.Run("recovery_evidence_"+path+"_"+mode, func(t *testing.T) {
				f, store, d := callbackRecoveryEvidenceFixture(t, prepared)
				callbackRecoveryEvidenceExpire(t, f, d)
				if f.count(t, `SELECT count(*) FROM callback_attempts a JOIN callback_outbox o USING(tenant,delivery_id) WHERE a.tenant=$1 AND a.delivery_id=$2 AND a.started_at=o.claimed_at AND a.started_at<=o.hard_deadline_at AND ((a.send_prepared_at IS NOT NULL)=$3) AND (a.send_prepared_at IS NULL OR a.send_prepared_at=a.started_at)`, d.Handle.Tenant, d.Handle.DeliveryID, prepared) != 1 {
					t.Fatal("valid control timing or preparation marker")
				}
				callbackRecoveryEvidenceEligible(t, f, d, true)
				stable := callbackRecoveryEvidenceStableSnapshot(t, f)
				var err error
				if path == "explicit" {
					err = store.RecoverExpired(f.ctx, d.Handle.Tenant, d.Handle.DeliveryID)
				} else {
					var recovered bool
					recovered, err = store.RecoverOneExpired(f.ctx)
					if !recovered {
						t.Fatal("valid expired control not reported recovered")
					}
				}
				if err != nil {
					t.Fatal("valid expired control failed; redacted")
				}
				outcome, code := "lease_lost", "LEASE_EXPIRED_BEFORE_PREPARE"
				if prepared {
					outcome, code = "delivery_unknown", "LEASE_EXPIRED_AFTER_PREPARE"
				}
				if f.count(t, `SELECT count(*) FROM callback_outbox WHERE tenant=$1 AND delivery_id=$2 AND state='ready' AND attempt_count=$3 AND fence=$4 AND lease_owner IS NULL AND lease_recovery_epoch IS NULL AND lease_until IS NULL AND delivered_at IS NULL AND last_error_code=$5`, d.Handle.Tenant, d.Handle.DeliveryID, d.Handle.Attempt, d.Handle.Fence, code) != 1 ||
					f.count(t, `SELECT count(*) FROM callback_attempts a JOIN callback_outbox o USING(tenant,delivery_id) WHERE a.tenant=$1 AND a.delivery_id=$2 AND a.fence=$3 AND a.outcome=$4 AND a.error_code=$5 AND a.finished_at>=a.started_at AND a.finished_at<=clock_timestamp() AND (a.send_prepared_at IS NULL OR a.finished_at>=a.send_prepared_at) AND a.status_code IS NULL AND o.available_at=a.finished_at+interval '1 second'`, d.Handle.Tenant, d.Handle.DeliveryID, d.Handle.Fence, outcome, code) != 1 ||
					f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 0 ||
					f.count(t, `SELECT count(*) FROM audit u JOIN callback_attempts a ON a.tenant=u.tenant AND a.delivery_id=u.target WHERE u.tenant=$1 AND u.action='callback.recovered' AND u.target=$2 AND u.actor='worker_demo' AND u.outcome=$3 AND u.created=floor(extract(epoch FROM a.finished_at)*1000)::bigint`, d.Handle.Tenant, d.Handle.DeliveryID, code) != 1 {
					t.Fatal("valid recovery settlement graph")
				}
				if callbackRecoveryEvidenceStableSnapshot(t, f) != stable || f.count(t, `SELECT count(*) FROM audit WHERE action='callback.recovered'`) != 1 {
					t.Fatal("recovery changed immutable graph, slot fences, retained audits or charges")
				}
				before := callbackRecoveryEvidenceSnapshot(t, f)
				if err = store.RecoverExpired(f.ctx, d.Handle.Tenant, d.Handle.DeliveryID); err != nil {
					t.Fatal("settled explicit recovery was not idempotent")
				}
				if recovered, err := store.RecoverOneExpired(f.ctx); err != nil || recovered {
					t.Fatal("settled selector recovery counted twice")
				}
				if callbackRecoveryEvidenceSnapshot(t, f) != before {
					t.Fatal("repeated recovery rewrote settlement or added audit")
				}
			})
		}
	}
	t.Run("recovery_evidence_explicit_unexpired_bad_status", func(t *testing.T) {
		f, store, d := callbackRecoveryEvidenceFixture(t, false)
		// A static unexpired row cannot be selected by RecoverOneExpired. Only the
		// explicit path is covered here; the stale-discovery race remains scripted.
		callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_attempts SET status_code=204 WHERE tenant=$1 AND delivery_id=$2`, d)
		callbackRecoveryEvidenceEligible(t, f, d, false)
		before := callbackRecoveryEvidenceSnapshot(t, f)
		callbackRecoveryEvidenceReject(t, f, store, d, "explicit")
		if callbackRecoveryEvidenceSnapshot(t, f) != before {
			t.Fatal("unexpired refusal changed durable graph")
		}
	})
	t.Run("recovery_evidence_schema_rejects_missing_claim", func(t *testing.T) {
		f, _, d := callbackRecoveryEvidenceFixture(t, false)
		before := callbackRecoveryEvidenceSnapshot(t, f)
		// Schema evidence, not a claim that recovery observed an impossible row.
		if _, err := f.owner.Exec(f.ctx, `UPDATE callback_outbox SET claimed_at=NULL WHERE tenant=$1 AND delivery_id=$2`, d.Handle.Tenant, d.Handle.DeliveryID); stateCode(err) != "23514" {
			t.Fatal("leased missing claim was not rejected by the schema")
		}
		if callbackRecoveryEvidenceSnapshot(t, f) != before {
			t.Fatal("schema refusal changed durable graph")
		}
	})
}

func callbackRecoveryEvidenceFixture(t *testing.T, prepared bool) (*gwFixture, *pg.CallbackStore, cb.Delivery) {
	t.Helper()
	f := newGWWithStoreConstructor(t, callbackSyntheticStore)
	store, _ := callbackSetup(t, f)
	callbackCompletedCommand(t, f)
	// Join the existing gateway before snapshots so background session cleanup
	// cannot masquerade as a recovery mutation. Preserve the fixture cleanup veto.
	ctx, cancel := context.WithTimeout(f.ctx, 7*time.Second)
	defer cancel()
	if err := f.controller.Shutdown(ctx); err != nil {
		f.retainStorage()
		t.Fatal("gateway join failed; storage retained for isolated resource cleanup")
	}
	if f.controller.LiveCount() != 0 || f.controller.CleanupErrors() != 0 {
		t.Fatal("gateway did not finish cleanly")
	}
	d, ok, err := store.Claim(f.ctx)
	if err != nil || !ok {
		t.Fatal("recovery fixture claim failed; redacted")
	}
	if prepared {
		if permit, err := store.Prepare(f.ctx, callbackPrepared(d)); err != nil || !permit.Fresh {
			t.Fatal("recovery fixture prepare failed; redacted")
		}
	}
	return f, store, d
}

func callbackRecoveryEvidenceUpdate(t *testing.T, f *gwFixture, q string, d cb.Delivery) {
	t.Helper()
	tag, err := f.owner.Exec(f.ctx, q, d.Handle.Tenant, d.Handle.DeliveryID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("recovery fixture mutation failed SQLSTATE=%s", stateCode(err))
	}
}

func callbackRecoveryEvidenceExpire(t *testing.T, f *gwFixture, d cb.Delivery) {
	t.Helper()
	callbackWorkerExpire(t, f, d.Handle.DeliveryID)
	// Normalize attempt timestamps to the injected claim, preserving the marker's
	// presence after an actual Prepare. This makes claim=start<=prepare<=hard<now
	// consistent on both fast and slow hosts. It is deliberate stored-time fault
	// injection, not a natural timeout or a replacement clock/store read.
	callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_attempts a SET started_at=o.claimed_at,send_prepared_at=CASE WHEN a.send_prepared_at IS NOT NULL THEN o.claimed_at ELSE NULL END FROM callback_outbox o WHERE a.tenant=$1 AND a.delivery_id=$2 AND o.tenant=a.tenant AND o.delivery_id=a.delivery_id AND o.fence=a.fence`, d)
}

func callbackRecoveryEvidenceEligible(t *testing.T, f *gwFixture, d cb.Delivery, expired bool) {
	t.Helper()
	if f.count(t, `SELECT count(*) FROM callback_outbox WHERE tenant=$1 AND delivery_id=$2 AND destination_id='dest_demo' AND state='leased' AND lease_recovery_epoch='recovery_demo' AND (lease_until<=clock_timestamp())=$3`, d.Handle.Tenant, d.Handle.DeliveryID, expired) != 1 ||
		f.count(t, `SELECT count(*) FROM callback_attempts WHERE tenant=$1 AND delivery_id=$2 AND fence=$3 AND outcome='in_progress' AND finished_at IS NULL`, d.Handle.Tenant, d.Handle.DeliveryID, d.Handle.Fence) != 1 ||
		f.count(t, `SELECT count(*) FROM scope_slots s JOIN callback_outbox o ON o.tenant=s.tenant AND o.delivery_id=s.delivery_id WHERE o.tenant=$1 AND o.delivery_id=$2 AND s.owner_instance=o.lease_owner AND s.delivery_fence=o.fence AND s.recovery_epoch=o.lease_recovery_epoch AND s.lease_until=o.lease_until AND s.slot_fence>0`, d.Handle.Tenant, d.Handle.DeliveryID) != 2 ||
		f.count(t, `SELECT count(*) FROM audit WHERE action='callback.recovered'`) != 0 {
		t.Fatal("recovery fixture does not meet selection and retained-evidence prerequisites")
	}
}

func callbackRecoveryEvidenceReject(t *testing.T, f *gwFixture, store *pg.CallbackStore, d cb.Delivery, path string) {
	t.Helper()
	var err error
	recovered := false
	if path == "explicit" {
		err = store.RecoverExpired(f.ctx, d.Handle.Tenant, d.Handle.DeliveryID)
	} else {
		recovered, err = store.RecoverOneExpired(f.ctx)
	}
	if recovered || !errors.Is(err, cb.ErrStale) || err.Error() != cb.ErrStale.Error() {
		t.Fatal("contradictory recovery was not refused with the exact redacted stale error")
	}
}

func callbackRecoveryEvidenceSnapshot(t *testing.T, f *gwFixture) string {
	t.Helper()
	var value string
	// Include xmin so a committed identical rewrite cannot pass. This proves
	// durable preservation, not absence of DML later rolled back. Never print it.
	err := f.owner.QueryRow(f.ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,delivery_id) FROM callback_outbox x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,delivery_id,fence) FROM callback_attempts x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY scope_kind,tenant,scope_id,slot_no) FROM scope_slots x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY id) FROM audit x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY scope_kind,tenant,scope_id) FROM scope_limits x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,reservation_id) FROM admission_reservations x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,id) FROM commands x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,event) FROM developer_callbacks x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,command) FROM callback_replay_receipts x)
 )::text`).Scan(&value)
	if err != nil {
		t.Fatal("recovery durable snapshot failed; redacted")
	}
	return value
}

// Projection of exactly the fields/tables that a valid settlement must retain.
// Updated rows omit xmin and the finite settlement columns; all other rows keep
// xmin. Slot fences, attempt timing, immutable bodies, receipts and quotas remain.
func callbackRecoveryEvidenceStableSnapshot(t *testing.T, f *gwFixture) string {
	t.Helper()
	var value string
	err := f.owner.QueryRow(f.ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x)-ARRAY['state','available_at','delivered_at','lease_owner','lease_recovery_epoch','lease_until','last_error_code'] ORDER BY tenant,delivery_id) FROM callback_outbox x),
 (SELECT jsonb_agg(to_jsonb(x)-ARRAY['finished_at','outcome','status_code','error_code'] ORDER BY tenant,delivery_id,fence) FROM callback_attempts x),
 (SELECT jsonb_agg(to_jsonb(x)-ARRAY['owner_instance','delivery_id','delivery_fence','recovery_epoch','lease_until'] ORDER BY scope_kind,tenant,scope_id,slot_no) FROM scope_slots x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY id) FROM audit x WHERE action<>'callback.recovered'),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY scope_kind,tenant,scope_id) FROM scope_limits x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,reservation_id) FROM admission_reservations x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,id) FROM commands x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,event) FROM developer_callbacks x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,command) FROM callback_replay_receipts x)
 )::text`).Scan(&value)
	if err != nil {
		t.Fatal("recovery stable snapshot failed; redacted")
	}
	return value
}

// Pure source regression: count queries in this fixture must bind every SQL
// placeholder. This catches missing arguments before gated PG execution; it
// does not parse PostgreSQL syntax or establish real database behavior.
func TestCallbackRecoveryEvidenceCountBindings(t *testing.T) {
	inspect := func(source []byte) (checked, slots, problems int) {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
		if err != nil {
			return 0, 0, 1
		}
		parameter := regexp.MustCompile(`\$([0-9]+)`)
		binding := func(expr ast.Expr, field string) bool {
			outer, ok := expr.(*ast.SelectorExpr)
			if !ok || outer.Sel.Name != field {
				return false
			}
			inner, ok := outer.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "Handle" {
				return false
			}
			name, ok := inner.X.(*ast.Ident)
			return ok && name.Name == "d"
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "count" {
				return true
			}
			receiver, ok := method.X.(*ast.Ident)
			if !ok || receiver.Name != "f" {
				return true
			}
			checked++
			if len(call.Args) < 2 || call.Ellipsis.IsValid() {
				problems++
				return true
			}
			literal, ok := call.Args[1].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				problems++
				return true
			}
			sql, err := strconv.Unquote(literal.Value)
			if err != nil {
				problems++
				return true
			}
			if strings.Contains(sql, "s.slot_fence>0") {
				slots++
			}
			indices, highest := map[int]bool{}, 0
			for _, match := range parameter.FindAllStringSubmatch(sql, -1) {
				n, err := strconv.Atoi(match[1])
				if err != nil || n < 1 {
					problems++
					return true
				}
				indices[n] = true
				if n > highest {
					highest = n
				}
			}
			if highest != len(call.Args)-2 || len(indices) != highest {
				problems++
				return true
			}
			if strings.Contains(sql, "tenant=$1") && (strings.Contains(sql, "delivery_id=$2") || strings.Contains(sql, "target=$2")) {
				if len(call.Args) < 4 || !binding(call.Args[2], "Tenant") || !binding(call.Args[3], "DeliveryID") {
					problems++
				}
			}
			return true
		})
		return
	}
	_, path, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("fixture source location unavailable")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("fixture source unavailable")
	}
	if checked, slots, problems := inspect(source); checked == 0 || slots != 1 || problems != 0 {
		t.Fatal("fixture count SQL bindings incomplete or out of order")
	}
	for _, args := range []string{"", ",d.Handle.Tenant", ",d.Handle.DeliveryID,d.Handle.Tenant", ",d.Handle.Tenant,d.Handle.Tenant", ",d.Handle.Tenant,d.Handle.DeliveryID,true"} {
		fixture := []byte("package gateway; func fixture(){ f.count(t, \"SELECT count(*) FROM scope_slots WHERE tenant=$1 AND delivery_id=$2\"" + args + ") }")
		if checked, _, problems := inspect(fixture); checked != 1 || problems == 0 {
			t.Fatal("invalid synthetic SQL bindings were accepted")
		}
	}
	fixture := []byte(`package gateway; func fixture(){ f.count(t, "SELECT count(*) FROM scope_slots WHERE tenant=$1 AND delivery_id=$2",d.Handle.Tenant,d.Handle.DeliveryID) }`)
	if checked, _, problems := inspect(fixture); checked != 1 || problems != 0 {
		t.Fatal("valid synthetic SQL bindings were rejected")
	}
}
