/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package admin

import (
	"encoding/json"
	"strings"
)

// adminCommandCatalog 为管理台指令目录常量（2026-11-04 所有者反馈②③）。
// 编号面=openapi 注册表（52 条，openapi_registry.go——两处同步维护）；中文名与
// scheduler/dryrun.go plannedActionNames 同源；风险级取 ClassifyRisk 口径
// （L1={206,207,213,220,227,228,229,230}、L2={219,234,304}∪{236,237,238,239,240,244
// ——S7 补缺六条显式钉 L2（scheduler/guard.go riskClassByType），其余 L0；
// docs/01 FR-53 裁决③基线）；required=compat/prevalidate.go 必填表；
// example=最小可提交示例（合成值）。排序=编号升序；group=发送页分组。
//
// 机器可校验注册表（2026-11-04 契约注册表车道）：每条目另带 status 能力履历
// 标记（Status* 常量）与 rawMsg 语义注记（rawMsgNote，按结果数据通道推导）；
// 不变量由 catalog_registry_test.go 逐条机检（编号集合=registry、example 过
// prevalidate、risk=ClassifyRisk、status/rawMsg 口径）。rawMsg 注记为推导值
// （单一事实源=结果通道装配，见 rawMsgNote 注），不入表避免 52 行重复漂移。
type adminCommandEntry struct {
	Type     int
	Name     string
	Risk     string
	Required []string
	Example  string
	Group    string
	// Status 能力履历标记（StatusRegistered/Implemented/Wired/Verified）。
	// 当前口径（2026-11-04）：501/502/513=verified（真机验证通过），其余全部
	// implemented（端侧执行器均已在 feature:executor defaultFullBatch 实装）；
	// registered 保留给 Unimplemented 执行器条目（235/243 等排期批入册时使用，
	// 现目录无）。
	Status string
}

// 指令能力履历标记（终态=verified；演进方向 registered→implemented→wired→verified）。
const (
	// StatusRegistered 已注册（REST 面可提交）但端侧执行器
	// UnimplementedCommandExecutor/未实现——提交放行而端侧不可执行，UI 须明示。
	StatusRegistered = "registered"
	// StatusImplemented 端侧执行器已实装（ExecutorRegistry defaultFullBatch）。
	StatusImplemented = "implemented"
	// StatusWired 管线端到端贯通（真机联调通过，含结果回执链路）。
	StatusWired = "wired"
	// StatusVerified 真机验证通过（结果数据本体经实测核对）。
	StatusVerified = "verified"
)

// rawMsg 语义注记（回执/回调中 rawMsg 字段在本条目下的语义；口径 = api/outbox.go
// compatExecResultBody「端侧携 rawMsg 即透传、缺失回落指令原文」+ wire.ExecResult.RawMsg）。
const (
	// rawMsgNoteQueryData：端侧 QueryResultSink 出口执行器（501/502/509，
	// ExecutorRegistry 装配）——查询结果数据本体经 RAW_CONFIRM rawMsg 透传。
	rawMsgNoteQueryData = "查询结果数据本体（端侧 RAW_CONFIRM rawMsg 透传：名单/文件元数据 JSON，api-44952776）"
	// rawMsgNoteStructured：端侧 StructuredResultSink 出口执行器（503~508/511~515）
	// ——数据本体经 socketType=2 结构化上行另行推送（511/512/513/515 → query_result
	// 事件回调，events.md §五）；本条目回执 rawMsg 缺省=指令原文。
	rawMsgNoteStructured = "数据本体经端侧结构化上行另行推送（socketType=2；511/512/513/515 → query_result 事件回调）；回执 rawMsg=指令原文"
	// rawMsgNotePayloadEcho：动作/运维类——回执 rawMsg=指令原文（commands.payload
	// 回查，compat 层缺失回填）。
	rawMsgNotePayloadEcho = "回执 rawMsg=指令原文（commands.payload 回查，compat 缺失回填）"
)

// rawMsgNote 返回条目的 rawMsg 语义注记（按结果数据通道三分；通道归属 =
// feature:executor ExecutorRegistry 出口绑定：querySink={501,502,509}、
// structured={503~508,511~515}，510 上传日志与动作类无数据本体轨道）。
func rawMsgNote(commandType int) string {
	switch commandType {
	case 501, 502, 509:
		return rawMsgNoteQueryData
	case 503, 504, 505, 506, 507, 508, 511, 512, 513, 514, 515:
		return rawMsgNoteStructured
	default:
		return rawMsgNotePayloadEcho
	}
}

