package composition

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
)

func fixture() manifest {
	return manifest{Version: 1, Destinations: []Destination{{
		Tenant: "tenant_synthetic", App: "app_synthetic", Destination: "dest_a",
		PolicyVersion: 1, KeyVersion: 1, URL: "https://hooks.example.com/callback",
		CIDRs: []string{"8.8.8.0/24", "2606:4700::/32"}, SignatureProfile: SignatureProfile,
		KeyRef: "synthetic-key-reference", ProviderRevision: Revision{Kind: "exact", ID: "revision_001"},
	}}, Budgets: Budgets{
		Runner: RunnerBudget{AcquireMS: 2000, BeginMS: 2000, TransactionMS: 5000, CommitMS: 2000, CleanupMS: 1000},
		Worker: WorkerBudget{PollMS: 1000, RecoveryMS: 1000, RunMS: 1000, ShutdownMS: 10000, JoinHeadroomMS: 1000},
		Pool:   PoolBudget{MaxConnections: 10, SystemReserve: 1, OperationalReserve: 1, Roles: []RolePool{{Role: "worker", Replicas: 1, MaxConns: 2}, {Role: "api", Replicas: 1, MaxConns: 2}}},
	}}
}

func rawFixture(t *testing.T, m manifest) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parsed(t *testing.T, m manifest) Plan {
	t.Helper()
	p, err := Parse(rawFixture(t, m), "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanAlwaysDisabledAndUnresolved(t *testing.T) {
	for _, p := range []Plan{{}, parsed(t, fixture())} {
		if p.Enabled() || len(p.Unresolved()) != 9 {
			t.Fatal("plan granted readiness")
		}
		u := p.Unresolved()
		u[0] = "removed"
		if p.Unresolved()[0] != "DURABLE_CONFIGURATION_BINDING" {
			t.Fatal("prerequisites alias")
		}
		if got := fmt.Sprintf("%v %+v %#v", p, p, p); strings.Contains(got, "example.com") || strings.Contains(got, "synthetic-key") {
			t.Fatal("plan formatting leaked configuration")
		}
		if b, err := json.Marshal(p); err != nil || string(b) != "{}" {
			t.Fatal("plan unexpectedly serializes private configuration")
		}
	}
	p := parsed(t, fixture())
	if !identitySyntax(p.Identity()) || p.JoinAllowanceMillis() != 9000 || p.DeclaredPoolUsed() != 6 {
		t.Fatal("wrong declaration summary")
	}
	if (Plan{}).Identity() != "" || len((Plan{}).Destinations()) != 0 || (Plan{}).JoinAllowanceMillis() != 0 || (Plan{}).DeclaredPoolUsed() != 0 {
		t.Fatal("zero plan is not empty")
	}
}

// Independent Python reference encoding pins schema 1, including Go JSON HTML
// escaping. Changing field order/encoding must not silently change this identity.
func TestPlanSchemaOneGoldenIdentity(t *testing.T) {
	m := fixture()
	m.Destinations[0].KeyRef = "opaque<&>\"\\ref"
	const want = "cbplan-v1:sha256:2f9bf27e23e466d3ae178dc0f364580357ffd6ed5132dce978121ab7c0dd59b8"
	p, err := Parse(rawFixture(t, m), want)
	if err != nil || p.Identity() != want || p.Enabled() {
		t.Fatal("schema 1 identity drift")
	}
}

func TestPlanCopyAndOrdering(t *testing.T) {
	m := fixture()
	second := m.Destinations[0]
	second.Destination = "dest_b"
	second.CIDRs = []string{"2606:4700::/32", "8.8.8.0/24"}
	m.Destinations = append(m.Destinations, second)
	raw := rawFixture(t, m)
	p, err := Parse(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	baseline := p.Identity()
	for i := range raw {
		raw[i] = 'x'
	}
	out := p.Destinations()
	out[0].CIDRs[0], out[0].URL, out[0].ProviderRevision.ID = "127.0.0.0/8", "https://evil.example.com/", "latest"
	budgets := p.Budgets()
	budgets.Pool.Roles[0].MaxConns = 999
	budgets.Worker.ShutdownMS = 1
	if p.Identity() != baseline || p.Destinations()[0].CIDRs[0] == "127.0.0.0/8" || p.Destinations()[0].URL != fixture().Destinations[0].URL || p.Budgets().Worker.ShutdownMS != 10000 || p.Budgets().Pool.Roles[0].MaxConns != 2 {
		t.Fatal("input or accessor mutation changed plan")
	}
	m.Destinations[0], m.Destinations[1] = m.Destinations[1], m.Destinations[0]
	m.Budgets.Pool.Roles[0], m.Budgets.Pool.Roles[1] = m.Budgets.Pool.Roles[1], m.Budgets.Pool.Roles[0]
	m.Destinations[1].CIDRs[1] = "2606:4700:0000:0000:0000:0000:0000:0000/32"
	if parsed(t, m).Identity() != baseline {
		t.Fatal("set ordering or canonical IPv6 text changed identity")
	}
	if got := fmt.Sprintf("%v %+v %#v", p.Destinations()[0], p.Destinations()[0], p.Destinations()[0]); strings.Contains(got, "example.com") {
		t.Fatal("destination formatting leaked")
	}
}

func TestPlanIdentityBindsEveryDeclaration(t *testing.T) {
	base := parsed(t, fixture()).Identity()
	changes := map[string]func(*manifest){
		"tenant":                         func(m *manifest) { m.Destinations[0].Tenant += "2" },
		"app":                            func(m *manifest) { m.Destinations[0].App += "2" },
		"destination":                    func(m *manifest) { m.Destinations[0].Destination += "2" },
		"policy_version":                 func(m *manifest) { m.Destinations[0].PolicyVersion++ },
		"key_version":                    func(m *manifest) { m.Destinations[0].KeyVersion++ },
		"same_version_url":               func(m *manifest) { m.Destinations[0].URL += "2" },
		"same_version_cidr":              func(m *manifest) { m.Destinations[0].CIDRs[0] = "8.8.4.0/24" },
		"same_version_key_ref":           func(m *manifest) { m.Destinations[0].KeyRef += "2" },
		"same_version_provider_revision": func(m *manifest) { m.Destinations[0].ProviderRevision.ID += "2" },
		"acquire":                        func(m *manifest) { m.Budgets.Runner.AcquireMS++ },
		"begin":                          func(m *manifest) { m.Budgets.Runner.BeginMS++ },
		"transaction":                    func(m *manifest) { m.Budgets.Runner.TransactionMS++ },
		"commit":                         func(m *manifest) { m.Budgets.Runner.CommitMS++ },
		"cleanup":                        func(m *manifest) { m.Budgets.Runner.CleanupMS++ },
		"poll":                           func(m *manifest) { m.Budgets.Worker.PollMS++ },
		"recovery":                       func(m *manifest) { m.Budgets.Worker.RecoveryMS++ },
		"run":                            func(m *manifest) { m.Budgets.Worker.RunMS++ },
		"shutdown":                       func(m *manifest) { m.Budgets.Worker.ShutdownMS++ },
		"headroom":                       func(m *manifest) { m.Budgets.Worker.JoinHeadroomMS++ },
		"pool_limit":                     func(m *manifest) { m.Budgets.Pool.MaxConnections++ },
		"system_reserve":                 func(m *manifest) { m.Budgets.Pool.SystemReserve++ },
		"operational_reserve":            func(m *manifest) { m.Budgets.Pool.OperationalReserve++ },
		"pool_replicas":                  func(m *manifest) { m.Budgets.Pool.Roles[0].Replicas++ },
		"pool_conns":                     func(m *manifest) { m.Budgets.Pool.Roles[0].MaxConns++ },
		"pool_role":                      func(m *manifest) { m.Budgets.Pool.Roles[1].Role = "gateway" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			m := fixture()
			change(&m)
			if p := parsed(t, m); p.Identity() == base {
				t.Fatal("changed declaration not bound")
			}
			if p, err := Parse(rawFixture(t, m), base); err != ErrIdentity || p.Identity() != "" || p.Enabled() {
				t.Fatal("expected identity mismatch did not fail closed")
			}
		})
	}
	// Unsupported values remain rejected. Private encoder probes prove schema,
	// signature and revision kind are hashed; these are NOT accepted profiles.
	for _, field := range []string{"schema", "signature", "revision_kind"} {
		t.Run(field, func(t *testing.T) {
			m := fixture()
			if err := validateDestinations(m.Destinations); err != nil {
				t.Fatal(err)
			}
			if _, _, err := validateBudgets(&m.Budgets); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "schema":
				m.Version = 2
			case "signature":
				m.Destinations[0].SignatureProfile = "hypothetical-v2"
			case "revision_kind":
				m.Destinations[0].ProviderRevision.Kind = "alias"
			}
			if contentIdentity(m) == base {
				t.Fatal("domain field omitted")
			}
			if _, err := Parse(rawFixture(t, m), ""); err != ErrManifest {
				t.Fatal("unsupported declaration accepted")
			}
		})
	}
	if p, err := Parse(rawFixture(t, fixture()), base); err != nil || p.Enabled() || !reflect.DeepEqual(p.Unresolved(), (Plan{}).Unresolved()) {
		t.Fatal("matching identity changes authority")
	}
	for _, id := range []string{"secret-invalid", base + "0", strings.ToUpper(base), strings.Repeat("a", 64), "cbplan-v1:sha256:" + strings.Repeat("z", 64)} {
		if _, err := Parse(rawFixture(t, fixture()), id); err != ErrIdentity || strings.Contains(err.Error(), id) {
			t.Fatal("identity syntax or redaction failure")
		}
	}
}

