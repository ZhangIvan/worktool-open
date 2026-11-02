package auth

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerStrict(t *testing.T) {
	for _, v := range []string{"", "Bearer " + strings.Repeat("a", 64) + "\n", "Bearer " + strings.Repeat("a", 64) + " ", "bearer " + strings.Repeat("a", 64)} {
		r := httptest.NewRequest("GET", "http://127.0.0.1/", nil)
		r.Header.Set("Authorization", v)
		if _, e := Bearer(r, true); e == nil {
			t.Fatal("invalid accepted")
		}
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1/", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	if _, e := Bearer(r, true); e != nil {
		t.Fatal("valid rejected")
	}
	r.Header.Add("Authorization", "Bearer "+strings.Repeat("b", 64))
	if _, e := Bearer(r, true); e == nil {
		t.Fatal("duplicate accepted")
	}
}
