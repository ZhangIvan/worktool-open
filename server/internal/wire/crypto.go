// v2 会话加密原语（protocol/spec.md §5.3，冻结）：
// AES-256-GCM + 会话密钥 ECDH(X25519)+HKDF-SHA256 派生（修复上游硬编码 CBC，SR-02）。
package wire

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// SessionKeyInfo 为 HKDF info 输入（spec §5.3 冻结值）。
const SessionKeyInfo = "worktool-open/v2/session"

// SessionKeySize 为派生会话密钥长度（32 字节 = AES-256）。
const SessionKeySize = 32

// GenerateX25519 生成一对新的 X25519 临时密钥（每次配对/重协商新生成，spec §5.5.1）。
// 返回 (privateHex, publicHex)。
func GenerateX25519() (string, string, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("wire/crypto: 生成 X25519 密钥失败: %w", err)
	}
	return hex.EncodeToString(priv.Bytes()), hex.EncodeToString(priv.PublicKey().Bytes()), nil
}

// DeriveSessionKey 计算会话密钥（spec §5.3 冻结式）：
//
//	shared  = X25519(本端私钥, 对端公钥)
//	K_session = HKDF-SHA256(ikm=shared, salt=UTF-8(sessionId), info=SessionKeyInfo, L=32)
func DeriveSessionKey(ownPrivHex, peerPubHex, sessionID string) ([]byte, error) {
	privBytes, err := hex.DecodeString(ownPrivHex)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: 本端 X25519 私钥非 hex: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: 本端 X25519 私钥非法: %w", err)
	}
	pubBytes, err := hex.DecodeString(peerPubHex)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: 对端 X25519 公钥非 hex: %w", err)
	}
	pub, err := ecdh.X25519().NewPublicKey(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: 对端 X25519 公钥非法: %w", err)
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: X25519 ECDH 失败: %w", err)
	}
	return hkdfSHA256(shared, []byte(sessionID), []byte(SessionKeyInfo), SessionKeySize), nil
}

// hkdfSHA256 实现 RFC 5869 HKDF（extract + expand），SHA-256 为哈希函数。
// 不引 golang.org/x/crypto（车道依赖纪律：仅 sqlite + websocket 两个第三方库）。
func hkdfSHA256(ikm, salt, info []byte, length int) []byte {
	extract := func(key, data []byte) []byte {
		m := hmac.New(sha256.New, key)
		m.Write(data)
		return m.Sum(nil)
	}
	prk := extract(salt, ikm)
	var out, t []byte
	for i := byte(1); len(out) < length; i++ {
		t = append(append(append([]byte{}, t...), info...), i)
		t = extract(prk, t)
		out = append(out, t...)
	}
	return out[:length]
}

// GCMNonce 由 eventId 确定性导出 12 字节 GCM nonce（spec §5.3 冻结：
// bytes[0:12] of SHA-256(UTF-8(eventId))，帧唯一、免传输）。
func GCMNonce(eventID string) []byte {
	sum := sha256.Sum256([]byte(eventID))
	return sum[:12]
}

// GCMAAD 构造 GCM 附加认证数据（spec §5.3 冻结式；C-9 后保留为纵深防御）：
//
//	UTF-8("ver=2|seq=" + seq + "|eventId=" + eventId + "|ts=" + ts)
func GCMAAD(seq int64, eventID string, ts int64) []byte {
	return fmt.Appendf(nil, "ver=2|seq=%d|eventId=%s|ts=%d", seq, eventID, ts)
}

// SealList 以 AES-256-GCM 加密明文 JSON 数组，返回 hex(密文||16字节tag)。
func SealList(key, nonce, aad, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("wire/crypto: AES 初始化失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("wire/crypto: GCM 初始化失败: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	return hex.EncodeToString(sealed), nil
}

// OpenList 解密 hex(密文||tag)。验签失败/解密失败 = 帧整体拒绝（spec §5.3）。
func OpenList(key, nonce, aad []byte, sealedHex string) ([]byte, error) {
	sealed, err := hex.DecodeString(sealedHex)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: encryptedList 非 hex: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: AES 初始化失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: GCM 初始化失败: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("wire/crypto: GCM 解密/认证失败: %w", err)
	}
	return plain, nil
}

// RandomHex 生成 n 字节加密随机数的 hex 表示（deviceToken 32 字节 / nonce 16 字节等）。
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("wire/crypto: 随机数生成失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewUUID 生成符合 protocol schema UUID 模式（[0-9a-f]{8}-…-[0-9a-f]{12}）的 v4 UUID。
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源失败属系统级异常：响亮失败（panic），绝不静默退化为可预测/可碰撞值（AUD-M2-04）。
		panic(fmt.Sprintf("wire/crypto: 随机数生成失败: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
