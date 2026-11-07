package runtimeassembly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Frozen full-statement digests, authored from the baseline SQL. This is a
// finite protocol script, not a database, SQL interpreter or durability proof.
const (
	asSQLRecovery        = "c47f91cd6639ffe0de3eaa88fb2352792a6c38448d06cdc4c0f5a7c513aa6ef7"
	asSQLTenant          = "b70eb2169d0a95b9f9d65ab80cbb6b64fabf7e1ba346c131a47f0d5f7930536d"
	asSQLDestination     = "b5cb13da461849d355c315078a4aea33506d1f322f37ee9b05440954463a1567"
	asSQLDelivery        = "20f45786f6f0a39e24b5a3cf88104f9339b67adfefe0536d3f356eb864f8949e"
	asSQLAttempt         = "c8915b5b71494c0e2619647519b79d0ad1d64307c3b083a5873c53322f74cbc6"
	asSQLSlots           = "314179a1459b7a43a604ef6432a8e87e6386c38f446e62830bc0f761d3d4812c"
	asSQLClock           = "2461f8bb8070d8f1e4e669943dfb1cc042ed3e05c56721efdd125ae68b55f78c"
	asSQLVersion         = "6ba51b5ef27f23c503723bb938c52f982883bf152a286945e8be8a7dccd1ab76"
	asSQLHint            = "b897a359422727e15f82e55d520ba7d17fa1a0b29f0ff7a2f5099fd5aa068fc1"
	asSQLOldSlot         = "e7f24937560c48ed774b004767f3dfe090cc6de00c2c7ac0f30a5c80dd151286"
	asSQLCompletionAudit = "6fddbe2f7e33891d23272ad44f9d8b9185f013ab14d8abdd7bf465a5cfc4a874"
	asSQLUpdateAttempt   = "b21ae8f1fa5066e98a44d587360118002a1fb263cc79626d020cfb63bfcc8d67"
	asSQLUpdateDelivery  = "5b3127f285cbd463d4101d6032bee706ec747971a3a4e3e7010390815bb62c35"
	asSQLReleaseSlot     = "474bf85ecf8c947fde8cfd46eeb0b786152e87dbebe3ba2035ecd2e360574885"
	asSQLInsertAudit     = "cf2594ea8a554b15d546eb4c12fca4ed9aa0b6f20c2b43e7fab580b561de48c2"
	asSQLDiscovery       = "1082619b9a7fcaf20c823bdeae5dbc1a5c1f603b4b358b4686112c10b29c077e"
	asSQLRuntime         = "301ddc4bc1110a16a09262685c818ef6649540cd0c18fb2e08bc98a60354d1fa"
	asSQLScope           = "0d58cbe51089d1f4af99d56b4fa5f83566f4249809746c273edafbbb421d87bd"
	asSQLApp             = "9268511e2e97f56d1ad1c562bdd5aaafff627a44e64108694668a81c9bac5191"
	asSQLDevice          = "5571fa8f5dd059d4c639657fe00d1d73803cb42225b771f2c2cd2870171b8b85"
	asSQLBinding         = "bc84881355d38ed35ab8ef7fb305cde45d3d3e761aaf3d8c9b61a4a1b11253a7"
	asSQLDestActive      = "7ee393326e9c7bc22ce2df079e51bd4a38c2c1948608c4a13014a77da6fd3a2e"
	asSQLSubscription    = "2fcc9441e8717d2d9b00d1e2e972f6317009793fd589d6028bc2f9f3922ab208"
	asSQLCommand         = "855e8555f97296a4a0effef80486830a2c91f4984bd70e1180164c1108324541"
	asSQLEvent           = "ffa8cfb0652f95b319b8ea7470c98dc4fa431cbd0ba56add27e194b4c3aebd89"
	asSQLFreeSlot        = "9b4fcfa7c34f4f485cbb981f8fec1a0cda96fa2636005fbcf3f7d25dbad88af1"
	asSQLEligible        = "a9e6808093d755dd4df6b4a188ea2ecf672b0fe65bdd310e2f60e2223423ca29"
	asSQLDebit           = "85f52756bca4c6a89f62f966cc579e9099bdbb5d23dab3ede5f9ba1c3494b8a1"
	asSQLClaim           = "adfc75c58efe67fbeaedce347fe893f8d225fecf7a72d038f2cbf98536ebcd31"
	asSQLInsertAttempt   = "5f763b6de547a3345522db8f2e4e4e412ec53b574c8fbb24f68cc3c28256ecb1"
	asSQLAllocateSlot    = "d7027f43ded5e1a99ab3c979010fec43dd6bcc44fd3338e4168f2ea025d92a03"
	asSQLAudit           = "467db3e42a1ad76e95fc4f6641c9f426d04aad84976115bea41d68173c0366e7"
	asSQLEnabled         = "c41ba2095dbd0ef39d5dbdd47bf5a1f8a748c83c898493ee903be71eee7fa0bf"
	asSQLPrepare         = "0d0f4228dad99e90d585d548c160ee7ea79067f99d259599c75b46f5ef66519c"
	asSQLSkipTenant      = "732811226f7a75d7fdd1269de63d2bc4843a4793421bb0c990f1784d14146aa6"
	asSQLSkipDestination = "7363d4cacfc5cc1f482cf9edc3af93436b32824c649d384bcac0de82af665e95"
)

