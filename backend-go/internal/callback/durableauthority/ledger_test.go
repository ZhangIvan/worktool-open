package durableauthority

import (
	"context"
	"errors"
	"fmt"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
)

func TestIntentAndOccupiedScope(t *testing.T) {
	_, _, b := config(t)
	a, s := adapter(t, intentSteps(b))
	out, err := a.recordIntent(context.Background(), b)
	if err != nil || out.Classification != "INTENT_RECORDED_NO_AUTHORITY" {
		t.Fatal(out, err)
	}
	s.done(1)
	for _, state := range []string{"running", "halted"} {
		t.Run(state, func(t *testing.T) {
			code := ""
			if state == "halted" {
				code = "UNCERTAINTY"
			}
			a, s := adapter(t, append(baseSteps(b), ledgerStep(b, state, code)))
			out, err := a.recordIntent(context.Background(), b)
			if !errors.Is(err, ErrConflict) || out != (Evidence{}) {
				t.Fatal("occupied scope accepted", err)
			}
			s.done(0)
		})
	}
	for _, field := range []string{"allocation", "instance", "boot", "epoch", "revision", "scope", "plan", "provider"} {
		t.Run("changed_"+field, func(t *testing.T) {
			_, ds, original := config(t)
			i := original.identity
			switch field {
			case "allocation":
				i.Allocation = "new_allocation"
			case "instance":
				i.Instance = "new_instance"
			case "boot":
				i.Boot = "new_boot"
			case "epoch":
				i.Epoch = "new_epoch"
			case "revision":
				i.Revision++
			case "scope":
				i.Scope = "another_partition"
			}
			p, _, _ := config(t)
			candidate, e := Bind(p, ds, i)
			if e != nil {
				t.Fatal(e)
			}
			if field == "plan" || field == "provider" {
				candidate.digest = strings.Repeat("a", 64)
			}
			steps := baseSteps(original)
			steps[1].args = []any{candidate.identity.Scope}
			a, s := adapter(t, steps)
			out, err := a.recordIntent(context.Background(), candidate)
			if !errors.Is(err, ErrInvalid) || out != (Evidence{}) {
				t.Fatal("changed binding accepted", err)
			}
			s.done(0)
		})
	}
}

func TestRegistryAndLedgerGraphFailures(t *testing.T) {
	_, _, b := config(t)
	cases := map[string][]step{}
	missing := baseSteps(b)
	missing[1].values = nil
	missing[1].err = pgx.ErrNoRows
	cases["missing_registry"] = missing
	malformed := baseSteps(b)
	malformed[1].values[9] = []byte(`{}`)
	cases["malformed_plan"] = malformed
	descriptors := baseSteps(b)
	descriptors[1].values[10] = []byte(`[]`)
	cases["missing_descriptors"] = descriptors
	changed := append(baseSteps(b), ledgerStep(b, "running", ""))
	changed[2].values[2] = "other_instance"
	cases["mismatched_ledger"] = changed
	badState := append(baseSteps(b), ledgerStep(b, "halted", ""))
	cases["malformed_ledger"] = badState
	orphanHalt := append(baseSteps(b), ledgerStep(b, "halted", "JOINED"))
	orphanHalt[2].values[10] = false
	cases["halt_without_matching_event"] = orphanHalt
	terminalRunning := append(baseSteps(b), ledgerStep(b, "running", ""))
	terminalRunning[2].values[9] = true
	cases["running_with_terminal_event"] = terminalRunning
	cases["orphan_events_without_ledger"] = append(baseSteps(b), absentLedger(b), step{op: "row", hash: hashanyEventsSQL, args: []any{b.identity.Scope}, values: []any{true}})
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			a, s := adapter(t, steps)
			out, err := a.recordIntent(context.Background(), b)
			if err == nil || out != (Evidence{}) {
				t.Fatal("graph accepted")
			}
			s.done(0)
		})
	}
}

