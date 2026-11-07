package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type replayCorruptionCase struct {
	fanout int
	path   string
	kind   string
}

func replayCorruptionMatrix() []replayCorruptionCase {
	var cases []replayCorruptionCase
	for _, fanout := range []int{0, 1, 4} {
		for _, path := range []string{"prior_event", "fresh_event", "reconnected"} {
			for _, kind := range []string{"missing", "result_digest"} {
				cases = append(cases, replayCorruptionCase{fanout, path, kind})
			}
		}
	}
	for _, kind := range []string{"dispatch_session", "callback_binding", "noncanonical", "empty_object", "wrong_type"} {
		cases = append(cases, replayCorruptionCase{1, "fresh_event", kind})
	}
	return cases
}

// Deliberately corrupts only synthetic fixture bytes. The normal table trigger
// forbids these writes; the integration helper restores that trigger before
// sending the rejected frame. This is not a supported client mutation path.
func corruptReplayReceipt(raw, kind string) (*string, error) {
	if kind == "missing" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil || object == nil {
		return nil, errors.New("invalid synthetic receipt")
	}
	switch kind {
	case "result_digest":
		value := strings.Repeat("0", 64)
		if string(object["resultDigest"]) == `"`+value+`"` {
			value = strings.Repeat("1", 64)
		}
		object["resultDigest"], _ = json.Marshal(value)
	case "dispatch_session":
		object["dispatchSession"] = json.RawMessage(`"session_corrupted"`)
	case "callback_binding":
		var bindings []map[string]json.RawMessage
		if json.Unmarshal(object["callbacks"], &bindings) != nil || len(bindings) == 0 {
			return nil, errors.New("synthetic callback binding missing")
		}
		bindings[0]["destination"] = json.RawMessage(`"destination_corrupted"`)
		object["callbacks"], _ = json.Marshal(bindings)
	case "noncanonical":
		value := " " + raw
		return &value, nil
	case "empty_object":
		value := "{}"
		return &value, nil
	case "wrong_type":
		object["dispatchEpoch"] = json.RawMessage(`"not_an_epoch"`)
	default:
		return nil, errors.New("unknown synthetic corruption")
	}
	encoded, err := json.Marshal(object)
	if err != nil || string(encoded) == raw {
		return nil, errors.New("synthetic corruption did not change receipt")
	}
	value := string(encoded)
	return &value, nil
}

// Called only by the established CB02 group. Gate precedes every schema,
// connection and listener; compiling this source is not a PostgreSQL result.
func replayReceiptCorruptionCases(t *testing.T) {
	gatewayGate(t)
	for _, tc := range replayCorruptionMatrix() {
		t.Run(fmt.Sprintf("receipt_no_ack_F%d_%s_%s", tc.fanout, tc.path, tc.kind), func(t *testing.T) {
			f := replayCorruptionFixture(t, tc.fanout)
			d := f.enroll(t)
			p := f.dial(t, d, f.session(t, d))
			p.hello(t)
			id, _ := f.submit(t, d, "receipt_corruption")
			cmd := commandBody(p.until(t, "command"))
			start(t, p, cmd)
			body := result(cmd, "SYNTHETIC_APPLIED")
			p.seq++
			event := "evt_" + randHex(t, 16)
			originalFrame := map[string]any{"version": "1.0", "kind": "execution_result", "eventId": event,
				"tenantId": "tenant_demo", "deviceId": p.device, "sessionId": p.session, "seq": p.seq,
				"sentAt": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), "mode": "synthetic", "body": body}
			p.writeJSON(t, originalFrame)
			assertAck(t, p, event, id)

			// A positive control proves this exact replay path works before corruption.
			switch tc.path {
			case "prior_event":
				p.writeJSON(t, originalFrame)
				assertAck(t, p, event, id)
			case "fresh_event":
				assertAck(t, p, p.send(t, "execution_result", body), id)
			case "reconnected":
				_ = p.ws.CloseNow()
				waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
				p = f.dial(t, d, f.session(t, d))
				p.hello(t)
				assertAck(t, p, p.send(t, "execution_result", body), id)
			default:
				t.Fatal("invalid synthetic replay path")
			}
			if f.count(t, `SELECT count(*) FROM inbox WHERE event=$1 AND session=$2 AND seq=$3`, event, originalFrame["sessionId"], originalFrame["seq"]) != 1 {
				t.Fatal("first result inbox evidence missing")
			}
			if f.count(t, `SELECT count(*) FROM callback_outbox WHERE command=$1`, id) != int64(tc.fanout) {
				t.Fatal("synthetic callback fanout mismatch")
			}
			var original string
			if err := f.owner.QueryRow(f.ctx, `SELECT receipt FROM callback_replay_receipts WHERE tenant='tenant_demo' AND command=$1`, id).Scan(&original); err != nil {
				t.Fatal("first result receipt missing")
			}
			changed, err := corruptReplayReceipt(original, tc.kind)
			if err != nil || (changed != nil && (len(*changed) > 16384 || *changed == original)) {
				t.Fatal("synthetic corruption invalid")
			}
			corruptReplayFixture(t, f, id, changed)
			before := replayCorruptionSnapshot(t, f)
			if tc.path == "prior_event" {
				p.writeJSON(t, originalFrame)
			} else {
				p.send(t, "execution_result", body)
			}
			assertReplayFrameRejected(t, f, p)
			if after := replayCorruptionSnapshot(t, f); after != before {
				t.Fatal("rejected replay repaired evidence, grew ACK/inbox or changed retained graph/charges")
			}
		})
	}
}

