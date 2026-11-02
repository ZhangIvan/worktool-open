// Package statemachine 以 protocol/state-transitions.csv 为唯一权威实现
// 表驱动执行状态机（docs/02-ARCHITECTURE.md §5.1；任务卡 T2.3）。
//
// 权威件通过 go:embed 嵌入二进制（部署自包含，ADR-02 单二进制）；data/ 下的
// 副本与仓库根 protocol/ 权威件的逐字节一致由测试 TestEmbeddedAuthoritySync
// 强制（CI paths 已含 protocol/**，权威件变更必然触发同步，关 AUD-03 C-8）。
// 终态判定与策略参数来自 protocol/state-policy.json（CSV 只承载边集）。
package statemachine

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	_ "embed"
)

//go:embed data/state-transitions.csv
var transitionsCSV []byte

//go:embed data/state-policy.json
var policyJSON []byte

// Edge 为状态机一条边（state-transitions.csv 一行）。
type Edge struct {
	ID     string // E01..E18
	From   string // from_state
	To     string // to_state
	Event  string // event（管线的迁移触发词）
	Guard  string // guard（人工/审批语义，机器不可判部分由调用方实现）
	Effect string // effect（落库副作用的自然语言规格）
}

// SameKeyDiffParams 为同键变参冲突策略（state-policy.json#idempotency）。
type SameKeyDiffParams struct {
	HTTPStatus int    `json:"http_status"`
	Code       string `json:"code"`
}

// IdempotencyPolicy 为幂等键策略段。
type IdempotencyPolicy struct {
	KeyFields         []string          `json:"key_fields"`
	WindowHours       int               `json:"window_hours"`
	SameKeyDiffParams SameKeyDiffParams `json:"same_key_diff_params"`
}

// Policy 为 state-policy.json 的机器可读子集。
type Policy struct {
	Version            string
	TerminalStates     []string
	IllegalHTTPStatus  int
	IllegalCode        string
	Idempotency        IdempotencyPolicy
	TombstoneMinDays   int
	AckTTLSeconds      int
	ClockSkewSeconds   int
	NonceWindowSeconds int
	RotationDays       int
}

type policyFile struct {
	Version        string   `json:"version"`
	TerminalStates []string `json:"terminal_states"`
	Illegal        struct {
		HTTPStatus int    `json:"http_status"`
		Code       string `json:"code"`
	} `json:"illegal_transition"`
	Idempotency IdempotencyPolicy `json:"idempotency"`
	Tombstone   struct {
		MinRetentionDays int `json:"min_retention_days"`
	} `json:"tombstone"`
	Dispatch struct {
		AckTTLSeconds int `json:"ack_ttl_seconds"`
	} `json:"dispatch"`
	SecurityConstants struct {
		ClockSkewToleranceSeconds int `json:"clock_skew_tolerance_seconds"`
		NonceDedupWindowSeconds   int `json:"nonce_dedup_window_seconds"`
		CredentialRotationDays    int `json:"credential_rotation_days_default"`
	} `json:"security_constants"`
}

// Machine 为表驱动状态机。
type Machine struct {
	edges     []Edge
	byFromEvt map[string][]Edge // "from\x00event" → 候选边（同事件可按结果分流，如 E10/E11）
	fromEdges map[string][]Edge
	policy    Policy
}

// LoadCSV 从 CSV 文本构建状态机（列序固定：id,from_state,to_state,event,guard,effect）。
func LoadCSV(data []byte, policy Policy) (*Machine, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = 6
	var edges []Edge
	headerSeen := false
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("statemachine: CSV 解析失败: %w", err)
		}
		if !headerSeen {
			headerSeen = true
			continue // 表头
		}
		if len(rec) != 6 {
			return nil, fmt.Errorf("statemachine: 列数 %d ≠ 6（行 %q）", len(rec), rec)
		}
		e := Edge{ID: rec[0], From: rec[1], To: rec[2], Event: rec[3], Guard: rec[4], Effect: rec[5]}
		if e.ID == "" || e.From == "" || e.To == "" || e.Event == "" {
			return nil, fmt.Errorf("statemachine: 边 %v 含空必填列", rec)
		}
		edges = append(edges, e)
	}
	if len(edges) == 0 {
		return nil, fmt.Errorf("statemachine: 边集为空")
	}
	return New(edges, policy), nil
}