// jsonKindAtPath 取 example JSON 对象中点号路径值的参数类型（string/array/object/
// number/boolean；路径缺失或类型未知 → 空串。嵌套路径如 friend.phone）。
func jsonKindAtPath(ex map[string]json.RawMessage, path string) string {
	parts := strings.Split(path, ".")
	var raw json.RawMessage = nil
	node := ex
	for i, p := range parts {
		v, ok := node[p]
		if !ok {
			return ""
		}
		if i == len(parts)-1 {
			raw = v
			break
		}
		if err := json.Unmarshal(v, &node); err != nil || node == nil {
			return ""
		}
	}
	if raw == nil {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "string"
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		return "array"
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		return "object"
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return "number"
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return "boolean"
	}
	return ""
}

// commandParamSchema 为条目的参数类型表（能力矩阵，?withSchema=1 输出）：每个
// required 路径 → 参数类型（由 example 值形态导出——example 经注册表校验测试
// 锁定为最小合法载荷，类型与 compat prevalidate 必填表传递一致）。路径缺失
// → ""（表内必填而示例缺字段 = 注册表缺陷，校验测试拦截）。
func commandParamSchema(example string, required []string) map[string]any {
	ex := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(example), &ex) // example 合法性由注册表测试锁定
	types := make(map[string]any, len(required))
	for _, p := range required {
		types[p] = jsonKindAtPath(ex, p)
	}
	return map[string]any{"required": types}
}