func replayCorruptionFixture(t *testing.T, fanout int) *gwFixture {
	t.Helper()
	if fanout == 0 {
		return newGW(t)
	}
	f := newGWWithStoreConstructor(t, callbackSyntheticStore)
	callbackSetup(t, f)
	for i := 1; i < fanout; i++ {
		destination, subscription := fmt.Sprintf("dest_corrupt_%d", i), fmt.Sprintf("sub_corrupt_%d", i)
		execOwner(t, f.owner, f.ctx, `INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('tenant_demo',$1,'app_demo','https://callbacks.example.com/result',1,'key_demo',1,'active')`, destination)
		execOwner(t, f.owner, f.ctx, `INSERT INTO scope_limits(scope_kind,tenant,scope_id,destination_id,max_in_flight,rate_per_sec,burst,tokens,policy_version,enabled,capacity_jobs,capacity_bytes,admission_enabled) VALUES('destination','tenant_demo',$1,$1,2,8,16,16,1,true,100,20000000,true)`, destination)
		execOwner(t, f.owner, f.ctx, `INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('tenant_demo','app_demo',$1,1,$2,1,1,'command.result','bounded-result-v1',1,65536,4)`, subscription, destination)
	}
	return f
}

func corruptReplayFixture(t *testing.T, f *gwFixture, command string, changed *string) {
	t.Helper()
	tx, err := f.owner.Begin(f.ctx)
	if err != nil {
		t.Fatal("corruption fixture transaction")
	}
	defer resultGuardRollback(t, tx)
	execOwner(t, tx, f.ctx, `ALTER TABLE callback_replay_receipts DISABLE TRIGGER callback_replay_receipt_guard`)
	var affected int64
	if changed == nil {
		tag, err := tx.Exec(f.ctx, `DELETE FROM callback_replay_receipts WHERE tenant='tenant_demo' AND command=$1`, command)
		if err != nil {
			t.Fatal("synthetic missing receipt fixture")
		}
		affected = tag.RowsAffected()
	} else {
		tag, err := tx.Exec(f.ctx, `UPDATE callback_replay_receipts SET receipt=$2 WHERE tenant='tenant_demo' AND command=$1`, command, *changed)
		if err != nil {
			t.Fatal("synthetic changed receipt fixture")
		}
		affected = tag.RowsAffected()
	}
	if affected != 1 {
		t.Fatal("corruption fixture did not target exactly one receipt")
	}
	execOwner(t, tx, f.ctx, `ALTER TABLE callback_replay_receipts ENABLE TRIGGER callback_replay_receipt_guard`)
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal("synthetic corruption commit")
	}
	if f.count(t, `SELECT count(*) FROM pg_trigger WHERE tgrelid='callback_replay_receipts'::regclass AND tgname='callback_replay_receipt_guard' AND tgenabled='O'`) != 1 {
		t.Fatal("receipt guard was not restored before ingress")
	}
}

func assertReplayFrameRejected(t *testing.T, f *gwFixture, p *peer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	_, _, err := p.ws.Read(ctx)
	var closeError websocket.CloseError
	// A timeout or any unrelated close is a failed test, not proof of no ACK.
	if !errors.As(err, &closeError) || closeError.Code != websocket.StatusPolicyViolation || closeError.Reason != "FRAME_REJECTED" {
		t.Fatal("corrupt replay did not yield explicit FRAME_REJECTED without an ACK")
	}
	waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
}

func replayCorruptionSnapshot(t *testing.T, f *gwFixture) string {
	t.Helper()
	var value string
	err := f.owner.QueryRow(f.ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY command) FROM callback_replay_receipts x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM commands x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY command) FROM developer_messages x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY reservation_id) FROM admission_reservations x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY delivery_id) FROM callback_outbox x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY event) FROM developer_callbacks x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY delivery_id,fence) FROM callback_attempts x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM callback_destinations x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY destination_id,policy_version,key_version) FROM callback_destination_versions x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY subscription_id,version) FROM callback_subscription_versions x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM developer_apps x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY scope_kind,scope_id,slot_no) FROM scope_slots x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY scope_kind,scope_id) FROM scope_limits x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY command,event_id) FROM command_events x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY command) FROM tombstones x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY event) FROM inbox x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit x WHERE action IN ('command.result','command.result_conflict')),
 (SELECT jsonb_agg(jsonb_build_array(id,ws_recv_seq) ORDER BY id) FROM device_sessions)
 )::text`).Scan(&value)
	if err != nil {
		t.Fatal("synthetic retained graph fingerprint failed")
	}
	return value
}

func TestReplayCorruptionMatrixAndMutation(t *testing.T) {
	const raw = `{"callbacks":[{"destination":"destination_original"}],"dispatchEpoch":1,"dispatchSession":"session_original","resultDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	cases := replayCorruptionMatrix()
	if len(cases) != 23 {
		t.Fatal("bounded corruption matrix changed")
	}
	seen := make(map[replayCorruptionCase]bool)
	for _, tc := range cases {
		if seen[tc] {
			t.Fatal("duplicate corruption case")
		}
		seen[tc] = true
		changed, err := corruptReplayReceipt(raw, tc.kind)
		if err != nil || (tc.kind == "missing") != (changed == nil) {
			t.Fatal("invalid mutation classification")
		}
		if changed != nil {
			var object map[string]any
			if *changed == raw || len(*changed) > 16384 || json.Unmarshal([]byte(*changed), &object) != nil || object == nil {
				t.Fatal("mutation must remain a bounded JSON object")
			}
		}
	}
	for _, bad := range []struct{ raw, kind string }{{"{", "result_digest"}, {"null", "result_digest"}, {"{}", "callback_binding"}, {raw, "unknown"}} {
		if _, err := corruptReplayReceipt(bad.raw, bad.kind); err == nil {
			t.Fatal("invalid mutation input accepted")
		}
	}
}
