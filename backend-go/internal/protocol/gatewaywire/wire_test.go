package gatewaywire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
)

func fixedTime() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }
func header(kind string) Header {
	return Header{Version: "1.0", Kind: kind, EventID: "event_example", TenantID: "tenant_example", DeviceID: "device_example", SessionID: "session_example", Seq: 1, SentAt: fixedTime(), Mode: "synthetic"}
}
func rawJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func rawFrame(t *testing.T, kind string, body any) []byte {
	t.Helper()
	return rawJSON(t, map[string]any{"version": "1.0", "kind": kind, "eventId": "event_example", "tenantId": "tenant_example", "deviceId": "device_example", "sessionId": "session_example", "seq": 1, "sentAt": "2030-01-01T00:00:00Z", "mode": "synthetic", "body": body})
}
func baseResult() map[string]any {
	return map[string]any{"commandId": "command_example", "attemptId": "attempt_example", "status": "succeeded", "effectState": "applied", "code": "APPLIED", "evidenceRefs": []string{"journal_example"}}
}
func invalidFrame(t *testing.T, b []byte) {
	t.Helper()
	v, e := DecodeDeviceFrame(b)
	if !errors.Is(e, ErrInvalid) || !reflect.DeepEqual(v, DeviceFrame{}) {
		t.Fatalf("wanted fixed invalid and zero value, got %v / %#v", e, v)
	}
}
func baseCommand() TextCommandBody {
	return TextCommandBody{CommandID: "command_example", AttemptID: "attempt_example", ExpiresAt: fixedTime().Add(2 * time.Minute), PolicyVersion: 1, Payload: TextPayload{TargetID: "synthetic_target_001", Text: "Synthetic example only"}}
}
func serverCommand(t *testing.T) ServerFrame {
	t.Helper()
	b := baseCommand()
	d, e := CommandDigest("device_example", b)
	if e != nil {
		t.Fatal(e)
	}
	b.PayloadDigest = d
	return ServerFrame{Header: header("command"), Body: ServerBody{Command: &b}}
}
func invalidServer(t *testing.T, f ServerFrame) {
	t.Helper()
	b, e := EncodeServerFrame(f)
	if !errors.Is(e, ErrInvalid) || b != nil {
		t.Fatalf("wanted fixed invalid and nil, got %v / %s", e, b)
	}
}

