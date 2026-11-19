package gateway

import (
	"fmt"
	"testing"
)

// Source-only children of CB02. The unchanged gate skips before fixtures,
// connections or listeners. No PostgreSQL execution is claimed for this candidate.
func callbackReplayReceiptIntegrationCases(t *testing.T) {
	gatewayGate(t)
	for _, fanout := range []int{0, 1, 4} {
		t.Run(fmt.Sprintf("replay_receipt_F%d", fanout), func(t *testing.T) {
			var f *gwFixture
			if fanout == 0 {
				f = newGW(t)
			} else {
				f = newGWWithStoreConstructor(t, callbackSyntheticStore)
				callbackSetup(t, f)
				for i := 1; i < fanout; i++ {
					dest, sub := fmt.Sprintf("dest_extra_%d", i), fmt.Sprintf("sub_extra_%d", i)
					execOwner(t, f.owner, f.ctx, `INSERT INTO callback_destinations(tenant,id,app,canonical_url,policy_version,key_ref,key_version,state) VALUES('tenant_demo',$1,'app_demo','https://callbacks.example.com/result',1,'key_demo',1,'active');`, dest)
					execOwner(t, f.owner, f.ctx, `INSERT INTO scope_limits(scope_kind,tenant,scope_id,destination_id,max_in_flight,rate_per_sec,burst,tokens,policy_version,enabled,capacity_jobs,capacity_bytes,admission_enabled) VALUES('destination','tenant_demo',$1,$1,2,8,16,16,1,true,100,20000000,true)`, dest)
					execOwner(t, f.owner, f.ctx, `INSERT INTO callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,event_type,event_schema_version,max_events_per_child,max_body_bytes,max_attempts) VALUES('tenant_demo','app_demo',$1,1,$2,1,1,'command.result','bounded-result-v1',1,65536,4)`, sub, dest)
				}
			}
			d := f.enroll(t)
			p := f.dial(t, d, f.session(t, d))
			p.hello(t)
			originalSession := p.session
			id, _ := f.submit(t, d, "receipt_first_result")
			cmd := commandBody(p.until(t, "command"))
			start(t, p, cmd)
			event := p.send(t, "execution_result", result(cmd, "OK"))
			assertAck(t, p, event, id)
			var receipt string
			if err := f.owner.QueryRow(f.ctx, `SELECT receipt FROM callback_replay_receipts WHERE tenant='tenant_demo' AND command=$1`, id).Scan(&receipt); err != nil || len(receipt) > 16384 {
				t.Fatal("bounded first-result receipt missing")
			}
			if f.count(t, `SELECT count(*) FROM callback_replay_receipts WHERE receipt::json->>'dispatchSession'=$1`, originalSession) != 1 || f.count(t, `SELECT count(*) FROM callback_outbox`) != int64(fanout) {
				t.Fatal("original provenance or callback fanout")
			}
			snapshot := func() string {
				var value string
				err := f.owner.QueryRow(f.ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY command) FROM callback_replay_receipts x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM commands x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY reservation_id) FROM admission_reservations x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY delivery_id) FROM callback_outbox x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY event) FROM developer_callbacks x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY scope_kind,scope_id) FROM scope_limits x),
 (SELECT count(*) FROM inbox),(SELECT count(*) FROM outbox))::text`).Scan(&value)
				if err != nil {
					t.Fatal("synthetic graph fingerprint")
				}
				return value
			}
			before := snapshot()
			fresh := p.send(t, "execution_result", result(cmd, "OK"))
			assertAck(t, p, fresh, id)
			if snapshot() != before {
				t.Fatal("duplicate result changed retained graph or charge")
			}
			for _, q := range []string{
				`UPDATE commands SET result=NULL WHERE tenant='tenant_demo' AND id=$1`,
				`UPDATE commands SET result='{}' WHERE tenant='tenant_demo' AND id=$1`,
				`UPDATE callback_replay_receipts SET receipt=receipt WHERE tenant='tenant_demo' AND command=$1`,
				`DELETE FROM callback_replay_receipts WHERE tenant='tenant_demo' AND command=$1`,
			} {
				if _, err := f.owner.Exec(f.ctx, q, id); stateCode(err) != "23514" {
					t.Fatal("immutable evidence mutation not rejected")
				}
			}
			if snapshot() != before {
				t.Fatal("rejected mutation changed graph")
			}
			_ = p.ws.CloseNow()
			waitCondition(t, f, func() bool { return f.controller.LiveCount() == 0 })
			// Revoking future application authority must not discard old execution evidence.
			execOwner(t, f.owner, f.ctx, `UPDATE developer_apps SET active=false,auth_epoch=auth_epoch+1 WHERE tenant='tenant_demo' AND id='app_demo'`)
			p2 := f.dial(t, d, f.session(t, d))
			p2.hello(t)
			replayed := p2.send(t, "execution_result", result(cmd, "OK"))
			assertAck(t, p2, replayed, id)
			var after string
			if err := f.owner.QueryRow(f.ctx, `SELECT receipt FROM callback_replay_receipts WHERE tenant='tenant_demo' AND command=$1`, id).Scan(&after); err != nil || after != receipt {
				t.Fatal("reconnect rewrote first-result receipt")
			}
		})
	}
}