// New 由已解析边集构建状态机。
func New(edges []Edge, policy Policy) *Machine {
	m := &Machine{
		edges:     append([]Edge(nil), edges...),
		byFromEvt: make(map[string][]Edge, len(edges)),
		fromEdges: make(map[string][]Edge),
		policy:    policy,
	}
	for _, e := range edges {
		k := e.From + "\x00" + e.Event
		// 同源同事件允许多条（exec_result 按 errorCode 分流 E10/E11、
		// reconcile_evidence 按证据结论分流 E12/E14），但不允许同源同事件同目标重复。
		for _, dup := range m.byFromEvt[k] {
			if dup.To == e.To {
				panic(fmt.Sprintf("statemachine: 同源同事件同目标重复边 %s --%s--> %s（%s vs %s）", e.From, e.Event, e.To, dup.ID, e.ID))
			}
		}
		m.byFromEvt[k] = append(m.byFromEvt[k], e)
		m.fromEdges[e.From] = append(m.fromEdges[e.From], e)
	}
	return m
}

// Default 返回嵌入权威件构建的状态机。
func Default() *Machine {
	policy := LoadPolicy(policyJSON)
	m, err := LoadCSV(transitionsCSV, policy)
	if err != nil {
		panic(fmt.Sprintf("statemachine: 嵌入边集损坏（data/ 副本失同步）: %v", err))
	}
	return m
}

// LoadPolicy 解析 state-policy.json（解析失败 panic：权威件损坏属构建期阻断）。
func LoadPolicy(data []byte) Policy {
	var raw policyFile
	if err := json.Unmarshal(data, &raw); err != nil {
		panic(fmt.Sprintf("statemachine: state-policy.json 解析失败: %v", err))
	}
	if len(raw.TerminalStates) == 0 || raw.Illegal.HTTPStatus == 0 || raw.Illegal.Code == "" {
		panic("statemachine: state-policy.json 缺少必备段（terminal_states/illegal_transition）")
	}
	return Policy{
		Version:            raw.Version,
		TerminalStates:     raw.TerminalStates,
		IllegalHTTPStatus:  raw.Illegal.HTTPStatus,
		IllegalCode:        raw.Illegal.Code,
		Idempotency:        raw.Idempotency,
		TombstoneMinDays:   raw.Tombstone.MinRetentionDays,
		AckTTLSeconds:      raw.Dispatch.AckTTLSeconds,
		ClockSkewSeconds:   raw.SecurityConstants.ClockSkewToleranceSeconds,
		NonceWindowSeconds: raw.SecurityConstants.NonceDedupWindowSeconds,
		RotationDays:       raw.SecurityConstants.CredentialRotationDays,
	}
}

// Edges 返回全部边（按 CSV 顺序）。
func (m *Machine) Edges() []Edge { return append([]Edge(nil), m.edges...) }

// EdgeFor 返回 from 状态在 event 下的唯一合法边；不存在或需分流（多条候选）时
// ok=false。需按结果分流的迁移（exec_result/reconcile_evidence）用 EdgeTo。
func (m *Machine) EdgeFor(from, event string) (Edge, bool) {
	cands := m.byFromEvt[from+"\x00"+event]
	if len(cands) != 1 {
		return Edge{}, false
	}
	return cands[0], true
}

// EdgeTo 返回 from 状态在 event 下目标为 to 的边（分流选择器：E10/E11、E12/E14）。
func (m *Machine) EdgeTo(from, event, to string) (Edge, bool) {
	for _, e := range m.byFromEvt[from+"\x00"+event] {
		if e.To == to {
			return e, true
		}
	}
	return Edge{}, false
}

// EdgesFrom 返回从 from 出发的全部边。
func (m *Machine) EdgesFrom(from string) []Edge { return m.fromEdges[from] }

// States 返回边集中出现的全部状态（排序）。
func (m *Machine) States() []string {
	set := map[string]bool{}
	for _, e := range m.edges {
		set[e.From] = true
		set[e.To] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// IsTerminal 报告状态是否终态（state-policy.json）。
func (m *Machine) IsTerminal(state string) bool {
	for _, s := range m.policy.TerminalStates {
		if s == state {
			return true
		}
	}
	return false
}

// Policy 返回策略参数。
func (m *Machine) Policy() Policy { return m.policy }

// IllegalTransitionError 为非法迁移（state-policy：409 ILLEGAL_TRANSITION）。
type IllegalTransitionError struct {
	From   string
	Event  string
	Note   string
	Status int
	Code   string
}

// NewIllegalTransitionError 构造非法迁移错误（状态码/错误码取自嵌入权威策略）。
func NewIllegalTransitionError(from, event, note string) *IllegalTransitionError {
	p := Default().Policy()
	return &IllegalTransitionError{From: from, Event: event, Note: note, Status: p.IllegalHTTPStatus, Code: p.IllegalCode}
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("statemachine: 非法状态迁移（%d %s）: state=%s event=%s %s", e.Status, e.Code, e.From, e.Event, e.Note)
}
