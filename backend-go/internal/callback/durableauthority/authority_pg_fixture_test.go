//go:build authority_pg

package durableauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Test-only embedding. No production installer or additional schema copy.
//
//go:embed testdata/schema_candidate.sql
var authoritySchema []byte

const authoritySchemaHash = "f029a454b01705e2f02479f4280285f0aeeab57824c3d2d259f4bb27ef375294"
const authorityImage = "postgres@sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c"
const authorityRun = "^TestPGAuthorityA(0[1-9]|1[0-6])$"
const ns = "wt_synthetic_callback_authority."

type authorityInput struct {
	Version  int    `json:"version"`
	Binary   string `json:"binary_sha256"`
	Nonce    string `json:"run_nonce"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Schema   string `json:"schema_sha256"`
	Image    string `json:"image"`
	Network  string `json:"network_mode"`
	PID      string `json:"pid_mode"`
	CPU      int64  `json:"cpu_nanos"`
	Memory   int64  `json:"memory_bytes"`
	Swap     int64  `json:"memory_swap_bytes"`
	Shm      int64  `json:"shm_bytes"`
	Tmpfs    int64  `json:"tmpfs_bytes"`
	Isolated bool   `json:"no_mounts_or_ports"`
	Owned    bool   `json:"run_owned"`
}

func authorityValidate(in authorityInput, env []string) error {
	bad := errors.New("AUTHORITY_PREFLIGHT_REJECTED")
	if in.Version != 1 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(in.Binary) {
		return bad
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(in.Nonce) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(in.Password) || in.Database != "wt_authority_test_"+in.Nonce || in.User != "wt_authority_owner_"+in.Nonce {
		return bad
	}
	if in.Host != "127.0.0.1" || in.Port != 5432 || in.Schema != authoritySchemaHash || in.Image != authorityImage || in.Network != "none" || in.PID != "" || in.CPU != 2000000000 || in.Memory != 1073741824 || in.Swap != in.Memory || in.Shm != 67108864 || in.Tmpfs != 2147483648 || !in.Isolated || !in.Owned {
		return bad
	}
	allowed := map[string]bool{"WORKTOOL_AUTHORITY_PG_STAGE_A": true, "WORKTOOL_AUTHORITY_PG_NONCE": true, "GOMAXPROCS": true, "TZ": true}
	for _, e := range env {
		k, _, ok := strings.Cut(e, "=")
		if !ok || !allowed[k] {
			return bad
		}
	}
	sum := sha256.Sum256(authoritySchema)
	if hex.EncodeToString(sum[:]) != in.Schema {
		return bad
	}
	return nil
}
func authorityDecode(r io.Reader) (authorityInput, error) {
	var in authorityInput
	raw, e := io.ReadAll(io.LimitReader(r, 8193))
	if e != nil || len(raw) > 8192 {
		return in, errors.New("INPUT_REJECTED")
	}
	// Reject duplicate and missing keys, including zero-valued pid_mode.
	keys := json.NewDecoder(bytes.NewReader(raw))
	token, err := keys.Token()
	if err != nil || token != json.Delim('{') {
		return in, errors.New("INPUT_REJECTED")
	}
	seen := map[string]bool{}
	for keys.More() {
		tok, err := keys.Token()
		key, ok := tok.(string)
		if err != nil || !ok || seen[key] {
			return in, errors.New("INPUT_REJECTED")
		}
		seen[key] = true
		var value json.RawMessage
		if keys.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return in, errors.New("INPUT_REJECTED")
		}
	}
	if _, err = keys.Token(); err != nil || len(seen) != reflect.TypeOf(in).NumField() {
		return in, errors.New("INPUT_REJECTED")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		return in, errors.New("INPUT_REJECTED")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return in, errors.New("INPUT_REJECTED")
	}
	return in, nil
}

var af *authorityFixture

type authorityFixture struct {
	pools     [3]*pgxpool.Pool
	ctx       context.Context
	cancel    context.CancelFunc
	next      int
	installed bool
	failed    bool
}

func TestMain(m *testing.M) {
	if os.Getenv("WORKTOOL_AUTHORITY_PG_STAGE_A") != "1" {
		os.Exit(m.Run())
	}
	// Exact selection excludes legacy cwd-sensitive tests. Gate precedes stdin and sockets.
	required := map[string]bool{"-test.run=" + authorityRun: false, "-test.count=1": false, "-test.timeout=10m0s": false, "-test.v=test2json": false}
	for _, a := range os.Args[1:] {
		if _, ok := required[a]; !ok {
			fmt.Println("AUTHORITY_PREFLIGHT_REJECTED")
			os.Exit(2)
		}
		if required[a] {
			os.Exit(2)
		}
		required[a] = true
	}
	for _, ok := range required {
		if !ok {
			os.Exit(2)
		}
	}
	in, e := authorityDecode(os.Stdin)
	if e != nil || authorityValidate(in, os.Environ()) != nil || os.Getenv("WORKTOOL_AUTHORITY_PG_NONCE") != in.Nonce || os.Getenv("GOMAXPROCS") != "2" || os.Getenv("TZ") != "UTC" {
		fmt.Println("AUTHORITY_PREFLIGHT_REJECTED")
		os.Exit(2)
	}
	path, e := os.Executable()
	if e != nil {
		os.Exit(2)
	}
	file, e := os.Open(path)
	if e != nil {
		os.Exit(2)
	}
	binary, e := io.ReadAll(io.LimitReader(file, 64*1024*1024+1))
	closeErr := file.Close()
	if e != nil || closeErr != nil || len(binary) > 64*1024*1024 {
		os.Exit(2)
	}
	digest := sha256.Sum256(binary)
	if hex.EncodeToString(digest[:]) != in.Binary {
		os.Exit(2)
	}
	f, e := authorityOpen(in)
	if e != nil {
		fmt.Println("AUTHORITY_TARGET_REJECTED")
		os.Exit(2)
	}
	af = f
	code := m.Run()
	if !f.close() {
		code = 1
		fmt.Println("AUTHORITY_CLEANUP_UNCONFIRMED")
	}
	os.Exit(code)
}
func authorityOpen(in authorityInput) (*authorityFixture, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	f := &authorityFixture{ctx: ctx, cancel: cancel}
	fail := func() (*authorityFixture, error) {
		cancel()
		if !authorityClosePools(f.pools) {
			fmt.Println("AUTHORITY_CLEANUP_UNCONFIRMED")
		}
		return nil, errors.New("TARGET_REJECTED")
	}
	for n := range f.pools {
		c, e := pgxpool.ParseConfig("host=127.0.0.1 port=5432 dbname=" + in.Database + " user=" + in.User + " password=" + in.Password + " sslmode=disable connect_timeout=3")
		if e != nil {
			return fail()
		}
		if c.ConnConfig.Host != in.Host || c.ConnConfig.Port != in.Port || c.ConnConfig.Database != in.Database || c.ConnConfig.User != in.User || c.ConnConfig.Password != in.Password || c.ConnConfig.TLSConfig != nil || len(c.ConnConfig.Fallbacks) != 0 {
			return fail()
		}
		c.MaxConns = 1
		c.MinConns = 0
		c.ConnConfig.RuntimeParams = map[string]string{"application_name": "authority_" + in.Nonce, "statement_timeout": "5000", "lock_timeout": "1000", "idle_in_transaction_session_timeout": "5000"}
		// Deadline-only cancellation cannot open an auxiliary CancelRequest connection.
		c.ConnConfig.BuildContextWatcherHandler = func(p *pgconn.PgConn) ctxwatch.Handler { return &pgconn.DeadlineContextWatcherHandler{Conn: p.Conn()} }
		f.pools[n], e = pgxpool.NewWithConfig(ctx, c)
		if e != nil {
			return fail()
		}
	}
	var db, user, addr, owner, version, listen, data string
	var port, ver, max int
	var roleOK bool
	var unexpected int
	e := f.pools[2].QueryRow(ctx, `SELECT current_database(),current_user,inet_server_addr()::text,inet_server_port(),(SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=current_database()),current_setting('server_version_num')::int,version(),current_setting('listen_addresses'),current_setting('max_connections')::int,current_setting('data_directory'),(SELECT rolsuper AND rolcreatedb AND rolcreaterole AND rolbypassrls AND rolcanlogin FROM pg_roles WHERE rolname=current_user),(SELECT count(*) FROM pg_roles WHERE rolname !~ '^pg_' AND rolname<>current_user)`).Scan(&db, &user, &addr, &port, &owner, &ver, &version, &listen, &max, &data, &roleOK, &unexpected)
	if e != nil || db != in.Database || user != in.User || owner != in.User || addr != "127.0.0.1/32" && addr != "127.0.0.1" || port != 5432 || ver != 180006 || !strings.HasPrefix(version, "PostgreSQL 18.6 ") || listen != "127.0.0.1" || max != 10 || data != "/var/lib/postgresql/18/docker" || !roleOK || unexpected != 0 {
		return fail()
	}
	var st, lt, it string
	if f.pools[2].QueryRow(ctx, `SELECT current_setting('statement_timeout'),current_setting('lock_timeout'),current_setting('idle_in_transaction_session_timeout')`).Scan(&st, &lt, &it) != nil || st != "5s" || lt != "1s" || it != "5s" {
		return fail()
	}
	var unexpectedObjects int
	if f.pools[2].QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_namespace WHERE nspname NOT IN ('pg_catalog','information_schema','public') AND nspname !~ '^pg_(toast|temp)')+(SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace)`).Scan(&unexpectedObjects) != nil || unexpectedObjects != 0 {
		return fail()
	}
	var exists bool
	if f.pools[2].QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_namespace WHERE nspname='wt_synthetic_callback_authority')`).Scan(&exists) != nil || exists {
		return fail()
	}
	return f, nil
}
func (f *authorityFixture) close() bool {
	ok := true
	checkctx, stop := context.WithTimeout(context.Background(), time.Second)
	var busy, count int
	if f.pools[2].QueryRow(checkctx, `SELECT count(*) FILTER (WHERE pid<>pg_backend_pid() AND state IN ('active','idle in transaction','idle in transaction (aborted)')),count(*) FROM pg_stat_activity WHERE datname=current_database()`).Scan(&busy, &count) != nil || busy != 0 || count > 3 {
		ok = false
	}
	stop()
	for _, p := range f.pools {
		if p.Stat().AcquiredConns() != 0 {
			ok = false
		}
	}
	f.cancel()
	if !authorityClosePools(f.pools) {
		ok = false
	}
	return ok
}
func fixture(t *testing.T) *authorityFixture {
	t.Helper()
	if af == nil {
		t.Skip("NOT_RUN: dedicated Stage A opt-in absent")
	}
	if af.failed {
		t.Fatal("PREREQUISITE_FAILED")
	}
	t.Cleanup(func() {
		if t.Failed() {
			af.failed = true
		}
	})
	return af
}
func (f *authorityFixture) exec(t *testing.T, s string, args ...any) {
	t.Helper()
	if f.failed {
		t.Fatal("PREREQUISITE_FAILED")
	}
	before := f.snapshot(t)
	if tag, e := f.pools[2].Exec(f.ctx, s, args...); e != nil || tag.RowsAffected() != 1 {
		t.Fatal("SQL_EXEC_FAILED")
	}
	after := f.snapshot(t)
	table, mode := 0, "insert"
	switch s {
	case registryInsert:
		table = 0
	case insertIntentSQL:
		table = 1
	case insertEventSQL:
		table = 2
	case haltSQL:
		table = 1
		mode = "update"
	default:
		t.Fatal("UNREVIEWED_DIRECT_WRITE")
	}
	if len(args) == 0 {
		t.Fatal("MISSING_WRITE_ID")
	}
	id, ok := args[0].(string)
	if !ok {
		t.Fatal("INVALID_WRITE_ID")
	}
	graphDelta(t, before, after, map[string]string{graphKey(table, id): mode})
}
func (f *authorityFixture) check(t *testing.T) {
	t.Helper()
	var n, r, e, busy int
	if f.pools[2].QueryRow(f.ctx, `SELECT (SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()),(SELECT count(*) FROM `+ns+`registry),(SELECT count(*) FROM `+ns+`events),(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND state IN ('active','idle in transaction','idle in transaction (aborted)'))`).Scan(&n, &r, &e, &busy) != nil || n > 3 || r > 128 || e > 512 || busy != 0 {
		t.Fatal("RESOURCE_OR_SESSION_BOUND_FAILED")
	}
	for _, p := range f.pools {
		if p.Stat().AcquiredConns() != 0 {
			t.Fatal("BORROWED_SESSION_REMAINS")
		}
	}
}
func (f *authorityFixture) snapshot(t *testing.T) string {
	t.Helper()
	var s string
	e := f.pools[2].QueryRow(f.ctx, `SELECT json_build_array((SELECT coalesce(json_agg(x ORDER BY scope_id),'[]') FROM (SELECT r.*,xmin::text AS tuple_xmin FROM `+ns+`registry r)x),(SELECT coalesce(json_agg(x ORDER BY scope_id),'[]') FROM (SELECT l.*,xmin::text AS tuple_xmin FROM `+ns+`ledger l)x),(SELECT coalesce(json_agg(x ORDER BY operation_id),'[]') FROM (SELECT e.*,xmin::text AS tuple_xmin FROM `+ns+`events e)x))::text`).Scan(&s)
	if e != nil || len(s) > 15000000 {
		t.Fatal("SNAPSHOT_FAILED")
	}
	return s
}
func (f *authorityFixture) unchanged(t *testing.T, before string) {
	t.Helper()
	if f.snapshot(t) != before {
		t.Fatal("GRAPH_CHANGED")
	}
	f.check(t)
}
func sqlstate(e error) string {
	var p *pgconn.PgError
	if errors.As(e, &p) {
		return p.Code
	}
	return ""
}
func (f *authorityFixture) reject(t *testing.T, want, s string, args ...any) {
	t.Helper()
	before := f.snapshot(t)
	tx, e := f.pools[0].Begin(f.ctx)
	if e != nil {
		t.Fatal("BEGIN_FAILED")
	}
	defer tx.Rollback(f.ctx)
	_, e = tx.Exec(f.ctx, s, args...)
	if sqlstate(e) != want {
		t.Fatal("SQLSTATE_MISMATCH")
	}
	if tx.Rollback(f.ctx) != nil {
		t.Fatal("ROLLBACK_FAILED")
	}
	f.unchanged(t, before)
	t.Log("AUTHORITY_SQLSTATE " + want)
}
func (f *authorityFixture) binding(t *testing.T) Binding {
	t.Helper()
	p, ds, b := config(t)
	f.next++
	if f.next > 128 {
		t.Fatal("SCOPE_BUDGET")
	}
	b.identity.Scope = fmt.Sprintf("scope_%03d", f.next)
	b, e := Bind(p, ds, b.identity)
	if e != nil {
		t.Fatal("BIND_FAILED")
	}
	return b
}

const registryInsert = `INSERT INTO wt_synthetic_callback_authority.registry(scope_id,allocation_id,instance_id,boot_id,epoch_id,provenance_ref,registry_revision,plan_identity,binding_identity,plan_raw,descriptors_raw) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

