// 101011 必填预校验表（T3.6；13 项字段缺失清单第 7/13 条骨架）。
//
// 上游对缺必填字段在提交时即返回 101011「数据格式错误」；我方既有 /openapi/v1
// 面只校验顶层 type（载荷原文透传，错误延后到设备执行回执）。本表把文档各端点
// 的必填字段前置到提交时校验（fail-fast 整单拒绝，对齐上游语义）。
//
// 覆盖口径（本卡 = 已有 crawl 存证页明确 required 清单的 6 类；其余端点必填表
// 随 B2/B3 契约冻结逐条补全——13 项清单第 7 条登记为「本卡骨架+B2/B3 全量」）：
//   - 203 发送消息：titleList + receivedContent（api-23520034）；
//   - 206 创建外部群：groupName + selectList（api-23520350）；
//   - 213 手机号加好友：friend.phone（api-25405464）；
//   - 220 从外部群加好友：groupName + friend.name（api-48642563）；
//   - 226 消息撤回：titleList + originalContent（字段级比对报告 §1.1 行 14）；
//   - 234 删除联系人：friend.name（api-104075163）。
//
// 配额组（第 13 条：213 ≤100 人次/天、218 ≤9 目标/>10M 拒/webp 拒、236 ≤50/次、
// 撤回 2 分钟）需要 DB 计数器与文件探测，dodDeferred 归 B2/B3；本卡仅闭环
// 批量 list ≤100（router.go 信封层）与 up:true 识别（router.go 提交层）。
package compat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// fieldKind 为必填字段的形态校验枚举。
type fieldKind string

const (
	kindString fieldKind = "string" // 非空字符串
	kindArray  fieldKind = "array"  // 非空数组（元素不限定）
	kindObject fieldKind = "object" // 非空对象（子字段另行校验）
)

// fieldSpec 为一条必填字段规格（path 支持一级点号嵌套，如 friend.phone）。
type fieldSpec struct {
	Path string
	Kind fieldKind
	Desc string
}

// requiredFields 为 type → 必填字段表（来源见文件头；B2/B3 批随契约冻结补全）。
var requiredFields = map[int][]fieldSpec{
	203: {
		{Path: "titleList", Kind: kindArray, Desc: "昵称或群名"},
		{Path: "receivedContent", Kind: kindString, Desc: "发送文本内容"},
	},
	206: {
		{Path: "groupName", Kind: kindString, Desc: "群名"},
		{Path: "selectList", Kind: kindArray, Desc: "拉人名单"},
	},
	213: {
		{Path: "friend.phone", Kind: kindString, Desc: "手机号"},
	},
	220: {
		{Path: "groupName", Kind: kindString, Desc: "外部群名"},
		{Path: "friend.name", Kind: kindString, Desc: "好友名称"},
	},
	226: {
		{Path: "titleList", Kind: kindArray, Desc: "昵称或群名"},
		{Path: "originalContent", Kind: kindString, Desc: "被撤回消息 messageId"},
	},
	234: {
		{Path: "friend.name", Kind: kindString, Desc: "联系人名称"},
	},
}

// validateRequired 对单条 list 项做必填校验（表外类型 = 透传不校验，维持既有
// 宽容面）。命中缺失/形态不符返回 101011 语义错误（调用方整单拒绝）。
func validateRequired(commandType int, item map[string]json.RawMessage) error {
	specs, ok := requiredFields[commandType]
	if !ok {
		return nil // 表外类型：不预校验（B2/B3 补全前维持透传）
	}
	for _, sp := range specs {
		parts := strings.SplitN(sp.Path, ".", 2)
		raw, ok := item[parts[0]]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			return fmt.Errorf("list 项缺必填字段 %s（type=%d，%s）", sp.Path, commandType, sp.Desc)
		}
		if len(parts) == 2 {
			var sub map[string]json.RawMessage
			if err := json.Unmarshal(raw, &sub); err != nil {
				return fmt.Errorf("list 项字段 %s 须为对象（type=%d）", parts[0], commandType)
			}
			raw, ok = sub[parts[1]]
			if !ok || len(raw) == 0 || string(raw) == "null" {
				return fmt.Errorf("list 项缺必填字段 %s（type=%d，%s）", sp.Path, commandType, sp.Desc)
			}
		}
		switch sp.Kind {
		case kindString:
			var s string
			if err := json.Unmarshal(raw, &s); err != nil || strings.TrimSpace(s) == "" {
				return fmt.Errorf("list 项字段 %s 须为非空字符串（type=%d，%s）", sp.Path, commandType, sp.Desc)
			}
		case kindArray:
			var arr []json.RawMessage
			if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
				return fmt.Errorf("list 项字段 %s 须为非空数组（type=%d，%s）", sp.Path, commandType, sp.Desc)
			}
		case kindObject:
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
				return fmt.Errorf("list 项字段 %s 须为非空对象（type=%d，%s）", sp.Path, commandType, sp.Desc)
			}
		}
	}
	return nil
}

// ValidateRequired 为 validateRequired 的导出包装（载荷字符串形态）：解析 JSON
// 对象后走兼容面提交同一张必填表。admin 注册表一致性校验消费（catalog
// example/最小载荷经同一必填表机检——单一事实源，不旁路不复刻）。payload 须
// 为 JSON 对象，解析失败返回错误；表外类型 = 透传不校验（与提交路径同语义）。
func ValidateRequired(commandType int, payload string) error {
	var item map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return fmt.Errorf("载荷须为 JSON 对象: %w", err)
	}
	return validateRequired(commandType, item)
}
