package pg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func rafOfflineEnvelope() rafRunEnvelope {
	e := rafRunEnvelope{Format: "worktool-registration-isolated-fixture-v1", Allow: "one-reviewed-synthetic-pending-only-run-no-retry", RunID: "synthetic_offline_run", Host: "127.0.0.1", Port: 55432, Database: "wt_test_synthetic_offline", DatabaseOID: 12000, SchemaOID: 12001, IssuerOID: 12002, ConsumerOID: 12003, Environment: "synthetic_offline_environment", RecoveryGeneration: "synthetic_offline_recovery", Image: rafImage, ServerVersion: 180006, BoundarySHA256: rafSHA([]byte("boundary")), RegistrySHA256: rafSHA([]byte("registry")), RoutineDefinitionSHA256: rafSHA([]byte("routine")), SourceManifestSHA256: rafSHA([]byte("manifest")), ExecutableSHA256: rafSHA([]byte("executable")), ProvisioningEvidenceSHA256: rafSHA([]byte("unproven external resource evidence")), BootstrapRole: "wt_admin_0123456789abcdef", BootstrapOID: 10, ActivityGuardOID: 12004, AdmissionSchemaOID: 12005, AdmissionFunctionOID: 12006, AdmissionFunctionDefinitionSHA256: rafSHA([]byte("admission definition")), AdmissionSHA256: rafSHA([]byte("admission source"))}
	dsn := func(role string) string {
		return "postgresql://" + role + ":synthetic-not-a-real-secret@127.0.0.1:55432/" + e.Database + "?sslmode=disable"
	}
	e.OwnerDSN = dsn(rafOwner)
	e.IssuerDSN = dsn(rafIssuer)
	e.ConsumerDSN = dsn(rafConsumer)
	e.UnrelatedDSN = dsn(rafUnrelated)
	return e
}
func rafOfflineAuthority() rafSyntheticAuthority {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	return rafSyntheticAuthority{run: rafOfflineEnvelope(), issued: now, notBefore: now, expires: now.Add(time.Minute)}
}
func rafClone(g rafPreparedGrant) rafPreparedGrant {
	g.CIDRs = append([]string(nil), g.CIDRs...)
	g.DescriptorBytes = append([]byte(nil), g.DescriptorBytes...)
	g.IntentBytes = append([]byte(nil), g.IntentBytes...)
	return g
}

type rafCaptureSink struct {
	calls int
	sql   string
	args  []any
	err   error
	rows  int64
}

func (s *rafCaptureSink) Exec(_ context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	s.calls++
	s.sql = q
	s.args = args
	return pgconn.NewCommandTag(fmt.Sprintf("INSERT 0 %d", s.rows)), s.err
}

type rafCaptureQuery struct {
	calls  int
	sql    string
	args   []any
	result string
	err    error
}

func (q *rafCaptureQuery) QueryRow(_ context.Context, s string, args ...any) pgx.Row {
	q.calls++
	q.sql = s
	q.args = args
	return callbackTestRow{values: []any{q.result}, err: q.err}
}

