package pg

import (
	wire "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol/gatewaywire"
	"testing"
)

func TestGatewayUnstartedReconciliationRequiresHistoryAndEvidence(t *testing.T) {
	for _, status := range []string{"rejected", "cancelled", "expired"} {
		r := &wire.ExecutionResult{Status: status, EffectState: "not_started", EvidenceRefs: []string{"journal_synthetic"}}
		c := leaseCommand{status: "execution_unknown", effect: "unknown"}
		event, e := resultEvent(c, r, false)
		if e != nil || event != "reconcile_unstarted_"+status {
			t.Fatal("valid unstarted mapping")
		}
		if _, e = resultEvent(c, r, true); e == nil {
			t.Fatal("begun history ignored")
		}
		r.EvidenceRefs = nil
		if _, e = resultEvent(c, r, false); e == nil {
			t.Fatal("no evidence accepted")
		}
		r.EvidenceRefs = []string{"journal_synthetic"}
		c.effect = "applied"
		if _, e = resultEvent(c, r, false); e == nil {
			t.Fatal("known effect ignored")
		}
	}
}
func TestGatewayResultAckBindsCurrentEvent(t *testing.T) {
	f := wire.DeviceFrame{Header: wire.Header{Kind: "execution_result", EventID: "evt_new"}, Body: wire.DeviceBody{Result: &wire.ExecutionResult{CommandID: "cmd_synthetic"}}}
	a := ackFor(f)
	if a.Kind != "result_ack" || a.Body.ResultAck.AckEventID != "evt_new" || a.Body.ResultAck.CommandID != "cmd_synthetic" {
		t.Fatal("result correlation")
	}
	f.Header.Kind = "transport_ack"
	if ackFor(f).Kind != "" {
		t.Fatal("recursive ACK")
	}
}
