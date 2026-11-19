package pg

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
)

// These are actual Claim/Prepare calls through scripted transaction boundaries.
// They neither parse SQL nor prove PostgreSQL trigger/locking behavior.
func TestCallbackClaimFrozenDestinationAuthority(t *testing.T) {
	for _, name := range []string{"rotated_current", "version_revoked", "same_version_url_drift", "same_version_key_ref_drift", "reloaded_url", "reloaded_key_ref", "reloaded_version", "subscription_tuple_mismatch"} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
			tx := callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
			tx.steps = tx.steps[:16]
			tx.steps = append(tx.steps,
				callbackClaimStep{op: "exec", sql: "UPDATE callback_outbox SET state=$3,last_error_code=$4", args: []any{"tenant_cb", "delivery_a", "cancelled", "DELIVERY_AUTHORITY_REVOKED"}},
				callbackClaimStep{op: "exec", sql: "INSERT INTO audit("})
			s, _ := callbackClaimStore(t, tx)
			s.now = func() time.Time { return now }
			p := s.policies["tenant_cb\x00destination_a"]
			switch name {
			case "rotated_current":
				tx.steps[7].values[0] = false
			case "version_revoked":
				tx.steps[8].values[2] = false
			case "same_version_url_drift":
				tx.steps[8].values[0] = "https://callbacks.example.com/changed"
			case "same_version_key_ref_drift":
				tx.steps[8].values[1] = "key_changed"
			case "reloaded_url":
				p.Binding.URL = "https://callbacks.example.com/changed"
			case "reloaded_key_ref":
				p.Binding.KeyRef = "key_changed"
			case "reloaded_version":
				p.Binding.PolicyVersion = 2
			case "subscription_tuple_mismatch":
				tx.steps[9].values[0] = false
			}
			s.policies["tenant_cb\x00destination_a"] = p
			d, ok, err := s.claimOne(context.Background(), callbackCandidate{"tenant_cb", "destination_a", "delivery_a"})
			if err != nil || ok || !reflect.DeepEqual(d, cb.Delivery{}) || tx.commits != 1 {
				t.Fatalf("invalid frozen authority issued a claim: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestCallbackClaimMissingHistoricalVersionFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	tx := callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
	tx.steps = tx.steps[:9]
	tx.steps[8].err = pgx.ErrNoRows
	s, _ := callbackClaimStore(t, tx)
	s.now = func() time.Time { return now }
	d, ok, err := s.claimOne(context.Background(), callbackCandidate{"tenant_cb", "destination_a", "delivery_a"})
	if err == nil || ok || !reflect.DeepEqual(d, cb.Delivery{}) || tx.commits != 0 {
		t.Fatal("missing history fell back to current destination")
	}
}

func TestCallbackPrepareRechecksFrozenDestinationAuthority(t *testing.T) {
	for _, name := range []string{"rotation", "version_revocation", "subscription_retirement"} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
			lease := now.Add(10 * time.Second)
			owner, recovery := "worker_cb", "recovery_cb"
			tx := callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
			tx.steps = tx.steps[:13]
			tx.steps[0].sql, tx.steps[1].sql = callbackLockTenant, callbackLockDestination
			row := tx.steps[11].values
			row[1], row[2], row[3], row[5], row[6], row[7], row[8], row[9] = "leased", int64(1), 1, &owner, &recovery, &lease, &now, &lease
			switch name {
			case "rotation":
				tx.steps[7].values[0] = false
			case "version_revocation":
				tx.steps[8].values[2] = false
			case "subscription_retirement":
				tx.steps[9].values[0] = false
			}
			tx.steps = append(tx.steps,
				callbackClaimStep{op: "row", sql: callbackReadAttempt, args: []any{"tenant_cb", "delivery_a", int64(1)}, values: []any{1, owner, recovery, now, nil, nil, "in_progress", nil, nil}},
				callbackClaimStep{op: "rows", sql: callbackReadSlots, args: []any{"tenant_cb", "delivery_a"}, rows: [][]any{{"tenant", "tenant_cb", 0, owner, "delivery_a", int64(1), recovery, int64(3), lease}, {"destination", "destination_a", 0, owner, "delivery_a", int64(1), recovery, int64(4), lease}}},
				callbackClaimStep{op: "row", sql: callbackFinalTime, values: []any{now}})
			s, _ := callbackClaimStore(t, tx)
			s.now = func() time.Time { return now }
			p := cb.Prepared{Handle: cb.Handle{Tenant: "tenant_cb", DeliveryID: "delivery_a", Owner: owner, Recovery: recovery, Fence: 1, Attempt: 1, MaxAttempts: 4}, Binding: s.policies["tenant_cb\x00destination_a"].Binding, EventID: "event_cb", Digest: row[14].(string), IP: netip.MustParseAddr("93.184.216.34"), Deadline: now.Add(time.Second)}
			permit, err := s.Prepare(context.Background(), p)
			if !errors.Is(err, cb.ErrDenied) || permit.Fresh || tx.commits != 0 {
				t.Fatalf("changed authority issued a send permit: %v", err)
			}
		})
	}
}

func TestCallbackStoreRetainsSingleCurrentPolicyBound(t *testing.T) {
	complete, db, _ := callbackFixture(t)
	p := cb.Policy{Binding: cb.Binding{Tenant: "tenant_cb", Destination: "destination_cb", URL: "https://callbacks.example.com/result", KeyRef: "key_cb", PolicyVersion: 1, KeyVersion: 1}, CIDRs: []string{"93.184.216.0/24"}}
	newer := p
	newer.Binding.PolicyVersion = 2
	if _, err := NewCallbackStore(complete.runner, db.runtime, complete.gate, []cb.Policy{p, newer}); err == nil {
		t.Fatal("phase A silently enabled multi-version draining")
	}
	if db.acquires != 0 {
		t.Fatal("configuration touched DB")
	}
}
