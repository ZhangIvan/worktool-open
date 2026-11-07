package httpapi

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerBindingNeverListens(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:3100", ":3100", "localhost:3100", "192.0.2.1:3100"} {
		if _, e := NewServer(addr, nil); e == nil {
			t.Fatal("nonliteral loopback accepted")
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		s, e := NewServer(addr, nil)
		if e != nil || s.ReadHeaderTimeout <= 0 || s.MaxHeaderBytes != 8192 {
			t.Fatal("bounded configuration")
		}
	}
}
func TestStrictHTTPBodyNoSockets(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{"x":1,"extra":2}`, `{"x":1} {}`, `{"x":"\ud800"}`, strings.Repeat(" ", 65537)} {
		r := httptest.NewRequest("POST", "http://synthetic.invalid/", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if _, e := body(r, []string{"x"}); e == nil {
			t.Fatal("noncanonical shape accepted")
		}
	}
	r := httptest.NewRequest("POST", "http://synthetic.invalid/", strings.NewReader(`{"x":"synthetic"}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	if _, e := body(r, []string{"x"}); e != nil {
		t.Fatal("valid body rejected")
	}
}
func TestErrorsRedacted(t *testing.T) {
	w := httptest.NewRecorder()
	failure(w, errors.New("synthetic-sensitive-detail"))
	if w.Code != 503 || strings.Contains(w.Body.String(), "sensitive-detail") || !strings.Contains(w.Body.String(), "STORAGE_UNAVAILABLE") {
		t.Fatal("untrusted error disclosed")
	}
}