func TestHelloExactAndNoDowngrade(t *testing.T) {
	good := `{"kind":"hello","supportedVersions":["1.0"],"mode":"synthetic","capabilities":["message.send_text","file.export"]}`
	h, e := DecodeHello([]byte(good))
	if e != nil || !reflect.DeepEqual(h.Capabilities, []string{"message.send_text", "file.export"}) {
		t.Fatalf("hello: %#v %v", h, e)
	}
	for _, caps := range []string{`[]`, `["message.send_text"]`, `["file.export"]`, `["file.export","message.send_text"]`} {
		if _, e := DecodeHello([]byte(strings.Replace(good, `["message.send_text","file.export"]`, caps, 1))); e != nil {
			t.Errorf("schema-permitted capabilities rejected: %v", e)
		}
	}
	cases := []struct {
		name, raw string
		want      error
	}{
		{"fixture11", `{"kind":"hello","supportedVersions":["1.1"],"mode":"synthetic","capabilities":["fixture.ui.submit_text"],"drivers":[]}`, ErrUnsupportedVersion},
		{"mixed", strings.Replace(good, `["1.0"]`, `["1.0","1.1"]`, 1), ErrUnsupportedVersion},
		{"newversion", strings.Replace(good, `["1.0"]`, `["2.0"]`, 1), ErrUnsupportedVersion},
		{"driver10", strings.TrimSuffix(good, "}") + `,"drivers":[]}`, ErrInvalid},
		{"duplicatecap", strings.Replace(good, `"file.export"`, `"message.send_text"`, 1), ErrInvalid},
		{"unknowncap", strings.Replace(good, `"file.export"`, `"fixture.ui.submit_text"`, 1), ErrInvalid},
		{"nullcaps", strings.Replace(good, `["message.send_text","file.export"]`, `null`, 1), ErrInvalid},
		{"badcaptype", strings.Replace(good, `"file.export"`, `7`, 1), ErrInvalid},
		{"emptyversion", strings.Replace(good, `["1.0"]`, `[]`, 1), ErrInvalid},
		{"duplicatedversion", strings.Replace(good, `["1.0"]`, `["1.0","1.0"]`, 1), ErrInvalid},
		{"versiontype", strings.Replace(good, `["1.0"]`, `[1]`, 1), ErrInvalid},
		{"malformedversion", strings.Replace(good, `["1.0"]`, `["1.1\n"]`, 1), ErrInvalid},
		{"missing", strings.Replace(good, `"mode":"synthetic",`, "", 1), ErrInvalid},
		{"extra", strings.TrimSuffix(good, "}") + `,"tenantId":"tenant_fake"}`, ErrInvalid},
		{"duplicate", strings.TrimSuffix(good, "}") + `,"kind":"hello"}`, ErrInvalid},
		{"escapedduplicate", strings.TrimSuffix(good, "}") + `,"\u006bind":"hello"}`, ErrInvalid},
		{"wrongkind", strings.Replace(good, "hello", "hello_ack", 1), ErrInvalid},
		{"wrongmode", strings.Replace(good, "synthetic", "live", 1), ErrInvalid},
		{"trailing", good + `{}`, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, e := DecodeHello([]byte(tc.raw))
			if !errors.Is(e, tc.want) || !reflect.DeepEqual(h, Hello{}) {
				t.Fatalf("wanted %v / zero, got %v %#v", tc.want, e, h)
			}
		})
	}
}

func TestHelloAckGolden(t *testing.T) {
	a := HelloAck{TenantID: "tenant_example", DeviceID: "device_example", SessionID: "session_example", PolicyVersion: 1, ServerTime: fixedTime()}
	got, e := EncodeHelloAck(a)
	want := `{"deviceId":"device_example","heartbeatIntervalSec":15,"kind":"hello_ack","maxFrameBytes":65536,"mode":"synthetic","policyVersion":1,"serverTime":"2030-01-01T00:00:00Z","sessionId":"session_example","tenantId":"tenant_example","version":"1.0"}`
	if e != nil || string(got) != want {
		t.Fatalf("hello_ack: %v %s", e, got)
	}
	for _, mut := range []func(*HelloAck){func(a *HelloAck) { a.TenantID += "\n" }, func(a *HelloAck) { a.DeviceID = "" }, func(a *HelloAck) { a.SessionID = "x" }, func(a *HelloAck) { a.PolicyVersion = 0 }, func(a *HelloAck) { a.PolicyVersion = protocol.MaxSafeInteger + 1 }, func(a *HelloAck) { a.ServerTime = a.ServerTime.Add(time.Nanosecond) }, func(a *HelloAck) { a.ServerTime = a.ServerTime.In(time.FixedZone("offset", 3600)) }} {
		bad := a
		mut(&bad)
		b, e := EncodeHelloAck(bad)
		if e != ErrInvalid || b != nil {
			t.Fatal("invalid ack accepted")
		}
	}
}