func registryArgs(b Binding) []any {
	i := b.identity
	return []any{i.Scope, i.Allocation, i.Instance, i.Boot, i.Epoch, i.ProvenanceRef, i.Revision, b.planIdentity, b.digest, []byte(testManifest), b.descriptors}
}
func (f *authorityFixture) seed(t *testing.T, ledger bool) Binding {
	b := f.binding(t)
	f.exec(t, registryInsert, registryArgs(b)...)
	if ledger {
		f.exec(t, insertIntentSQL, bindingArgs(b)...)
	}
	return b
}
func (f *authorityFixture) adapter(t *testing.T, n int, h *authorityHook) *Adapter {
	t.Helper()
	var pool pg.Pool = pg.PoolAdapter{Pool: f.pools[n]}
	if h != nil {
		pool = &authorityPool{pool, h}
	}
	a, e := New(pool, pg.Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: 4 * time.Second, Commit: time.Second, Cleanup: time.Second})
	if e != nil {
		t.Fatal("ADAPTER_FAILED")
	}
	return a
}
func outcome(t *testing.T, ev Evidence, e error, class string, want error) {
	t.Helper()
	if want != nil {
		if !errors.Is(e, want) || ev != (Evidence{}) {
			t.Fatal("FAILURE_OR_EMPTY_EVIDENCE_MISMATCH")
		}
		return
	}
	if e != nil || ev.Classification != class {
		t.Fatal("CLASSIFICATION_MISMATCH")
	}
}
func leaf(t *testing.T, name string, fn func(*testing.T)) {
	t.Run(name, func(t *testing.T) {
		if af != nil {
			t.Cleanup(func() {
				if t.Failed() {
					af.failed = true
				}
			})
			if af.failed {
				t.Fatal("PREREQUISITE_FAILED")
			}
		}
		if af != nil && af.installed {
			af.check(t)
		}
		fn(t)
		if af != nil && af.installed {
			af.check(t)
		}
		if !t.Failed() {
			t.Log("AUTHORITY_EVIDENCE " + t.Name())
		}
	})
}

