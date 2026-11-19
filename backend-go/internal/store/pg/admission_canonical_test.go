package pg

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type goldenDigest struct{ CanonicalText, SHA256 string }
type goldenSubscription struct {
	App, SubscriptionID, DestinationID, EventType, EventSchemaVersion                                       string
	SubscriptionVersion, DestinationPolicyVersion, KeyVersion, MaxAttempts, MaxEventsPerChild, MaxBodyBytes int64
}

func TestAdmissionRestrictedGolden(t *testing.T) {
	raw, e := os.ReadFile("testdata/admission-golden.json")
	if e != nil {
		t.Fatal(e)
	}
	var data struct {
		Vectors []struct {
			ID               string
			ContractAccepted bool
			Rejection        string
			Input            struct {
				Request struct {
					Type, ExpiresInSeconds   int64
					DeviceID, TargetID, Text string
				}
				CreatedMS     string
				PolicyVersion int64
				Tenant, AppID string
				Subscriptions []goldenSubscription
			}
			Timing                                     struct{ AcceptedAt, ExpiresAt, ExpiresMS string }
			RequestHash, CommandDigest, SnapshotDigest goldenDigest
		}
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	for _, v := range data.Vectors {
		t.Run(v.ID, func(t *testing.T) {
			r := AppTextRequest{"synthetic-golden-key", v.Input.Request.DeviceID, v.Input.Request.TargetID, v.Input.Request.Text, "synthetic", v.Input.Request.Type, v.Input.Request.ExpiresInSeconds}
			request, e := requestCanonical(r)
			ms, parseErr := strconv.ParseInt(v.Input.CreatedMS, 10, 64)
			var expiry, created string
			validTime := parseErr == nil && ms >= 0 && ms <= maxWireMillis-r.ExpiresInSeconds*1000
			if validTime {
				expiry, e2 := wireTime(ms + r.ExpiresInSeconds*1000)
				if e2 != nil {
					validTime = false
				}
				_ = expiry
			}
			if !v.ContractAccepted {
				if e == nil && validTime {
					t.Fatal("rejected golden unexpectedly accepted")
				}
				return
			}
			if e != nil || !validTime {
				t.Fatal("valid golden rejected")
			}
			expiry, _ = wireTime(ms + r.ExpiresInSeconds*1000)
			created, _ = wireTime(ms)
			if expiry != v.Timing.ExpiresAt || created != v.Timing.AcceptedAt {
				t.Fatal("wire time mismatch")
			}
			command, e := commandCanonical(r, v.Input.PolicyVersion, expiry)
			if e != nil {
				t.Fatal(e)
			}
			subs := make([]subscription, len(v.Input.Subscriptions))
			for i, x := range v.Input.Subscriptions {
				subs[i] = subscription{x.App, x.SubscriptionID, x.DestinationID, x.EventType, x.EventSchemaVersion, x.SubscriptionVersion, x.DestinationPolicyVersion, x.KeyVersion, x.MaxEventsPerChild, x.MaxBodyBytes, x.MaxAttempts}
			}
			sort.Slice(subs, func(i, j int) bool { return subs[i].Destination < subs[j].Destination })
			snapshot, e := snapshotCanonical(v.Input.Tenant, v.Input.AppID, subs)
			if e != nil {
				t.Fatal(e)
			}
			for _, pair := range []struct {
				actual string
				want   goldenDigest
			}{{request, v.RequestHash}, {command, v.CommandDigest}, {snapshot, v.SnapshotDigest}} {
				if pair.actual != pair.want.CanonicalText || hashText(pair.actual) != pair.want.SHA256 {
					t.Fatal("independent canonical bytes/digest mismatch")
				}
			}
		})
	}
}
func TestAdmissionRejectsForgedDTOAndInvalidUTF8(t *testing.T) {
	base := AppTextRequest{"synthetic-valid-key", "A", "synthetic_demo", "hello", "synthetic", 203, 30}
	for _, key := range []string{"has space", "has\nnewline", "short", strings.Repeat("a", 257)} {
		r := base
		r.IdempotencyKey = key
		if _, e := requestCanonical(r); e == nil {
			t.Fatal("bad idempotency key accepted")
		}
	}
	for _, text := range []string{string([]byte{0xff}), string([]byte{0xed, 0xa0, 0x80})} {
		r := base
		r.Text = text
		if _, e := requestCanonical(r); e == nil {
			t.Fatal("invalid UTF8/surrogate bytes accepted")
		}
	}
	for _, value := range []any{float64(1), int64(-1), maxSafeInteger + 1, nil} {
		if _, e := canonicalRestricted(value); e == nil {
			t.Fatal("out of restricted canonical subset")
		}
	}
	if _, e := AppAuthorityFromAuthenticated("T1", "X", "A", "not-a-token", 0, 0); e == nil {
		t.Fatal("unvalidated authority accepted")
	}
}
