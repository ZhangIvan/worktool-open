package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPGAdmissionPayloadStorage(t *testing.T) {
	a := newAdmissionFixture(t, 0)
	a.request.Text = "prefix\x00suffix\n\"😀"
	first := awaitAdmission(t, a, a.start(a.request))
	if first.err != nil {
		t.Fatal("PAYLOAD-01 canonical NUL text not stored")
	}
	var raw, digest, requestHash string
	if e := a.f.owner.QueryRow(a.f.ctx, `SELECT c.payload,c.digest,m.request_hash FROM commands c JOIN developer_messages m ON m.tenant=c.tenant AND m.command=c.id WHERE c.tenant='T1' AND c.id=$1`, first.receipt.CommandID).Scan(&raw, &digest, &requestHash); e != nil {
		t.Fatal("payload read failed")
	}
	expected, e := canonicalRestricted(map[string]any{"targetId": a.request.TargetID, "text": a.request.Text})
	if e != nil {
		t.Fatal(e)
	}
	if raw != expected || strings.ContainsRune(raw, 0) || !strings.Contains(raw, `\u0000`) {
		t.Fatal("PAYLOAD-01/05 canonical escaped bytes were not preserved")
	}
	request, e := requestCanonical(a.request)
	if e != nil || requestHash != hashText(request) {
		t.Fatal("stored request digest mismatch")
	}
	command, e := commandCanonical(a.request, 1, first.receipt.ExpiresAt)
	if e != nil || digest != hashText(command) {
		t.Fatal("stored command digest mismatch")
	}
	checkLedger(t, a, 1, 0)
	second := awaitAdmission(t, a, a.start(a.request))
	if second.err != nil || first.receipt != second.receipt {
		t.Fatal("PAYLOAD-02 NUL replay changed receipt")
	}
	checkLedger(t, a, 1, 0)
	for _, tc := range []struct{ name, value, code string }{
		{"PAYLOAD-03_bad_json", `{"text":"missing brace"`, "22P02"},
		{"PAYLOAD-04_array", `[]`, "23514"}, {"PAYLOAD-04_string", `"x"`, "23514"}, {"PAYLOAD-04_number", `1`, "23514"}, {"PAYLOAD-04_boolean", `true`, "23514"}, {"PAYLOAD-04_json_null", `null`, "23514"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, e := a.f.store.Begin(a.f.ctx)
			if e != nil {
				t.Fatal("negative begin")
			}
			defer rollbackTest(tx)
			_, e = tx.Exec(a.f.ctx, `UPDATE commands SET payload=$2 WHERE tenant='T1' AND id=$1`, first.receipt.CommandID, tc.value)
			if e == nil {
				e = tx.Commit(a.f.ctx)
			}
			var pe *pgconn.PgError
			if !errors.As(e, &pe) || pe.Code != tc.code || tc.code == "23514" && pe.ConstraintName != "commands_payload_json_object" {
				t.Fatal("malformed/nonobject payload not rejected by expected validation")
			}
		})
	}
	t.Run("PAYLOAD-04_sql_null_legacy", func(t *testing.T) {
		tx, e := a.f.store.Begin(a.f.ctx)
		if e != nil {
			t.Fatal("NULL begin")
		}
		defer rollbackTest(tx)
		mustExec(t, tx, a.f.ctx, `UPDATE commands SET payload=NULL WHERE tenant='T1' AND id=$1`, first.receipt.CommandID)
		var isnull bool
		if e = tx.QueryRow(a.f.ctx, `SELECT payload IS NULL FROM commands WHERE id=$1`, first.receipt.CommandID).Scan(&isnull); e != nil || !isnull {
			t.Fatal("SQL NULL compatibility changed")
		}
	})
	var retained string
	if e = a.f.owner.QueryRow(a.f.ctx, `SELECT payload FROM commands WHERE id=$1`, first.receipt.CommandID).Scan(&retained); e != nil || retained != expected {
		t.Fatal("negative/NULL rollback changed original payload")
	}
	checkLedger(t, a, 1, 0)
}