func TestAppendEventsHistoricalConflictAndReconciliation(t *testing.T) {
	_, _, b := config(t)
	for _, e := range []event{{"op_join", "joined", "JOINED", "evidence_one"}, {"op_incident", "incident", "UNCERTAINTY", "evidence_two"}, {"op_reconcile", "reconciliation_fact", "EVIDENCE_RECORDED", "evidence_three"}} {
		t.Run(e.kind, func(t *testing.T) {
			a, s := adapter(t, eventSteps(b, e, "running", ""))
			out, err := a.recordEvent(context.Background(), b, e)
			if err != nil || out.Classification != "FACT_RECORDED_NO_AUTHORITY" {
				t.Fatal(out, err)
			}
			s.done(1)
			state, code := "halted", e.code
			if e.kind == "reconciliation_fact" {
				state, code = "running", ""
			}
			a, s = adapter(t, append(baseSteps(b), ledgerStep(b, state, code), eventStep(b, e)))
			out, err = a.recordEvent(context.Background(), b, e)
			if err != nil || out.Classification != "HISTORICAL_FACT_NO_AUTHORITY" {
				t.Fatal(out, err)
			}
			s.done(1)
		})
	}
	for _, test := range []struct {
		name, oldCode string
		e             event
	}{{"joined_then_incident", "JOINED", event{"op_late", "incident", "CLEANUP_UNCONFIRMED", "late_evidence"}}, {"incident_then_joined", "UNCERTAINTY", event{"op_join", "joined", "JOINED", "join_evidence"}}} {
		t.Run(test.name, func(t *testing.T) {
			a, s := adapter(t, eventSteps(b, test.e, "halted", test.oldCode))
			out, err := a.recordEvent(context.Background(), b, test.e)
			if err != nil || out.Classification != "FACT_RECORDED_NO_AUTHORITY" {
				t.Fatal(out, err)
			}
			s.done(1)
			a, s = adapter(t, append(baseSteps(b), ledgerStep(b, "halted", test.oldCode), step{op: "row", hash: hashincidentsSQL, args: []any{b.identity.Scope}, values: []any{true}}))
			out, err = a.Observe(context.Background(), b)
			if err != nil || out.Classification != "INCIDENT_BLOCKED" {
				t.Fatal(out, err)
			}
			s.done(1)
		})
	}
	e := event{"global_operation", "incident", "UNCERTAINTY", "evidence"}
	for _, index := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9} {
		t.Run("changed_operation_payload_"+string(rune('a'+index)), func(t *testing.T) {
			prior := eventStep(b, e)
			if index == 5 {
				prior.values[index] = int64(2)
			} else {
				prior.values[index] = "changed"
			}
			a, s := adapter(t, append(baseSteps(b), ledgerStep(b, "halted", "UNCERTAINTY"), prior))
			out, err := a.recordEvent(context.Background(), b, e)
			if !errors.Is(err, ErrConflict) || out != (Evidence{}) {
				t.Fatal("operation reuse accepted", err)
			}
			s.done(0)
		})
	}
	a, s := adapter(t, append(baseSteps(b), absentLedger(b)))
	out, err := a.recordEvent(context.Background(), b, e)
	if !errors.Is(err, ErrMissing) || out != (Evidence{}) {
		t.Fatal("event without intent", err)
	}
	s.done(0)
}

func TestObservationOnlyNeverAcquires(t *testing.T) {
	_, _, b := config(t)
	for _, test := range []struct {
		state, code, want string
		incident          bool
	}{{"", "", "INTENT_ABSENT_NO_AUTHORITY", false}, {"running", "", "RUNNING_INTENT_BLOCKED", false}, {"halted", "JOINED", "HALTED_BLOCKED", false}, {"halted", "JOINED", "INCIDENT_BLOCKED", true}} {
		t.Run(test.want, func(t *testing.T) {
			ss := baseSteps(b)
			if test.state == "" {
				ss = append(ss, absentLedger(b))
			} else {
				ss = append(ss, ledgerStep(b, test.state, test.code), step{op: "row", hash: hashincidentsSQL, args: []any{b.identity.Scope}, values: []any{test.incident}})
			}
			a, s := adapter(t, ss)
			out, err := a.Observe(context.Background(), b)
			if err != nil || out.Classification != test.want {
				t.Fatal(out, err)
			}
			s.done(1)
		})
	}
	// Separate newly constructed adapter sees occupied scripted durable content.
	// This is SQL/control-flow evidence, not real database restart durability.
	a, s := adapter(t, append(baseSteps(b), ledgerStep(b, "halted", "UNCERTAINTY")))
	out, err := a.recordIntent(context.Background(), b)
	if !errors.Is(err, ErrConflict) || out != (Evidence{}) {
		t.Fatal("new adapter acquired")
	}
	s.done(0)
}

