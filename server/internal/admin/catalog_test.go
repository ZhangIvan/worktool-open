package admin

import (
	"encoding/json"
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
