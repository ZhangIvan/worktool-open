// Package registration contains a local, non-activating registration candidate.
// A descriptor is pure content, never authenticated writer or send authority.
package registration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
)

const (
	SchemaVersion      = "worktool-destination-descriptor-v1"
	SignatureProfile   = "worktool-callback-hmac-sha256-v1"
	MaxDescriptorBytes = 16384
	MaxDescriptorCIDRs = 32
)

const (
	policyDomain  = "worktool-destination-policy-v1"
	keyDomain     = "worktool-destination-key-v1"
	bindingDomain = "worktool-destination-binding-v1"
)

// ErrDescriptor never includes untrusted descriptor bytes or provider references.
var ErrDescriptor = errors.New("CALLBACK_DESTINATION_DESCRIPTOR_INVALID")

// Revision declares an exact provider-local immutable revision. Syntax cannot
// establish provider immutability or authenticate its authority mapping.
type Revision struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Value is the complete fixed schema, not an authority or runtime configuration.
// IDs are 1..128 ASCII letters, digits, '_' or '-'. KeyRef is 1..256 printable
// non-space ASCII bytes, opaque to this package. ProviderAuthorityID is a
// non-secret identifier for an independently authenticated, immutable/versioned
// provider namespace; parsing it does not establish that mapping. Versions are
// positive signed 64-bit integers, encoded as exact decimal integer tokens.
// Field order and encoding/json escaping are part of this schema's canonical
// encoding, not RFC 8785/JCS. Any encoding change requires a new schema version.
type Value struct {
	Schema              string   `json:"schema"`
	Tenant              string   `json:"tenant"`
	App                 string   `json:"app"`
	Destination         string   `json:"destination"`
	PolicyVersion       int64    `json:"policy_version"`
	KeyVersion          int64    `json:"key_version"`
	URL                 string   `json:"url"`
	CIDRs               []string `json:"cidrs"`
	SignatureProfile    string   `json:"signature_profile"`
	KeyRef              string   `json:"key_ref"`
	ProviderAuthorityID string   `json:"provider_authority_id"`
	ProviderRevision    Revision `json:"provider_revision"`
}

func (Value) String() string   { return "DestinationDescriptorValue(redacted)" }
func (Value) GoString() string { return "DestinationDescriptorValue(redacted)" }

// Descriptor owns validated canonical content. Its zero value is invalid.
// Callers can copy it safely; all mutable storage remains private. No method
// resolves a provider, performs I/O, or creates permission to register or send.
type Descriptor struct {
	value           Value
	canonical       []byte
	policyIdentity  string
	keyIdentity     string
	bindingIdentity string
}

func (Descriptor) String() string   { return "DestinationDescriptor(inert,redacted)" }
func (Descriptor) GoString() string { return "DestinationDescriptor(inert,redacted)" }

// Valid means only that Decode accepted the content. It is not approval.
func (d Descriptor) Valid() bool { return len(d.canonical) != 0 }

func (d Descriptor) Value() Value {
	v := d.value
	v.CIDRs = append([]string(nil), v.CIDRs...)
	return v
}

func (d Descriptor) CanonicalBytes() []byte { return append([]byte(nil), d.canonical...) }

// These identities are unkeyed, domain-separated content hashes, not signatures,
// secret-key hashes, grants, or evidence of durable registry state. Policy and
// key identities each include scope and only their own version/content. Binding
// binds the complete tuple and both projections, never budgets or other targets.
func (d Descriptor) PolicyIdentity() string  { return d.policyIdentity }
func (d Descriptor) KeyIdentity() string     { return d.keyIdentity }
func (d Descriptor) BindingIdentity() string { return d.bindingIdentity }

