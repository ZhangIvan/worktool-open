package admin

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestAdminCommandCatalogData（S8 回归，2026-11-04 review）：目录常量是管理台
// 表单与示例的唯一数据源（index.html 直接 JSON.parse(example)、按 required 生成
// 输入项），数据缺陷会静默破坏前端——本测试锁三条纪律：
//  1. example 必须是合法 JSON 且 "type" 与条目编号一致（防 {"type":511",...} 复发）；
//  2. required 只能是真实参数路径（防 ["messageId 或时间段"] 伪必填复发——
//    伪字段会生成一个不可能填对的输入框）；
//  3. 中文名与编号非零（防占位行混入）。
func TestAdminCommandCatalogData(t *testing.T) {
	seen := map[int]bool{}
	for _, c := range adminCommandCatalog {
		if c.Type == 0 || c.Name == "" {
			t.Fatalf("条目缺编号或中文名: %+v", c)
		}
		if seen[c.Type] {
			t.Fatalf("type=%d 重复", c.Type)
		}
		seen[c.Type] = true

		var ex map[string]any
		if err := json.Unmarshal([]byte(c.Example), &ex); err != nil {
			t.Fatalf("type=%d example 非法 JSON（%v）: %s", c.Type, err, c.Example)
		}
		if tf, ok := ex["type"].(float64); !ok || int(tf) != c.Type {
			t.Fatalf("type=%d example 的 type 字段缺失或不一致: %s", c.Type, c.Example)
		}
		for _, req := range c.Required {
			if strings.ContainsAny(req, " ") || strings.Contains(req, "或") {
				t.Fatalf("type=%d required 含伪字段 %q（须为真实参数路径，「二选一」类说明入 example）", c.Type, req)
			}
		}
	}
	if len(adminCommandCatalog) != 52 {
		t.Fatalf("目录应与 openapi 注册表同面 52 条，实际 %d", len(adminCommandCatalog))
	}
}

// TestCatalogExamplesFixtureInSync（2026-11-05 契约冻结车道）：目录 52 条 example
// 与跨语言交换夹具 protocol/fixtures/admin-command-examples.json 逐字节一致。
// 夹具是 Kotlin 侧 CatalogExampleContractTest（每条 example 经端侧执行器载荷解析
// 路径 IntentNormalizer 断言可消费）的唯一数据源——本测试锁「catalog 即夹具」，
// 防两端各改一份后目录示例再次漂移出端侧可消费面。type 集合相等 + example 字节
// 级相等 + name 一致（夹具仅承载展示名，防目录改名未同步）。
func TestCatalogExamplesFixtureInSync(t *testing.T) {
	raw, err := os.ReadFile("../../../protocol/fixtures/admin-command-examples.json")
	if err != nil {
		t.Fatalf("读取跨语言交换夹具失败（admin-command-examples.json 须与 catalog 同步维护）: %v", err)
	}
	var fixture struct {
		Commands []struct {
			Type    int    `json:"type"`
			Name    string `json:"name"`
			Example string `json:"example"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("夹具非法 JSON: %v", err)
	}
	if len(fixture.Commands) != len(adminCommandCatalog) {
		t.Fatalf("夹具条数 %d ≠ 目录 %d", len(fixture.Commands), len(adminCommandCatalog))
	}
	byType := map[int]adminCommandEntry{}
	for _, c := range adminCommandCatalog {
		byType[c.Type] = c
	}
	seen := map[int]bool{}
	for _, fc := range fixture.Commands {
		c, ok := byType[fc.Type]
		if !ok {
			t.Fatalf("夹具 type=%d 不在目录", fc.Type)
		}
		if seen[fc.Type] {
			t.Fatalf("夹具 type=%d 重复", fc.Type)
		}
		seen[fc.Type] = true
		if fc.Example != c.Example {
			t.Fatalf("type=%d 夹具 example 与目录漂移：\n夹具=%s\n目录=%s", fc.Type, fc.Example, c.Example)
		}
		if fc.Name != c.Name {
			t.Fatalf("type=%d 夹具 name=%q ≠ 目录 %q", fc.Type, fc.Name, c.Name)
		}
	}
	for _, c := range adminCommandCatalog {
		if !seen[c.Type] {
			t.Fatalf("目录 type=%d 未入夹具（Kotlin 侧消费断言缺该条）", c.Type)
		}
	}
}
