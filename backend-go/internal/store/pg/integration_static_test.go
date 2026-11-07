package pg

import (
	"github.com/ZhangIvan/worktool-open/backend-go/migrations"
	"strings"
	"testing"
)

// Text inventory checks only. This does NOT parse or execute PostgreSQL SQL.
func TestCandidateMigrationStaticInventory(t *testing.T) {
	all := ""
	for _, n := range migrations.Order {
		b, e := migrations.Files.ReadFile(n)
		if e != nil {
			t.Fatal(e)
		}
		all += string(b)
	}
	if got := strings.Count(all, "CREATE TABLE "); got != 30 {
		t.Fatalf("candidate table count=%d, expected30", got)
	}
	for _, required := range []string{"callback_destination_versions", "subscription_exact_destination_version", "CALLBACK_LEGACY_HISTORY_REQUIRES_RECONCILIATION", "DESTINATION_REVOCATION_TERMINAL", "SET search_path TO pg_catalog, %I, pg_temp", "commands_payload_json_object", "PAYLOAD_CHECK_GUARD_DEFINITION", "delivery_reserved_identity", "reservation_exact_delivery", "DEFERRABLE INITIALLY DEFERRED", "UNIQUE(tenant,reservation_id)", "FOREIGN KEY(tenant,device,session)", "FOREIGN KEY(tenant,device,command)", "role IN ('owner','admin','operator','approver','auditor')"} {
		if !strings.Contains(all, required) {
			t.Errorf("missing static marker %s", required)
		}
	}
	if strings.Contains(strings.ToUpper(all), "CREATE ROLE ") || strings.Contains(strings.ToUpper(all), "CREATE USER ") {
		t.Fatal("migrations must not create identities")
	}
}