func TestDeviceKindsAndExactBodies(t *testing.T) {
	cases := []struct {
		kind string
		body map[string]any
		want DeviceBody
	}{
		{"accepted", map[string]any{"commandId": "command_example", "attemptId": "attempt_example"}, DeviceBody{Accepted: &CommandRef{CommandID: "command_example", AttemptID: "attempt_example"}}},
		{"started", map[string]any{"commandId": "command_example", "attemptId": "attempt_example"}, DeviceBody{Started: &CommandRef{CommandID: "command_example", AttemptID: "attempt_example"}}},
		{"execution_result", baseResult(), DeviceBody{Result: &ExecutionResult{CommandID: "command_example", AttemptID: "attempt_example", Status: "succeeded", EffectState: "applied", Code: "APPLIED", EvidenceRefs: []string{"journal_example"}}}},
		{"transport_ack", map[string]any{"ackEventId": "event_ack"}, DeviceBody{TransportAck: &TransportAck{AckEventID: "event_ack"}}},
		{"heartbeat", map[string]any{"policyVersion": 1, "lastReceivedSeq": 0}, DeviceBody{Heartbeat: &Heartbeat{PolicyVersion: 1, LastReceivedSeq: 0}}},
		{"device_event", map[string]any{"name": "synthetic.message_received", "data": map[string]any{"referenceId": "reference_example"}}, DeviceBody{DeviceEvent: &DeviceEvent{Name: "synthetic.message_received", ReferenceID: "reference_example"}}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			raw := rawFrame(t, tc.kind, tc.body)
			f, e := DecodeDeviceFrame(raw)
			if e != nil || !reflect.DeepEqual(f.Body, tc.want) || !reflect.DeepEqual(f.Header, header(tc.kind)) {
				t.Fatalf("body/header mismatch: %v %#v", e, f)
			}
			if len(f.Canonical) == 0 || len(f.FrameDigest) != 64 {
				t.Fatal("missing canonical")
			}
			if tc.kind != "execution_result" && (f.ResultCanonical != nil || f.ResultDigest != "") {
				t.Fatal("non-result has result digest")
			}
			for key := range tc.body {
				bad := map[string]any{}
				for k, v := range tc.body {
					if k != key {
						bad[k] = v
					}
				}
				invalidFrame(t, rawFrame(t, tc.kind, bad))
				bad[key] = nil
				invalidFrame(t, rawFrame(t, tc.kind, bad))
			}
			bad := map[string]any{}
			for k, v := range tc.body {
				bad[k] = v
			}
			bad["unexpected"] = true
			invalidFrame(t, rawFrame(t, tc.kind, bad))
			duplicate := strings.Replace(string(raw), `"body":{`, `"body":{"unexpected":0,"unexpected":1,`, 1)
			invalidFrame(t, []byte(duplicate))
		})
	}
	for _, kind := range []string{"command", "result_ack", "stop_request", "config_notification", "hello", "unknown"} {
		invalidFrame(t, rawFrame(t, kind, map[string]any{}))
	}
	for _, data := range []any{nil, []any{}, map[string]any{}, map[string]any{"referenceId": "reference_example", "extra": 0}, map[string]any{"referenceId": "bad\n"}} {
		invalidFrame(t, rawFrame(t, "device_event", map[string]any{"name": "permission.changed", "data": data}))
	}
	if _, e := DecodeDeviceFrame(rawFrame(t, "device_event", map[string]any{"name": "permission.changed", "data": map[string]any{"referenceId": "reference_example"}})); e != nil {
		t.Fatal(e)
	}
	invalidFrame(t, rawFrame(t, "device_event", map[string]any{"name": "actual.message_sent", "data": map[string]any{"referenceId": "reference_example"}}))
}

func TestResultStatusEffectCrossProduct(t *testing.T) {
	valid := map[string]map[string]bool{"succeeded": {"applied": true}, "partially_succeeded": {"partially_applied": true}, "execution_unknown": {"unknown": true}, "failed": {"not_applied": true, "not_started": true}, "cancelled": {"not_applied": true, "not_started": true}, "expired": {"not_started": true}, "rejected": {"not_started": true}}
	for _, status := range []string{"succeeded", "partially_succeeded", "execution_unknown", "failed", "cancelled", "expired", "rejected", "canceled", "unknown", ""} {
		for _, effect := range []string{"applied", "partially_applied", "unknown", "not_applied", "not_started", ""} {
			t.Run(status+"_"+effect, func(t *testing.T) {
				b := baseResult()
				b["status"] = status
				b["effectState"] = effect
				_, e := DecodeDeviceFrame(rawFrame(t, "execution_result", b))
				if (e == nil) != valid[status][effect] {
					t.Fatalf("pair mismatch %v", e)
				}
			})
		}
	}
}