// Hooks always forward actual pgx statements. They are synthetic driver-boundary
// qualification, not transport faults, server crash, or durable recovery evidence.
type authorityHook struct {
	before  func(context.Context, string) error
	after   func(context.Context, string, error) error
	release func()
	pid     uint32
	state   string
}
type authorityPool struct {
	raw pg.Pool
	h   *authorityHook
}

func (p *authorityPool) Acquire(ctx context.Context) (pg.Connection, error) {
	c, e := p.raw.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	return &authorityConn{c, p.h}, nil
}

type authorityConn struct {
	pg.Connection
	h *authorityHook
}

func (c *authorityConn) BeginTx(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	tx, e := c.Connection.BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	c.h.pid = tx.Conn().PgConn().PID()
	var iso string
	if tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&iso) != nil || iso != "read committed" {
		tx.Rollback(ctx)
		return nil, errors.New("ISOLATION_MISMATCH")
	}
	return &authorityTx{tx, c.h}, nil
}
func (c *authorityConn) Release() {
	if c.h.release != nil {
		c.h.release()
	}
	c.Connection.Release()
}

type authorityTx struct {
	pgx.Tx
	h *authorityHook
}

func (h *authorityHook) pre(ctx context.Context, s string) error {
	if h.before != nil {
		return h.before(ctx, s)
	}
	return nil
}
func (h *authorityHook) post(ctx context.Context, s string, e error) error {
	if code := sqlstate(e); code != "" {
		h.state = code
	}
	if h.after != nil {
		return h.after(ctx, s, e)
	}
	return e
}
func (tx *authorityTx) Exec(ctx context.Context, s string, a ...any) (pgconn.CommandTag, error) {
	if e := tx.h.pre(ctx, s); e != nil {
		return pgconn.CommandTag{}, e
	}
	v, e := tx.Tx.Exec(ctx, s, a...)
	return v, tx.h.post(ctx, s, e)
}
func (tx *authorityTx) QueryRow(ctx context.Context, s string, a ...any) pgx.Row {
	if e := tx.h.pre(ctx, s); e != nil {
		return authorityRow{err: e}
	}
	return authorityRow{row: tx.Tx.QueryRow(ctx, s, a...), ctx: ctx, s: s, h: tx.h}
}

