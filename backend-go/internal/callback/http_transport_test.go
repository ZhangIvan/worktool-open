package callback

import (
	"context"
	"errors"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type unreadBody struct{ read, closed bool }

func (b *unreadBody) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }
func (b *unreadBody) Close() error             { b.closed = true; return nil }
func transportFixture(t *testing.T) (*PinnedHTTPTransport, Request, context.Context) {
	t.Helper()
	tr, e := NewPinnedHTTPTransport([]security.Rule{{URL: "https://callbacks.example.com/result", CIDRs: []string{"93.184.216.0/24"}}})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return tr, Request{URL: "https://callbacks.example.com/result", Host: "callbacks.example.com", IP: netip.MustParseAddr("93.184.216.34"), Method: "POST", ContentType: "application/json", ContentLength: 2, EventID: "evt_test", Timestamp: "1", Signature: strings.Repeat("a", 64), Body: []byte("{}")}, ctx
}
func TestHTTPTransportRequestPinAndHeaders(t *testing.T) {
	tr, r, ctx := transportFixture(t)
	req, e := tr.request(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	r.Body[0] = 'x'
	body, _ := io.ReadAll(req.Body)
	if string(body) != "{}" || req.GetBody != nil || !req.Close || req.Host != r.Host || req.ContentLength != 2 {
		t.Fatal("request isolation")
	}
	if req.Header.Get("X-WorkTool-Signature") != r.Signature {
		t.Fatal("signature")
	}
}
func TestHTTPTransportRejectsBeforeDial(t *testing.T) {
	tr, r, ctx := transportFixture(t)
	for _, mut := range []func(*Request){func(v *Request) { v.IP = netip.MustParseAddr("127.0.0.1") }, func(v *Request) { v.URL = "http://callbacks.example.com/result" }, func(v *Request) { v.Host = "other.example.com" }, func(v *Request) { v.EventID = "evt\n" }, func(v *Request) { v.Signature = "bad" }, func(v *Request) { v.ContentLength++ }, func(v *Request) { v.Body = make([]byte, 4097); v.ContentLength = 4097 }, func(v *Request) { v.Timestamp = "01" }} {
		copy := r
		mut(&copy)
		if _, e := tr.request(ctx, copy); !errors.Is(e, ErrTransportPolicy) {
			t.Fatal("bad request accepted")
		}
	}
	if _, e := tr.request(context.Background(), r); e == nil {
		t.Fatal("unbounded deadline")
	}
}
func TestHTTPTransportSingleCallNoBodyRead(t *testing.T) {
	tr, r, ctx := transportFixture(t)
	req, e := tr.request(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, status := range []int{204, 302, 429, 503} {
		body := &unreadBody{}
		calls := 0
		res, e := singleRoundTrip(transportFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Body: body, Header: http.Header{"Location": []string{"https://other.example.com"}}}, nil
		}), req)
		if e != nil || res.Status != status || calls != 1 || body.read || !body.closed {
			t.Fatal("response policy")
		}
	}
}
