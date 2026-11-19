package security

import (
	"net/netip"
	"strings"
	"testing"
)

func TestCanonicalDestination(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Hooks.Example.COM:443/callback-v1": "https://hooks.example.com/callback-v1",
		"https://hooks.example.com":                 "https://hooks.example.com/",
		"https://hooks.example.com/a_b.~":           "https://hooks.example.com/a_b.~",
	} {
		got, err := CanonicalURL(raw)
		if err != nil || got != want {
			t.Errorf("canonical mismatch for %q", raw)
		}
	}
	for _, raw := range []string{
		"", "http://hooks.example.com/", "https:hooks.example.com", "https://user:secret@hooks.example.com/",
		"https://hooks.example.com:80/", "https://hooks.example.com:0443/", "https://hooks.example.com:/",
		"https://127.0.0.1/", "https://127.1/", "https://2130706433/", "https://0x7f000001/",
		"https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://hooks.example.com./",
		"https://localhost/", "https://metadata.internal/", "https://foo.local/", "https://a..example.com/",
		"https://-a.example.com/", "https://a_.example.com/", "https://中文.example.com/",
		"https://hooks.example.com/a?", "https://hooks.example.com/a?token=x", "https://hooks.example.com/a#",
		"https://hooks.example.com/%2Fsecret", "https://hooks.example.com/../secret", "https://hooks.example.com/./a",
		"https://hooks.example.com//a", "https://hooks.example.com\\@internal/", "https://hooks.example.com/a\nb",
		"https://hooks.example.com/a;b", "https://hooks.example.com/a b", "https://" + strings.Repeat("a", 64) + ".com/",
	} {
		if _, err := CanonicalURL(raw); err == nil {
			t.Errorf("unsafe or unsupported URL accepted: %q", raw)
		}
	}
}

func TestExactPolicyAndCopiedPins(t *testing.T) {
	rules := []Rule{{URL: "https://hooks.example.com/callback", CIDRs: []string{"8.8.8.0/24", "2606:4700::/32"}}}
	p, err := NewPolicy(rules)
	if err != nil {
		t.Fatal(err)
	}
	rules[0].CIDRs[0] = "10.0.0.0/8"
	target, err := p.Authorize("https://hooks.example.com/callback")
	if err != nil || target.Host() != "hooks.example.com" || target.String() != "CallbackTarget(redacted)" {
		t.Fatal("target identity")
	}
	for _, raw := range []string{"https://hooks.example.com/other", "https://hooks.example.com:443/callback", "https://other.example.com/callback"} {
		if _, err := p.Authorize(raw); err == nil {
			t.Fatal("policy matched nonexact URL")
		}
	}
	answers := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2606:4700:4700::1111")}
	pins, err := target.Pin(answers)
	if err != nil || len(pins) != 2 {
		t.Fatal("expected copied unique pins", err)
	}
	answers[0] = netip.MustParseAddr("127.0.0.1")
	if pins[0].String() != "8.8.8.8" {
		t.Fatal("pins alias caller DNS storage")
	}
	for _, raw := range []string{"10.0.0.1", "1.1.1.1", "2001:4860:4860::8888", "::ffff:8.8.8.8", "2606:4700::1%eth0"} {
		if _, err := target.Pin([]netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(raw)}); err == nil {
			t.Fatal("mixed unsafe/unapproved answer accepted")
		}
	}
	if _, err := target.Pin(nil); err == nil {
		t.Fatal("empty DNS answers")
	}
	if _, err := target.Pin(make([]netip.Addr, 17)); err == nil {
		t.Fatal("oversized DNS answers")
	}
	if _, err := (Target{}).Pin([]netip.Addr{netip.MustParseAddr("8.8.8.8")}); err == nil {
		t.Fatal("zero target granted pins")
	}
	var empty *Policy
	if _, err := empty.Authorize("https://hooks.example.com/callback"); err == nil {
		t.Fatal("nil policy")
	}
}

func TestPublicAddressDenials(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1",
		"192.0.0.9", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.1", "192.168.1.1",
		"192.175.48.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:8.8.8.8", "64:ff9b::808:808", "64:ff9b:1::1", "100::1", "100:0:0:1::1",
		"2001::1", "2001:1::1", "2001:db8::1", "2002:808:808::1", "2620:4f:8000::1", "3fff::1",
		"5f00::1", "fc00::1", "fe80::1", "ff02::1", "2606:4700::1%en0",
	} {
		if publicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("special address allowed: %s", raw)
		}
	}
	if publicAddress(netip.Addr{}) {
		t.Fatal("zero address")
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !publicAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("public address rejected: %s", raw)
		}
	}
}

func TestInvalidPolicy(t *testing.T) {
	for _, rules := range [][]Rule{
		nil,
		{{URL: "https://hooks.example.com/"}},
		{{URL: "https://Hooks.Example.com/", CIDRs: []string{"8.8.8.0/24"}}},
		{{URL: "https://hooks.example.com/", CIDRs: []string{"0.0.0.0/0"}}},
		{{URL: "https://hooks.example.com/", CIDRs: []string{"10.0.0.0/8"}}},
		{{URL: "https://hooks.example.com/", CIDRs: []string{"8.8.8.8/24"}}},
		{{URL: "https://hooks.example.com/", CIDRs: []string{"8.0.0.0/6"}}},
		{{URL: "https://hooks.example.com/", CIDRs: []string{"bad"}}},
		{{URL: "https://hooks.example.com/", CIDRs: []string{"8.8.8.0/24"}}, {URL: "https://hooks.example.com/", CIDRs: []string{"8.8.8.0/24"}}},
	} {
		if _, err := NewPolicy(rules); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
