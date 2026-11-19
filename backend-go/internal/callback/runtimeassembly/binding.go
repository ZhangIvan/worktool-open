// Package runtimeassembly provides closed-by-construction callback ownership.
// It has no production launch-capability issuer. Content equality is not trust.
package runtimeassembly

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
)

type RuntimeBinding struct{ Instance, Boot, Epoch string }

func (RuntimeBinding) String() string   { return "CallbackRuntimeBinding(redacted)" }
func (RuntimeBinding) GoString() string { return "CallbackRuntimeBinding(redacted)" }

type bindingSnapshot struct {
	identity string
	runtime  RuntimeBinding
	policies []cb.Policy
	exact    map[cb.Binding]registration.Value
}

func compileBindings(plan composition.Plan, descriptors []registration.Descriptor, runtime RuntimeBinding) (*bindingSnapshot, error) {
	if plan.Identity() == "" {
		return nil, ErrConfiguration
	}
	if _, err := pg.RuntimeAuthorityFromTrusted(runtime.Instance, runtime.Boot, runtime.Epoch); err != nil {
		return nil, ErrConfiguration
	}
	ds := plan.Destinations()
	if len(ds) != len(descriptors) {
		return nil, ErrConfiguration
	}
	byPair := make(map[string]registration.Descriptor)
	for _, d := range descriptors {
		if !d.Valid() {
			return nil, ErrConfiguration
		}
		v := d.Value()
		pair := v.Tenant + "\x00" + v.Destination
		if _, ok := byPair[pair]; ok {
			return nil, ErrConfiguration
		}
		byPair[pair] = d
	}
	s := &bindingSnapshot{runtime: runtime, exact: make(map[cb.Binding]registration.Value)}
	ids := []string{plan.Identity(), runtime.Instance, runtime.Boot, runtime.Epoch}
	for _, d := range ds {
		desc, ok := byPair[d.Tenant+"\x00"+d.Destination]
		if !ok {
			return nil, ErrConfiguration
		}
		v := desc.Value()
		if d.App != v.App || d.PolicyVersion != v.PolicyVersion || d.KeyVersion != v.KeyVersion || d.URL != v.URL || d.KeyRef != v.KeyRef || d.SignatureProfile != v.SignatureProfile || d.ProviderRevision.Kind != v.ProviderRevision.Kind || d.ProviderRevision.ID != v.ProviderRevision.ID || !reflect.DeepEqual(d.CIDRs, v.CIDRs) {
			return nil, ErrConfiguration
		}
		b := cb.Binding{Tenant: v.Tenant, Destination: v.Destination, URL: v.URL, KeyRef: v.KeyRef, PolicyVersion: v.PolicyVersion, KeyVersion: v.KeyVersion}
		s.policies = append(s.policies, cb.Policy{Binding: b, CIDRs: append([]string(nil), v.CIDRs...)})
		s.exact[b] = v
		ids = append(ids, desc.BindingIdentity())
	}
	sort.Strings(ids[4:])
	raw, _ := json.Marshal(ids)
	h := sha256.Sum256(raw)
	s.identity = hex.EncodeToString(h[:])
	return s, nil
}
