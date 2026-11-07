package security

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestPinnedNodeSignatureVectors(t *testing.T) {
	raw, err := os.ReadFile("node_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Vectors []struct{ KeyHex, EventID, Timestamp, Body, Signature string }
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || len(fixture.Vectors) != 3 {
		t.Fatal("missing reference vectors", err)
	}
	for i, v := range fixture.Vectors {
		key, _ := hex.DecodeString(v.KeyHex)
		ms, _ := strconv.ParseInt(v.Timestamp, 10, 64)
		got, err := Sign(key, v.EventID, ms, []byte(v.Body))
		if err != nil || got != v.Signature || !Verify(key, v.EventID, v.Timestamp, got, []byte(v.Body), ms) {
			t.Fatalf("vector %d differs from pinned Node behavior", i)
		}
	}
}

func TestSignatureBoundaries(t *testing.T) {
	key := bytes.Repeat([]byte{0x61}, 32) // deliberately public synthetic fixture
	body := []byte(`{"status":"succeeded"}`)
	const ms int64 = 1793667900000
	ts := strconv.FormatInt(ms, 10)
	sig, err := Sign(key, "event_fixture", ms, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int64{-30000, 0, 30000} {
		if !Verify(key, "event_fixture", ts, sig, body, ms+offset) {
			t.Fatal("inclusive replay boundary")
		}
	}
	for _, offset := range []int64{-30001, 30001} {
		if Verify(key, "event_fixture", ts, sig, body, ms+offset) {
			t.Fatal("accepted stale/future timestamp")
		}
	}
	for _, bad := range []string{"", "0" + ts, "+" + ts, " " + ts, ts + "\n", "1e12", "9007199254740992", "9223372036854775808"} {
		if Verify(key, "event_fixture", bad, sig, body, ms) {
			t.Fatal("accepted noncanonical timestamp")
		}
	}
	for _, bad := range []string{"", strings.ToUpper(sig), sig + "0", strings.Repeat("g", 64), strings.Repeat("0", 64)} {
		if Verify(key, "event_fixture", ts, bad, body, ms) {
			t.Fatal("accepted invalid signature")
		}
	}
	if Verify(key, "event_other", ts, sig, body, ms) || Verify(key, "event_fixture", ts, sig, append(append([]byte{}, body...), '\n'), ms) || Verify(bytes.Repeat([]byte{0x62}, 32), "event_fixture", ts, sig, body, ms) {
		t.Fatal("accepted changed event/body/key")
	}
	for _, event := range []string{"", "x.y", "x\ny", "中文", strings.Repeat("e", 129)} {
		if _, err := Sign(key, event, ms, body); err == nil {
			t.Fatal("accepted ambiguous event ID")
		}
	}
	for _, n := range []int{0, 31, 33} {
		if _, err := Sign(make([]byte, n), "event_fixture", ms, body); err == nil {
			t.Fatal("accepted noncontract key length")
		}
	}
	for _, n := range []int{0, MaxBodyBytes + 1} {
		if _, err := Sign(key, "event_fixture", ms, make([]byte, n)); err == nil {
			t.Fatal("accepted body bound violation")
		}
	}
	if _, err := Sign(key, "event_fixture", ms, make([]byte, MaxBodyBytes)); err != nil {
		t.Fatal("rejected exact body limit")
	}
	for _, bad := range []int64{-1, maxSafeInteger + 1} {
		if _, err := Sign(key, "event_fixture", bad, body); err == nil {
			t.Fatal("accepted timestamp overflow")
		}
	}
}
