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
// docs/01 FR-53 裁决③基线）；required=该指令最小可提交必填字段（=端侧执行器
// 载荷解析必填口径，compat/prevalidate.go 必填表为其提交面子集——203/206/213/
// 220/226/234 已入提交预校验）；example=最小可提交示例（合成值，端侧可消费）。
// 排序=编号升序；group=发送页分组。
//
// 机器可校验注册表（2026-11-04 契约注册表车道）：每条目另带 status 能力履历
// 标记（Status* 常量）与 rawMsg 语义注记（rawMsgNote，按结果数据通道推导）；
// 不变量由 catalog_registry_test.go 逐条机检（编号集合=registry、example 过
// prevalidate、risk=ClassifyRisk、status/rawMsg 口径）。rawMsg 注记为推导值
// （单一事实源=结果通道装配，见 rawMsgNote 注），不入表避免 52 行重复漂移。
//
// ---- 指令字段契约冻结（2026-11-05 契约冻结车道，docs/16 同名节） ----
//
// 52 条 example 逐一对照端侧执行器载荷解析路径（feature:executor
// IntentNormalizer——设备侧唯一线上字段读取点）审计：22 条漂移，30 条一致。
// 决策规则：默认以执行器语义为准修 example；仅当 example 原字段名更符合
// upstream openapi 契约（local-private/parity apifox 字段级比对 + upstream
// WeworkController @param 注解）时改执行器读取并在对应条目注释登记迁移。
// 全表决策注记（逐条目理由见各 entry 行注释；端侧消费由跨语言夹具
// protocol/fixtures/admin-command-examples.json + Kotlin CatalogExampleContractTest
// 机检，夹具↔本表同步由 TestCatalogExamplesFixtureInSync 锁定）：
//   1. catalog example 改（字段幻影/缺漏，17 条）：202（loop.on→loopEnabled，
//      upstream 202 无字段、我方 B1 开关扩展自拟名）；204/205/208/209/218/227/
//      228/229/230/238（字段缺漏或 originTitle/titleList 错位——upstream 契约
//      与执行器同名，见各条）；210/211/224（补 objectName 等执行器必填——
//      210/211/224 为 WorkTool-Open 自拟 card schema，upstream originalContent
//      映射留 T3.2 候选）；223（corpName→objectName，upstream api-59089854
//      schema required）；514（messageId→objectName，upstream doc-9564531 §514）。
//   2. catalog Required 补齐执行器必填（2 条）：511（objectName 必填未标）、
//      514（同前）。
//   3. 执行器迁移（example 原字段更合 upstream 契约，4 指令）：203
//      （targetRoom→titleList 主读）、206（groupMembers→selectList 主读）、
//      207/225（targetRoom→groupName 主读）——IntentNormalizer 决策注释；
//      候选名 targetRoom 降级迁移期只读别名（T3.2 apifox 冒烟后撤），
//      groupMembers 退役不再读取。
type adminCommandEntry struct {
	Type     int
	Name     string
	Risk     string
	Required []string
	Example  string
	Group    string
	// Status 能力履历标记（StatusRegistered/Implemented/Wired/Verified）。
	// 当前口径（2026-11-04）：501/502/513=verified，其余全部
	// implemented（端侧执行器均已在 feature:executor defaultFullBatch 实装）；
	// registered 保留给 Unimplemented 执行器条目（235/243 等排期批入册时使用，
	// 现目录无）。verified 与支持矩阵翻格门不是同一口径：501/502=回执+数据本体
	// 双面真机核对（矩阵 ✅，docs/05）；513=仅回执终态面（succeeded ec=0 ×2，
	// local-private/evidence/session10、session11）——数据本体 query_result 回调
	// 未实测捕获、含 H1/H2 构建复测未收敛，矩阵侧 513 行 ⬜ 不翻（docs/05 v1.10
	// 证据口径节专项条+513 行，差异显式登记非漂移）。
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
	// 202 冻结决策（2026-11-05）：upstream 202=无字段（仅重启主循环）；我方 B1
	// 开关扩展为执行器语义权威——开关字段=loopEnabled（string "true"/"false"，
	// fail-closed）；catalog 原 loop.on 为两侧均不消费的幻影字段 → 修 example。
	{202, "主循环开关", "L0", []string{"loopEnabled"}, `{"type":202,"loopEnabled":"false"}`, "会话与循环", StatusImplemented},
	// 203 冻结决策（2026-11-05）：执行器迁移例——原执行器读候选名 targetRoom，
	// 而 catalog/prevalidate/upstream api-23520034（titleList:string[]，建议单目标）
	// 同为 titleList → IntentNormalizer 改 titleList 主读（多元素显式拒绝，
	// targetRoom 降级迁移期别名）。example 本来就是冻结形态，不变。
	{203, "发送文本消息", "L0", []string{"titleList", "receivedContent"}, `{"type":203,"titleList":["成员甲"],"receivedContent":"你好"}`, "消息·发送", StatusImplemented},
	// 204 冻结决策（2026-11-05）：example 缺 upstream/执行器同名的
	// receivedName+originalContent（引用定位键，非空断言）→ 补齐。
	{204, "引用回复消息", "L0", []string{"titleList", "receivedName", "originalContent", "receivedContent"}, `{"type":204,"titleList":["成员甲"],"receivedName":"发送人昵称","originalContent":"被引用消息原文","receivedContent":"收到"}`, "消息·发送", StatusImplemented},
	// 205 冻结决策（2026-11-05）：originTitle 为两侧均不消费的幻影字段
	// （upstream=receivedName+originalContent 定位原文，nameList=接收人）→
	// 修 example 对齐 upstream/执行器同名四必填。
	{205, "转发消息", "L0", []string{"titleList", "receivedName", "originalContent", "nameList"}, `{"type":205,"titleList":["来源会话"],"receivedName":"发送人昵称","originalContent":"消息原文","nameList":["成员乙"]}`, "消息·发送", StatusImplemented},
	// 206 冻结决策（2026-11-05）：执行器迁移例——原执行器成员读候选名
	// groupMembers（目录按 selectList 提交被端侧静默忽略=拉人名单丢失），
	// upstream initGroup「selectList 添加群成员名称列表」= catalog 同名 →
	// IntentNormalizer 改 selectList 主读，groupMembers 退役。example 不变。
	{206, "创建群（拉人/公告/备注）", "L1", []string{"groupName", "selectList"}, `{"type":206,"groupName":"交付群","selectList":["成员甲"]}`, "群·写操作", StatusImplemented},
	// 207 冻结决策（2026-11-05）：目标字段=执行器迁移例（targetRoom→groupName
	// 主读，upstream updateGroup「groupName 待修改的群」）；公告字段 catalog 原
	// groupAnnouncement 为 upstream 206 的字段名（207 upstream=newGroupAnnouncement，
	// catalog 与 upstream 亦不符）→ example 从执行器自拟名 announcement。
	{207, "修改群配置（含拉人/踢人）", "L1", []string{"groupName", "announcement"}, `{"type":207,"groupName":"交付群","announcement":"新公告"}`, "群·写操作", StatusImplemented},
	// 208 冻结决策（2026-11-05）：fileUrl 为幻影字段（微盘推送不下载——
	// upstream api-23520748=type,titleList,objectName(微盘里存在)；执行器读
	// objectName）→ 修 example。
	{208, "推送微盘图片", "L0", []string{"titleList", "objectName"}, `{"type":208,"titleList":["成员甲"],"objectName":"微盘图片.png"}`, "文件·推送", StatusImplemented},
	{209, "推送微盘文件", "L0", []string{"titleList", "objectName"}, `{"type":209,"titleList":["成员甲"],"objectName":"微盘文件.pdf"}`, "文件·推送", StatusImplemented},
	// 210 冻结决策（2026-11-05）：执行器 card schema 必填 objectName(标题)+
	// fileUrl(跳转)；catalog 缺 objectName → 补。upstream pushMicroprogram 用
	// originalContent 承载链接（我方自拟 fileUrl 留 T3.2 候选）。
	{210, "推送小程序", "L0", []string{"titleList", "objectName", "fileUrl"}, `{"type":210,"titleList":["成员甲"],"objectName":"小程序名","fileUrl":"https://example.com/mp"}`, "文件·推送", StatusImplemented},
	// 211 冻结决策（2026-11-05）：执行器 card schema 必填 objectName+fileUrl
	// （upstream pushOffice 按文档名检索无 URL——我方等价实现保留 URL 通道，
	// docs/10 B4 已登记）→ example 补 objectName。
	{211, "推送腾讯文档", "L0", []string{"titleList", "objectName", "fileUrl"}, `{"type":211,"titleList":["成员甲"],"objectName":"交付文档","fileUrl":"https://example.com/doc"}`, "文件·推送", StatusImplemented},
	{212, "通过全部待处理好友请求", "L0", nil, `{"type":212}`, "联系人", StatusImplemented},
	{213, "按手机号添加好友", "L1", []string{"friend.phone"}, `{"type":213,"friend":{"phone":"13800000000","leavingMsg":"你好"}}`, "联系人", StatusImplemented},
	{214, "展示群信息", "L0", []string{"groupName"}, `{"type":214,"groupName":"交付群"}`, "群·查询", StatusImplemented},
	// 218 冻结决策（2026-11-05）：upstream api-43191166=type,titleList,objectName,
	// fileUrl,fileType(image/audio/video/*) 全必填——执行器同读此五项，example 缺
	// objectName+fileType → 补齐（同 objectName 防缓存覆盖、fileType=* 通配）。
	{218, "推送文件", "L0", []string{"titleList", "objectName", "fileUrl", "fileType"}, `{"type":218,"titleList":["成员甲"],"objectName":"交付文件.bin","fileUrl":"https://example.com/f.bin","fileType":"*"}`, "文件·推送", StatusImplemented},
	{219, "解散群", "L2", []string{"groupName"}, `{"type":219,"groupName":"交付群"}`, "群·高风险", StatusImplemented},
	{220, "从群添加好友", "L1", []string{"groupName", "friend.name"}, `{"type":220,"groupName":"交付群","friend":{"name":"成员乙"}}`, "联系人", StatusImplemented},
	{221, "添加待办", "L0", []string{"titleList", "receivedContent"}, `{"type":221,"titleList":["成员甲"],"receivedContent":"待办内容"}`, "效率", StatusImplemented},
	{222, "打卡", "L0", nil, `{"type":222}`, "效率", StatusImplemented},
	// 223 冻结决策（2026-11-05）：corpName 为幻影字段（upstream api-59089854
	// schema required=objectName「目标企业名称」= 执行器同名）→ 修 example。
	{223, "切换企业", "L0", []string{"objectName"}, `{"type":223,"objectName":"目标企业名"}`, "会话与循环", StatusImplemented},
	// 224 冻结决策（2026-11-05）：执行器自拟 card schema（WorkTool-Open 自拟，
	// 命令注释已登记）必填 objectName(标题)+extraText(副标题)+fileUrl(跳转) →
	// example 补齐。upstream pushLink 用 receivedContent/originalContent 分承
	// 副标题/链接，留 T3.2 候选。
	{224, "推送链接", "L0", []string{"titleList", "objectName", "extraText", "fileUrl"}, `{"type":224,"titleList":["成员甲"],"objectName":"标题","extraText":"副标题","fileUrl":"https://example.com"}`, "文件·推送", StatusImplemented},
	// 225 冻结决策（2026-11-05）：目标字段=执行器迁移例（targetRoom→groupName
	// 主读，upstream modifyGroupMemberInfo「groupName」）；selectList 为幻影字段
	// （成员定位=friend 子结构，upstream 与执行器同读 friend.name）→ example
	// 改 friend{name,markName} 形态。
	{225, "修改群成员备注/标签", "L0", []string{"groupName", "friend.name"}, `{"type":225,"groupName":"交付群","friend":{"name":"成员乙","markName":"重要"}}`, "群·写操作", StatusImplemented},
	{226, "撤回消息", "L0", []string{"titleList", "originalContent"}, `{"type":226,"titleList":["成员甲"],"originalContent":"被撤回消息ID"}`, "消息·管理", StatusImplemented},
	// 227 冻结决策（2026-11-05）：originTitle 幻影 → upstream/执行器同名三必填
	// titleList(源会话)+messageList(原文定位键)+nameList(接收人)。
	{227, "批量转发", "L1", []string{"titleList", "messageList", "nameList"}, `{"type":227,"titleList":["来源会话"],"messageList":[{"originalContent":"消息原文一","textType":1},{"originalContent":"消息原文二","textType":1}],"nameList":["成员甲"]}`, "消息·发送", StatusImplemented},
	{228, "合并转发", "L1", []string{"titleList", "messageList", "nameList"}, `{"type":228,"titleList":["来源会话"],"messageList":[{"originalContent":"消息原文一","textType":1}],"nameList":["成员甲"]}`, "消息·发送", StatusImplemented},
	// 229 冻结决策（2026-11-05）：upstream/执行器=weworkMessageList(消息对象数组，
	// 元素最小集 {textType,receivedContent})+nameList(接收人；本指令无 titleList)
	// ——catalog 原 titleList+receivedContent 为 238 形态错位 → 修 example。
	{229, "批量发送", "L1", []string{"weworkMessageList", "nameList"}, `{"type":229,"weworkMessageList":[{"textType":1,"receivedContent":"通知"}],"nameList":["成员甲","成员乙"]}`, "消息·发送", StatusImplemented},
	// 230 冻结决策（2026-11-05）：messageList 是 227/228 的字段（230 upstream/
	// 执行器=weworkMessageList+nameList）→ 修 example。
	{230, "合并发送", "L1", []string{"weworkMessageList", "nameList"}, `{"type":230,"weworkMessageList":[{"textType":1,"receivedContent":"内容一"}],"nameList":["成员甲"]}`, "消息·发送", StatusImplemented},
	{233, "扫一扫", "L0", nil, `{"type":233}`, "效率", StatusImplemented},
	{234, "删除联系人", "L2", []string{"friend.name"}, `{"type":234,"friend":{"name":"成员丙"}}`, "联系人·高风险", StatusImplemented},
	{236, "删除单向联系人", "L2", []string{"friend.name"}, `{"type":236,"friend":{"name":"成员丙"}}`, "联系人", StatusImplemented},
	{237, "清理企微缓存", "L2", nil, `{"type":237}`, "运维·高风险", StatusImplemented},
	// 238 冻结决策（2026-11-05）：receivedContent=JSON 数组字符串（≥2 条非空
	// 文本，upstream「批量发送至少需要两条非空文本」101011 族）——原 example 纯
	// 字符串端侧解析必败 → 修值形态（字段名不变）。
	{238, "批量发送文本", "L2", []string{"titleList", "receivedContent"}, `{"type":238,"titleList":["成员甲"],"receivedContent":"[\"文本一\",\"文本二\"]"}`, "消息·发送", StatusImplemented},
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
	// 511 冻结决策（2026-11-05）：upstream doc-9564531 §511 objectName(yyyy-MM-dd)
	// = schema required（执行器同读且必填）——example 本已可消费，补 Required 标注。
	{511, "查询会议信息", "L0", []string{"objectName"}, `{"type":511,"objectName":"2026-11-04"}`, "查询", StatusImplemented},
	{512, "查询群成员信息", "L0", []string{"groupName"}, `{"type":512,"groupName":"交付群"}`, "查询", StatusImplemented},
	// 513 口径加注（2026-11-05 矩阵口径对齐车道）：此处 StatusVerified=回执终态面
	// （真机 succeeded ec=0 ×2，local-private/evidence/session10/s10f-result.json、
	// session11/s11-summary.json）；数据本体经 query_result 事件回调的契约面未实测
	// 捕获、含 H1/H2 构建复测未收敛（session20 poll 90s timeout），支持矩阵 513 行
	// ⬜ 不翻（docs/05 v1.10 证据口径节+513 行；收敛后同步全义）。
	{513, "查询设备资源", "L0", nil, `{"type":513}`, "查询", StatusVerified},
	// 514 冻结决策（2026-11-05）：messageId 为幻影字段（upstream doc-9564531 §514
	// messageId 模式承载字段=objectName，时间段模式=logStartTime/logEndTime）→
	// 修 example 并补 Required（objectName 单项即最小可提交）。
	{514, "查询日志片段", "L0", []string{"objectName"}, `{"type":514,"objectName":"消息ID"}`, "运维", StatusImplemented},
	{515, "查询群名信息", "L0", []string{"groupName"}, `{"type":515,"groupName":"交付群"}`, "查询", StatusImplemented},
}
