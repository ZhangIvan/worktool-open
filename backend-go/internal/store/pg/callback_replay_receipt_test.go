package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func replayFactsFixture(n int) callbackReplayFacts {
	f := callbackReplayFacts{tenant: "tenant", device: "device", command: "command", app: "app", attempt: "attempt", commandDigest: hashText("command"), snapshotDigest: hashText("snapshot"), resultDigest: hashText("result"), status: "succeeded", effect: "applied", dispatchSession: "original_session", resultReservation: "result_reservation", batch: "batch", message: "message", child: "child", requestHash: hashText("request"), appEpoch: 1, dispatchEpoch: 2}
	for i := 0; i < n; i++ {
		f.bindings = append(f.bindings, callbackReplayBinding{reservation: fmt.Sprintf("r%d", i), delivery: fmt.Sprintf("d%d", i), event: "event", app: f.app, subscription: fmt.Sprintf("s%d", i), destination: fmt.Sprintf("dest%d", i), bodyDigest: hashText("body"), version: 1, policy: 1, key: 1, ordinal: 0, attempts: 4})
	}
	return f
}

func TestCallbackReplayReceiptCanonicalAndF0(t *testing.T) {
	for _, n := range []int{0, 1, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := replayFactsFixture(n)
			a, err := canonicalCallbackReplayReceipt(f)
			b, second := canonicalCallbackReplayReceipt(f)
			if err != nil || second != nil || a != b || len(a) > callbackReplayReceiptLimit {
				t.Fatal("unstable bounded receipt")
			}
			var decoded map[string]any
			if json.Unmarshal([]byte(a), &decoded) != nil || decoded["schema"] != "callback-result-replay-v1" || len(decoded["callbacks"].([]any)) != n {
				t.Fatal("schema/callback shape")
			}
			if n == 0 && !strings.Contains(a, `"callbacks":[]`) {
				t.Fatal("F0 must be explicit empty array")
			}
		})
	}
	// App/config identities previously accepted UTF-8. Do not silently narrow them.
	f := replayFactsFixture(1)
	f.app = "应用"
	f.bindings[0].app = f.app
	f.bindings[0].subscription = "订阅"
	f.bindings[0].destination = "目标"
	if _, err := canonicalCallbackReplayReceipt(f); err != nil {
		t.Fatal("UTF-8 identity compatibility")
	}
}

func TestCallbackReplayReceiptMaximumEscapedMetadata(t *testing.T) {
	f := replayFactsFixture(4)
	for _, p := range []*string{&f.tenant, &f.device, &f.command, &f.attempt, &f.dispatchSession, &f.resultReservation, &f.batch, &f.message, &f.child} {
		*p = strings.Repeat("a", 128)
	}
	f.app = strings.Repeat("\x01", 128)
	for i := range f.bindings {
		b := &f.bindings[i]
		b.reservation = fmt.Sprintf("r%d", i) + strings.Repeat("r", 126)
		b.delivery = fmt.Sprintf("d%d", i) + strings.Repeat("d", 126)
		b.event = strings.Repeat("e", 128)
		b.app = f.app
		b.subscription = strings.Repeat("\x02", 128)
		b.destination = strings.Repeat("\x03", 127) + string(rune('a'+i))
		b.version = maxSafeInteger
		b.policy = maxSafeInteger
		b.key = maxSafeInteger
	}
	f.appEpoch = maxSafeInteger
	f.dispatchEpoch = maxSafeInteger
	receipt, err := canonicalCallbackReplayReceipt(f)
	if err != nil || len(receipt) > callbackReplayReceiptLimit {
		t.Fatal("valid maximum metadata refused")
	}
	if len(receipt) <= 8192 {
		t.Fatal("fixture did not exercise escaping above old bound")
	}
}