func TestPlanDestinationLimits(t *testing.T) {
	for _, count := range []int{0, 1, 32, 33} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m := fixture()
			m.Destinations = nil
			for i := 0; i < count; i++ {
				d := fixture().Destinations[0]
				d.Destination = fmt.Sprintf("dest_%02d", i)
				m.Destinations = append(m.Destinations, d)
			}
			_, err := Parse(rawFixture(t, m), "")
			if (err == nil) != (count >= 1 && count <= 32) {
				t.Fatal("entry bound")
			}
		})
	}
	cases := map[string]func(*manifest){
		"duplicate_pair": func(m *manifest) {
			d := m.Destinations[0]
			d.App += "2"
			d.PolicyVersion++
			m.Destinations = append(m.Destinations, d)
		},
		"shared_url_different_cidrs": func(m *manifest) {
			d := m.Destinations[0]
			d.Destination += "2"
			d.CIDRs = []string{"8.8.4.0/24"}
			m.Destinations = append(m.Destinations, d)
		},
		"empty_tenant":       func(m *manifest) { m.Destinations[0].Tenant = "" },
		"long_app":           func(m *manifest) { m.Destinations[0].App = strings.Repeat("a", 129) },
		"bad_destination":    func(m *manifest) { m.Destinations[0].Destination = "a/b" },
		"zero_policy":        func(m *manifest) { m.Destinations[0].PolicyVersion = 0 },
		"zero_key":           func(m *manifest) { m.Destinations[0].KeyVersion = 0 },
		"empty_key_ref":      func(m *manifest) { m.Destinations[0].KeyRef = "" },
		"long_key_ref":       func(m *manifest) { m.Destinations[0].KeyRef = strings.Repeat("k", 257) },
		"whitespace_key_ref": func(m *manifest) { m.Destinations[0].KeyRef = "secret\nvalue" },
		"nonascii_key_ref":   func(m *manifest) { m.Destinations[0].KeyRef = "密钥" },
		"empty_revision":     func(m *manifest) { m.Destinations[0].ProviderRevision.ID = "" },
		"long_revision":      func(m *manifest) { m.Destinations[0].ProviderRevision.ID = strings.Repeat("a", 129) },
		"revision_url":       func(m *manifest) { m.Destinations[0].ProviderRevision.ID = "https://provider.example.com/latest" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := fixture()
			mutate(&m)
			if p, err := Parse(rawFixture(t, m), ""); err != ErrManifest || p.Identity() != "" {
				t.Fatal("invalid destination accepted")
			}
		})
	}
	for _, url := range []string{"https://Hooks.Example.com/callback", "https://hooks.example.com:443/callback", "https://hooks.example.com", "http://hooks.example.com/", "https://user:secret@hooks.example.com/", "https://127.0.0.1/", "https://hooks.example.com/a?secret=x", "https://hooks.example.com/%2f", "https://foo.internal/"} {
		t.Run("url_"+fmt.Sprint(len(url)), func(t *testing.T) {
			m := fixture()
			m.Destinations[0].URL = url
			if _, err := Parse(rawFixture(t, m), ""); err != ErrManifest {
				t.Fatal("URL policy bypass")
			}
		})
	}
	for _, cidrs := range [][]string{nil, {}, {"127.0.0.0/8"}, {"8.8.8.1/24"}, {"0.0.0.0/0"}, {"8.8.8.0/24", "8.8.8.0/24"}, {"2606:4700::/32", "2606:4700:0000::/32"}, {"::ffff:8.8.8.0/120"}, {"2001:db8::/32"}} {
		m := fixture()
		m.Destinations[0].CIDRs = cidrs
		if _, err := Parse(rawFixture(t, m), ""); err != ErrManifest {
			t.Fatal("CIDR policy bypass")
		}
	}
	for _, count := range []int{32, 33} {
		m := fixture()
		m.Destinations[0].CIDRs = nil
		for i := 0; i < count; i++ {
			m.Destinations[0].CIDRs = append(m.Destinations[0].CIDRs, fmt.Sprintf("8.8.%d.0/24", i))
		}
		_, err := Parse(rawFixture(t, m), "")
		if (err == nil) != (count == 32) {
			t.Fatal("CIDR bound")
		}
	}
	for _, alias := range []string{"latest", "LATEST", "current", "active", "default", "alias", "fallback", "AWSCURRENT", "AWSPREVIOUS", "AWSPENDING", "version_Latest", "current-001", "version_AWSCURRENT_001"} {
		t.Run("alias_"+alias, func(t *testing.T) {
			m := fixture()
			m.Destinations[0].ProviderRevision.ID = alias
			if _, err := Parse(rawFixture(t, m), ""); err != ErrManifest {
				t.Fatal("alias accepted")
			}
		})
	}
	m := fixture()
	m.Destinations[0].ProviderRevision.ID = "opaque_latestness_0123"
	parsed(t, m) // not substring blacklisting
}

