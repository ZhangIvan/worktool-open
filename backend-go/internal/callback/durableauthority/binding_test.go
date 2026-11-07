package durableauthority

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"strings"
	"testing"
)

const testManifest = `{"version":1,"destinations":[{"tenant":"tenant_cb","app":"app_cb","destination":"destination_a","policy_version":1,"key_version":1,"url":"https://callbacks.example.com/result","cidrs":["93.184.216.0/24"],"signature_profile":"worktool-callback-hmac-sha256-v1","key_ref":"key_cb","provider_revision":{"kind":"exact","id":"revision_001"}}],"budgets":{"runner":{"acquire_ms":1000,"begin_ms":1000,"transaction_ms":3000,"commit_ms":1000,"cleanup_ms":1000},"worker":{"poll_ms":100,"recovery_ms":1000,"run_ms":1000,"shutdown_ms":9000,"join_headroom_ms":1000},"pool":{"max_connections":10,"system_reserve":1,"operational_reserve":1,"roles":[{"role":"worker","replicas":1,"max_conns":2}]}}}`
const testDescriptor = `{"schema":"worktool-destination-descriptor-v1","tenant":"tenant_cb","app":"app_cb","destination":"destination_a","policy_version":1,"key_version":1,"url":"https://callbacks.example.com/result","cidrs":["93.184.216.0/24"],"signature_profile":"worktool-callback-hmac-sha256-v1","key_ref":"key_cb","provider_authority_id":"provider_synthetic","provider_revision":{"kind":"exact","id":"revision_001"}}`

func config(t *testing.T) (composition.Plan, []registration.Descriptor, Binding) {
	t.Helper()
	p, e := composition.Parse([]byte(testManifest), "")
	if e != nil {
		t.Fatal(e)
	}
	d, e := registration.Decode([]byte(testDescriptor))
	if e != nil {
		t.Fatal(e)
	}
	ds := []registration.Descriptor{d}
	b, e := Bind(p, ds, Identity{"partition_synthetic", "allocation_synthetic", "worker_cb", "boot_cb", "epoch_cb", "provenance_synthetic", 1})
	if e != nil {
		t.Fatal(e)
	}
	return p, ds, b
}
func TestFullBindingContentAndRawPlan(t *testing.T) {
	p, ds, b := config(t)
	if e := b.validateRegistry(b.identity, b.planIdentity, b.digest, []byte("  "+testManifest+"\n"), b.descriptors); e != nil {
		t.Fatal(e)
	}
	for _, pair := range [][2]string{{"app_cb", "app_other"}, {"provider_synthetic", "provider_other"}, {"revision_001", "revision_002"}, {"key_cb", "key_other"}, {"93.184.216.0/24", "8.8.8.0/24"}, {"callbacks.example.com", "different.example.com"}, {`"policy_version":1`, `"policy_version":2`}, {`"key_version":1`, `"key_version":2`}} {
		t.Run(pair[1], func(t *testing.T) {
			raw := strings.ReplaceAll(testDescriptor, pair[0], pair[1])
			d, e := registration.Decode([]byte(raw))
			if e != nil {
				t.Fatal(e)
			}
			changed, e := Bind(p, []registration.Descriptor{d}, b.identity)
			if e == nil && changed.digest == b.digest {
				t.Fatal("changed descriptor retained identity")
			}
			candidate, _ := json.Marshal([]json.RawMessage{d.CanonicalBytes()})
			if b.validateRegistry(b.identity, b.planIdentity, b.digest, []byte(testManifest), candidate) == nil {
				t.Fatal("changed registry accepted")
			}
		})
	}
	for _, raw := range []string{strings.Replace(testManifest, `"max_connections":10`, `"max_connections":11`, 1), `{}`, testManifest + ` {}`} {
		if b.validateRegistry(b.identity, b.planIdentity, b.digest, []byte(raw), b.descriptors) == nil {
			t.Fatal("bad plan accepted")
		}
	}
	if _, e := Bind(p, append(ds, ds...), b.identity); e == nil {
		t.Fatal("duplicate")
	}
	if _, e := Bind(p, nil, b.identity); e == nil {
		t.Fatal("missing")
	}
	noncanonical := bytes.Replace(b.descriptors, []byte(`"schema":`), []byte(`"schema" : `), 1)
	if b.validateRegistry(b.identity, b.planIdentity, b.digest, []byte(testManifest), noncanonical) == nil {
		t.Fatal("noncanonical descriptor accepted")
	}
	changed := b.identity
	changed.Revision++
	if b.validateRegistry(changed, b.planIdentity, b.digest, []byte(testManifest), b.descriptors) == nil {
		t.Fatal("revision mismatch")
	}
	changed.Scope = ""
	if _, e := Bind(p, ds, changed); e == nil {
		t.Fatal("empty scope")
	}
	copied := append([]byte(nil), b.descriptors...)
	v := ds[0].Value()
	v.CIDRs[0] = "8.8.8.0/24"
	ds[0] = registration.Descriptor{}
	if !bytes.Equal(copied, b.descriptors) {
		t.Fatal("input mutation")
	}
	for _, value := range []any{b, b.identity, Evidence{Classification: "INCIDENT_BLOCKED", identity: b.digest}} {
		for _, f := range []string{"%v", "%+v", "%#v"} {
			s := fmt.Sprintf(f, value)
			if strings.Contains(s, "synthetic") || strings.Contains(s, b.digest) || strings.Contains(s, "callbacks.example") {
				t.Fatal("format leak")
			}
		}
	}
}
