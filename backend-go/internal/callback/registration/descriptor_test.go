package registration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func descriptorFixture() Value {
	return Value{
		Schema: SchemaVersion, Tenant: "tenant_synthetic", App: "app_synthetic", Destination: "dest_a",
		PolicyVersion: 7, KeyVersion: 11, URL: "https://hooks.example.com/callback",
		CIDRs: []string{"8.8.8.0/24", "2606:4700::/32"}, SignatureProfile: SignatureProfile,
		KeyRef: "synthetic-key-reference", ProviderAuthorityID: "provider_namespace_001",
		ProviderRevision: Revision{Kind: "exact", ID: "revision_001"},
	}
}

func descriptorJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodedDescriptor(t *testing.T, v Value) Descriptor {
	t.Helper()
	d, err := Decode(descriptorJSON(t, v))
	if err != nil || !d.Valid() {
		t.Fatalf("valid descriptor rejected: %v", err)
	}
	return d
}

func descriptorObject(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(descriptorJSON(t, descriptorFixture())))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func rejectedDescriptor(t *testing.T, raw []byte) {
	t.Helper()
	d, err := Decode(raw)
	if err != ErrDescriptor || d.Valid() || d.PolicyIdentity() != "" || d.KeyIdentity() != "" || d.BindingIdentity() != "" || len(d.CanonicalBytes()) != 0 || !reflect.DeepEqual(d.Value(), Value{}) {
		t.Fatal("invalid input did not return empty descriptor and redacted error")
	}
	if err.Error() != "CALLBACK_DESTINATION_DESCRIPTOR_INVALID" {
		t.Fatal("error leaked input")
	}
}

func TestDescriptorZeroAndRedaction(t *testing.T) {
	zero := Descriptor{}
	if zero.Valid() || zero.PolicyIdentity() != "" || zero.KeyIdentity() != "" || zero.BindingIdentity() != "" || zero.CanonicalBytes() != nil || !reflect.DeepEqual(zero.Value(), Value{}) {
		t.Fatal("zero descriptor is not empty and invalid")
	}
	for _, d := range []Descriptor{zero, decodedDescriptor(t, descriptorFixture())} {
		if b, err := json.Marshal(d); err != nil || string(b) != "{}" {
			t.Fatal("descriptor accidentally serializes private content")
		}
		for _, v := range []any{d, d.Value()} {
			if out := fmt.Sprintf("%v %+v %#v", v, v, v); strings.Contains(out, "example.com") || strings.Contains(out, "synthetic") || strings.Contains(out, "namespace") {
				t.Fatal("default formatting disclosed content")
			}
		}
	}
}

// Independently generated Python stdlib vectors pin this proposed v1 encoding.
// Python json.dumps(..., ensure_ascii=True, separators=(',', ':')) plus replacing
// literal '<', '&', '>' with their lowercase \u escapes matches Go's frozen HTML
// escaping. SHA-256 input is ASCII domain + NUL + that encoded projection.
func TestDescriptorV1KnownAnswer(t *testing.T) {
	v := descriptorFixture()
	v.KeyRef = "opaque<&>\"\\ref"
	d := decodedDescriptor(t, v)
	const canonical = `{"schema":"worktool-destination-descriptor-v1","tenant":"tenant_synthetic","app":"app_synthetic","destination":"dest_a","policy_version":7,"key_version":11,"url":"https://hooks.example.com/callback","cidrs":["2606:4700::/32","8.8.8.0/24"],"signature_profile":"worktool-callback-hmac-sha256-v1","key_ref":"opaque\u003c\u0026\u003e\"\\ref","provider_authority_id":"provider_namespace_001","provider_revision":{"kind":"exact","id":"revision_001"}}`
	if string(d.CanonicalBytes()) != canonical {
		t.Fatal("canonical descriptor encoding drift")
	}
	if d.PolicyIdentity() != "worktool-destination-policy-v1:sha256:85d5b89d36da7354f5d764f425ed8174304bc0c962743928b16b714ab55d73be" ||
		d.KeyIdentity() != "worktool-destination-key-v1:sha256:7f3e4a5f0d91ee3ee02054e5900369faca2c18a90e062f2dd089ba34a3177cf2" ||
		d.BindingIdentity() != "worktool-destination-binding-v1:sha256:9fd9d5c92e2d134c5996df965e4a5a34c0fe3c0ed1512803f8c2f501a39bb143" {
		t.Fatal("v1 content identity drift")
	}
	decoded, err := Decode([]byte(canonical))
	if err != nil || !reflect.DeepEqual(decoded.Value(), d.Value()) || !bytes.Equal(decoded.CanonicalBytes(), d.CanonicalBytes()) || decoded.BindingIdentity() != d.BindingIdentity() {
		t.Fatal("canonical reparse changed descriptor")
	}
	// Reusing the same content in different digest domains must not collide.
	if descriptorHash(policyDomain, []byte(canonical)) == descriptorHash(keyDomain, []byte(canonical)) || descriptorHash(keyDomain, []byte(canonical)) == descriptorHash(bindingDomain, []byte(canonical)) {
		t.Fatal("hash domains are not separated")
	}
}

