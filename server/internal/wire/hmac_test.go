package wire

import (
	"encoding/json"
	"os"
	"testing"

	"cn.worktool.open/server/internal/repo"
)

// vector 为金标准向量条目（镜像 fixtures/hmac-vector.json；消费契约：字段冻结）。
type vector struct {
	Fixture       string `json:"fixture"`
	TokenVersion  int64  `json:"tokenVersion"`
	CommandID     string `json:"commandId"`
	EventID       string `json:"eventId"`
	Seq           int64  `json:"seq"`
	Ts            int64  `json:"ts"`
	Nonce         string `json:"nonce"`
	EncryptType   int    `json:"encryptType"`
	PayloadDigest string `json:"payloadDigest"`
	InputString   string `json:"inputString"`
	HMAC          string `json:"hmac"`
}

type negativeCase struct {
	Case                string `json:"case"`
	BaseFixture         string `json:"baseFixture"`
	OriginalSeq         int64  `json:"originalSeq"`
	TamperedSeq         int64  `json:"tamperedSeq"`
	DeclaredHMAC        string `json:"declaredHmac"`
	TamperedInputString string `json:"tamperedInputString"`
	HMACOverTampered    string `json:"hmacOverTamperedInput"`
}

type hmacVectorJSON struct {
	DeviceTokenTest   string `json:"deviceTokenTest"`
	HMACInputFormat   string `json:"hmacInputFormat"`
	PayloadDigestRule struct {
		EmptyPayloadSHA256 string `json:"emptyPayloadSha256"`
	} `json:"payloadDigestRule"`
	Vectors  []vector       `json:"vectors"`
	Negative []negativeCase `json:"negative"`
}

