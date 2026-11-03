// 事件类回调分发内核（T3.5，W-M6；契约 local-private/parity/b3/events.md 为
// 规格源，body 字段以该契约为准）。六类被动事件（type 101 被拉进群 / 102 群名
// 更新 / 103 群解散 / 104 被移出群 / 105 好友通过）+ 查询结果推送（511/512/513/
// 515）+ 图片 fileBase64 回调，全部复用回调 outbox 既有分发内核（同事务 enqueue
// → HMAC 签名 → 有界重试 1/2/4s → 第 4 次死信，见 outbox.go）——FR-35 第三
// 子面（events.md 前言：复用同一回调基础设施，body 协议与触发时机独立）。
//
// 旁路接线点（车道边界登记）：设备端 socketType=2 事件帧（list 项 type 101~105
// 事件族）到达 gateway 后，除既有指令管线外由网关车道对 HandleEventFrameItems
// 做一次旁路调用（一行接线；本车道不动 gateway 包）。验收口径 = 事件全类型注入
// → 回调可达 + 签名校验冒烟（docs/03 T3.5），注入面即本文件公开入口。
//
// 投递目标解析（events.md §〇 URL 解析的兼容面映射）：per-robot type=11 消息回调
// 绑定（qaUrl 语义——doc-9564531 事件/查询/图片三节均为「机器人配置的消息回调
// 地址」）> 部署级全局 OutboxConfig.CallbackURL > 行保持 pending（不推送）。
// 路由实现见 outbox.go callbackURLFor。
//
// 开关（events.md §〇公共层）：
//   - pushEventCallback 默认 **true**（e2/a.java:209）——101~105 与 511~515 总门；
//   - pushImage 默认 **false**（doc-861677「需在WTAPP里打开图片消息回调开关
//     （默认关闭）」）——图片回调独立门。
//
// 开关持久化于 admin_settings KV（key=event_callback_config，JSON 两布尔）：
// 管理面 PUT /api/v1/admin/settings/event-callbacks 为唯一写方（落审计 SR-12），
// 本包只读同一单一事实源；表未建/值损坏 → 回落缺省（总门开、图片关——与上游
// 缺省形态一致，不静默扩权）。
//
// 语义对拍登记（events.md §五）：查询指令双轨——ExecCallbackBean 回执（任务成败，
// 既有 exec_result outbox 行）+ 查询结果推送（数据本体，本文件）；回调地址未配置
// 时跳过推送但仍回执（E7）。对账键增强（events.md 建议统一附 messageId）：512/
// 513/515 在设备帧携带 messageId 时透传附加，511 恒透传（契约本带）。
package api

import (
	"context"
	"encoding/json"
	"fmt"

	"cn.worktool.open/server/internal/wire"
)

// 事件族 type 常量（events.md §一~§四、§七；int 字面量复用登记——编号与
// wire list 项 type=101 receiveMessageList 同号不同域，见 events.md §〇命名空间
// 警示；分发识别靠事件签名消歧，见 HandleDeviceEvent）。
const (
	EventRobotAddedToGroup = 101 // 被拉进群
	EventGroupNameUpdated  = 102 // 群名更新
	EventGroupDismissed    = 103 // 群解散
	EventRobotKickedGroup  = 104 // 被移出群
	EventFriendAccepted    = 105 // 好友通过
)

// 查询结果推送 type（events.md §五；指令 CMD-511/512/513/515 的数据本体轨道）。
const (
	QueryPushMeeting        = 511 // 会议（无会议不推送）
	QueryPushGroupMembers   = 512 // 群成员（空成员列表仍推送）
	QueryPushDeviceResource = 513 // 设备资源（data 14 字段透传）
	QueryPushGroupRemark    = 515 // 群名+备注（轻量）
)

// outbox 事件面 kind（与 exec_result/report 同列；管理面回调日志按 kind 投影）。
const (
	outboxKindEventCallback = "event_callback" // 101~105 被动事件
	outboxKindQueryResult   = "query_result"   // 511/512/513/515 查询结果推送
	outboxKindImageCallback = "image_callback" // 图片 fileBase64 回调
)

// settingsKeyEventCallbacks 为事件回调开关 KV 键（admin_settings 表单一事实源；
// 值 = EventCallbackConfig 的 JSON。写方 = 管理面 settings/event-callbacks）。
const settingsKeyEventCallbacks = "event_callback_config"