func TestDescriptorProjectionIsolationAndCompleteBinding(t *testing.T) {
	base := decodedDescriptor(t, descriptorFixture())
	cases := []struct {
		name        string
		change      func(*Value)
		policy, key bool
	}{
		{"tenant", func(v *Value) { v.Tenant += "2" }, true, true},
		{"app", func(v *Value) { v.App += "2" }, true, true},
		{"destination", func(v *Value) { v.Destination += "2" }, true, true},
		{"policy_version", func(v *Value) { v.PolicyVersion++ }, true, false},
		{"url_same_version", func(v *Value) { v.URL += "2" }, true, false},
		{"cidr_same_version", func(v *Value) { v.CIDRs[0] = "8.8.4.0/24" }, true, false},
		{"key_version", func(v *Value) { v.KeyVersion++ }, false, true},
		{"key_ref_same_version", func(v *Value) { v.KeyRef += "2" }, false, true},
		{"provider_namespace_same_local_revision", func(v *Value) { v.ProviderAuthorityID += "2" }, false, true},
		{"revision_same_version", func(v *Value) { v.ProviderRevision.ID += "2" }, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := descriptorFixture()
			tc.change(&v)
			d := decodedDescriptor(t, v)
			if (d.PolicyIdentity() != base.PolicyIdentity()) != tc.policy || (d.KeyIdentity() != base.KeyIdentity()) != tc.key || d.BindingIdentity() == base.BindingIdentity() || bytes.Equal(d.CanonicalBytes(), base.CanonicalBytes()) {
				t.Fatal("projection isolation or full binding failed")
			}
		})
	}
	// Probe rejected future schema/profile/kind values only through private hash
	// helpers: they are bound but never accepted under the supported schema.
	for _, field := range []string{"schema", "signature_profile", "revision_kind"} {
		t.Run(field, func(t *testing.T) {
			v := base.Value()
			switch field {
			case "schema":
				v.Schema += "2"
			case "signature_profile":
				v.SignatureProfile += "2"
			case "revision_kind":
				v.ProviderRevision.Kind = "alias"
			}
			p, k, b := descriptorIdentities(v)
			if b == base.BindingIdentity() || (field != "revision_kind" && p == base.PolicyIdentity()) || (field != "signature_profile" && k == base.KeyIdentity()) {
				t.Fatal("unsupported domain field omitted from content projection")
			}
			rejectedDescriptor(t, descriptorJSON(t, v))
		})
	}
}

func TestDescriptorOrderingAndDefensiveCopies(t *testing.T) {
	v := descriptorFixture()
	raw := descriptorJSON(t, v)
	d, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	before := d.CanonicalBytes()
	id := d.BindingIdentity()
	for i := range raw {
		raw[i] = 'x'
	}
	value := d.Value()
	value.CIDRs[0] = "127.0.0.0/8"
	value.URL = "https://evil.example.com/"
	value.ProviderRevision.ID = "latest"
	value.ProviderAuthorityID = "other"
	canonical := d.CanonicalBytes()
	for i := range canonical {
		canonical[i] = 'x'
	}
	if !bytes.Equal(d.CanonicalBytes(), before) || d.BindingIdentity() != id || d.Value().CIDRs[0] != "2606:4700::/32" || d.Value().URL != v.URL || d.Value().ProviderRevision.ID != v.ProviderRevision.ID {
		t.Fatal("input or returned value aliases immutable content")
	}
	v.CIDRs = []string{"2606:4700:0000:0000:0000:0000:0000:0000/32", "8.8.8.0/24"}
	other := decodedDescriptor(t, v)
	if !bytes.Equal(other.CanonicalBytes(), before) || other.PolicyIdentity() != d.PolicyIdentity() || other.KeyIdentity() != d.KeyIdentity() || other.BindingIdentity() != id {
		t.Fatal("ordering or equivalent IPv6 representation changed identity")
	}
	// Map ordering, insignificant whitespace and ASCII escape spelling are not
	// content changes. Unknown plan/budget fields remain rejected, not ignored.
	m := descriptorObject(t)
	m["cidrs"] = []any{"2606:4700::/32", "8.8.8.0/24"}
	ordered := bytes.Replace(descriptorJSON(t, m), []byte("tenant_synthetic"), []byte(`\u0074enant_synthetic`), 1)
	ordered = append([]byte("\n\t"), append(ordered, []byte(" \r\n")...)...)
	other, err = Decode(ordered)
	if err != nil || !bytes.Equal(other.CanonicalBytes(), before) || other.BindingIdentity() != id {
		t.Fatal("JSON presentation changed identity")
	}
}

