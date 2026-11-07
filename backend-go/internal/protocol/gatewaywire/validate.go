package gatewaywire

import (
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
)

const utcSecond = "2006-01-02T15:04:05Z"

var versionPattern = regexp.MustCompile(`\A[0-9]+\.[0-9]+\z`)
var targetPattern = regexp.MustCompile(`\Asynthetic_[a-z0-9_-]{1,48}\z`)
var digestPattern = regexp.MustCompile(`\A[a-f0-9]{64}\z`)

func exact(v any, keys ...string) (map[string]any, error) {
	m, e := protocol.ExactObject(v, keys, nil)
	if e != nil {
		return nil, ErrInvalid
	}
	return m, nil
}
func str(v any) (string, error) {
	s, ok := v.(string)
	if !ok || !utf8.ValidString(s) {
		return "", ErrInvalid
	}
	return s, nil
}
func id(v any) (string, error) {
	if !protocol.ValidID(v) {
		return "", ErrInvalid
	}
	return v.(string), nil
}
func number(v any, min int64) (int64, error) {
	n, e := protocol.SafeInteger(v, min)
	if e != nil {
		return 0, ErrInvalid
	}
	return n, nil
}
func safe(n, min int64) bool { return n >= min && n <= protocol.MaxSafeInteger }
func timestamp(v any) (time.Time, error) {
	if !protocol.ValidTimestamp(v) {
		return time.Time{}, ErrInvalid
	}
	t, e := time.Parse(utcSecond, v.(string))
	if e != nil {
		return time.Time{}, ErrInvalid
	}
	return t, nil
}
func formatTime(t time.Time) (string, error) {
	_, offset := t.Zone()
	if offset != 0 || t.Nanosecond() != 0 || t.Year() < 0 || t.Year() > 9999 {
		return "", ErrInvalid
	}
	s := t.UTC().Format(utcSecond)
	if !protocol.ValidTimestamp(s) {
		return "", ErrInvalid
	}
	return s, nil
}
func validText(p TextPayload) bool {
	if !targetPattern.MatchString(p.TargetID) || !utf8.ValidString(p.Text) {
		return false
	}
	units := 0
	for _, r := range p.Text {
		units++
		if r > 0xffff {
			units++
		}
		if units > 4096 {
			return false
		}
	}
	return units >= 1
}
func validResult(r ExecutionResult) bool {
	if !protocol.ValidID(r.CommandID) || !protocol.ValidID(r.AttemptID) || !utf8.ValidString(r.Code) {
		return false
	}
	if n := utf8.RuneCountInString(r.Code); n < 1 || n > 128 {
		return false
	}
	paired := false
	switch r.Status {
	case "succeeded":
		paired = r.EffectState == "applied"
	case "partially_succeeded":
		paired = r.EffectState == "partially_applied"
	case "execution_unknown":
		paired = r.EffectState == "unknown"
	case "failed", "cancelled":
		paired = r.EffectState == "not_started" || r.EffectState == "not_applied"
	case "expired", "rejected":
		paired = r.EffectState == "not_started"
	}
	if !paired || len(r.EvidenceRefs) < 1 || len(r.EvidenceRefs) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, ref := range r.EvidenceRefs {
		if !protocol.ValidID(ref) || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	return true
}
func refObject(r CommandRef) object {
	return object{"commandId": r.CommandID, "attemptId": r.AttemptID}
}
func resultObject(r ExecutionResult) object {
	return object{"commandId": r.CommandID, "attemptId": r.AttemptID, "status": r.Status, "effectState": r.EffectState, "code": r.Code, "evidenceRefs": r.EvidenceRefs}
}
func headerObject(h Header, body object) (object, error) {
	if h.Version != "1.0" || h.Mode != "synthetic" || !protocol.ValidID(h.EventID) || !protocol.ValidID(h.TenantID) || !protocol.ValidID(h.DeviceID) || !protocol.ValidID(h.SessionID) || !safe(h.Seq, 1) {
		return nil, ErrInvalid
	}
	t, e := formatTime(h.SentAt)
	if e != nil {
		return nil, ErrInvalid
	}
	return object{"version": h.Version, "kind": h.Kind, "eventId": h.EventID, "tenantId": h.TenantID, "deviceId": h.DeviceID, "sessionId": h.SessionID, "seq": h.Seq, "sentAt": t, "mode": h.Mode, "body": body}, nil
}