func TestPlanBudgets(t *testing.T) {
	runnerFields := []string{"acquire", "begin", "transaction", "commit", "cleanup"}
	for _, field := range runnerFields {
		for _, value := range []int64{0, 1, 30000, 30001, 9223372036854775807} {
			t.Run(fmt.Sprintf("runner_%s_%d", field, value), func(t *testing.T) {
				m := fixture()
				r := &m.Budgets.Runner
				r.BeginMS, r.CommitMS, r.TransactionMS = 1, 1, 30000
				switch field {
				case "acquire":
					r.AcquireMS = value
				case "begin":
					r.BeginMS = value
				case "transaction":
					r.TransactionMS = value
				case "commit":
					r.CommitMS = value
				case "cleanup":
					r.CleanupMS = value
				}
				_, err := Parse(rawFixture(t, m), "")
				want := value >= 1 && value <= 30000 && (field != "cleanup" || value == 1)
				if (err == nil) != want {
					t.Fatal("runner boundary mismatch")
				}
			})
		}
	}
	for _, field := range []string{"begin", "commit"} {
		m := fixture()
		if field == "begin" {
			m.Budgets.Runner.BeginMS = 5001
		} else {
			m.Budgets.Runner.CommitMS = 5001
		}
		if _, err := Parse(rawFixture(t, m), ""); err != ErrManifest {
			t.Fatal("transaction hierarchy")
		}
	}
	for _, shutdown := range []int64{7999, 8000, 8999, 9000, 60000, 60001} {
		m := fixture()
		m.Budgets.Worker.ShutdownMS = shutdown
		_, err := Parse(rawFixture(t, m), "")
		if (err == nil) != (shutdown >= 9000 && shutdown <= 60000) {
			t.Fatal("join allowance boundary")
		}
	}
	for _, headroom := range []int64{0, 1, 52000, 52001, 60001, 9223372036854775807} {
		m := fixture()
		m.Budgets.Worker.ShutdownMS = 60000
		m.Budgets.Worker.JoinHeadroomMS = headroom
		_, err := Parse(rawFixture(t, m), "")
		if (err == nil) != (headroom >= 1 && headroom <= 52000) {
			t.Fatal("headroom boundary")
		}
	}
	cases := map[string]func(*PoolBudget){
		"zero_limit":            func(p *PoolBudget) { p.MaxConnections = 0 },
		"reserve_exceeds":       func(p *PoolBudget) { p.SystemReserve = 11 },
		"operational_exceeds":   func(p *PoolBudget) { p.OperationalReserve = 10 },
		"empty_roles":           func(p *PoolBudget) { p.Roles = nil },
		"no_worker":             func(p *PoolBudget) { p.Roles = p.Roles[1:] },
		"duplicate_role":        func(p *PoolBudget) { p.Roles[1].Role = "worker" },
		"unknown_role_redacted": func(p *PoolBudget) { p.Roles[1].Role = "DO_NOT_ECHO_SECRET" },
		"zero_replicas":         func(p *PoolBudget) { p.Roles[0].Replicas = 0 },
		"zero_conns":            func(p *PoolBudget) { p.Roles[0].MaxConns = 0 },
		"max_conns":             func(p *PoolBudget) { p.Roles[0].MaxConns = 2147483648 },
		"multiply_overflow":     func(p *PoolBudget) { p.MaxConnections = 9223372036854775807; p.Roles[0].Replicas = 9223372036854775807 },
		"over_allocation":       func(p *PoolBudget) { p.Roles[0].MaxConns = 7 },
	}
	for name, mutate := range cases {
		t.Run("pool_"+name, func(t *testing.T) {
			m := fixture()
			mutate(&m.Budgets.Pool)
			if _, err := Parse(rawFixture(t, m), ""); err != ErrManifest || err.Error() != "CALLBACK_PLAN_INVALID" {
				t.Fatal("pool invalid or leak")
			}
		})
	}
	m := fixture()
	m.Budgets.Pool.Roles[0].MaxConns = 6
	if parsed(t, m).DeclaredPoolUsed() != 10 {
		t.Fatal("exact pool boundary")
	}
}

