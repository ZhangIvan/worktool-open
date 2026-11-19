package gateway

import (
	"bytes"
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

// Children of CB02's unchanged isolated-PG gate, NOT_RUN locally. Owner DML
// injects schema-legal stored evidence with all constraints/triggers active.
// Prepare still uses the real store role, Runner and PostgreSQL clock. These
// cases do not inject server-clock regression or claim a natural timeout.
func callbackPrepareEvidenceIntegrationCases(t *testing.T) {
	gatewayGate(t)
	for _, mode := range []string{"status_present", "error_present", "empty_error_present", "leased_delivered", "zero_start", "start_before_claim", "start_after_now"} {
		t.Run("prepare_evidence_"+mode, func(t *testing.T) {
			f, store, d := callbackRecoveryEvidenceFixture(t, false)
			callbackPrepareEvidenceInject(t, f, d, mode)
			callbackPrepareEvidenceEligible(t, f, d, false)
			before := callbackRecoveryEvidenceSnapshot(t, f)
			p := callbackPrepared(d)
			permit, err := store.Prepare(f.ctx, p)
			callbackPrepareEvidenceRefused(t, p, permit, err)
			callbackPrepareEvidenceEligible(t, f, d, false)
			if callbackRecoveryEvidenceSnapshot(t, f) != before {
				t.Fatal("refused prepare changed durable evidence, slots, audit or charges")
			}
		})
	}
	t.Run("prepare_evidence_start_equals_claim", func(t *testing.T) {
		f, store, d := callbackRecoveryEvidenceFixture(t, false)
		callbackPrepareEvidenceEligible(t, f, d, false)
		if f.count(t, `SELECT count(*) FROM callback_attempts a JOIN callback_outbox o USING(tenant,delivery_id) WHERE a.tenant=$1 AND a.delivery_id=$2 AND a.started_at=o.claimed_at AND a.started_at>='1970-01-01'::timestamptz AND a.status_code IS NULL AND a.error_code IS NULL AND o.delivered_at IS NULL`, d.Handle.Tenant, d.Handle.DeliveryID) != 1 {
			t.Fatal("actual claim did not establish the valid equality control")
		}
		stable := callbackPrepareEvidenceStableSnapshot(t, f)
		p := callbackPrepared(d)
		permit, err := store.Prepare(f.ctx, p)
		if err != nil || !permit.Fresh || permit.Prepared != p || !time.Now().Before(p.Deadline) {
			t.Fatal("valid prepare did not grant the exact fresh permit within its deadline")
		}
		callbackPrepareEvidenceEligible(t, f, d, true)
		if f.count(t, `SELECT count(*) FROM callback_attempts a JOIN callback_outbox o USING(tenant,delivery_id) WHERE a.tenant=$1 AND a.delivery_id=$2 AND a.fence=$3 AND a.started_at=o.claimed_at AND a.send_prepared_at>=a.started_at AND a.send_prepared_at<=clock_timestamp() AND a.send_prepared_at<o.lease_until AND a.status_code IS NULL AND a.error_code IS NULL AND o.delivered_at IS NULL`, d.Handle.Tenant, d.Handle.DeliveryID, d.Handle.Fence) != 1 ||
			f.count(t, `SELECT count(*) FROM audit u JOIN callback_attempts a ON a.tenant=u.tenant AND a.delivery_id=u.target WHERE u.tenant=$1 AND u.target=$2 AND u.action='callback.prepared' AND u.actor='worker_demo' AND u.outcome='prepared' AND u.created=floor(extract(epoch FROM a.send_prepared_at)*1000)::bigint`, d.Handle.Tenant, d.Handle.DeliveryID) != 1 ||
			callbackPrepareEvidenceStableSnapshot(t, f) != stable {
			t.Fatal("valid prepare changed more than its marker and single matching audit")
		}
		before := callbackRecoveryEvidenceSnapshot(t, f)
		permit, err = store.Prepare(f.ctx, p)
		callbackPrepareEvidenceRefused(t, p, permit, err)
		callbackPrepareEvidenceEligible(t, f, d, true)
		if callbackRecoveryEvidenceSnapshot(t, f) != before {
			t.Fatal("repeated prepare rewrote the marker, audit or retained graph")
		}
	})
	for _, mode := range []string{"status_present", "start_before_claim"} {
		t.Run("prepare_evidence_sender_"+mode, func(t *testing.T) {
			f := newGWWithStoreConstructor(t, callbackSyntheticStore)
			actual, policy := callbackSetup(t, f)
			callbackCompletedCommand(t, f)
			// Join the existing gateway before Claim, injection and snapshots. No
			// new callback receiver, HTTP listener or worker loop is constructed.
			ctx, cancel := context.WithTimeout(f.ctx, 7*time.Second)
			defer cancel()
			if err := f.controller.Shutdown(ctx); err != nil {
				f.retainStorage()
				t.Fatal("gateway join failed; storage retained for isolated cleanup")
			}
			if f.controller.LiveCount() != 0 || f.controller.CleanupErrors() != 0 {
				t.Fatal("gateway did not finish cleanly")
			}
			store := &callbackPrepareEvidenceSenderStore{CallbackStore: actual, t: t, f: f, mode: mode}
			key := []byte(randHex(t, 16))
			defer clear(key)
			keys := &callbackPrepareEvidenceKey{base: callbackFixtureKey{policy.Binding, key}}
			transport := &callbackObservationTransport{callbackWorkerSyntheticTransport: callbackWorkerSyntheticTransport{key: key}}
			sender, err := cb.New(cb.Dependencies{Store: store, Clock: callbackWallClock{}, Gate: callbackLocalGate{}, DNS: callbackFixtureDNS{}, Keys: keys, Transport: transport}, []cb.Policy{policy})
			if err != nil {
				t.Fatal("prepare evidence sender configuration")
			}
			r := sender.RunOne(f.ctx)
			callbackPrepareEvidenceRefused(t, store.prepared, store.permit, store.prepareErr)
			if store.claims != 1 || store.prepares != 1 || store.completes != 0 || transport.calls != 0 || !r.Claimed || !r.Reconcile || r.TransportInvoked || r.Code != "PREPARE_UNCERTAIN_OR_STALE" || r.State != "" || r.Outcome != "" {
				t.Fatal("contradictory prepare reached send or settlement, or changed its refusal classification")
			}
			if keys.calls != 1 || len(keys.resolved) != 32 || !bytes.Equal(keys.resolved, make([]byte, 32)) {
				t.Fatal("sender did not clear its resolved key on refused prepare")
			}
			callbackPrepareEvidenceEligible(t, f, store.delivery, false)
			if store.before == "" || callbackRecoveryEvidenceSnapshot(t, f) != store.before {
				t.Fatal("sender refusal changed the post-injection durable graph")
			}
		})
	}
}

func callbackPrepareEvidenceInject(t *testing.T, f *gwFixture, d cb.Delivery, mode string) {
	t.Helper()
	switch mode {
	case "status_present":
		callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_attempts SET status_code=204 WHERE tenant=$1 AND delivery_id=$2`, d)
	case "error_present":
		callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_attempts SET error_code='SYNTHETIC_PRIOR_EVIDENCE' WHERE tenant=$1 AND delivery_id=$2`, d)
	case "empty_error_present":
		callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_attempts SET error_code='' WHERE tenant=$1 AND delivery_id=$2`, d)
	case "leased_delivered":
		callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_outbox SET delivered_at=clock_timestamp() WHERE tenant=$1 AND delivery_id=$2`, d)
	case "zero_start":
		callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_attempts SET started_at='0001-01-01 00:00:00+00'::timestamptz WHERE tenant=$1 AND delivery_id=$2`, d)
	case "start_before_claim":
		callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_attempts a SET started_at=o.claimed_at-interval '1 microsecond' FROM callback_outbox o WHERE a.tenant=$1 AND a.delivery_id=$2 AND o.tenant=a.tenant AND o.delivery_id=a.delivery_id`, d)
	case "start_after_now":
		callbackRecoveryEvidenceUpdate(t, f, `UPDATE callback_attempts SET started_at=clock_timestamp()+interval '1 hour' WHERE tenant=$1 AND delivery_id=$2`, d)
	default:
		t.Fatal("unknown prepare evidence fixture mode")
	}
}