func TestRegistrationAuthorityPreparedRoundTrip(t *testing.T) {
	i := frIntent(t)
	a := rafOfflineAuthority()
	g, err := rafBuildPrepared(i, a, "grant_synthetic_offline")
	if err != nil {
		t.Fatal(err)
	}
	if err = rafValidatePrepared(g, i, a, g.GrantID, a.notBefore); err != nil {
		t.Fatal(err)
	}
	if len(g.args()) != 57 {
		t.Fatal("prepared ABI field count changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sink := &rafCaptureSink{rows: 1}
	if err = rafImportPrepared(ctx, sink, g, i, a, g.GrantID, a.notBefore); err != nil || sink.calls != 1 || sink.sql != rafPreparedInsert || !reflect.DeepEqual(sink.args, g.args()) {
		t.Fatal("exact prepared INSERT not established")
	}
	for n, arg := range g.args() {
		if !reflect.DeepEqual(arg, reflect.ValueOf(g).Field(n).Interface()) {
			t.Fatalf("column/field argument ABI mismatch at %d", n)
		}
	}
	// Returned slices own their storage; mutation does not alter frozen descriptor.
	before := i.descriptor.CanonicalBytes()
	g.DescriptorBytes[0] ^= 1
	g.CIDRs[0] = "0.0.0.0/0"
	if !bytes.Equal(before, i.descriptor.CanonicalBytes()) {
		t.Fatal("builder aliases descriptor storage")
	}
}

func TestRegistrationAuthorityEveryPreparedFieldDeniedBeforeInsert(t *testing.T) {
	i := frIntent(t)
	a := rafOfflineAuthority()
	g, err := rafBuildPrepared(i, a, "grant_synthetic_offline")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	kind := reflect.TypeOf(g)
	if kind.NumField() != 57 {
		t.Fatal("mutation matrix must be reviewed for new prepared fields")
	}
	for n := 0; n < kind.NumField(); n++ {
		t.Run(kind.Field(n).Name, func(t *testing.T) {
			altered := rafClone(g)
			field := reflect.ValueOf(&altered).Elem().Field(n)
			switch field.Kind() {
			case reflect.String:
				field.SetString(field.String() + "x")
			case reflect.Uint32:
				field.SetUint(field.Uint() + 1)
			case reflect.Int64:
				field.SetInt(field.Int() + 1)
			case reflect.Slice:
				if field.Type().Elem().Kind() == reflect.Uint8 {
					field.Index(0).SetUint(field.Index(0).Uint() ^ 1)
				} else {
					field.Index(0).SetString(field.Index(0).String() + "x")
				}
			case reflect.Struct:
				field.Set(reflect.ValueOf(field.Interface().(time.Time).Add(time.Microsecond)))
			default:
				t.Fatal("unreviewed prepared field kind")
			}
			sink := &rafCaptureSink{rows: 1}
			if rafImportPrepared(ctx, sink, altered, i, a, g.GrantID, a.notBefore) == nil || sink.calls != 0 {
				t.Fatal("changed typed field reached issuer INSERT")
			}
		})
	}
}

func TestRegistrationAuthorityEveryCanonicalByteDeniedBeforeInsert(t *testing.T) {
	i := frIntent(t)
	a := rafOfflineAuthority()
	g, err := rafBuildPrepared(i, a, "grant_synthetic_offline")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, which := range []string{"descriptor", "intent"} {
		original := g.DescriptorBytes
		if which == "intent" {
			original = g.IntentBytes
		}
		for n := range original {
			altered := rafClone(g)
			target := altered.DescriptorBytes
			if which == "intent" {
				target = altered.IntentBytes
			}
			target[n] ^= 1
			sink := &rafCaptureSink{rows: 1}
			if rafImportPrepared(ctx, sink, altered, i, a, g.GrantID, a.notBefore) == nil || sink.calls != 0 {
				t.Fatalf("changed %s byte %d reached INSERT", which, n)
			}
		}
	}
	for _, which := range []string{"descriptor", "intent"} {
		for _, mode := range []string{"nil", "truncated", "extended"} {
			altered := rafClone(g)
			target := &altered.DescriptorBytes
			if which == "intent" {
				target = &altered.IntentBytes
			}
			switch mode {
			case "nil":
				*target = nil
			case "truncated":
				*target = (*target)[:len(*target)-1]
			case "extended":
				*target = append(*target, ' ')
			}
			sink := &rafCaptureSink{rows: 1}
			if rafImportPrepared(ctx, sink, altered, i, a, g.GrantID, a.notBefore) == nil || sink.calls != 0 {
				t.Fatalf("%s %s reached INSERT", which, mode)
			}
		}
	}
}

func TestRegistrationAuthorityIndependentIntentAndAuthorityMismatch(t *testing.T) {
	i := frIntent(t)
	a := rafOfflineAuthority()
	g, err := rafBuildPrepared(i, a, "grant_synthetic_offline")
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*registrationIntent){"operation": func(i *registrationIntent) { i.operationID += "_other" }, "actor": func(i *registrationIntent) { i.actor = "actor_other" }, "approval": func(i *registrationIntent) { i.approvalRef = "approval_other" }, "authority": func(i *registrationIntent) { i.authorityID = "authority_other" }, "fence": func(i *registrationIntent) { i.fenceRef = "fence_other" }, "history": func(i *registrationIntent) { i.historyRef = "history_other" }, "plan": func(i *registrationIntent) { i.planIdentity = "" }, "revision": func(i *registrationIntent) { i.expectedRevision = 1 }, "schema": func(i *registrationIntent) { i.schema = "public" }}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			other := i
			change(&other)
			if rafValidatePrepared(g, other, a, g.GrantID, a.notBefore) == nil {
				t.Fatal("independent intent mismatch accepted")
			}
		})
	}
	for _, alter := range []func(*rafSyntheticAuthority){func(a *rafSyntheticAuthority) { a.run.DatabaseOID++ }, func(a *rafSyntheticAuthority) { a.run.SchemaOID++ }, func(a *rafSyntheticAuthority) { a.run.ConsumerOID++ }, func(a *rafSyntheticAuthority) { a.run.IssuerOID++ }, func(a *rafSyntheticAuthority) { a.run.Environment += "_other" }, func(a *rafSyntheticAuthority) { a.run.RecoveryGeneration += "_other" }, func(a *rafSyntheticAuthority) { a.run.RoutineDefinitionSHA256 = rafSHA([]byte("other")) }, func(a *rafSyntheticAuthority) { a.run.SourceManifestSHA256 = rafSHA([]byte("other")) }, func(a *rafSyntheticAuthority) { a.run.RunID += "_other" }} {
		other := a
		alter(&other)
		if rafValidatePrepared(g, i, other, g.GrantID, a.notBefore) == nil {
			t.Fatal("independent environment/principal mismatch accepted")
		}
	}
	if rafValidatePrepared(g, i, a, "grant_synthetic_other", a.notBefore) == nil {
		t.Fatal("grant selection mismatch accepted")
	}
}

func TestRegistrationAuthorityFiniteBoundedTimes(t *testing.T) {
	i := frIntent(t)
	a := rafOfflineAuthority()
	g, err := rafBuildPrepared(i, a, "grant_synthetic_offline")
	if err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{{}, a.notBefore.Add(-time.Microsecond), a.expires, a.expires.Add(time.Microsecond), a.notBefore.Add(time.Nanosecond), a.notBefore.In(time.FixedZone("not-UTC", 0))} {
		if rafValidatePrepared(g, i, a, g.GrantID, now) == nil {
			t.Fatal("invalid decision time accepted")
		}
	}
	for _, alter := range []func(*rafSyntheticAuthority){func(a *rafSyntheticAuthority) { a.issued = time.Time{} }, func(a *rafSyntheticAuthority) { a.issued = a.notBefore.Add(time.Second) }, func(a *rafSyntheticAuthority) { a.expires = a.notBefore }, func(a *rafSyntheticAuthority) { a.expires = a.issued.Add(15*time.Minute + time.Microsecond) }, func(a *rafSyntheticAuthority) { a.expires = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }, func(a *rafSyntheticAuthority) { a.notBefore = a.notBefore.Add(time.Nanosecond) }} {
		other := a
		alter(&other)
		if _, err := rafBuildPrepared(i, other, g.GrantID); err == nil {
			t.Fatal("invalid finite validity interval accepted")
		}
	}
}

