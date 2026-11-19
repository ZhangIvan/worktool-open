package gatewaywire

import "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"

// DecodeHello recognizes unsupported version offers but never negotiates a downgrade.
func DecodeHello(raw []byte) (Hello, error) {
	v, e := protocol.Decode(raw, MaxRawFrameBytes)
	if e != nil {
		return Hello{}, ErrInvalid
	}
	m, e := protocol.ExactObject(v, []string{"kind", "supportedVersions", "mode", "capabilities"}, []string{"drivers"})
	if e != nil || m["kind"] != "hello" || m["mode"] != "synthetic" {
		return Hello{}, ErrInvalid
	}
	versions, ok := m["supportedVersions"].([]any)
	if !ok || len(versions) < 1 || len(versions) > 8 {
		return Hello{}, ErrInvalid
	}
	seen := map[string]bool{}
	unsupported := false
	for _, v := range versions {
		s, e := str(v)
		if e != nil || !versionPattern.MatchString(s) || seen[s] {
			return Hello{}, ErrInvalid
		}
		seen[s] = true
		if s != "1.0" {
			unsupported = true
		}
	}
	// Only classify a recognizable offer. Its future body is not accepted or validated as 1.1.
	if unsupported {
		return Hello{}, ErrUnsupportedVersion
	}
	if len(versions) != 1 {
		return Hello{}, ErrInvalid
	}
	if _, ok := m["drivers"]; ok {
		return Hello{}, ErrInvalid
	}
	values, ok := m["capabilities"].([]any)
	if !ok || len(values) > 2 {
		return Hello{}, ErrInvalid
	}
	caps := make([]string, 0, len(values))
	seen = map[string]bool{}
	for _, v := range values {
		s, e := str(v)
		if e != nil || (s != "message.send_text" && s != "file.export") || seen[s] {
			return Hello{}, ErrInvalid
		}
		seen[s] = true
		caps = append(caps, s)
	}
	return Hello{Capabilities: caps}, nil
}

func EncodeHelloAck(a HelloAck) ([]byte, error) {
	if !protocol.ValidID(a.TenantID) || !protocol.ValidID(a.DeviceID) || !protocol.ValidID(a.SessionID) || !safe(a.PolicyVersion, 1) {
		return nil, ErrInvalid
	}
	t, e := formatTime(a.ServerTime)
	if e != nil {
		return nil, ErrInvalid
	}
	return canonical(object{"kind": "hello_ack", "version": "1.0", "tenantId": a.TenantID, "deviceId": a.DeviceID, "sessionId": a.SessionID, "policyVersion": a.PolicyVersion, "serverTime": t, "heartbeatIntervalSec": int64(15), "maxFrameBytes": int64(65536), "mode": "synthetic"})
}