type authorityRow struct {
	row pgx.Row
	ctx context.Context
	s   string
	h   *authorityHook
	err error
}

func (r authorityRow) Scan(a ...any) error {
	if r.err != nil {
		return r.err
	}
	return r.h.post(r.ctx, r.s, r.row.Scan(a...))
}
func (tx *authorityTx) Commit(ctx context.Context) error {
	if e := tx.h.pre(ctx, "COMMIT"); e != nil {
		return e
	}
	return tx.h.post(ctx, "COMMIT", tx.Tx.Commit(ctx))
}
func (tx *authorityTx) Rollback(ctx context.Context) error {
	return tx.h.post(ctx, "ROLLBACK", tx.Tx.Rollback(ctx))
}

type authorityGate struct {
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
	opened  sync.Once
}

func gate() *authorityGate {
	return &authorityGate{arrived: make(chan struct{}), release: make(chan struct{})}
}
func (g *authorityGate) wait(ctx context.Context) error {
	g.once.Do(func() { close(g.arrived) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *authorityGate) open() { g.opened.Do(func() { close(g.release) }) }
func await(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(750 * time.Millisecond):
		t.Fatal("BARRIER_DEADLINE")
	}
}
func (f *authorityFixture) blocked(t *testing.T, waiter, holder uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 500*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for n := 0; n < 50; n++ {
		var yes bool
		var count int
		e := f.pools[2].QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND $2=ANY(pg_blocking_pids(pid))),(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database())`, waiter, holder).Scan(&yes, &count)
		if e != nil || count > 3 {
			t.Fatal("LOCK_OBSERVATION_FAILED")
		}
		if yes {
			t.Log("AUTHORITY_LOCK_OBSERVED")
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("LOCK_OBSERVATION_DEADLINE")
		case <-ticker.C:
		}
	}
	t.Fatal("LOCK_OBSERVATION_EXHAUSTED")
}
func TestAuthorityPGOfflinePreflight(t *testing.T) {
	in := authorityInput{Version: 1, Binary: strings.Repeat("c", 64), Nonce: strings.Repeat("a", 32), Password: strings.Repeat("b", 64), Host: "127.0.0.1", Port: 5432, Schema: authoritySchemaHash, Image: authorityImage, Network: "none", CPU: 2000000000, Memory: 1073741824, Swap: 1073741824, Shm: 67108864, Tmpfs: 2147483648, Isolated: true, Owned: true}
	in.Database = "wt_authority_test_" + in.Nonce
	in.User = "wt_authority_owner_" + in.Nonce
	if authorityValidate(in, nil) != nil {
		t.Fatal("valid preflight rejected")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal("encode failed")
	}
	decoded, err := authorityDecode(bytes.NewReader(raw))
	if err != nil || decoded != in {
		t.Fatal("valid input rejected")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("decode failed")
	}
	for key := range fields {
		t.Run("missing_"+key, func(t *testing.T) {
			var copy map[string]json.RawMessage
			_ = json.Unmarshal(raw, &copy)
			delete(copy, key)
			b, _ := json.Marshal(copy)
			if _, err := authorityDecode(bytes.NewReader(b)); err == nil {
				t.Fatal("missing key accepted")
			}
		})
	}
	duplicate := append([]byte(`{"version":1,`), raw[1:]...)
	if _, err := authorityDecode(bytes.NewReader(duplicate)); err == nil {
		t.Fatal("duplicate key accepted")
	}

	for _, k := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGOPTIONS", "PGAPPNAME", "PGTARGETSESSIONATTRS", "HOME", "UNREVIEWED"} {
		t.Run(k, func(t *testing.T) {
			if authorityValidate(in, []string{k + "=forbidden"}) == nil {
				t.Fatal("ambient fallback accepted")
			}
		})
	}
	v := reflect.ValueOf(&in).Elem()
	for i := 0; i < v.NumField(); i++ {
		t.Run(v.Type().Field(i).Name, func(t *testing.T) {
			copy := in
			x := reflect.ValueOf(&copy).Elem().Field(i)
			if x.Kind() == reflect.String {
				x.SetString("wrong")
			} else {
				x.Set(reflect.Zero(x.Type()))
			}
			if authorityValidate(copy, nil) == nil {
				t.Fatal("bad attestation accepted")
			}
		})
	}
	for _, s := range []string{`{}`, `{"pid_mode":"","pid_mode":""}`, `{"pid_mode":null}`, `{`, strings.Repeat("x", 8193), `{"host":"127.0.0.1","fallbacks":[]}`, `{} {}`} {
		if _, e := authorityDecode(strings.NewReader(s)); e == nil {
			t.Fatal("bad input accepted")
		}
	}
}

// Canonical full-row snapshots include xmin. Excluding an expected changed key
// still requires all unrelated rows in all tables to remain byte-for-byte equal.
func authorityChanges(t *testing.T, before, after string) map[string][2]json.RawMessage {
	t.Helper()
	decode := func(s string) map[string]json.RawMessage {
		var tables [][]json.RawMessage
		if json.Unmarshal([]byte(s), &tables) != nil || len(tables) != 3 {
			t.Fatal("SNAPSHOT_DECODE_FAILED")
		}
		out := map[string]json.RawMessage{}
		for n, rows := range tables {
			for _, row := range rows {
				var value map[string]json.RawMessage
				if json.Unmarshal(row, &value) != nil {
					t.Fatal("ROW_DECODE_FAILED")
				}
				key := value["scope_id"]
				if n == 2 {
					key = value["operation_id"]
				}
				out[fmt.Sprintf("%d:%s", n, key)] = row
			}
		}
		return out
	}
	a, b := decode(before), decode(after)
	out := map[string][2]json.RawMessage{}
	for key, old := range a {
		if !bytes.Equal(old, b[key]) {
			out[key] = [2]json.RawMessage{old, b[key]}
		}
	}
	for key, row := range b {
		if _, ok := a[key]; !ok {
			out[key] = [2]json.RawMessage{nil, row}
		}
	}
	return out
}
func graphDelta(t *testing.T, before, after string, allowed map[string]string) {
	t.Helper()
	changes := authorityChanges(t, before, after)
	if len(changes) != len(allowed) {
		t.Fatal("GRAPH_DELTA_COUNT_MISMATCH")
	}
	for key, mode := range allowed {
		change, ok := changes[key]
		if !ok || len(change[1]) == 0 || mode == "insert" && len(change[0]) != 0 || mode == "update" && len(change[0]) == 0 {
			t.Fatal("GRAPH_DELTA_IDENTITY_MISMATCH")
		}
	}
}
func graphKey(table int, id string) string { return fmt.Sprintf("%d:%q", table, id) }
func (f *authorityFixture) intent(t *testing.T, a *Adapter, b Binding) (Evidence, error) {
	t.Helper()
	before := f.snapshot(t)
	ev, e := a.recordIntent(f.ctx, b)
	if e == nil {
		graphDelta(t, before, f.snapshot(t), map[string]string{graphKey(1, b.identity.Scope): "insert"})
	}
	return ev, e
}
func (f *authorityFixture) append(t *testing.T, a *Adapter, b Binding, event event) (Evidence, error) {
	t.Helper()
	before := f.snapshot(t)
	ev, e := a.recordEvent(f.ctx, b, event)
	if e == nil {
		allowed := map[string]string{}
		if ev.Classification != "HISTORICAL_FACT_NO_AUTHORITY" {
			allowed[graphKey(2, event.operation)] = "insert"
			var tables [][]map[string]any
			if json.Unmarshal([]byte(before), &tables) != nil {
				t.Fatal("SNAPSHOT_DECODE_FAILED")
			}
			for _, row := range tables[1] {
				if row["scope_id"] == b.identity.Scope && row["state"] == "running" && event.kind != "reconciliation_fact" {
					allowed[graphKey(1, b.identity.Scope)] = "update"
				}
			}
		}
		graphDelta(t, before, f.snapshot(t), allowed)
	}
	return ev, e
}
func TestAuthorityPGOfflineSnapshots(t *testing.T) {
	before := `[[{"scope_id":"x","tuple_xmin":"1"}],[],[]]`
	after := `[[{"scope_id":"x","tuple_xmin":"1"}],[{"scope_id":"x","tuple_xmin":"2"}],[]]`
	graphDelta(t, before, after, map[string]string{graphKey(1, "x"): "insert"})
	if len(authorityChanges(t, before, before)) != 0 {
		t.Fatal("UNCHANGED_DELTA")
	}
	changed := strings.Replace(before, `"1"`, `"2"`, 1)
	if len(authorityChanges(t, before, changed)) != 1 {
		t.Fatal("XMIN_NOT_CHECKED")
	}
}

// Close stays synchronous and owned by the main goroutine. If a pool refuses
// to close, the watchdog terminates this test process with explicit uncertainty;
// this is failure supervision, never evidence of joined driver cleanup.
func authorityClosePools(pools [3]*pgxpool.Pool) bool {
	stop := authorityWatchdog(2*time.Second, func() { fmt.Println("AUTHORITY_CLEANUP_UNCONFIRMED"); os.Exit(2) })
	defer stop()
	for _, p := range pools {
		if p != nil {
			p.Close()
		}
	}
	return true
}
func authorityWatchdog(limit time.Duration, onTimeout func()) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		timer := time.NewTimer(limit)
		defer timer.Stop()
		select {
		case <-stop:
			return
		case <-timer.C:
			onTimeout()
		}
	}()
	return func() { once.Do(func() { close(stop) }); <-done }
}
func TestAuthorityPGOfflineBoundedClose(t *testing.T) {
	timed := make(chan struct{}, 1)
	stop := authorityWatchdog(time.Second, func() { timed <- struct{}{} })
	stop()
	select {
	case <-timed:
		t.Fatal("normal close watchdog fired")
	default:
	}
	stop = authorityWatchdog(time.Millisecond, func() { timed <- struct{}{} })
	select {
	case <-timed:
	case <-time.After(time.Second):
		t.Fatal("deadline did not fire")
	}
	stop()
}
