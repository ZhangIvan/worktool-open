package pg

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func capacityModelFixture() capacitySnapshot {
	m := auditMeta{"tenant", true}
	s := capacitySnapshot{tenant: "tenant", observed: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)}
	s.scopes = []auditScope{{m, "tenant", "tenant", "", 3, 376832, 100, 10000000}, {m, "destination", "d1", "d1", 1, 90112, 100, 10000000}, {m, "destination", "d2", "d2", 1, 90112, 100, 10000000}}
	s.commands = []auditCommand{{auditMeta: m, id: "cmd", app: "app", status: "succeeded", effect: "applied", supported: true, result: true, snapshotValid: true, destinationCount: 2, destinations: [4]string{"d1", "d2"}}}
	s.reservations = []auditReservation{{auditMeta: m, id: "result", command: "cmd", kind: "result", state: "materialized", jobs: 1, bytes: 196608, resultIdentity: true}}
	for i := 1; i <= 2; i++ {
		d := fmt.Sprintf("d%d", i)
		r := fmt.Sprintf("r%d", i)
		delivery := fmt.Sprintf("delivery%d", i)
		b := auditBinding{"app", fmt.Sprintf("sub%d", i), d, 1, 1, 1, 4, 0}
		s.reservations = append(s.reservations, auditReservation{auditMeta: m, id: r, command: "cmd", kind: "callback", state: "materialized", delivery: delivery, event: "event", binding: b, jobs: 1, bytes: 90112})
		s.deliveries = append(s.deliveries, auditDelivery{auditMeta: m, id: delivery, reservation: r, command: "cmd", event: "event", state: "delivered", binding: b, leaseValid: true, fence: 1})
	}
	s.events = []auditEvent{{m, "event", "cmd", "app", true, true}}
	return s
}
func assertCapacityStatus(t *testing.T, s capacitySnapshot, want CapacityAuditStatus) CapacityAudit {
	t.Helper()
	r := reduceCapacitySnapshot(s)
	if r.Status != want || r.ReleasableJobs != 0 || r.ReleasableBytes != 0 {
		t.Fatalf("status=%s expected=%s issues=%v", r.Status, want, r.Issues)
	}
	return r
}
func TestRetentionAuditConservationAndHolds(t *testing.T) {
	s := capacityModelFixture()
	m := auditMeta{s.tenant, true}
	s.attempts = []auditAttempt{{m, "delivery1", "delivery_unknown", 1, 1, true}}
	s.slots = []auditSlot{{m, "destination", "d1", "delivery1", 0, 1, 1, true, true}}
	s.intents = []auditIntent{{m, 1, "cmd", "result_ack", "pending"}}
	r := assertCapacityStatus(t, s, CapacityConserved)
	for _, reason := range []string{"RETENTION_POLICY_NOT_IMPLEMENTED", "REPLAY_RETENTION_NOT_PROVEN", "RETENTION_DEADLINE_UNSET", "SHARED_EVENT_COHORT", "CALLBACK_ATTEMPT_UNRESOLVED", "CALLBACK_SLOT_REFERENCE", "OUTBOX_SETTLEMENT_NOT_PROVEN"} {
		if !slices.Contains(r.Holds, reason) {
			t.Fatal("missing hold", reason)
		}
	}
	if len(r.Scopes) != 3 {
		t.Fatal("separate accounting views missing")
	}
	for _, v := range r.Scopes {
		if v.Kind == "tenant" && (v.ExpectedJobs != 3 || v.ExpectedBytes != 376832) {
			t.Fatal("shared event deduplicated callback charge")
		}
	}
	if strings.Contains(fmt.Sprint(r), "cmd") || strings.Contains(fmt.Sprint(r), "tenant") {
		t.Fatal("String leaked identity")
	}
}
func TestRetentionAuditLifecycleCombinations(t *testing.T) {
	states := []string{"held", "materialized", "cancelled_retained", "purged"}
	for _, a := range states {
		for _, b := range states {
			for _, c := range states {
				t.Run(a+"_"+b+"_"+c, func(t *testing.T) {
					s := capacityModelFixture()
					s.deliveries = nil
					for i, state := range []string{a, b, c} {
						v := &s.reservations[i]
						v.state = state
						v.released = state == "purged"
						v.reason = v.released
						if i > 0 && state == "materialized" {
							orig := capacityModelFixture()
							s.deliveries = append(s.deliveries, orig.deliveries[i-1])
						} else {
							v.delivery = ""
							v.event = ""
						}
						if state == "purged" {
							s.scopes[0].jobs--
							s.scopes[0].bytes -= v.bytes
							if i > 0 {
								s.scopes[i].jobs = 0
								s.scopes[i].bytes = 0
							}
						}
					}
					assertCapacityStatus(t, s, CapacityConserved)
				})
			}
		}
	}
}
func TestRetentionAuditConflictMatrix(t *testing.T) {
	tests := []struct {
		name   string
		change func(*capacitySnapshot)
	}{
		{"missing_tenant", func(s *capacitySnapshot) { s.scopes = s.scopes[1:] }},
		{"missing_destination", func(s *capacitySnapshot) { s.scopes = s.scopes[:2] }},
		{"missing_command", func(s *capacitySnapshot) { s.commands = nil }},
		{"missing_result", func(s *capacitySnapshot) { s.reservations = s.reservations[1:] }},
		{"missing_body", func(s *capacitySnapshot) { s.events[0].body = false }},
		{"missing_materialized_result", func(s *capacitySnapshot) { s.commands[0].result = false }},
		{"missing_delivery", func(s *capacitySnapshot) { s.deliveries = s.deliveries[:1] }},
		{"missing_event", func(s *capacitySnapshot) { s.events = nil }},
		{"mismatched_command_app", func(s *capacitySnapshot) { s.commands[0].app = "other" }},
		{"mismatched_event_app", func(s *capacitySnapshot) { s.events[0].app = "other" }},
		{"mismatched_delivery_policy", func(s *capacitySnapshot) { s.deliveries[0].binding.policy++ }},
		{"mismatched_reservation_subscription", func(s *capacitySnapshot) { s.reservations[1].binding.subscription = "other" }},
		{"mismatched_snapshot", func(s *capacitySnapshot) { s.commands[0].destinations[0] = "other" }},
		{"duplicate_snapshot", func(s *capacitySnapshot) { s.commands[0].destinations[0] = "d2" }},
		{"invalid_snapshot_size", func(s *capacitySnapshot) { s.commands[0].destinationCount = 5 }},
		{"invalid_snapshot_shape", func(s *capacitySnapshot) { s.commands[0].snapshotValid = false }},
		{"duplicate_scope", func(s *capacitySnapshot) { s.scopes = append(s.scopes, s.scopes[0]) }},
		{"duplicate_reservation", func(s *capacitySnapshot) { s.reservations = append(s.reservations, s.reservations[0]) }},
		{"duplicate_command", func(s *capacitySnapshot) { s.commands = append(s.commands, s.commands[0]) }},
		{"duplicate_delivery", func(s *capacitySnapshot) { s.deliveries = append(s.deliveries, s.deliveries[0]) }},
		{"duplicate_event", func(s *capacitySnapshot) { s.events = append(s.events, s.events[0]) }},
		{"negative_counter", func(s *capacitySnapshot) { s.scopes[0].jobs = -1 }},
		{"above_capacity", func(s *capacitySnapshot) { s.scopes[0].capacityBytes = 0 }},
		{"invalid_scope_identity", func(s *capacitySnapshot) { s.scopes[0].id = "other" }},
		{"invalid_amount", func(s *capacitySnapshot) { s.reservations[0].bytes = math.MaxInt64 }},
		{"invalid_jobs", func(s *capacitySnapshot) { s.reservations[0].jobs = -1 }},
		{"invalid_kind", func(s *capacitySnapshot) { s.reservations[0].kind = "" }},
		{"invalid_state", func(s *capacitySnapshot) { s.reservations[0].state = "" }},
		{"invalid_release", func(s *capacitySnapshot) { s.reservations[0].released = true }},
		{"live_release_reason", func(s *capacitySnapshot) { s.reservations[0].reason = true }},
		{"invalid_result_binding", func(s *capacitySnapshot) { s.reservations[0].binding.app = "app" }},
		{"invalid_callback_attempt_limit", func(s *capacitySnapshot) { s.reservations[1].binding.attempts = 5 }},
		{"invalid_callback_ordinal", func(s *capacitySnapshot) { s.reservations[1].binding.ordinal = 1 }},
		{"cross_tenant", func(s *capacitySnapshot) { s.reservations[0].tenant = "other" }},
		{"oversize_metadata_flag", func(s *capacitySnapshot) { s.commands[0].valid = false }},
		{"invalid_delivery_lease", func(s *capacitySnapshot) { s.deliveries[0].leaseValid = false }},
		{"invalid_delivery_state", func(s *capacitySnapshot) { s.deliveries[0].state = "" }},
		{"orphan_attempt", func(s *capacitySnapshot) {
			s.attempts = []auditAttempt{{auditMeta{s.tenant, true}, "missing", "delivered", 1, 1, true}}
		}},
		{"invalid_attempt_finish", func(s *capacitySnapshot) {
			s.attempts = []auditAttempt{{auditMeta{s.tenant, true}, "delivery1", "in_progress", 1, 1, true}}
		}},
		{"future_attempt_fence", func(s *capacitySnapshot) {
			s.attempts = []auditAttempt{{auditMeta{s.tenant, true}, "delivery1", "delivered", 2, 1, true}}
		}},
		{"orphan_slot", func(s *capacitySnapshot) {
			s.slots = []auditSlot{{auditMeta{s.tenant, true}, "destination", "d1", "missing", 0, 1, 1, true, true}}
		}},
		{"invalid_slot_scope", func(s *capacitySnapshot) {
			s.slots = []auditSlot{{auditMeta{s.tenant, true}, "destination", "missing", "", 0, 0, 0, true, false}}
		}},
		{"orphan_intent", func(s *capacitySnapshot) {
			s.intents = []auditIntent{{auditMeta{s.tenant, true}, 1, "missing", "result_ack", "pending"}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := capacityModelFixture()
			tc.change(&s)
			r := assertCapacityStatus(t, s, CapacityIntegrityConflict)
			if len(r.Scopes) != 0 {
				t.Fatal("invalid graph exposed usable totals")
			}
		})
	}
}
func TestRetentionAuditStatusesAndNoEligibility(t *testing.T) {
	for _, where := range []string{"command", "event"} {
		t.Run(where, func(t *testing.T) {
			s := capacityModelFixture()
			if where == "command" {
				s.commands[0].supported = false
			} else {
				s.events[0].managed = false
			}
			r := assertCapacityStatus(t, s, CapacityUnsupported)
			if len(r.Scopes) > 0 || !r.ObservedAt.IsZero() {
				t.Fatal("unsupported candidate exposed")
			}
		})
	}
	s := capacityModelFixture()
	s.scopes[0].bytes--
	s.scopes[1].bytes++
	assertCapacityStatus(t, s, CapacityDrift)
	s = capacityModelFixture()
	s.scopes = append(s.scopes, auditScope{auditMeta{s.tenant, true}, "destination", "unused", "unused", 1, 1, 100, 10000000})
	assertCapacityStatus(t, s, CapacityDrift)
	s = capacityModelFixture()
	past := s.observed.Add(-time.Hour)
	for i := range s.reservations {
		s.reservations[i].retain = &past
	}
	assertCapacityStatus(t, s, CapacityConserved)
	s.commands[0].effect = "unknown"
	r := assertCapacityStatus(t, s, CapacityConserved)
	if !slices.Contains(r.Holds, "COMMAND_UNSETTLED_OR_UNKNOWN") {
		t.Fatal("unknown effect not withheld")
	}
	s = capacityModelFixture()
	s.deliveries = nil
	for i := 1; i < 3; i++ {
		v := &s.reservations[i]
		v.state = "purged"
		v.released = true
		v.reason = true
		v.delivery = ""
		v.event = ""
		s.scopes[i].jobs = 0
		s.scopes[i].bytes = 0
	}
	s.scopes[0].jobs = 1
	s.scopes[0].bytes = 196608
	r = assertCapacityStatus(t, s, CapacityConserved)
	for _, h := range []string{"UNREFERENCED_EVENT_BODY_RETAINED", "MIXED_PURGED_REPLAY_GRAPH"} {
		if !slices.Contains(r.Holds, h) {
			t.Fatal("missing hold", h)
		}
	}
}
func TestRetentionAuditDeterminismAndCheckedArithmetic(t *testing.T) {
	s := capacityModelFixture()
	before := fmt.Sprintf("%#v", s)
	r := reduceCapacitySnapshot(s)
	if before != fmt.Sprintf("%#v", s) {
		t.Fatal("mutated model")
	}
	slices.Reverse(s.scopes)
	slices.Reverse(s.reservations)
	slices.Reverse(s.deliveries)
	if !reflect.DeepEqual(r, reduceCapacitySnapshot(s)) {
		t.Fatal("order-dependent result")
	}
	for _, v := range [][2]int64{{math.MaxInt64, 1}, {-1, 0}, {0, -1}} {
		if _, ok := auditAdd(v[0], v[1]); ok {
			t.Fatal("unchecked arithmetic")
		}
	}
	if n, ok := auditAdd(math.MaxInt64-1, 1); !ok || n != math.MaxInt64 {
		t.Fatal("valid bound rejected")
	}
}

// Scripted pgx boundary fake. It validates call contracts, not SQL syntax or MVCC.
type auditFake struct {
	pgx.Tx
	data                                                                          [8][][]any
	calls                                                                         []string
	rows                                                                          []*auditFakeRows
	options                                                                       pgx.TxOptions
	now                                                                           time.Time
	queryFail, scanFail, rowsFail                                                 int
	execErr, timeErr, beginErr, acquireErr, commitErr, rollbackErr, discardErr    error
	acquires, begins, commits, rollbacks, discards, releases, gateCalls, denyGate int
	afterCommit, afterRollback                                                    func()
}
type auditFakeRows struct {
	pgx.Rows
	data         [][]any
	index, scans int
	closed       bool
	scanErr, err error
}

func (r *auditFakeRows) Next() bool {
	if r.index < len(r.data) {
		r.index++
		return true
	}
	return false
}
func (r *auditFakeRows) Scan(dst ...any) error {
	r.scans++
	return (callbackTestRow{values: r.data[r.index-1], err: r.scanErr}).Scan(dst...)
}
func (r *auditFakeRows) Close()     { r.closed = true }
func (r *auditFakeRows) Err() error { return r.err }
func (f *auditFake) Acquire(context.Context) (Connection, error) {
	f.acquires++
	return f, f.acquireErr
}
func (f *auditFake) BeginTx(_ context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	f.begins++
	f.options = o
	return f, f.beginErr
}
func (f *auditFake) Release()                      { f.releases++ }
func (f *auditFake) Discard(context.Context) error { f.discards++; return f.discardErr }
func (f *auditFake) Commit(context.Context) error {
	f.commits++
	if f.afterCommit != nil {
		f.afterCommit()
	}
	return f.commitErr
}
func (f *auditFake) Rollback(context.Context) error {
	f.rollbacks++
	if f.afterRollback != nil {
		f.afterRollback()
	}
	return f.rollbackErr
}
func (f *auditFake) Exec(_ context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	f.calls = append(f.calls, q)
	if len(args) != 0 || !(q == "SET LOCAL statement_timeout='1500ms'" || q == "SET LOCAL lock_timeout='500ms'") {
		panic("unexpected audit exec")
	}
	return pgconn.CommandTag{}, f.execErr
}
func (f *auditFake) QueryRow(_ context.Context, q string, args ...any) pgx.Row {
	f.calls = append(f.calls, q)
	if q != "SELECT transaction_timestamp()" || len(args) != 0 {
		panic("unexpected audit time query")
	}
	return callbackTestRow{values: []any{f.now}, err: f.timeErr}
}
func (f *auditFake) Query(_ context.Context, q string, args ...any) (pgx.Rows, error) {
	f.calls = append(f.calls, q)
	i := slices.Index(capacityAuditQueries[:], q)
	if i < 0 || !reflect.DeepEqual(args, []any{"tenant"}) {
		panic("unexpected audit query")
	}
	if f.queryFail == i {
		return nil, errors.New("private SQL driver error")
	}
	r := &auditFakeRows{data: f.data[i]}
	if f.scanFail == i {
		r.scanErr = errors.New("private scan error")
	}
	if f.rowsFail == i {
		r.err = errors.New("private rows error")
	}
	f.rows = append(f.rows, r)
	return r, nil
}
func (f *auditFake) gate(context.Context) error {
	f.gateCalls++
	if f.gateCalls == f.denyGate {
		return errors.New("private recovery state")
	}
	return nil
}
func bindingValues(b auditBinding) []any {
	return []any{b.app, b.subscription, b.destination, b.version, b.policy, b.key, b.attempts, b.ordinal}
}
func capacityFakeData(s capacitySnapshot) [8][][]any {
	var d [8][][]any
	for _, r := range s.scopes {
		d[0] = append(d[0], []any{r.tenant, r.valid, r.kind, r.id, r.destination, r.jobs, r.bytes, r.capacityJobs, r.capacityBytes})
	}
	for _, r := range s.reservations {
		x := []any{r.tenant, r.valid, r.id, r.command, r.kind, r.state, r.delivery, r.event}
		x = append(x, bindingValues(r.binding)...)
		d[1] = append(d[1], append(x, r.jobs, r.bytes, r.retain, r.released, r.reason, r.resultIdentity))
	}
	for _, r := range s.commands {
		d[2] = append(d[2], []any{r.tenant, r.valid, r.id, r.app, r.status, r.effect, r.supported, r.result, r.snapshotValid, r.destinationCount, r.destinations[0], r.destinations[1], r.destinations[2], r.destinations[3]})
	}
	for _, r := range s.deliveries {
		x := []any{r.tenant, r.valid, r.id, r.reservation, r.command, r.event, r.state}
		x = append(x, bindingValues(r.binding)...)
		d[3] = append(d[3], append(x, r.leaseValid, r.leased, r.fence))
	}
	for _, r := range s.events {
		d[4] = append(d[4], []any{r.tenant, r.valid, r.id, r.command, r.app, r.body, r.managed})
	}
	for _, r := range s.attempts {
		d[5] = append(d[5], []any{r.tenant, r.valid, r.delivery, r.outcome, r.fence, r.number, r.finished})
	}
	for _, r := range s.slots {
		d[6] = append(d[6], []any{r.tenant, r.valid, r.kind, r.id, r.delivery, r.number, r.deliveryFence, r.slotFence, r.ownershipValid, r.occupied})
	}
	for _, r := range s.intents {
		d[7] = append(d[7], []any{r.tenant, r.valid, r.id, r.command, r.kind, r.state})
	}
	return d
}
func newCapacityFake(t *testing.T) (*RetentionAuditor, *auditFake, TrustedCapacityTenant) {
	t.Helper()
	s := capacityModelFixture()
	f := &auditFake{data: capacityFakeData(s), now: s.observed, queryFail: -1, scanFail: -1, rowsFail: -1}
	r, err := NewRunner(f, Timeouts{time.Second, time.Second, 2 * time.Second, time.Second, time.Second})
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewRetentionAuditor(r, f.gate)
	if err != nil {
		t.Fatal(err)
	}
	tenant, _ := CapacityTenantFromAuthorizedRead("tenant")
	return a, f, tenant
}
func TestRetentionAuditRunnerContract(t *testing.T) {
	a, f, tenant := newCapacityFake(t)
	r, e := a.AuditRetainedCapacity(context.Background(), tenant)
	if e != nil || r.Status != CapacityConserved || f.options != (pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}) || f.acquires != 1 || f.begins != 1 || f.commits != 1 || f.rollbacks != 1 || f.releases != 1 || len(f.rows) != 8 || len(f.calls) != 11 {
		t.Fatalf("wrong lifecycle: %v %s", e, r.Status)
	}
	for _, rows := range f.rows {
		if !rows.closed {
			t.Fatal("rows left open")
		}
	}
}
func TestRetentionAuditFailureSuppression(t *testing.T) {
	secret := errors.New("secret driver details")
	changes := map[string]func(*auditFake){"acquire": func(f *auditFake) { f.acquireErr = secret }, "begin": func(f *auditFake) { f.beginErr = secret }, "set_local": func(f *auditFake) { f.execErr = secret }, "timestamp": func(f *auditFake) { f.timeErr = secret }, "commit": func(f *auditFake) { f.commitErr = secret }, "cleanup": func(f *auditFake) { f.rollbackErr = secret }, "discard": func(f *auditFake) { f.commitErr = secret; f.discardErr = secret }}
	for i := 0; i < 8; i++ {
		i := i
		changes[fmt.Sprintf("query_%d", i)] = func(f *auditFake) { f.queryFail = i }
		changes[fmt.Sprintf("rows_%d", i)] = func(f *auditFake) { f.rowsFail = i }
	}
	changes["scan"] = func(f *auditFake) { f.scanFail = 0 }
	for i := 1; i <= 12; i++ {
		i := i
		changes[fmt.Sprintf("gate_%d", i)] = func(f *auditFake) { f.denyGate = i }
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			a, f, tenant := newCapacityFake(t)
			change(f)
			r, e := a.AuditRetainedCapacity(context.Background(), tenant)
			if e == nil || r.Status != CapacityUnavailable || len(r.Scopes) > 0 || len(r.Holds) > 0 || !r.ObservedAt.IsZero() || strings.Contains(e.Error(), "secret") {
				t.Fatal("candidate or details escaped", r, e)
			}
			for _, rows := range f.rows {
				if !rows.closed {
					t.Fatal("rows leaked")
				}
			}
			if f.acquires > 1 || f.commits > 1 {
				t.Fatal("audit retried")
			}
			if name == "cleanup" && !errors.Is(e, ErrCleanupUnconfirmed) {
				t.Fatal("lost cleanup identity")
			}
			if name == "commit" && !errors.Is(e, ErrCommitOutcomeUnknown) {
				t.Fatal("lost commit identity")
			}
		})
	}
}
func TestRetentionAuditCancellationAfterCleanup(t *testing.T) {
	for _, stage := range []string{"before", "commit", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			a, f, tenant := newCapacityFake(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "before":
				cancel()
			case "commit":
				f.afterCommit = cancel
			case "cleanup":
				f.afterRollback = cancel
			}
			r, e := a.AuditRetainedCapacity(ctx, tenant)
			if !errors.Is(e, context.Canceled) || r.Status != CapacityUnavailable || len(r.Scopes) > 0 {
				t.Fatal("cancelled candidate escaped")
			}
		})
	}
}
func TestRetentionAuditRowAndByteBounds(t *testing.T) {
	for _, count := range []int{retentionAuditRowCap, retentionAuditRowCap + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			a, f, tenant := newCapacityFake(t)
			f.data = [8][][]any{}
			for i := 0; i < count; i++ {
				kind, id, d := "destination", fmt.Sprint(i), fmt.Sprint(i)
				if i == 0 {
					kind, id, d = "tenant", "tenant", ""
				}
				f.data[0] = append(f.data[0], []any{"tenant", true, kind, id, d, int64(0), int64(0), int64(100), int64(1000000)})
			}
			r, e := a.AuditRetainedCapacity(context.Background(), tenant)
			want := CapacityConserved
			if count > retentionAuditRowCap {
				want = CapacityIncomplete
			}
			if e != nil || r.Status != want {
				t.Fatal(r, e)
			}
			if f.rows[0].scans != retentionAuditRowCap {
				t.Fatal("scanned sentinel row")
			}
			if want == CapacityIncomplete && (len(r.Scopes) > 0 || len(f.rows) != 1 || f.commits != 0) {
				t.Fatal("partial totals/continued reads")
			}
		})
	}
	a, f, tenant := newCapacityFake(t)
	for len(f.data[0]) < 1024 {
		f.data[0] = append(f.data[0], f.data[0][0])
	}
	for len(f.data[1]) < 1024 {
		f.data[1] = append(f.data[1], f.data[1][0])
	}
	r, e := a.AuditRetainedCapacity(context.Background(), tenant)
	if e != nil || r.Status != CapacityIncomplete || len(r.Scopes) > 0 || f.rows[2].scans != 0 {
		t.Fatal("byte budget not checked before scan")
	}
	f.rollbackErr = errors.New("cleanup uncertain")
	r, e = a.AuditRetainedCapacity(context.Background(), tenant)
	if r.Status != CapacityUnavailable || !errors.Is(e, ErrCleanupUnconfirmed) {
		t.Fatal("incomplete masked cleanup failure")
	}
}
func TestRetentionAuditTrustedConstruction(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), string([]byte{0xff})} {
		if _, e := CapacityTenantFromAuthorizedRead(id); e == nil {
			t.Fatal("invalid trusted identity")
		}
	}
	if _, e := NewRetentionAuditor(nil, nil); e == nil {
		t.Fatal("nil runner allowed")
	}
	a, f, _ := newCapacityFake(t)
	for _, ctx := range []context.Context{nil, context.Background()} {
		r, e := a.AuditRetainedCapacity(ctx, TrustedCapacityTenant{})
		if e == nil || r.Status != CapacityUnavailable || f.acquires != 0 {
			t.Fatal("untrusted input acquired")
		}
	}
}
func TestRetentionAuditSQLInventory(t *testing.T) {
	for i, q := range capacityAuditQueries {
		u := strings.ToUpper(q)
		if !strings.HasPrefix(u, "SELECT ") || !strings.HasSuffix(u, " WHERE TENANT=$1 LIMIT 1025") {
			t.Fatal("missing independent cap", i)
		}
		for _, bad := range []string{" FOR UPDATE", " FOR SHARE", "SKIP LOCKED", "JOIN ", "GROUP BY", "ORDER BY", "SUM(", "COUNT(", "INSERT ", "DELETE ", "UPDATE ", "ADVISORY", "KEY_REF", "CANONICAL_URL", "BODY_BYTES", "PAYLOAD", "ERROR_CODE"} {
			if strings.Contains(u, bad) {
				t.Fatalf("unbounded/sensitive/locking query %d token %s", i, bad)
			}
		}
		if !strings.Contains(u, "OCTET_LENGTH(TENANT)") {
			t.Fatal("unbounded tenant")
		}
	}
	if !strings.Contains(capacityAuditQueries[2], "16384") || strings.Contains(capacityAuditQueries[2], "THEN subscription_snapshot ") {
		t.Fatal("raw snapshot transfer")
	}
	if !strings.Contains(capacityAuditQueries[4], "body IS NOT NULL") {
		t.Fatal("body presence absent")
	}
}