func callbackPrepareEvidenceEligible(t *testing.T, f *gwFixture, d cb.Delivery, prepared bool) {
	t.Helper()
	audits := int64(0)
	if prepared {
		audits = 1
	}
	if f.count(t, `SELECT count(*) FROM callback_outbox WHERE tenant=$1 AND delivery_id=$2 AND destination_id='dest_demo' AND state='leased' AND lease_owner=$3 AND lease_recovery_epoch=$4 AND fence=$5 AND attempt_count=$6 AND claimed_at IS NOT NULL AND claimed_at<=clock_timestamp() AND lease_until>clock_timestamp() AND hard_deadline_at>clock_timestamp() AND expires_at>clock_timestamp()`, d.Handle.Tenant, d.Handle.DeliveryID, d.Handle.Owner, d.Handle.Recovery, d.Handle.Fence, d.Handle.Attempt) != 1 ||
		f.count(t, `SELECT count(*) FROM callback_attempts WHERE tenant=$1 AND delivery_id=$2 AND fence=$3 AND owner_instance=$4 AND recovery_epoch=$5 AND attempt_no=$6 AND outcome='in_progress' AND finished_at IS NULL AND (send_prepared_at IS NOT NULL)=$7`, d.Handle.Tenant, d.Handle.DeliveryID, d.Handle.Fence, d.Handle.Owner, d.Handle.Recovery, d.Handle.Attempt, prepared) != 1 ||
		f.count(t, `SELECT count(DISTINCT s.scope_kind) FROM scope_slots s JOIN callback_outbox o ON o.tenant=s.tenant AND o.delivery_id=s.delivery_id WHERE o.tenant=$1 AND o.delivery_id=$2 AND s.owner_instance=o.lease_owner AND s.delivery_fence=o.fence AND s.recovery_epoch=o.lease_recovery_epoch AND s.lease_until=o.lease_until AND s.slot_fence>0 AND ((s.scope_kind='tenant' AND s.scope_id=o.tenant) OR (s.scope_kind='destination' AND s.scope_id=o.destination_id))`, d.Handle.Tenant, d.Handle.DeliveryID) != 2 ||
		f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 2 ||
		f.count(t, `SELECT count(*) FROM audit WHERE action='callback.prepared'`) != audits ||
		f.count(t, `SELECT count(*) FROM audit WHERE action IN ('callback.completed','callback.recovered')`) != 0 {
		t.Fatal("prepare fixture lacks live lease, exact slots, unfinished attempt or expected audits")
	}
}

