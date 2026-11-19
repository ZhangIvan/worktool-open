package pg

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCallbackProjectionBoundedAndImmutable(t *testing.T) {
	body, err := callbackEventBody("evt_a", "batch_a", "msg_a", "child_a", "cmd_a", "succeeded", "applied")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err = json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 10 || fields["mode"] != "synthetic" || fields["eventSchemaVersion"] != "bounded-result-v1" {
		t.Fatal("projection contract")
	}
	again, err := callbackEventBody("evt_a", "batch_a", "msg_a", "child_a", "cmd_a", "succeeded", "applied")
	if err != nil || again != body || hashText(again) != hashText(body) {
		t.Fatal("unstable projection")
	}
	if strings.Contains(body, "targetId") || strings.Contains(body, "payload") {
		t.Fatal("payload disclosure")
	}
}
func TestCallbackProjectionRejectsInvalidEvidence(t *testing.T) {
	for _, p := range [][2]string{{"succeeded", "unknown"}, {"rejected", "applied"}, {"running", "unknown"}, {"failed", "partial"}, {"partially_succeeded", "applied"}} {
		if _, err := callbackEventBody("evt_a", "batch_a", "msg_a", "child_a", "cmd_a", p[0], p[1]); err == nil {
			t.Fatal(p)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 129), "id\n", "用户", "https://example.com"} {
		if _, err := callbackEventBody(id, "batch_a", "msg_a", "child_a", "cmd_a", "succeeded", "applied"); err == nil {
			t.Fatal("invalid identifier")
		}
	}
	for _, p := range [][2]string{{"rejected", "not_started"}, {"cancelled", "not_applied"}, {"partially_succeeded", "partially_applied"}, {"execution_unknown", "unknown"}} {
		if _, err := callbackEventBody("evt_a", "batch_a", "msg_a", "child_a", "cmd_a", p[0], p[1]); err != nil {
			t.Fatal(p, err)
		}
	}
}
func TestSyntheticCallbackConstructorFailsClosed(t *testing.T) {
	for _, ttl := range []time.Duration{-1, 0, 25 * time.Hour, time.Minute} {
		if _, err := NewSyntheticCallbackGatewayStore(nil, nil, RuntimeAuthority{}, ttl); err == nil {
			t.Fatal("untrusted composition")
		}
	}
}