func TestMutationFailureAndUnknownCleanupNeverReceipt(t *testing.T) {
	_, _, b := config(t)
	private := errors.New("PRIVATE driver URL and credential")
	for _, test := range []struct {
		name  string
		index int
		rows  bool
	}{{"insert_error", 3, false}, {"insert_no_row", 3, true}, {"constraints", 4, false}, {"readback", 5, false}} {
		t.Run(test.name, func(t *testing.T) {
			ss := intentSteps(b)
			ss = ss[:test.index+1]
			if test.rows {
				ss[test.index].affected = 0
			} else {
				ss[test.index].err = private
			}
			a, s := adapter(t, ss)
			out, err := a.recordIntent(context.Background(), b)
			if err == nil || out != (Evidence{}) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal(out, err)
			}
			s.done(0)
		})
	}
	for _, test := range []struct {
		name                      string
		commit, rollback, discard error
		unknown, cleanup          bool
	}{{"commit_lost_ack", private, nil, nil, true, false}, {"commit_and_rollback", private, private, nil, true, true}, {"commit_and_discard", private, nil, private, true, true}, {"successful_commit_bad_cleanup", nil, private, nil, false, true}} {
		t.Run(test.name, func(t *testing.T) {
			a, s := adapter(t, intentSteps(b))
			s.commitErr = test.commit
			s.rollbackErr = test.rollback
			s.discardErr = test.discard
			out, err := a.recordIntent(context.Background(), b)
			if out != (Evidence{}) || err == nil || errors.Is(err, pg.ErrCommitOutcomeUnknown) != test.unknown || errors.Is(err, pg.ErrCleanupUnconfirmed) != test.cleanup || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal(out, err)
			}
			s.done(1)
		})
	}

	e := event{"op", "incident", "UNCERTAINTY", "evidence"}
	for _, hash := range []string{hashinsertEventSQL, hashhaltSQL} {
		t.Run("zero_rows_"+hash[:8], func(t *testing.T) {
			ss := eventSteps(b, e, "running", "")
			found := false
			for n := range ss {
				if ss[n].hash == hash {
					ss[n].affected = 0
					ss = ss[:n+1]
					found = true
					break
				}
			}
			if !found {
				t.Fatal("missing targeted SQL")
			}
			a, s := adapter(t, ss)
			out, err := a.recordEvent(context.Background(), b, e)
			if !errors.Is(err, ErrConflict) || out != (Evidence{}) {
				t.Fatal("zero-row mutation accepted", err)
			}
			s.done(0)
		})
	}

}

func TestInertInvalidAndCancelled(t *testing.T) {
	_, _, b := config(t)
	a, s := adapter(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		out, err := a.recordIntent(ctx, b)
		if err == nil || out != (Evidence{}) {
			t.Fatal("invalid context")
		}
	}
	if _, err := a.Observe(context.Background(), Binding{}); err == nil {
		t.Fatal("zero binding")
	}
	if _, err := a.recordEvent(context.Background(), b, event{}); err == nil {
		t.Fatal("invalid event")
	}
	for _, v := range []any{a, *a} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, v); got != "CallbackAuthorityAdapter(unwired,redacted)" {
				t.Fatal("adapter format not redacted")
			}
		}
	}
	var typedNil *script
	if _, err := New(typedNil, a.timeouts); err == nil {
		t.Fatal("typed nil pool")
	}
	if s.next != 0 || s.commits != 0 || s.releases != 0 {
		t.Fatal("constructor invoked adapter")
	}
	var zero *Adapter
	if _, err := zero.Observe(context.Background(), b); err == nil {
		t.Fatal("nil adapter")
	}
}

