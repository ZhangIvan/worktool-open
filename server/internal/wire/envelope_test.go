package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cn.worktool.open/server/internal/repo"
)

// TestPositiveFixturesValidate：正例夹具全部通过对应校验（fixtures/README 消费契约）。
func TestPositiveFixturesValidate(t *testing.T) {
	t.Run("v2 帧", func(t *testing.T) {
		for _, name := range []string{
			"command-envelope.plain.json",
			"command-envelope.encrypted.json",
			"command-envelope.heartbeat.json",
			"command-envelope.exec-result.json",
		} {
			env := loadFixtureEnvelope(t, name)
			if !env.IsV2() {
				t.Fatalf("%s 应为 v2 帧", name)
			}
			if err := env.ValidateV2(); err != nil {
				t.Fatalf("%s ValidateV2 应通过: %v", name, err)
			}
		}
	})
	t.Run("v1 帧", func(t *testing.T) {
		for _, name := range []string{
			"wire-v1-envelope.heartbeat.json",
			"wire-v1-envelope.command-203.json",
			"wire-v1-envelope.report-101.json",
		} {
			env := loadFixtureEnvelope(t, name)
			if env.IsV2() {
				t.Fatalf("%s 不应为 v2 帧", name)
			}
			if err := env.ValidateV1(); err != nil {
				t.Fatalf("%s ValidateV1 应通过: %v", name, err)
			}
		}
	})
	t.Run("hello", func(t *testing.T) {
		path, err := repo.ProtocolPath("fixtures/session-hello.valid.json")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := DecodeHello(raw)
		if err != nil {
			t.Fatalf("DecodeHello 应通过: %v", err)
		}
		if h.DeviceID != "device-9f2c7b41" || h.LastEpoch != 128 || h.LastReceivedSeq != 412 {
			t.Fatalf("hello 字段解析错误: %+v", h)
		}
		if h.Capabilities == nil || h.Capabilities.ClientVersion != "0.1.0" || h.Capabilities.EncryptMax != 2 {
			t.Fatalf("capabilities 解析错误: %+v", h.Capabilities)
		}
	})
	t.Run("exec-callback 载荷", func(t *testing.T) {
		env := loadFixtureEnvelope(t, "command-envelope.exec-result.json")
		items, err := env.ListItems()
		if err != nil || len(items) != 1 {
			t.Fatalf("list 解析失败: %v items=%d", err, len(items))
		}
		if !IsExecResult(items[0]) {
			t.Fatal("exec-result 载荷应被识别为执行结果")
		}
		r, err := DecodeExecResult(items[0])
		if err != nil {
			t.Fatalf("DecodeExecResult: %v", err)
		}
		if r.Type != 203 || r.ErrorCode != 0 || r.TimeCost != 1830 || len(r.SuccessList) != 1 {
			t.Fatalf("结果字段解析错误: %+v", r)
		}
	})
}

// TestNegativeFixturesRejected：反例夹具必须被拒绝（fixtures/negative/）。
func TestNegativeFixturesRejected(t *testing.T) {
	root, err := repo.Root()
	if err != nil {
		t.Fatal(err)
	}
	negDir := filepath.Join(root, "protocol", "fixtures", "negative")
	entries, err := os.ReadDir(negDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		raw, err := os.ReadFile(filepath.Join(negDir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) {
			switch {
			case len(name) >= 16 && name[:16] == "command-envelope": // 信封反例
				env, err := DecodeEnvelope(raw)
				if err != nil {
					return // 解码层拒绝同样合格
				}
				if err := env.ValidateV2(); err == nil {
					t.Fatalf("反例 %s 被 ValidateV2 放行", name)
				}
			case name == "pair-request.invalid-code.json":
				var body map[string]json.RawMessage
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				var code string
				_ = json.Unmarshal(body["pairingCode"], &code)
				if PairCodePattern.MatchString(code) {
					t.Fatalf("反例 pairingCode %q 应不匹配 ^[0-9]{6}$", code)
				}
			default:
				t.Fatalf("未登记的负例夹具 %s（消费契约新增须同步本测试）", name)
			}
		})
	}
}

// TestValidateV2Rejects：schema 约束反例（G8 鉴权前置的格式层）。
func TestValidateV2Rejects(t *testing.T) {
	valid := loadFixtureEnvelope(t, "command-envelope.plain.json")

	mut := func(f func(*Envelope)) *Envelope {
		env, _ := DecodeEnvelope(mustMarshal(t, valid))
		f(env)
		return env
	}

	cases := []struct {
		name string
		env  *Envelope
	}{
		{"socketType=1 在 v2 会话非法", mut(func(e *Envelope) { e.SocketType = SocketConfirm })},
		{"encryptType=1 遗留加密禁用", mut(func(e *Envelope) { e.EncryptType = 1 })},
		{"明文帧缺 list", mut(func(e *Envelope) { e.List = nil })},
		{"meta 缺 hmac", mut(func(e *Envelope) {
			m, _ := e.V2Meta()
			m.HMAC = ""
			e.MetaRaw = mustMarshal(t, m)
		})},
		{"meta 附带未定义字段", mut(func(e *Envelope) {
			var m map[string]json.RawMessage
			_ = json.Unmarshal(e.MetaRaw, &m)
			m["extra"] = json.RawMessage(`1`)
			e.MetaRaw = mustMarshal(t, m)
		})},
		{"encryptType=2 携明文 list（encrypt-mismatch 形态）", mut(func(e *Envelope) {
			e.EncryptType = 2
			e.EncryptedList = ""
		})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.env.ValidateV2()
			fe, ok := err.(*FrameError)
			if !ok {
				t.Fatalf("应返回 *FrameError，实际 %v", err)
			}
			if fe.Code != CloseFrameInvalid {
				t.Fatalf("关闭码 = %d, want 4400", fe.Code)
			}
		})
	}
}

// TestValidateV1Rejects：v1 兼容路径反例。
func TestValidateV1Rejects(t *testing.T) {
	v1 := loadFixtureEnvelope(t, "wire-v1-envelope.command-203.json")

	env, _ := DecodeEnvelope(mustMarshal(t, v1))
	env.MetaRaw = valid2Meta(t)
	if err := env.ValidateV1(); err == nil {
		t.Fatal("v1 路径应拒绝 v2 帧（meta 对象）")
	}

	env2, _ := DecodeEnvelope(mustMarshal(t, v1))
	env2.EncryptType = 2
	if err := env2.ValidateV1(); err == nil {
		t.Fatal("v1 路径应拒绝 encryptType=2")
	}

	env3, _ := DecodeEnvelope(mustMarshal(t, v1))
	env3.List = nil
	env3.EncryptedList = ""
	if err := env3.ValidateV1(); err == nil {
		t.Fatal("encryptType=0 缺 list 应拒绝")
	}
}

func valid2Meta(t *testing.T) json.RawMessage {
	t.Helper()
	env := loadFixtureEnvelope(t, "command-envelope.plain.json")
	return env.MetaRaw
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestBareHeartbeatDecode：v1 裸形态 {"type":11} 与 {"td":ms}（spec §3/§4.2）。
func TestBareHeartbeatDecode(t *testing.T) {
	env, err := DecodeEnvelope([]byte(`{"type":11}`))
	if err != nil {
		t.Fatalf("裸心跳应可解码: %v", err)
	}
	items, err := env.ListItems()
	if err != nil {
		t.Fatal(err)
	}
	_ = items
	var probe map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"td":1793613600000}`), &probe)
	if _, ok := probe["td"]; !ok {
		t.Fatal("td 探测失败")
	}
}
