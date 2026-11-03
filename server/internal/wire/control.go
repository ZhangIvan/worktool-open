// 控制帧载荷与 hello_ack 形状（protocol/spec.md §5.7 T3.4 冻结；
// schema = protocol/schema/control-payload.schema.json 与 session-hello-ack.schema.json）。
// 本文件为机器件的 Go 侧镜像：解析 + 约束校验，供网关分发与夹具一致性测试消费。
package wire

import (
	"encoding/json"
	"fmt"
)

// 控制帧类型常量（spec §4.2 v2 控制段；903/904 复用 envelope.go 既有常量）。
const (
	TypeCredentialRotate  = 901 // CREDENTIAL_ROTATE（云→端，§5.5.1；信封必须 encryptType=2）
	TypeCredentialRevoked = 902 // CREDENTIAL_REVOKED（云→端，§5.5.2）
)

// CredentialRotatePayload 为 901 载荷（schema credentialRotate；additionalProperties:false）。
type CredentialRotatePayload struct {
	Type                  int    `json:"type"`
	DeviceToken           string `json:"deviceToken"`
	TokenVersion          int64  `json:"tokenVersion"`
	ServerX25519PublicKey string `json:"serverX25519PublicKey"`
}

// DecodeCredentialRotate 解析并校验 901 载荷（schema 约束镜像）。
// 信封级约束（所在帧必须 encryptType=2）由调用方校验（schema 无法表达）。
func DecodeCredentialRotate(item json.RawMessage) (*CredentialRotatePayload, error) {
	var p CredentialRotatePayload
	if err := json.Unmarshal(item, &p); err != nil {
		return nil, fmt.Errorf("wire/control: 901 载荷解析失败: %w", err)
	}
	if p.Type != TypeCredentialRotate {
		return nil, fmt.Errorf("wire/control: type 须为 901，实际 %d", p.Type)
	}
	if !pubKeyPattern.MatchString(p.DeviceToken) {
		return nil, fmt.Errorf("wire/control: deviceToken 须为 64 字符小写 hex")
	}
	if p.TokenVersion < 2 {
		return nil, fmt.Errorf("wire/control: tokenVersion 须 ≥2（当前版本+1），实际 %d", p.TokenVersion)
	}
	if !pubKeyPattern.MatchString(p.ServerX25519PublicKey) {
		return nil, fmt.Errorf("wire/control: serverX25519PublicKey 须为 64 字符小写 hex")
	}
	return &p, nil
}