func TestExactEnvelopeAndParser(t *testing.T) {
	b := rawFrame(t, "accepted", map[string]any{"commandId": "command_example", "attemptId": "attempt_example"})
	var obj map[string]any
	if e := json.Unmarshal(b, &obj); e != nil {
		t.Fatal(e)
	}
	for key := range obj {
		bad := map[string]any{}
		for k, v := range obj {
			if k != key {
				bad[k] = v
			}
		}
		invalidFrame(t, rawJSON(t, bad))
		bad[key] = nil
		invalidFrame(t, rawJSON(t, bad))
	}
	extra := map[string]any{}
	for k, v := range obj {
		extra[k] = v
	}
	extra["extra"] = 0
	invalidFrame(t, rawJSON(t, extra))
	for _, key := range []string{"eventId", "tenantId", "deviceId", "sessionId"} {
		bad := map[string]any{}
		for k, v := range obj {
			bad[k] = v
		}
		for _, id := range []string{"ab", "Aaa", "id\n", strings.Repeat("a", 65)} {
			bad[key] = id
			invalidFrame(t, rawJSON(t, bad))
		}
	}
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`1`), append(append([]byte{}, b...), b...), []byte(`{"secret":"DO_NOT_REFLECT"}`), bytes.Replace(b, []byte("event_example"), []byte{0xff}, 1), bytes.Replace(b, []byte("event_example"), []byte(`\ud800`), 1), bytes.Replace(b, []byte("event_example"), []byte(`\udc00`), 1), bytes.Replace(b, []byte(`"seq":1`), []byte(`"seq":1,"\u0073eq":1`), 1)} {
		invalidFrame(t, raw)
	}
	// The parent parser enforces recursion before any exact-shape projection.
	invalidFrame(t, []byte(strings.Repeat("[", 66)+"0"+strings.Repeat("]", 66)))
}

func TestSequenceNumbersAndTimes(t *testing.T) {
	raw := string(rawFrame(t, "heartbeat", map[string]any{"policyVersion": 1, "lastReceivedSeq": 0}))
	for _, n := range []string{"0", "-1", "9007199254740992", "1.1", "1e-1", "true", "null", `"1"`, "1e999999999"} {
		invalidFrame(t, []byte(strings.Replace(raw, `"seq":1`, `"seq":`+n, 1)))
	}
	var previous []byte
	for _, n := range []string{"1", "1.0", "1e0", "0.1e1"} {
		f, e := DecodeDeviceFrame([]byte(strings.Replace(raw, `"seq":1`, `"seq":`+n, 1)))
		if e != nil {
			t.Fatal(e)
		}
		if previous != nil && !bytes.Equal(previous, f.Canonical) {
			t.Fatal("integral forms canonical mismatch")
		}
		previous = f.Canonical
	}
	f, e := DecodeDeviceFrame([]byte(strings.Replace(raw, `"seq":1`, `"seq":9007199254740991`, 1)))
	if e != nil || f.Header.Seq != protocol.MaxSafeInteger {
		t.Fatal("maxsafe rejected")
	}
	for _, k := range []string{"policyVersion", "lastReceivedSeq"} {
		old := `"` + k + `":1`
		if k == "lastReceivedSeq" {
			old = `"` + k + `":0`
		}
		for _, n := range []string{"-1", "9007199254740992", "1.5", "null"} {
			invalidFrame(t, []byte(strings.Replace(raw, old, `"`+k+`":`+n, 1)))
		}
	}
	invalidFrame(t, []byte(strings.Replace(raw, `"policyVersion":1`, `"policyVersion":0`, 1)))
	for _, ts := range []string{"2030-02-29T00:00:00Z", "2030-01-01T00:00:60Z", "2030-01-01T24:00:00Z", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00+00:00", "2030-01-01T00:00:00Z\n", "10000-01-01T00:00:00Z"} {
		invalidFrame(t, []byte(strings.Replace(raw, `"2030-01-01T00:00:00Z"`, string(rawJSON(t, ts)), 1)))
	}
	for _, ts := range []string{"0000-01-01T00:00:00Z", "0001-01-01T00:00:00Z", "2000-02-29T00:00:00Z", "9999-12-31T23:59:59Z"} {
		if _, e := DecodeDeviceFrame([]byte(strings.Replace(raw, "2030-01-01T00:00:00Z", ts, 1))); e != nil {
			t.Errorf("calendar-valid timestamp rejected: %v", e)
		}
	}
}