func rafEncode(t *testing.T, e rafRunEnvelope) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestRegistrationAuthorityGateBeforeResources(t *testing.T) {
	e := rafOfflineEnvelope()
	reads, connects := 0, 0
	read := func(path string) ([]byte, error) {
		reads++
		switch path {
		case "testdata/registration_authority_boundary.sql":
			return []byte("boundary"), nil
		case "testdata/fresh_registration_registry_candidate.sql":
			return []byte("registry"), nil
		case rafActivitySourcePath:
			return []byte("admission source"), nil
		default:
			return nil, errors.New("unexpected read")
		}
	}
	attempt := func(raw string, environment []string) error {
		_, err := rafPreconnect(raw, environment, read, func() (string, error) { return e.SourceManifestSHA256, nil })
		if err == nil {
			connects++
		}
		return err
	}
	if !errors.Is(attempt("", nil), rafNotConfigured) || reads != 0 || connects != 0 {
		t.Fatal("absent gate touched resource")
	}
	if attempt("{}", nil) == nil || reads != 0 || connects != 0 {
		t.Fatal("incomplete gate touched resource")
	}
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGSERVICE", "PGOPTIONS", "PGPASSFILE", "PGSSLMODE"} {
		if attempt(rafEncode(t, e), []string{key + "=ambient"}) == nil || reads != 0 || connects != 0 {
			t.Fatal("ambient PostgreSQL setting reached file/connect")
		}
	}
	if attempt(rafEncode(t, e), nil) != nil || reads != 3 || connects != 1 {
		t.Fatal("complete exact mocked envelope not accepted")
	}
	reads, connects = 0, 0
	bad := e
	bad.BoundarySHA256 = rafSHA([]byte("changed source"))
	if attempt(rafEncode(t, bad), nil) == nil || connects != 0 {
		t.Fatal("source mismatch reached connect")
	}
	if _, err := rafPreconnect(rafEncode(t, e), nil, read, func() (string, error) { return rafSHA([]byte("changed source tree")), nil }); err == nil {
		t.Fatal("full source tree mismatch accepted")
	}
}

func TestRegistrationAuthorityEnvelopeMalformedAndMissing(t *testing.T) {
	e := rafOfflineEnvelope()
	raw := rafEncode(t, e)
	// Every required field has a nonzero value; deleting one fails before connect.
	fields := map[string]json.RawMessage{}
	if json.Unmarshal([]byte(raw), &fields) != nil {
		t.Fatal("fixture JSON invalid")
	}
	for name := range fields {
		t.Run("missing_"+name, func(t *testing.T) {
			var m map[string]json.RawMessage
			_ = json.Unmarshal([]byte(raw), &m)
			delete(m, name)
			b, _ := json.Marshal(m)
			if _, err := rafParseEnvelope(string(b)); err == nil {
				t.Fatal("missing field accepted")
			}
		})
	}
	for _, bad := range []string{" " + raw, raw + "\n", raw + "{}", strings.Replace(raw, `"format":`, `"unknown":1,"format":`, 1), strings.Replace(raw, `"format":`, `"format":"ignored","format":`, 1), strings.Repeat("x", 16385)} {
		if _, err := rafParseEnvelope(bad); err == nil {
			t.Fatal("noncanonical/duplicate/unknown envelope accepted")
		}
	}
	for _, change := range []func(*rafRunEnvelope){func(e *rafRunEnvelope) { e.Allow = "true" }, func(e *rafRunEnvelope) { e.Host = "localhost" }, func(e *rafRunEnvelope) { e.Port = 543 }, func(e *rafRunEnvelope) { e.Database = "production" }, func(e *rafRunEnvelope) { e.ServerVersion = 180005 }, func(e *rafRunEnvelope) { e.Image = "postgres:18" }, func(e *rafRunEnvelope) { e.ConsumerOID = e.IssuerOID }, func(e *rafRunEnvelope) { e.RunID = "not_synthetic" }, func(e *rafRunEnvelope) { e.SourceManifestSHA256 = strings.Repeat("0", 64) }, func(e *rafRunEnvelope) { e.BootstrapRole = "postgres" }, func(e *rafRunEnvelope) { e.BootstrapOID++ }, func(e *rafRunEnvelope) { e.ActivityGuardOID = e.ConsumerOID }, func(e *rafRunEnvelope) { e.AdmissionSchemaOID = e.SchemaOID }, func(e *rafRunEnvelope) { e.AdmissionFunctionOID = 0 }, func(e *rafRunEnvelope) { e.AdmissionFunctionDefinitionSHA256 = strings.Repeat("0", 64) }, func(e *rafRunEnvelope) { e.AdmissionSHA256 = strings.Repeat("0", 64) }} {
		bad := e
		change(&bad)
		if _, err := rafParseEnvelope(rafEncode(t, bad)); err == nil {
			t.Fatal("invalid exact envelope accepted")
		}
	}
}

func TestRegistrationAuthorityDSNNoFallbackOrRoleSubstitution(t *testing.T) {
	e := rafOfflineEnvelope()
	for _, bad := range []string{"", "host=127.0.0.1", strings.Replace(e.ConsumerDSN, "127.0.0.1", "localhost", 1), strings.Replace(e.ConsumerDSN, "127.0.0.1", "127.0.0.1,127.0.0.2", 1), strings.Replace(e.ConsumerDSN, ":55432", ":5432", 1), strings.Replace(e.ConsumerDSN, rafConsumer, rafOwner, 1), strings.Replace(e.ConsumerDSN, e.Database, "wt_test_otherdatabase", 1), strings.Replace(e.ConsumerDSN, "sslmode=disable", "sslmode=prefer", 1), e.ConsumerDSN + "&options=-crole=wt_registration_lab_owner", e.ConsumerDSN + "#fragment", strings.Replace(e.ConsumerDSN, ":synthetic-not-a-real-secret", "", 1)} {
		if rafValidateDSN(bad, rafConsumer, e) == nil {
			t.Fatal("fallback/foreign/incomplete DSN accepted")
		}
	}
}

