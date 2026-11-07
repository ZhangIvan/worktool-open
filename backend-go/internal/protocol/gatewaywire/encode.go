package gatewaywire

import "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"

// CommandDigest hashes the six-field text-command projection, excluding
// commandId, attemptId and payloadDigest. It is valid to compute it before those
// fields are assigned. It confers no permission to dispatch or execute.
func CommandDigest(deviceID string, b TextCommandBody) (string, error) {
	p, e := commandProjection(deviceID, b)
	if e != nil {
		return "", ErrInvalid
	}
	encoded, e := canonical(p)
	if e != nil {
		return "", ErrInvalid
	}
	return digest(encoded), nil
}
func commandProjection(deviceID string, b TextCommandBody) (object, error) {
	if !protocol.ValidID(deviceID) || !validText(b.Payload) || !safe(b.PolicyVersion, 1) || b.RequiresLocalConfirmation {
		return nil, ErrInvalid
	}
	t, e := formatTime(b.ExpiresAt)
	if e != nil {
		return nil, ErrInvalid
	}
	return object{"capability": "message.send_text", "payload": object{"targetId": b.Payload.TargetID, "text": b.Payload.Text}, "deviceId": deviceID, "expiresAt": t, "policyVersion": b.PolicyVersion, "requiresLocalConfirmation": false}, nil
}

// EncodeServerFrame serializes exactly one caller-selected server body. The
// caller supplies eventId, sequence and time; this function never allocates or
// advances them. Successful encoding is not a send or an authorization check.
func EncodeServerFrame(f ServerFrame) ([]byte, error) {
	b, e := encodeBody(f.Header.Kind, f.Header.DeviceID, f.Body)
	if e != nil {
		return nil, ErrInvalid
	}
	env, e := headerObject(f.Header, b)
	if e != nil {
		return nil, ErrInvalid
	}
	encoded, e := canonical(env)
	// The smaller 8192-byte canonical-envelope limit is for device ingress, not
	// valid outbound commands (4096 UTF-16 units may require more JSON bytes).
	if e != nil || len(encoded) > MaxRawFrameBytes {
		return nil, ErrInvalid
	}
	return encoded, nil
}
func encodeBody(kind, deviceID string, b ServerBody) (object, error) {
	count := 0
	if b.Command != nil {
		count++
	}
	if b.TransportAck != nil {
		count++
	}
	if b.ResultAck != nil {
		count++
	}
	if b.Stop != nil {
		count++
	}
	if b.Config != nil {
		count++
	}
	if count != 1 {
		return nil, ErrInvalid
	}
	switch kind {
	case "command":
		if b.Command == nil {
			return nil, ErrInvalid
		}
		c := *b.Command
		if !protocol.ValidID(c.CommandID) || !protocol.ValidID(c.AttemptID) || !digestPattern.MatchString(c.PayloadDigest) {
			return nil, ErrInvalid
		}
		d, e := CommandDigest(deviceID, c)
		if e != nil || d != c.PayloadDigest {
			return nil, ErrInvalid
		}
		p, e := commandProjection(deviceID, c)
		if e != nil {
			return nil, ErrInvalid
		}
		delete(p, "deviceId")
		p["commandId"] = c.CommandID
		p["attemptId"] = c.AttemptID
		p["payloadDigest"] = c.PayloadDigest
		return p, nil
	case "transport_ack":
		if b.TransportAck == nil || !protocol.ValidID(b.TransportAck.AckEventID) {
			return nil, ErrInvalid
		}
		return object{"ackEventId": b.TransportAck.AckEventID}, nil
	case "result_ack":
		if b.ResultAck == nil || !protocol.ValidID(b.ResultAck.AckEventID) || !protocol.ValidID(b.ResultAck.CommandID) {
			return nil, ErrInvalid
		}
		return object{"ackEventId": b.ResultAck.AckEventID, "commandId": b.ResultAck.CommandID}, nil
	case "stop_request":
		if b.Stop == nil {
			return nil, ErrInvalid
		}
		s := b.Stop
		if !protocol.ValidID(s.CommandID) || !protocol.ValidID(s.AttemptID) || !safe(s.PolicyVersion, 1) || (s.Reason != "operator_cancel" && s.Reason != "policy_revoked") {
			return nil, ErrInvalid
		}
		return object{"commandId": s.CommandID, "attemptId": s.AttemptID, "reason": s.Reason, "policyVersion": s.PolicyVersion}, nil
	case "config_notification":
		if b.Config == nil || !safe(b.Config.PolicyVersion, 1) {
			return nil, ErrInvalid
		}
		return object{"policyVersion": b.Config.PolicyVersion, "revoked": b.Config.Revoked}, nil
	default:
		return nil, ErrInvalid
	}
}
