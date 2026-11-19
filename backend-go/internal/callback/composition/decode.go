package composition

import (
	"encoding/json"
	"strconv"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
)

func object(v any, keys ...string) (map[string]any, error) {
	m, err := protocol.ExactObject(v, keys, nil)
	if err != nil {
		return nil, ErrManifest
	}
	return m, nil
}

func text(m map[string]any, key string) (string, error) {
	s, ok := m[key].(string)
	if !ok {
		return "", ErrManifest
	}
	return s, nil
}

// Manifest integers must be canonical nonnegative base-10 JSON integer tokens.
// No float/exponent, signed zero, string, null, silent default or overflow.
func integer(m map[string]any, key string) (int64, error) {
	n, ok := m[key].(json.Number)
	if !ok {
		return 0, ErrManifest
	}
	s := string(n)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 || strconv.FormatInt(v, 10) != s {
		return 0, ErrManifest
	}
	return v, nil
}

func array(v any, max int) ([]any, error) {
	a, ok := v.([]any)
	if !ok || len(a) < 1 || len(a) > max {
		return nil, ErrManifest
	}
	return a, nil
}

func decodeManifest(v any) (manifest, error) {
	m, err := object(v, "version", "destinations", "budgets")
	if err != nil {
		return manifest{}, ErrManifest
	}
	version, err := integer(m, "version")
	if err != nil || version != 1 {
		return manifest{}, ErrManifest
	}
	ds, err := array(m["destinations"], MaxDestinations)
	if err != nil {
		return manifest{}, ErrManifest
	}
	out := manifest{Version: version}
	for _, entry := range ds {
		d, err := decodeDestination(entry)
		if err != nil {
			return manifest{}, ErrManifest
		}
		out.Destinations = append(out.Destinations, d)
	}
	out.Budgets, err = decodeBudgets(m["budgets"])
	if err != nil {
		return manifest{}, ErrManifest
	}
	return out, nil
}

func decodeDestination(v any) (Destination, error) {
	m, err := object(v, "tenant", "app", "destination", "policy_version", "key_version", "url", "cidrs", "signature_profile", "key_ref", "provider_revision")
	if err != nil {
		return Destination{}, ErrManifest
	}
	var d Destination
	for key, target := range map[string]*string{"tenant": &d.Tenant, "app": &d.App, "destination": &d.Destination, "url": &d.URL, "signature_profile": &d.SignatureProfile, "key_ref": &d.KeyRef} {
		*target, err = text(m, key)
		if err != nil {
			return Destination{}, ErrManifest
		}
	}
	d.PolicyVersion, err = integer(m, "policy_version")
	if err != nil {
		return Destination{}, ErrManifest
	}
	d.KeyVersion, err = integer(m, "key_version")
	if err != nil {
		return Destination{}, ErrManifest
	}
	cidrs, err := array(m["cidrs"], 32)
	if err != nil {
		return Destination{}, ErrManifest
	}
	for _, value := range cidrs {
		cidr, ok := value.(string)
		if !ok {
			return Destination{}, ErrManifest
		}
		d.CIDRs = append(d.CIDRs, cidr)
	}
	r, err := object(m["provider_revision"], "kind", "id")
	if err != nil {
		return Destination{}, ErrManifest
	}
	d.ProviderRevision.Kind, err = text(r, "kind")
	if err != nil {
		return Destination{}, ErrManifest
	}
	d.ProviderRevision.ID, err = text(r, "id")
	return d, err
}

func numbers(v any, targets map[string]*int64) error {
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	m, err := object(v, keys...)
	if err != nil {
		return ErrManifest
	}
	for key, target := range targets {
		*target, err = integer(m, key)
		if err != nil {
			return ErrManifest
		}
	}
	return nil
}

func decodeBudgets(v any) (Budgets, error) {
	m, err := object(v, "runner", "worker", "pool")
	if err != nil {
		return Budgets{}, ErrManifest
	}
	var b Budgets
	r, w := &b.Runner, &b.Worker
	if numbers(m["runner"], map[string]*int64{"acquire_ms": &r.AcquireMS, "begin_ms": &r.BeginMS, "transaction_ms": &r.TransactionMS, "commit_ms": &r.CommitMS, "cleanup_ms": &r.CleanupMS}) != nil || numbers(m["worker"], map[string]*int64{"poll_ms": &w.PollMS, "recovery_ms": &w.RecoveryMS, "run_ms": &w.RunMS, "shutdown_ms": &w.ShutdownMS, "join_headroom_ms": &w.JoinHeadroomMS}) != nil {
		return Budgets{}, ErrManifest
	}
	p, err := object(m["pool"], "max_connections", "system_reserve", "operational_reserve", "roles")
	if err != nil {
		return Budgets{}, ErrManifest
	}
	for key, target := range map[string]*int64{"max_connections": &b.Pool.MaxConnections, "system_reserve": &b.Pool.SystemReserve, "operational_reserve": &b.Pool.OperationalReserve} {
		*target, err = integer(p, key)
		if err != nil {
			return Budgets{}, ErrManifest
		}
	}
	roles, err := array(p["roles"], 3)
	if err != nil {
		return Budgets{}, ErrManifest
	}
	for _, raw := range roles {
		role, err := object(raw, "role", "replicas", "max_conns")
		if err != nil {
			return Budgets{}, ErrManifest
		}
		var r RolePool
		r.Role, err = text(role, "role")
		if err != nil {
			return Budgets{}, ErrManifest
		}
		r.Replicas, err = integer(role, "replicas")
		if err != nil {
			return Budgets{}, ErrManifest
		}
		r.MaxConns, err = integer(role, "max_conns")
		if err != nil {
			return Budgets{}, ErrManifest
		}
		b.Pool.Roles = append(b.Pool.Roles, r)
	}
	return b, nil
}