func TestDescriptorConcurrentCopyAccess(t *testing.T) {
	d := decodedDescriptor(t, descriptorFixture())
	before := d.CanonicalBytes()
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				v := d.Value()
				v.CIDRs[0] = "changed"
				b := d.CanonicalBytes()
				b[0] = 'x'
				if !d.Valid() || d.PolicyIdentity() == "" || d.KeyIdentity() == "" || d.BindingIdentity() == "" {
					t.Error("immutable value changed")
				}
			}
		}()
	}
	wg.Wait()
	if !bytes.Equal(before, d.CanonicalBytes()) {
		t.Fatal("concurrent copy accesses mutated descriptor")
	}
}

func TestDescriptorExactSchema(t *testing.T) {
	for _, nested := range []bool{false, true} {
		locate := func(m map[string]any) map[string]any {
			if nested {
				return m["provider_revision"].(map[string]any)
			}
			return m
		}
		for key := range locate(descriptorObject(t)) {
			for _, mode := range []string{"missing", "null", "case_alias", "duplicate", "escaped_duplicate"} {
				t.Run(fmt.Sprintf("nested_%v_%s_%s", nested, key, mode), func(t *testing.T) {
					m := descriptorObject(t)
					obj := locate(m)
					switch mode {
					case "missing":
						delete(obj, key)
					case "null":
						obj[key] = nil
					case "case_alias":
						obj[strings.ToUpper(key)] = obj[key]
						delete(obj, key)
					default:
						before := descriptorJSON(t, obj)
						name := strconv.Quote(key)
						if mode == "escaped_duplicate" {
							name = `"\u` + fmt.Sprintf("%04x", key[0]) + key[1:] + `"`
						}
						after := append([]byte("{"+name+":"+string(descriptorJSON(t, obj[key]))+","), before[1:]...)
						rejectedDescriptor(t, bytes.Replace(descriptorJSON(t, m), before, after, 1))
						return
					}
					rejectedDescriptor(t, descriptorJSON(t, m))
				})
			}
		}
		for _, key := range []string{"policy_identity", "key_identity", "binding_identity", "trusted", "enabled", "approved", "secret_bytes", "budgets", "destinations", "plan_identity"} {
			t.Run(fmt.Sprintf("nested_%v_unknown_%s", nested, key), func(t *testing.T) {
				m := descriptorObject(t)
				locate(m)[key] = "DO_NOT_ECHO_SECRET"
				rejectedDescriptor(t, descriptorJSON(t, m))
			})
		}
	}
}

func TestDescriptorStrictTypesAndIntegerTokens(t *testing.T) {
	for _, nested := range []bool{false, true} {
		locate := func(m map[string]any) map[string]any {
			if nested {
				return m["provider_revision"].(map[string]any)
			}
			return m
		}
		for key, original := range locate(descriptorObject(t)) {
			for n, wrong := range []any{nil, true, json.Number("1"), "wrong", []any{}, map[string]any{}} {
				// Values matching the original category are validated separately.
				if reflect.TypeOf(wrong) == reflect.TypeOf(original) {
					continue
				}
				t.Run(fmt.Sprintf("nested_%v_%s_wrong_%d", nested, key, n), func(t *testing.T) {
					m := descriptorObject(t)
					locate(m)[key] = wrong
					rejectedDescriptor(t, descriptorJSON(t, m))
				})
			}
		}
	}
	for _, wrong := range []any{nil, true, json.Number("1"), map[string]any{}, []any{}} {
		m := descriptorObject(t)
		m["cidrs"] = []any{wrong}
		rejectedDescriptor(t, descriptorJSON(t, m))
	}
	for _, key := range []string{"policy_version", "key_version"} {
		for _, token := range []string{"0", "-0", "-1", "1.0", "1e0", "1E0", "1e100", "+1", "01", "9223372036854775808", "-9223372036854775809", `"1"`, "true", "null", "[]", "{}"} {
			t.Run(key+"_"+token, func(t *testing.T) {
				m := descriptorObject(t)
				raw := descriptorJSON(t, m)
				old := []byte(strconv.Quote(key) + ":" + string(descriptorJSON(t, m[key])))
				rejectedDescriptor(t, bytes.Replace(raw, old, []byte(strconv.Quote(key)+":"+token), 1))
			})
		}
	}
	for _, version := range []int64{1, 9007199254740991, 9007199254740992, math.MaxInt64} {
		v := descriptorFixture()
		v.PolicyVersion, v.KeyVersion = version, version
		got := decodedDescriptor(t, v).Value()
		if got.PolicyVersion != version || got.KeyVersion != version {
			t.Fatal("exact int64 version lost precision")
		}
	}
}