func callbackPrepareEvidenceRefused(t *testing.T, p cb.Prepared, permit cb.Permit, err error) {
	t.Helper()
	if permit != (cb.Permit{}) || !errors.Is(err, cb.ErrStale) || err.Error() != cb.ErrStale.Error() || !time.Now().Before(p.Deadline) {
		t.Fatal("prepare did not return the exact empty stale refusal within its live deadline")
	}
}

func callbackPrepareEvidenceStableSnapshot(t *testing.T, f *gwFixture) string {
	t.Helper()
	var value string
	// A fresh Prepare may change only the marker and insert its matching audit.
	// All other rows retain xmin; omit xmin only on the one changed attempt.
	err := f.owner.QueryRow(f.ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,delivery_id) FROM callback_outbox x),
 (SELECT jsonb_agg(to_jsonb(x)-'send_prepared_at' ORDER BY tenant,delivery_id,fence) FROM callback_attempts x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY scope_kind,tenant,scope_id,slot_no) FROM scope_slots x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY id) FROM audit x WHERE action<>'callback.prepared'),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY scope_kind,tenant,scope_id) FROM scope_limits x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,reservation_id) FROM admission_reservations x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,id) FROM commands x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,event) FROM developer_callbacks x),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(x)) ORDER BY tenant,command) FROM callback_replay_receipts x)
 )::text`).Scan(&value)
	if err != nil {
		t.Fatal("prepare stable snapshot failed; redacted")
	}
	return value
}

// Claim is genuine PostgreSQL Claim, then owner DML injects the contradiction
// before returning that exact Delivery. No claim/read/clock result is mocked.
type callbackPrepareEvidenceSenderStore struct {
	*pg.CallbackStore
	t                           *testing.T
	f                           *gwFixture
	mode, before                string
	delivery                    cb.Delivery
	prepared                    cb.Prepared
	permit                      cb.Permit
	prepareErr                  error
	claims, prepares, completes int
}

func (s *callbackPrepareEvidenceSenderStore) Claim(ctx context.Context) (cb.Delivery, bool, error) {
	s.claims++
	d, ok, err := s.CallbackStore.Claim(ctx)
	if err != nil || !ok {
		s.t.Fatal("sender real claim failed; redacted")
	}
	s.delivery = d
	callbackPrepareEvidenceInject(s.t, s.f, d, s.mode)
	callbackPrepareEvidenceEligible(s.t, s.f, d, false)
	s.before = callbackRecoveryEvidenceSnapshot(s.t, s.f)
	return d, ok, err
}

func (s *callbackPrepareEvidenceSenderStore) Prepare(ctx context.Context, p cb.Prepared) (cb.Permit, error) {
	s.prepares++
	s.prepared = p
	s.permit, s.prepareErr = s.CallbackStore.Prepare(ctx, p)
	return s.permit, s.prepareErr
}

func (s *callbackPrepareEvidenceSenderStore) Complete(ctx context.Context, c cb.Completion) error {
	s.completes++
	return s.CallbackStore.Complete(ctx, c)
}

type callbackPrepareEvidenceKey struct {
	base     callbackFixtureKey
	resolved []byte
	calls    int
}

func (k *callbackPrepareEvidenceKey) Resolve(ctx context.Context, b cb.Binding) (cb.Key, error) {
	k.calls++
	key, err := k.base.Resolve(ctx, b)
	k.resolved = key.Bytes
	return key, err
}

// Pure AST regression for every SQL call authored in this file. It checks
// placeholder arity and tenant/delivery binding order, including the seven
// injections delegated to the existing exact-two-argument owner DML helper.
// This does not parse PostgreSQL syntax or establish real database behavior.
func TestCallbackPrepareEvidenceSQLBindings(t *testing.T) {
	inspect := func(source []byte) (checked, injections, problems int) {
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
			sqlIndex, bound, injection := 1, len(call.Args)-2, false
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fn.Sel.Name != "count" && fn.Sel.Name != "QueryRow" && fn.Sel.Name != "Exec" {
					return true
				}
			case *ast.Ident:
				if fn.Name != "callbackRecoveryEvidenceUpdate" {
					return true
				}
				injections++
				injection, sqlIndex, bound = true, 2, 2
			default:
				return true
			}
			checked++
			if len(call.Args) <= sqlIndex || call.Ellipsis.IsValid() {
				problems++
				return true
			}
			literal, ok := call.Args[sqlIndex].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				problems++
				return true
			}
			sql, err := strconv.Unquote(literal.Value)
			if err != nil {
				problems++
				return true
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
			if highest != bound || len(indices) != highest {
				problems++
			}
			if injection {
				if len(call.Args) != 4 {
					problems++
				} else if name, ok := call.Args[3].(*ast.Ident); !ok || name.Name != "d" {
					problems++
				}
			} else if strings.Contains(sql, "tenant=$1") && (strings.Contains(sql, "delivery_id=$2") || strings.Contains(sql, "target=$2")) {
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
		t.Fatal("prepare fixture source location unavailable")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("prepare fixture source unavailable")
	}
	if checked, injections, problems := inspect(source); checked != 17 || injections != 7 || problems != 0 {
		t.Fatal("prepare fixture SQL bindings incomplete or out of order")
	}
	for _, args := range []string{"", ",d.Handle.Tenant", ",d.Handle.DeliveryID,d.Handle.Tenant", ",d.Handle.Tenant,d.Handle.Tenant", ",d.Handle.Tenant,d.Handle.DeliveryID,true"} {
		fixture := []byte("package gateway; func fixture(){ f.count(t, \"SELECT count(*) FROM scope_slots WHERE tenant=$1 AND delivery_id=$2\"" + args + ") }")
		if checked, _, problems := inspect(fixture); checked != 1 || problems == 0 {
			t.Fatal("invalid synthetic prepare SQL bindings were accepted")
		}
	}
	for _, fixture := range []string{
		`package gateway; func fixture(){ f.count(t,"SELECT count(*) FROM scope_slots WHERE tenant=$1 AND delivery_id=$2",d.Handle.Tenant,d.Handle.DeliveryID) }`,
		`package gateway; func fixture(){ callbackRecoveryEvidenceUpdate(t,f,"UPDATE callback_attempts SET status_code=204 WHERE tenant=$1 AND delivery_id=$2",d) }`,
	} {
		if checked, _, problems := inspect([]byte(fixture)); checked != 1 || problems != 0 {
			t.Fatal("valid synthetic prepare SQL bindings were rejected")
		}
	}
	for _, fixture := range []string{
		`package gateway; func fixture(){ callbackRecoveryEvidenceUpdate(t,f,"UPDATE callback_attempts SET status_code=204 WHERE tenant=$1",d) }`,
		`package gateway; func fixture(){ callbackRecoveryEvidenceUpdate(t,f,"UPDATE callback_attempts SET status_code=204 WHERE tenant=$1 AND delivery_id=$3",d) }`,
		`package gateway; func fixture(){ callbackRecoveryEvidenceUpdate(t,f,"UPDATE callback_attempts SET status_code=204 WHERE tenant=$1 AND delivery_id=$2",other) }`,
	} {
		if checked, _, problems := inspect([]byte(fixture)); checked != 1 || problems == 0 {
			t.Fatal("invalid synthetic prepare injection bindings were accepted")
		}
	}
}
