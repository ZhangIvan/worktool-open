package wire

import (
	"encoding/hex"
	"testing"
)

// TestSessionKeyAgreement：ECDH 双端一致性 + HKDF 派生（spec §5.3 冻结式）。
func TestSessionKeyAgreement(t *testing.T) {
	devPriv, devPub, err := GenerateX25519()
	if err != nil {
		t.Fatalf("生成设备密钥失败: %v", err)
	}
	srvPriv, srvPub, err := GenerateX25519()
	if err != nil {
		t.Fatalf("生成服务端密钥失败: %v", err)
	}
	const sessionID = "5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80"
	deviceSide, err := DeriveSessionKey(devPriv, srvPub, sessionID)
	if err != nil {
		t.Fatalf("设备侧派生失败: %v", err)
	}
	serverSide, err := DeriveSessionKey(srvPriv, devPub, sessionID)
	if err != nil {
		t.Fatalf("服务端派生失败: %v", err)
	}
	if len(deviceSide) != SessionKeySize {
		t.Fatalf("会话密钥长度 = %d, want %d", len(deviceSide), SessionKeySize)
	}
	if hex.EncodeToString(deviceSide) != hex.EncodeToString(serverSide) {
		t.Fatal("双端会话密钥不一致（ECDH/HKDF 实现错误）")
	}
	// 换 sessionId（HKDF salt）应派生不同密钥：前向隔离以协商周期为界（spec §5.3）
	other, err := DeriveSessionKey(devPriv, srvPub, "other-session")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hex.EncodeToString(other) == hex.EncodeToString(deviceSide) {
		t.Fatal("不同 sessionId 派生出相同密钥（salt 未生效）")
	}
}

// TestSealOpenRoundtrip：AES-256-GCM 封解 + AAD/nonce 冻结式 + 篡改必败。
func TestSealOpenRoundtrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	const eventID = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b"
	nonce := GCMNonce(eventID)
	if len(nonce) != 12 {
		t.Fatalf("GCM nonce 长度 = %d, want 12", len(nonce))
	}
	aad := GCMAAD(414, eventID, 1793613606000)
	wantAAD := "ver=2|seq=414|eventId=8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b|ts=1793613606000"
	if string(aad) != wantAAD {
		t.Fatalf("AAD = %q, want %q", aad, wantAAD)
	}
	plain := []byte(`[{"type":203,"targetRoom":"外部群测试群"}]`)
	sealedHex, err := SealList(key, nonce, aad, plain)
	if err != nil {
		t.Fatalf("SealList: %v", err)
	}
	got, err := OpenList(key, nonce, aad, sealedHex)
	if err != nil {
		t.Fatalf("OpenList: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("roundtrip 不一致: %q != %q", got, plain)
	}
	// nonce 由 eventId 确定性导出：同 eventId 双方一致
	if n2 := GCMNonce(eventID); hex.EncodeToString(n2) != hex.EncodeToString(nonce) {
		t.Fatal("GCMNonce 非确定性")
	}
	// 篡改必败
	tampered := sealedHex[:len(sealedHex)-2] + "ff"
	if _, err := OpenList(key, nonce, aad, tampered); err == nil {
		t.Fatal("篡改密文应解密失败")
	}
	// AAD 不匹配必败（改 seq）
	if _, err := OpenList(key, nonce, GCMAAD(415, eventID, 1793613606000), sealedHex); err == nil {
		t.Fatal("AAD 不匹配应解密失败")
	}
	// eventId 变化 → nonce 变化 → 解密失败
	if _, err := OpenList(key, GCMNonce("another-event-id"), aad, sealedHex); err == nil {
		t.Fatal("nonce 不匹配应解密失败")
	}
}

// TestHKDFRFC5869TestCase1：HKDF-SHA256 对照 RFC 5869 测试向量 #1（OKM 42 字节）。
func TestHKDFRFC5869TestCase1(t *testing.T) {
	ikm := mustHex(t, "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b")
	salt := mustHex(t, "000102030405060708090a0b0c")
	info := mustHex(t, "f0f1f2f3f4f5f6f7f8f9")
	okm := hkdfSHA256(ikm, salt, info, 42)
	want := "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865"
	if hex.EncodeToString(okm) != want {
		t.Fatalf("HKDF OKM 不一致:\n got  %x\n want %s", okm, want)
	}
}

// TestNewUUIDFormat：UUID 满足 schema 模式（小写 hex 8-4-4-4-12）。
func TestNewUUIDFormat(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := NewUUID()
		if !UUIDPattern.MatchString(id) {
			t.Fatalf("UUID %q 不匹配模式", id)
		}
		if seen[id] {
			t.Fatalf("UUID 重复: %s", id)
		}
		seen[id] = true
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex 解码失败: %v", err)
	}
	return b
}
