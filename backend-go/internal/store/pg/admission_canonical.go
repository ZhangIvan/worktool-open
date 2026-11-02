package pg

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxSafeInteger int64 = 9007199254740991
const maxWireMillis int64 = 253402300799999 // last millisecond in four-digit UTC year9999

// canonicalRestricted is deliberately NOT a generic JCS implementation: only
// internally built fixed-key objects, arrays, valid strings, bools, and safe ints.
func canonicalRestricted(v any) (string, error) {
	switch x := v.(type) {
	case string:
		if !utf8.ValidString(x) {
			return "", admissionError("INVALID_INPUT")
		}
		var b strings.Builder
		b.WriteByte('"')
		for _, r := range x {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\t':
				b.WriteString(`\t`)
			case '\n':
				b.WriteString(`\n`)
			case '\f':
				b.WriteString(`\f`)
			case '\r':
				b.WriteString(`\r`)
			default:
				if r < 32 {
					b.WriteString(`\u00`)
					b.WriteByte("0123456789abcdef"[r>>4])
					b.WriteByte("0123456789abcdef"[r&15])
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
		return b.String(), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int64:
		if x < 0 || x > maxSafeInteger {
			return "", admissionError("INVALID_INPUT")
		}
		return strconv.FormatInt(x, 10), nil
	case []any:
		parts := make([]string, len(x))
		for i, v := range x {
			s, e := canonicalRestricted(v)
			if e != nil {
				return "", e
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			for _, c := range k {
				if c > 127 {
					return "", admissionError("INVALID_INPUT")
				}
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			q, e := canonicalRestricted(k)
			if e != nil {
				return "", e
			}
			value, e := canonicalRestricted(x[k])
			if e != nil {
				return "", e
			}
			parts = append(parts, q+":"+value)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	default:
		return "", admissionError("INVALID_INPUT")
	}
}
func hashText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func asciiIdentifier(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func requestCanonical(r AppTextRequest) (string, error) {
	if r.Type != 203 || r.Mode != "synthetic" || !asciiIdentifier(r.DeviceID) || r.ExpiresInSeconds < 30 || r.ExpiresInSeconds > 300 || len(r.IdempotencyKey) < 8 || len(r.IdempotencyKey) > 256 {
		return "", admissionError("INVALID_INPUT")
	}
	for _, c := range []byte(r.IdempotencyKey) {
		if c < 0x21 || c > 0x7e {
			return "", admissionError("INVALID_INPUT")
		}
	}
	if !strings.HasPrefix(r.TargetID, "synthetic_") {
		return "", admissionError("INVALID_INPUT")
	}
	suffix := strings.TrimPrefix(r.TargetID, "synthetic_")
	if len(suffix) < 1 || len(suffix) > 48 {
		return "", admissionError("INVALID_INPUT")
	}
	for _, c := range []byte(suffix) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "", admissionError("INVALID_INPUT")
		}
	}
	if !utf8.ValidString(r.Text) {
		return "", admissionError("INVALID_INPUT")
	}
	units := 0
	for _, c := range r.Text {
		units++
		if c > 0xffff {
			units++
		}
	}
	if units < 1 || units > 4096 {
		return "", admissionError("INVALID_INPUT")
	}
	return canonicalRestricted(map[string]any{"deviceId": r.DeviceID, "expiresInSeconds": r.ExpiresInSeconds, "targetId": r.TargetID, "text": r.Text, "type": int64(203)})
}

func commandCanonical(r AppTextRequest, policy int64, expires string) (string, error) {
	return canonicalRestricted(map[string]any{"capability": "message.send_text", "payload": map[string]any{"targetId": r.TargetID, "text": r.Text}, "deviceId": r.DeviceID, "policyVersion": policy, "requiresLocalConfirmation": false, "expiresAt": expires})
}
func snapshotCanonical(tenant, app string, subs []subscription) (string, error) {
	return canonicalRestricted(map[string]any{"tenant": tenant, "appId": app, "admissionVersion": "bounded-result-v1", "subscriptions": snapshotValues(subs)})
}
