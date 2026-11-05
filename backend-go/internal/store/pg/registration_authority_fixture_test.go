package pg

// Test-only synthetic importer and deliberately opt-in PostgreSQL fixture.
// Nothing in this file is compiled into an application binary. An envelope is
// a bounded test configuration, NOT human approval or deployment authority.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const rafOwner = "wt_registration_lab_owner"
const rafIssuer = "wt_registration_lab_issuer"
const rafConsumer = "wt_registration_lab_consumer"
const rafUnrelated = "wt_registration_lab_unrelated"
const rafRoutineOwner = "wt_registration_lab_routine"
const rafConsumeSQL = `SELECT wt_synthetic_registry.consume_prepared_fresh_registration_v1($1::text,$2::text)`
const rafImage = "postgres@sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c"
const rafEnvelopeEnv = "WORKTOOL_REGISTRATION_PG_ENVELOPE"

var rafInvalid = errors.New("SYNTHETIC_REGISTRATION_FIXTURE_INVALID")
var rafNotConfigured = errors.New("NOT_RUN: explicit isolated registration envelope absent")
var rafUnknown = errors.New("SYNTHETIC_REGISTRATION_UNKNOWN_STOP_NO_RETRY")
var rafDBPattern = regexp.MustCompile(`^wt_test_[a-z0-9_]{8,48}$`)
var rafRunPattern = regexp.MustCompile(`^synthetic_[a-z0-9_]{8,40}$`)

// Every field is explicit. No fallback DSN, role selection, source adoption or
// permission repair is available. External provisioning/resource evidence still
// requires independent review; a string in this envelope does not prove it.
type rafRunEnvelope struct {
	Format                     string `json:"format"`
	Allow                      string `json:"allow"`
	RunID                      string `json:"run_id"`
	Host                       string `json:"host"`
	Port                       uint16 `json:"port"`
	Database                   string `json:"database"`
	DatabaseOID                uint32 `json:"database_oid"`
	SchemaOID                  uint32 `json:"schema_oid"`
	ConsumerOID                uint32 `json:"consumer_oid"`
	IssuerOID                  uint32 `json:"issuer_oid"`
	Environment                string `json:"environment"`
	RecoveryGeneration         string `json:"recovery_generation"`
	Image                      string `json:"image"`
	ServerVersion              int    `json:"server_version"`
	BoundarySHA256             string `json:"boundary_sha256"`
	RegistrySHA256             string `json:"registry_sha256"`
	RoutineDefinitionSHA256    string `json:"routine_definition_sha256"`
	SourceManifestSHA256       string `json:"source_manifest_sha256"`
	ExecutableSHA256           string `json:"executable_sha256"`
	ProvisioningEvidenceSHA256 string `json:"provisioning_evidence_sha256"`
	OwnerDSN                   string `json:"owner_dsn"`
	IssuerDSN                  string `json:"issuer_dsn"`
	ConsumerDSN                string `json:"consumer_dsn"`
	UnrelatedDSN               string `json:"unrelated_dsn"`
}

func rafSHA(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func rafDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s) && s != strings.Repeat("0", 64)
}

// Decode once with unknown/duplicate/missing field rejection. Canonical JSON
// comparison also rejects trailing data, whitespace, alternate numbers and keys.
// Credentials are never printed in a returned error.
func rafParseEnvelope(raw string) (rafRunEnvelope, error) {
	var e rafRunEnvelope
	if raw == "" {
		return e, rafNotConfigured
	}
	if len(raw) > 16384 {
		return e, rafInvalid
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil {
		return e, rafInvalid
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return e, rafInvalid
	}
	canonical, err := json.Marshal(e)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return e, rafInvalid
	}
	if e.Format != "worktool-registration-isolated-fixture-v1" || e.Allow != "one-reviewed-synthetic-pending-only-run-no-retry" ||
		!rafRunPattern.MatchString(e.RunID) || e.Host != "127.0.0.1" || e.Port < 1024 || !rafDBPattern.MatchString(e.Database) ||
		e.DatabaseOID == 0 || e.SchemaOID == 0 || e.ConsumerOID == 0 || e.IssuerOID == 0 || e.ConsumerOID == e.IssuerOID ||
		!rafRunPattern.MatchString(e.Environment) || !rafRunPattern.MatchString(e.RecoveryGeneration) ||
		e.Image != rafImage || e.ServerVersion != 180006 || !rafDigest(e.BoundarySHA256) || !rafDigest(e.RegistrySHA256) ||
		!rafDigest(e.RoutineDefinitionSHA256) || !rafDigest(e.SourceManifestSHA256) || !rafDigest(e.ExecutableSHA256) || !rafDigest(e.ProvisioningEvidenceSHA256) {
		return e, rafInvalid
	}
	for _, pair := range []struct{ dsn, role string }{{e.OwnerDSN, rafOwner}, {e.IssuerDSN, rafIssuer}, {e.ConsumerDSN, rafConsumer}, {e.UnrelatedDSN, rafUnrelated}} {
		if rafValidateDSN(pair.dsn, pair.role, e) != nil {
			return e, rafInvalid
		}
	}
	return e, nil
}

func rafValidateDSN(dsn, role string, e rafRunEnvelope) error {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgresql" || u.User == nil || u.User.Username() != role || u.Hostname() != e.Host ||
		u.Port() != strconv.Itoa(int(e.Port)) || u.Path != "/"+e.Database || u.RawPath != "" || u.Fragment != "" || u.RawQuery != "sslmode=disable" || u.Opaque != "" {
		return rafInvalid
	}
	password, ok := u.User.Password()
	if !ok || password == "" || strings.ContainsAny(password, "\x00\r\n") {
		return rafInvalid
	}
	return nil
}

// This gate runs before file reads, ParseConfig or connect. Injection permits
// offline proof that a rejected or absent envelope cannot touch a resource.
func rafPreconnect(raw string, environment []string, read func(string) ([]byte, error), sourceDigest func() (string, error)) (rafRunEnvelope, error) {
	e, err := rafParseEnvelope(raw)
	if err != nil {
		return e, err
	}
	for _, kv := range environment {
		p := strings.SplitN(kv, "=", 2)
		if len(p) == 2 && p[1] != "" && strings.HasPrefix(p[0], "PG") {
			return e, rafInvalid
		}
	}
	if read == nil {
		return e, rafInvalid
	}
	for _, v := range []struct{ path, hash string }{{"testdata/registration_authority_boundary.sql", e.BoundarySHA256}, {"testdata/fresh_registration_registry_candidate.sql", e.RegistrySHA256}} {
		body, err := read(v.path)
		if err != nil || rafSHA(body) != v.hash {
			return e, rafInvalid
		}
	}
	if sourceDigest == nil {
		return e, rafInvalid
	}
	manifest, err := sourceDigest()
	if err != nil || manifest != e.SourceManifestSHA256 {
		return e, rafInvalid
	}
	return e, nil
}