func TestEveryReceiptPathRejectsCommitAndCleanupUncertainty(t *testing.T) {
	_, _, b := config(t)
	e := event{"op_final", "incident", "UNCERTAINTY", "evidence_final"}
	for _, operation := range []string{"append", "historical", "observe"} {
		for _, mode := range []string{"unknown", "cleanup", "combined"} {
			t.Run(operation+"_"+mode, func(t *testing.T) {
				var ss []step
				switch operation {
				case "append":
					ss = eventSteps(b, e, "running", "")
				case "historical":
					ss = append(baseSteps(b), ledgerStep(b, "halted", "UNCERTAINTY"), eventStep(b, e))
				case "observe":
					ss = append(baseSteps(b), ledgerStep(b, "halted", "UNCERTAINTY"), step{op: "row", hash: hashincidentsSQL, args: []any{b.identity.Scope}, values: []any{true}})
				}
				a, s := adapter(t, ss)
				private := errors.New("PRIVATE SQL")
				if mode != "cleanup" {
					s.commitErr = private
				}
				if mode != "unknown" {
					s.rollbackErr = private
				}
				var out Evidence
				var err error
				if operation == "observe" {
					out, err = a.Observe(context.Background(), b)
				} else {
					out, err = a.recordEvent(context.Background(), b, e)
				}
				if err == nil || out != (Evidence{}) || errors.Is(err, pg.ErrCommitOutcomeUnknown) != (mode != "cleanup") || errors.Is(err, pg.ErrCleanupUnconfirmed) != (mode != "unknown") {
					t.Fatal(out, err)
				}
				s.done(1)
			})
		}
	}
}

func TestHistoricalTerminalAgainstRunningRejects(t *testing.T) {
	_, _, b := config(t)
	for _, e := range []event{{"op_incident", "incident", "UNCERTAINTY", "evidence"}, {"op_join", "joined", "JOINED", "evidence"}} {
		// This deliberately contradictory script bypasses the earlier EXISTS graph
		// rejection, so the historical branch itself must still reject the mismatch.
		a, s := adapter(t, append(baseSteps(b), ledgerStep(b, "running", ""), eventStep(b, e)))
		out, err := a.recordEvent(context.Background(), b, e)
		if !errors.Is(err, ErrInvalid) || out != (Evidence{}) {
			t.Fatal("partial historical graph accepted", out, err)
		}
		s.done(0)
	}
}

func TestBoundedProjectionNullsRejectWithoutReceipt(t *testing.T) {
	_, _, b := config(t)
	e := event{"op_null", "incident", "UNCERTAINTY", "evidence_null"}
	for _, table := range []string{"registry", "ledger", "event"} {
		positions := []int{0, 1, 2, 3, 4, 6, 7, 8}
		if table == "registry" {
			positions = []int{0, 1, 2, 3, 4, 5, 7, 8, 9, 10}
		} else if table == "event" {
			positions = []int{0, 1, 2, 3, 4, 6, 7, 8, 9}
		}
		for _, position := range positions {
			t.Run(fmt.Sprintf("%s_column_%d", table, position), func(t *testing.T) {
				ss := baseSteps(b)
				switch table {
				case "registry":
					ss[1].values[position] = nil
				case "ledger":
					r := ledgerStep(b, "halted", "UNCERTAINTY")
					r.values[position] = nil
					ss = append(ss, r)
				case "event":
					r := eventStep(b, e)
					r.values[position] = nil
					ss = append(ss, ledgerStep(b, "halted", "UNCERTAINTY"), r)
				}
				a, s := adapter(t, ss)
				var out Evidence
				var err error
				if table == "event" {
					out, err = a.recordEvent(context.Background(), b, e)
				} else {
					out, err = a.recordIntent(context.Background(), b)
				}
				if err == nil || out != (Evidence{}) {
					t.Fatal("NULL projection accepted", out, err)
				}
				s.done(0)
			})
		}
	}
	// A NULL readback after mutation is not a successful receipt or clean commit.
	ss := intentSteps(b)
	ss[len(ss)-1].values[6] = nil
	a, s := adapter(t, ss)
	out, err := a.recordIntent(context.Background(), b)
	if err == nil || out != (Evidence{}) {
		t.Fatal("NULL mutated readback accepted", out, err)
	}
	s.done(0)
}
