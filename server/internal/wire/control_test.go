// 控制帧冻结夹具消费测试（T3.4，spec §5.7）：Go 侧与 protocol/ 机器件三方对齐的
// Go 分量——正例夹具全部通过 [DecodeControlPayload]/[ValidateHelloAckShape]，
// 反例夹具必须被拒。夹具清单冻结契约见 protocol/fixtures/README.md。
package wire

import (
	"encoding/json"
	"os"
	"testing"

	"cn.worktool.open/server/internal/repo"
)

func controlFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	path, err := repo.ProtocolPath(name)
	if err != nil {
		t.Fatalf("定位夹具失败: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读夹具失败: %v", err)
	}
	return b
}

// 正例：901~904 五种变体全部按 schema 变体名解出。
func TestControlPayloadFixturesPositive(t *testing.T) {
	cases := map[string]string{
		"fixtures/control-payload.901-rotate.json":  "credentialRotate",
		"fixtures/control-payload.902-revoked.json": "credentialRevoked",
		"fixtures/control-payload.903-resync.json":  "resyncRequest",
		"fixtures/control-payload.904-replay.json":  "journalReplay",
		"fixtures/control-payload.904-request.json": "journalRequest",
	}
	for name, want := range cases {
		got, err := DecodeControlPayload(controlFixture(t, name))
		if err != nil {
			t.Errorf("%s: 正例夹具必须通过: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("%s: 变体 %s != 期望 %s", name, got, want)
		}
	}
}

// 反例：schema 拒绝形态在 Go 镜像同样拒绝。
func TestControlPayloadFixturesNegative(t *testing.T) {
	for _, name := range []string{
		"fixtures/negative/control-payload.901-bad-token.json",
		"fixtures/negative/control-payload.903-extra-field.json",
		"fixtures/negative/control-payload.904-empty.json",
	} {
		if _, err := DecodeControlPayload(controlFixture(t, name)); err == nil {
			t.Errorf("%s: 反例夹具必须被拒", name)
		}
	}
}

// hello_ack 冻结基准夹具（Go 实际产出字节采集）：形状校验通过；
// 反例（冻结值漂移/信封化残缺）拒绝。
func TestHelloAckFrozenShape(t *testing.T) {
	var ack HelloAck
	if err := json.Unmarshal(controlFixture(t, "fixtures/session-hello-ack.go-actual.json"), &ack); err != nil {
		t.Fatalf("基准夹具解析失败: %v", err)
	}
	if err := ValidateHelloAckShape(&ack); err != nil {
		t.Fatalf("基准夹具（Go 实际字节）必须符合冻结形状: %v", err)
	}
	for _, name := range []string{
		"fixtures/negative/session-hello-ack.bad-frozen.json",
		"fixtures/negative/session-hello-ack.envelope-wrapped.json",
	} {
		var bad HelloAck
		if err := json.Unmarshal(controlFixture(t, name), &bad); err != nil {
			t.Fatalf("%s 解析失败: %v", name, err)
		}
		if err := ValidateHelloAckShape(&bad); err == nil {
			t.Errorf("%s: 反例夹具必须被拒", name)
		}
	}
}
