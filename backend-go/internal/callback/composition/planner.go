// Package composition validates an inert callback configuration plan. Parsing
// never grants authority, creates a worker, resolves dependencies or starts work.
package composition

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
)

const (
	MaxManifestBytes = 262144
	MaxDestinations  = 32
	SignatureProfile = "worktool-callback-hmac-sha256-v1"
)

var (
	ErrManifest = errors.New("CALLBACK_PLAN_INVALID")
	ErrIdentity = errors.New("CALLBACK_PLAN_IDENTITY_MISMATCH")
)

// Revision is only a declared exact provider version, not proof that a provider
// implements immutable version lookup. No provider is contacted by this package.
type Revision struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Destination struct {
	Tenant           string   `json:"tenant"`
	App              string   `json:"app"`
	Destination      string   `json:"destination"`
	PolicyVersion    int64    `json:"policy_version"`
	KeyVersion       int64    `json:"key_version"`
	URL              string   `json:"url"`
	CIDRs            []string `json:"cidrs"`
	SignatureProfile string   `json:"signature_profile"`
	KeyRef           string   `json:"key_ref"`
	ProviderRevision Revision `json:"provider_revision"`
}

func (Destination) String() string   { return "CallbackPlanDestination(redacted)" }
func (Destination) GoString() string { return "CallbackPlanDestination(redacted)" }

// Millisecond integers have no implicit defaults. These are declarations only;
// they never configure an existing Runner, Worker, database, or deployment.
type RunnerBudget struct {
	AcquireMS     int64 `json:"acquire_ms"`
	BeginMS       int64 `json:"begin_ms"`
	TransactionMS int64 `json:"transaction_ms"`
	CommitMS      int64 `json:"commit_ms"`
	CleanupMS     int64 `json:"cleanup_ms"`
}

type WorkerBudget struct {
	PollMS         int64 `json:"poll_ms"`
	RecoveryMS     int64 `json:"recovery_ms"`
	RunMS          int64 `json:"run_ms"`
	ShutdownMS     int64 `json:"shutdown_ms"`
	JoinHeadroomMS int64 `json:"join_headroom_ms"`
}

type RolePool struct {
	Role     string `json:"role"`
	Replicas int64  `json:"replicas"`
	MaxConns int64  `json:"max_conns"`
}

type PoolBudget struct {
	MaxConnections     int64      `json:"max_connections"`
	SystemReserve      int64      `json:"system_reserve"`
	OperationalReserve int64      `json:"operational_reserve"`
	Roles              []RolePool `json:"roles"`
}

type Budgets struct {
	Runner RunnerBudget `json:"runner"`
	Worker WorkerBudget `json:"worker"`
	Pool   PoolBudget   `json:"pool"`
}

type manifest struct {
	Version      int64         `json:"version"`
	Destinations []Destination `json:"destinations"`
	Budgets      Budgets       `json:"budgets"`
}

// Plan owns its copied data and has no activation or dependency interface. A
// zero value is also disabled, with no identity and no validated declarations.
type Plan struct {
	identity string
	data     manifest
	joinMS   int64
	poolUsed int64
}

func (Plan) String() string   { return "CallbackPlan(disabled,redacted)" }
func (Plan) GoString() string { return "CallbackPlan(disabled,redacted)" }
func (Plan) Enabled() bool    { return false }

// Identity is a versioned local content digest, not an approval, secret digest,
// persistent registry binding, authorization assertion, or send permission.
func (p Plan) Identity() string { return p.identity }

func (p Plan) Destinations() []Destination {
	out := append([]Destination(nil), p.data.Destinations...)
	for i := range out {
		out[i].CIDRs = append([]string(nil), out[i].CIDRs...)
	}
	return out
}

func (p Plan) Budgets() Budgets {
	b := p.data.Budgets
	b.Pool.Roles = append([]RolePool(nil), b.Pool.Roles...)
	return b
}

// These are declared planning quantities, not actual resource usage or a hard
// join deadline. Noncooperative dependencies can outlive any declared budget.
func (p Plan) JoinAllowanceMillis() int64 { return p.joinMS }
func (p Plan) DeclaredPoolUsed() int64    { return p.poolUsed }

// Unresolved cannot be cleared by parsing or an expected-identity match. Return
// a fresh bounded list even for a zero/invalid plan. There is no READY state.
func (Plan) Unresolved() []string {
	return []string{
		"DURABLE_CONFIGURATION_BINDING", "EXACT_VERSION_SECRET_PROVIDER",
		"STICKY_GENERATION_AUTHORITY", "UTC_HEALTH_AUTHORITY",
		"APPROVED_DNS_AND_EGRESS", "WORKER_DATABASE_ROLE",
		"DURABLE_HALT_RECONCILIATION", "JOINT_LIFECYCLE_AND_RECOVERY_OWNERSHIP",
		"DEPLOYMENT_AND_RECEIVER_EVIDENCE",
	}
}

// Parse is the only plan constructor. It takes bytes, not paths, environment,
// adapters or credentials. The caller must not mutate raw concurrently. The
// optional expectedIdentity is only a consistency check, never approval.
func Parse(raw []byte, expectedIdentity string) (Plan, error) {
	if expectedIdentity != "" && !identitySyntax(expectedIdentity) {
		return Plan{}, ErrIdentity
	}
	v, err := protocol.Decode(raw, MaxManifestBytes)
	if err != nil {
		return Plan{}, ErrManifest
	}
	m, err := decodeManifest(v)
	if err != nil {
		return Plan{}, ErrManifest
	}
	if err = validateDestinations(m.Destinations); err != nil {
		return Plan{}, ErrManifest
	}
	join, used, err := validateBudgets(&m.Budgets)
	if err != nil {
		return Plan{}, ErrManifest
	}
	identity := contentIdentity(m)
	if expectedIdentity != "" && expectedIdentity != identity {
		return Plan{}, ErrIdentity
	}
	return Plan{identity: identity, data: m, joinMS: join, poolUsed: used}, nil
}

