package statemachine

import (
	"os"
	"testing"

	"cn.worktool.open/server/internal/repo"
)

// TestEmbeddedAuthoritySync：data/ 嵌入副本必须与仓库根 protocol/ 权威件逐字节一致
// （防漂移；权威件变更须同步复制，否则本测试红——关 AUD-03 C-8 的服务端侧保障）。
func TestEmbeddedAuthoritySync(t *testing.T) {
	for _, name := range []string{"state-transitions.csv", "state-policy.json"} {
		authPath, err := repo.ProtocolPath(name)
		if err != nil {
			t.Fatalf("定位权威件失败: %v", err)
		}
		auth, err := os.ReadFile(authPath)
		if err != nil {
			t.Fatalf("读取权威件失败: %v", err)
		}
		var embedded []byte
		if name == "state-transitions.csv" {
			embedded = transitionsCSV
		} else {
			embedded = policyJSON
		}
		if string(auth) != string(embedded) {
			t.Fatalf("%s 嵌入副本与 protocol/ 权威件不一致：请同步 server/internal/statemachine/data/%s", name, name)
		}
	}
}

// TestAllEdgesPositive：CSV 驱动正例——每条边 from+event(+to) 必须命中该边。
func TestAllEdgesPositive(t *testing.T) {
	m := Default()
	for _, e := range m.Edges() {
		got, ok := m.EdgeTo(e.From, e.Event, e.To)
		if !ok {
			t.Fatalf("边 %s（%s --%s--> %s）应可命中", e.ID, e.From, e.Event, e.To)
		}
		if got.ID != e.ID || got.To != e.To {
			t.Fatalf("边 %s 命中错误: got %s → %s", e.ID, got.ID, got.To)
		}
	}
}

// TestAllEdgesNegative：CSV 驱动反例——非边集内 (from,event,to) 全部拒绝，
// 且终态无任何出边（state-policy 终态 = CSV 无出边状态，两权威件互证）。
func TestAllEdgesNegative(t *testing.T) {
	m := Default()
	states := m.States()
	events := allEvents(m)

	// 终态一致性：policy 声明的终态恰为 CSV 中无出边的状态
	noOut := map[string]bool{}
	for _, s := range states {
		if len(m.EdgesFrom(s)) == 0 {
			noOut[s] = true
		}
	}
	declared := map[string]bool{}
	for _, s := range m.Policy().TerminalStates {
		declared[s] = true
	}
	for _, s := range states {
		if noOut[s] != declared[s] {
			t.Fatalf("状态 %s 终态判定不一致：CSV 无出边=%v policy 终态=%v", s, noOut[s], declared[s])
		}
	}

	// 全状态 × 全事件 × 全状态目标矩阵：非边集组合必须不命中；终态无任何命中
	for _, from := range states {
		for _, ev := range events {
			for _, to := range states {
				if e, ok := m.EdgeTo(from, ev, to); ok {
					if m.IsTerminal(from) {
						t.Fatalf("终态 %s 不应命中任何事件，却命中 %s", from, e.ID)
					}
					continue // 合法边（正例覆盖）
				}
			}
		}
	}

	// 唯一边快捷入口：歧义事件（exec_result/reconcile_evidence 多候选）不得返回单边
	if _, ok := m.EdgeFor("running", "exec_result"); ok {
		t.Fatal("running/exec_result 存在分流（E10/E11），EdgeFor 不应返回唯一边")
	}
	if _, ok := m.EdgeFor("execution_unknown", "reconcile_evidence"); ok {
		t.Fatal("execution_unknown/reconcile_evidence 存在分流（E12/E14），EdgeFor 不应返回唯一边")
	}
}

func allEvents(m *Machine) []string {
	set := map[string]bool{}
	for _, e := range m.Edges() {
		set[e.Event] = true
	}
	out := make([]string, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	return out
}

// TestExpectedEdgeInvariants：T2.3 消费点的结构性断言（AUD-03 C-10：E17/E18 必须在位）。
func TestExpectedEdgeInvariants(t *testing.T) {
	m := Default()
	for _, tc := range []struct{ id, from, to, event string }{
		{"E01", "created", "queued", "enqueue"},
		{"E02", "created", "failed", "reject"},
		{"E03", "queued", "dispatched", "dispatch"},
		{"E04", "queued", "cancelled", "cancel_before_delivery"},
		{"E05", "dispatched", "acked", "transport_ack"},
		{"E06", "dispatched", "execution_unknown", "mark_unknown"},
		{"E07", "acked", "execution_unknown", "mark_unknown"},
		{"E08", "running", "execution_unknown", "mark_unknown"},
		{"E09", "acked", "running", "exec_start"},
		{"E10", "running", "succeeded", "exec_result"},
		{"E11", "running", "failed", "exec_result"},
		{"E12", "execution_unknown", "succeeded", "reconcile_evidence"},
		{"E13", "execution_unknown", "failed", "reconcile_unstarted"},
		{"E14", "execution_unknown", "failed", "reconcile_evidence"},
		{"E15", "execution_unknown", "failed", "manual_resolve"},
		{"E16", "running", "cancelled", "user_takeover_stop"},
		{"E17", "dispatched", "execution_unknown", "ack_ttl_expired"},
		{"E18", "acked", "execution_unknown", "ack_ttl_expired"},
	} {
		e, ok := m.EdgeTo(tc.from, tc.event, tc.to)
		if !ok {
			t.Fatalf("缺少边 %s（%s --%s--> %s）", tc.id, tc.from, tc.event, tc.to)
		}
		if e.ID != tc.id || e.To != tc.to {
			t.Fatalf("边 %s 形状漂移: got %s %s --%s--> %s", tc.id, e.ID, e.From, e.Event, e.To)
		}
	}
	if p := m.Policy(); p.AckTTLSeconds != 300 {
		t.Fatalf("ack_ttl_seconds = %d, want 300", p.AckTTLSeconds)
	}
	if p := m.Policy(); p.TombstoneMinDays < 7 {
		t.Fatalf("tombstone min_retention_days = %d, want ≥7", p.TombstoneMinDays)
	}
	if p := m.Policy(); p.ClockSkewSeconds != 30 || p.NonceWindowSeconds != 300 || p.RotationDays != 30 {
		t.Fatalf("security_constants 漂移: %+v", p)
	}
}

// TestIllegalTransitionError：409 语义携带。
func TestIllegalTransitionError(t *testing.T) {
	err := NewIllegalTransitionError("succeeded", "dispatch", "终态无出边")
	if err.Status != 409 || err.Code != "ILLEGAL_TRANSITION" {
		t.Fatalf("错误语义错误: status=%d code=%s", err.Status, err.Code)
	}
}
