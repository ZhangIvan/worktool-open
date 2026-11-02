package pg

import (
	"context"
	"github.com/ZhangIvan/worktool-open/backend-go/migrations"
	"strings"
	"testing"
	"time"
)

func TestLeaseTrustedTypesAndIdentity(t *testing.T) {
	r, e := RuntimeAuthorityFromTrusted("instance", "boot", "epoch")
	if e != nil {
		t.Fatal(e)
	}
	a, e := GatewayAuthorityFromAuthenticated(r, "tenant", "device", "session", strings.Repeat("a", 64), 0)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(a.String(), strings.Repeat("a", 64)) || strings.Contains(r.String(), "boot") {
		t.Fatal("sensitive String")
	}
	if _, e = GatewayAuthorityFromAuthenticated(RuntimeAuthority{}, "tenant", "device", "session", strings.Repeat("a", 64), 0); e == nil {
		t.Fatal("untrusted runtime")
	}
	h := LeaseHandle{"tenant", "device", "session", "instance", "connection", "epoch", 1, time.Unix(100, 0)}
	if !validHandle(h) || !authorityHandle(a, h) {
		t.Fatal("valid exact tuple")
	}
	b := h
	b.LeaseUntil = b.LeaseUntil.Add(time.Second)
	if !sameHandle(h, b) {
		t.Fatal("observed deadline incorrectly part of CAS")
	}
	b.ConnectionID = "other"
	if sameHandle(h, b) {
		t.Fatal("physical connection omitted")
	}
	if _, e = NewLeaseStore(nil, func(context.Context) error { return nil }); e == nil {
		t.Fatal("nil runner")
	}
	if got := deadline(time.Unix(100, 0), 110000); !got.Equal(time.Unix(110, 0)) {
		t.Fatal("session clamp")
	}
}
func TestLeaseMigrationStaticContract(t *testing.T) {
	if len(migrations.Order) != 7 || migrations.Order[6] != "0007_gateway_lease_connection.sql" {
		t.Fatal("append-only migration order")
	}
	b, e := migrations.Files.ReadFile(migrations.Order[6])
	if e != nil {
		t.Fatal(e)
	}
	s := string(b)
	for _, marker := range []string{"LEASE_CONNECTION_MIGRATION_REQUIRES_RECONCILIATION", "device_one_nonrevoked_session", "device_one_unresolved_command", "dispatch_connection", "gateway_connection", "connection_id text NOT NULL", "OLD.state<>'active'", "TG_OP='DELETE'", "NEW.fence<OLD.fence"} {
		if !strings.Contains(s, marker) {
			t.Fatalf("missing static marker %s", marker)
		}
	}
	// Inventory only: no PostgreSQL parser/DDL execution is implied.
}