func TestCallbackReplayReceiptRejectsMalformedFacts(t *testing.T) {
	cases := map[string]func(*callbackReplayFacts){
		"empty_tenant": func(f *callbackReplayFacts) { f.tenant = "" }, "wrong_device_encoding": func(f *callbackReplayFacts) { f.device = "device/other" },
		"oversized_app": func(f *callbackReplayFacts) { f.app = strings.Repeat("x", 129) }, "invalid_app_utf8": func(f *callbackReplayFacts) { f.app = string([]byte{255}) },
		"negative_app_epoch": func(f *callbackReplayFacts) { f.appEpoch = -1 }, "unsafe_epoch": func(f *callbackReplayFacts) { f.dispatchEpoch = maxSafeInteger + 1 },
		"empty_original_session": func(f *callbackReplayFacts) { f.dispatchSession = "" }, "wrong_command_hash": func(f *callbackReplayFacts) { f.commandDigest = "bad" },
		"uppercase_result_hash": func(f *callbackReplayFacts) { f.resultDigest = strings.Repeat("A", 64) }, "missing_request_hash": func(f *callbackReplayFacts) { f.requestHash = "" },
		"status_effect_mismatch": func(f *callbackReplayFacts) { f.effect = "unknown" }, "unknown_status": func(f *callbackReplayFacts) { f.status = "delivered" },
		"too_many_callbacks":    func(f *callbackReplayFacts) { f.bindings = replayFactsFixture(5).bindings },
		"duplicate_destination": func(f *callbackReplayFacts) { f.bindings[1].destination = f.bindings[0].destination },
		"unsorted_destination":  func(f *callbackReplayFacts) { f.bindings[0], f.bindings[1] = f.bindings[1], f.bindings[0] },
		"duplicate_delivery":    func(f *callbackReplayFacts) { f.bindings[1].delivery = f.bindings[0].delivery },
		"duplicate_reservation": func(f *callbackReplayFacts) { f.bindings[1].reservation = f.bindings[0].reservation },
		"wrong_app":             func(f *callbackReplayFacts) { f.bindings[0].app = "other" }, "event_split": func(f *callbackReplayFacts) { f.bindings[0].event = "other" },
		"result_as_callback": func(f *callbackReplayFacts) { f.bindings[0].reservation = f.resultReservation },
		"zero_version":       func(f *callbackReplayFacts) { f.bindings[0].version = 0 }, "negative_policy": func(f *callbackReplayFacts) { f.bindings[0].policy = -1 },
		"unsafe_key": func(f *callbackReplayFacts) { f.bindings[0].key = maxSafeInteger + 1 }, "invalid_attempt_limit": func(f *callbackReplayFacts) { f.bindings[0].attempts = 5 },
		"wrong_ordinal": func(f *callbackReplayFacts) { f.bindings[0].ordinal = 1 }, "bad_body_digest": func(f *callbackReplayFacts) { f.bindings[0].bodyDigest = "" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := replayFactsFixture(2)
			change(&f)
			if s, err := canonicalCallbackReplayReceipt(f); err == nil || s != "" {
				t.Fatal("malformed evidence accepted")
			}
		})
	}
}

// Scripted driver boundary, not PostgreSQL parsing/locking/transaction evidence.
type replayScriptTx struct {
	pgx.Tx
	f                callbackReplayFacts
	stored           string
	queryErr, rowErr error
	complete         bool
	inserted         int
	writeErr         error
	affected         int64
	rows             *replayScriptRows
}
type replayScriptRows struct {
	callbackTestRows
	closed bool
}

func (r *replayScriptRows) Close() { r.closed = true }

func (tx *replayScriptTx) QueryRow(_ context.Context, q string, args ...any) pgx.Row {
	if tx.rowErr != nil {
		return callbackTestRow{err: tx.rowErr}
	}
	if len(args) < 2 || args[0] != tx.f.tenant {
		panic("unscoped metadata query")
	}
	switch q {
	case callbackReplayOriginSQL:
		return callbackTestRow{values: []any{tx.f.dispatchSession, tx.f.dispatchEpoch}}
	case callbackReplayMessageSQL:
		return callbackTestRow{values: []any{tx.f.batch, tx.f.message, tx.f.child, tx.f.requestHash}}
	case callbackReplayResultSQL:
		return callbackTestRow{values: []any{tx.f.resultReservation}}
	default:
		if strings.HasPrefix(q, "SELECT (SELECT count(*)") {
			return callbackTestRow{values: []any{tx.complete}}
		}
		if strings.Contains(q, "FROM callback_replay_receipts") {
			if tx.stored == "MISSING" {
				return callbackTestRow{err: pgx.ErrNoRows}
			}
			return callbackTestRow{values: []any{tx.stored}}
		}
		panic("unexpected replay query")
	}
}
func (tx *replayScriptTx) Query(_ context.Context, q string, args ...any) (pgx.Rows, error) {
	if q != callbackReplayBindingsSQL || !reflect.DeepEqual(args, []any{tx.f.tenant, tx.f.command}) {
		panic("unscoped binding query")
	}
	if tx.queryErr != nil {
		return nil, tx.queryErr
	}
	r := &replayScriptRows{}
	for _, b := range tx.f.bindings {
		r.rows = append(r.rows, []any{b.reservation, b.delivery, b.event, b.app, b.subscription, b.destination, b.version, b.policy, b.key, b.attempts, b.ordinal, b.bodyDigest})
	}
	tx.rows = r
	return r, nil
}
func (tx *replayScriptTx) Exec(_ context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	if q != `INSERT INTO callback_replay_receipts(tenant,command,receipt) VALUES($1,$2,$3)` || len(args) != 3 || args[0] != tx.f.tenant || args[1] != tx.f.command {
		panic("unexpected write")
	}
	tx.inserted++
	tx.stored = args[2].(string)
	return pgconn.NewCommandTag(fmt.Sprintf("INSERT 0 %d", tx.affected)), tx.writeErr
}
func replayScriptFixture(n int) (*replayScriptTx, LeaseHandle, leaseCommand) {
	f := replayFactsFixture(n)
	result := "result"
	payload := "payload"
	c := leaseCommand{id: f.command, device: f.device, actorType: "app", capability: "message.send_text", app: &f.app, appEpoch: &f.appEpoch, attempt: &f.attempt, result: &result, payload: &payload, digest: f.commandDigest, snapshotDigest: f.snapshotDigest, status: f.status, effect: f.effect, admission: "bounded-result-v1"}
	return &replayScriptTx{f: f, complete: true, affected: 1}, LeaseHandle{Tenant: f.tenant, DeviceID: f.device, SessionID: "new_current_session"}, c
}