// Decode is the sole descriptor constructor. It accepts bounded strict JSON,
// rejects unknown/missing/duplicate keys and caller-supplied identities, reuses
// the existing pure destination policy, and computes identities from content.
// URL must already be canonical. CIDRs must be masked, public policy prefixes;
// equivalent textual IPv6 forms are normalized, the set is sorted by ASCII
// bytes, and duplicate canonical entries are rejected rather than discarded.
// raw must not be mutated concurrently with this call. No input storage is kept.
func Decode(raw []byte) (Descriptor, error) {
	parsed, err := protocol.Decode(raw, MaxDescriptorBytes)
	if err != nil {
		return Descriptor{}, ErrDescriptor
	}
	v, err := decodeValue(parsed)
	if err != nil || validateValue(&v) != nil {
		return Descriptor{}, ErrDescriptor
	}
	canonical := marshalDescriptor(v)
	policyID, keyID, bindingID := descriptorIdentities(v)
	return Descriptor{value: v, canonical: canonical, policyIdentity: policyID, keyIdentity: keyID, bindingIdentity: bindingID}, nil
}

func decodeValue(parsed any) (Value, error) {
	m, err := protocol.ExactObject(parsed, []string{"schema", "tenant", "app", "destination", "policy_version", "key_version", "url", "cidrs", "signature_profile", "key_ref", "provider_authority_id", "provider_revision"}, nil)
	if err != nil {
		return Value{}, ErrDescriptor
	}
	var v Value
	for key, dest := range map[string]*string{"schema": &v.Schema, "tenant": &v.Tenant, "app": &v.App, "destination": &v.Destination, "url": &v.URL, "signature_profile": &v.SignatureProfile, "key_ref": &v.KeyRef, "provider_authority_id": &v.ProviderAuthorityID} {
		text, ok := m[key].(string)
		if !ok {
			return Value{}, ErrDescriptor
		}
		*dest = text
	}
	v.PolicyVersion, err = descriptorInteger(m["policy_version"])
	if err != nil {
		return Value{}, ErrDescriptor
	}
	v.KeyVersion, err = descriptorInteger(m["key_version"])
	if err != nil {
		return Value{}, ErrDescriptor
	}
	cidrs, ok := m["cidrs"].([]any)
	if !ok || len(cidrs) < 1 || len(cidrs) > MaxDescriptorCIDRs {
		return Value{}, ErrDescriptor
	}
	v.CIDRs = make([]string, len(cidrs))
	for i, entry := range cidrs {
		v.CIDRs[i], ok = entry.(string)
		if !ok {
			return Value{}, ErrDescriptor
		}
	}
	r, err := protocol.ExactObject(m["provider_revision"], []string{"kind", "id"}, nil)
	if err != nil {
		return Value{}, ErrDescriptor
	}
	v.ProviderRevision.Kind, ok = r["kind"].(string)
	if !ok {
		return Value{}, ErrDescriptor
	}
	v.ProviderRevision.ID, ok = r["id"].(string)
	if !ok {
		return Value{}, ErrDescriptor
	}
	return v, nil
}

func descriptorInteger(raw any) (int64, error) {
	n, ok := raw.(json.Number)
	if !ok {
		return 0, ErrDescriptor
	}
	value, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || value < 1 || strconv.FormatInt(value, 10) != string(n) {
		return 0, ErrDescriptor
	}
	return value, nil
}

