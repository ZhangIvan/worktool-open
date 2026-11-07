package consoleapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testHash = "worktool-pbkdf2-sha256$600000$MDEyMzQ1Njc4OWFiY2RlZg==$3ArOiyG6CqntzRIDHJINBA8mwW84bEFOne2AFV1f+7Q="

type fakeStore struct {
	user                User
	missing             bool
	err                 error
	deviceTenant        string
	reads               int
	revokeBeforeDevices bool
}

func (s *fakeStore) FindUser(_ context.Context, tenant, login string) (User, error) {
	if s.err != nil {
		return User{}, s.err
	}
	if s.missing || tenant != s.user.Tenant || login != s.user.Username {
		return User{}, ErrNotFound
	}
	return s.user, nil
}
func (s *fakeStore) CurrentUser(_ context.Context, tenant, id string) (User, error) {
	s.reads++
	if s.err != nil {
		return User{}, s.err
	}
	if s.missing || tenant != s.user.Tenant || id != s.user.ID {
		return User{}, ErrNotFound
	}
	return s.user, nil
}
func (s *fakeStore) ListDevices(_ context.Context, a DeviceAuthority) ([]Device, error) {
	if s.revokeBeforeDevices {
		return nil, ErrUnauthenticated
	}
	s.deviceTenant = a.Tenant
	return []Device{{ID: "safe-device", Name: "Synthetic", State: "active", Capabilities: []string{}}}, s.err
}
func setup(t *testing.T) (*router, *fakeStore) {
	t.Helper()
	s := &fakeStore{user: User{ID: "user-a", Tenant: "tenant-a", Username: "admin", Role: "admin", Password: testHash, Active: true}}
	h, e := New(Options{Store: s, Origin: "http://127.0.0.1:5173", Fallback: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("X-Fallback", q.Header.Get("Authorization"))
		w.WriteHeader(418)
	})})
	if e != nil {
		t.Fatal(e)
	}
	return h.(*router), s
}
func req(r *router, method, path, body, token string, origin bool) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, "http://127.0.0.1:5173"+path, strings.NewReader(body))
	q.RemoteAddr = "127.0.0.1:12345"
	q.Header.Set("Content-Type", "application/json")
	if origin {
		q.Header.Set("Origin", r.origin)
	}
	if token != "" {
		q.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	return w
}
func seed(t *testing.T, r *router, s *fakeStore) (string, string) {
	t.Helper()
	token, e := newToken()
	if e != nil {
		t.Fatal(e)
	}
	csrf, e := newToken()
	if e != nil {
		t.Fatal(e)
	}
	u := s.user
	fp := sha256.Sum256([]byte(u.Password))
	u.Password = ""
	r.sessions[sha256.Sum256([]byte(token))] = session{u, fp, csrf, r.now().Add(sessionTTL)}
	return token, csrf
}
func TestBrowserBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		mutate             func(*http.Request)
		want               int
	}{
		{"foreign remote", "GET", "/api/v1/auth/me", func(q *http.Request) { q.RemoteAddr = "192.0.2.1:8888" }, 403},
		{"foreign host", "GET", "/api/v1/auth/me", func(q *http.Request) { q.Host = "evil.example" }, 403},
		{"foreign origin", "GET", "/api/v1/auth/me", func(q *http.Request) { q.Header.Set("Origin", "http://evil.example") }, 403},
		{"duplicate origin", "GET", "/api/v1/auth/me", func(q *http.Request) {
			q.Header.Add("Origin", "http://127.0.0.1:5173")
			q.Header.Add("Origin", "http://127.0.0.1:5173")
		}, 403},
		{"bearer forbidden", "GET", "/api/v1/devices", func(q *http.Request) { q.Header.Set("Authorization", "Bearer anything") }, 403},
		{"duplicate cookie", "GET", "/api/v1/auth/me", func(q *http.Request) { q.Header.Add("Cookie", q.Header.Get("Cookie")) }, 400},
		{"malformed cookie", "GET", "/api/v1/auth/me", func(q *http.Request) { q.Header.Add("Cookie", "bad") }, 400},
		{"logout origin required", "POST", "/api/v1/auth/logout", func(q *http.Request) {}, 403},
		{"unsupported device write", "POST", "/api/v1/devices", func(q *http.Request) {}, 405},
		{"query tenant forbidden", "GET", "/api/v1/devices?tenantId=other", func(q *http.Request) {}, 400},
		{"absent get origin allowed", "GET", "/api/v1/auth/me", func(q *http.Request) {}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := setup(t)
			token, _ := seed(t, r, s)
			q := httptest.NewRequest(tc.method, "http://127.0.0.1:5173"+tc.path, nil)
			q.RemoteAddr = "127.0.0.1:12345"
			q.AddCookie(&http.Cookie{Name: cookieName, Value: token})
			tc.mutate(q)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, q)
			if w.Code != tc.want {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestFallbackUnchanged(t *testing.T) {
	r, _ := setup(t)
	q := httptest.NewRequest("POST", "http://untrusted/api/v1/commands", nil)
	q.Header.Set("Authorization", "Bearer unchanged")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	if w.Code != 418 || w.Header().Get("X-Fallback") != "Bearer unchanged" {
		t.Fatal("fallback modified")
	}
}
func TestRoleAndRevocation(t *testing.T) {
	for _, role := range []string{"admin", "operator", "owner", "approver", "auditor"} {
		t.Run(role, func(t *testing.T) {
			r, s := setup(t)
			s.user.Role = role
			token, _ := seed(t, r, s)
			w := req(r, "GET", "/api/v1/devices", "", token, false)
			want := 403
			if role == "admin" || role == "operator" {
				want = 200
			}
			if w.Code != want {
				t.Fatal(w.Code)
			}
			if want == 200 && s.deviceTenant != "tenant-a" {
				t.Fatal("tenant not server held")
			}
			if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), testHash) {
				t.Fatal("secret leak")
			}
		})
	}
	for _, kind := range []string{"role", "password", "inactive", "missing", "expired", "store unavailable"} {
		t.Run(kind, func(t *testing.T) {
			r, s := setup(t)
			token, _ := seed(t, r, s)
			want := 401
			switch kind {
			case "role":
				s.user.Role = "operator"
			case "password":
				s.user.Password = "!disabled"
			case "inactive":
				s.user.Active = false
			case "missing":
				s.missing = true
			case "expired":
				r.now = func() time.Time { return time.Now().Add(sessionTTL + time.Second) }
			case "store unavailable":
				s.err = errors.New("secret db error")
				want = 503
			}
			w := req(r, "GET", "/api/v1/auth/me", "", token, false)
			if w.Code != want || strings.Contains(w.Body.String(), "secret db error") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
func TestLoginRotationAndLogout(t *testing.T) {
	r, s := setup(t)
	old, _ := seed(t, r, s)
	w := req(r, "POST", "/api/v1/auth/login", `{"tenantId":"tenant-a","loginName":"admin","password":"synthetic-password"}`, old, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("cookie missing")
	}
	c := cookies[0]
	if c.Name != cookieName || c.Value == old || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Domain != "" || c.Path != "/api/v1" || c.MaxAge != 900 {
		t.Fatal("unsafe cookie", c)
	}
	if req(r, "GET", "/api/v1/auth/me", "", old, false).Code != 401 {
		t.Fatal("old token survived rotation")
	}
	var body struct {
		CSRF string `json:"csrfToken"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil || !validToken(body.CSRF) {
		t.Fatal("csrf missing")
	}
	if strings.Contains(w.Body.String(), testHash) {
		t.Fatal("credential leaked")
	}
	if req(r, "POST", "/api/v1/auth/logout", "", c.Value, true).Code != 403 {
		t.Fatal("csrf not required")
	}
	q := httptest.NewRequest("POST", r.origin+"/api/v1/auth/logout", nil)
	q.RemoteAddr = "127.0.0.1:12345"
	q.Header.Set("Origin", r.origin)
	q.Header.Set("X-CSRF-Token", body.CSRF)
	q.AddCookie(c)
	out := httptest.NewRecorder()
	r.ServeHTTP(out, q)
	if out.Code != 200 {
		t.Fatal(out.Code)
	}
	if req(r, "GET", "/api/v1/auth/me", "", c.Value, false).Code != 401 {
		t.Fatal("logout did not revoke")
	}
}
func TestPasswordFormats(t *testing.T) {
	for _, tc := range []struct {
		hash, password string
		want           bool
	}{{testHash, "synthetic-password", true}, {testHash, "wrong", false}, {"!disabled", "synthetic-password", false}, {"scrypt$legacy", "x", false}, {strings.Replace(testHash, "600000", "999999999", 1), "x", false}, {testHash, strings.Repeat("x", 1025), false}} {
		if VerifyPassword(tc.hash, tc.password) != tc.want {
			t.Fatal("verification mismatch")
		}
	}
}
func TestLoginLimits(t *testing.T) {
	r, _ := setup(t)
	r.attempts = 20
	r.window = r.now()
	body := `{"tenantId":"tenant-a","loginName":"admin","password":"synthetic-password"}`
	if req(r, "POST", "/api/v1/auth/login", body, "", true).Code != 429 {
		t.Fatal("missing attempt cap")
	}
	r.attempts = 0
	r.derive <- struct{}{}
	r.derive <- struct{}{}
	if req(r, "POST", "/api/v1/auth/login", body, "", true).Code != 429 {
		t.Fatal("missing concurrency cap")
	}
}
func TestOriginConfiguration(t *testing.T) {
	_, s := setup(t)
	for _, origin := range []string{"https://127.0.0.1:5173", "http://example.com:5173", "http://127.0.0.1:5173/", "http://user@127.0.0.1:5173", "http://127.0.0.1:5173?x=1", "http://localhost:5173"} {
		if _, e := New(Options{Store: s, Origin: origin, Fallback: http.NotFoundHandler()}); e == nil {
			t.Fatal("accepted", origin)
		}
	}
}
func TestLoginRejectsInvalidCredentialsAndInput(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		inactive   bool
		want       int
	}{
		{"unknown tenant", `{"tenantId":"other","loginName":"admin","password":"synthetic-password"}`, false, 401},
		{"unknown login", `{"tenantId":"tenant-a","loginName":"absent","password":"synthetic-password"}`, false, 401},
		{"inactive", `{"tenantId":"tenant-a","loginName":"admin","password":"synthetic-password"}`, true, 401},
		{"wrong password", `{"tenantId":"tenant-a","loginName":"admin","password":"wrong"}`, false, 401},
		{"duplicate tenant", `{"tenantId":"other","tenantId":"tenant-a","loginName":"admin","password":"synthetic-password"}`, false, 400},
		{"duplicate password", `{"tenantId":"tenant-a","loginName":"admin","password":"bad","password":"synthetic-password"}`, false, 400},
		{"unknown field", `{"tenantId":"tenant-a","loginName":"admin","password":"synthetic-password","role":"admin"}`, false, 400},
		{"multiple objects", `{"tenantId":"tenant-a","loginName":"admin","password":"synthetic-password"}{}`, false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := setup(t)
			s.user.Active = !tc.inactive
			w := req(r, "POST", "/api/v1/auth/login", tc.body, "", true)
			if w.Code != tc.want || len(w.Result().Cookies()) != 0 || len(r.sessions) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
func TestSessionCapacityAndCleanup(t *testing.T) {
	r, s := setup(t)
	for i := 0; i < maxSessions; i++ {
		seed(t, r, s)
	}
	body := `{"tenantId":"tenant-a","loginName":"admin","password":"synthetic-password"}`
	if req(r, "POST", "/api/v1/auth/login", body, "", true).Code != 503 || len(r.sessions) != maxSessions {
		t.Fatal("session cap failed")
	}
	future := time.Now().Add(sessionTTL + time.Second)
	r.now = func() time.Time { return future }
	w := req(r, "POST", "/api/v1/auth/login", body, "", true)
	if w.Code != 200 || len(r.sessions) != 1 {
		t.Fatal("cleanup failed", w.Code, len(r.sessions))
	}
}

func TestDeviceRevocationBetweenReads(t *testing.T) {
	r, s := setup(t)
	token, _ := seed(t, r, s)
	s.revokeBeforeDevices = true
	w := req(r, "GET", "/api/v1/devices", "", token, false)
	if w.Code != 401 || strings.Contains(w.Body.String(), "safe-device") || len(r.sessions) != 0 {
		t.Fatal("revoked data escaped", w.Code, w.Body.String())
	}
}
func TestLoginRejectsInvalidUTF8(t *testing.T) {
	r, _ := setup(t)
	body := "{\"tenantId\":\"tenant-a\",\"loginName\":\"admin\",\"password\":\"" + string([]byte{0xff}) + "\"}"
	if req(r, "POST", "/api/v1/auth/login", body, "", true).Code != 400 {
		t.Fatal("invalid UTF-8 accepted")
	}
}
func TestResponseErrorAndExpiryContract(t *testing.T) {
	r, s := setup(t)
	token, _ := seed(t, r, s)
	w := req(r, "GET", "/api/v1/auth/me", "", token, false)
	var success map[string]any
	if json.Unmarshal(w.Body.Bytes(), &success) != nil {
		t.Fatal("invalid success JSON")
	}
	at, ok := success["expiresAt"].(string)
	if !ok {
		t.Fatal("missing expiresAt")
	}
	if _, e := time.Parse(time.RFC3339, at); e != nil {
		t.Fatal(e)
	}
	w = req(r, "GET", "/api/v1/auth/me", "", "", false)
	var failure struct {
		Error struct {
			Code, Message, RequestID string
			Retryable                bool
			Details                  []string
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error.Code != "UNAUTHENTICATED" || failure.Error.Message == "" || failure.Error.RequestID == "" || failure.Error.Details == nil {
		t.Fatal("error contract", w.Body.String())
	}
}
