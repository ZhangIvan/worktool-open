// 逐帧鉴权原语（protocol/spec.md §5.2，冻结；AUD-03 C-9：seq 纳入 HMAC 输入式）。
package wire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// EmptyPayloadDigest：list 缺失或空数组时的 payloadDigest（SHA-256(空字节串)，
// spec §5.2.2 冻结常量）。
const EmptyPayloadDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// PayloadDigest 计算帧载荷摘要（spec §5.2.2）：
//   - encryptType=0：hex(SHA-256(JCS(list)))；list 缺失或空数组 → EmptyPayloadDigest
//   - encryptType=2：hex(SHA-256(encryptedList 字符串原样 UTF-8 字节))（先验签后解密）
//
// listJSON 须为 JSON 数组文本（可为 nil）。
func PayloadDigest(encryptType int, listJSON []byte, encryptedList string) (string, error) {
	if encryptType == 2 {
		sum := sha256.Sum256([]byte(encryptedList))
		return hex.EncodeToString(sum[:]), nil
	}
	trimmed := trimJSONSpace(listJSON)
	if len(trimmed) == 0 || string(trimmed) == "[]" || string(trimmed) == "null" {
		return EmptyPayloadDigest, nil
	}
	canonical, err := Canonical(trimmed)
	if err != nil {
		return "", fmt.Errorf("wire/hmac: list 规范化失败: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// HMACInputString 构造 HMAC 输入串（spec §5.2.2，C-9 冻结输入式）：
//
//	commandId|eventId|seq|ts|nonce|payloadDigest
func HMACInputString(commandID, eventID string, seq, ts int64, nonce, payloadDigest string) string {
	return fmt.Sprintf("%s|%s|%d|%d|%s|%s", commandID, eventID, seq, ts, nonce, payloadDigest)
}

// ComputeHMAC 以 deviceToken 原始字节（hex 输入）为密钥计算逐帧 HMAC（小写 hex 64 字符）。
func ComputeHMAC(deviceTokenHex, commandID, eventID string, seq, ts int64, nonce, payloadDigest string) (string, error) {
	return ComputeHMACOverInputString(deviceTokenHex, HMACInputString(commandID, eventID, seq, ts, nonce, payloadDigest))
}

// ComputeHMACOverInputString 直接对完整输入串计算 HMAC
// （金标准向量 inputString 逐字节复算入口，fixtures/hmac-vector.json）。
func ComputeHMACOverInputString(deviceTokenHex, inputString string) (string, error) {
	key, err := hex.DecodeString(deviceTokenHex)
	if err != nil {
		return "", fmt.Errorf("wire/hmac: deviceToken 非 hex: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprint(mac, inputString)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifyHMAC 常量时间比对声明的 hmac 与重算值。
func VerifyHMAC(deviceTokenHex, declaredHMAC, commandID, eventID string, seq, ts int64, nonce, payloadDigest string) (bool, error) {
	want, err := ComputeHMAC(deviceTokenHex, commandID, eventID, seq, ts, nonce, payloadDigest)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(want), []byte(declaredHMAC)), nil
}

func trimJSONSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isJSONSpace(b[start]) {
		start++
	}
	for end > start && isJSONSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
