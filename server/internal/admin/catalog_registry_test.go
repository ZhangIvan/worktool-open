package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"cn.worktool.open/server/internal/api"
	"cn.worktool.open/server/internal/api/compat"
	"cn.worktool.open/server/internal/scheduler"
)

// 契约注册表机检（2026-11-04 契约注册表车道）：catalog.go 是跨端单一能力契约的
// 服务端注册表面——本文件把「目录与各契约源一致」从口头纪律变为回归测试：
//   1. 编号集合 = openapi 注册表（52 条集合相等）；
//   2. 每条 example 是合法 JSON（catalog_test.go 已锁）且经 compat 必填预校验
//      通过（含按 required 合成的最小合法载荷；命中 prevalidate 表的类型缺
//      字段必须报错——校验敏感性锁死）；
//   3. risk 与 scheduler.ClassifyRisk 逐条一致；
//   4. status 履历标记与 rawMsg 语义注记口径锁定；
//   5. ?withSchema=1 能力矩阵每个 required 路径均有类型且与 example 形态一致。

// TestCatalogRegistrySetMatchesOpenAPI 锁「52 条 catalog 与 openapi_registry 编号
// 集合一致」。api 侧不导出枚举面，集合相等由三段式证明：|catalog|=52 且无重复 +
// catalog ⊆ registry（OpenAPIIntent 逐条非空）+ |registry|=52（api 包
// openapi_rest_test.go 锁定）⟹ registry ⊆ catalog。
func TestCatalogRegistrySetMatchesOpenAPI(t *testing.T) {
	seen := map[int]bool{}
	for _, c := range adminCommandCatalog {
		if seen[c.Type] {
			t.Fatalf("type=%d 重复", c.Type)
		}
		seen[c.Type] = true
		if api.OpenAPIIntent(c.Type) == "" {
			t.Fatalf("type=%d（%s）不在 openapi 注册表——目录与注册表面漂移", c.Type, c.Name)
		}
	}
	if len(adminCommandCatalog) != 52 {
		t.Fatalf("目录应与 openapi 注册表同面 52 条（api 侧 52 条由 api 包测试锁定），实际 %d", len(adminCommandCatalog))
	}
}

// prevalidateCoveredTypes 为 compat/prevalidate.go requiredFields 现覆盖的提交
// 类型（来源：internal/api/compat/prevalidate.go 必填表；扩表时同步本集）。
var prevalidateCoveredTypes = map[int]bool{203: true, 206: true, 213: true, 220: true, 226: true, 234: true}

// rawAtPath 取 example JSON 的点号路径原始值（路径缺失 ok=false）。
func rawAtPath(ex map[string]json.RawMessage, path string) (json.RawMessage, bool) {
	node := ex
	parts := strings.Split(path, ".")
	for i, p := range parts {
		v, ok := node[p]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		sub := map[string]json.RawMessage{}
		if err := json.Unmarshal(v, &sub); err != nil {
			return nil, false
		}
		node = sub
	}
	return nil, false
}

// minimalPayload 按 required 从 example 取值合成最小合法载荷（{"type":N, …}）。
// example 缺 required 路径 = 目录缺陷，直接 Fatal（防「必填示例缺字段」复发）。
func minimalPayload(t *testing.T, c adminCommandEntry) string {
	t.Helper()
	var ex map[string]json.RawMessage
	if err := json.Unmarshal([]byte(c.Example), &ex); err != nil {
		t.Fatalf("type=%d example 非法 JSON: %v", c.Type, err)
	}
	typeRaw, _ := json.Marshal(c.Type)
	root := map[string]any{"type": json.RawMessage(typeRaw)}
	for _, p := range c.Required {
		raw, ok := rawAtPath(ex, p)
		if !ok {
			t.Fatalf("type=%d required %q 在 example 中无值（最小载荷不可合成）", c.Type, p)
		}
		node := root
		parts := strings.Split(p, ".")
		for i, seg := range parts {
			if i == len(parts)-1 {
				node[seg] = raw
				break
			}
			sub, ok := node[seg].(map[string]any)
			if !ok {
				sub = map[string]any{}
				node[seg] = sub
			}
			node = sub
		}
	}
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("type=%d 最小载荷序列化失败: %v", c.Type, err)
	}
	return string(b)
}