// These panic-on-call adapters are only used for inert constructor parity.
// Neither Parse nor callback.New/NewWorker may call them. Start is never called.
type noEffects struct{}

func (noEffects) Claim(context.Context) (cb.Delivery, bool, error)        { panic("side effect") }
func (noEffects) Prepare(context.Context, cb.Prepared) (cb.Permit, error) { panic("side effect") }
func (noEffects) Complete(context.Context, cb.Completion) error           { panic("side effect") }
func (noEffects) RecoverOneExpired(context.Context) (bool, error)         { panic("side effect") }
func (noEffects) Now() time.Time                                          { panic("side effect") }
func (noEffects) Deadline(context.Context, time.Time) (context.Context, context.CancelFunc) {
	panic("side effect")
}
func (noEffects) Check(context.Context, cb.Handle) error                { panic("side effect") }
func (noEffects) Resolve(context.Context, string) ([]netip.Addr, error) { panic("side effect") }
func (noEffects) Send(context.Context, cb.Request) (cb.Response, error) { panic("side effect") }

type noKeys struct{}

func (noKeys) Resolve(context.Context, cb.Binding) (cb.Key, error) { panic("side effect") }

func TestPlanWorkerBoundsMatchExistingInertConstructors(t *testing.T) {
	d := fixture().Destinations[0]
	n := noEffects{}
	sender, err := cb.New(cb.Dependencies{Store: n, Clock: n, Gate: n, DNS: n, Keys: noKeys{}, Transport: n}, []cb.Policy{{Binding: cb.Binding{Tenant: d.Tenant, Destination: d.Destination, URL: d.URL, KeyRef: d.KeyRef, PolicyVersion: 1, KeyVersion: 1}, CIDRs: d.CIDRs}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"poll", "recovery", "run", "shutdown"} {
		for _, value := range []int64{0, 9, 10, 99, 100, 10000, 10001, 60000, 60001} {
			t.Run(fmt.Sprintf("%s_%d", field, value), func(t *testing.T) {
				m := fixture()
				w := &m.Budgets.Worker
				switch field {
				case "poll":
					w.PollMS = value
				case "recovery":
					w.RecoveryMS = value
				case "run":
					w.RunMS = value
				case "shutdown":
					w.ShutdownMS = value
				}
				_, planErr := Parse(rawFixture(t, m), "")
				worker, workerErr := cb.NewWorker(sender, cb.WorkerConfig{Enabled: true, PollInterval: time.Duration(w.PollMS) * time.Millisecond, RecoveryTimeout: time.Duration(w.RecoveryMS) * time.Millisecond, RunTimeout: time.Duration(w.RunMS) * time.Millisecond, ShutdownTimeout: time.Duration(w.ShutdownMS) * time.Millisecond})
				want := workerErr == nil && w.ShutdownMS >= 9000
				if (planErr == nil) != want {
					t.Fatal("worker boundary drift")
				}
				if workerErr == nil && worker.Health().Active {
					t.Fatal("constructor started worker")
				}
			})
		}
	}
	// Reused pg validation is pure too; no pool constructor, acquire, or SQL.
	if err := (pg.Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: time.Second, Commit: time.Second, Cleanup: time.Second}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanConcurrentReadersAndParsers(t *testing.T) {
	raw := rawFixture(t, fixture())
	p := parsed(t, fixture())
	want := p.Identity()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				q, err := Parse(raw, want)
				if err != nil || q.Identity() != want || p.Enabled() {
					t.Error("concurrent parse")
				}
				x := p.Destinations()
				x[0].CIDRs[0] = "changed"
				b := p.Budgets()
				b.Pool.Roles[0].Role = "changed"
				u := p.Unresolved()
				u[0] = "changed"
			}
		}()
	}
	wg.Wait()
}
