package gatewaywire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
)

func TestServerKindsExactAndDirection(t *testing.T) {
	command := serverCommand(t)
	cases := []struct {
		kind string
		body ServerBody
		keys []string
	}{
		{"command", command.Body, []string{"commandId", "attemptId", "capability", "payloadDigest", "expiresAt", "policyVersion", "requiresLocalConfirmation", "payload"}},
		{"transport_ack", ServerBody{TransportAck: &TransportAck{AckEventID: "event_ack"}}, []string{"ackEventId"}},
		{"result_ack", ServerBody{ResultAck: &ResultAck{AckEventID: "event_ack", CommandID: "command_example"}}, []string{"ackEventId", "commandId"}},
		{"stop_request", ServerBody{Stop: &StopBody{CommandID: "command_example", AttemptID: "attempt_example", Reason: "operator_cancel", PolicyVersion: 1}}, []string{"commandId", "attemptId", "reason", "policyVersion"}},
		{"config_notification", ServerBody{Config: &ConfigBody{PolicyVersion: 2, Revoked: true}}, []string{"policyVersion", "revoked"}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			f := ServerFrame{Header: header(tc.kind), Body: tc.body}
			before := rawJSON(t, f)
			encoded, e := EncodeServerFrame(f)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(before, rawJSON(t, f)) {
				t.Fatal("encoder mutated caller values")
			}
			env, e := protocol.ValidateEnvelope(encoded, MaxRawFrameBytes)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = protocol.ExactObject(env["body"], tc.keys, nil); e != nil {
				t.Fatal("incorrect body keys")
			}
			if env["seq"] != json.Number("1") || env["eventId"] != "event_example" || env["sentAt"] != "2030-01-01T00:00:00Z" {
				t.Fatal("encoder allocated common fields")
			}
			if tc.kind == "command" {
				b := env["body"].(map[string]any)
				if b["capability"] != "message.send_text" || b["requiresLocalConfirmation"] != false {
					t.Fatal("P0 text projection wrong")
				}
				if _, e = protocol.ExactObject(b["payload"], []string{"targetId", "text"}, nil); e != nil {
					t.Fatal(e)
				}
			}
			f.Body = ServerBody{}
			invalidServer(t, f)
			f.Body = tc.body
			f.Body.Config = &ConfigBody{PolicyVersion: 1}
			if tc.kind == "config_notification" {
				f.Body.TransportAck = &TransportAck{AckEventID: "event_ack"}
			}
			invalidServer(t, f)
			f.Body = tc.body
			f.Header.Kind = "heartbeat"
			invalidServer(t, f)
			// The only legal bidirectional frame is transport_ack.
			if tc.kind != "transport_ack" {
				invalidFrame(t, encoded)
			}
		})
	}
	f := ServerFrame{Header: header("stop_request"), Body: ServerBody{Stop: &StopBody{CommandID: "command_example", AttemptID: "attempt_example", Reason: "policy_revoked", PolicyVersion: 1}}}
	if _, e := EncodeServerFrame(f); e != nil {
		t.Fatal(e)
	}
	// A valid union member for another direction/kind cannot be smuggled through.
	for _, kind := range []string{"accepted", "started", "execution_result", "heartbeat", "device_event", "result_ack", "stop_request", "config_notification", "unknown"} {
		f := serverCommand(t)
		f.Header.Kind = kind
		invalidServer(t, f)
	}
}