// EventCallbackConfig 事件回调开关集（events.md §〇公共层缺省形态）。
type EventCallbackConfig struct {
	PushEventCallback bool // 总门：101~105 + 511~515（缺省 true）
	PushImage         bool // 图片回调独立门（缺省 false）
}

// DefaultEventCallbackConfig 缺省开关（上游缺省：总门开、图片关）。
func DefaultEventCallbackConfig() EventCallbackConfig {
	return EventCallbackConfig{PushEventCallback: true, PushImage: false}
}

// MaxImageCallbackPayloadBytes 图片回调载荷上限（Base64 串长度；出站方向整帧
// HTTP POST 无外置，events.md §六大载荷分界——上限防失控设备撑爆 outbox 单行，
// 超限跳过 + 审计。var 供测试注入小值）。
var MaxImageCallbackPayloadBytes = 16 << 20

// eventCallbackSettings 读取开关（admin_settings KV；表未建 → 缺省；值损坏 →
// 缺省 + 日志——读路径永不失败，缺省即上游缺省形态）。
func (s *Server) eventCallbackSettings(ctx context.Context) EventCallbackConfig {
	def := DefaultEventCallbackConfig()
	rows, err := s.DB.QueryDump(ctx,
		`SELECT value FROM admin_settings WHERE key = ?`, settingsKeyEventCallbacks)
	if err != nil || len(rows) != 1 {
		return def // 表未建（管理面未装配）或行缺失 = 上游缺省
	}
	var cfg EventCallbackConfig
	if err := json.Unmarshal([]byte(rows[0][0]), &cfg); err != nil {
		s.Log.Printf("api/events: 开关持久化值损坏，回落缺省（总门开/图片关）: %v", err)
		return def
	}
	return cfg
}

// ---- ① 六类被动事件（101~105） ----

// HandleDeviceEvent 处理设备上报事件帧单项（socketType=2 list 项，type 101~105
// 事件族）。入向字段宽容（eventType 别名 type、memberName 别名单元素 memberList、
// roomType 缺省 1——任务书面向与 events.md 双向兼容）；出向 body 严格 events.md。
//
// 返回 enqueued=true 表示已旁路 enqueue（outbox 行 kind=event_callback，
// CommandID=管线目标键，路由见 callbackURLFor）；false=非事件族/开关关/事件
// 不合法（后者落审计 kind=event_callback_invalid，不撕裂调用方管线）。
func (s *Server) HandleDeviceEvent(ctx context.Context, target string, item json.RawMessage) (bool, error) {
	var raw struct {
		Type        *int     `json:"type"`
		EventType   *int     `json:"eventType"` // 任务书面向别名
		GroupName   string   `json:"groupName"`
		GroupRemark *string  `json:"groupRemark"`
		RoomType    *int     `json:"roomType"`
		MemberList  []string `json:"memberList"`
		MemberName  string   `json:"memberName"` // 任务书面向别名（单成员）
		FriendName  string   `json:"friendName"`
		FriendRem   *string  `json:"friendRemark"`
	}
	if err := json.Unmarshal(item, &raw); err != nil {
		return false, nil // 非 JSON 对象：非事件族，交回调用方
	}
	evType := 0
	if raw.Type != nil {
		evType = *raw.Type
	}
	if raw.EventType != nil {
		evType = *raw.EventType
	}
	switch evType {
	case EventRobotAddedToGroup, EventGroupNameUpdated, EventGroupDismissed, EventRobotKickedGroup, EventFriendAccepted:
	default:
		return false, nil // 非事件族（含 type=101 receiveMessageList 消息上报——同号不同域）
	}
	// type=101 同号消歧：既无任何事件签名字段（group*/roomType/member*）→ 视为
	// receiveMessageList 消息上报（gateway 既有路径），不拦截。
	if evType == EventRobotAddedToGroup && raw.GroupName == "" && raw.RoomType == nil &&
		raw.MemberList == nil && raw.MemberName == "" && raw.GroupRemark == nil {
		return false, nil
	}
	cfg := s.eventCallbackSettings(ctx)
	if !cfg.PushEventCallback {
		return false, nil // 总门关（上游缺省开；管理面可关）——不分发不审计
	}

	// 出向 body 严格 events.md；路由键：事件/图片行 outbox.CommandID=管线目标键
	//（callbackURLFor 直读反查 robot；query_result 行携带真实指令号走 commands 反查）。
	body := map[string]any{"type": evType}
	switch evType {
	case EventFriendAccepted: // 105：{type, friendName, friendRemark?}
		if raw.FriendName == "" {
			return false, s.auditInvalidEvent(ctx, target, evType, "缺 friendName")
		}
		body["friendName"] = raw.FriendName
		if raw.FriendRem != nil && *raw.FriendRem != "" {
			body["friendRemark"] = *raw.FriendRem
		}
	default: // 101/102/103/104：{type, groupName, groupRemark, roomType[, memberList]}
		if raw.GroupName == "" {
			return false, s.auditInvalidEvent(ctx, target, evType, "缺 groupName")
		}
		remark := ""
		if raw.GroupRemark != nil {
			remark = *raw.GroupRemark
		}
		roomType := 1 // 缺省 1=外部群（四值枚举容缺省；显式越界拒绝）
		if raw.RoomType != nil {
			if *raw.RoomType < 1 || *raw.RoomType > 4 {
				return false, s.auditInvalidEvent(ctx, target, evType,
					fmt.Sprintf("roomType=%d 不在四值枚举 1..4", *raw.RoomType))
			}
			roomType = *raw.RoomType
		}
		body["groupName"] = raw.GroupName
		body["groupRemark"] = remark // 无备注群照发（E2：有无备注都触发，remark=""）
		body["roomType"] = roomType
		if evType == EventRobotAddedToGroup {
			members := raw.MemberList
			if members == nil && raw.MemberName != "" {
				members = []string{raw.MemberName}
			}
			if len(members) > 0 { // u.q：仅非空才放（memberList 可选）
				body["memberList"] = members
			}
		}
	}
	return s.enqueueEventCallback(ctx, outboxKindEventCallback, target, target, body)
}

