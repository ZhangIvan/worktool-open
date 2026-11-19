package protocol

import (
	"encoding/json"
	"math/big"
	"regexp"
	"time"
)

const MaxSafeInteger int64 = 9007199254740991

var numberPattern = regexp.MustCompile(`\A-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?\z`)
var idPattern = regexp.MustCompile(`\A[a-z][a-z0-9_-]{2,63}\z`)
var timePattern = regexp.MustCompile(`\A[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z\z`)

func ValidID(v any) bool { s, ok := v.(string); return ok && idPattern.MatchString(s) }

// SafeInteger accepts JSON Schema integer values, including 1.0 and 1e0,
// without routing through float64. The lexical bound prevents huge exponent
// allocation; exponent values outside this bound cannot be valid safe integers
// except zero, which is intentionally rejected by this ingress hardening.
func SafeInteger(v any, min int64) (int64, error) {
	n, ok := v.(json.Number)
	if !ok || len(n) > 128 || !numberPattern.MatchString(string(n)) {
		return 0, ErrInvalid
	}
	// Bound exponent before big.Rat parsing (otherwise 1e999999999 allocates).
	s := string(n)
	for i, c := range s {
		if c == 'e' || c == 'E' {
			exp, ok := new(big.Int).SetString(s[i+1:], 10)
			if !ok || !exp.IsInt64() || exp.Int64() > 128 || exp.Int64() < -128 {
				return 0, ErrInvalid
			}
			break
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, ErrInvalid
	}
	x := r.Num().Int64()
	if x < min || x > MaxSafeInteger {
		return 0, ErrInvalid
	}
	return x, nil
}
func ValidTimestamp(v any) bool {
	s, ok := v.(string)
	if !ok || !timePattern.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02T15:04:05Z", s)
	return err == nil
}

// ValidateEnvelope validates ONLY common wire fields. A returned object is NOT
// authorized nor schema-valid until the caller validates its kind-specific body.
func ValidateEnvelope(data []byte, maxBytes int) (map[string]any, error) {
	v, e := Decode(data, maxBytes)
	if e != nil {
		return nil, e
	}
	m, e := ExactObject(v, []string{"version", "kind", "eventId", "tenantId", "deviceId", "sessionId", "seq", "sentAt", "mode", "body"}, nil)
	if e != nil {
		return nil, e
	}
	if m["version"] != "1.0" {
		return nil, ErrInvalid
	}
	kind, ok := m["kind"].(string)
	if !ok {
		return nil, ErrInvalid
	}
	switch kind {
	case "command", "transport_ack", "accepted", "started", "execution_result", "result_ack", "stop_request", "heartbeat", "device_event", "config_notification":
	default:
		return nil, ErrInvalid
	}
	for _, key := range []string{"eventId", "tenantId", "deviceId", "sessionId"} {
		if !ValidID(m[key]) {
			return nil, ErrInvalid
		}
	}
	if _, e = SafeInteger(m["seq"], 1); e != nil {
		return nil, e
	}
	if !ValidTimestamp(m["sentAt"]) || m["mode"] != "synthetic" {
		return nil, ErrInvalid
	}
	if _, ok = m["body"].(map[string]any); !ok {
		return nil, ErrInvalid
	}
	return m, nil
}