// TestCatalogExamplesPassPrevalidate：每条 example 与按 required 合成的最小合法
// 载荷都须通过 compat 必填预校验（无 missing 错误）；prevalidate 表内类型去掉
// 任一必填字段必须报错（校验敏感性——防包装函数恒 nil 的假通过）。
func TestCatalogExamplesPassPrevalidate(t *testing.T) {
	for _, c := range adminCommandCatalog {
		if err := compat.ValidateRequired(c.Type, c.Example); err != nil {
			t.Fatalf("type=%d example 未过 prevalidate: %v", c.Type, err)
		}
		payload := minimalPayload(t, c)
		if err := compat.ValidateRequired(c.Type, payload); err != nil {
			t.Fatalf("type=%d 最小合法载荷 %s 未过 prevalidate: %v", c.Type, payload, err)
		}
		if !prevalidateCoveredTypes[c.Type] {
			continue // 表外类型透传不校验（prevalidate.go 口径），敏感性断言仅表内类型
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			t.Fatalf("type=%d 最小载荷非 JSON 对象: %v", c.Type, err)
		}
		for _, p := range c.Required {
			trimmed := map[string]json.RawMessage{}
			for k, v := range obj {
				trimmed[k] = v
			}
			delete(trimmed, strings.Split(p, ".")[0]) // 摘除必填路径顶层键
			b, _ := json.Marshal(trimmed)
			if err := compat.ValidateRequired(c.Type, string(b)); err == nil {
				t.Fatalf("type=%d 缺必填 %q 却通过 prevalidate（校验未命中缺失）: %s", c.Type, p, string(b))
			}
		}
	}
}

// TestCatalogRiskMatchesClassifyRisk：风险级逐条 = scheduler.ClassifyRisk（单一
// 事实源口径；目录漂移 = 管理台展示与提交管线实际分级不一致）。
func TestCatalogRiskMatchesClassifyRisk(t *testing.T) {
	for _, c := range adminCommandCatalog {
		if want := scheduler.ClassifyRisk(c.Type); c.Risk != want {
			t.Fatalf("type=%d risk=%s ≠ ClassifyRisk=%s", c.Type, c.Risk, want)
		}
	}
}

// TestCatalogStatusLifecycle：status 词表锁定 + 当前口径（501/502/513=verified，
// 其余 implemented；registered 保留给 Unimplemented 执行器条目——52 条现均有
// 实装执行器，出现 registered 即登记错误）。
func TestCatalogStatusLifecycle(t *testing.T) {
	valid := map[string]bool{
		StatusRegistered: true, StatusImplemented: true, StatusWired: true, StatusVerified: true,
	}
	verified := map[int]bool{501: true, 502: true, 513: true}
	for _, c := range adminCommandCatalog {
		if !valid[c.Status] {
			t.Fatalf("type=%d status=%q 不在履历词表 registered/implemented/wired/verified", c.Type, c.Status)
		}
		if c.Status == StatusRegistered {
			t.Fatalf("type=%d status=registered：该值保留给 Unimplemented 执行器条目（现 52 条均实装）", c.Type)
		}
		want := StatusImplemented
		if verified[c.Type] {
			want = StatusVerified
		}
		if c.Status != want {
			t.Fatalf("type=%d status=%s ≠ 当前口径 %s（verified 集={501,502,513}）", c.Type, c.Status, want)
		}
	}
}

// TestCatalogRawMsgAnnotations：rawMsg 注记按结果数据通道三分（通道归属 =
// feature:executor ExecutorRegistry 出口绑定：querySink={501,502,509}、
// structured={503~508,511~515}，其余=指令原文回查），且 buildCommandCatalog
// 每条目输出 status/rawMsg 与目录一致。
func TestCatalogRawMsgAnnotations(t *testing.T) {
	querySink := map[int]bool{501: true, 502: true, 509: true}
	structured := map[int]bool{503: true, 504: true, 505: true, 506: true, 507: true, 508: true,
		511: true, 512: true, 513: true, 514: true, 515: true}
	for _, c := range adminCommandCatalog {
		want := rawMsgNotePayloadEcho
		switch {
		case querySink[c.Type]:
			want = rawMsgNoteQueryData
		case structured[c.Type]:
			want = rawMsgNoteStructured
		}
		if got := rawMsgNote(c.Type); got != want {
			t.Fatalf("type=%d rawMsg 注记口径错：got %q want %q", c.Type, got, want)
		}
	}
	items := buildCommandCatalog()
	if len(items) != len(adminCommandCatalog) {
		t.Fatalf("buildCommandCatalog 条数 %d ≠ 目录 %d", len(items), len(adminCommandCatalog))
	}
	for i, item := range items {
		c := adminCommandCatalog[i]
		if item["status"] != c.Status {
			t.Fatalf("type=%d 输出 status=%v ≠ 目录 %s", c.Type, item["status"], c.Status)
		}
		if item["rawMsg"] != rawMsgNote(c.Type) || item["rawMsg"] == "" {
			t.Fatalf("type=%d 输出 rawMsg=%v 与注记不符", c.Type, item["rawMsg"])
		}
	}
}