var errAssemblyScript = errors.New("assembly script mismatch")

type assemblyStep struct {
	op, digest   string
	args, values []any
	rows         [][]any
	randomAudit  bool
}
type assemblyScriptSnapshot struct {
	Acquisitions                                  int
	Commits, Rollbacks, Releases, Discards, Execs [6]int
	Phase                                         string
}
type assemblyScript struct {
	mu                 sync.Mutex
	t                  *testing.T
	txs                [6]*assemblyTx
	state              assemblyScriptSnapshot
	observationEntered chan struct{}
	observationRelease chan struct{}
	releaseOnce        sync.Once
}
type assemblyTx struct {
	p           *assemblyScript
	index, next int
	steps       []assemblyStep
	begun       bool
}

func assemblyDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (p *assemblyScript) fail(label string, tx, step int) error {
	p.t.Errorf("assembly script %s at transaction %d step %d", label, tx+1, step+1)
	return errAssemblyScript
}
func assemblyBounded(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}
func (p *assemblyScript) Acquire(ctx context.Context) (pg.Connection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := p.state.Acquisitions
	if !assemblyBounded(ctx) || n >= len(p.txs) || (n > 0 && p.state.Releases[n-1] != 1) {
		return nil, p.fail("acquire", n, 0)
	}
	p.state.Acquisitions++
	return p.txs[n], nil
}
func (tx *assemblyTx) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx.p.mu.Lock()
	defer tx.p.mu.Unlock()
	expected := pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	if tx.index < 2 {
		expected.AccessMode = pgx.ReadOnly
	}
	if !assemblyBounded(ctx) || tx.begun || opts != expected {
		return nil, tx.p.fail("begin", tx.index, tx.next)
	}
	tx.begun = true
	return tx, nil
}
func (tx *assemblyTx) Release() {
	tx.p.mu.Lock()
	defer tx.p.mu.Unlock()
	tx.p.state.Releases[tx.index]++
	if tx.p.state.Releases[tx.index] != 1 || tx.p.state.Rollbacks[tx.index] != 1 {
		tx.p.fail("release", tx.index, tx.next)
	}
}
func (tx *assemblyTx) Discard(ctx context.Context) error {
	tx.p.mu.Lock()
	defer tx.p.mu.Unlock()
	tx.p.state.Discards[tx.index]++
	if !assemblyBounded(ctx) || tx.index != 4 || tx.p.state.Discards[tx.index] != 1 {
		return tx.p.fail("discard", tx.index, tx.next)
	}
	return nil
}
func (tx *assemblyTx) take(ctx context.Context, op, sql string, args []any) (assemblyStep, error) {
	tx.p.mu.Lock()
	defer tx.p.mu.Unlock()
	if !assemblyBounded(ctx) || !tx.begun || tx.next >= len(tx.steps) || tx.p.state.Commits[tx.index] != 0 {
		return assemblyStep{}, tx.p.fail("unexpected operation", tx.index, tx.next)
	}
	s := tx.steps[tx.next]
	matches := reflect.DeepEqual(s.args, args)
	if s.randomAudit && len(args) == len(s.args) {
		matches = false
		id, ok := args[0].(string)
		if ok && len(id) == 38 && id[:6] == "audit_" {
			decoded, e := hex.DecodeString(id[6:])
			matches = e == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == id[6:] && reflect.DeepEqual(s.args[1:], args[1:])
		}
	}
	if s.op != op || assemblyDigest([]byte(sql)) != s.digest || !matches {
		return assemblyStep{}, tx.p.fail("statement or arguments", tx.index, tx.next)
	}
	tx.next++
	if op == "exec" {
		tx.p.state.Execs[tx.index]++
	}
	return s, nil
}
func (tx *assemblyTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	s, e := tx.take(ctx, "row", sql, args)
	return assemblyRow{tx, s.values, e}
}
func (tx *assemblyTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	s, e := tx.take(ctx, "rows", sql, args)
	if e != nil {
		return nil, e
	}
	return &assemblyRows{tx: tx, rows: s.rows}, nil
}
func (tx *assemblyTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	_, e := tx.take(ctx, "exec", sql, args)
	if e != nil {
		return pgconn.CommandTag{}, e
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (tx *assemblyTx) Commit(ctx context.Context) error {
	tx.p.mu.Lock()
	defer tx.p.mu.Unlock()
	if !assemblyBounded(ctx) || tx.next != len(tx.steps) || tx.p.state.Commits[tx.index] != 0 {
		return tx.p.fail("commit", tx.index, tx.next)
	}
	tx.p.state.Commits[tx.index]++
	switch tx.index {
	case 2:
		tx.p.state.Phase = "claimed"
	case 3:
		tx.p.state.Phase = "prepared"
	case 4:
		tx.p.state.Phase = "scripted-completed-retry"
		return errors.New("synthetic lost commit acknowledgement")
	}
	return nil
}
func (tx *assemblyTx) Rollback(ctx context.Context) error {
	tx.p.mu.Lock()
	tx.p.state.Rollbacks[tx.index]++
	valid := assemblyBounded(ctx) && tx.p.state.Rollbacks[tx.index] == 1
	if !valid {
		tx.p.mu.Unlock()
		return tx.p.fail("rollback", tx.index, tx.next)
	}
	barrier := tx.index == 5 && tx.p.state.Commits[5] == 1
	tx.p.mu.Unlock()
	if barrier {
		close(tx.p.observationEntered)
		select {
		case <-tx.p.observationRelease:
		case <-ctx.Done():
			return tx.p.fail("observation cleanup timeout", tx.index, tx.next)
		}
	}
	return pgx.ErrTxClosed
}
func (tx *assemblyTx) unexpected() error {
	return tx.p.fail("unsupported operation", tx.index, tx.next)
}
func (tx *assemblyTx) Begin(context.Context) (pgx.Tx, error) { return nil, tx.unexpected() }
func (tx *assemblyTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, tx.unexpected()
}
func (tx *assemblyTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	tx.unexpected()
	return nil
}
func (tx *assemblyTx) LargeObjects() pgx.LargeObjects { tx.unexpected(); return pgx.LargeObjects{} }
func (tx *assemblyTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, tx.unexpected()
}
func (tx *assemblyTx) Conn() *pgx.Conn { tx.unexpected(); return nil }
func (p *assemblyScript) releaseObservation() {
	p.releaseOnce.Do(func() { close(p.observationRelease) })
}
func (p *assemblyScript) snapshot() assemblyScriptSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}
func (p *assemblyScript) assertFinished(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Acquisitions != 6 || p.state.Phase != "scripted-completed-retry" {
		t.Error("assembly did not complete six scripted phases")
	}
	for i, tx := range p.txs {
		discard := 0
		if i == 4 {
			discard = 1
		}
		if tx.next != len(tx.steps) || p.state.Commits[i] != 1 || p.state.Rollbacks[i] != 1 || p.state.Releases[i] != 1 || p.state.Discards[i] != discard {
			t.Errorf("assembly transaction %d incomplete or cleanup repeated", i+1)
		}
	}
	if p.state.Execs != [6]int{0, 0, 7, 2, 5, 0} {
		t.Error("assembly unexpected DML counts")
	}
}

type assemblyRow struct {
	tx     *assemblyTx
	values []any
	err    error
}

func (r assemblyRow) Scan(dst ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dst) != len(r.values) {
		return r.tx.unexpected()
	}
	for i, v := range r.values {
		d := reflect.ValueOf(dst[i])
		if d.Kind() != reflect.Pointer || d.IsNil() {
			return r.tx.unexpected()
		}
		d = d.Elem()
		if v == nil {
			d.SetZero()
			continue
		}
		s := reflect.ValueOf(v)
		if !s.Type().AssignableTo(d.Type()) {
			return r.tx.unexpected()
		}
		d.Set(s)
	}
	return nil
}

