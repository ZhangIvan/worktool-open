// Package security contains pure, fail-closed callback preparation helpers.
// It does not authenticate callers, obtain keys, resolve DNS or open sockets.
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
)

const MaxBodyBytes = 65536
const maxSafeInteger int64 = 9007199254740991
const ReplayWindowMillis int64 = 30000

var ErrSignatureInput = errors.New("CALLBACK_SIGNATURE_INPUT_INVALID")

func eventIDValid(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// Sign accepts only an already-provisioned 32-byte runtime key. It generates and
// persists nothing. The caller retains ownership of key/body and must prevent
// concurrent mutation. Body bytes are neither parsed nor reserialized.
func Sign(key []byte, eventID string, timestampMS int64, body []byte) (string, error) {
	if len(key) != 32 || !eventIDValid(eventID) || timestampMS < 0 || timestampMS > maxSafeInteger || len(body) == 0 || len(body) > MaxBodyBytes {
		return "", ErrSignatureInput
	}
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(eventID))
	_, _ = m.Write([]byte("." + strconv.FormatInt(timestampMS, 10) + "."))
	_, _ = m.Write(body)
	return hex.EncodeToString(m.Sum(nil)), nil
}

// Verify is a pure receiver/reference helper. Durable event deduplication must be
// performed separately and atomically with the receiver's own processing.
func Verify(key []byte, eventID, timestamp, signature string, body []byte, nowMS int64) bool {
	if len(timestamp) == 0 || len(timestamp) > 16 || nowMS < 0 || nowMS > maxSafeInteger || len(signature) != 64 {
		return false
	}
	ms, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || ms < 0 || ms > maxSafeInteger || strconv.FormatInt(ms, 10) != timestamp || ms-nowMS > ReplayWindowMillis || nowMS-ms > ReplayWindowMillis {
		return false
	}
	for _, c := range []byte(signature) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	expected, err := Sign(key, eventID, ms, body)
	if err != nil {
		return false
	}
	got, _ := hex.DecodeString(signature)
	want, _ := hex.DecodeString(expected)
	return hmac.Equal(got, want)
}
