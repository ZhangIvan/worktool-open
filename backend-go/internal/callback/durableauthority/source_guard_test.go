package durableauthority

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition"
)

func TestFixedV1ProjectionEncoding(t *testing.T) {
	encoded, err := json.Marshal(bindingProjectionV1{"s", "a", "i", "b", "e", "p", 1, "plan", []string{"d"}})
	if err != nil || string(encoded) != `{"scope":"s","allocation":"a","instance":"i","boot":"b","epoch":"e","provenance_ref":"p","registry_revision":1,"plan_identity":"plan","descriptor_identities":["d"]}` {
		t.Fatal("v1 encoding drift")
	}
}

func TestNoProductionIssuerOrStartupWiring(t *testing.T) {
	imports := map[string]bool{"bytes": true, "context": true, "crypto/sha256": true, "encoding/hex": true, "encoding/json": true, "errors": true, "reflect": true, "sort": true, "time": true, "github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition": true, "github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration": true, "github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg": true, "github.com/jackc/pgx/v5": true, "github.com/jackc/pgx/v5/pgconn": true}
	exported := map[string]bool{"Bind": true, "New": true, "Observe": true, "String": true, "GoString": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range node.Imports {
			name, _ := strconv.Unquote(i.Path.Value)
			if !imports[name] {
				t.Fatalf("unreviewed import %s", name)
			}
		}
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.GoStmt:
				t.Error("detached work added")
			case *ast.FuncDecl:
				publicReceiver := x.Recv == nil
				if x.Recv != nil {
					var expr ast.Expr = x.Recv.List[0].Type
					if star, ok := expr.(*ast.StarExpr); ok {
						expr = star.X
					}
					if id, ok := expr.(*ast.Ident); ok {
						publicReceiver = ast.IsExported(id.Name)
					}
				}
				if x.Name.Name == "init" || publicReceiver && ast.IsExported(x.Name.Name) && !exported[x.Name.Name] {
					t.Errorf("unreviewed entry point %s", x.Name.Name)
				}
			case *ast.SelectorExpr:
				if x.Sel.Name == "NewWorker" || x.Sel.Name == "RuntimeAuthorityFromTrusted" || x.Sel.Name == "NewCallbackStore" {
					t.Error("runtime construction added")
				}
			}
			return true
		})
	}
	for _, root := range []string{"../../startup", "../../../cmd", "../runtimeassembly"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "callback/durableauthority") {
				t.Error("production wiring added")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"DURABLE_CONFIGURATION_BINDING", "EXACT_VERSION_SECRET_PROVIDER", "STICKY_GENERATION_AUTHORITY", "UTC_HEALTH_AUTHORITY", "APPROVED_DNS_AND_EGRESS", "WORKER_DATABASE_ROLE", "DURABLE_HALT_RECONCILIATION", "JOINT_LIFECYCLE_AND_RECOVERY_OWNERSHIP", "DEPLOYMENT_AND_RECEIVER_EVIDENCE"}
	if !reflect.DeepEqual((composition.Plan{}).Unresolved(), want) || (composition.Plan{}).Enabled() {
		t.Fatal("unresolved admission gates changed")
	}
}

func TestReadProjectionsBoundEveryVariableLengthResult(t *testing.T) {
	// This checks frozen SQL text, not PostgreSQL execution or physical wire bytes.
	bound := func(column, limits string) string {
		return "CASE WHEN octet_length(" + column + ") BETWEEN " + limits + " THEN " + column + " ELSE NULL END"
	}
	ids := []string{"scope_id", "allocation_id", "instance_id", "boot_id", "epoch_id"}
	registry := []string{}
	ledger := []string{}
	event := []string{}
	for _, id := range ids {
		registry = append(registry, bound(id, "1 AND 128"))
		ledger = append(ledger, bound("l."+id, "1 AND 128"))
		event = append(event, bound(id, "1 AND 128"))
	}
	registry = append(registry, bound("provenance_ref", "1 AND 128"), "registry_revision", bound("plan_identity", "81 AND 81"), bound("binding_identity", "64 AND 64"), bound("plan_raw", "1 AND 262144"), bound("descriptors_raw", "1 AND 524288"))
	ledger = append(ledger, "l.registry_revision", bound("l.binding_identity", "64 AND 64"), bound("l.state", "6 AND 7"), bound("l.halt_code", "0 AND 19"))
	event = append(event, "registry_revision", bound("binding_identity", "64 AND 64"), bound("kind", "6 AND 19"), bound("code", "6 AND 19"), bound("evidence_ref", "1 AND 128"))
	for _, test := range []struct {
		name, sql, suffix string
		projection        []string
		count             int
	}{{"registry", registrySQL, " FROM wt_synthetic_callback_authority.registry", registry, 10}, {"ledger", ledgerSQL, ",EXISTS(", ledger, 8}, {"event", eventSQL, " FROM wt_synthetic_callback_authority.events", event, 9}} {
		t.Run(test.name, func(t *testing.T) {
			want := "SELECT " + strings.Join(test.projection, ",") + test.suffix
			if !strings.HasPrefix(test.sql, want) || strings.Count(test.sql, "CASE WHEN octet_length(") != test.count || strings.Count(test.sql, " ELSE NULL END") != test.count {
				t.Fatal("unbounded, truncating or drifted result projection")
			}
		})
	}
}