// ---- ② 查询结果推送（511/512/513/515） ----

// HandleQueryResult 处理查询结果推送单项（type 511/512/513/515；数据本体轨道，
// 与 ExecCallbackBean 回执双轨独立，E7）。commandID 为提交侧指令号（对账键；
// 可空——空时投递目标回落全局 URL）。返回 enqueued 语义同 HandleDeviceEvent。
func (s *Server) HandleQueryResult(ctx context.Context, target, commandID string, item json.RawMessage) (bool, error) {
	var probe struct {
		Type *int `json:"type"`
	}
	if err := json.Unmarshal(item, &probe); err != nil || probe.Type == nil {
		return false, nil
	}
	var kind string
	var body map[string]any
	switch *probe.Type {
	case QueryPushMeeting: // {type, messageId, date, meetingInfoList[]}；无会议不推送
		var raw struct {
			MessageID       string          `json:"messageId"`
			Date            string          `json:"date"`
			MeetingInfoList json.RawMessage `json:"meetingInfoList"`
		}
		if err := json.Unmarshal(item, &raw); err != nil {
			return false, s.auditInvalidEvent(ctx, target, *probe.Type, "载荷解析失败")
		}
		var meetings []json.RawMessage
		if len(raw.MeetingInfoList) > 0 {
			if err := json.Unmarshal(raw.MeetingInfoList, &meetings); err != nil || len(meetings) == 0 {
				return false, s.auditInvalidEvent(ctx, target, *probe.Type, "无会议不推送（meetingInfoList 空/非法）")
			}
		}
		if len(meetings) == 0 {
			return false, s.auditInvalidEvent(ctx, target, *probe.Type, "无会议不推送")
		}
		kind = "query_result"
		body = map[string]any{"type": *probe.Type, "date": raw.Date, "meetingInfoList": meetings}
		if raw.MessageID != "" {
			body["messageId"] = raw.MessageID
		}
	case QueryPushGroupMembers: // {type, groupName, groupRemark, groupOwner, groupAnnouncement, memberList[], memberCount}；空成员仍推送
		var raw struct {
			GroupName     string   `json:"groupName"`
			GroupRemark   string   `json:"groupRemark"`
			GroupOwner    string   `json:"groupOwner"`
			GroupAnnounce string   `json:"groupAnnouncement"`
			MemberList    []string `json:"memberList"`
			MemberCount   *int     `json:"memberCount"`
		}
		if err := json.Unmarshal(item, &raw); err != nil {
			return false, s.auditInvalidEvent(ctx, target, *probe.Type, "载荷解析失败")
		}
		if raw.GroupName == "" {
			return false, s.auditInvalidEvent(ctx, target, *probe.Type, "缺 groupName")
		}
		count := len(raw.MemberList) // u.l:1104：memberCount=list.size() 兜底
		if raw.MemberCount != nil {
			count = *raw.MemberCount
		}
		if raw.MemberList == nil {
			raw.MemberList = []string{} // 空成员列表仍推送（空数组非 null）
		}
		kind = "query_result"
		body = map[string]any{"type": *probe.Type, "groupName": raw.GroupName, "groupRemark": raw.GroupRemark,
			"groupOwner": raw.GroupOwner, "groupAnnouncement": raw.GroupAnnounce,
			"memberList": raw.MemberList, "memberCount": count}
	case QueryPushDeviceResource: // {type, data:{14 字段}}（b3/513.md；data 对象透传）
		var raw struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(item, &raw); err != nil || len(raw.Data) == 0 ||
			!json.Valid(raw.Data) || raw.Data[0] != '{' {
			return false, s.auditInvalidEvent(ctx, target, *probe.Type, "缺 data 对象")
		}
		kind = "query_result"
		body = map[string]any{"type": *probe.Type, "data": raw.Data}
	case QueryPushGroupRemark: // {type, groupName, groupRemark}（轻量）
		var raw struct {
			GroupName   string `json:"groupName"`
			GroupRemark string `json:"groupRemark"`
		}
		if err := json.Unmarshal(item, &raw); err != nil {
			return false, s.auditInvalidEvent(ctx, target, *probe.Type, "载荷解析失败")
		}
		if raw.GroupName == "" {
			return false, s.auditInvalidEvent(ctx, target, *probe.Type, "缺 groupName")
		}
		kind = "query_result"
		body = map[string]any{"type": *probe.Type, "groupName": raw.GroupName, "groupRemark": raw.GroupRemark}
	default:
		return false, nil // 非查询推送族
	}
	// 对账键增强（events.md §五：512/513/515 契约不带 messageId，建议统一附）：
	// 设备帧携带 messageId 且本行无（511 已带）时透传附加。
	var mid struct {
		MessageID string `json:"messageId"`
	}
	if body["messageId"] == nil && json.Unmarshal(item, &mid) == nil && mid.MessageID != "" {
		body["messageId"] = mid.MessageID
	}
	cfg := s.eventCallbackSettings(ctx)
	if !cfg.PushEventCallback {
		return false, nil // 总门关：跳过推送但仍回执（回执轨道不在本文件）
	}
	return s.enqueueEventCallback(ctx, kind, target, commandID, body)
}

