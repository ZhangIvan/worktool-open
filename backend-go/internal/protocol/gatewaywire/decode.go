package gatewaywire

import "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"

// DecodeDeviceFrame validates only device-to-server shapes. Its result is not
// authenticated, fresh, sequenced, authorized or durable; the gateway must apply
// those checks before accepting evidence or producing any acknowledgement.
func DecodeDeviceFrame(raw []byte) (DeviceFrame, error) {
	m, err := protocol.ValidateEnvelope(raw, MaxRawFrameBytes)
	if err != nil {
		return DeviceFrame{}, ErrInvalid
	}
	seq, err := number(m["seq"], 1)
	if err != nil {
		return DeviceFrame{}, ErrInvalid
	}
	sentAt, err := timestamp(m["sentAt"])
	if err != nil {
		return DeviceFrame{}, ErrInvalid
	}
	h := Header{Version: m["version"].(string), Kind: m["kind"].(string), EventID: m["eventId"].(string), TenantID: m["tenantId"].(string), DeviceID: m["deviceId"].(string), SessionID: m["sessionId"].(string), Mode: m["mode"].(string), Seq: seq, SentAt: sentAt}
	b, projection, err := decodeBody(h.Kind, m["body"])
	if err != nil {
		return DeviceFrame{}, ErrInvalid
	}
	bodyBytes, err := canonical(projection)
	if err != nil {
		return DeviceFrame{}, ErrInvalid
	}
	if (h.Kind == "execution_result" && len(bodyBytes) > MaxResultBytes) || ((h.Kind == "accepted" || h.Kind == "started") && len(bodyBytes) > MaxCommandRefBytes) {
		return DeviceFrame{}, ErrInvalid
	}
	envelope, err := headerObject(h, projection)
	if err != nil {
		return DeviceFrame{}, ErrInvalid
	}
	encoded, err := canonical(envelope)
	if err != nil || len(encoded) > MaxCanonicalFrameBytes {
		return DeviceFrame{}, ErrInvalid
	}
	f := DeviceFrame{Header: h, Body: b, Canonical: encoded, FrameDigest: digest(encoded)}
	if h.Kind == "execution_result" {
		f.ResultCanonical = bodyBytes
		f.ResultDigest = digest(bodyBytes)
	}
	return f, nil
}

func decodeBody(kind string, v any) (DeviceBody, object, error) {
	switch kind {
	case "accepted", "started":
		m, e := exact(v, "commandId", "attemptId")
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		c, e := id(m["commandId"])
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		a, e := id(m["attemptId"])
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		r := CommandRef{CommandID: c, AttemptID: a}
		b := DeviceBody{}
		if kind == "accepted" {
			b.Accepted = &r
		} else {
			b.Started = &r
		}
		return b, refObject(r), nil
	case "execution_result":
		m, e := exact(v, "commandId", "attemptId", "status", "effectState", "code", "evidenceRefs")
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		r := ExecutionResult{}
		fields := []struct {
			k   string
			dst *string
		}{{"commandId", &r.CommandID}, {"attemptId", &r.AttemptID}, {"status", &r.Status}, {"effectState", &r.EffectState}, {"code", &r.Code}}
		for _, f := range fields {
			s, e := str(m[f.k])
			if e != nil {
				return DeviceBody{}, nil, ErrInvalid
			}
			*f.dst = s
		}
		refs, ok := m["evidenceRefs"].([]any)
		if !ok || len(refs) < 1 || len(refs) > 16 {
			return DeviceBody{}, nil, ErrInvalid
		}
		r.EvidenceRefs = make([]string, len(refs))
		for i, v := range refs {
			s, e := id(v)
			if e != nil {
				return DeviceBody{}, nil, ErrInvalid
			}
			r.EvidenceRefs[i] = s
		}
		if !validResult(r) {
			return DeviceBody{}, nil, ErrInvalid
		}
		return DeviceBody{Result: &r}, resultObject(r), nil
	case "transport_ack":
		m, e := exact(v, "ackEventId")
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		a, e := id(m["ackEventId"])
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		return DeviceBody{TransportAck: &TransportAck{AckEventID: a}}, object{"ackEventId": a}, nil
	case "heartbeat":
		m, e := exact(v, "policyVersion", "lastReceivedSeq")
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		p, e := number(m["policyVersion"], 1)
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		s, e := number(m["lastReceivedSeq"], 0)
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		return DeviceBody{Heartbeat: &Heartbeat{PolicyVersion: p, LastReceivedSeq: s}}, object{"policyVersion": p, "lastReceivedSeq": s}, nil
	case "device_event":
		m, e := exact(v, "name", "data")
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		n, e := str(m["name"])
		if e != nil || (n != "synthetic.message_received" && n != "permission.changed") {
			return DeviceBody{}, nil, ErrInvalid
		}
		data, e := exact(m["data"], "referenceId")
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		r, e := id(data["referenceId"])
		if e != nil {
			return DeviceBody{}, nil, ErrInvalid
		}
		return DeviceBody{DeviceEvent: &DeviceEvent{Name: n, ReferenceID: r}}, object{"name": n, "data": object{"referenceId": r}}, nil
	default:
		return DeviceBody{}, nil, ErrInvalid
	}
}
