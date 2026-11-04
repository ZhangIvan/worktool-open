// WS 帧信封编解码与校验（protocol/spec.md §4 v1 兼容信封 + §5.2 v2 帧；
// schema = protocol/schema/wire-v1-envelope.schema.json 与 command-envelope.schema.json）。
package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// SocketType 语义（spec §4.1）。
const (
	SocketHeartbeat   = 0 // 心跳
	SocketConfirm     = 1 // 消息确认（仅 v1；v2 确认语义由 seq/eventId 承载）
	SocketMessageList = 2 // 消息列表（指令或上报）
	SocketRawConfirm  = 3 // 指令执行结果确认（RAW_CONFIRM，载荷 ExecCallbackBean）
)

// 消息/指令类型（本网关显式消费的子集，全表见 spec §4.2）。
const (
	TypeHeartbeat          = 11  // HEART_BEAT（v1 裸形态 {"type":11} 亦须接受）
	TypeReceiveMessageList = 101 // TYPE_RECEIVE_MESSAGE_LIST（端→云上报）
	TypeRobotLog           = 301
	TypeRobotErrorLog      = 302
	TypeResyncRequest      = 903 // RESYNC_REQUEST（服务端在 4405 关闭前下发，spec §5.4.4）
	TypeJournalReplay      = 904 // JOURNAL_REPLAY（端→云离线 journal 补报，spec §5.4.5）

	minExecCallbackType = 201 // ExecCallbackBean.type 下界（schema exec-callback）
	maxCommandType      = 509 // 指令/结果 type 上界
)

// v2 会话 WS 关闭码（spec §5.2.5）。
const (
	CloseFrameInvalid = 4400 // 帧格式/schema 非法
	CloseAuthFailure  = 4401 // 鉴权失败（HMAC 不符/无凭据）
	CloseNonceReplay  = 4402 // nonce 重放
	CloseClockSkew    = 4403 // 时钟偏差超差
	CloseTokenVersion = 4404 // tokenVersion 校验失败
	CloseSeqOrEventID = 4405 // seq 乱序或 eventId 冲突
)

// FrameMeta 为 v2 逐帧鉴权 meta（schema frameMeta；additionalProperties:false）。
type FrameMeta struct {
	Ver          int    `json:"ver"`
	Ts           int64  `json:"ts"`
	Nonce        string `json:"nonce"`
	HMAC         string `json:"hmac"`
	Seq          int64  `json:"seq"`
	EventID      string `json:"eventId"`
	CommandID    string `json:"commandId"`
	TokenVersion int64  `json:"tokenVersion"`
}

// Envelope 为 v1/v2 统一信封（WeworkMessageListBean）。
// v2 帧判别条件 = meta 为 JSON 对象（spec §2）。
type Envelope struct {
	SocketType    int             `json:"socketType"`
	MessageID     string          `json:"messageId,omitempty"`
	MetaRaw       json.RawMessage `json:"meta,omitempty"`
	APISend       *int            `json:"apiSend,omitempty"`
	List          json.RawMessage `json:"list,omitempty"`
	EncryptedList string          `json:"encryptedList,omitempty"`
	EncryptType   int             `json:"encryptType"`
}

// DecodeEnvelope 解析一帧 WS 文本消息为信封。
func DecodeEnvelope(data []byte) (*Envelope, error) {
	var env Envelope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		// 信封级未知字段宽容：上游 v1 客户端可能携带新增顶层字段，未知字段不拒帧
		// （载荷 additionalProperties:true，spec §4.4 冻结状态表）。
		env = Envelope{}
		if err2 := json.Unmarshal(data, &env); err2 != nil {
			return nil, fmt.Errorf("wire/envelope: JSON 解析失败: %w", err2)
		}
	}
	return &env, nil
}

// IsV2 报告信封是否为 v2 帧（meta 为 JSON 对象；spec §2 帧判别条件）。
func (e *Envelope) IsV2() bool {
	t := trimJSONSpace(e.MetaRaw)
	return len(t) > 0 && t[0] == '{'
}

// V2Meta 解析 v2 meta；非 v2 帧返回错误。
func (e *Envelope) V2Meta() (*FrameMeta, error) {
	if !e.IsV2() {
		return nil, fmt.Errorf("wire/envelope: 非 v2 帧（meta 不是对象）")
	}
	var m FrameMeta
	if err := json.Unmarshal(e.MetaRaw, &m); err != nil {
		return nil, fmt.Errorf("wire/envelope: meta 解析失败: %w", err)
	}
	return &m, nil
}