// ---- ③ 图片消息回调（fileBase64） ----

// DeviceImageMessage 图片回调入向载荷（events.md §六字段；入向宽容归一，出向
// 严格契约：spoken/rawSpoken 恒空串、atMe 字符串型 "true"/"false" 勿按布尔、
// textType 恒 2）。
type DeviceImageMessage struct {
	ReceivedName string `json:"receivedName"` // 图片发送者名称
	GroupName    string `json:"groupName"`
	GroupRemark  string `json:"groupRemark"`
	RoomType     int    `json:"roomType"` // 四值枚举；缺省 1
	AtMe         string `json:"atMe"`     // 布尔入向归一为字符串；缺省 "false"
	TextType     int    `json:"textType"` // 须 2（图片）
	FileBase64   string `json:"fileBase64"`
}

// HandleImageMessage 处理图片消息回调（pushImage 开关默认关）。atMe 兼容布尔
// 入向（归一为字符串，契约「勿按布尔解析」防坑登记）。返回 enqueued 语义同上。
func (s *Server) HandleImageMessage(ctx context.Context, target string, msg DeviceImageMessage) (bool, error) {
	cfg := s.eventCallbackSettings(ctx)
	if !cfg.PushImage {
		return false, nil // 独立门默认关（doc-861677）
	}
	if msg.TextType != 0 && msg.TextType != 2 {
		return false, s.auditInvalidEvent(ctx, target, msg.TextType, "textType 非 2（非图片消息）")
	}
	if msg.FileBase64 == "" {
		return false, s.auditInvalidEvent(ctx, target, 2, "缺 fileBase64")
	}
	if len(msg.FileBase64) > MaxImageCallbackPayloadBytes {
		return false, s.auditInvalidEvent(ctx, target, 2,
			fmt.Sprintf("fileBase64 %d B 超上限 %d B", len(msg.FileBase64), MaxImageCallbackPayloadBytes))
	}
	atMe := msg.AtMe
	switch atMe {
	case "true", "false":
	case "1", "True":
		atMe = "true"
	case "", "0", "False":
		atMe = "false"
	default:
		return false, s.auditInvalidEvent(ctx, target, 2, fmt.Sprintf("atMe=%q 非 true/false 字符串", atMe))
	}
	roomType := msg.RoomType
	if roomType == 0 {
		roomType = 1
	}
	if roomType < 1 || roomType > 4 {
		return false, s.auditInvalidEvent(ctx, target, 2, fmt.Sprintf("roomType=%d 不在四值枚举", roomType))
	}
	body := map[string]any{
		"spoken": "", "rawSpoken": "", // 图片消息恒空串（doc 原文）
		"receivedName": msg.ReceivedName,
		"groupName":    msg.GroupName, "groupRemark": msg.GroupRemark,
		"roomType": roomType, "atMe": atMe,
		"textType": 2, "fileBase64": msg.FileBase64,
	}
	return s.enqueueEventCallback(ctx, outboxKindImageCallback, target, target, body)
}