// DecodeControlPayload 解析 901~904 载荷单元并按 schema 变体校验
// （additionalProperties:false 由未知字段拒绝承载；904 双变体 oneOf）。
// 返回 (变体名, 错误)：变体 ∈ credentialRotate|credentialRevoked|resyncRequest|journalReplay|journalRequest。
func DecodeControlPayload(item json.RawMessage) (string, error) {
	var probe struct {
		Type    *int            `json:"type"`
		Request string          `json:"request"`
		Entries json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(item, &probe); err != nil {
		return "", fmt.Errorf("wire/control: 载荷解析失败: %w", err)
	}
	if probe.Type == nil {
		return "", fmt.Errorf("wire/control: type 必含且为整数")
	}
	var extra map[string]json.RawMessage
	_ = json.Unmarshal(item, &extra)
	delete(extra, "type")
	switch *probe.Type {
	case TypeCredentialRotate:
		v, err := DecodeCredentialRotate(item)
		if err != nil {
			return "", err
		}
		if len(extra) != 3 { // deviceToken/tokenVersion/serverX25519PublicKey 之外拒绝
			return "", fmt.Errorf("wire/control: 901 含未定义字段")
		}
		_ = v
		return "credentialRotate", nil
	case TypeCredentialRevoked:
		if len(extra) != 0 {
			return "", fmt.Errorf("wire/control: 902 载荷仅 type（additionalProperties:false）")
		}
		return "credentialRevoked", nil
	case TypeResyncRequest:
		if len(extra) != 0 {
			return "", fmt.Errorf("wire/control: 903 载荷仅 type（additionalProperties:false）")
		}
		return "resyncRequest", nil
	case TypeJournalReplay:
		hasEntries := len(probe.Entries) > 0 && string(probe.Entries) != "null"
		hasRequest := probe.Request != ""
		if hasEntries == hasRequest { // 两者均缺或并存 → oneOf 不满足
			return "", fmt.Errorf("wire/control: 904 须为补报（entries）或请求（request）之一")
		}
		if hasRequest {
			if probe.Request != "journal_replay" {
				return "", fmt.Errorf("wire/control: 904 request 须为 journal_replay，实际 %q", probe.Request)
			}
			if len(extra) != 1 {
				return "", fmt.Errorf("wire/control: 904 request 变体含未定义字段")
			}
			return "journalRequest", nil
		}
		if len(extra) != 1 {
			return "", fmt.Errorf("wire/control: 904 entries 变体含未定义字段")
		}
		var payload struct {
			Entries []JournalEntryPayload `json:"entries"`
		}
		if err := json.Unmarshal(item, &payload); err != nil {
			return "", fmt.Errorf("wire/control: 904 entries 解析失败: %w", err)
		}
		if len(payload.Entries) == 0 {
			return "", fmt.Errorf("wire/control: 904 entries minItems:1")
		}
		for i, e := range payload.Entries {
			if err := e.validate(); err != nil {
				return "", fmt.Errorf("wire/control: entries[%d]: %w", i, err)
			}
		}
		return "journalReplay", nil
	default:
		return "", fmt.Errorf("wire/control: 非法控制类型 %d（合法 901~904）", *probe.Type)
	}
}

// JournalEntryPayload 为 904 补报条目（schema journalReplay.entries；与 scheduler.JournalEntry 字段一致）。
type JournalEntryPayload struct {
	CommandID string `json:"commandId"`
	Epoch     int64  `json:"epoch"`
	Started   bool   `json:"started"`
	Outcome   string `json:"outcome"`
	Source    string `json:"source"`
}

func (e *JournalEntryPayload) validate() error {
	if !uuidPattern.MatchString(e.CommandID) {
		return fmt.Errorf("commandId 非小写 UUID: %q", e.CommandID)
	}
	if e.Epoch < 0 {
		return fmt.Errorf("epoch 须 ≥0，实际 %d", e.Epoch)
	}
	switch e.Outcome {
	case "", "success", "failure", "never_started":
	default:
		return fmt.Errorf("outcome 非法: %q", e.Outcome)
	}
	switch e.Source {
	case "local", "server":
	default:
		return fmt.Errorf("source 非法: %q", e.Source)
	}
	return nil
}

// ValidateHelloAckShape 校验服务端出站 hello_ack 是否符合冻结形状
// （session-hello-ack.schema.json 约束的 Go 侧镜像；语义不变式 serverTime==meta.ts、
// resumeFromSeq==meta.seq==lastReceivedSeq+1 中前两者在此校验，第三者由会话层保证）。
func ValidateHelloAckShape(ack *HelloAck) error {
	if ack.Type != "hello_ack" {
		return fmt.Errorf("wire/control: hello_ack type 非法: %q", ack.Type)
	}
	if ack.ServerTime != ack.Meta.Ts {
		return fmt.Errorf("wire/control: serverTime(%d) != meta.ts(%d)", ack.ServerTime, ack.Meta.Ts)
	}
	if ack.ResumeFromSeq != ack.Meta.Seq || ack.ResumeFromSeq < 1 {
		return fmt.Errorf("wire/control: resumeFromSeq(%d) 须 == meta.seq(%d) 且 ≥1", ack.ResumeFromSeq, ack.Meta.Seq)
	}
	if ack.HeartbeatIntervalSecs != 15 || ack.SessionTimeoutSecs != 45 {
		return fmt.Errorf("wire/control: 冻结值漂移 heartbeat=%d timeout=%d（须 15/45）",
			ack.HeartbeatIntervalSecs, ack.SessionTimeoutSecs)
	}
	if ack.Meta.Ver != 2 || ack.Meta.CommandID != "" || ack.Meta.Seq < 1 || ack.Meta.TokenVersion < 1 {
		return fmt.Errorf("wire/control: hello_ack meta 约束失败（ver=2/commandId 空/seq≥1/tokenVersion≥1）")
	}
	if !noncePattern.MatchString(ack.Meta.Nonce) || !hmacPattern.MatchString(ack.Meta.HMAC) ||
		!uuidPattern.MatchString(ack.Meta.EventID) {
		return fmt.Errorf("wire/control: hello_ack meta pattern 约束失败")
	}
	return nil
}
