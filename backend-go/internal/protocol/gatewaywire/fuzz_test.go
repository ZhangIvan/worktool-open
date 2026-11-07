package gatewaywire

import (
	"bytes"
	"testing"
)

func FuzzPureDecoders(f *testing.F) {
	for _, seed := range []string{
		`{"kind":"hello","supportedVersions":["1.0"],"mode":"synthetic","capabilities":["message.send_text","file.export"]}`,
		`{"kind":"hello","supportedVersions":["1.1"],"mode":"synthetic","capabilities":["fixture.ui.submit_text"],"drivers":[]}`,
		`{"version":"1.0","kind":"heartbeat","eventId":"event_example","tenantId":"tenant_example","deviceId":"device_example","sessionId":"session_example","seq":1e0,"sentAt":"2030-01-01T00:00:00Z","mode":"synthetic","body":{"policyVersion":1,"lastReceivedSeq":0}}`,
		`{"version":"1.0","kind":"execution_result","eventId":"event_example","tenantId":"tenant_example","deviceId":"device_example","sessionId":"session_example","seq":1,"sentAt":"2030-01-01T00:00:00Z","mode":"synthetic","body":{"commandId":"command_example","attemptId":"attempt_example","status":"succeeded","effectState":"applied","code":"\u0000\ud83d\ude00","evidenceRefs":["journal_example"]}}`,
		`{"kind":"hello","kind":"hello"}`, `{"a":"\ud800"}`, `[]`, "\xff",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, e := DecodeHello(raw)
		if e != nil && e != ErrInvalid && e != ErrUnsupportedVersion {
			t.Fatal("unredacted hello error")
		}
		decoded, e := DecodeDeviceFrame(raw)
		if e != nil {
			if e != ErrInvalid {
				t.Fatal("unredacted frame error")
			}
			return
		}
		second, e := DecodeDeviceFrame(decoded.Canonical)
		if e != nil || !bytes.Equal(decoded.Canonical, second.Canonical) || decoded.FrameDigest != second.FrameDigest || decoded.ResultDigest != second.ResultDigest {
			t.Fatal("canonical decode not idempotent")
		}
	})
}