// TestCatalogSchemaMatrix：?withSchema=1 能力矩阵——每个 required 路径有类型且
// 与 example 值形态一致（string/array/object/boolean/number；空串 = 示例缺字段
// 即注册表缺陷）。
func TestCatalogSchemaMatrix(t *testing.T) {
	// 类型词表 = example 值形态全集（string/array/object/boolean/number——
	// 能力矩阵如实导出，不折损为 string；2026-11-05 契约冻结后现表内形态仅
	// string/array，boolean/number 保留为合法形态防词表收窄误伤后续条目）。
	okKinds := map[string]bool{"string": true, "array": true, "object": true, "boolean": true, "number": true}
	for _, c := range adminCommandCatalog {
		var ex map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.Example), &ex); err != nil {
			t.Fatalf("type=%d example 非法 JSON: %v", c.Type, err)
		}
		schema := commandParamSchema(c.Example, c.Required)
		types, _ := schema["required"].(map[string]any)
		if types == nil || len(types) != len(c.Required) {
			t.Fatalf("type=%d schema.required 覆盖 %v ≠ required %v", c.Type, types, c.Required)
		}
		for _, p := range c.Required {
			kind, _ := types[p].(string)
			if !okKinds[kind] {
				t.Fatalf("type=%d required %q 的类型 %q 非法（example 缺该路径或形态不可识别）", c.Type, p, kind)
			}
			if want := jsonKindAtPath(ex, p); kind != want {
				t.Fatalf("type=%d required %q 类型 %q ≠ example 形态 %q", c.Type, p, kind, want)
			}
		}
	}
}

// TestCatalogHandlerWithSchemaMatrix（HTTP 形态回归）：GET /api/v1/admin/catalog
// 恒带 status/rawMsg；?withSchema=1 时每条目附 schema.required（路径→类型），且
// 嵌套路径（friend.phone）类型正确；缺省请求不带 schema 键（向后兼容）。
func TestCatalogHandlerWithSchemaMatrix(t *testing.T) {
	f := newFixture(t)
	byType := map[int]adminCommandEntry{}
	for _, c := range adminCommandCatalog {
		byType[c.Type] = c
	}
	assertCommands := func(t *testing.T, out map[string]any, wantSchema bool) {
		t.Helper()
		cmds, ok := out["commands"].([]any)
		if !ok || len(cmds) != 52 {
			t.Fatalf("catalog commands 应为 52 条，got %T len=%v", out["commands"], len(cmds))
		}
		for _, raw := range cmds {
			item, _ := raw.(map[string]any)
			typ, _ := item["type"].(float64)
			c, known := byType[int(typ)]
			if !known {
				t.Fatalf("目录外 type=%v 出现在响应", typ)
			}
			if item["status"] != c.Status || item["status"] == "" {
				t.Fatalf("type=%d 响应 status=%v ≠ %s", c.Type, item["status"], c.Status)
			}
			if item["rawMsg"] != rawMsgNote(c.Type) {
				t.Fatalf("type=%d 响应 rawMsg=%v ≠ 注记", c.Type, item["rawMsg"])
			}
			schema, hasSchema := item["schema"].(map[string]any)
			if !wantSchema {
				if hasSchema {
					t.Fatalf("type=%d 缺省请求不应带 schema 键", c.Type)
				}
				continue
			}
			if !hasSchema {
				t.Fatalf("type=%d withSchema=1 未附 schema", c.Type)
			}
			types, _ := schema["required"].(map[string]any)
			if len(types) != len(c.Required) {
				t.Fatalf("type=%d schema.required=%v 覆盖 ≠ required %v", c.Type, types, c.Required)
			}
			for _, p := range c.Required {
				if k, _ := types[p].(string); k == "" {
					t.Fatalf("type=%d schema.required[%q] 缺类型", c.Type, p)
				}
			}
		}
	}
	code, out := f.call(t, "GET", "/api/v1/admin/catalog?withSchema=1", "", true)
	if code != 200 {
		t.Fatalf("withSchema=1 应 200，got %d", code)
	}
	assertCommands(t, out, true)
	// 抽样深断言：203（顶层 string+array）与 213（嵌套 friend.phone=string）。
	cmds, _ := out["commands"].([]any)
	sample := map[int]map[string]string{}
	for _, raw := range cmds {
		item, _ := raw.(map[string]any)
		typ := int(item["type"].(float64))
		if typ == 203 || typ == 213 {
			schema, _ := item["schema"].(map[string]any)
			types, _ := schema["required"].(map[string]any)
			m := map[string]string{}
			for k, v := range types {
				m[k], _ = v.(string)
			}
			sample[typ] = m
		}
	}
	if sample[203]["titleList"] != "array" || sample[203]["receivedContent"] != "string" {
		t.Fatalf("203 schema 类型错: %v", sample[203])
	}
	if sample[213]["friend.phone"] != "string" {
		t.Fatalf("213 嵌套路径类型错: %v", sample[213])
	}
	code, out = f.call(t, "GET", "/api/v1/admin/catalog", "", true)
	if code != 200 {
		t.Fatalf("缺省 catalog 应 200，got %d", code)
	}
	assertCommands(t, out, false)
}