func TestRetentionAuditClockAndPurgedResultBody(t *testing.T) {
	for _, now := range []time.Time{{}, time.Unix(-1, 0), time.UnixMilli(maxWireMillis).Add(time.Millisecond)} {
		a, f, tenant := newCapacityFake(t)
		f.now = now
		r, e := a.AuditRetainedCapacity(context.Background(), tenant)
		if e == nil || r.Status != CapacityUnavailable || len(f.rows) != 0 || !r.ObservedAt.IsZero() {
			t.Fatal("invalid clock confirmed an audit")
		}
	}
	s := capacityModelFixture()
	r := &s.reservations[0]
	r.state = "purged"
	r.released = true
	r.reason = true
	s.scopes[0].jobs--
	s.scopes[0].bytes -= 196608
	report := assertCapacityStatus(t, s, CapacityConserved)
	if !slices.Contains(report.Holds, "RESULT_BODY_RETAINED_AFTER_PURGE") {
		t.Fatal("purged retained result body not distinguished")
	}
}

type auditCloseErrorRows struct {
	*auditFakeRows
	afterClose func()
}

func (r *auditCloseErrorRows) Close() {
	r.auditFakeRows.Close()
	if r.afterClose != nil {
		r.afterClose()
	}
}

type auditSingleRowsTx struct {
	pgx.Tx
	rows pgx.Rows
}

func (tx auditSingleRowsTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return tx.rows, nil
}
func TestRetentionAuditSentinelCloseErrorAndCancellation(t *testing.T) {
	for _, mode := range []string{"rows_error", "transaction_cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := &auditFakeRows{data: [][]any{{}}}
			rows := &auditCloseErrorRows{auditFakeRows: base}
			sentinel := errors.New("close/drain failed")
			if mode == "rows_error" {
				rows.afterClose = func() { base.err = sentinel }
			} else {
				rows.afterClose = cancel
			}
			budget := 0
			_, err := readCapacityRows(ctx, auditSingleRowsTx{rows: rows}, "synthetic", "tenant", &budget, func(pgx.Rows) (int, error) { t.Fatal("scanned after exhausted budget"); return 0, nil }, func(ctx context.Context) error { return ctx.Err() })
			if err == nil || errors.Is(err, errCapacityAuditBound) || !base.closed {
				t.Fatal("bounded abort masked unconfirmed read")
			}
			if mode == "rows_error" && !errors.Is(err, sentinel) {
				t.Fatal("close read error lost")
			}
			if mode == "transaction_cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("transaction cancellation lost")
			}
		})
	}
}
