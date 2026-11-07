package callback

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
)

// PinnedHTTPTransport is opt-in trusted composition. Constructing it does not
// resolve, dial or start a worker. The application has no default instance.
type PinnedHTTPTransport struct{ policy *security.Policy }

func NewPinnedHTTPTransport(rules []security.Rule) (*PinnedHTTPTransport, error) {
	p, e := security.NewPolicy(rules)
	if e != nil {
		return nil, ErrTransportPolicy
	}
	return &PinnedHTTPTransport{p}, nil
}
func (t *PinnedHTTPTransport) request(ctx context.Context, r Request) (*http.Request, error) {
	if t == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrTransportPolicy
	}
	deadline, ok := ctx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining <= 0 || remaining > 2*time.Second {
		return nil, ErrTransportPolicy
	}
	target, e := t.policy.Authorize(r.URL)
	if e != nil {
		return nil, ErrTransportPolicy
	}
	if _, e = target.Pin([]netip.Addr{r.IP}); e != nil {
		return nil, ErrTransportPolicy
	}
	if r.Host != target.Host() || r.Method != "POST" || r.ContentType != "application/json" || len(r.Body) == 0 || len(r.Body) > 4096 || r.ContentLength != len(r.Body) {
		return nil, ErrTransportPolicy
	}
	// Validate header syntax without keys or signature recomputation. Authenticity
	// belongs to the sender/receiver; this prevents newline/header injection.
	if !headerID(r.EventID) || !lowerHex64(r.Signature) {
		return nil, ErrTransportPolicy
	}
	ms, e := strconv.ParseInt(r.Timestamp, 10, 64)
	if e != nil || ms < 0 || ms > 9007199254740991 || strconv.FormatInt(ms, 10) != r.Timestamp {
		return nil, ErrTransportPolicy
	}
	req, e := http.NewRequestWithContext(ctx, "POST", r.URL, bytes.NewReader(bytes.Clone(r.Body)))
	if e != nil {
		return nil, ErrTransportPolicy
	}
	req.Host = r.Host
	req.ContentLength = int64(r.ContentLength)
	req.GetBody = nil
	req.Close = true
	req.Header.Set("Content-Type", r.ContentType)
	req.Header.Set("X-WorkTool-Event-Id", r.EventID)
	req.Header.Set("X-WorkTool-Timestamp", r.Timestamp)
	req.Header.Set("X-WorkTool-Signature", r.Signature)
	return req, nil
}
func headerID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, b := range []byte(s) {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-') {
			return false
		}
	}
	return true
}
func lowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, b := range []byte(s) {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func (t *PinnedHTTPTransport) Send(ctx context.Context, r Request) (Response, error) {
	req, e := t.request(ctx, r)
	if e != nil {
		return Response{}, e
	}
	tr := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false,
		MaxResponseHeaderBytes: 8192, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 2 * time.Second,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: r.Host},
		TLSNextProto:    make(map[string]func(string, *tls.Conn) http.RoundTripper),
	}
	tr.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != net.JoinHostPort(r.Host, "443") {
			return nil, ErrTransportPolicy
		}
		return (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(r.IP.String(), "443"))
	}
	defer tr.CloseIdleConnections()
	// RoundTrip, not Client.Do: there is no redirect or cookie processing. A fresh
	// HTTP/1 transport and non-rewindable body do not retry on a reused connection.
	return singleRoundTrip(tr, req)
}
func singleRoundTrip(rt http.RoundTripper, req *http.Request) (Response, error) {
	resp, e := rt.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if e != nil {
		return Response{}, errors.New("CALLBACK_HTTP_UNAVAILABLE")
	}
	if resp == nil || resp.Body == nil || resp.StatusCode < 100 || resp.StatusCode > 599 {
		return Response{}, errors.New("CALLBACK_HTTP_INVALID_RESPONSE")
	}
	// Do not read or retain receiver bodies. Status-only result is bounded.
	return Response{Status: resp.StatusCode}, nil
}