type assemblyRows struct {
	tx     *assemblyTx
	rows   [][]any
	index  int
	closed bool
	err    error
}

func (r *assemblyRows) Next() bool {
	if r.closed {
		return false
	}
	if r.index >= len(r.rows) {
		r.Close()
		return false
	}
	r.index++
	return true
}
func (r *assemblyRows) Scan(dst ...any) error {
	if r.closed || r.index < 1 {
		return r.tx.unexpected()
	}
	r.err = (assemblyRow{r.tx, r.rows[r.index-1], nil}).Scan(dst...)
	return r.err
}
func (r *assemblyRows) Close() {
	if !r.closed && r.index < len(r.rows) {
		r.err = r.tx.unexpected()
	}
	r.closed = true
}
func (r *assemblyRows) Err() error                    { return r.err }
func (r *assemblyRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *assemblyRows) FieldDescriptions() []pgconn.FieldDescription {
	r.err = r.tx.unexpected()
	return nil
}
func (r *assemblyRows) Values() ([]any, error) { return nil, r.tx.unexpected() }
func (r *assemblyRows) RawValues() [][]byte    { r.err = r.tx.unexpected(); return nil }
func (r *assemblyRows) Conn() *pgx.Conn        { r.err = r.tx.unexpected(); return nil }

// The only successful path is recovery-empty, claim-scan, claim, prepare,
// retry settlement with lost ACK, then read-only settlement observation.
func newAssemblyScript(t *testing.T, now time.Time) *assemblyScript {
	t.Helper()
	p := &assemblyScript{t: t, observationEntered: make(chan struct{}), observationRelease: make(chan struct{})}
	t.Cleanup(p.releaseObservation)
	row := func(sql string, args, values []any) assemblyStep {
		return assemblyStep{op: "row", digest: sql, args: args, values: values}
	}
	exec := func(sql string, args []any) assemblyStep { return assemblyStep{op: "exec", digest: sql, args: args} }
	audit := func(action, outcome string) assemblyStep {
		s := exec(asSQLAudit, []any{"", "tenant_cb", "worker_cb", action, "delivery_a", outcome, now.UnixMilli()})
		s.randomAudit = true
		return s
	}
	dest, id := "destination_a", "delivery_a"
	body := []byte(`{"eventId":"event_cb","status":"succeeded"}`)
	lease := now.Add(10 * time.Second)
	claim := []assemblyStep{
		row(asSQLSkipTenant, []any{"tenant_cb"}, []any{"tenant_cb"}),
		row(asSQLSkipDestination, []any{"tenant_cb", dest}, []any{dest}),
		row(asSQLRuntime, []any{"worker_cb", "worker"}, []any{"boot_cb", "recovery_cb", "active", true}),
		row(asSQLScope, []any{"tenant_cb", id, dest}, []any{"app_cb", "device_cb", "command_cb", int64(1), int64(1), "sub_cb", int64(1), int64(1), int64(1)}),
		row(asSQLApp, []any{"tenant_cb", "app_cb"}, []any{true, int64(1), now.Add(time.Hour).UnixMilli()}),
		row(asSQLDevice, []any{"tenant_cb", "device_cb"}, []any{true, int64(1)}),
		row(asSQLBinding, []any{"tenant_cb", "app_cb", "device_cb"}, []any{"device_cb"}),
		row(asSQLDestActive, []any{"tenant_cb", dest, "app_cb", int64(1), int64(1)}, []any{true}),
		row(asSQLVersion, []any{"tenant_cb", "app_cb", dest, int64(1), int64(1)}, []any{"https://callbacks.example.com/result", "key_cb", true}),
		row(asSQLSubscription, []any{"tenant_cb", "app_cb", "sub_cb", int64(1), dest, int64(1), int64(1)}, []any{true}),
		row(asSQLCommand, []any{"tenant_cb", "app_cb", "device_cb", "command_cb"}, []any{"command_cb"}),
		row(asSQLDelivery, []any{"tenant_cb", id}, []any{dest, "ready", int64(0), 0, 4, nil, nil, nil, nil, nil, now.Add(time.Hour), nil, now.Add(-time.Minute), body, assemblyDigest(body), nil, true, body}),
		row(asSQLEvent, []any{"tenant_cb", id}, []any{"event_cb", int64(1), int64(1)}),
		row(asSQLFreeSlot, []any{"tenant", "tenant_cb", "tenant_cb"}, []any{0, int64(2)}),
		row(asSQLFreeSlot, []any{"destination", "tenant_cb", dest}, []any{0, int64(3)}),
		row(asSQLClock, nil, []any{now}),
		row(asSQLEligible, []any{"tenant", "tenant_cb", "tenant_cb", now}, []any{true}),
		row(asSQLEligible, []any{"destination", "tenant_cb", dest, now}, []any{true}),
		exec(asSQLDebit, []any{"tenant", "tenant_cb", "tenant_cb", now}),
		exec(asSQLDebit, []any{"destination", "tenant_cb", dest, now}),
		exec(asSQLClaim, []any{"tenant_cb", id, int64(1), 1, now, lease, "worker_cb", "recovery_cb", int64(0), 0}),
		exec(asSQLInsertAttempt, []any{"tenant_cb", id, int64(1), 1, "recovery_cb", "worker_cb", now}),
		exec(asSQLAllocateSlot, []any{"tenant", "tenant_cb", "tenant_cb", 0, "worker_cb", id, int64(1), "recovery_cb", lease, int64(2)}),
		exec(asSQLAllocateSlot, []any{"destination", "tenant_cb", dest, 0, "worker_cb", id, int64(1), "recovery_cb", lease, int64(3)}),
		audit("callback.claimed", "leased")}
	// Immutable rows are literal protocol evidence; phase labels record synthetic
	// commit publication only, never PostgreSQL durability or actual SQL effects.
	owner, epoch, code := "worker_cb", "recovery_cb", "HTTP_RETRYABLE"
	status := 503
	leased := []any{dest, "leased", int64(1), 1, 4, &owner, &epoch, &lease, &now, &lease, now.Add(time.Hour), nil, now.Add(-time.Minute), body, assemblyDigest(body), nil, true, body}
	attempt := []any{1, owner, epoch, now, nil, nil, "in_progress", nil, nil}
	slots := assemblyStep{op: "rows", digest: asSQLSlots, args: []any{"tenant_cb", id}, rows: [][]any{
		{"tenant", "tenant_cb", 0, owner, id, int64(1), epoch, int64(3), lease},
		{"destination", dest, 0, owner, id, int64(1), epoch, int64(4), lease},
	}}
	prepare := append([]assemblyStep(nil), claim[:13]...)
	prepare[0].digest = asSQLTenant
	prepare[1].digest = asSQLDestination
	prepare[11] = row(asSQLDelivery, []any{"tenant_cb", id}, leased)
	prepare = append(prepare, row(asSQLAttempt, []any{"tenant_cb", id, int64(1)}, attempt), slots, row(asSQLClock, nil, []any{now}),
		row(asSQLEnabled, []any{"tenant", "tenant_cb", "tenant_cb", now}, []any{true}),
		row(asSQLEnabled, []any{"destination", "tenant_cb", dest, now}, []any{true}),
		exec(asSQLPrepare, []any{"tenant_cb", id, int64(1), owner, epoch, now, 1}), audit("callback.prepared", "prepared"))
	preparedAttempt := append([]any(nil), attempt...)
	preparedAttempt[4] = &now
	common := []assemblyStep{row(asSQLTenant, []any{"tenant_cb"}, []any{"tenant_cb"}), row(asSQLHint, []any{"tenant_cb", id}, []any{dest}), row(asSQLDestination, []any{"tenant_cb", dest}, []any{dest}), claim[2]}
	settle := append([]assemblyStep(nil), common...)
	settle = append(settle, row(asSQLDelivery, []any{"tenant_cb", id}, leased), row(asSQLAttempt, []any{"tenant_cb", id, int64(1)}, preparedAttempt), slots, row(asSQLClock, nil, []any{now}),
		exec(asSQLUpdateAttempt, []any{"tenant_cb", id, int64(1), owner, epoch, now, "retryable_failure", 503, code, 1}),
		exec(asSQLUpdateDelivery, []any{"tenant_cb", id, int64(1), owner, epoch, "ready", now.Add(time.Second), now, code, 1, 4}),
		exec(asSQLReleaseSlot, []any{"tenant", "tenant_cb", "tenant_cb", 0, owner, id, int64(1), epoch, int64(3), lease}),
		exec(asSQLReleaseSlot, []any{"destination", "tenant_cb", dest, 0, owner, id, int64(1), epoch, int64(4), lease}))
	auditID := "callback_complete_" + assemblyDigest([]byte("callback.complete.v1\x00tenant_cb\x00delivery_a\x001"))
	settle = append(settle, exec(asSQLInsertAudit, []any{auditID, "tenant_cb", owner, id, "ready:" + code, now.UnixMilli()}))
	finished := append([]any(nil), preparedAttempt...)
	finished[5] = &now
	finished[6] = "retryable_failure"
	finished[7] = &status
	finished[8] = &code
	completed := append([]any(nil), leased...)
	completed[1] = "ready"
	completed[5] = nil
	completed[6] = nil
	completed[7] = nil
	completed[12] = now.Add(time.Second)
	completed[15] = &code
	observe := append([]assemblyStep(nil), common...)
	observe = append(observe, row(asSQLDelivery, []any{"tenant_cb", id}, completed), row(asSQLAttempt, []any{"tenant_cb", id, int64(1)}, finished),
		row(asSQLOldSlot, []any{"tenant_cb", id, owner, epoch, int64(1)}, []any{false}),
		row(asSQLCompletionAudit, []any{"tenant_cb", auditID}, []any{owner, "callback.completed", id, "ready:" + code, now.UnixMilli()}), row(asSQLClock, nil, []any{now}))
	scripts := [][]assemblyStep{
		{{op: "rows", digest: asSQLRecovery, args: []any{[]string{"tenant_cb"}, []string{dest}, epoch}}},
		{{op: "rows", digest: asSQLDiscovery, args: []any{[]string{"tenant_cb"}, []string{dest}}, rows: [][]any{{"tenant_cb", dest, id}}}},
		claim, prepare, settle, observe,
	}
	for i, steps := range scripts {
		p.txs[i] = &assemblyTx{p: p, index: i, steps: steps}
	}
	return p
}

var _ pg.Pool = (*assemblyScript)(nil)
var _ pg.Connection = (*assemblyTx)(nil)
var _ pgx.Tx = (*assemblyTx)(nil)
var _ pgx.Rows = (*assemblyRows)(nil)

func (r *assemblyRows) TypeMap() *pgtype.Map { r.err = r.tx.unexpected(); return nil }