// MetaString 返回 v1 meta 字符串（缺失返回空串）。
func (e *Envelope) MetaString() string {
	t := trimJSONSpace(e.MetaRaw)
	if len(t) > 0 && t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err == nil {
			return s
		}
	}
	return ""
}

// 校验模式（与 protocol/schema/*.schema.json 的 pattern 一一对应）。
var (
	uuidPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	messageIDPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}#[0-9]{2}:[0-9]{2}:[0-9]{2}#[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	noncePattern     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hmacPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hexPattern       = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	deviceIDPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)
	pairCodePattern  = regexp.MustCompile(`^[0-9]{6}$`)
	pubKeyPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// 对外暴露的模式（供 api/pair 校验复用，避免重复定义漂移）。
var (
	UUIDPattern      = uuidPattern
	DeviceIDPattern  = deviceIDPattern
	PairCodePattern  = pairCodePattern
	PublicKeyPattern = pubKeyPattern
	MessageIDPattern = messageIDPattern
)

// FrameError 携带 v2 帧校验失败对应的关闭码（spec §5.2.5）。
type FrameError struct {
	Code int
	Msg  string
}

func (e *FrameError) Error() string { return fmt.Sprintf("wire/envelope: %s", e.Msg) }

func frameErr(code int, format string, args ...any) *FrameError {
	return &FrameError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// ValidateV2 校验 v2 帧结构（schema command-envelope 约束的服务端镜像；
// 逐帧鉴权与 seq/eventId 语义由 gateway 会话层按 §5.2.4 顺序校验）。
func (e *Envelope) ValidateV2() error {
	if e.SocketType != SocketHeartbeat && e.SocketType != SocketMessageList && e.SocketType != SocketRawConfirm {
		return frameErr(CloseFrameInvalid, "v2 帧 socketType 非法: %d（仅 0/2/3）", e.SocketType)
	}
	if e.MessageID != "" && !messageIDPattern.MatchString(e.MessageID) {
		return frameErr(CloseFrameInvalid, "v2 帧 messageId 格式非法: %q", e.MessageID)
	}
	if e.EncryptType != 0 && e.EncryptType != 2 {
		return frameErr(CloseFrameInvalid, "v2 帧 encryptType 非法: %d（仅 0/2；1 为上游遗留默认禁用）", e.EncryptType)
	}
	m, err := e.V2Meta()
	if err != nil {
		return frameErr(CloseFrameInvalid, "meta 非法: %v", err)
	}
	if m.Ver != 2 {
		return frameErr(CloseFrameInvalid, "meta.ver 必须为 2，实际 %d", m.Ver)
	}
	if m.Ts < 0 {
		return frameErr(CloseFrameInvalid, "meta.ts 非法: %d", m.Ts)
	}
	if !noncePattern.MatchString(m.Nonce) {
		return frameErr(CloseFrameInvalid, "meta.nonce 须为 32 字符小写 hex")
	}
	if !hmacPattern.MatchString(m.HMAC) {
		return frameErr(CloseFrameInvalid, "meta.hmac 缺失或非 64 字符小写 hex")
	}
	if m.Seq < 1 {
		return frameErr(CloseFrameInvalid, "meta.seq 须 ≥1，实际 %d", m.Seq)
	}
	if !uuidPattern.MatchString(m.EventID) {
		return frameErr(CloseFrameInvalid, "meta.eventId 非小写 UUID: %q", m.EventID)
	}
	if m.CommandID != "" && !uuidPattern.MatchString(m.CommandID) {
		return frameErr(CloseFrameInvalid, "meta.commandId 须为空或小写 UUID: %q", m.CommandID)
	}
	if m.TokenVersion < 1 {
		return frameErr(CloseFrameInvalid, "meta.tokenVersion 须 ≥1，实际 %d", m.TokenVersion)
	}
	// additionalProperties:false（frameMeta 未定义字段拒绝）
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(e.MetaRaw, &extra); err == nil {
		for _, k := range []string{"ver", "ts", "nonce", "hmac", "seq", "eventId", "commandId", "tokenVersion"} {
			delete(extra, k)
		}
		if len(extra) > 0 {
			keys := make([]string, 0, len(extra))
			for k := range extra {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return frameErr(CloseFrameInvalid, "meta 含未定义字段: %v", keys)
		}
	}
	// 载荷与 encryptType 匹配（schema allOf 条件约束）
	switch e.EncryptType {
	case 0:
		t := trimJSONSpace(e.List)
		if len(t) == 0 {
			return frameErr(CloseFrameInvalid, "encryptType=0 须携带 list")
		}
		if t[0] != '[' {
			return frameErr(CloseFrameInvalid, "list 须为 JSON 数组")
		}
	case 2:
		if e.EncryptedList == "" || !hexPattern.MatchString(e.EncryptedList) {
			return frameErr(CloseFrameInvalid, "encryptType=2 须携带 hex encryptedList")
		}
		if len(trimJSONSpace(e.List)) > 0 {
			return frameErr(CloseFrameInvalid, "encryptType=2 不得同时携带明文 list")
		}
	}
	return nil
}

// ValidateV1 校验 v1 兼容帧（schema wire-v1-envelope 约束）。
func (e *Envelope) ValidateV1() error {
	if e.SocketType < SocketHeartbeat || e.SocketType > SocketRawConfirm {
		return fmt.Errorf("v1 帧 socketType 非法: %d", e.SocketType)
	}
	if e.EncryptType != 0 && e.EncryptType != 1 {
		return fmt.Errorf("v1 帧 encryptType 非法: %d（仅 0/1）", e.EncryptType)
	}
	if len(e.MetaRaw) > 0 && trimJSONSpace(e.MetaRaw)[0] == '{' {
		return fmt.Errorf("v1 兼容路径不接受 v2 帧（meta 为对象）")
	}
	switch e.EncryptType {
	case 0:
		t := trimJSONSpace(e.List)
		if len(t) == 0 {
			return fmt.Errorf("encryptType=0 须携带 list")
		}
		if t[0] != '[' {
			return fmt.Errorf("list 须为 JSON 数组")
		}
	case 1:
		// 上游 AES/CBC 遗留形态：默认禁用，仅解码兼容（密文原样透传，不做解密；
		// SR-02/G2：上游硬编码 key/iv 值不录入本仓库）。
		if e.EncryptedList == "" || !hexPattern.MatchString(e.EncryptedList) {
			return fmt.Errorf("encryptType=1 须携带 hex encryptedList")
		}
	}
	return nil
}

// ListItems 返回 list 的逐项原始 JSON（供管线分发）。
func (e *Envelope) ListItems() ([]json.RawMessage, error) {
	var items []json.RawMessage
	t := trimJSONSpace(e.List)
	if len(t) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(t, &items); err != nil {
		return nil, fmt.Errorf("wire/envelope: list 解析失败: %w", err)
	}
	return items, nil
}

// ListItemType 解析单个载荷项的 type（缺省 -1）。
func ListItemType(item json.RawMessage) int {
	var probe struct {
		Type *int `json:"type"`
	}
	if err := json.Unmarshal(item, &probe); err != nil {
		return -1
	}
	if probe.Type != nil {
		return *probe.Type
	}
	return -1
}

// ExecResult 为 socketType=3 / 结果帧载荷（ExecCallbackBean，schema exec-callback）。
type ExecResult struct {
	Type        int      `json:"type"`
	ErrorCode   int      `json:"errorCode"`
	ErrorReason string   `json:"errorReason"`
	RunTime     string   `json:"runTime"`
	TimeCost    int64    `json:"timeCost"`
	SuccessList []string `json:"successList"`
	FailList    []string `json:"failList"`
	MetaJSON    string   `json:"metaJson"`
	// RawMsg 查询类指令结果数据本体（apifox api-44952776 rawMsg=回执数据：
	// 501/502 等名单 JSON——2026-11-04 契约修正，端侧 DeviceSessionLoop 填充）。
	RawMsg string `json:"rawMsg,omitempty"`
}

// IsExecResult 报告载荷项是否为执行结果（type ∈ 201~509 且携带 errorCode）。
func IsExecResult(item json.RawMessage) bool {
	var probe struct {
		Type      *int `json:"type"`
		ErrorCode *int `json:"errorCode"`
	}
	if err := json.Unmarshal(item, &probe); err != nil {
		return false
	}
	return probe.Type != nil && probe.ErrorCode != nil &&
		*probe.Type >= minExecCallbackType && *probe.Type <= maxCommandType
}

// DecodeExecResult 解析结果载荷。
func DecodeExecResult(item json.RawMessage) (*ExecResult, error) {
	var r ExecResult
	if err := json.Unmarshal(item, &r); err != nil {
		return nil, fmt.Errorf("wire/envelope: 结果载荷解析失败: %w", err)
	}
	return &r, nil
}

// Hello 为 v2 会话首帧（schema session-hello；seq 恒 1、空载荷）。
type Hello struct {
	Type            string     `json:"type"`
	DeviceID        string     `json:"deviceId"`
	TokenVersion    int64      `json:"tokenVersion"`
	LastEpoch       int64      `json:"lastEpoch"`
	LastReceivedSeq int64      `json:"lastReceivedSeq"`
	Capabilities    *HelloCaps `json:"capabilities,omitempty"`
	Meta            FrameMeta  `json:"meta"`
}

// HelloCaps 为端侧能力声明。
type HelloCaps struct {
	ClientVersion string `json:"clientVersion"`
	EncryptMax    int    `json:"encryptTypeMax"`
	Commands      []int  `json:"commands"`
}

// DecodeHello 解析 hello 首帧并校验 schema 约束。
func DecodeHello(data []byte) (*Hello, error) {
	var h Hello
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, frameErr(CloseFrameInvalid, "hello 解析失败: %v", err)
	}
	if h.Type != "hello" {
		return nil, frameErr(CloseFrameInvalid, "首帧 type 须为 hello，实际 %q", h.Type)
	}
	if !deviceIDPattern.MatchString(h.DeviceID) {
		return nil, frameErr(CloseFrameInvalid, "deviceId 格式非法: %q", h.DeviceID)
	}
	if h.TokenVersion < 1 {
		return nil, frameErr(CloseFrameInvalid, "tokenVersion 须 ≥1")
	}
	if h.LastEpoch < 0 || h.LastReceivedSeq < 0 {
		return nil, frameErr(CloseFrameInvalid, "lastEpoch/lastReceivedSeq 须 ≥0")
	}
	if h.Capabilities != nil {
		if h.Capabilities.ClientVersion == "" || len(h.Capabilities.ClientVersion) > 32 {
			return nil, frameErr(CloseFrameInvalid, "capabilities.clientVersion 须为 1~32 字符")
		}
		if em := h.Capabilities.EncryptMax; em != 0 && em != 2 {
			return nil, frameErr(CloseFrameInvalid, "capabilities.encryptTypeMax 须为 0 或 2")
		}
	}
	if h.Meta.Ver != 2 {
		return nil, frameErr(CloseFrameInvalid, "meta.ver 须为 2")
	}
	if h.Meta.Seq != 1 {
		return nil, frameErr(CloseFrameInvalid, "hello meta.seq 恒为 1，实际 %d", h.Meta.Seq)
	}
	if h.Meta.CommandID != "" {
		return nil, frameErr(CloseFrameInvalid, "hello meta.commandId 须为空串")
	}
	if !noncePattern.MatchString(h.Meta.Nonce) {
		return nil, frameErr(CloseFrameInvalid, "meta.nonce 须为 32 字符小写 hex")
	}
	if !hmacPattern.MatchString(h.Meta.HMAC) {
		return nil, frameErr(CloseFrameInvalid, "meta.hmac 缺失或非 64 字符小写 hex")
	}
	if !uuidPattern.MatchString(h.Meta.EventID) {
		return nil, frameErr(CloseFrameInvalid, "meta.eventId 非小写 UUID")
	}
	if h.Meta.TokenVersion < 1 {
		return nil, frameErr(CloseFrameInvalid, "meta.tokenVersion 须 ≥1")
	}
	return &h, nil
}

// HelloAck 为 hello_ack 应答（形状按 spec §5.4.1 冻结，schema 随 T3.4）。
type HelloAck struct {
	Type                  string    `json:"type"`
	ServerTime            int64     `json:"serverTime"`
	ResumeFromSeq         int64     `json:"resumeFromSeq"`
	HeartbeatIntervalSecs int       `json:"heartbeatIntervalSeconds"`
	SessionTimeoutSecs    int       `json:"sessionTimeoutSeconds"`
	Meta                  FrameMeta `json:"meta"`
}

// BuildMessageID 生成上游还原格式的 messageId：yyyy-MM-dd#HH:mm:ss#<UUID>（spec §4.1）。
func BuildMessageID(nowMillis int64, uuid string) string {
	t := time.UnixMilli(nowMillis).UTC().Format("2006-01-02#15:04:05")
	return t + "#" + uuid
}