var adminCommandCatalog = []adminCommandEntry{
	{201, "停止任务回首页", "L0", nil, `{"type":201}`, "会话与循环", StatusImplemented},
	{202, "主循环开关", "L0", []string{"loop.on"}, `{"type":202,"loop":{"on":false}}`, "会话与循环", StatusImplemented},
	{203, "发送文本消息", "L0", []string{"titleList", "receivedContent"}, `{"type":203,"titleList":["成员甲"],"receivedContent":"你好"}`, "消息·发送", StatusImplemented},
	{204, "引用回复消息", "L0", []string{"titleList", "receivedContent"}, `{"type":204,"titleList":["成员甲"],"receivedContent":"收到"}`, "消息·发送", StatusImplemented},
	{205, "转发消息", "L0", []string{"titleList"}, `{"type":205,"titleList":["成员甲"],"originTitle":"来源会话"}`, "消息·发送", StatusImplemented},
	{206, "创建群（拉人/公告/备注）", "L1", []string{"groupName", "selectList"}, `{"type":206,"groupName":"交付群","selectList":["成员甲"]}`, "群·写操作", StatusImplemented},
	{207, "修改群配置（含拉人/踢人）", "L1", []string{"groupName"}, `{"type":207,"groupName":"交付群","groupAnnouncement":"新公告"}`, "群·写操作", StatusImplemented},
	{208, "推送微盘图片", "L0", []string{"titleList", "fileUrl"}, `{"type":208,"titleList":["成员甲"],"fileUrl":"https://example.com/a.png"}`, "文件·推送", StatusImplemented},
	{209, "推送微盘文件", "L0", []string{"titleList", "fileUrl"}, `{"type":209,"titleList":["成员甲"],"fileUrl":"https://example.com/a.pdf"}`, "文件·推送", StatusImplemented},
	{210, "推送小程序", "L0", []string{"titleList"}, `{"type":210,"titleList":["成员甲"],"fileUrl":"https://example.com/mp"}`, "文件·推送", StatusImplemented},
	{211, "推送腾讯文档", "L0", []string{"titleList", "fileUrl"}, `{"type":211,"titleList":["成员甲"],"fileUrl":"https://example.com/doc"}`, "文件·推送", StatusImplemented},
	{212, "通过全部待处理好友请求", "L0", nil, `{"type":212}`, "联系人", StatusImplemented},
	{213, "按手机号添加好友", "L1", []string{"friend.phone"}, `{"type":213,"friend":{"phone":"13800000000","leavingMsg":"你好"}}`, "联系人", StatusImplemented},
	{214, "展示群信息", "L0", []string{"groupName"}, `{"type":214,"groupName":"交付群"}`, "群·查询", StatusImplemented},
	{218, "推送文件", "L0", []string{"titleList", "fileUrl"}, `{"type":218,"titleList":["成员甲"],"fileUrl":"https://example.com/f.bin"}`, "文件·推送", StatusImplemented},
	{219, "解散群", "L2", []string{"groupName"}, `{"type":219,"groupName":"交付群"}`, "群·高风险", StatusImplemented},
	{220, "从群添加好友", "L1", []string{"groupName", "friend.name"}, `{"type":220,"groupName":"交付群","friend":{"name":"成员乙"}}`, "联系人", StatusImplemented},
	{221, "添加待办", "L0", []string{"titleList", "receivedContent"}, `{"type":221,"titleList":["成员甲"],"receivedContent":"待办内容"}`, "效率", StatusImplemented},
	{222, "打卡", "L0", nil, `{"type":222}`, "效率", StatusImplemented},
	{223, "切换企业", "L0", []string{"corpName"}, `{"type":223,"corpName":"目标企业名"}`, "会话与循环", StatusImplemented},
	{224, "推送链接", "L0", []string{"titleList", "fileUrl"}, `{"type":224,"titleList":["成员甲"],"fileUrl":"https://example.com"}`, "文件·推送", StatusImplemented},
	{225, "修改群成员备注/标签", "L0", []string{"groupName", "selectList"}, `{"type":225,"groupName":"交付群","selectList":["成员乙"],"friend":{"markName":"重要"}}`, "群·写操作", StatusImplemented},
	{226, "撤回消息", "L0", []string{"titleList", "originalContent"}, `{"type":226,"titleList":["成员甲"],"originalContent":"被撤回消息ID"}`, "消息·管理", StatusImplemented},
	{227, "批量转发", "L1", []string{"titleList"}, `{"type":227,"titleList":["成员甲"],"originTitle":"来源"}`, "消息·发送", StatusImplemented},
	{228, "合并转发", "L1", []string{"titleList"}, `{"type":228,"titleList":["成员甲"],"originTitle":"来源"}`, "消息·发送", StatusImplemented},
	{229, "批量发送", "L1", []string{"titleList", "receivedContent"}, `{"type":229,"titleList":["成员甲","成员乙"],"receivedContent":"通知"}`, "消息·发送", StatusImplemented},
	{230, "合并发送", "L1", []string{"titleList"}, `{"type":230,"titleList":["成员甲"],"messageList":[{"originalContent":"内容一"}]}`, "消息·发送", StatusImplemented},
	{233, "扫一扫", "L0", nil, `{"type":233}`, "效率", StatusImplemented},
	{234, "删除联系人", "L2", []string{"friend.name"}, `{"type":234,"friend":{"name":"成员丙"}}`, "联系人·高风险", StatusImplemented},
	{236, "删除单向联系人", "L2", []string{"friend.name"}, `{"type":236,"friend":{"name":"成员丙"}}`, "联系人", StatusImplemented},
	{237, "清理企微缓存", "L2", nil, `{"type":237}`, "运维·高风险", StatusImplemented},
	{238, "批量发送文本", "L2", []string{"titleList", "receivedContent"}, `{"type":238,"titleList":["成员甲"],"receivedContent":"批量内容"}`, "消息·发送", StatusImplemented},
	{239, "创建客户群活码", "L2", []string{"groupName"}, `{"type":239,"groupName":"客户群"}`, "群·写操作", StatusImplemented},
	{240, "添加群公告网页", "L2", []string{"groupName"}, `{"type":240,"groupName":"交付群","fileUrl":"https://example.com/board"}`, "群·写操作", StatusImplemented},
	{244, "退出群", "L2", []string{"groupName"}, `{"type":244,"groupName":"交付群"}`, "群·写操作", StatusImplemented},
	{303, "接口连通测试", "L0", nil, `{"type":303}`, "运维", StatusImplemented},
	{304, "清空待执行队列", "L2", nil, `{"type":304}`, "队列", StatusImplemented},
	{501, "查询群信息（按名单）", "L0", []string{"selectList"}, `{"type":501,"selectList":["成员甲、成员乙、成员丙、成员丁"]}`, "查询", StatusVerified},
	{502, "查询好友列表", "L0", nil, `{"type":502}`, "查询", StatusVerified},
	{503, "查询我的信息", "L0", nil, `{"type":503}`, "查询", StatusImplemented},
	{504, "获取群二维码", "L0", []string{"groupName"}, `{"type":504,"groupName":"交付群"}`, "查询", StatusImplemented},
	{505, "查询最近会话", "L0", nil, `{"type":505}`, "查询", StatusImplemented},
	{506, "查询企业列表", "L0", nil, `{"type":506}`, "查询", StatusImplemented},
	{507, "查询全部好友", "L0", nil, `{"type":507}`, "查询", StatusImplemented},
	{508, "查询全部群", "L0", nil, `{"type":508}`, "查询", StatusImplemented},
	{509, "上传本地文件/日志", "L0", []string{"filePath"}, `{"type":509,"filePath":"/sdcard/Download/文件.pdf"}`, "运维", StatusImplemented},
	{510, "上传运行日志包", "L0", nil, `{"type":510,"logDate":"2026-11-04"}`, "运维", StatusImplemented},
	{511, "查询会议信息", "L0", nil, `{"type":511,"objectName":"2026-11-04"}`, "查询", StatusImplemented},
	{512, "查询群成员信息", "L0", []string{"groupName"}, `{"type":512,"groupName":"交付群"}`, "查询", StatusImplemented},
	{513, "查询设备资源", "L0", nil, `{"type":513}`, "查询", StatusVerified},
	{514, "查询日志片段", "L0", nil, `{"type":514,"messageId":"消息ID（或按时间段查询，与 messageId 二选一）"}`, "运维", StatusImplemented},
	{515, "查询群名信息", "L0", []string{"groupName"}, `{"type":515,"groupName":"交付群"}`, "查询", StatusImplemented},
}