func loadHMACVector(t *testing.T) *hmacVectorJSON {
	t.Helper()
	path, err := repo.ProtocolPath("fixtures/hmac-vector.json")
	if err != nil {
		t.Fatalf("定位金标准向量失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	var v hmacVectorJSON
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("解析 hmac-vector.json 失败: %v", err)
	}
	return &v
}

func loadFixtureEnvelope(t *testing.T, name string) *Envelope {
	t.Helper()
	path, err := repo.ProtocolPath("fixtures/" + name)
	if err != nil {
		t.Fatalf("定位夹具失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取夹具失败: %v", err)
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("解析信封 %s 失败: %v", name, err)
	}
	return env
}

func findVector(t *testing.T, v *hmacVectorJSON, fixture string) vector {
	t.Helper()
	for _, x := range v.Vectors {
		if x.Fixture == fixture {
			return x
		}
	}
	t.Fatalf("向量中未找到夹具 %s", fixture)
	return vector{}
}

// TestGoldenVectorPayloadDigest：payloadDigest 逐字节复算（JCS/密文串/空载荷三路）。
// 共同关闭 AUD-03 C-4（夹具被 L2 单测逐字节复算引用）。
func TestGoldenVectorPayloadDigest(t *testing.T) {
	v := loadHMACVector(t)
	if v.PayloadDigestRule.EmptyPayloadSHA256 != EmptyPayloadDigest {
		t.Fatalf("空载荷常量与规格不一致: 规格=%s 实现=%s", v.PayloadDigestRule.EmptyPayloadSHA256, EmptyPayloadDigest)
	}
	for _, vec := range v.Vectors {
		t.Run(vec.Fixture, func(t *testing.T) {
			env := loadFixtureEnvelope(t, vec.Fixture)
			digest, err := PayloadDigest(env.EncryptType, env.List, env.EncryptedList)
			if err != nil {
				t.Fatalf("payloadDigest 计算失败: %v", err)
			}
			if digest != vec.PayloadDigest {
				t.Fatalf("payloadDigest 不一致\n 夹具=%s\n 实现=%s", vec.PayloadDigest, digest)
			}
			if env.EncryptType == 0 && len(env.List) > 0 {
				// JCS 规范化应幂等（canonical 再解析后再规范化不变）
				c1, err := Canonical(env.List)
				if err != nil {
					t.Fatalf("JCS 失败: %v", err)
				}
				c2, err := Canonical(c1)
				if err != nil {
					t.Fatalf("JCS 二次规范化失败: %v", err)
				}
				if string(c1) != string(c2) {
					t.Fatalf("JCS 不幂等:\n %s\n %s", c1, c2)
				}
			}
		})
	}
}

// TestGoldenVectorInputStringAndHMAC：输入式（C-9：commandId|eventId|seq|ts|nonce|digest）
// 与 HMAC 逐字节复算，且与夹具帧内 meta.hmac 一致。
func TestGoldenVectorInputStringAndHMAC(t *testing.T) {
	v := loadHMACVector(t)
	if v.HMACInputFormat != "commandId|eventId|seq|ts|nonce|payloadDigest" {
		t.Fatalf("向量输入式与本实现口径不一致（C-9 修订未同步？）: %s", v.HMACInputFormat)
	}
	for _, vec := range v.Vectors {
		t.Run(vec.Fixture, func(t *testing.T) {
			gotInput := HMACInputString(vec.CommandID, vec.EventID, vec.Seq, vec.Ts, vec.Nonce, vec.PayloadDigest)
			if gotInput != vec.InputString {
				t.Fatalf("inputString 不一致\n 夹具=%q\n 实现=%q", vec.InputString, gotInput)
			}
			gotHMAC, err := ComputeHMAC(v.DeviceTokenTest, vec.CommandID, vec.EventID, vec.Seq, vec.Ts, vec.Nonce, vec.PayloadDigest)
			if err != nil {
				t.Fatalf("HMAC 计算失败: %v", err)
			}
			if gotHMAC != vec.HMAC {
				t.Fatalf("hmac 不一致\n 夹具=%s\n 实现=%s", vec.HMAC, gotHMAC)
			}
			// 夹具帧内 meta.hmac 同源验证
			env := loadFixtureEnvelope(t, vec.Fixture)
			meta, err := env.V2Meta()
			if err != nil {
				t.Fatalf("解析 meta 失败: %v", err)
			}
			if meta.HMAC != gotHMAC {
				t.Fatalf("夹具 meta.hmac 与复算不一致: 夹具=%s 复算=%s", meta.HMAC, gotHMAC)
			}
			ok, err := VerifyHMAC(v.DeviceTokenTest, meta.HMAC, vec.CommandID, vec.EventID, vec.Seq, vec.Ts, vec.Nonce, vec.PayloadDigest)
			if err != nil || !ok {
				t.Fatalf("VerifyHMAC 应通过: ok=%v err=%v", ok, err)
			}
		})
	}
}

// TestGoldenVectorNegativeTamperSeq：篡改 seq 必败（AUD-03 C-9 反例）。
func TestGoldenVectorNegativeTamperSeq(t *testing.T) {
	v := loadHMACVector(t)
	if len(v.Negative) == 0 {
		t.Fatal("金标准向量 negative[] 为空，消费契约变化须提 AUD")
	}
	for _, neg := range v.Negative {
		t.Run(neg.Case, func(t *testing.T) {
			got, err := ComputeHMACOverInputString(v.DeviceTokenTest, neg.TamperedInputString)
			if err != nil {
				t.Fatalf("复算失败: %v", err)
			}
			if got != neg.HMACOverTampered {
				t.Fatalf("篡改输入的 HMAC 不一致\n 夹具=%s\n 实现=%s", neg.HMACOverTampered, got)
			}
			if got == neg.DeclaredHMAC {
				t.Fatal("篡改 seq 后 HMAC 仍等于声明值（seq 未纳入输入式？C-9 违规）")
			}
			// 端到端口径：篡改 seq 后按篡改帧字段重算并与声明 hmac 比对应失配（→ 4401）
			v0 := findVector(t, v, neg.BaseFixture)
			ok, err := VerifyHMAC(v.DeviceTokenTest, neg.DeclaredHMAC, v0.CommandID, v0.EventID, neg.TamperedSeq, v0.Ts, v0.Nonce, v0.PayloadDigest)
			if err != nil {
				t.Fatalf("VerifyHMAC 失败: %v", err)
			}
			if ok {
				t.Fatal("篡改 seq 的帧不应通过验签（期望 4401）")
			}
		})
	}
}

// TestHMACInputStringFormat：输入式格式快照（防回归）。
func TestHMACInputStringFormat(t *testing.T) {
	got := HMACInputString("cid", "eid", 7, 1234, "n", "d")
	want := "cid|eid|7|1234|n|d"
	if got != want {
		t.Fatalf("inputString = %q, want %q", got, want)
	}
}