func TestPGAdmissionPayloadMigrationGuard(t *testing.T) {
	f := integrationFixture(t)
	// Existing base fixture already proves the full append-only path; each subcase
	// additionally builds exactly original0001..0005 in its own random-child schema.
	for index, tc := range []struct {
		name, mutate string
		success      bool
	}{
		{"PAYLOAD-06_expected_catalog", "", true},
		{"PAYLOAD-06_renamed", `ALTER TABLE commands RENAME CONSTRAINT commands_payload_check TO unexpected_payload_check`, false},
		{"PAYLOAD-06_changed_expression", `ALTER TABLE commands DROP CONSTRAINT commands_payload_check;ALTER TABLE commands ADD CONSTRAINT commands_payload_check CHECK(payload IS NULL OR length(payload)>0)`, false},
		{"PAYLOAD-06_missing", `ALTER TABLE commands DROP CONSTRAINT commands_payload_check`, false},
		{"PAYLOAD-06_wrong_column_type", `ALTER TABLE commands ALTER COLUMN payload TYPE varchar`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := fmt.Sprintf("%s_g%d", f.schema, index)
			q := pgx.Identifier{schema}.Sanitize()
			mustExec(t, f.owner, f.ctx, "CREATE SCHEMA "+q)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, e := f.owner.Exec(ctx, "SET search_path TO "+pgx.Identifier{f.schema}.Sanitize()+",pg_catalog;DROP SCHEMA "+q+" CASCADE"); e != nil {
					t.Error("guard fixture cleanup failed")
				}
			})
			mustExec(t, f.owner, f.ctx, "SET search_path TO "+q+",pg_catalog")
			for _, name := range migrations.Order[:5] {
				b, e := migrations.Files.ReadFile(name)
				if e != nil {
					t.Fatal("migration fixture missing")
				}
				tx, e := f.owner.Begin(f.ctx)
				if e != nil {
					t.Fatal("migration begin")
				}
				_, e = tx.Exec(f.ctx, string(b))
				if e == nil {
					e = tx.Commit(f.ctx)
				} else {
					rollbackTest(tx)
				}
				if e != nil {
					t.Fatal("original-five fixture migration failed")
				}
			}
			var payloadName, payloadExpr, resultBefore string
			if e := f.owner.QueryRow(f.ctx, `SELECT conname,pg_get_expr(conbin,conrelid) FROM pg_constraint WHERE conrelid='commands'::regclass AND conname='commands_payload_check'`).Scan(&payloadName, &payloadExpr); e != nil || payloadName != "commands_payload_check" || payloadExpr != `((payload IS NULL) OR (jsonb_typeof((payload)::jsonb) = 'object'::text))` {
				t.Fatal("actual original payload catalog differs from reviewed guard; do not guess/drop")
			}
			if e := f.owner.QueryRow(f.ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='commands'::regclass AND conname='commands_result_check'`).Scan(&resultBefore); e != nil {
				t.Fatal("original result check missing")
			}
			if tc.mutate != "" {
				mustExec(t, f.owner, f.ctx, tc.mutate)
			}
			var before string
			if e := f.owner.QueryRow(f.ctx, `SELECT COALESCE(string_agg(conname||':'||pg_get_constraintdef(oid),'|' ORDER BY conname),'') FROM pg_constraint WHERE conrelid='commands'::regclass`).Scan(&before); e != nil {
				t.Fatal("guard prestate")
			}
			body, e := migrations.Files.ReadFile("0006_commands_payload_json.sql")
			if e != nil {
				t.Fatal("0006 missing")
			}
			tx, e := f.owner.Begin(f.ctx)
			if e != nil {
				t.Fatal("guard begin")
			}
			_, e = tx.Exec(f.ctx, string(body))
			if e == nil {
				e = tx.Commit(f.ctx)
			} else {
				rollbackTest(tx)
			}
			if tc.success {
				if e != nil {
					t.Fatal("expected catalog migration rejected")
				}
			} else {
				var pe *pgconn.PgError
				if !errors.As(e, &pe) || pe.Code != "P0001" || !strings.HasPrefix(pe.Message, "PAYLOAD_CHECK_GUARD_") {
					t.Fatal("unexpected catalog must fail closed via guard")
				}
				var after string
				if e = f.owner.QueryRow(f.ctx, `SELECT COALESCE(string_agg(conname||':'||pg_get_constraintdef(oid),'|' ORDER BY conname),'') FROM pg_constraint WHERE conrelid='commands'::regclass`).Scan(&after); e != nil || after != before {
					t.Fatal("failed guard modified constraints")
				}
			}
			var resultAfter string
			if e = f.owner.QueryRow(f.ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='commands'::regclass AND conname='commands_result_check'`).Scan(&resultAfter); e != nil || resultAfter != resultBefore {
				t.Fatal("unrelated result check changed")
			}
		})
	}
}