func descriptorIdentifier(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func descriptorKeyRef(s string) bool {
	if len(s) < 1 || len(s) > 256 {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func descriptorRevision(r Revision) bool {
	if r.Kind != "exact" || !descriptorIdentifier(r.ID) {
		return false
	}
	// Reject known mutable alias tokens for consistency with the existing plan.
	// This is only a syntactic guard, not proof that any other ID is immutable.
	for _, token := range strings.FieldsFunc(strings.ToLower(r.ID), func(c rune) bool { return c == '-' || c == '_' }) {
		switch token {
		case "latest", "current", "active", "default", "alias", "fallback", "awscurrent", "awsprevious", "awspending":
			return false
		}
	}
	return true
}

func validateValue(v *Value) error {
	if v.Schema != SchemaVersion || !descriptorIdentifier(v.Tenant) || !descriptorIdentifier(v.App) || !descriptorIdentifier(v.Destination) || v.PolicyVersion < 1 || v.KeyVersion < 1 || v.SignatureProfile != SignatureProfile || !descriptorKeyRef(v.KeyRef) || !descriptorIdentifier(v.ProviderAuthorityID) || !descriptorRevision(v.ProviderRevision) {
		return ErrDescriptor
	}
	// This compiles and discards pure policy data. It does not authorize this
	// descriptor, perform DNS, or retain an operational Policy/Target handle.
	if _, err := security.NewPolicy([]security.Rule{{URL: v.URL, CIDRs: v.CIDRs}}); err != nil {
		return ErrDescriptor
	}
	for i, raw := range v.CIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return ErrDescriptor
		}
		v.CIDRs[i] = prefix.String()
	}
	sort.Strings(v.CIDRs)
	for i := 1; i < len(v.CIDRs); i++ {
		if v.CIDRs[i] == v.CIDRs[i-1] {
			return ErrDescriptor
		}
	}
	return nil
}

// Explicit structs define each projection's fixed JSON field order. Keeping
// policy and key version/content separate prevents one dimension from changing
// the other identity. Durable uniqueness/immutability still needs DB constraints.
type policyProjection struct {
	Schema           string   `json:"schema"`
	Tenant           string   `json:"tenant"`
	App              string   `json:"app"`
	Destination      string   `json:"destination"`
	PolicyVersion    int64    `json:"policy_version"`
	URL              string   `json:"url"`
	CIDRs            []string `json:"cidrs"`
	SignatureProfile string   `json:"signature_profile"`
}

type keyProjection struct {
	Schema              string   `json:"schema"`
	Tenant              string   `json:"tenant"`
	App                 string   `json:"app"`
	Destination         string   `json:"destination"`
	KeyVersion          int64    `json:"key_version"`
	KeyRef              string   `json:"key_ref"`
	ProviderAuthorityID string   `json:"provider_authority_id"`
	ProviderRevision    Revision `json:"provider_revision"`
}

type bindingProjection struct {
	Schema         string `json:"schema"`
	Tenant         string `json:"tenant"`
	App            string `json:"app"`
	Destination    string `json:"destination"`
	PolicyVersion  int64  `json:"policy_version"`
	KeyVersion     int64  `json:"key_version"`
	PolicyIdentity string `json:"policy_identity"`
	KeyIdentity    string `json:"key_identity"`
}

func descriptorIdentities(v Value) (string, string, string) {
	policy := policyProjection{Schema: v.Schema, Tenant: v.Tenant, App: v.App, Destination: v.Destination, PolicyVersion: v.PolicyVersion, URL: v.URL, CIDRs: v.CIDRs, SignatureProfile: v.SignatureProfile}
	key := keyProjection{Schema: v.Schema, Tenant: v.Tenant, App: v.App, Destination: v.Destination, KeyVersion: v.KeyVersion, KeyRef: v.KeyRef, ProviderAuthorityID: v.ProviderAuthorityID, ProviderRevision: v.ProviderRevision}
	policyID := descriptorHash(policyDomain, marshalPolicy(policy))
	keyID := descriptorHash(keyDomain, marshalKey(key))
	binding := bindingProjection{Schema: v.Schema, Tenant: v.Tenant, App: v.App, Destination: v.Destination, PolicyVersion: v.PolicyVersion, KeyVersion: v.KeyVersion, PolicyIdentity: policyID, KeyIdentity: keyID}
	return policyID, keyID, descriptorHash(bindingDomain, marshalBinding(binding))
}

// Concrete structs contain no unsupported fields or custom JSON marshalers, so
// json.Marshal cannot fail. These helpers deliberately do not accept arbitrary
// caller-defined types. The default JSON HTML escaping is frozen into v1.
func marshalDescriptor(v Value) []byte          { b, _ := json.Marshal(v); return b }
func marshalPolicy(v policyProjection) []byte   { b, _ := json.Marshal(v); return b }
func marshalKey(v keyProjection) []byte         { b, _ := json.Marshal(v); return b }
func marshalBinding(v bindingProjection) []byte { b, _ := json.Marshal(v); return b }

func descriptorHash(domain string, canonical []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(domain + "\x00"))
	_, _ = h.Write(canonical)
	return domain + ":sha256:" + hex.EncodeToString(h.Sum(nil))
}
