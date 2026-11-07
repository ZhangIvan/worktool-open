package gatewaywire

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// One repository fixture is consumed by Go's production wire package and the
// Kotlin engine JVM tests. This test does not run Kotlin, sockets, or storage.
func TestSharedAndroid10Exchange(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/interop/android-go-1.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Clock    time.Time
		Hello    json.RawMessage
		HelloAck json.RawMessage
		Frames   []struct {
			Direction string
			Frame     json.RawMessage
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeHello(fixture.Hello); err != nil {
		t.Fatal(err)
	}
	var a HelloAck
	if err = json.Unmarshal(fixture.HelloAck, &a); err != nil {
		t.Fatal(err)
	}
	actual, err := EncodeHelloAck(a)
	if err != nil {
		t.Fatal(err)
	}
	sameInteropJSON(t, actual, fixture.HelloAck)
	deviceKinds := map[string]bool{}
	for _, entry := range fixture.Frames {
		var h Header
		if err = json.Unmarshal(entry.Frame, &h); err != nil {
			t.Fatal(err)
		}
		var envelope struct{ Body json.RawMessage }
		if err = json.Unmarshal(entry.Frame, &envelope); err != nil {
			t.Fatal(err)
		}
		if entry.Direction == "device_to_server" {
			f, e := DecodeDeviceFrame(entry.Frame)
			if e != nil {
				t.Fatal(e)
			}
			deviceKinds[f.Header.Kind] = true
			if f.Body.Result != nil {
				if f.Body.Result.Code != "APP_LOCAL_SYNTHETIC_APPLIED" {
					t.Fatal("wrong native result code")
				}
				sameInteropJSON(t, f.ResultCanonical, envelope.Body)
			}
			continue
		}
		if entry.Direction != "server_to_device" {
			t.Fatal("unknown direction")
		}
		f := ServerFrame{Header: h}
		switch h.Kind {
		case "command":
			var b TextCommandBody
			if err = json.Unmarshal(envelope.Body, &b); err != nil {
				t.Fatal(err)
			}
			d, e := CommandDigest(h.DeviceID, b)
			if e != nil || d != b.PayloadDigest {
				t.Fatal("native digest mismatch", e)
			}
			f.Body.Command = &b
		case "transport_ack":
			var b TransportAck
			if err = json.Unmarshal(envelope.Body, &b); err != nil {
				t.Fatal(err)
			}
			f.Body.TransportAck = &b
		case "result_ack":
			var b ResultAck
			if err = json.Unmarshal(envelope.Body, &b); err != nil {
				t.Fatal(err)
			}
			f.Body.ResultAck = &b
		default:
			t.Fatal("unexpected server kind")
		}
		encoded, e := EncodeServerFrame(f)
		if e != nil {
			t.Fatal(e)
		}
		sameInteropJSON(t, encoded, entry.Frame)
	}
	for _, kind := range []string{"accepted", "started", "execution_result"} {
		if !deviceKinds[kind] {
			t.Fatal("missing native stage", kind)
		}
	}
}

func sameInteropJSON(t *testing.T, a, b []byte) {
	t.Helper()
	var x, y any
	if e := json.Unmarshal(a, &x); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal(b, &y); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatal("shared cross-language JSON mismatch")
	}
}