func TestCanonicalGoldenUnicodeAndDigestSeparation(t *testing.T) {
	data, e := os.ReadFile("testdata/restricted-golden.json")
	if e != nil {
		t.Fatal(e)
	}
	var g struct {
		ResultInput                                                                                                                          json.RawMessage
		ResultCanonical, ResultDigest, FrameCanonical, FrameDigest, CommandText, CommandProjectionCanonical, CommandDigest, CommandCanonical string
	}
	if e = json.Unmarshal(data, &g); e != nil {
		t.Fatal(e)
	}
	f, e := DecodeDeviceFrame(g.ResultInput)
	if e != nil {
		t.Fatal(e)
	}
	if string(f.Canonical) != g.FrameCanonical || f.FrameDigest != g.FrameDigest || string(f.ResultCanonical) != g.ResultCanonical || f.ResultDigest != g.ResultDigest {
		t.Fatal("independent golden frame/result mismatch")
	}
	if bytes.IndexByte(f.ResultCanonical, 0) >= 0 || !bytes.Contains(f.ResultCanonical, []byte(`\u0000`)) {
		t.Fatal("NUL must remain textually escaped")
	}
	for _, s := range []string{"<>&", "\u2028", "\u2029", "😀"} {
		if !bytes.Contains(f.ResultCanonical, []byte(s)) {
			t.Fatal("gratuitous Unicode/HTML escaping")
		}
	}
	var obj map[string]any
	if e = json.Unmarshal(g.ResultInput, &obj); e != nil {
		t.Fatal(e)
	}
	for key, value := range map[string]any{"eventId": "event_changed", "sessionId": "session_changed", "seq": 2, "sentAt": "2030-01-01T00:00:01Z"} {
		old := obj[key]
		obj[key] = value
		f2, e := DecodeDeviceFrame(rawJSON(t, obj))
		if e != nil {
			t.Fatal(e)
		}
		obj[key] = old
		if f2.FrameDigest == f.FrameDigest || f2.ResultDigest != f.ResultDigest || !bytes.Equal(f2.ResultCanonical, f.ResultCanonical) {
			t.Fatal("frame/result identity conflated")
		}
	}
	c := baseCommand()
	c.Payload.Text = g.CommandText
	c.PayloadDigest = g.CommandDigest
	d, e := CommandDigest("device_example", c)
	if e != nil || d != g.CommandDigest {
		t.Fatalf("golden command digest: %v %s", e, d)
	}
	projection, e := commandProjection("device_example", c)
	if e != nil {
		t.Fatal(e)
	}
	p, e := canonical(projection)
	if e != nil || string(p) != g.CommandProjectionCanonical {
		t.Fatal("projection bytes mismatch")
	}
	encoded, e := EncodeServerFrame(ServerFrame{Header: header("command"), Body: ServerBody{Command: &c}})
	if e != nil || string(encoded) != g.CommandCanonical {
		t.Fatalf("golden command bytes: %v", e)
	}
	// Paired-surrogate JSON escapes decode to the same scalar/UTF-8 canonical bytes.
	escaped := bytes.ReplaceAll(g.ResultInput, []byte(`\ud83d\ude00`), []byte("😀"))
	f2, e := DecodeDeviceFrame(escaped)
	if e != nil || !bytes.Equal(f2.Canonical, f.Canonical) {
		t.Fatal("paired surrogate mismatch")
	}
}

func TestResultCodeAndEvidenceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		ok         bool
	}{{"empty", "", false}, {"128unicode", strings.Repeat("😀", 128), true}, {"129unicode", strings.Repeat("😀", 129), false}, {"128nul", strings.Repeat("\x00", 128), true}, {"129nul", strings.Repeat("\x00", 129), false}, {"unpairedhigh", `\ud800`, false}, {"unpairedlow", `\udc00`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := baseResult()
			r["code"] = tc.code
			b := rawFrame(t, "execution_result", r)
			if strings.HasPrefix(tc.name, "unpaired") {
				b = bytes.Replace(b, []byte(`\\ud`), []byte(`\ud`), 1)
			}
			_, e := DecodeDeviceFrame(b)
			if (e == nil) != tc.ok {
				t.Fatalf("boundary: %v", e)
			}
		})
	}
	for _, refs := range []any{nil, []string{}, []string{"journal_dup", "journal_dup"}, []string{"bad\n"}, []any{1}, make([]string, 17)} {
		r := baseResult()
		r["evidenceRefs"] = refs
		invalidFrame(t, rawFrame(t, "execution_result", r))
	}
	for _, code := range []string{strings.Repeat("😀", 128), strings.Repeat("\x00", 128)} {
		r := baseResult()
		r["code"] = code
		r["commandId"] = strings.Repeat("c", 64)
		r["attemptId"] = strings.Repeat("a", 64)
		refs := []string{}
		for i := 0; i < 16; i++ {
			refs = append(refs, fmt.Sprintf("j%02d", i)+strings.Repeat("x", 61))
		}
		r["evidenceRefs"] = refs
		f, e := DecodeDeviceFrame(rawFrame(t, "execution_result", r))
		if e != nil || len(f.ResultCanonical) > 4096 || len(f.Canonical) > 8192 {
			t.Fatalf("maximum schema-permitted result exceeded budget: %v", e)
		}
	}
}

func TestRawByteLimits(t *testing.T) {
	raw := rawFrame(t, "accepted", map[string]any{"commandId": strings.Repeat("c", 64), "attemptId": strings.Repeat("a", 64)})
	padded := append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), MaxRawFrameBytes-len(raw))...)
	if _, e := DecodeDeviceFrame(padded); e != nil {
		t.Fatal("raw exact limit rejected")
	}
	invalidFrame(t, append(padded, ' '))
	hello := []byte(`{"kind":"hello","supportedVersions":["1.0"],"mode":"synthetic","capabilities":[]}`)
	hello = append(hello, bytes.Repeat([]byte(" "), MaxRawFrameBytes-len(hello))...)
	if _, e := DecodeHello(hello); e != nil {
		t.Fatal(e)
	}
	if _, e := DecodeHello(append(hello, ' ')); e != ErrInvalid {
		t.Fatal("oversize hello accepted")
	}
}

func TestDuplicateBodyAndRedactedFailure(t *testing.T) {
	raw := string(rawFrame(t, "accepted", map[string]any{"commandId": "command_example", "attemptId": "attempt_example"}))
	for _, duplicate := range []string{`"commandId":"command_example","commandId":"command_other"`, `"commandId":"command_example","\u0063ommandId":"command_other"`} {
		invalidFrame(t, []byte(strings.Replace(raw, `"commandId":"command_example"`, duplicate, 1)))
	}
	// Even input chosen to resemble a credential never becomes part of errors.
	marker := "SYNTHETIC_DO_NOT_REFLECT"
	for _, raw := range []string{`{"kind":"` + marker + `"}`, strings.Replace(raw, "command_example", marker, 1)} {
		_, e := DecodeDeviceFrame([]byte(raw))
		if e != ErrInvalid || strings.Contains(e.Error(), marker) {
			t.Fatal("unredacted error")
		}
	}
	f := serverCommand(t)
	f.Body.Command.PayloadDigest = marker
	b, e := EncodeServerFrame(f)
	if e != ErrInvalid || b != nil || strings.Contains(e.Error(), marker) {
		t.Fatal("unredacted encode error")
	}
}