func TestServerHeaderBoundaries(t *testing.T) {
	changes := map[string]func(*Header){
		"version": func(h *Header) { h.Version = "1.1" }, "mode": func(h *Header) { h.Mode = "live" },
		"event": func(h *Header) { h.EventID = "event_bad\n" }, "tenant": func(h *Header) { h.TenantID = "" }, "device": func(h *Header) { h.DeviceID = "bad device" }, "session": func(h *Header) { h.SessionID = "ab" },
		"seqzero": func(h *Header) { h.Seq = 0 }, "seqnegative": func(h *Header) { h.Seq = -1 }, "seqoverflow": func(h *Header) { h.Seq = protocol.MaxSafeInteger + 1 },
		"nanos": func(h *Header) { h.SentAt = h.SentAt.Add(time.Nanosecond) }, "offset": func(h *Header) { h.SentAt = h.SentAt.In(time.FixedZone("nonUTC", 3600)) }, "yearnegative": func(h *Header) { h.SentAt = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC) }, "yearoverflow": func(h *Header) { h.SentAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	for name, mut := range changes {
		t.Run(name, func(t *testing.T) { f := serverCommand(t); mut(&f.Header); invalidServer(t, f) })
	}
	for _, year := range []int{0, 1, 2000, 9999} {
		f := serverCommand(t)
		f.Header.Seq = protocol.MaxSafeInteger
		f.Header.SentAt = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, e := EncodeServerFrame(f); e != nil {
			t.Fatalf("valid boundary rejected: %v", e)
		}
	}
}

func TestServerBodyNegatives(t *testing.T) {
	c := serverCommand(t)
	for name, mut := range map[string]func(*TextCommandBody){
		"commandid": func(b *TextCommandBody) { b.CommandID = "" }, "attemptid": func(b *TextCommandBody) { b.AttemptID = "bad\n" },
		"missingdigest": func(b *TextCommandBody) { b.PayloadDigest = "" }, "uppercase": func(b *TextCommandBody) { b.PayloadDigest = strings.ToUpper(b.PayloadDigest) }, "digestmismatch": func(b *TextCommandBody) { b.PayloadDigest = strings.Repeat("0", 64) },
		"policyzero": func(b *TextCommandBody) { b.PolicyVersion = 0 }, "policyoverflow": func(b *TextCommandBody) { b.PolicyVersion = protocol.MaxSafeInteger + 1 }, "confirmation": func(b *TextCommandBody) { b.RequiresLocalConfirmation = true },
		"target": func(b *TextCommandBody) { b.Payload.TargetID = "arbitrary_target" }, "targetnewline": func(b *TextCommandBody) { b.Payload.TargetID += "\n" }, "emptytext": func(b *TextCommandBody) { b.Payload.Text = "" }, "invalidutf8": func(b *TextCommandBody) { b.Payload.Text = string([]byte{0xff}) },
		"expiryfraction": func(b *TextCommandBody) { b.ExpiresAt = b.ExpiresAt.Add(time.Nanosecond) }, "expiryoffset": func(b *TextCommandBody) { b.ExpiresAt = b.ExpiresAt.In(time.FixedZone("offset", 3600)) },
	} {
		t.Run(name, func(t *testing.T) { f := c; b := *c.Body.Command; f.Body.Command = &b; mut(&b); invalidServer(t, f) })
	}
	cases := []ServerFrame{
		{Header: header("transport_ack"), Body: ServerBody{TransportAck: &TransportAck{AckEventID: ""}}},
		{Header: header("result_ack"), Body: ServerBody{ResultAck: &ResultAck{AckEventID: "event_ack", CommandID: ""}}},
		{Header: header("result_ack"), Body: ServerBody{ResultAck: &ResultAck{AckEventID: "event_ack\n", CommandID: "command_example"}}},
		{Header: header("stop_request"), Body: ServerBody{Stop: &StopBody{CommandID: "command_example", AttemptID: "attempt_example", PolicyVersion: 1, Reason: "cancelled"}}},
		{Header: header("stop_request"), Body: ServerBody{Stop: &StopBody{CommandID: "", AttemptID: "attempt_example", PolicyVersion: 1, Reason: "operator_cancel"}}},
		{Header: header("stop_request"), Body: ServerBody{Stop: &StopBody{CommandID: "command_example", AttemptID: "", PolicyVersion: 1, Reason: "operator_cancel"}}},
		{Header: header("stop_request"), Body: ServerBody{Stop: &StopBody{CommandID: "command_example", AttemptID: "attempt_example", PolicyVersion: 0, Reason: "operator_cancel"}}},
		{Header: header("config_notification"), Body: ServerBody{Config: &ConfigBody{PolicyVersion: 0}}},
		{Header: header("config_notification"), Body: ServerBody{Config: &ConfigBody{PolicyVersion: protocol.MaxSafeInteger + 1}}},
		{Header: header("command"), Body: ServerBody{TransportAck: &TransportAck{AckEventID: "event_ack"}}},
		{Header: header("transport_ack"), Body: ServerBody{ResultAck: &ResultAck{AckEventID: "event_ack", CommandID: "command_example"}}},
		{Header: header("result_ack"), Body: ServerBody{TransportAck: &TransportAck{AckEventID: "event_ack"}}},
		{Header: header("stop_request"), Body: ServerBody{TransportAck: &TransportAck{AckEventID: "event_ack"}}},
		{Header: header("config_notification"), Body: ServerBody{TransportAck: &TransportAck{AckEventID: "event_ack"}}},
	}
	for i, f := range cases {
		t.Run(string(rune('a'+i)), func(t *testing.T) { invalidServer(t, f) })
	}
}

func TestCommandDigestScopeAndASCIIReference(t *testing.T) {
	c := baseCommand()
	d, e := CommandDigest("device_example", c)
	if e != nil || d != "3ffcf135341a879e98fca8cef6f8cb12a28104923c146b6ad0512374f57f2d9e" {
		t.Fatalf("client-contract ASCII vector mismatch: %v %s", e, d)
	}
	c.CommandID = "not evaluated"
	c.AttemptID = ""
	c.PayloadDigest = "not evaluated"
	if got, e := CommandDigest("device_example", c); e != nil || got != d {
		t.Fatal("digest illegally includes receipt identity")
	}
	for _, mut := range []func(*TextCommandBody){func(b *TextCommandBody) { b.Payload.Text += "!" }, func(b *TextCommandBody) { b.Payload.TargetID = "synthetic_other" }, func(b *TextCommandBody) { b.PolicyVersion++ }, func(b *TextCommandBody) { b.ExpiresAt = b.ExpiresAt.Add(time.Second) }} {
		b := c
		mut(&b)
		got, e := CommandDigest("device_example", b)
		if e != nil || got == d {
			t.Fatal("digest omits bound field")
		}
	}
	if got, e := CommandDigest("device_other", c); e != nil || got == d {
		t.Fatal("digest omits device")
	}
	if got, e := CommandDigest("bad\n", c); e != ErrInvalid || got != "" {
		t.Fatal("invalid device digest")
	}
	c.RequiresLocalConfirmation = true
	if _, e := CommandDigest("device_example", c); e != ErrInvalid {
		t.Fatal("P0 confirmation=true accepted")
	}
}

func TestTextUTF16LimitsAndOutboundSize(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		ok         bool
	}{{"ascii4096", strings.Repeat("a", 4096), true}, {"ascii4097", strings.Repeat("a", 4097), false}, {"nonbmp2048", strings.Repeat("😀", 2048), true}, {"nonbmp2048plus", strings.Repeat("😀", 2048) + "a", false}, {"mixed4096", strings.Repeat("😀", 2047) + "ab", true}, {"nul4096", strings.Repeat("\x00", 4096), true}, {"empty", "", false}, {"invalidutf8", string([]byte{0xed, 0xa0, 0x80}), false}} {
		t.Run(tc.name, func(t *testing.T) {
			c := baseCommand()
			c.Payload.Text = tc.text
			d, e := CommandDigest("device_example", c)
			if (e == nil) != tc.ok {
				t.Fatalf("UTF16 boundary %v", e)
			}
			if !tc.ok {
				return
			}
			c.PayloadDigest = d
			b, e := EncodeServerFrame(ServerFrame{Header: header("command"), Body: ServerBody{Command: &c}})
			if e != nil {
				t.Fatal(e)
			}
			if tc.name == "nul4096" && (len(b) <= 8192 || len(b) > 65536) {
				t.Fatal("outbound must allow valid escaped text above ingress limit")
			}
		})
	}
	for _, target := range []string{"synthetic_", "synthetic_" + strings.Repeat("a", 49), "synthetic_A", "synthetic_a\n"} {
		b := baseCommand()
		b.Payload.TargetID = target
		if _, e := CommandDigest("device_example", b); e != ErrInvalid {
			t.Fatal("target boundary accepted")
		}
	}
	b := baseCommand()
	b.Payload.TargetID = "synthetic_" + strings.Repeat("a", 48)
	if _, e := CommandDigest("device_example", b); e != nil {
		t.Fatal(e)
	}
	// No implicit Unicode normalization changes signed text semantics.
	b.Payload.Text = "é"
	d1, e := CommandDigest("device_example", b)
	if e != nil {
		t.Fatal(e)
	}
	b.Payload.Text = "e\u0301"
	d2, e := CommandDigest("device_example", b)
	if e != nil || d1 == d2 {
		t.Fatal("text unexpectedly normalized")
	}
}

func TestRestrictedCanonicalizerRejectsUnprojectedTypes(t *testing.T) {
	for _, v := range []any{nil, float64(1), int(1), json.Number("1"), map[string]any{"x": int64(1)}, object{"é": int64(1)}, object{"x": string([]byte{0xff})}, int64(-1), protocol.MaxSafeInteger + 1, []string{string([]byte{0xff})}} {
		b, e := canonical(v)
		if e != ErrInvalid || b != nil {
			t.Fatalf("unprojected value accepted: %T", v)
		}
	}
	got, e := canonical(object{"b": false, "a": []string{"x", "y"}})
	if e != nil || string(got) != `{"a":["x","y"],"b":false}` {
		t.Fatal("restricted object order")
	}
}

func TestParallelPureCalls(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			t.Parallel()
			f := serverCommand(t)
			before := *f.Body.Command
			for j := 0; j < 20; j++ {
				if _, e := EncodeServerFrame(f); e != nil {
					t.Fatal(e)
				}
				if _, e := DecodeDeviceFrame(rawFrame(t, "execution_result", baseResult())); e != nil {
					t.Fatal(e)
				}
			}
			if !reflect.DeepEqual(before, *f.Body.Command) {
				t.Fatal("input mutation")
			}
		})
	}
}