func identitySyntax(s string) bool {
	if len(s) != len("cbplan-v1:sha256:")+64 || !strings.HasPrefix(s, "cbplan-v1:sha256:") {
		return false
	}
	for _, c := range s[len("cbplan-v1:sha256:"):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func contentIdentity(m manifest) string {
	// Fixed structs, sorted entries/sets and bounded integer values define this
	// version's encoding. This is not an implementation of RFC 8785/JCS.
	b, _ := json.Marshal(m) // These concrete types have no failing/custom marshaler.
	h := sha256.Sum256(append([]byte("worktool-callback-plan-v1\x00"), b...))
	return "cbplan-v1:sha256:" + hex.EncodeToString(h[:])
}

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

func keyRef(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func exactRevision(r Revision) bool {
	if r.Kind != "exact" || !identifier(r.ID) {
		return false
	}
	// Reject recognized mutable alias words, including tokenized forms, without
	// pretending arbitrary opaque IDs can prove provider-side immutability.
	for _, token := range strings.FieldsFunc(strings.ToLower(r.ID), func(c rune) bool { return c == '-' || c == '_' }) {
		switch token {
		case "latest", "current", "active", "default", "alias", "fallback", "awscurrent", "awsprevious", "awspending":
			return false
		}
	}
	return true
}

func validateDestinations(ds []Destination) error {
	if len(ds) == 0 || len(ds) > MaxDestinations {
		return ErrManifest
	}
	seen := map[string]bool{}
	urls := map[string]string{}
	for i := range ds {
		d := &ds[i]
		if !identifier(d.Tenant) || !identifier(d.App) || !identifier(d.Destination) || d.PolicyVersion < 1 || d.KeyVersion < 1 || !keyRef(d.KeyRef) || d.SignatureProfile != SignatureProfile || !exactRevision(d.ProviderRevision) {
			return ErrManifest
		}
		// Match CallbackStore's pair scope, not Sender's looser full-binding key.
		pair := d.Tenant + "\x00" + d.Destination
		if seen[pair] {
			return ErrManifest
		}
		seen[pair] = true
		// Existing pure policy remains the URL/public-network authority. It also
		// demands an already canonical URL; do not silently rewrite destination.
		if _, err := security.NewPolicy([]security.Rule{{URL: d.URL, CIDRs: d.CIDRs}}); err != nil {
			return ErrManifest
		}
		cidrs := make([]string, len(d.CIDRs))
		for n, raw := range d.CIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return ErrManifest
			}
			cidrs[n] = prefix.String()
		}
		sort.Strings(cidrs)
		for n := 1; n < len(cidrs); n++ {
			if cidrs[n] == cidrs[n-1] {
				return ErrManifest
			}
		}
		d.CIDRs = cidrs
		networkSet := strings.Join(cidrs, "\x00")
		if previous, ok := urls[d.URL]; ok && previous != networkSet {
			return ErrManifest
		}
		urls[d.URL] = networkSet
	}
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Tenant != ds[j].Tenant {
			return ds[i].Tenant < ds[j].Tenant
		}
		return ds[i].Destination < ds[j].Destination
	})
	return nil
}

func validateBudgets(b *Budgets) (int64, int64, error) {
	r := b.Runner
	// Check before duration multiplication; malicious integers must not wrap.
	for _, ms := range []int64{r.AcquireMS, r.BeginMS, r.TransactionMS, r.CommitMS, r.CleanupMS} {
		if ms < 1 || ms > 30000 {
			return 0, 0, ErrManifest
		}
	}
	ms := time.Millisecond
	if err := (pg.Timeouts{Acquire: time.Duration(r.AcquireMS) * ms, Begin: time.Duration(r.BeginMS) * ms, Transaction: time.Duration(r.TransactionMS) * ms, Commit: time.Duration(r.CommitMS) * ms, Cleanup: time.Duration(r.CleanupMS) * ms}).Validate(); err != nil {
		return 0, 0, ErrManifest
	}
	w := b.Worker
	// These mirror NewWorker's enabled bounds without constructing a worker or
	// bypassing checks through its disabled path. Parity is regression-tested.
	if w.PollMS < 100 || w.PollMS > 60000 || w.RecoveryMS < 10 || w.RecoveryMS > 10000 || w.RunMS < 10 || w.RunMS > 10000 || w.ShutdownMS < 10 || w.ShutdownMS > 60000 || w.JoinHeadroomMS < 1 || w.JoinHeadroomMS > 60000 {
		return 0, 0, ErrManifest
	}
	join := int64(2000) + 6*r.CleanupMS + w.JoinHeadroomMS
	if w.ShutdownMS < join {
		return 0, 0, ErrManifest
	}
	p := b.Pool
	if len(p.Roles) < 1 || len(p.Roles) > 3 {
		return 0, 0, ErrManifest
	}
	pool := pg.PoolBudget{MaxConnections: p.MaxConnections, SystemReserve: p.SystemReserve, OperationalReserve: p.OperationalReserve}
	worker := false
	for _, role := range p.Roles {
		worker = worker || role.Role == "worker"
		pool.Roles = append(pool.Roles, pg.RolePool{Role: role.Role, Replicas: role.Replicas, MaxConns: role.MaxConns})
	}
	used, err := pool.Validate()
	if err != nil || !worker {
		return 0, 0, ErrManifest
	}
	sort.Slice(b.Pool.Roles, func(i, j int) bool { return b.Pool.Roles[i].Role < b.Pool.Roles[j].Role })
	return join, used, nil
}
