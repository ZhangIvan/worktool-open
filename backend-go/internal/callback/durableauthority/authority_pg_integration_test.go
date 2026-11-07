//go:build authority_pg

package durableauthority

import (
	"context"
	"errors"
	"fmt"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
	"time"
)

func TestPGAuthorityA01(t *testing.T) {
	f := fixture(t)
	var catalog string
	leaf(t, "rollback_absence", func(t *testing.T) {
		tx, e := f.pools[0].Begin(f.ctx)
		if e != nil {
			t.Fatal("BEGIN_FAILED")
		}
		defer tx.Rollback(f.ctx)
		if _, e = tx.Exec(f.ctx, string(authoritySchema)); e != nil {
			t.Fatal("SCHEMA_INSTALL_FAILED")
		}
		catalog = authorityCatalog(t, tx, f.ctx)
		if tx.Rollback(f.ctx) != nil {
			t.Fatal("ROLLBACK_FAILED")
		}
		var n int
		if f.pools[2].QueryRow(f.ctx, `SELECT count(*) FROM pg_namespace WHERE nspname='wt_synthetic_callback_authority'`).Scan(&n) != nil || n != 0 {
			t.Fatal("ROLLBACK_CATALOG_REMAINS")
		}
	})
	leaf(t, "commit_catalog", func(t *testing.T) {
		if catalog == "" {
			t.Fatal("INSTALL_PREREQUISITE_FAILED")
		}
		tx, e := f.pools[0].Begin(f.ctx)
		if e != nil {
			t.Fatal("BEGIN_FAILED")
		}
		defer tx.Rollback(f.ctx)
		if _, e = tx.Exec(f.ctx, string(authoritySchema)); e != nil {
			t.Fatal("SCHEMA_INSTALL_FAILED")
		}
		if authorityCatalog(t, tx, f.ctx) != catalog {
			t.Fatal("CATALOG_DRIFT")
		}
		if tx.Commit(f.ctx) != nil {
			t.Fatal("INSTALL_COMMIT_FAILED")
		}
		tx2, e := f.pools[2].Begin(f.ctx)
		if e != nil {
			t.Fatal("BEGIN_FAILED")
		}
		defer tx2.Rollback(f.ctx)
		if authorityCatalog(t, tx2, f.ctx) != catalog {
			t.Fatal("COMMITTED_CATALOG_DRIFT")
		}
		if tx2.Rollback(f.ctx) != nil {
			t.Fatal("ROLLBACK_FAILED")
		}
		f.installed = true
		f.check(t)
	})
	leaf(t, "duplicate_install", func(t *testing.T) {
		f.reject(t, "42P06", string(authoritySchema))
		tx, e := f.pools[2].Begin(f.ctx)
		if e != nil {
			t.Fatal("BEGIN_FAILED")
		}
		defer tx.Rollback(f.ctx)
		if authorityCatalog(t, tx, f.ctx) != catalog {
			t.Fatal("DUPLICATE_INSTALL_CHANGED_CATALOG")
		}
		if tx.Rollback(f.ctx) != nil {
			t.Fatal("ROLLBACK_FAILED")
		}
	})
}
func authorityCatalog(t *testing.T, tx pgx.Tx, ctx context.Context) string {
	t.Helper()
	var tables, functions, triggers, indexes, constraints, bad int
	e := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_class WHERE relnamespace='wt_synthetic_callback_authority'::regnamespace AND relkind='r'),(SELECT count(*) FROM pg_proc WHERE pronamespace='wt_synthetic_callback_authority'::regnamespace),(SELECT count(*) FROM pg_trigger WHERE tgrelid IN (SELECT oid FROM pg_class WHERE relnamespace='wt_synthetic_callback_authority'::regnamespace) AND NOT tgisinternal),(SELECT count(*) FROM pg_class WHERE relnamespace='wt_synthetic_callback_authority'::regnamespace AND relkind='i'),(SELECT count(*) FROM pg_constraint WHERE connamespace='wt_synthetic_callback_authority'::regnamespace AND contype<>'n'),(SELECT count(*) FROM pg_namespace WHERE nspname NOT IN ('pg_catalog','information_schema','public','wt_synthetic_callback_authority') AND nspname !~ '^pg_(toast|temp)')`).Scan(&tables, &functions, &triggers, &indexes, &constraints, &bad)
	if e != nil || tables != 3 || functions != 2 || triggers != 7 || indexes != 8 || constraints != 23 || bad != 0 {
		t.Fatal("CATALOG_COUNTS_MISMATCH")
	}
	var invalid int
	e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_trigger WHERE tgrelid IN (SELECT oid FROM pg_class WHERE relnamespace='wt_synthetic_callback_authority'::regnamespace) AND NOT tgisinternal AND (tgenabled<>'O' OR tgname NOT IN ('registry_immutable','registry_no_truncate','events_immutable','events_no_truncate','ledger_no_delete','ledger_no_truncate','ledger_monotone')))+(SELECT count(*) FROM pg_proc WHERE pronamespace='wt_synthetic_callback_authority'::regnamespace AND (prosecdef OR proconfig IS DISTINCT FROM ARRAY['search_path=pg_catalog, pg_temp']::text[]))+(SELECT count(*) FROM pg_constraint WHERE connamespace='wt_synthetic_callback_authority'::regnamespace AND (condeferrable OR condeferred OR NOT convalidated))`).Scan(&invalid)
	if e != nil || invalid != 0 {
		t.Fatal("CATALOG_GUARD_MISMATCH")
	}
	// Full normalized definitions are compared across rolled-back, installed and
	// independently observed catalogs. They originate only from hashed exact DDL.
	var result string
	e = tx.QueryRow(ctx, `SELECT json_build_array((SELECT json_agg(x ORDER BY relname,attnum) FROM (SELECT c.relname,a.attnum,a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid WHERE c.relnamespace='wt_synthetic_callback_authority'::regnamespace AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped)x),(SELECT json_agg(x ORDER BY conname) FROM (SELECT conname,pg_get_constraintdef(oid) FROM pg_constraint WHERE connamespace='wt_synthetic_callback_authority'::regnamespace)x),(SELECT json_agg(x ORDER BY tgname) FROM (SELECT tgname,pg_get_triggerdef(oid) FROM pg_trigger WHERE NOT tgisinternal AND tgrelid IN (SELECT oid FROM pg_class WHERE relnamespace='wt_synthetic_callback_authority'::regnamespace))x),(SELECT json_agg(x ORDER BY proname) FROM (SELECT proname,pg_get_functiondef(oid) FROM pg_proc WHERE pronamespace='wt_synthetic_callback_authority'::regnamespace)x),(SELECT json_agg(x ORDER BY indexname) FROM (SELECT indexname,indexdef FROM pg_indexes WHERE schemaname='wt_synthetic_callback_authority')x))::text`).Scan(&result)
	if e != nil || len(result) > 65536 {
		t.Fatal("CATALOG_DEFINITION_FAILED")
	}
	return result
}
func TestPGAuthorityA02(t *testing.T) {
	f := fixture(t)
	b := f.binding(t)
	base := registryArgs(b)
	for i, name := range []string{"scope", "allocation", "instance", "boot", "epoch", "provenance"} {
		for _, variant := range []string{"invalid", "overbound"} {
			i, name, variant := i, name, variant
			leaf(t, name+"_"+variant, func(t *testing.T) {
				a := append([]any(nil), base...)
				a[i] = "!"
				if variant == "overbound" {
					a[i] = strings.Repeat("a", 129)
				}
				f.reject(t, "23514", registryInsert, a...)
			})
		}
	}
	for _, c := range []struct {
		name  string
		index int
		value any
		state string
	}{{"revision_zero", 6, int64(0), "23514"}, {"plan_shape", 7, "bad", "23514"}, {"digest_shape", 8, "bad", "23514"}, {"plan_empty", 9, []byte{}, "23514"}, {"plan_overbound", 9, make([]byte, 262145), "23514"}, {"descriptors_empty", 10, []byte{}, "23514"}, {"descriptors_overbound", 10, make([]byte, 524289), "23514"}, {"not_null", 1, nil, "23502"}} {
		leaf(t, c.name, func(t *testing.T) {
			a := append([]any(nil), base...)
			a[c.index] = c.value
			f.reject(t, c.state, registryInsert, a...)
		})
	}
	leaf(t, "valid_limits", func(t *testing.T) {
		a := append([]any(nil), base...)
		for i := 0; i < 6; i++ {
			a[i] = strings.Repeat(string(rune('a'+i)), 128)
		}
		a[9] = make([]byte, 262144)
		a[10] = make([]byte, 524288)
		f.exec(t, registryInsert, a...)
		f.check(t)
	})
}
func TestPGAuthorityA03(t *testing.T) {
	f := fixture(t)
	b := f.seed(t, false)
	for _, target := range []string{"ledger", "event"} {
		for i, name := range []string{"scope", "allocation", "instance", "boot", "epoch", "revision", "digest"} {
			target, i, name := target, i, name
			leaf(t, target+"_"+name, func(t *testing.T) {
				a := bindingArgs(b)
				if i == 5 {
					a[i] = int64(2)
				} else if i == 6 {
					a[i] = strings.Repeat("a", 64)
				} else {
					a[i] = "missing"
				}
				s := insertIntentSQL
				if target == "event" {
					a = append([]any{"fk_event"}, a...)
					a = append(a, "joined", "JOINED", "ref")
					s = insertEventSQL
				}
				f.reject(t, "23503", s, a...)
			})
		}
		if target == "ledger" {
			f.exec(t, insertIntentSQL, bindingArgs(b)...)
		}
	}
	leaf(t, "event_absent_ledger", func(t *testing.T) {
		missing := f.seed(t, false)
		a := append([]any{"orphan"}, bindingArgs(missing)...)
		a = append(a, "joined", "JOINED", "ref")
		f.reject(t, "23503", insertEventSQL, a...)
	})
}
func TestPGAuthorityA04(t *testing.T) {
	f := fixture(t)
	b := f.seed(t, true)
	leaf(t, "registry_scope", func(t *testing.T) { f.reject(t, "23505", registryInsert, registryArgs(b)...) })
	leaf(t, "ledger_scope", func(t *testing.T) { f.reject(t, "23505", insertIntentSQL, bindingArgs(b)...) })
	leaf(t, "global_operation", func(t *testing.T) {
		a := append([]any{"global_duplicate"}, bindingArgs(b)...)
		a = append(a, "reconciliation_fact", "EVIDENCE_RECORDED", "ref")
		f.exec(t, insertEventSQL, a...)
		other := f.seed(t, true)
		a = append([]any{"global_duplicate"}, bindingArgs(other)...)
		a = append(a, "reconciliation_fact", "EVIDENCE_RECORDED", "ref")
		f.reject(t, "23505", insertEventSQL, a...)
	})
}
func TestPGAuthorityA05(t *testing.T) {
	f := fixture(t)
	b := f.seed(t, true)
	a := append([]any{"immutable_event"}, bindingArgs(b)...)
	a = append(a, "reconciliation_fact", "EVIDENCE_RECORDED", "ref")
	f.exec(t, insertEventSQL, a...)
	for _, table := range []string{"registry", "events"} {
		for _, op := range []string{"update", "self_update", "delete"} {
			table, op := table, op
			leaf(t, table+"_"+op, func(t *testing.T) {
				s := "DELETE FROM " + ns + table + " WHERE scope_id=$1"
				if op == "update" {
					s = "UPDATE " + ns + table + " SET allocation_id='changed' WHERE scope_id=$1"
				}
				if op == "self_update" {
					s = "UPDATE " + ns + table + " SET allocation_id=allocation_id WHERE scope_id=$1"
				}
				f.reject(t, "23514", s, b.identity.Scope)
			})
		}
	}
	for _, c := range []struct{ name, sql string }{{"truncate_registry_collective", "TRUNCATE " + ns + "registry," + ns + "ledger," + ns + "events"}, {"truncate_ledger_collective", "TRUNCATE " + ns + "ledger," + ns + "events"}, {"truncate_events", "TRUNCATE " + ns + "events"}, {"ledger_delete", "DELETE FROM " + ns + "ledger WHERE scope_id=$1"}} {
		leaf(t, c.name, func(t *testing.T) {
			if c.name == "ledger_delete" {
				f.reject(t, "23514", c.sql, b.identity.Scope)
			} else {
				f.reject(t, "23514", c.sql)
			}
		})
	}
}
func TestPGAuthorityA06(t *testing.T) {
	f := fixture(t)
	b := f.seed(t, true)
	leaf(t, "initial_halted", func(t *testing.T) {
		x := f.seed(t, false)
		f.reject(t, "23514", strings.Replace(insertIntentSQL, "'running',''", "'halted','JOINED'", 1), bindingArgs(x)...)
	})
	leaf(t, "running_self_update", func(t *testing.T) {
		f.reject(t, "23514", "UPDATE "+ns+"ledger SET state=state WHERE scope_id=$1", b.identity.Scope)
	})
	for _, col := range []string{"scope_id", "allocation_id", "instance_id", "boot_id", "epoch_id", "registry_revision", "binding_identity"} {
		leaf(t, "identity_"+col, func(t *testing.T) {
			value := "'changed'"
			if col == "registry_revision" {
				value = "2"
			}
			f.reject(t, "23514", "UPDATE "+ns+"ledger SET "+col+"="+value+",state='halted',halt_code='JOINED' WHERE scope_id=$1", b.identity.Scope)
		})
	}
	for _, reason := range []string{"JOINED", "UNCERTAINTY", "CLEANUP_UNCONFIRMED", "START_INTERRUPTED"} {
		leaf(t, "valid_"+reason, func(t *testing.T) {
			x := f.seed(t, true)
			a := append(bindingArgs(x), reason)
			f.exec(t, haltSQL, a...)
			f.check(t)
		})
	}
	f.exec(t, haltSQL, append(bindingArgs(b), "JOINED")...)
	for _, c := range []struct{ name, set string }{{"halted_to_running", "state='running',halt_code=''"}, {"halted_replace_reason", "halt_code='UNCERTAINTY'"}, {"halted_self_update", "state=state"}} {
		leaf(t, c.name, func(t *testing.T) {
			f.reject(t, "23514", "UPDATE "+ns+"ledger SET "+c.set+" WHERE scope_id=$1", b.identity.Scope)
		})
	}
}
func TestPGAuthorityA07(t *testing.T) {
	f := fixture(t)
	b := f.seed(t, true)
	kinds := []string{"incident", "joined", "reconciliation_fact", "unknown"}
	codes := []string{"UNCERTAINTY", "CLEANUP_UNCONFIRMED", "START_INTERRUPTED", "JOINED", "EVIDENCE_RECORDED", "UNKNOWN"}
	for _, kind := range kinds {
		for _, code := range codes {
			leaf(t, kind+"_"+code, func(t *testing.T) {
				op := "pair_" + kind + "_" + code
				e := event{op, kind, code, "ref"}
				a := append([]any{op}, bindingArgs(b)...)
				a = append(a, kind, code, "ref")
				if e.valid() {
					f.exec(t, insertEventSQL, a...)
					f.check(t)
				} else {
					f.reject(t, "23514", insertEventSQL, a...)
				}
			})
		}
	}
	for _, field := range []string{"operation", "reference"} {
		for _, bad := range []string{"invalid", "overbound"} {
			leaf(t, field+"_"+bad, func(t *testing.T) {
				op, ref := "event_bound", "ref"
				value := "!"
				if bad == "overbound" {
					value = strings.Repeat("a", 129)
				}
				if field == "operation" {
					op = value
				} else {
					ref = value
				}
				a := append([]any{op}, bindingArgs(b)...)
				a = append(a, "joined", "JOINED", ref)
				f.reject(t, "23514", insertEventSQL, a...)
			})
		}
	}
	leaf(t, "not_null", func(t *testing.T) {
		a := append([]any{"event_null"}, bindingArgs(b)...)
		a = append(a, "joined", "JOINED", nil)
		f.reject(t, "23502", insertEventSQL, a...)
	})
}
func TestPGAuthorityA08(t *testing.T) {
	f := fixture(t)
	a := f.adapter(t, 0, nil)
	leaf(t, "missing_registry", func(t *testing.T) {
		b := f.binding(t)
		before := f.snapshot(t)
		ev, e := f.intent(t, a, b)
		outcome(t, ev, e, "", ErrMissing)
		f.unchanged(t, before)
	})
	b := f.seed(t, false)
	leaf(t, "valid_absent", func(t *testing.T) {
		ev, e := f.intent(t, a, b)
		outcome(t, ev, e, "INTENT_RECORDED_NO_AUTHORITY", nil)
		f.check(t)
	})
	leaf(t, "second_intent", func(t *testing.T) {
		before := f.snapshot(t)
		ev, e := f.intent(t, a, b)
		outcome(t, ev, e, "", ErrConflict)
		f.unchanged(t, before)
	})
	for _, field := range []string{"allocation", "instance", "boot", "epoch", "provenance", "revision", "plan", "descriptor"} {
		leaf(t, "changed_"+field, func(t *testing.T) {
			p, ds, _ := config(t)
			i := b.identity
			switch field {
			case "allocation":
				i.Allocation = "changed"
			case "instance":
				i.Instance = "changed"
			case "boot":
				i.Boot = "changed"
			case "epoch":
				i.Epoch = "changed"
			case "provenance":
				i.ProvenanceRef = "changed"
			case "revision":
				i.Revision++
			}
			if field == "plan" {
				var err error
				p, err = composition.Parse([]byte(strings.Replace(testManifest, `"max_connections":10`, `"max_connections":11`, 1)), "")
				if err != nil {
					t.Fatal("PLAN_FAILED")
				}
			}
			if field == "descriptor" {
				d, err := registration.Decode([]byte(strings.Replace(testDescriptor, "provider_synthetic", "provider_changed", 1)))
				if err != nil {
					t.Fatal("DESCRIPTOR_FAILED")
				}
				ds = []registration.Descriptor{d}
			}
			changed, e := Bind(p, ds, i)
			if e != nil {
				t.Fatal("BIND_FAILED")
			}
			before := f.snapshot(t)
			ev, e := f.intent(t, a, changed)
			outcome(t, ev, e, "", ErrInvalid)
			f.unchanged(t, before)
		})
	}
}
func TestPGAuthorityA09(t *testing.T) {
	f := fixture(t)
	a := f.adapter(t, 0, nil)
	for _, code := range []string{"JOINED", "UNCERTAINTY", "CLEANUP_UNCONFIRMED", "START_INTERRUPTED", "EVIDENCE_RECORDED"} {
		leaf(t, "history_"+code, func(t *testing.T) {
			b := f.seed(t, true)
			kind := "incident"
			if code == "JOINED" {
				kind = "joined"
			}
			if code == "EVIDENCE_RECORDED" {
				kind = "reconciliation_fact"
			}
			e := event{"history_" + code, kind, code, "ref"}
			ev, err := f.append(t, a, b, e)
			outcome(t, ev, err, "FACT_RECORDED_NO_AUTHORITY", nil)
			before := f.snapshot(t)
			ev, err = f.append(t, a, b, e)
			outcome(t, ev, err, "HISTORICAL_FACT_NO_AUTHORITY", nil)
			f.unchanged(t, before)
			changed := e
			changed.reference = "changed"
			ev, err = f.append(t, a, b, changed)
			outcome(t, ev, err, "", ErrConflict)
			f.unchanged(t, before)
			other := f.seed(t, true)
			before = f.snapshot(t)
			ev, err = f.append(t, a, other, e)
			outcome(t, ev, err, "", ErrConflict)
			f.unchanged(t, before)
			for j, later := range []event{{kind: "joined", code: "JOINED", reference: "later"}, {kind: "incident", code: "UNCERTAINTY", reference: "later"}, {kind: "reconciliation_fact", code: "EVIDENCE_RECORDED", reference: "later"}} {
				later.operation = fmt.Sprintf("later_%s_%d", code, j)
				ev, err = f.append(t, a, b, later)
				outcome(t, ev, err, "FACT_RECORDED_NO_AUTHORITY", nil)
			}
			want := code
			if code == "EVIDENCE_RECORDED" {
				want = "JOINED"
			}
			var got string
			if f.pools[2].QueryRow(f.ctx, "SELECT halt_code FROM "+ns+"ledger WHERE scope_id=$1", b.identity.Scope).Scan(&got) != nil || got != want {
				t.Fatal("FIRST_HALT_CHANGED")
			}
			ev, err = a.Observe(f.ctx, b)
			outcome(t, ev, err, "INCIDENT_BLOCKED", nil)
			f.check(t)
		})
	}
}
func TestPGAuthorityA10(t *testing.T) {
	f := fixture(t)
	a := f.adapter(t, 0, nil)
	for _, name := range []string{"absent", "running", "joined", "incident", "running_terminal", "halt_without_event", "corrupt_plan", "corrupt_descriptors", "corrupt_hash"} {
		leaf(t, name, func(t *testing.T) {
			b := f.binding(t)
			args := registryArgs(b)
			if name == "corrupt_plan" {
				args[9] = []byte(`{}`)
			}
			if name == "corrupt_descriptors" {
				args[10] = []byte(`[]`)
			}
			if name == "corrupt_hash" {
				args[8] = strings.Repeat("a", 64)
			}
			f.exec(t, registryInsert, args...)
			if name != "absent" && !strings.HasPrefix(name, "corrupt_") {
				f.exec(t, insertIntentSQL, bindingArgs(b)...)
			}
			want := "RUNNING_INTENT_BLOCKED"
			var failure error
			switch name {
			case "absent":
				want = "INTENT_ABSENT_NO_AUTHORITY"
			case "joined", "incident":
				e := event{"observe_" + name, "joined", "JOINED", "ref"}
				want = "HALTED_BLOCKED"
				if name == "incident" {
					e.kind = "incident"
					e.code = "UNCERTAINTY"
					want = "INCIDENT_BLOCKED"
				}
				ev, err := f.append(t, a, b, e)
				outcome(t, ev, err, "FACT_RECORDED_NO_AUTHORITY", nil)
			case "running_terminal":
				x := append([]any{"illegal_graph"}, bindingArgs(b)...)
				x = append(x, "joined", "JOINED", "ref")
				f.exec(t, insertEventSQL, x...)
				failure = ErrInvalid
			case "halt_without_event":
				f.exec(t, haltSQL, append(bindingArgs(b), "JOINED")...)
				failure = ErrInvalid
			case "corrupt_plan", "corrupt_descriptors", "corrupt_hash":
				failure = ErrInvalid
			}
			before := f.snapshot(t)
			ev, err := a.Observe(f.ctx, b)
			outcome(t, ev, err, want, failure)
			f.unchanged(t, before)
		})
	}
}
func TestPGAuthorityA11(t *testing.T) {
	f := fixture(t)
	leaf(t, "direct_rollback", func(t *testing.T) {
		b := f.seed(t, true)
		before := f.snapshot(t)
		tx, e := f.pools[0].Begin(f.ctx)
		if e != nil {
			t.Fatal("BEGIN_FAILED")
		}
		defer tx.Rollback(f.ctx)
		args := append([]any{"rollback_direct"}, bindingArgs(b)...)
		args = append(args, "joined", "JOINED", "ref")
		if _, e = tx.Exec(f.ctx, insertEventSQL, args...); e != nil {
			t.Fatal("EVENT_FAILED")
		}
		if _, e = tx.Exec(f.ctx, haltSQL, append(bindingArgs(b), "JOINED")...); e != nil {
			t.Fatal("HALT_FAILED")
		}
		if tx.Rollback(f.ctx) != nil {
			t.Fatal("ROLLBACK_FAILED")
		}
		f.unchanged(t, before)
	})
	for _, point := range []string{"before_halt", "before_readback"} {
		leaf(t, point, func(t *testing.T) {
			b := f.seed(t, true)
			before := f.snapshot(t)
			written := false
			h := &authorityHook{after: func(_ context.Context, s string, e error) error {
				if s == insertEventSQL && e == nil {
					written = true
				}
				return e
			}, before: func(_ context.Context, s string) error {
				if written && (point == "before_halt" && s == haltSQL || point == "before_readback" && s == eventSQL) {
					return errors.New("INJECTED_BOUNDARY_FAILURE")
				}
				return nil
			}}
			a := f.adapter(t, 0, h)
			ev, e := f.append(t, a, b, event{"rollback_" + point, "joined", "JOINED", "ref"})
			outcome(t, ev, e, "", ErrUnavailable)
			f.unchanged(t, before)
		})
	}
	leaf(t, "aborted_transaction", func(t *testing.T) {
		b := f.seed(t, true)
		before := f.snapshot(t)
		tx, e := f.pools[0].Begin(f.ctx)
		if e != nil {
			t.Fatal("BEGIN_FAILED")
		}
		defer tx.Rollback(f.ctx)
		_, e = tx.Exec(f.ctx, insertIntentSQL, bindingArgs(b)...)
		if sqlstate(e) != "23505" {
			t.Fatal("PRIMARY_SQLSTATE_MISMATCH")
		}
		_, e = tx.Exec(f.ctx, "SELECT 1")
		if sqlstate(e) != "25P02" {
			t.Fatal("ABORTED_SQLSTATE_MISMATCH")
		}
		if tx.Rollback(f.ctx) != nil {
			t.Fatal("ROLLBACK_FAILED")
		}
		f.unchanged(t, before)
		t.Log("AUTHORITY_SQLSTATE 25P02")
	})
}

