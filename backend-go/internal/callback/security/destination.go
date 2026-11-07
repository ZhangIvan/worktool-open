package security

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

var ErrDestination = errors.New("CALLBACK_DESTINATION_DENIED")
var ErrAddresses = errors.New("CALLBACK_DNS_ANSWER_DENIED")

// Rule is trusted reviewed configuration, not request or event input. CIDRs are
// the destination's explicitly approved egress networks, in addition to the
// conservative public-address policy. Compilation never resolves the hostname.
type Rule struct {
	URL   string
	CIDRs []string
}

type Policy struct{ targets map[string]Target }
type Target struct {
	canonical string
	host      string
	networks  []netip.Prefix
}

func (Target) String() string { return "CallbackTarget(redacted)" }
func (t Target) URL() string  { return t.canonical }
func (t Target) Host() string { return t.host }

// CanonicalURL deliberately supports a narrow HTTPS:443 subset: ASCII DNS host,
// no credentials/query/fragment/percent escapes, and simple absolute path. It is
// normalization, NOT approval. IDN conversion and arbitrary URL compatibility
// are deliberately absent; reviewed ASCII A-label hosts can be allowed exactly.
func CanonicalURL(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return "", ErrDestination
	}
	for _, c := range []byte(raw) {
		if c <= 0x20 || c >= 0x7f || c == '\\' || c == '%' || c == '?' || c == '#' {
			return "", ErrDestination
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Host == "" {
		return "", ErrDestination
	}
	host := strings.ToLower(u.Hostname())
	if !dnsHost(host) || (u.Host != u.Hostname() && u.Host != u.Hostname()+":443") {
		return "", ErrDestination
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "//") {
		return "", ErrDestination
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "." || segment == ".." {
			return "", ErrDestination
		}
		for _, c := range []byte(segment) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~') {
				return "", ErrDestination
			}
		}
	}
	return "https://" + host + p, nil
}

func dnsHost(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range []byte(label) {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	// Reject numeric/alternate IPv4-looking hostnames, including hex final labels.
	last := labels[len(labels)-1]
	if last[0] < 'a' || last[0] > 'z' {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home", ".lan", ".arpa", ".invalid", ".test"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

// NewPolicy copies all configuration. An empty or malformed policy fails closed.
// Network allowlists cannot override the special-address deny policy.
func NewPolicy(rules []Rule) (*Policy, error) {
	if len(rules) == 0 || len(rules) > 64 {
		return nil, ErrDestination
	}
	p := &Policy{targets: make(map[string]Target, len(rules))}
	for _, rule := range rules {
		canonical, err := CanonicalURL(rule.URL)
		if err != nil || canonical != rule.URL || len(rule.CIDRs) == 0 || len(rule.CIDRs) > 32 {
			return nil, ErrDestination
		}
		if _, exists := p.targets[canonical]; exists {
			return nil, ErrDestination
		}
		u, _ := url.Parse(canonical)
		t := Target{canonical: canonical, host: u.Hostname()}
		for _, raw := range rule.CIDRs {
			n, err := netip.ParsePrefix(raw)
			if err != nil || n != n.Masked() || n.Addr().Is4In6() || !publicAddress(n.Addr()) || (n.Addr().Is4() && n.Bits() < 8) || (n.Addr().Is6() && n.Bits() < 16) {
				return nil, ErrDestination
			}
			t.networks = append(t.networks, n)
		}
		p.targets[canonical] = t
	}
	return p, nil
}

func (p *Policy) Authorize(canonicalURL string) (Target, error) {
	if p == nil {
		return Target{}, ErrDestination
	}
	t, ok := p.targets[canonicalURL]
	if !ok {
		return Target{}, ErrDestination
	}
	return t, nil
}

// Pin validates a supplied bounded DNS result and copies a deduplicated address
// list. Any unsafe/unapproved answer rejects the whole set. No resolver or dial
// is called. The future transport MUST dial only these literal IPs on port 443,
// with Host/SNI t.Host(), never re-resolve or use a proxy/redirect/pool fallback.
func (t Target) Pin(answers []netip.Addr) ([]netip.Addr, error) {
	if t.canonical == "" || len(answers) == 0 || len(answers) > 16 {
		return nil, ErrAddresses
	}
	seen := make(map[netip.Addr]bool, len(answers))
	pins := make([]netip.Addr, 0, len(answers))
	for _, a := range answers {
		allowed := false
		if !publicAddress(a) {
			return nil, ErrAddresses
		}
		for _, n := range t.networks {
			allowed = allowed || n.Contains(a)
		}
		if !allowed {
			return nil, ErrAddresses
		}
		if !seen[a] {
			seen[a] = true
			pins = append(pins, a)
		}
	}
	return pins, nil
}

// Conservative IANA special-purpose deny snapshot reviewed 2026-11-03. Some
// globally reachable special services are intentionally denied. This is not a
// guarantee of public routing; per-destination CIDRs and deployment egress
// controls remain required. See CONTRACT.md for source and maintenance gate.
var denied = prefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.31.196.0/24", "192.52.193.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"192.175.48.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32",
	"2002::/16", "2620:4f:8000::/48", "3fff::/20",
)

var ipv6Global = netip.MustParsePrefix("2000::/3")

func prefixes(raw ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(raw))
	for i, s := range raw {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func publicAddress(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" || a.Is4In6() || !a.IsGlobalUnicast() || (a.Is6() && !ipv6Global.Contains(a)) {
		return false
	}
	for _, n := range denied {
		if n.Contains(a) {
			return false
		}
	}
	return true
}