func TestRegistrationAuthorityConstantSQLSurfaces(t *testing.T) {
	if rafConsumeSQL != `SELECT wt_synthetic_registry.consume_prepared_fresh_registration_v1($1::text,$2::text)` {
		t.Fatal("consumer surface changed")
	}
	if strings.Count(rafPreparedInsert, "$") != 57 || strings.Contains(rafPreparedInsert, "RETURNING") || strings.Contains(rafPreparedInsert, "ON CONFLICT") {
		t.Fatal("issuer insert grew unreviewed surface")
	}
	for n := 1; n <= 57; n++ {
		token := "$" + strconv.Itoa(n)
		suffix := ","
		if n == 57 {
			suffix = ")"
		}
		if !strings.Contains(rafPreparedInsert, token+suffix) {
			t.Fatalf("missing fixed parameter %d", n)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q := &rafCaptureQuery{result: string(registrationPending)}
	result, err := rafInvoke(ctx, q, "grant_synthetic_example", "op_synthetic_example")
	if err != nil || result != string(registrationPending) || q.calls != 1 || q.sql != rafConsumeSQL || !reflect.DeepEqual(q.args, []any{"grant_synthetic_example", "op_synthetic_example"}) {
		t.Fatal("consumer supplied more than exact grant/operation")
	}
	for _, bad := range []string{"", "x'; SET ROLE owner;--", "a b", strings.Repeat("x", 129)} {
		calls := q.calls
		if _, err := rafInvoke(ctx, q, bad, "op_synthetic_example"); err == nil || q.calls != calls {
			t.Fatal("invalid consumer input touched SQL")
		}
	}
	if _, err := rafInvoke(context.Background(), q, "grant_synthetic_example", "op_synthetic_example"); err == nil {
		t.Fatal("unbounded invocation accepted")
	}
	q.result = "ACTIVE"
	if _, err := rafInvoke(ctx, q, "grant_synthetic_example", "op_synthetic_example"); !errors.Is(err, rafUnknown) {
		t.Fatal("unreviewed result accepted")
	}
}

func TestRegistrationAuthorityOutcomeClassification(t *testing.T) {
	for _, out := range []string{string(registrationPending), string(registrationObserved)} {
		result, err := rafClassify(out, nil, nil, nil)
		if err != nil || result != out {
			t.Fatal("clean confirmed commit lost")
		}
	}
	failure := errors.New("redacted transport")
	for _, c := range []struct {
		out                   string
		body, commit, cleanup error
	}{{string(registrationPending), nil, failure, nil}, {string(registrationPending), nil, nil, failure}, {"", nil, nil, nil}, {"ACTIVE", nil, nil, nil}, {string(registrationPending), failure, nil, failure}} {
		result, err := rafClassify(c.out, c.body, c.commit, c.cleanup)
		if result != "" || !errors.Is(err, rafUnknown) {
			t.Fatal("ambiguous outcome reported success or denial")
		}
	}
	denial := &pgconn.PgError{Code: "42501"}
	result, err := rafClassify("", denial, nil, nil)
	if result != "" || !errors.Is(err, denial) {
		t.Fatal("confirmed rolled-back denial lost")
	}
	for _, c := range []struct {
		err  error
		want string
	}{{denial, "INSUFFICIENT_PRIVILEGE"}, {&pgconn.PgError{Code: "23514"}, "CONSTRAINT_REJECTION"}, {&pgconn.PgError{Code: "40001"}, "SERIALIZATION_FAILURE_NOT_DENIAL"}, {&pgconn.PgError{Code: "57014"}, "CANCELLED_NOT_DENIAL"}, {&pgconn.PgError{Code: "42P01"}, "UNEXPECTED_SQLSTATE"}, {failure, "TRANSPORT_OR_LOCAL_NOT_DENIAL"}, {rafUnknown, "UNKNOWN_STOP"}} {
		if rafErrorClass(c.err) != c.want {
			t.Fatal("error classification overclaimed evidence")
		}
	}
}

func TestRegistrationAuthorityPrivateFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "envelope.json")
	if err := os.WriteFile(path, []byte("private-fixture-only"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := rafReadPrivate(path)
	if err != nil || string(b) != "private-fixture-only" {
		t.Fatal("private file rejected")
	}
	if _, err := rafReadPrivate("relative.json"); err == nil {
		t.Fatal("relative file accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := rafReadPrivate(path); err == nil {
		t.Fatal("broad file permissions accepted")
	}
	_ = os.Chmod(path, 0600)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := rafReadPrivate(path); err == nil {
		t.Fatal("broad parent permissions accepted")
	}
	_ = os.Chmod(dir, 0700)
	link := filepath.Join(dir, "linked.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := rafReadPrivate(link); err == nil {
		t.Fatal("symlink envelope accepted")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 32769), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rafReadPrivate(path); err == nil {
		t.Fatal("oversized envelope accepted")
	}
}

func TestRegistrationAuthoritySourceManifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"b.txt": "second", "a.txt": "first"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	expected := rafSHA([]byte("a.txt\x00" + rafSHA([]byte("first")) + "\n" + "b.txt\x00" + rafSHA([]byte("second")) + "\n"))
	digest, err := rafSourceDigestAt(root, 2, rafSHA([]byte("a.txt\nb.txt\n")))
	if err != nil || digest != expected {
		t.Fatal("source manifest ordering/encoding changed")
	}
	if _, err := rafSourceDigestAt(root, 1, rafSHA([]byte("a.txt\n"))); err == nil {
		t.Fatal("extra source accepted")
	}
	if _, err := rafSourceDigestAt(root, 3, rafSHA([]byte("a.txt\nb.txt\nlinked\n"))); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := rafSourceDigestAt(root, 2, rafSHA([]byte("different.txt\nb.txt\n"))); err == nil {
		t.Fatal("same-count inventory substitution accepted")
	}
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := rafSourceDigestAt(root, 3, rafSHA([]byte("a.txt\nb.txt\nlinked\n"))); err == nil {
		t.Fatal("source symlink accepted")
	}
	_ = os.Remove(filepath.Join(root, "linked"))
	if err := os.WriteFile(filepath.Join(root, "a.txt"), bytes.Repeat([]byte("x"), (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rafSourceDigestAt(root, 2, rafSHA([]byte("a.txt\nb.txt\n"))); err == nil {
		t.Fatal("oversized source accepted")
	}
	if _, err := rafSourceDigestAt(filepath.Dir(root), 2, rafSHA([]byte("a.txt\nb.txt\n"))); err == nil {
		t.Fatal("alternate root accepted")
	}

}

func TestRegistrationAuthorityProvisioningEvidence(t *testing.T) {
	e := rafOfflineEnvelope()
	p := rafProvisioningEvidence{Format: "worktool-registration-provisioning-evidence-v1", RunID: e.RunID, Database: e.Database, Image: e.Image, Host: e.Host, Port: e.Port, CPUs: 2, MemoryBytes: 1 << 30, MemorySwapBytes: 1 << 30, SharedMemoryBytes: 64 << 20, TmpfsBytes: 2 << 30, PGData: "/var/lib/postgresql/18/docker", WAL: "/var/lib/postgresql/18/docker/pg_wal", FreshDatabase: true, BootstrapClosed: true, NoForeignOrPersistentMounts: true, NoHostNetworkOrPrivileged: true, OwnedResourceCleanupPlan: "external-supervisor-cleans-exact-run-owned-resources-once"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	e.ProvisioningEvidenceSHA256 = rafSHA(b)
	if rafValidateProvisioning(b, e) != nil {
		t.Fatal("exact synthetic evidence fixture rejected")
	}
	for n := 0; n < reflect.TypeOf(p).NumField(); n++ {
		altered := p
		field := reflect.ValueOf(&altered).Elem().Field(n)
		switch field.Kind() {
		case reflect.String:
			field.SetString(field.String() + "x")
		case reflect.Bool:
			field.SetBool(!field.Bool())
		case reflect.Int, reflect.Int64:
			field.SetInt(field.Int() + 1)
		case reflect.Uint16:
			field.SetUint(field.Uint() + 1)
		default:
			t.Fatal("unreviewed evidence field")
		}
		bad, _ := json.Marshal(altered)
		changed := e
		changed.ProvisioningEvidenceSHA256 = rafSHA(bad)
		if rafValidateProvisioning(bad, changed) == nil {
			t.Fatalf("changed resource evidence field %s accepted", reflect.TypeOf(p).Field(n).Name)
		}
	}
	if rafValidateProvisioning(append(b, ' '), e) == nil {
		t.Fatal("changed evidence bytes accepted")
	}
}

// Ordinary Go tests may run in a git checkout with any directory name. Only the
// opt-in PG gate scans the strict staged source snapshot. This offline helper
// reads one reviewed manifest file and never walks the checkout or .git.
func rafCheckReviewedManifestAt(root string) error {
	body, err := os.ReadFile(filepath.Join(root, "scripts", "pg-registration-cases.json"))
	if err != nil || len(body) > 65536 {
		return rafInvalid
	}
	var manifest struct {
		Format                string   `json:"format"`
		Scope                 string   `json:"scope"`
		Package               string   `json:"package"`
		Test                  string   `json:"test"`
		Leaves                []string `json:"leaves"`
		SourceFiles           []string `json:"source_files"`
		SourceInventorySHA256 string   `json:"source_inventory_sha256"`
		ScopeNotes            []string `json:"scope_notes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || manifest.Format != "worktool-registration-cases-v1" || manifest.Scope != "focused-registration-consumer-boundary-v1" || manifest.Package != "github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg" || manifest.Test != "TestRegistrationAuthorityIsolatedPostgreSQL" || len(manifest.Leaves) != 46 || len(manifest.SourceFiles) != 342 || !sort.StringsAreSorted(manifest.SourceFiles) || manifest.SourceInventorySHA256 != rafSourceInventorySHA256 || rafSHA([]byte(strings.Join(manifest.SourceFiles, "\n")+"\n")) != rafSourceInventorySHA256 {
		return rafInvalid
	}
	return nil
}

func TestRegistrationAuthorityReviewedManifestWithoutCheckoutScan(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	if err != nil || rafCheckReviewedManifestAt(root) != nil {
		t.Fatal("reviewed source inventory contract mismatch")
	}
	body, err := os.ReadFile(filepath.Join(root, "scripts", "pg-registration-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "worktool-open")
	if os.MkdirAll(filepath.Join(checkout, "scripts"), 0700) != nil || os.Mkdir(filepath.Join(checkout, ".git"), 0700) != nil {
		t.Fatal("temp checkout creation failed")
	}
	if os.WriteFile(filepath.Join(checkout, "scripts", "pg-registration-cases.json"), body, 0600) != nil || os.WriteFile(filepath.Join(checkout, ".git", "not-source"), []byte("not part of the staged source"), 0600) != nil {
		t.Fatal("temp checkout fixture failed")
	}
	if rafCheckReviewedManifestAt(checkout) != nil {
		t.Fatal("ordinary checkout required strict staged-source scan")
	}
	if _, err := rafSourceDigestAt(checkout, 342, rafSourceInventorySHA256); err == nil {
		t.Fatal("opt-in snapshot root rule weakened")
	}
}

func TestRegistrationAuthorityExecutableSelfDigest(t *testing.T) {
	digest, err := rafExecutableDigest()
	if err != nil || !rafDigest(digest) {
		t.Fatal("bounded executable hash unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil || rafSHA(body) != digest {
		t.Fatal("executable self-binding mismatch")
	}
}

func TestRegistrationAuthorityDistinctRollbackControlIntent(t *testing.T) {
	i := frIntent(t)
	other := i
	other.operationID = "op_synthetic_rollback_control"
	frChangeDescriptor(t, &other, func(v *registration.Value) { v.Destination = "dest_synthetic_rollback_control" })
	a := rafOfflineAuthority()
	g, err := rafBuildPrepared(other, a, "grant_synthetic_rollback_control")
	if err != nil || rafValidatePrepared(g, other, a, g.GrantID, a.notBefore) != nil {
		t.Fatal("distinct valid rollback-control intent rejected")
	}
	if rafValidatePrepared(g, i, a, g.GrantID, a.notBefore) == nil {
		t.Fatal("rollback-control intent confused with committed graph intent")
	}
}

// These tests exercise Go's source binding and nullable driver boundary only.
// String checks below are regression fences, not a SQL parser or live PG proof.
type rafActivityCaptureQuery struct {
	calls  int
	sql    string
	args   []any
	values []any
	err    error
}

func (q *rafActivityCaptureQuery) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.calls++
	q.sql, q.args = sql, args
	return callbackTestRow{values: q.values, err: q.err}
}

func TestRegistrationAuthorityActivitySourceBinding(t *testing.T) {
	source, err := os.ReadFile(rafActivitySourcePath)
	if err != nil {
		t.Fatal("reviewed activity source missing")
	}
	body, err := rafActivitySourceBody(source, rafSHA(source))
	if err != nil || body == "" || !strings.Contains(body, "PERFORM pg_catalog.pg_stat_clear_snapshot();") {
		t.Fatal("fixed activity source body unavailable")
	}
	if _, err := rafActivitySourceBody(source, rafSHA(append(append([]byte{}, source...), ' '))); err == nil {
		t.Fatal("activity source digest mismatch accepted")
	}
	for _, invalid := range [][]byte{nil, []byte("SELECT true"), bytes.Replace(source, []byte(rafActivityBodyDelimiter), []byte("$$"), 1), append(append([]byte{}, source...), []byte(rafActivityBodyDelimiter)...), []byte(rafActivityBodyDelimiter + rafActivityBodyDelimiter)} {
		if _, err := rafActivitySourceBody(invalid, rafSHA(invalid)); err == nil {
			t.Fatal("missing, ambiguous or empty fixed source body accepted")
		}
	}
	for _, required := range []string{"activity AS MATERIALIZED", "a.allowed IS NOT TRUE", "RETURN admitted IS TRUE;", "a.usesysid = 10", "a.client_addr IS NULL", "a.client_hostname IS NULL", "a.client_port IS NULL", "a.leader_pid IS NULL", "session_user = 'wt_registration_lab_owner'", "current_user = 'wt_registration_lab_activity_guard'", "AND d.datdba = o.oid", "ELSE false"} {
		if !strings.Contains(body, required) {
			t.Fatalf("activity source lost fail-closed condition %q", required)
		}
	}
	if strings.Count(body, "FROM pg_catalog.pg_stat_activity") != 1 || strings.Index(body, "pg_stat_clear_snapshot()") > strings.Index(body, "FROM pg_catalog.pg_stat_activity") {
		t.Fatal("activity source does not clear snapshot before its one materialized activity read")
	}
}

func TestRegistrationAuthorityActivityCatalogIndependentBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e := rafOfflineEnvelope()
	q := &rafActivityCaptureQuery{values: []any{true, "fixed source body", "admission definition"}}
	if rafCheckActivityCatalog(ctx, q, e, "fixed source body") != nil || q.calls != 1 || q.sql != rafActivityCatalogSQL ||
		!reflect.DeepEqual(q.args, []any{e.BootstrapRole, e.BootstrapOID, e.ActivityGuardOID, e.AdmissionSchemaOID, e.AdmissionFunctionOID, e.Database, e.DatabaseOID}) {
		t.Fatal("catalog evidence was not independently bound to the reviewed envelope")
	}
	for _, values := range [][]any{{false, "fixed source body", "admission definition"}, {nil, "fixed source body", "admission definition"}, {true, "replaced source body", "admission definition"}, {true, "fixed source body", "replaced definition"}, {true, nil, "admission definition"}, {true, "fixed source body", nil}} {
		q.values = values
		if rafCheckActivityCatalog(ctx, q, e, "fixed source body") == nil {
			t.Fatal("untrusted catalog metadata/body was adopted as expected source")
		}
	}
	q.values = []any{true, "fixed source body", "admission definition"}
	q.err = errors.New("redacted transport")
	if rafCheckActivityCatalog(ctx, q, e, "fixed source body") == nil {
		t.Fatal("catalog read error accepted")
	}
	q.err = nil
	before := q.calls
	if rafCheckActivityCatalog(context.Background(), q, e, "fixed source body") == nil || q.calls != before || rafCheckActivityCatalog(ctx, q, e, "") == nil || q.calls != before {
		t.Fatal("unbounded or source-free catalog verification reached database")
	}
	for _, required := range []string{
		"WITH RECURSIVE", "JOIN actual_members m ON m.member=r.roleid", "SELECT * FROM actual_members EXCEPT SELECT * FROM expected_members",
		"pg_catalog.aclexplode(COALESCE(ns.nspacl,pg_catalog.acldefault('n',ns.nspowner)))",
		"pg_catalog.aclexplode(COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner)))",
		"SELECT * FROM schema_acl EXCEPT SELECT * FROM expected_schema_acl", "SELECT * FROM function_acl EXCEPT SELECT * FROM expected_function_acl",
		"NOT EXISTS(SELECT 1 FROM pg_catalog.pg_default_acl)", "NOT EXISTS(SELECT 1 FROM pg_catalog.pg_db_role_setting)",
		"oid=3373 FROM monitor", "oid=3375 FROM stats", "oid=3374 FROM pg_catalog.pg_roles", "oid=3377 FROM pg_catalog.pg_roles",
		"rolvaliduntil IS NOT NULL OR rolconfig IS NOT NULL", "p.procost=100 AND p.prorows=0 AND p.probin IS NULL",
		"FROM builtin_roles WHERE rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls",
		"NOT pg_catalog.has_schema_privilege((SELECT oid FROM owner_role),$4::oid,'CREATE')",
		"p.prosecdef AND NOT p.proleakproof", "p.provolatile='v' AND p.proparallel='u'", "p.proconfig=ARRAY['search_path=pg_catalog, pg_temp']::text[]",
		") IS TRUE,(SELECT prosrc FROM helper)",
	} {
		if !strings.Contains(rafActivityCatalogSQL, required) {
			t.Fatalf("independent catalog guard lost required check %q", required)
		}
	}
	if strings.Contains(rafActivityCatalogSQL, "isolated_owner_session_v1()") {
		t.Fatal("catalog verification invoked the function it was meant to authenticate")
	}
}

func TestRegistrationAuthorityActivityAdmissionNullFailsClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	yes, no := true, false
	q := &rafActivityCaptureQuery{values: []any{&yes}}
	admitted, err := rafReadActivityAdmission(ctx, q)
	if err != nil || !admitted || q.calls != 1 || q.sql != rafActivityAdmissionSQL || len(q.args) != 0 {
		t.Fatal("fixed no-argument activity boundary changed")
	}
	q.values = []any{&no}
	if admitted, err := rafReadActivityAdmission(ctx, q); admitted || err != nil {
		t.Fatal("explicit synthetic negative was not preserved")
	}
	q.values = []any{nil}
	if admitted, err := rafReadActivityAdmission(ctx, q); admitted || err == nil {
		t.Fatal("SQL NULL accepted as admission or confirmed negative")
	}
	q.values = []any{&yes}
	q.err = errors.New("redacted transport")
	if admitted, err := rafReadActivityAdmission(ctx, q); admitted || err == nil {
		t.Fatal("failed read accepted")
	}
	before := q.calls
	if _, err := rafReadActivityAdmission(context.Background(), q); err == nil || q.calls != before {
		t.Fatal("unbounded activity call reached database")
	}
	cancel()
	if _, err := rafReadActivityAdmission(ctx, q); err == nil || q.calls != before {
		t.Fatal("cancelled activity call reached database")
	}
}

func TestRegistrationAuthorityRollbackDiagnosticErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "NONE"}, {rafInvalid, "LOCAL_INVALID"}, {rafUnknown, "UNKNOWN_STOP"},
		{&pgconn.PgError{Code: "42501"}, "INSUFFICIENT_PRIVILEGE"},
		{&pgconn.PgError{Code: "23514"}, "CONSTRAINT_REJECTION"},
		{&pgconn.PgError{Code: "40001"}, "SERIALIZATION_FAILURE_NOT_DENIAL"},
		{&pgconn.PgError{Code: "57014"}, "CANCELLED_NOT_DENIAL"},
		{&pgconn.PgError{Code: "42702"}, "SQL_AMBIGUOUS_REFERENCE"},
		{&pgconn.PgError{Code: "42703"}, "SQL_UNDEFINED_COLUMN"},
		{&pgconn.PgError{Code: "42883"}, "SQL_UNDEFINED_FUNCTION"},
		{&pgconn.PgError{Code: "42P01"}, "SQL_UNDEFINED_TABLE"},
		{&pgconn.PgError{Code: "42804"}, "SQL_DATATYPE_MISMATCH"},
		{&pgconn.PgError{Code: "0A000"}, "SQL_FEATURE_NOT_SUPPORTED"},
		{&pgconn.PgError{Code: "55P03"}, "SQL_LOCK_NOT_AVAILABLE"},
		{&pgconn.PgError{Code: "40P01"}, "SQL_DEADLOCK"},
		{&pgconn.PgError{Code: "23505"}, "SQL_UNIQUE_VIOLATION"},
		{&pgconn.PgError{Code: "23503"}, "SQL_FOREIGN_KEY_VIOLATION"},
		{&pgconn.PgError{Code: "XXXXX", Message: "42501 secret SQL text"}, "UNEXPECTED_SQLSTATE"},
		{errors.New("42501 secret SQL text"), "TRANSPORT_OR_LOCAL_NOT_DENIAL"},
		{context.DeadlineExceeded, "TRANSPORT_OR_LOCAL_NOT_DENIAL"},
		{errors.Join(rafUnknown, &pgconn.PgError{Code: "42501"}), "TRANSPORT_OR_LOCAL_NOT_DENIAL"},
		{errors.Join(rafUnknown, &pgconn.PgError{Code: "42702"}), "TRANSPORT_OR_LOCAL_NOT_DENIAL"},
	} {
		if got := rafRollbackDiagnosticClass(tc.err); got != tc.want || !rafRollbackDiagnosticClassValid(got) {
			t.Fatal("rollback diagnostic class did not stay within the fixed error taxonomy")
		}
	}
	// The existing classifier used by other leaves must not gain new behavior.
	if rafErrorClass(&pgconn.PgError{Code: "42702"}) != "UNEXPECTED_SQLSTATE" || rafErrorClass(rafInvalid) != "TRANSPORT_OR_LOCAL_NOT_DENIAL" {
		t.Fatal("rollback-only diagnostic changed the shared classifier")
	}
	secret := "SQL params database password data must never appear"
	if got := rafRollbackDiagnosticClass(&pgconn.PgError{Code: "42702", Message: secret, Detail: secret, Hint: secret, Where: secret, InternalQuery: secret}); got != "SQL_AMBIGUOUS_REFERENCE" {
		t.Fatal("diagnostic classification depended on error text")
	}
}

func TestRegistrationAuthorityRollbackDiagnosticFixedMarker(t *testing.T) {
	d := rafRollbackDiagnostic{stage: "INVOKE", errorClass: "SQL_AMBIGUOUS_REFERENCE", invokeClass: "SQL_AMBIGUOUS_REFERENCE", rollback: "UNCONFIRMED", rollbackClass: "TRANSPORT_OR_LOCAL_NOT_DENIAL"}
	want := "WT_REGISTRATION_ROLLBACK_DIAG_V1 stage=INVOKE error_class=SQL_AMBIGUOUS_REFERENCE invoke_class=SQL_AMBIGUOUS_REFERENCE rollback=UNCONFIRMED rollback_class=TRANSPORT_OR_LOCAL_NOT_DENIAL"
	if d.line() != want {
		t.Fatal("fixed cross-language diagnostic ABI changed")
	}
	calls := 0
	logf := func(format string, args ...any) {
		calls++
		if format != "%s" || len(args) != 1 || args[0] != want {
			t.Fatal("diagnostic emitted anything except the fixed marker")
		}
	}
	d.report(false, logf)
	if calls != 0 {
		t.Fatal("successful leaf emitted a diagnostic")
	}
	d.report(true, logf)
	if calls != 1 {
		t.Fatal("failed leaf did not emit exactly one marker per report")
	}
	for field := 0; field < reflect.TypeOf(d).NumField(); field++ {
		for _, value := range []string{"", "42501", "SQL text secret", "NONE\nWT_REGISTRATION_ROLLBACK_DIAG_V1", "NONE extra=value"} {
			invalid := d
			switch field {
			case 0:
				invalid.stage = value
			case 1:
				invalid.errorClass = value
			case 2:
				invalid.invokeClass = value
			case 3:
				invalid.rollback = value
			case 4:
				invalid.rollbackClass = value
			}
			if invalid.line() != "" {
				t.Fatal("non-enum diagnostic value leaked")
			}
			invalid.report(true, logf)
			if calls != 1 {
				t.Fatal("malformed diagnostic reached the output")
			}
		}
	}
}

func TestRegistrationAuthorityRollbackDiagnosticPreservesSequence(t *testing.T) {
	body, err := os.ReadFile("registration_authority_fixture_test.go")
	if err != nil {
		t.Fatal("fixture source unavailable for offline diagnostic scope check")
	}
	source := string(body)
	start := strings.Index(source, "func rafOtherGrantRollback(")
	end := strings.Index(source, "\nfunc TestRegistrationAuthorityIsolatedPostgreSQL(")
	if start < 0 || end <= start {
		t.Fatal("rollback-control source boundary changed")
	}
	leaf := source[start:end]
	sequence := []string{"rafBuildPrepared(i, a, grant)", "rafImportPrepared(f.ctx, f.issuer, g, i, a, grant, now)", "rafSeedControl(f.ctx, f.owner, g)", "rafSnapshot(t, f)", "f.consumer.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})", "rafInvoke(f.ctx, tx, grant, i.operationID)", "context.WithTimeout(context.Background(), 3*time.Second)", "tx.Rollback(clean)", "cancel()", "rafSnapshot(t, f)"}
	rest := leaf
	for _, call := range sequence {
		at := strings.Index(rest, call)
		if at < 0 {
			t.Fatal("rollback-control resource sequence changed")
		}
		rest = rest[at+len(call):]
	}
	for _, call := range sequence {
		want := 1
		if call == "rafSnapshot(t, f)" {
			want = 2
		}
		if strings.Count(leaf, call) != want {
			t.Fatal("rollback diagnostic added a resource call or retry")
		}
	}
	if !strings.Contains(leaf, "if body != nil || result != string(registrationPending) || cleanup != nil {") ||
		!strings.Contains(leaf, "if rafSnapshot(t, f) != before {") ||
		strings.Count(leaf, "diagnostic.report(t.Failed(), t.Logf)") != 1 ||
		!strings.Contains(leaf, "defer func() { diagnostic.report(t.Failed(), t.Logf) }()") ||
		strings.Contains(leaf, ".Commit(") || strings.Contains(leaf, ".Exec(") || strings.Contains(leaf, ".QueryRow(") {
		t.Fatal("diagnostic changed an assertion, emission boundary or SQL surface")
	}
}

// Explicitly synthetic logging specimen for the cross-language collector tests.
// This is not the isolated test/leaf and is never admissible as run evidence.
// It uses Go's actual testing logger without resources, SQL or a failed test.
func TestRegistrationAuthorityRollbackDiagnosticLogSpecimen(t *testing.T) {
	d := rafRollbackDiagnostic{stage: "SNAPSHOT_BEFORE", errorClass: "UNCLASSIFIED", invokeClass: "NOT_ATTEMPTED", rollback: "NOT_ATTEMPTED", rollbackClass: "NOT_ATTEMPTED"}
	d.report(true, t.Logf)
}

// Deliberately non-comparable dynamic error value. None of its methods is safe
// to call; classification must use only direct type/sentinel inspection.
type rafPanicDiagnosticError []string

func (rafPanicDiagnosticError) Error() string { panic("diagnostic called Error") }
func (rafPanicDiagnosticError) Is(error) bool { panic("diagnostic called Is") }
func (rafPanicDiagnosticError) As(any) bool   { panic("diagnostic called As") }
func (rafPanicDiagnosticError) Unwrap() error { panic("diagnostic called Unwrap") }

func TestRegistrationAuthorityRollbackDiagnosticNeverInvokesErrorMethods(t *testing.T) {
	var typedNil *pgconn.PgError
	for _, hostile := range []error{rafPanicDiagnosticError{"private SQL"}, typedNil} {
		rollbackReached := false
		class := rafRollbackDiagnosticClass(hostile)
		// This local marker represents the immediately following original rollback
		// step. No DB/driver operation is executed by this offline test.
		rollbackReached = true
		if !rollbackReached || class != "TRANSPORT_OR_LOCAL_NOT_DENIAL" {
			t.Fatal("diagnostic classification interrupted the rollback path")
		}
	}
}
