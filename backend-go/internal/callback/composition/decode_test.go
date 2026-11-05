package composition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func fixtureObject(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(rawFixture(t, fixture())))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func jsonObject(t *testing.T, m any) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func objectAt(m map[string]any, where string) map[string]any {
	switch where {
	case "root":
		return m
	case "destination":
		return m["destinations"].([]any)[0].(map[string]any)
	case "revision":
		return objectAt(m, "destination")["provider_revision"].(map[string]any)
	case "budgets":
		return m["budgets"].(map[string]any)
	case "runner", "worker", "pool":
		return objectAt(m, "budgets")[where].(map[string]any)
	case "role":
		return objectAt(m, "pool")["roles"].([]any)[0].(map[string]any)
	default:
		panic("test object")
	}
}

func rejected(t *testing.T, raw []byte) {
	t.Helper()
	p, err := Parse(raw, "")
	if err != ErrManifest || p.Identity() != "" || p.Enabled() || len(p.Destinations()) != 0 || len(p.Unresolved()) != 9 {
		t.Fatal("invalid input did not return empty disabled plan")
	}
	if err.Error() != "CALLBACK_PLAN_INVALID" {
		t.Fatal("error leaked input")
	}
}

func TestPlanExactNestedSchema(t *testing.T) {
	locations := []string{"root", "destination", "revision", "budgets", "runner", "worker", "pool", "role"}
	for _, where := range locations {
		for key := range objectAt(fixtureObject(t), where) {
			for _, mode := range []string{"missing", "null", "case_alias", "duplicate", "escaped_duplicate"} {
				t.Run(where+"_"+key+"_"+mode, func(t *testing.T) {
					m := fixtureObject(t)
					obj := objectAt(m, where)
					if mode == "missing" {
						delete(obj, key)
						rejected(t, jsonObject(t, m))
						return
					}
					if mode == "null" {
						obj[key] = nil
						rejected(t, jsonObject(t, m))
						return
					}
					if mode == "case_alias" {
						obj[strings.ToUpper(key)] = obj[key]
						delete(obj, key)
						rejected(t, jsonObject(t, m))
						return
					}
					// Inject duplicate keys only into the selected nested object's
					// bytes, so repeated field names in different scopes are distinct.
					old := jsonObject(t, obj)
					value := jsonObject(t, obj[key])
					dupKey := strconv.Quote(key)
					if mode == "escaped_duplicate" {
						dupKey = `"\u` + fmt.Sprintf("%04x", key[0]) + key[1:] + `"`
					}
					modified := append([]byte{'{'}, []byte(dupKey+":"+string(value)+",")...)
					modified = append(modified, old[1:]...)
					raw := jsonObject(t, m)
					if !bytes.Contains(raw, old) {
						t.Fatal("fixture subtree missing")
					}
					rejected(t, bytes.Replace(raw, old, modified, 1))
				})
			}
		}
		for _, extra := range []string{"enabled", "secret_bytes", "approval", "UNKNOWN_DO_NOT_ECHO"} {
			t.Run(where+"_unknown_"+extra, func(t *testing.T) {
				m := fixtureObject(t)
				objectAt(m, where)[extra] = "DO_NOT_ECHO_SECRET"
				rejected(t, jsonObject(t, m))
			})
		}
	}
}