func TestCallbackReplayReceiptPersistAndVerify(t *testing.T) {
	for _, n := range []int{0, 1, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			tx, h, c := replayScriptFixture(n)
			if err := persistCallbackReplayReceipt(context.Background(), tx, h, c, 1024); err != nil || tx.inserted != 1 || tx.rows == nil || !tx.rows.closed {
				t.Fatal("receipt insertion or closure")
			}
			first := tx.stored
			if !strings.Contains(first, `"dispatchSession":"original_session"`) || strings.Contains(first, "new_current_session") {
				t.Fatal("reconnect rewrote original provenance")
			}
			if err := verifyCallbackReplayReceipt(context.Background(), tx, h, c); err != nil || tx.inserted != 1 || tx.stored != first {
				t.Fatal("replay mutated evidence")
			}
		})
	}
}

func TestCallbackReplayReceiptFailClosedAndBudget(t *testing.T) {
	for _, kind := range []string{"missing", "modified", "oversized", "malformed", "missing_parent", "row_error", "query_error", "wrong_device", "no_result", "bound", "negative_budget", "zero_insert", "write_failure"} {
		t.Run(kind, func(t *testing.T) {
			tx, h, c := replayScriptFixture(1)
			tx.stored, _ = canonicalCallbackReplayReceipt(tx.f)
			budget := 1024
			verify := true
			switch kind {
			case "missing":
				tx.stored = "MISSING"
			case "modified":
				tx.stored = strings.Replace(tx.stored, "original_session", "other", 1)
			case "oversized":
				tx.stored = strings.Repeat("x", callbackReplayReceiptLimit+1)
			case "malformed":
				tx.stored = "{"
			case "missing_parent":
				tx.complete = false
			case "row_error":
				tx.rowErr = errors.New("redacted scripted read")
			case "query_error":
				tx.queryErr = errors.New("redacted scripted query")
			case "wrong_device":
				h.DeviceID = "other"
			case "no_result":
				c.result = nil
			case "bound":
				budget = 196608
				verify = false
			case "negative_budget":
				budget = -1
				verify = false
			case "zero_insert":
				tx.affected = 0
				verify = false
			case "write_failure":
				tx.writeErr = errors.New("scripted insert failure")
				verify = false
			}
			var err error
			if verify {
				err = verifyCallbackReplayReceipt(context.Background(), tx, h, c)
			} else {
				err = persistCallbackReplayReceipt(context.Background(), tx, h, c, budget)
			}
			if err == nil {
				t.Fatal("uncertain/invalid receipt accepted")
			}
			if verify && tx.inserted != 0 {
				t.Fatal("replay backfilled receipt")
			}
			if tx.rows != nil && !tx.rows.closed {
				t.Fatal("rows leaked")
			}
		})
	}
	tx, h, c := replayScriptFixture(1)
	receipt, _ := canonicalCallbackReplayReceipt(tx.f)
	if err := persistCallbackReplayReceipt(context.Background(), tx, h, c, 196608-32768-len(receipt)); err != nil {
		t.Fatal("exact logical budget refused")
	}
	tx, h, c = replayScriptFixture(1)
	if err := persistCallbackReplayReceipt(context.Background(), tx, h, c, 196608-32768-len(receipt)+1); err == nil || tx.inserted != 0 {
		t.Fatal("over budget written")
	}
}