func TestDescriptorJSONBoundsAndMalformedInput(t *testing.T) {
	valid := descriptorJSON(t, descriptorFixture())
	inputs := [][]byte{nil, {}, []byte("null"), []byte("[]"), []byte("true"), []byte(`"text"`), []byte("1"), []byte("{}"), []byte("{"), append(append([]byte{}, valid...), valid...), append(append([]byte{}, valid...), []byte(" garbage")...), bytes.Replace(valid, []byte("tenant_synthetic"), []byte{0xff}, 1), bytes.Replace(valid, []byte("tenant_synthetic"), []byte(`\ud800`), 1), bytes.Replace(valid, []byte("tenant_synthetic"), []byte(`\udc00`), 1), []byte(strings.Repeat("[", 66) + strings.Repeat("]", 66))}
	for n, raw := range inputs {
		t.Run(strconv.Itoa(n), func(t *testing.T) { rejectedDescriptor(t, raw) })
	}
	atCap := append(append([]byte{}, valid...), bytes.Repeat([]byte(" "), MaxDescriptorBytes-len(valid))...)
	if _, err := Decode(atCap); err != nil {
		t.Fatal("valid JSON at exact byte cap rejected")
	}
	rejectedDescriptor(t, append(atCap, ' '))
}

func TestDescriptorIdentifierAndReferenceBounds(t *testing.T) {
	fields := map[string]func(*Value) *string{
		"tenant": func(v *Value) *string { return &v.Tenant }, "app": func(v *Value) *string { return &v.App },
		"destination":           func(v *Value) *string { return &v.Destination },
		"provider_authority_id": func(v *Value) *string { return &v.ProviderAuthorityID },
		"provider_revision_id":  func(v *Value) *string { return &v.ProviderRevision.ID },
	}
	for name, target := range fields {
		for _, good := range []string{"a", "A0_-z", strings.Repeat("a", 128)} {
			v := descriptorFixture()
			*target(&v) = good
			decodedDescriptor(t, v)
		}
		for n, bad := range []string{"", strings.Repeat("a", 129), "white space", "a/b", "a:b", "https://provider.example.com/", "a.b", "a\x00b", "a\nb", "a\tb", "a\x7fb", "秘密", "é", "a😀b"} {
			t.Run(name+"_"+strconv.Itoa(n), func(t *testing.T) {
				v := descriptorFixture()
				*target(&v) = bad
				rejectedDescriptor(t, descriptorJSON(t, v))
			})
		}
	}
	for _, good := range []string{"!", "~", strings.Repeat("k", 256), "opaque<&>\"\\ref"} {
		v := descriptorFixture()
		v.KeyRef = good
		decodedDescriptor(t, v)
	}
	for _, bad := range []string{"", strings.Repeat("k", 257), "white space", "\nsecret", "\tsecret", "\x00secret", "secret\x7f", "密钥", "a😀b"} {
		v := descriptorFixture()
		v.KeyRef = bad
		rejectedDescriptor(t, descriptorJSON(t, v))
	}
	for _, bad := range []string{"latest", "CURRENT", "active", "default", "alias", "fallback", "AWSCURRENT", "AWSPREVIOUS", "AWSPENDING", "revision-latest-001", "revision_Alias_001"} {
		v := descriptorFixture()
		v.ProviderRevision.ID = bad
		rejectedDescriptor(t, descriptorJSON(t, v))
	}
	for _, kind := range []string{"", "Exact", "alias", "version", "latest"} {
		v := descriptorFixture()
		v.ProviderRevision.Kind = kind
		rejectedDescriptor(t, descriptorJSON(t, v))
	}
}