// The envelope environment variable contains ONLY an absolute private filename.
// Refuse symlinks, broad permissions, another owner, oversized files, and a
// nonprivate parent. Never print this path or the contents on failure.
func rafReadPrivate(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, rafInvalid
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 {
		return nil, rafInvalid
	}
	resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || resolved != filepath.Dir(path) {
		return nil, rafInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 32768 {
		return nil, rafInvalid
	}
	for _, st := range []fs.FileInfo{parent, info} {
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != uint32(os.Getuid()) {
			return nil, rafInvalid
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, rafInvalid
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, rafInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, 32769))
	if err != nil || len(body) > 32768 {
		return nil, rafInvalid
	}
	return body, nil
}

// rafSourceDigestAt hashes bytes only after verifying the complete inventory.
// expectedInventory is a path-list hash, not a content hash: no circularity.
func rafSourceDigestAt(root string, count int, expectedInventory string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Base(root) != "source" {
		return "", rafInvalid
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return "", rafInvalid
	}
	var paths []string
	nodes := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		nodes++
		if nodes > 1024 || len(path)-len(root) > 512 {
			return rafInvalid
		}
		if err != nil {
			return rafInvalid
		}
		if d.Type()&os.ModeSymlink != 0 {
			return rafInvalid
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".cache" || d.Name() == "__pycache__" {
				return rafInvalid
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return rafInvalid
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.ContainsAny(rel, "\x00\r\n") {
			return rafInvalid
		}
		paths = append(paths, filepath.ToSlash(rel))
		if len(paths) > count {
			return rafInvalid
		}
		return nil
	})
	if err != nil || len(paths) != count {
		return "", rafInvalid
	}
	sort.Strings(paths)
	if rafSHA([]byte(strings.Join(paths, "\n")+"\n")) != expectedInventory {
		return "", rafInvalid
	}
	h := sha256.New()
	total := 0
	for _, rel := range paths {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || len(body) > 1<<20 {
			return "", rafInvalid
		}
		total += len(body)
		if total > 16<<20 {
			return "", rafInvalid
		}
		_, _ = io.WriteString(h, rel+"\x00"+rafSHA(body)+"\n")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func rafSourceDigest() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		return "", rafInvalid
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	return rafSourceDigestAt(root, 341, rafSourceInventorySHA256)
}

// This independently reviewed constant hashes the 341 sorted POSIX paths listed
// in scripts/pg-registration-cases.json, each terminated by LF. File content is
// separately bound by the external reviewed source pin and run envelope.
const rafSourceInventorySHA256 = "09f805689c01aec0de08a388024ec3bde02ca39050198d58927a22b592f2069f"

func rafExecutableDigest() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", rafInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return "", rafInvalid
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 256<<20 {
		return "", rafInvalid
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (256<<20)+1))
	if err != nil || n != info.Size() {
		return "", rafInvalid
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Failed prerequisites abort the parent immediately. A failed subtest never
// permits later leaves to run against an uncertain or partially initialized DB.
func rafLeaf(t *testing.T, name string, body func(*testing.T)) {
	t.Helper()
	if !t.Run(name, body) {
		t.Fatal("isolated acceptance stopped at failed leaf; no retry")
	}
}

type rafProvisioningEvidence struct {
	Format                      string `json:"format"`
	RunID                       string `json:"run_id"`
	Database                    string `json:"database"`
	Image                       string `json:"image"`
	Host                        string `json:"host"`
	Port                        uint16 `json:"port"`
	CPUs                        int    `json:"cpus"`
	MemoryBytes                 int64  `json:"memory_bytes"`
	MemorySwapBytes             int64  `json:"memory_swap_bytes"`
	SharedMemoryBytes           int64  `json:"shared_memory_bytes"`
	TmpfsBytes                  int64  `json:"tmpfs_bytes"`
	PGData                      string `json:"pgdata"`
	WAL                         string `json:"wal"`
	FreshDatabase               bool   `json:"fresh_database"`
	BootstrapClosed             bool   `json:"bootstrap_closed"`
	NoForeignOrPersistentMounts bool   `json:"no_foreign_or_persistent_mounts"`
	NoHostNetworkOrPrivileged   bool   `json:"no_host_network_or_privileged"`
	OwnedResourceCleanupPlan    string `json:"owned_resource_cleanup_plan"`
}

func rafValidateProvisioning(body []byte, e rafRunEnvelope) error {
	if rafSHA(body) != e.ProvisioningEvidenceSHA256 {
		return rafInvalid
	}
	var p rafProvisioningEvidence
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return rafInvalid
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(canonical, body) {
		return rafInvalid
	}
	if p.Format != "worktool-registration-provisioning-evidence-v1" || p.RunID != e.RunID || p.Database != e.Database || p.Image != e.Image || p.Host != e.Host || p.Port != e.Port || p.CPUs != 2 || p.MemoryBytes != 1<<30 || p.MemorySwapBytes != 1<<30 || p.SharedMemoryBytes != 64<<20 || p.TmpfsBytes != 2<<30 || p.PGData != "/var/lib/postgresql/18/docker" || p.WAL != "/var/lib/postgresql/18/docker/pg_wal" || !p.FreshDatabase || !p.BootstrapClosed || !p.NoForeignOrPersistentMounts || !p.NoHostNetworkOrPrivileged || p.OwnedResourceCleanupPlan != "external-supervisor-cleans-exact-run-owned-resources-once" {
		return rafInvalid
	}
	return nil
}

type rafFixture struct {
	e                                  rafRunEnvelope
	owner, issuer, consumer, unrelated *pgx.Conn
	ctx                                context.Context
	halted                             bool // Supervisor stop only; NOT durable authority or restart safety.
	committedGrant, committedOperation string
}

func rafOpenFixture(t *testing.T) *rafFixture {
	t.Helper()
	path := os.Getenv(rafEnvelopeEnv)
	if path == "" {
		t.Skip("NOT_RUN: real PostgreSQL registration fixture needs a separately reviewed exact run envelope and preprovisioned isolated database")
	}
	var e rafRunEnvelope
	rafLeaf(t, "preflight_private_envelope_source_executable", func(t *testing.T) {
		raw, err := rafReadPrivate(path)
		if err != nil {
			t.Fatal("registration private envelope rejected; no connection")
		}
		e, err = rafPreconnect(string(raw), os.Environ(), os.ReadFile, rafSourceDigest)
		if err != nil {
			t.Fatal("registration source gate failed before connect; no fallback")
		}
		executable, err := rafExecutableDigest()
		if err != nil || executable != e.ExecutableSHA256 {
			t.Fatal("registration executable binding rejected; no connection")
		}
		provisioning, err := rafReadPrivate(filepath.Join(filepath.Dir(path), "provisioning-evidence.json"))
		if err != nil || rafValidateProvisioning(provisioning, e) != nil {
			t.Fatal("registration provisioning evidence rejected; no connection")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	f := &rafFixture{e: e, ctx: ctx}
	fixtureT := t
	// Four separately authenticated ordinary sessions, opened serially. Owner
	// proves capacity before any other connection. No pool or bootstrap session.
	for n, v := range []struct {
		dsn, role, leaf string
		target          **pgx.Conn
	}{{e.OwnerDSN, rafOwner, "preflight_owner_identity_capacity", &f.owner}, {e.IssuerDSN, rafIssuer, "preflight_issuer_identity", &f.issuer}, {e.ConsumerDSN, rafConsumer, "preflight_consumer_identity", &f.consumer}, {e.UnrelatedDSN, rafUnrelated, "preflight_unrelated_identity", &f.unrelated}} {
		rafLeaf(t, v.leaf, func(t *testing.T) {
			cfg, err := pgx.ParseConfig(v.dsn)
			if err != nil || cfg.Host != e.Host || cfg.Port != e.Port || cfg.Database != e.Database || cfg.User != v.role || len(cfg.Fallbacks) != 0 || cfg.TLSConfig != nil {
				t.Fatal("registration explicit connection config rejected")
			}
			cfg.ConnectTimeout = 2 * time.Second
			cfg.RuntimeParams = map[string]string{"application_name": e.RunID, "statement_timeout": "5000", "lock_timeout": "1000", "idle_in_transaction_session_timeout": "5000", "search_path": "pg_catalog,wt_synthetic_registry,pg_temp"}
			c, err := pgx.ConnectConfig(ctx, cfg)
			if err != nil {
				t.Fatal("registration connection failed; redacted; stop without retry")
			}
			*v.target = c
			fixtureT.Cleanup(func() {
				clean, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				if !c.IsClosed() && c.Close(clean) != nil {
					fixtureT.Error("registration connection disposition unconfirmed; no retry")
				}
			})
			var session, effective, db string
			var roleOID, databaseOID uint32
			var unsafe bool
			err = c.QueryRow(ctx, `SELECT session_user::text,current_user::text,current_database(),r.oid,d.oid,r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls FROM pg_catalog.pg_roles r,pg_catalog.pg_database d WHERE r.rolname=session_user AND d.datname=current_database()`).Scan(&session, &effective, &db, &roleOID, &databaseOID, &unsafe)
			if err != nil || session != v.role || effective != v.role || db != e.Database || databaseOID != e.DatabaseOID || unsafe ||
				(v.role == rafIssuer && roleOID != e.IssuerOID) || (v.role == rafConsumer && roleOID != e.ConsumerOID) {
				t.Fatal("registration authenticated principal/database mismatch")
			}
			if n == 0 {
				var version, max, reserved, superReserved, live int
				err = c.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer,current_setting('max_connections')::integer,current_setting('reserved_connections')::integer,current_setting('superuser_reserved_connections')::integer,(SELECT count(*)::integer FROM pg_catalog.pg_stat_activity WHERE usesysid IS NOT NULL OR usename IS NOT NULL OR backend_type IS NULL)`).Scan(&version, &max, &reserved, &superReserved, &live)
				if err != nil || version != 180006 || max != 10 || max-reserved-superReserved < 5 || live != 1 {
					t.Fatal("registration isolated capacity/version preflight failed")
				}
			}
		})
	}
	rafLeaf(t, "preflight_role_memberships", func(t *testing.T) {
		var memberships int
		err := f.owner.QueryRow(ctx, `SELECT count(*)::integer FROM pg_catalog.pg_auth_members m JOIN pg_catalog.pg_roles r ON r.oid=m.member WHERE r.rolname IN ('wt_registration_lab_issuer','wt_registration_lab_consumer','wt_registration_lab_unrelated','wt_registration_lab_routine')`).Scan(&memberships)
		if err != nil || memberships != 0 {
			t.Fatal("registration untrusted membership preflight failed")
		}
	})
	rafLeaf(t, "preflight_routine_definition", func(t *testing.T) {
		var definition, owner string
		var definer bool
		err := f.owner.QueryRow(ctx, `SELECT pg_catalog.pg_get_functiondef(p.oid),r.rolname,p.prosecdef FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_roles r ON r.oid=p.proowner WHERE p.oid='wt_synthetic_registry.consume_prepared_fresh_registration_v1(text,text)'::pg_catalog.regprocedure`).Scan(&definition, &owner, &definer)
		if err != nil || rafSHA([]byte(definition)) != e.RoutineDefinitionSHA256 || owner != rafRoutineOwner || !definer {
			t.Fatal("registration installed routine identity preflight failed")
		}
	})
	rafLeaf(t, "preflight_catalog_acl", func(t *testing.T) {
		var catalogSafe bool
		if err := f.owner.QueryRow(ctx, rafCatalogPreflightSQL).Scan(&catalogSafe); err != nil || !catalogSafe {
			t.Fatal("registration live catalog/membership/ACL posture rejected; no repair")
		}
	})
	return f
}

// Effective checks include inherited/PUBLIC and column grants, while raw ACL
// checks reject unrelated grantees/default grant options. Still only a focused
// live preflight; it is not the broader authority-plan acceptance matrix.
const rafCatalogPreflightSQL = `WITH
roles AS (SELECT * FROM pg_catalog.pg_roles WHERE rolname IN ('wt_registration_lab_owner','wt_registration_lab_issuer','wt_registration_lab_consumer','wt_registration_lab_unrelated','wt_registration_lab_routine')),
ns AS (SELECT * FROM pg_catalog.pg_namespace WHERE nspname='wt_synthetic_registry'),
owner AS (SELECT oid FROM roles WHERE rolname='wt_registration_lab_owner'),
consumer AS (SELECT oid FROM roles WHERE rolname='wt_registration_lab_consumer'),
unrelated AS (SELECT oid FROM roles WHERE rolname='wt_registration_lab_unrelated'),
issuer AS (SELECT oid FROM roles WHERE rolname='wt_registration_lab_issuer'),
routine AS (SELECT oid FROM roles WHERE rolname='wt_registration_lab_routine'),
relations AS (SELECT c.* FROM pg_catalog.pg_class c,ns WHERE c.relnamespace=ns.oid),
functions AS (SELECT p.* FROM pg_catalog.pg_proc p,ns WHERE p.pronamespace=ns.oid)
SELECT (SELECT count(*)=5 FROM roles)
AND NOT EXISTS(SELECT 1 FROM roles WHERE rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls OR rolinherit OR rolcanlogin<>(rolname<>'wt_registration_lab_routine'))
AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members m WHERE (m.roleid IN(SELECT oid FROM roles) OR m.member IN(SELECT oid FROM roles)) AND NOT(m.member=(SELECT oid FROM owner) AND m.roleid=(SELECT oid FROM routine) AND m.set_option AND NOT m.inherit_option AND NOT m.admin_option))
AND (SELECT nspowner=(SELECT oid FROM owner) FROM ns)
AND (SELECT datdba=(SELECT oid FROM owner) FROM pg_catalog.pg_database WHERE datname=current_database())
AND NOT EXISTS(SELECT 1 FROM roles,ns WHERE rolname<>'wt_registration_lab_owner' AND (pg_catalog.has_database_privilege(roles.oid,current_database(),'CREATE,TEMP') OR pg_catalog.has_schema_privilege(roles.oid,ns.oid,'CREATE')))
AND NOT EXISTS(SELECT 1 FROM relations WHERE relowner<>(SELECT oid FROM owner) OR relkind NOT IN('r','i','S') OR relpersistence<>'p' OR relrowsecurity OR relforcerowsecurity)
AND NOT EXISTS(SELECT 1 FROM relations r JOIN pg_catalog.pg_attribute a ON a.attrelid=r.oid WHERE r.relkind='r' AND a.attnum>0 AND NOT a.attisdropped AND (pg_catalog.has_column_privilege((SELECT oid FROM consumer),r.oid,a.attnum,'SELECT,INSERT,UPDATE,REFERENCES') OR pg_catalog.has_column_privilege((SELECT oid FROM unrelated),r.oid,a.attnum,'SELECT,INSERT,UPDATE,REFERENCES') OR pg_catalog.has_column_privilege((SELECT oid FROM issuer),r.oid,a.attnum,'SELECT,UPDATE,REFERENCES') OR (r.relname<>'registration_prepared_grants' AND pg_catalog.has_column_privilege((SELECT oid FROM issuer),r.oid,a.attnum,'INSERT'))))
AND NOT EXISTS(SELECT 1 FROM relations r CROSS JOIN roles WHERE r.relkind='r' AND roles.rolname IN('wt_registration_lab_consumer','wt_registration_lab_unrelated','wt_registration_lab_issuer') AND pg_catalog.has_table_privilege(roles.oid,r.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'))
AND NOT EXISTS(SELECT 1 FROM relations r CROSS JOIN roles WHERE r.relkind='S' AND roles.rolname<>'wt_registration_lab_owner' AND pg_catalog.has_sequence_privilege(roles.oid,r.oid,'USAGE,SELECT,UPDATE'))
AND NOT EXISTS(SELECT 1 FROM relations r CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(r.relacl,pg_catalog.acldefault(CASE WHEN r.relkind='S' THEN 's'::"char" ELSE 'r'::"char" END,r.relowner))) a WHERE r.relkind IN('r','S') AND (a.grantee NOT IN((SELECT oid FROM owner),(SELECT oid FROM routine)) OR (a.is_grantable AND a.grantee<>r.relowner)))
AND NOT EXISTS(SELECT 1 FROM relations r JOIN pg_catalog.pg_attribute c ON c.attrelid=r.oid CROSS JOIN LATERAL pg_catalog.aclexplode(c.attacl) a WHERE a.grantee NOT IN((SELECT oid FROM owner),(SELECT oid FROM routine)) AND NOT(a.grantee=(SELECT oid FROM issuer) AND r.relname='registration_prepared_grants' AND a.privilege_type='INSERT' AND NOT a.is_grantable))
AND NOT EXISTS(SELECT 1 FROM ns CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(ns.nspacl,pg_catalog.acldefault('n',ns.nspowner))) a WHERE (a.grantee NOT IN(SELECT oid FROM roles)) OR (a.grantee<>ns.nspowner AND (a.privilege_type<>'USAGE' OR a.is_grantable)))
AND (SELECT count(*)=1 FROM functions WHERE proname='consume_prepared_fresh_registration_v1')
AND NOT EXISTS(SELECT 1 FROM functions p WHERE (p.proname='consume_prepared_fresh_registration_v1' AND (p.proowner<>(SELECT oid FROM routine) OR NOT p.prosecdef OR NOT COALESCE('search_path=pg_catalog, wt_synthetic_registry, pg_temp'=ANY(p.proconfig),false))) OR (p.proname<>'consume_prepared_fresh_registration_v1' AND (p.proowner<>(SELECT oid FROM owner) OR p.prosecdef)))
AND pg_catalog.has_function_privilege((SELECT oid FROM consumer),'wt_synthetic_registry.consume_prepared_fresh_registration_v1(text,text)','EXECUTE')
AND NOT EXISTS(SELECT 1 FROM functions p CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))) a WHERE a.grantee=0 OR (a.is_grantable AND a.grantee<>p.proowner) OR a.grantee NOT IN((SELECT oid FROM owner),(SELECT oid FROM routine),(SELECT oid FROM consumer)) OR (a.grantee=(SELECT oid FROM consumer) AND p.proname<>'consume_prepared_fresh_registration_v1'))
AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_default_acl d CROSS JOIN LATERAL pg_catalog.aclexplode(d.defaclacl) a,ns WHERE (d.defaclnamespace=ns.oid OR d.defaclnamespace=0) AND d.defaclrole IN(SELECT oid FROM roles) AND a.grantee<>d.defaclrole)
AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint c,ns WHERE c.connamespace=ns.oid AND NOT c.convalidated)
AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_trigger t JOIN relations r ON r.oid=t.tgrelid WHERE t.tgenabled<>'O')`

// Caller supplies ONLY these two opaque identifiers. No descriptor, role, actor,
// intent, grant content or identifier interpolation can reach the SQL boundary.
func rafInvoke(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, grant, operation string) (string, error) {
	if ctx == nil || q == nil || !asciiIdentifier(grant) || !asciiIdentifier(operation) {
		return "", rafInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		return "", rafInvalid
	}
	var result string
	err := q.QueryRow(ctx, rafConsumeSQL, grant, operation).Scan(&result)
	if err != nil {
		return "", err
	}
	if result != string(registrationPending) && result != string(registrationObserved) {
		return "", rafUnknown
	}
	return result, nil
}

// A routine return is provisional until a clean outer COMMIT. Any ambiguous
// commit/cleanup stops the supervisor. This cannot survive process restart and
// is deliberately NOT the missing durable pre-mutation/recovery authority.
func rafClassify(provisional string, body, commit, cleanup error) (string, error) {
	if commit != nil || cleanup != nil {
		return "", rafUnknown
	}
	if body != nil {
		return "", body
	}
	if provisional != string(registrationPending) && provisional != string(registrationObserved) {
		return "", rafUnknown
	}
	return provisional, nil
}

func (f *rafFixture) consume(t *testing.T, grant, operation string, observed bool) string {
	t.Helper()
	if f.halted || (observed && (f.committedGrant != grant || f.committedOperation != operation)) || (!observed && f.committedGrant != "") {
		t.Fatal("registration supervisor refuses unconfirmed repeat or retry")
	}
	tx, err := f.consumer.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		f.halted = true
		t.Fatal("registration BEGIN unconfirmed; stop")
	}
	provisional, body := rafInvoke(f.ctx, tx, grant, operation)
	var commit error
	if body == nil {
		commit = tx.Commit(f.ctx)
	}
	clean, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	cleanup := tx.Rollback(clean)
	cancel()
	if errors.Is(cleanup, pgx.ErrTxClosed) {
		cleanup = nil
	}
	result, outcomeErr := rafClassify(provisional, body, commit, cleanup)
	if outcomeErr != nil {
		f.halted = true
		t.Fatalf("registration consume failed class=%s; stop without retry", rafErrorClass(outcomeErr))
	}
	want := string(registrationPending)
	if observed {
		want = string(registrationObserved)
	}
	if result != want {
		f.halted = true
		t.Fatal("registration unexpected outcome; stop")
	}
	f.committedGrant, f.committedOperation = grant, operation
	return result
}

func rafErrorClass(err error) string {
	if err == nil {
		return "NONE"
	}
	if errors.Is(err, rafUnknown) {
		return "UNKNOWN_STOP"
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "42501":
			return "INSUFFICIENT_PRIVILEGE"
		case "23514":
			return "CONSTRAINT_REJECTION"
		case "40001":
			return "SERIALIZATION_FAILURE_NOT_DENIAL"
		case "57014":
			return "CANCELLED_NOT_DENIAL"
		}
		return "UNEXPECTED_SQLSTATE"
	}
	return "TRANSPORT_OR_LOCAL_NOT_DENIAL"
}

// Full immutable prepared-row ABI. All fields are reconstructed, never trusted
// merely because a caller supplies matching hashes or a canonical-looking blob.
type rafPreparedGrant struct {
	GrantID                   string
	AuthorityID               string
	OperationID               string
	Tenant                    string
	App                       string
	DestinationID             string
	Format                    string
	EvidenceKind              string
	Action                    string
	ExpectedRevision          int64
	NewRevision               int64
	DatabaseName              string
	DatabaseOID               uint32
	SchemaName                string
	SchemaOID                 uint32
	EnvironmentID             string
	DeploymentGeneration      string
	RecoveryGeneration        string
	RoutineDefinitionSHA256   string
	SourceManifestSHA256      string
	IssuerRoleName            string
	IssuerRoleOID             uint32
	ConsumerRoleName          string
	ConsumerRoleOID           uint32
	AuthorityPolicyID         string
	Actor                     string
	ActorGeneration           string
	ApprovalRef               string
	ApprovalDigest            string
	ApprovalGeneration        string
	FenceRef                  string
	FenceGeneration           string
	HistoryRef                string
	HistoryDigest             string
	HistoryGeneration         string
	ProviderAttestationRef    string
	ProviderMappingGeneration string
	BarrierGeneration         string
	BootGeneration            string
	IssuedAt                  time.Time
	NotBefore                 time.Time
	ExpiresAt                 time.Time
	PlanIdentity              string
	PolicyVersion             int64
	KeyVersion                int64
	CanonicalURL              string
	CIDRs                     []string
	SignatureProfile          string
	KeyRef                    string
	ProviderAuthorityID       string
	RevisionKind              string
	RevisionID                string
	PolicyIdentity            string
	KeyIdentity               string
	BindingIdentity           string
	DescriptorBytes           []byte
	IntentBytes               []byte
}

const rafPreparedInsert = `INSERT INTO wt_synthetic_registry.registration_prepared_grants(grant_id,authority_id,operation_id,tenant,app,destination_id,format,evidence_kind,action,expected_revision,new_revision,database_name,database_oid,schema_name,schema_oid,environment_id,deployment_generation,recovery_generation,routine_definition_sha256,source_manifest_sha256,issuer_role_name,issuer_role_oid,consumer_role_name,consumer_role_oid,authority_policy_id,actor,actor_generation,approval_ref,approval_digest,approval_generation,fence_ref,fence_generation,history_ref,history_digest,history_generation,provider_attestation_ref,provider_mapping_generation,barrier_generation,boot_generation,issued_at,not_before,expires_at,plan_identity,policy_version,key_version,canonical_url,cidrs,signature_profile,key_ref,provider_authority_id,revision_kind,revision_id,policy_identity,key_identity,binding_identity,descriptor_bytes,intent_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47,$48,$49,$50,$51,$52,$53,$54,$55,$56,$57)`

func (g rafPreparedGrant) args() []any {
	return []any{g.GrantID, g.AuthorityID, g.OperationID, g.Tenant, g.App, g.DestinationID, g.Format, g.EvidenceKind, g.Action, g.ExpectedRevision, g.NewRevision, g.DatabaseName, g.DatabaseOID, g.SchemaName, g.SchemaOID, g.EnvironmentID, g.DeploymentGeneration, g.RecoveryGeneration, g.RoutineDefinitionSHA256, g.SourceManifestSHA256, g.IssuerRoleName, g.IssuerRoleOID, g.ConsumerRoleName, g.ConsumerRoleOID, g.AuthorityPolicyID, g.Actor, g.ActorGeneration, g.ApprovalRef, g.ApprovalDigest, g.ApprovalGeneration, g.FenceRef, g.FenceGeneration, g.HistoryRef, g.HistoryDigest, g.HistoryGeneration, g.ProviderAttestationRef, g.ProviderMappingGeneration, g.BarrierGeneration, g.BootGeneration, g.IssuedAt, g.NotBefore, g.ExpiresAt, g.PlanIdentity, g.PolicyVersion, g.KeyVersion, g.CanonicalURL, g.CIDRs, g.SignatureProfile, g.KeyRef, g.ProviderAuthorityID, g.RevisionKind, g.RevisionID, g.PolicyIdentity, g.KeyIdentity, g.BindingIdentity, g.DescriptorBytes, g.IntentBytes}
}

// This is a synthetic test authority supplied separately from the proposed row.
// Its source/definition/provenance hashes are pinned fixture inputs, not signatures
// and not evidence that an external authority or provider authenticated anything.
type rafSyntheticAuthority struct {
	run                        rafRunEnvelope
	issued, notBefore, expires time.Time
}

func rafFiniteUTC(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC && t.Year() >= 2020 && t.Year() < 2100 && t.Equal(t.Truncate(time.Microsecond))
}

func rafBuildPrepared(i registrationIntent, a rafSyntheticAuthority, grantID string) (rafPreparedGrant, error) {
	var z rafPreparedGrant
	// Re-decode canonical descriptor using the frozen parser and demand exact
	// re-encoding. Intent is independently canonicalized from private typed fields.
	d, err := registration.Decode(i.descriptor.CanonicalBytes())
	if err != nil || !bytes.Equal(d.CanonicalBytes(), i.descriptor.CanonicalBytes()) || d.PolicyIdentity() != i.descriptor.PolicyIdentity() || d.KeyIdentity() != i.descriptor.KeyIdentity() || d.BindingIdentity() != i.descriptor.BindingIdentity() {
		return z, rafInvalid
	}
	intent, err := i.canonical()
	if err != nil {
		return z, rafInvalid
	}
	v := d.Value()
	if !strings.HasPrefix(grantID, "grant_synthetic_") || !asciiIdentifier(grantID) || i.authorityID != "authority_synthetic" || i.actor != "actor_synthetic" || i.approvalRef != "approval_synthetic" || i.fenceRef != "fence_synthetic" || i.historyRef != "history_synthetic" || v.Tenant != "tenant_synthetic" || v.App != "app_synthetic" || !strings.HasPrefix(v.Destination, "dest_synthetic") || !strings.HasPrefix(i.operationID, "op_synthetic") || v.ProviderAuthorityID != "provider_synthetic" || !strings.HasPrefix(v.KeyRef, "synthetic-") {
		return z, rafInvalid
	}
	if a.run.SchemaOID == 0 || a.run.DatabaseOID == 0 || a.run.ConsumerOID == 0 || a.run.IssuerOID == 0 || a.run.ConsumerOID == a.run.IssuerOID || !rafDBPattern.MatchString(a.run.Database) || !rafRunPattern.MatchString(a.run.Environment) || !rafRunPattern.MatchString(a.run.RecoveryGeneration) || !rafRunPattern.MatchString(a.run.RunID) || !rafDigest(a.run.SourceManifestSHA256) || !rafDigest(a.run.RoutineDefinitionSHA256) {
		return z, rafInvalid
	}
	if !rafFiniteUTC(a.issued) || !rafFiniteUTC(a.notBefore) || !rafFiniteUTC(a.expires) || a.notBefore.Before(a.issued) || !a.expires.After(a.notBefore) || a.expires.Sub(a.issued) > 15*time.Minute {
		return z, rafInvalid
	}
	generation := a.run.RunID
	g := rafPreparedGrant{
		GrantID: grantID, AuthorityID: i.authorityID, OperationID: i.operationID, Tenant: v.Tenant, App: v.App, DestinationID: v.Destination,
		Format: "worktool-synthetic-prepared-registration-v1", EvidenceKind: "SYNTHETIC_ONLY", Action: "REGISTER_FRESH_PENDING", ExpectedRevision: 0, NewRevision: 1,
		DatabaseName: a.run.Database, DatabaseOID: a.run.DatabaseOID, SchemaName: registrationCandidateSchema, SchemaOID: a.run.SchemaOID,
		EnvironmentID: a.run.Environment, DeploymentGeneration: generation, RecoveryGeneration: a.run.RecoveryGeneration, RoutineDefinitionSHA256: a.run.RoutineDefinitionSHA256, SourceManifestSHA256: a.run.SourceManifestSHA256,
		IssuerRoleName: rafIssuer, IssuerRoleOID: a.run.IssuerOID, ConsumerRoleName: rafConsumer, ConsumerRoleOID: a.run.ConsumerOID, AuthorityPolicyID: "synthetic_pending_only_policy_v1",
		Actor: i.actor, ActorGeneration: generation, ApprovalRef: i.approvalRef, ApprovalDigest: rafSHA(append([]byte("SYNTHETIC_APPROVAL_NOT_AUTHENTICATED\x00"), intent...)), ApprovalGeneration: generation,
		FenceRef: i.fenceRef, FenceGeneration: generation, HistoryRef: i.historyRef, HistoryDigest: rafSHA([]byte("SYNTHETIC_HISTORY_NOT_PROVEN\x00" + generation)), HistoryGeneration: generation,
		ProviderAttestationRef: "synthetic_provider_attestation_unproven", ProviderMappingGeneration: generation, BarrierGeneration: generation, BootGeneration: generation,
		IssuedAt: a.issued, NotBefore: a.notBefore, ExpiresAt: a.expires, PlanIdentity: i.planIdentity, PolicyVersion: v.PolicyVersion, KeyVersion: v.KeyVersion, CanonicalURL: v.URL, CIDRs: v.CIDRs, SignatureProfile: v.SignatureProfile, KeyRef: v.KeyRef, ProviderAuthorityID: v.ProviderAuthorityID, RevisionKind: v.ProviderRevision.Kind, RevisionID: v.ProviderRevision.ID,
		PolicyIdentity: d.PolicyIdentity(), KeyIdentity: d.KeyIdentity(), BindingIdentity: d.BindingIdentity(), DescriptorBytes: d.CanonicalBytes(), IntentBytes: intent,
	}
	return g, nil
}

func rafValidatePrepared(proposed rafPreparedGrant, expectedIntent registrationIntent, authority rafSyntheticAuthority, expectedGrant string, now time.Time) error {
	expected, err := rafBuildPrepared(expectedIntent, authority, expectedGrant)
	if err != nil || !rafFiniteUTC(now) || now.Before(authority.notBefore) || !now.Before(authority.expires) || !reflect.DeepEqual(proposed, expected) {
		return rafInvalid
	}
	return nil
}

// No INSERT can occur before complete record validation. The sink itself is the
// separately authenticated trusted issuer connection, never the consumer.
func rafImportPrepared(ctx context.Context, sink execer, proposed rafPreparedGrant, expectedIntent registrationIntent, authority rafSyntheticAuthority, expectedGrant string, now time.Time) error {
	if ctx == nil || sink == nil {
		return rafInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		return rafInvalid
	}
	if err := ctx.Err(); err != nil {
		return rafInvalid
	}
	if err := rafValidatePrepared(proposed, expectedIntent, authority, expectedGrant, now); err != nil {
		return err
	}
	tag, err := sink.Exec(ctx, rafPreparedInsert, proposed.args()...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return rafUnknown
	}
	return nil
}

const rafControlInsert = `INSERT INTO wt_synthetic_registry.registration_authority_control(grant_id,authority_id,tenant,app,destination_id,database_name,database_oid,schema_oid,environment_id,deployment_generation,recovery_generation,routine_definition_sha256,source_manifest_sha256,issuer_role_name,issuer_role_oid,consumer_role_name,consumer_role_oid,authority_policy_id,actor_generation,approval_generation,fence_generation,history_generation,provider_mapping_generation,barrier_generation,boot_generation,control_generation,armed,revoked,clock_healthy,decision_not_after) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,1,true,false,true,$26)`

func rafSeedControl(ctx context.Context, owner execer, g rafPreparedGrant) error {
	tag, err := owner.Exec(ctx, rafControlInsert, g.GrantID, g.AuthorityID, g.Tenant, g.App, g.DestinationID, g.DatabaseName, g.DatabaseOID, g.SchemaOID, g.EnvironmentID, g.DeploymentGeneration, g.RecoveryGeneration, g.RoutineDefinitionSHA256, g.SourceManifestSHA256, g.IssuerRoleName, g.IssuerRoleOID, g.ConsumerRoleName, g.ConsumerRoleOID, g.AuthorityPolicyID, g.ActorGeneration, g.ApprovalGeneration, g.FenceGeneration, g.HistoryGeneration, g.ProviderMappingGeneration, g.BarrierGeneration, g.BootGeneration, g.ExpiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return rafUnknown
	}
	return nil
}

func rafObserverGraph(t *testing.T, f *rafFixture, i registrationIntent, g rafPreparedGrant) {
	t.Helper()
	tx, err := f.owner.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal("independent observer begin failed")
	}
	// Existing exact graph checker locks rows, so this observer transaction is not
	// declared READ ONLY. It performs no business mutation and is rolled back.
	body, err := i.canonical()
	if err == nil {
		err = registrationReadGraph(f.ctx, tx, newRegistrationSQL(registrationCandidateSchema), i, body)
	}
	var count int
	if err == nil {
		err = tx.QueryRow(f.ctx, `SELECT count(*)::integer FROM wt_synthetic_registry.registration_grant_consumptions WHERE grant_id=$1 AND authority_id=$2 AND tenant=$3 AND operation_id=$4 AND app=$5 AND destination_id=$6 AND policy_version=$7 AND key_version=$8 AND binding_identity=$9 AND intent_bytes=$10 AND control_generation=1 AND created_xid<>pg_catalog.pg_current_xact_id() AND pg_catalog.isfinite(recorded_at) AND recorded_at>=$11 AND recorded_at<$12 AND outcome='REGISTERED_PENDING_NOT_ENABLED'`, g.GrantID, g.AuthorityID, g.Tenant, g.OperationID, g.App, g.DestinationID, g.PolicyVersion, g.KeyVersion, g.BindingIdentity, g.IntentBytes, g.NotBefore, g.ExpiresAt).Scan(&count)
	}
	clean, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	cleanup := tx.Rollback(clean)
	cancel()
	if err != nil || count != 1 || cleanup != nil {
		f.halted = true
		t.Fatal("independent exact pending graph/consumption observation failed; no retry")
	}
}

func rafExpectSQLState(t *testing.T, f *rafFixture, c *pgx.Conn, q, want string, args ...any) {
	t.Helper()
	// Each statement gets an independent outer transaction. A previous expected
	// failure cannot mask the next assertion with 25P02.
	tx, err := c.BeginTx(f.ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal("denial transaction begin failed")
	}
	_, err = tx.Exec(f.ctx, q, args...)
	clean, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	cleanup := tx.Rollback(clean)
	cancel()
	var pe *pgconn.PgError
	if cleanup != nil || !errors.As(err, &pe) || pe.Code != want {
		f.halted = true
		t.Fatalf("expected denial not established, expected SQLSTATE=%s observed class=%s; stop", want, rafErrorClass(err))
	}
}

// Each denial includes confirmed rollback plus independent unchanged-state
// observation in the same leaf. No SQL/error message or connection data is logged.
func rafDenyUnchanged(t *testing.T, f *rafFixture, c *pgx.Conn, query, state string, args ...any) {
	t.Helper()
	before := rafSnapshot(t, f)
	rafExpectSQLState(t, f, c, query, state, args...)
	if rafSnapshot(t, f) != before {
		f.halted = true
		t.Fatal("denial changed protected state; stop")
	}
}

// A distinct valid grant proves that acceptance is not merely blanket refusal.
// The routine verifies the complete graph inside its transaction; it is then
// explicitly rolled back. Only inert prepared/control rows remain committed.
func rafOtherGrantRollback(t *testing.T, f *rafFixture) {
	t.Helper()
	i := frIntent(t)
	i.operationID = "op_synthetic_rollback_control"
	frChangeDescriptor(t, &i, func(v *registration.Value) { v.Destination = "dest_synthetic_rollback_control" })
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := rafSyntheticAuthority{run: f.e, issued: now.Add(-time.Second), notBefore: now.Add(-time.Second), expires: now.Add(time.Minute)}
	grant := "grant_synthetic_rollback_" + f.e.RunID
	g, err := rafBuildPrepared(i, a, grant)
	if err != nil || rafImportPrepared(f.ctx, f.issuer, g, i, a, grant, now) != nil {
		t.Fatal("rollback-control grant import unconfirmed; no retry")
	}
	if rafSeedControl(f.ctx, f.owner, g) != nil {
		t.Fatal("rollback-control seed unconfirmed; no retry")
	}
	before := rafSnapshot(t, f)
	tx, err := f.consumer.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		f.halted = true
		t.Fatal("rollback-control begin unconfirmed; no retry")
	}
	result, body := rafInvoke(f.ctx, tx, grant, i.operationID)
	// Consumer has no direct-table privileges. The routine performs its complete
	// graph checks; independent owner readback after rollback proves no persistence.
	clean, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	cleanup := tx.Rollback(clean)
	cancel()
	if body != nil || result != string(registrationPending) || cleanup != nil {
		f.halted = true
		t.Fatal("valid-other-grant rollback not established; no retry")
	}
	if rafSnapshot(t, f) != before {
		f.halted = true
		t.Fatal("rolled-back positive control persisted protected state")
	}
}

func TestRegistrationAuthorityIsolatedPostgreSQL(t *testing.T) {
	f := rafOpenFixture(t) // The absent gate skips before any leaf, read or connect.
	rafLeaf(t, "preflight_pristine_prerequisites", func(t *testing.T) {
		var schemaOID uint32
		var pristine bool
		err := f.owner.QueryRow(f.ctx, `SELECT n.oid,EXISTS(SELECT 1 FROM wt_synthetic_registry.developer_apps WHERE tenant='tenant_synthetic' AND id='app_synthetic' AND active) AND EXISTS(SELECT 1 FROM wt_synthetic_registry.scope_limits WHERE scope_kind='tenant' AND tenant='tenant_synthetic' AND scope_id='tenant_synthetic') AND NOT EXISTS(SELECT 1 FROM wt_synthetic_registry.callback_destinations) AND NOT EXISTS(SELECT 1 FROM wt_synthetic_registry.registration_prepared_grants) AND NOT EXISTS(SELECT 1 FROM wt_synthetic_registry.registration_grant_consumptions) FROM pg_catalog.pg_namespace n WHERE n.nspname='wt_synthetic_registry'`).Scan(&schemaOID, &pristine)
		if err != nil || schemaOID != f.e.SchemaOID || !pristine {
			t.Fatal("registration exact fresh prerequisites absent; no provisioning attempted")
		}
	})
	rafLeaf(t, "valid_other_grant_pending_rollback", func(t *testing.T) { rafOtherGrantRollback(t, f) })
	i := frIntent(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := rafSyntheticAuthority{run: f.e, issued: now.Add(-time.Second), notBefore: now.Add(-time.Second), expires: now.Add(20 * time.Second)}
	grant := "grant_synthetic_" + f.e.RunID
	var g rafPreparedGrant
	rafLeaf(t, "issuer_import_exact_grant", func(t *testing.T) {
		var err error
		g, err = rafBuildPrepared(i, a, grant)
		if err != nil || rafImportPrepared(f.ctx, f.issuer, g, i, a, grant, now) != nil {
			t.Fatal("synthetic issuer insert unconfirmed; no retry")
		}
	})
	rafLeaf(t, "owner_seed_exact_control", func(t *testing.T) {
		if rafSeedControl(f.ctx, f.owner, g) != nil {
			t.Fatal("synthetic control insert unconfirmed; no retry")
		}
	})
	rafLeaf(t, "consumer_prepared_select_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.consumer, `SELECT * FROM wt_synthetic_registry.registration_prepared_grants`, "42501")
	})
	rafLeaf(t, "unrelated_prepared_select_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.unrelated, `SELECT * FROM wt_synthetic_registry.registration_prepared_grants`, "42501")
	})
	rafLeaf(t, "consumer_current_update_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.consumer, `UPDATE wt_synthetic_registry.callback_destinations SET state='active' WHERE false`, "42501")
	})
	rafLeaf(t, "unrelated_current_update_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.unrelated, `UPDATE wt_synthetic_registry.callback_destinations SET state='active' WHERE false`, "42501")
	})
	const activeInsert = `INSERT INTO wt_synthetic_registry.callback_destinations(tenant,app,id,policy_version,key_version,canonical_url,key_ref,state) VALUES('tenant_synthetic','app_synthetic','dest_synthetic_forbidden',2,3,'https://hooks.example.com/callback','synthetic-key-reference','active')`
	rafLeaf(t, "consumer_active_insert_denied", func(t *testing.T) { rafDenyUnchanged(t, f, f.consumer, activeInsert, "42501") })
	rafLeaf(t, "unrelated_active_insert_denied", func(t *testing.T) { rafDenyUnchanged(t, f, f.unrelated, activeInsert, "42501") })
	rafLeaf(t, "consumer_current_delete_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.consumer, `DELETE FROM wt_synthetic_registry.callback_destinations WHERE false`, "42501")
	})
	rafLeaf(t, "consumer_current_truncate_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.consumer, `TRUNCATE wt_synthetic_registry.callback_destinations`, "42501")
	})
	rafLeaf(t, "consumer_set_owner_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.consumer, `SET LOCAL ROLE wt_registration_lab_owner`, "42501")
	})
	rafLeaf(t, "issuer_set_consumer_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.issuer, `SET LOCAL ROLE wt_registration_lab_consumer`, "42501")
	})
	rafLeaf(t, "unrelated_set_routine_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.unrelated, `SET LOCAL ROLE wt_registration_lab_routine`, "42501")
	})
	rafLeaf(t, "consumer_helper_execute_denied", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.consumer, `SELECT wt_synthetic_registry.reject_registration_authority_mutation()`, "42501")
	})
	rafLeaf(t, "issuer_consume_denied", func(t *testing.T) { rafDenyUnchanged(t, f, f.issuer, rafConsumeSQL, "42501", grant, i.operationID) })
	rafLeaf(t, "unrelated_consume_denied", func(t *testing.T) { rafDenyUnchanged(t, f, f.unrelated, rafConsumeSQL, "42501", grant, i.operationID) })
	rafLeaf(t, "consumer_wrong_operation_refused", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.consumer, rafConsumeSQL, "23514", grant, "op_synthetic_wrong")
	})
	rafLeaf(t, "owner_prepared_update_immutable", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.owner, `UPDATE wt_synthetic_registry.registration_prepared_grants SET actor=actor WHERE grant_id=$1`, "23514", grant)
	})
	rafLeaf(t, "owner_prepared_delete_immutable", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.owner, `DELETE FROM wt_synthetic_registry.registration_prepared_grants WHERE grant_id=$1`, "23514", grant)
	})
	rafLeaf(t, "owner_control_identity_immutable", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.owner, `UPDATE wt_synthetic_registry.registration_authority_control SET authority_id='authority_synthetic_other',control_generation=control_generation+1 WHERE grant_id=$1`, "23514", grant)
	})
	rafLeaf(t, "single_pending_commit_exact_graph", func(t *testing.T) { f.consume(t, grant, i.operationID, false); rafObserverGraph(t, f, i, g) })
	rafLeaf(t, "owner_consumption_update_immutable", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.owner, `UPDATE wt_synthetic_registry.registration_grant_consumptions SET outcome=outcome WHERE grant_id=$1`, "23514", grant)
	})
	rafLeaf(t, "owner_consumption_delete_immutable", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.owner, `DELETE FROM wt_synthetic_registry.registration_grant_consumptions WHERE grant_id=$1`, "23514", grant)
	})
	beforeRepeat := rafSnapshot(t, f)
	rafLeaf(t, "exact_committed_repeat_observes_without_write", func(t *testing.T) {
		f.consume(t, grant, i.operationID, true)
		rafObserverGraph(t, f, i, g)
		if rafSnapshot(t, f) != beforeRepeat {
			f.halted = true
			t.Fatal("observed repeat wrote protected state")
		}
	})
	rafLeaf(t, "server_clock_observes_original_grant_expiry", func(t *testing.T) {
		if rafAwaitExpiry(f.ctx, f.owner, g.ExpiresAt) != nil {
			f.halted = true
			t.Fatal("server-clock expiry observation incomplete; stop")
		}
	})
	rafLeaf(t, "expired_grant_refused_without_write", func(t *testing.T) {
		rafDenyUnchanged(t, f, f.consumer, rafConsumeSQL, "23514", grant, i.operationID)
		rafObserverGraph(t, f, i, g)
		if rafSnapshot(t, f) != beforeRepeat {
			f.halted = true
			t.Fatal("expiry denial changed protected state")
		}
	})
	rafLeaf(t, "all_fixture_sessions_closed", func(t *testing.T) {
		for _, c := range []*pgx.Conn{f.unrelated, f.consumer, f.issuer, f.owner} {
			clean, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := c.Close(clean)
			cancel()
			if err != nil || !c.IsClosed() {
				f.halted = true
				t.Fatal("fixture session close unconfirmed; no retry")
			}
		}
	})
}

// Independent owner-only observation. Include xmin to distinguish a hidden
// UPDATE to identical values from a no-write repeat. Row locks may change xmax,
// which is deliberately not treated as business mutation. No raw rows are logged.
func rafSnapshot(t *testing.T, f *rafFixture) string {
	t.Helper()
	h := sha256.New()
	for _, table := range []string{"callback_destinations", "callback_destination_versions", "callback_registration_policies", "callback_registration_keys", "callback_registration_bindings", "callback_registration_receipts", "registration_prepared_grants", "registration_authority_control", "registration_grant_consumptions", "scope_limits", "scope_slots", "callback_subscription_versions", "admission_reservations", "callback_outbox", "commands", "callback_replay_receipts", "audit"} {
		var snapshot string
		query := `SELECT COALESCE(pg_catalog.jsonb_agg(pg_catalog.to_jsonb(r)||pg_catalog.jsonb_build_object('_xmin',r.xmin::text) ORDER BY pg_catalog.to_jsonb(r)::text),'[]'::jsonb)::text FROM wt_synthetic_registry.` + table + ` r`
		if err := f.owner.QueryRow(f.ctx, query).Scan(&snapshot); err != nil {
			f.halted = true
			t.Fatal("independent state snapshot failed; no retry")
		}
		if len(snapshot) > 1<<20 {
			f.halted = true
			t.Fatal("independent state snapshot exceeded fixture bound")
		}
		_, _ = io.WriteString(h, table+"\x00"+snapshot+"\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rafAwaitExpiry(ctx context.Context, c interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, expiry time.Time) error {
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		var expired bool
		if err := c.QueryRow(ctx, `SELECT pg_catalog.clock_timestamp()>=$1::timestamptz`, expiry).Scan(&expired); err != nil {
			return err
		}
		if expired {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
