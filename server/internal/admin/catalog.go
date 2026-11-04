/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package admin

// adminCommandCatalog 为管理台指令目录常量（2026-11-04 所有者反馈②③）。
// 编号面=openapi 注册表（52 条，openapi_registry.go——两处同步维护）；中文名与
// scheduler/dryrun.go plannedActionNames 同源；风险级取 ClassifyRisk 口径
// （L1={206,207,213,220,227,228,229,230}、L2={219,234,304}，其余 L0）；
// required=compat/prevalidate.go 必填表；example=最小可提交示例（合成值）。
// 排序=编号升序；group=发送页分组。
type adminCommandEntry struct {
	Type     int
	Name     string
	Risk     string
	Required []string
	Example  string
	Group    string
}

var adminCommandCatalog = []adminCommandEntry{
	{201, "停止任务回首页", "L0", nil, `{"type":201}`, "会话与循环"},
	{202, "主循环开关", "L0", []string{"loop.on"}, `{"type":202,"loop":{"on":false}}`, "会话与循环"},
	{203, "发送文本消息", "L0", []string{"titleList", "receivedContent"}, `{"type":203,"titleList":["成员甲"],"receivedContent":"你好"}`, "消息·发送"},
	{204, "引用回复消息", "L0", []string{"titleList", "receivedContent"}, `{"type":204,"titleList":["成员甲"],"receivedContent":"收到"}`, "消息·发送"},
	{205, "转发消息", "L0", []string{"titleList"}, `{"type":205,"titleList":["成员甲"],"originTitle":"来源会话"}`, "消息·发送"},
	{206, "创建群（拉人/公告/备注）", "L1", []string{"groupName", "selectList"}, `{"type":206,"groupName":"交付群","selectList":["成员甲"]}`, "群·写操作"},
	{207, "修改群配置（含拉人/踢人）", "L1", []string{"groupName"}, `{"type":207,"groupName":"交付群","groupAnnouncement":"新公告"}`, "群·写操作"},
	{208, "推送微盘图片", "L0", []string{"titleList", "fileUrl"}, `{"type":208,"titleList":["成员甲"],"fileUrl":"https://example.com/a.png"}`, "文件·推送"},
	{209, "推送微盘文件", "L0", []string{"titleList", "fileUrl"}, `{"type":209,"titleList":["成员甲"],"fileUrl":"https://example.com/a.pdf"}`, "文件·推送"},
	{210, "推送小程序", "L0", []string{"titleList"}, `{"type":210,"titleList":["成员甲"],"fileUrl":"https://example.com/mp"}`, "文件·推送"},
	{211, "推送腾讯文档", "L0", []string{"titleList", "fileUrl"}, `{"type":211,"titleList":["成员甲"],"fileUrl":"https://example.com/doc"}`, "文件·推送"},
	{212, "通过全部待处理好友请求", "L0", nil, `{"type":212}`, "联系人"},
	{213, "按手机号添加好友", "L1", []string{"friend.phone"}, `{"type":213,"friend":{"phone":"13800000000","leavingMsg":"你好"}}`, "联系人"},
	{214, "展示群信息", "L0", []string{"groupName"}, `{"type":214,"groupName":"交付群"}`, "群·查询"},
	{218, "推送文件", "L0", []string{"titleList", "fileUrl"}, `{"type":218,"titleList":["成员甲"],"fileUrl":"https://example.com/f.bin"}`, "文件·推送"},
	{219, "解散群", "L2", []string{"groupName"}, `{"type":219,"groupName":"交付群"}`, "群·高风险"},
	{220, "从群添加好友", "L1", []string{"groupName", "friend.name"}, `{"type":220,"groupName":"交付群","friend":{"name":"成员乙"}}`, "联系人"},
	{221, "添加待办", "L0", []string{"titleList", "receivedContent"}, `{"type":221,"titleList":["成员甲"],"receivedContent":"待办内容"}`, "效率"},
	{222, "打卡", "L0", nil, `{"type":222}`, "效率"},
	{223, "切换企业", "L1", []string{"corpName"}, `{"type":223,"corpName":"目标企业名"}`, "会话与循环"},
	{224, "推送链接", "L0", []string{"titleList", "fileUrl"}, `{"type":224,"titleList":["成员甲"],"fileUrl":"https://example.com"}`, "文件·推送"},
	{225, "修改群成员备注/标签", "L0", []string{"groupName", "selectList"}, `{"type":225,"groupName":"交付群","selectList":["成员乙"],"friend":{"markName":"重要"}}`, "群·写操作"},
	{226, "撤回消息", "L0", []string{"titleList", "originalContent"}, `{"type":226,"titleList":["成员甲"],"originalContent":"被撤回消息ID"}`, "消息·管理"},
	{227, "批量转发", "L1", []string{"titleList"}, `{"type":227,"titleList":["成员甲"],"originTitle":"来源"}`, "消息·发送"},
	{228, "合并转发", "L1", []string{"titleList"}, `{"type":228,"titleList":["成员甲"],"originTitle":"来源"}`, "消息·发送"},
	{229, "批量发送", "L1", []string{"titleList", "receivedContent"}, `{"type":229,"titleList":["成员甲","成员乙"],"receivedContent":"通知"}`, "消息·发送"},
	{230, "合并发送", "L1", []string{"titleList"}, `{"type":230,"titleList":["成员甲"],"messageList":[{"originalContent":"内容一"}]}`, "消息·发送"},
	{233, "扫一扫", "L0", nil, `{"type":233}`, "效率"},
	{234, "删除联系人", "L2", []string{"friend.name"}, `{"type":234,"friend":{"name":"成员丙"}}`, "联系人·高风险"},
	{236, "删除单向联系人", "L0", []string{"friend.name"}, `{"type":236,"friend":{"name":"成员丙"}}`, "联系人"},
	{237, "清理企微缓存", "L2", nil, `{"type":237}`, "运维·高风险"},
	{238, "批量发送文本", "L0", []string{"titleList", "receivedContent"}, `{"type":238,"titleList":["成员甲"],"receivedContent":"批量内容"}`, "消息·发送"},
	{239, "创建客户群活码", "L0", []string{"groupName"}, `{"type":239,"groupName":"客户群"}`, "群·写操作"},
	{240, "添加群公告网页", "L0", []string{"groupName"}, `{"type":240,"groupName":"交付群","fileUrl":"https://example.com/board"}`, "群·写操作"},
	{244, "退出群", "L0", []string{"groupName"}, `{"type":244,"groupName":"交付群"}`, "群·写操作"},
	{303, "接口连通测试", "L0", nil, `{"type":303}`, "运维"},
	{304, "清空待执行队列", "L2", nil, `{"type":304}`, "队列"},
	{501, "查询群信息（按名单）", "L0", []string{"selectList"}, `{"type":501,"selectList":["成员甲、成员乙、成员丙、成员丁"]}`, "查询"},
	{502, "查询好友列表", "L0", nil, `{"type":502}`, "查询"},
	{503, "查询我的信息", "L0", nil, `{"type":503}`, "查询"},
	{504, "获取群二维码", "L0", []string{"groupName"}, `{"type":504,"groupName":"交付群"}`, "查询"},
	{505, "查询最近会话", "L0", nil, `{"type":505}`, "查询"},
	{506, "查询企业列表", "L0", nil, `{"type":506}`, "查询"},
	{507, "查询全部好友", "L0", nil, `{"type":507}`, "查询"},
	{508, "查询全部群", "L0", nil, `{"type":508}`, "查询"},
	{509, "上传本地文件/日志", "L0", []string{"filePath"}, `{"type":509,"filePath":"/sdcard/Download/文件.pdf"}`, "运维"},
	{510, "上传运行日志包", "L0", nil, `{"type":510,"logDate":"2026-11-04"}`, "运维"},
	{511, "查询会议信息", "L0", nil, `{"type":511,"objectName":"2026-11-04"}`, "查询"},
	{512, "查询群成员信息", "L0", []string{"groupName"}, `{"type":512,"groupName":"交付群"}`, "查询"},
	{513, "查询设备资源", "L0", nil, `{"type":513}`, "查询"},
	{514, "查询日志片段", "L0", nil, `{"type":514,"messageId":"消息ID（或按时间段查询，与 messageId 二选一）"}`, "运维"},
	{515, "查询群名信息", "L0", []string{"groupName"}, `{"type":515,"groupName":"交付群"}`, "查询"},
}