func TestDescriptorReusesDestinationPolicy(t *testing.T) {
	for _, url := range []string{"", "https://Hooks.Example.com/callback", "https://hooks.example.com:443/callback", "https://hooks.example.com", "http://hooks.example.com/", "https://user:secret@hooks.example.com/", "https://127.0.0.1/", "https://hooks.example.com/a?secret=x", "https://hooks.example.com/#f", "https://hooks.example.com/%2f", "https://hooks.example.com/a/../b", "https://hooks.example.com//b", "https://foo.internal/"} {
		v := descriptorFixture()
		v.URL = url
		rejectedDescriptor(t, descriptorJSON(t, v))
	}
	v := descriptorFixture()
	v.URL = "https://hooks.example.com/" + strings.Repeat("a", 2048-len("https://hooks.example.com/"))
	decodedDescriptor(t, v)
	v.URL += "a"
	rejectedDescriptor(t, descriptorJSON(t, v))
	for _, cidrs := range [][]string{nil, {}, {""}, {"not-a-prefix"}, {"127.0.0.0/8"}, {"10.0.0.0/8"}, {"8.8.8.1/24"}, {"0.0.0.0/0"}, {"8.8.8.0/24", "8.8.8.0/24"}, {"2606:4700::/32", "2606:4700:0000::/32"}, {"::ffff:8.8.8.0/120"}, {"2001:db8::/32"}, {"8.0.0.0/7"}} {
		v := descriptorFixture()
		v.CIDRs = cidrs
		rejectedDescriptor(t, descriptorJSON(t, v))
	}
	for _, count := range []int{1, MaxDescriptorCIDRs, MaxDescriptorCIDRs + 1} {
		v := descriptorFixture()
		v.CIDRs = nil
		for n := 0; n < count; n++ {
			v.CIDRs = append(v.CIDRs, fmt.Sprintf("8.8.%d.0/24", n))
		}
		if count <= MaxDescriptorCIDRs {
			decodedDescriptor(t, v)
		} else {
			rejectedDescriptor(t, descriptorJSON(t, v))
		}
	}
}

func TestDescriptorPureSurface(t *testing.T) {
	allowed := map[string]bool{"crypto/sha256": true, "encoding/hex": true, "encoding/json": true, "errors": true, "net/netip": true, "sort": true, "strconv": true, "strings": true,
		"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security": true,
		"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol":          true}
	raw, err := os.ReadFile("descriptor.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "descriptor.go", raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if !allowed[path] {
			t.Fatal("descriptor has unreviewed runtime import")
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt:
			t.Error("descriptor starts goroutine")
		case *ast.FuncDecl:
			if node.Name.Name == "init" || (node.Recv == nil && node.Name.IsExported() && node.Name.Name != "Decode") {
				t.Error("descriptor exposes extra constructor or initializer")
			}
		case *ast.CallExpr:
			if selector, ok := node.Fun.(*ast.SelectorExpr); ok {
				if receiver, ok := selector.X.(*ast.Ident); ok {
					if receiver.Name == "security" && selector.Sel.Name != "NewPolicy" {
						t.Error("descriptor exposes policy/transport authority")
					}
					if receiver.Name == "protocol" && selector.Sel.Name != "Decode" && selector.Sel.Name != "ExactObject" {
						t.Error("unreviewed protocol helper")
					}
				}
			}
		}
		return true
	})
}

func FuzzDescriptorDecode(f *testing.F) {
	v := descriptorFixture()
	valid, _ := json.Marshal(v)
	f.Add(valid)
	f.Add([]byte(`{"schema":"worktool-destination-descriptor-v1","schema":"duplicate"}`))
	f.Add([]byte("null"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		d, err := Decode(raw)
		if err != nil {
			if err != ErrDescriptor || d.Valid() || len(d.CanonicalBytes()) != 0 {
				t.Fatal("invalid descriptor did not fail closed")
			}
			return
		}
		if !d.Valid() || len(d.CanonicalBytes()) > MaxDescriptorBytes {
			t.Fatal("accepted descriptor exceeded canonical bound")
		}
		roundtrip, err := Decode(d.CanonicalBytes())
		if err != nil || !bytes.Equal(roundtrip.CanonicalBytes(), d.CanonicalBytes()) || roundtrip.PolicyIdentity() != d.PolicyIdentity() || roundtrip.KeyIdentity() != d.KeyIdentity() || roundtrip.BindingIdentity() != d.BindingIdentity() {
			t.Fatal("canonical reparse changed content or identity")
		}
	})
}