func TestPlanStrictJSONBoundsAndNumbers(t *testing.T) {
	valid := rawFixture(t, fixture())
	inputs := [][]byte{nil, {}, []byte("null"), []byte("[]"), []byte("true"), []byte(`"text"`), []byte("{}"), []byte("{"), append(append([]byte{}, valid...), valid...), append(append([]byte{}, valid...), []byte(" garbage")...), []byte(`{"version":1,"version":1}`), bytes.Replace(valid, []byte("tenant_synthetic"), []byte{0xff}, 1), bytes.Replace(valid, []byte("tenant_synthetic"), []byte(`\ud800`), 1), bytes.Replace(valid, []byte("tenant_synthetic"), []byte(`\udc00`), 1), []byte(strings.Repeat("[", 66) + strings.Repeat("]", 66))}
	for i, raw := range inputs {
		t.Run(fmt.Sprintf("json_%d", i), func(t *testing.T) { rejected(t, raw) })
	}
	atCap := append(append([]byte{}, valid...), bytes.Repeat([]byte(" "), MaxManifestBytes-len(valid))...)
	if _, err := Parse(atCap, ""); err != nil {
		t.Fatal("exact byte cap should accept valid padding")
	}
	rejected(t, append(atCap, ' '))
	// Walk every numeric field, not only version. No lexical alternate or null
	// can silently turn into a valid/default scalar at any nesting level.
	for _, where := range []string{"root", "destination", "runner", "worker", "pool", "role"} {
		for key, value := range objectAt(fixtureObject(t), where) {
			if _, ok := value.(json.Number); !ok {
				continue
			}
			for _, bad := range []string{"-0", "-1", "1.0", "1e0", "1e100", "9223372036854775808", `"1"`, "true", "null", "[]", "{}"} {
				t.Run(where+"_"+key+"_"+bad, func(t *testing.T) {
					m := fixtureObject(t)
					obj := objectAt(m, where)
					before := jsonObject(t, obj)
					after := bytes.Replace(before, []byte(strconv.Quote(key)+":"+string(jsonObject(t, obj[key]))), []byte(strconv.Quote(key)+":"+bad), 1)
					rejected(t, bytes.Replace(jsonObject(t, m), before, after, 1))
				})
			}
		}
	}
}

func TestPlanStrictContainerAndStringTypes(t *testing.T) {
	for _, where := range []string{"root", "destination", "revision", "budgets", "runner", "worker", "pool", "role"} {
		for key, val := range objectAt(fixtureObject(t), where) {
			if _, ok := val.(json.Number); ok {
				continue
			}
			for i, wrong := range []any{nil, true, json.Number("1"), map[string]any{}, []any{}} {
				t.Run(fmt.Sprintf("%s_%s_type_%d", where, key, i), func(t *testing.T) {
					m := fixtureObject(t)
					objectAt(m, where)[key] = wrong
					rejected(t, jsonObject(t, m))
				})
			}
		}
	}
	for _, location := range []string{"destinations", "cidrs", "roles"} {
		for i, wrong := range []any{nil, true, json.Number("1"), "wrong", []any{}} {
			t.Run(fmt.Sprintf("%s_element_%d", location, i), func(t *testing.T) {
				m := fixtureObject(t)
				switch location {
				case "destinations":
					m["destinations"] = []any{wrong}
				case "cidrs":
					objectAt(m, "destination")["cidrs"] = []any{wrong}
				case "roles":
					objectAt(m, "pool")["roles"] = []any{wrong}
				}
				rejected(t, jsonObject(t, m))
			})
		}
	}
}

func TestPlanHasNoRuntimeSideEffectSurface(t *testing.T) {
	// This source guard complements behavioral tests. The package's only runtime
	// external helpers are the reviewed pure decoder, policy and budget methods.
	allowedImports := map[string]bool{"crypto/sha256": true, "encoding/hex": true, "encoding/json": true, "errors": true, "net/netip": true, "sort": true, "strings": true, "time": true, "strconv": true,
		"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security": true,
		"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol":          true,
		"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg":          true}
	for _, name := range []string{"planner.go", "decode.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !allowedImports[path] {
				t.Fatal("unreviewed runtime import")
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.GoStmt:
				t.Error("planner starts a goroutine")
			case *ast.FuncDecl:
				if node.Name.Name == "init" {
					t.Error("planner has init work")
				}
			case *ast.CallExpr:
				if selector, ok := node.Fun.(*ast.SelectorExpr); ok {
					if receiver, ok := selector.X.(*ast.Ident); ok {
						switch receiver.Name {
						case "pg":
							t.Error("planner constructs a PG object")
						case "time":
							if selector.Sel.Name != "Duration" {
								t.Error("planner uses runtime clock/timer")
							}
						case "security":
							if selector.Sel.Name != "NewPolicy" {
								t.Error("unreviewed security effect")
							}
						case "protocol":
							if selector.Sel.Name != "Decode" && selector.Sel.Name != "ExactObject" {
								t.Error("unreviewed protocol effect")
							}
						}
					}
				}
			}
			return true
		})
	}
}