// ---- ④ 事件帧旁路分发（gateway 接线入口） ----

// HandleEventFrameItems 处理一帧 socketType=2 事件帧的 list 载荷（旁路入口，
// 供网关车道在既有指令管线之外一次调用）：逐项识别——
//   - type 101~105 事件族 → HandleDeviceEvent（type=101 与 receiveMessageList
//     同号不同域：事件签名消歧后仍不分流消息上报，交回既有路径）；
//   - type 511/512/513/515 → HandleQueryResult（commandID 用帧 meta.commandId）；
//   - 其余类型不动（调用方继续既有管线）。
//
// 返回 enqueue 行数。单项失败不撕裂整帧（逐项审计后继续）。
func (s *Server) HandleEventFrameItems(ctx context.Context, target, commandID string, list json.RawMessage) (int, error) {
	items, err := (&wire.Envelope{List: list}).ListItems()
	if err != nil {
		// 单对象形态容错（注入面友好；线缆形态恒为数组——wire 侧已有校验）。
		var one map[string]json.RawMessage
		if uerr := json.Unmarshal(list, &one); uerr != nil || one == nil {
			return 0, fmt.Errorf("api/events: list 解析失败: %w", err)
		}
		items = []json.RawMessage{list}
	}
	enqueued := 0
	for _, item := range items {
		switch wire.ListItemType(item) {
		case EventRobotAddedToGroup, EventGroupNameUpdated, EventGroupDismissed, EventRobotKickedGroup, EventFriendAccepted:
			ok, err := s.HandleDeviceEvent(ctx, target, item)
			if err != nil {
				s.Log.Printf("api/events: 事件分发失败 target=%s: %v", target, err)
				continue
			}
			if ok {
				enqueued++
			}
		case QueryPushMeeting, QueryPushGroupMembers, QueryPushDeviceResource, QueryPushGroupRemark:
			ok, err := s.HandleQueryResult(ctx, target, commandID, item)
			if err != nil {
				s.Log.Printf("api/events: 查询推送分发失败 target=%s command=%s: %v", target, commandID, err)
				continue
			}
			if ok {
				enqueued++
			}
		}
	}
	return enqueued, nil
}

// ---- 内部：同事务 enqueue + 审计 ----

// enqueueEventCallback 同事务落 outbox 行（复用既有分发内核：HMAC/重试/死信
// 全部由 OutboxWorker 承担）+ 审计留痕。CommandID 语义：query_result 行=提交侧
// 指令号（commands.target 反查路由）；事件/图片行=管线目标键（路由直读）。
func (s *Server) enqueueEventCallback(ctx context.Context, kind, target, commandID string, body map[string]any) (bool, error) {
	now := s.Now().UTC()
	payload, err := json.Marshal(body)
	if err != nil {
		return false, fmt.Errorf("api/events: body 序列化失败: %w", err)
	}
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.DB.EnqueueOutbox(ctx, tx, kind, commandID, json.RawMessage(payload), now); err != nil {
		return false, err
	}
	audit := map[string]any{"op": "enqueue", "kind": kind, "target": target, "type": body["type"], "at": now.UnixMilli()}
	auditRaw, _ := json.Marshal(audit)
	_ = s.DB.InsertAudit(ctx, tx, "event_callback", target, string(auditRaw), now)
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// auditInvalidEvent 事件不合法跳过分发（留审计不撕裂管线；开关关闭为正常配置
// 形态，不审计）。
func (s *Server) auditInvalidEvent(ctx context.Context, target string, evType int, reason string) error {
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_ = s.DB.InsertAudit(ctx, tx, "event_callback_invalid", target,
		fmt.Sprintf(`{"type":%d,"reason":%q}`, evType, reason), s.Now().UTC())
	return tx.Commit()
}