type authorityResult struct {
	ev  Evidence
	err error
}

func launchAuthority(ctx context.Context, fn func(context.Context) (Evidence, error)) <-chan authorityResult {
	out := make(chan authorityResult, 1)
	go func() { ev, e := fn(ctx); out <- authorityResult{ev, e}; close(out) }()
	return out
}
func joinedAuthority(t *testing.T, c <-chan authorityResult) authorityResult {
	t.Helper()
	select {
	case r := <-c:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("JOIN_UNCONFIRMED")
	}
	return authorityResult{}
}
func raceCleanup(t *testing.T, cancel context.CancelFunc, gates []*authorityGate, results ...<-chan authorityResult) {
	t.Cleanup(func() {
		for _, g := range gates {
			g.open()
		}
		cancel()
		cleanupAuthorityJoins(t, results...)
	})
}
func repeatAuthority(t *testing.T, name string, fn func(*testing.T)) {
	for n := 1; n <= 3; n++ {
		leaf(t, fmt.Sprintf("%s_r%d", name, n), fn)
	}
}
func TestPGAuthorityA12(t *testing.T) {
	f := fixture(t)
	repeatAuthority(t, "intent_unique", func(t *testing.T) {
		b := f.seed(t, false)
		before := f.snapshot(t)
		ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
		ga, gb, gcommit := gate(), gate(), gate()
		ha := &authorityHook{before: func(ctx context.Context, s string) error {
			if s == insertIntentSQL {
				return ga.wait(ctx)
			}
			if s == "COMMIT" {
				return gcommit.wait(ctx)
			}
			return nil
		}}
		hb := &authorityHook{before: func(ctx context.Context, s string) error {
			if s == insertIntentSQL {
				return gb.wait(ctx)
			}
			return nil
		}}
		a, bAdapter := f.adapter(t, 0, ha), f.adapter(t, 1, hb)
		ra := launchAuthority(ctx, func(c context.Context) (Evidence, error) { return a.recordIntent(c, b) })
		rb := launchAuthority(ctx, func(c context.Context) (Evidence, error) { return bAdapter.recordIntent(c, b) })
		raceCleanup(t, cancel, []*authorityGate{ga, gb, gcommit}, ra, rb)
		await(t, ga.arrived)
		await(t, gb.arrived)
		ga.open()
		await(t, gcommit.arrived)
		gb.open()
		f.blocked(t, hb.pid, ha.pid)
		gcommit.open()
		ar, br := joinedAuthority(t, ra), joinedAuthority(t, rb)
		outcome(t, ar.ev, ar.err, "INTENT_RECORDED_NO_AUTHORITY", nil)
		outcome(t, br.ev, br.err, "", ErrUnavailable)
		if hb.state != "23505" {
			t.Fatal("LOSER_SQLSTATE_MISMATCH")
		}
		t.Log("AUTHORITY_SQLSTATE 23505")
		var n int
		if f.pools[2].QueryRow(f.ctx, "SELECT count(*) FROM "+ns+"ledger WHERE scope_id=$1", b.identity.Scope).Scan(&n) != nil || n != 1 {
			t.Fatal("INTENT_COUNT_MISMATCH")
		}
		graphDelta(t, before, f.snapshot(t), map[string]string{graphKey(1, b.identity.Scope): "insert"})
		f.check(t)
	})
}
func TestPGAuthorityA13(t *testing.T) {
	f := fixture(t)
	for _, mode := range []string{"joined_observe", "incident_observe", "two_events"} {
		mode := mode
		repeatAuthority(t, mode, func(t *testing.T) {
			b := f.seed(t, true)
			before := f.snapshot(t)
			ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
			locked, enter := gate(), gate()
			first := true
			ha := &authorityHook{after: func(ctx context.Context, s string, e error) error {
				if first && s == ledgerSQL && e == nil {
					first = false
					return locked.wait(ctx)
				}
				return e
			}}
			hb := &authorityHook{before: func(ctx context.Context, s string) error {
				if s == ledgerSQL {
					enter.once.Do(func() { close(enter.arrived) })
				}
				return nil
			}}
			a, bAdapter := f.adapter(t, 0, ha), f.adapter(t, 1, hb)
			firstEvent := event{fmt.Sprintf("serialize_%d_a", f.next), "joined", "JOINED", "ref"}
			if mode == "incident_observe" {
				firstEvent.kind = "incident"
				firstEvent.code = "UNCERTAINTY"
			}
			ra := launchAuthority(ctx, func(c context.Context) (Evidence, error) { return a.recordEvent(c, b, firstEvent) })
			var rb <-chan authorityResult
			// Cleanup is registered before waiting, including the not-yet-launched waiter.
			t.Cleanup(func() {
				locked.open()
				enter.open()
				cancel()
				if rb != nil {
					cleanupAuthorityJoins(t, ra, rb)
				} else {
					cleanupAuthorityJoins(t, ra)
				}
			})
			await(t, locked.arrived)
			rb = launchAuthority(ctx, func(c context.Context) (Evidence, error) {
				if mode == "two_events" {
					return bAdapter.recordEvent(c, b, event{fmt.Sprintf("serialize_%d_b", f.next), "incident", "UNCERTAINTY", "ref"})
				}
				return bAdapter.Observe(c, b)
			})
			await(t, enter.arrived)
			f.blocked(t, hb.pid, ha.pid)
			locked.open()
			ar, br := joinedAuthority(t, ra), joinedAuthority(t, rb)
			outcome(t, ar.ev, ar.err, "FACT_RECORDED_NO_AUTHORITY", nil)
			want := "HALTED_BLOCKED"
			if mode == "incident_observe" {
				want = "INCIDENT_BLOCKED"
			}
			if mode == "two_events" {
				want = "FACT_RECORDED_NO_AUTHORITY"
			}
			// Strict READ COMMITTED acceptance: an updated locked tuple with stale EXISTS
			// projections is a defect, never retried, skipped, or weakened to ErrInvalid.
			outcome(t, br.ev, br.err, want, nil)
			if mode != "two_events" {
				ev, e := f.append(t, a, b, event{fmt.Sprintf("serialize_%d_later", f.next), "incident", "UNCERTAINTY", "ref"})
				outcome(t, ev, e, "FACT_RECORDED_NO_AUTHORITY", nil)
			}
			ev, e := a.Observe(f.ctx, b)
			outcome(t, ev, e, "INCIDENT_BLOCKED", nil)
			var n int
			var reason string
			if f.pools[2].QueryRow(f.ctx, "SELECT halt_code,(SELECT count(*) FROM "+ns+"events WHERE scope_id=$1) FROM "+ns+"ledger WHERE scope_id=$1", b.identity.Scope).Scan(&reason, &n) != nil || reason != firstEvent.code || n != 2 {
				t.Fatal("SERIALIZED_GRAPH_MISMATCH")
			}
			second := fmt.Sprintf("serialize_%d_later", f.next)
			if mode == "two_events" {
				second = fmt.Sprintf("serialize_%d_b", f.next)
			}
			graphDelta(t, before, f.snapshot(t), map[string]string{graphKey(1, b.identity.Scope): "update", graphKey(2, firstEvent.operation): "insert", graphKey(2, second): "insert"})
			f.check(t)
		})
	}
}
func TestPGAuthorityA14(t *testing.T) {
	f := fixture(t)
	repeatAuthority(t, "global_operation", func(t *testing.T) {
		ba, bb := f.seed(t, true), f.seed(t, true)
		before := f.snapshot(t)
		ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
		ga, gb, gcommit := gate(), gate(), gate()
		ha := &authorityHook{before: func(ctx context.Context, s string) error {
			if s == insertEventSQL {
				return ga.wait(ctx)
			}
			if s == "COMMIT" {
				return gcommit.wait(ctx)
			}
			return nil
		}}
		hb := &authorityHook{before: func(ctx context.Context, s string) error {
			if s == insertEventSQL {
				return gb.wait(ctx)
			}
			return nil
		}}
		a, b := f.adapter(t, 0, ha), f.adapter(t, 1, hb)
		event := event{fmt.Sprintf("race_global_%d", f.next), "joined", "JOINED", "ref"}
		ra := launchAuthority(ctx, func(c context.Context) (Evidence, error) { return a.recordEvent(c, ba, event) })
		rb := launchAuthority(ctx, func(c context.Context) (Evidence, error) { return b.recordEvent(c, bb, event) })
		raceCleanup(t, cancel, []*authorityGate{ga, gb, gcommit}, ra, rb)
		await(t, ga.arrived)
		await(t, gb.arrived)
		ga.open()
		await(t, gcommit.arrived)
		gb.open()
		f.blocked(t, hb.pid, ha.pid)
		gcommit.open()
		ar, br := joinedAuthority(t, ra), joinedAuthority(t, rb)
		outcome(t, ar.ev, ar.err, "FACT_RECORDED_NO_AUTHORITY", nil)
		outcome(t, br.ev, br.err, "", ErrUnavailable)
		if hb.state != "23505" {
			t.Fatal("LOSER_SQLSTATE_MISMATCH")
		}
		t.Log("AUTHORITY_SQLSTATE 23505")
		var scope, state, code string
		var n int
		if f.pools[2].QueryRow(f.ctx, "SELECT scope_id FROM "+ns+"events WHERE operation_id=$1", event.operation).Scan(&scope) != nil || scope != ba.identity.Scope {
			t.Fatal("GLOBAL_WINNER_MISMATCH")
		}
		if f.pools[2].QueryRow(f.ctx, "SELECT state,halt_code,(SELECT count(*) FROM "+ns+"events WHERE scope_id=$1) FROM "+ns+"ledger WHERE scope_id=$1", bb.identity.Scope).Scan(&state, &code, &n) != nil || state != "running" || code != "" || n != 0 {
			t.Fatal("LOSER_GRAPH_CHANGED")
		}
		graphDelta(t, before, f.snapshot(t), map[string]string{graphKey(1, ba.identity.Scope): "update", graphKey(2, event.operation): "insert"})
		f.check(t)
	})
}
func TestPGAuthorityA15(t *testing.T) {
	f := fixture(t)
	for _, mode := range []string{"lock_timeout", "server_cancel"} {
		mode := mode
		repeatAuthority(t, mode, func(t *testing.T) {
			b := f.seed(t, true)
			before := f.snapshot(t)
			tx, e := f.pools[0].Begin(f.ctx)
			if e != nil {
				t.Fatal("BEGIN_FAILED")
			}
			holder := tx.Conn().PgConn().PID()
			if _, e = tx.Exec(f.ctx, "SELECT scope_id FROM "+ns+"ledger WHERE scope_id=$1 FOR UPDATE", b.identity.Scope); e != nil {
				tx.Rollback(f.ctx)
				t.Fatal("LOCK_FAILED")
			}
			ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
			entered := gate()
			h := &authorityHook{before: func(_ context.Context, s string) error {
				if s == ledgerSQL {
					entered.once.Do(func() { close(entered.arrived) })
				}
				return nil
			}}
			a := f.adapter(t, 1, h)
			r := launchAuthority(ctx, func(c context.Context) (Evidence, error) { return a.Observe(c, b) })
			t.Cleanup(func() { cancel(); tx.Rollback(f.ctx); cleanupAuthorityJoins(t, r) })
			await(t, entered.arrived)
			f.blocked(t, h.pid, holder)
			want := "55P03"
			if mode == "server_cancel" {
				want = "57014"
				var cancelled bool
				if f.pools[2].QueryRow(f.ctx, `SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE pid=$1 AND datname=current_database() AND $2=ANY(pg_blocking_pids(pid))`, h.pid, holder).Scan(&cancelled) != nil || !cancelled {
					t.Fatal("TARGETED_CANCEL_FAILED")
				}
			}
			result := joinedAuthority(t, r)
			outcome(t, result.ev, result.err, "", ErrUnavailable)
			if h.state != want {
				t.Fatal("SERVER_SQLSTATE_MISMATCH")
			}
			t.Log("AUTHORITY_SQLSTATE " + want)
			if tx.Rollback(f.ctx) != nil {
				t.Fatal("ROLLBACK_FAILED")
			}
			f.unchanged(t, before)
		})
	}
}
func TestPGAuthorityA16(t *testing.T) {
	f := fixture(t)
	leaf(t, "release_before_receipt", func(t *testing.T) {
		b := f.seed(t, false)
		before := f.snapshot(t)
		g := gate()
		ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
		h := &authorityHook{release: func() { _ = g.wait(ctx) }}
		a := f.adapter(t, 0, h)
		r := launchAuthority(ctx, func(c context.Context) (Evidence, error) { return a.recordIntent(c, b) })
		raceCleanup(t, cancel, []*authorityGate{g}, r)
		await(t, g.arrived)
		select {
		case <-r:
			t.Fatal("RECEIPT_BEFORE_RELEASE")
		default:
		}
		g.open()
		result := joinedAuthority(t, r)
		outcome(t, result.ev, result.err, "INTENT_RECORDED_NO_AUTHORITY", nil)
		graphDelta(t, before, f.snapshot(t), map[string]string{graphKey(1, b.identity.Scope): "insert"})
		f.check(t)
	})
	leaf(t, "commit_acknowledgement_injection", func(t *testing.T) {
		b := f.seed(t, false)
		before := f.snapshot(t)
		h := &authorityHook{after: func(_ context.Context, s string, e error) error {
			if s == "COMMIT" && e == nil {
				return errors.New("INJECTED_COMMIT_ACK_FAILURE")
			}
			return e
		}}
		a := f.adapter(t, 0, h)
		ev, e := f.intent(t, a, b)
		outcome(t, ev, e, "", pg.ErrCommitOutcomeUnknown)
		observer := f.adapter(t, 1, nil)
		ev, e = observer.Observe(f.ctx, b)
		outcome(t, ev, e, "RUNNING_INTENT_BLOCKED", nil)
		graphDelta(t, before, f.snapshot(t), map[string]string{graphKey(1, b.identity.Scope): "insert"})
		f.check(t)
	})
	leaf(t, "rollback_cleanup_injection", func(t *testing.T) {
		b := f.seed(t, true)
		before := f.snapshot(t)
		h := &authorityHook{before: func(_ context.Context, s string) error {
			if s == haltSQL {
				return errors.New("INJECTED_WRITE_FAILURE")
			}
			return nil
		}, after: func(_ context.Context, s string, e error) error {
			if s == "ROLLBACK" && e == nil {
				return errors.New("INJECTED_ROLLBACK_ACK_FAILURE")
			}
			return e
		}}
		a := f.adapter(t, 0, h)
		ev, e := f.append(t, a, b, event{"cleanup_injection", "joined", "JOINED", "ref"})
		outcome(t, ev, e, "", pg.ErrCleanupUnconfirmed)
		f.unchanged(t, before)
	})
}

// Never Goexit on the first missing join: attempt all launched contenders, with
// one shared bounded cleanup deadline, then report uncertainty nonfatally.
func cleanupAuthorityJoins(t *testing.T, results ...<-chan authorityResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok := true
	for _, r := range results {
		select {
		case <-r:
			continue
		default:
		}
		select {
		case <-r:
		case <-ctx.Done():
			ok = false
		}
	}
	if !ok {
		t.Error("JOIN_UNCONFIRMED")
	}
}
