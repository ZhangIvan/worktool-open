// Package durableauthority implements unwired registry-content and persistent
// record primitives. No value in this package authenticates or enables a worker.
package durableauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
)

var (
	ErrInvalid     = errors.New("CALLBACK_AUTHORITY_CONTENT_INVALID")
	ErrUnavailable = errors.New("CALLBACK_AUTHORITY_UNCONFIRMED")
	ErrConflict    = errors.New("CALLBACK_AUTHORITY_CONFLICT")
	ErrMissing     = errors.New("CALLBACK_AUTHORITY_HISTORY_MISSING")
)

// Identity contains declared, non-secret identifiers. Equality is not provenance.
// Scope must eventually come from authenticated deployment/partition allocation,
// never be invented by a requester to bypass occupied history.
type Identity struct {
	Scope, Allocation, Instance, Boot, Epoch, ProvenanceRef string
	Revision                                                int64
}

func (Identity) String() string   { return "CallbackAuthorityIdentity(unverified,redacted)" }
func (Identity) GoString() string { return "CallbackAuthorityIdentity(unverified,redacted)" }

type Binding struct {
	identity             Identity
	planIdentity, digest string
	descriptors          []byte
}

func (Binding) String() string   { return "CallbackAuthorityBinding(inert,redacted)" }
func (Binding) GoString() string { return "CallbackAuthorityBinding(inert,redacted)" }

func identifier(s string) bool {
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
func validIdentity(i Identity) bool {
	for _, s := range []string{i.Scope, i.Allocation, i.Instance, i.Boot, i.Epoch, i.ProvenanceRef} {
		if !identifier(s) {
			return false
		}
	}
	return i.Revision > 0
}

// Bind compares full plan/descriptor content. It never contacts a provider or
// authenticates identifiers, and returns no send or acquisition capability.
func Bind(p composition.Plan, ds []registration.Descriptor, i Identity) (Binding, error) {
	if !validIdentity(i) || p.Identity() == "" || len(ds) == 0 || len(ds) > composition.MaxDestinations {
		return Binding{}, ErrInvalid
	}
	destinations := p.Destinations()
	if len(ds) != len(destinations) {
		return Binding{}, ErrInvalid
	}
	byPair := make(map[string]registration.Descriptor, len(ds))
	for _, d := range ds {
		if !d.Valid() {
			return Binding{}, ErrInvalid
		}
		v := d.Value()
		pair := v.Tenant + "\x00" + v.Destination
		if _, ok := byPair[pair]; ok {
			return Binding{}, ErrInvalid
		}
		byPair[pair] = d
	}
	sorted := append([]composition.Destination(nil), destinations...)
	sort.Slice(sorted, func(a, b int) bool {
		if sorted[a].Tenant != sorted[b].Tenant {
			return sorted[a].Tenant < sorted[b].Tenant
		}
		return sorted[a].Destination < sorted[b].Destination
	})
	raw := make([]json.RawMessage, 0, len(ds))
	ids := make([]string, 0, len(ds))
	for _, d := range sorted {
		descriptor, ok := byPair[d.Tenant+"\x00"+d.Destination]
		if !ok {
			return Binding{}, ErrInvalid
		}
		v := descriptor.Value()
		if d.App != v.App || d.PolicyVersion != v.PolicyVersion || d.KeyVersion != v.KeyVersion || d.URL != v.URL || d.KeyRef != v.KeyRef || d.SignatureProfile != v.SignatureProfile || d.ProviderRevision.Kind != v.ProviderRevision.Kind || d.ProviderRevision.ID != v.ProviderRevision.ID || !reflect.DeepEqual(d.CIDRs, v.CIDRs) {
			return Binding{}, ErrInvalid
		}
		raw = append(raw, descriptor.CanonicalBytes())
		ids = append(ids, descriptor.BindingIdentity())
	}
	descriptorBytes, _ := json.Marshal(raw)
	if len(descriptorBytes) > 32*registration.MaxDescriptorBytes {
		return Binding{}, ErrInvalid
	}
	// v1 encoding is fixed tagged JSON, independent of public Identity evolution.
	content, _ := json.Marshal(bindingProjectionV1{i.Scope, i.Allocation, i.Instance, i.Boot, i.Epoch, i.ProvenanceRef, i.Revision, p.Identity(), ids})
	hash := sha256.Sum256(append([]byte("worktool-callback-authority-binding-v1\x00"), content...))
	return Binding{i, p.Identity(), hex.EncodeToString(hash[:]), descriptorBytes}, nil
}

// Field order and tags are part of the v1 persistence encoding contract.
// Changes require a new domain/version, not reinterpretation of stored hashes.
type bindingProjectionV1 struct {
	Scope                string   `json:"scope"`
	Allocation           string   `json:"allocation"`
	Instance             string   `json:"instance"`
	Boot                 string   `json:"boot"`
	Epoch                string   `json:"epoch"`
	ProvenanceRef        string   `json:"provenance_ref"`
	RegistryRevision     int64    `json:"registry_revision"`
	PlanIdentity         string   `json:"plan_identity"`
	DescriptorIdentities []string `json:"descriptor_identities"`
}

func (b Binding) valid() bool {
	return validIdentity(b.identity) && len(b.digest) == 64 && b.planIdentity != "" && len(b.descriptors) > 0 && len(b.descriptors) <= 32*registration.MaxDescriptorBytes
}

// validateRegistry reuses the existing parser/identity contract. planRaw is not
// required or claimed to be canonical JSON. Descriptor bytes ARE canonical.
func (b Binding) validateRegistry(i Identity, planID, digest string, planRaw, descriptorsRaw []byte) error {
	if !b.valid() || i != b.identity || planID != b.planIdentity || digest != b.digest || len(planRaw) == 0 || len(planRaw) > composition.MaxManifestBytes || len(descriptorsRaw) == 0 || len(descriptorsRaw) > 32*registration.MaxDescriptorBytes {
		return ErrInvalid
	}
	p, err := composition.Parse(planRaw, planID)
	if err != nil {
		return ErrInvalid
	}
	var values []json.RawMessage
	if json.Unmarshal(descriptorsRaw, &values) != nil || len(values) < 1 || len(values) > 32 {
		return ErrInvalid
	}
	ds := make([]registration.Descriptor, len(values))
	for n, raw := range values {
		ds[n], err = registration.Decode(raw)
		if err != nil {
			return ErrInvalid
		}
	}
	rebuilt, err := Bind(p, ds, i)
	if err != nil || rebuilt.digest != b.digest || !bytes.Equal(rebuilt.descriptors, descriptorsRaw) || !bytes.Equal(b.descriptors, descriptorsRaw) {
		return ErrInvalid
	}
	return nil
}
