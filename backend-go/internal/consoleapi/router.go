// Package consoleapi is a loopback HTTP-only synthetic browser console. Sessions
// are process-local and lost on restart. It is not production authentication.
package consoleapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("console user not found")
var ErrUnauthenticated = errors.New("console authority changed")

// DeviceAuthority is server-held session evidence, never a request DTO.
type DeviceAuthority struct {
	Tenant, ID, Role    string
	PasswordFingerprint [32]byte
}

type User struct {
	ID       string `json:"principalId"`
	Tenant   string `json:"tenantId"`
	Username string `json:"loginName"`
	Role     string `json:"role"`
	Password string `json:"-"`
	Active   bool   `json:"-"`
}
type Device struct {
	ID              string   `json:"deviceId"`
	Name            string   `json:"name"`
	State           string   `json:"state"`
	PolicyVersion   int64    `json:"policyVersion"`
	RevocationEpoch int64    `json:"revocationEpoch"`
	Capabilities    []string `json:"capabilities"`
}
type BrowserStore interface {
	FindUser(context.Context, string, string) (User, error)
	CurrentUser(context.Context, string, string) (User, error)
	ListDevices(context.Context, DeviceAuthority) ([]Device, error)
}
type Options struct {
	Store    BrowserStore
	Origin   string
	Fallback http.Handler
}
type session struct {
	user        User
	fingerprint [32]byte
	csrf        string
	expires     time.Time
}
type router struct {
	store        BrowserStore
	origin, host string
	fallback     http.Handler
	mu           sync.Mutex
	sessions     map[[32]byte]session
	window       time.Time
	attempts     int
	derive       chan struct{}
	now          func() time.Time
}

const cookieName = "worktool_go_console"
const sessionTTL = 15 * time.Minute
const maxSessions = 128

func New(o Options) (http.Handler, error) {
	u, e := url.Parse(o.Origin)
	if e != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Host == "" || u.String() != o.Origin {
		return nil, errors.New("exact loopback HTTP origin required")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" || o.Store == nil || o.Fallback == nil {
		return nil, errors.New("loopback origin, store and fallback required")
	}
	return &router{store: o.Store, origin: o.Origin, host: u.Host, fallback: o.Fallback, sessions: make(map[[32]byte]session), derive: make(chan struct{}, 2), now: time.Now}, nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code string) {
	id, e := newToken()
	if e != nil {
		id = "unavailable"
	}
	respond(w, status, map[string]any{"error": map[string]any{"code": code, "message": code, "requestId": "req_" + id, "retryable": status == 429 || status == 503, "details": []string{}}})
}
func (r *router) ServeHTTP(w http.ResponseWriter, q *http.Request) {
	path := q.URL.Path
	if path != "/api/v1/auth/login" && path != "/api/v1/auth/me" && path != "/api/v1/auth/logout" && path != "/api/v1/devices" {
		r.fallback.ServeHTTP(w, q)
		return
	}
	remote, _, remoteErr := net.SplitHostPort(q.RemoteAddr)
	ip := net.ParseIP(remote)
	if remoteErr != nil || ip == nil || !ip.IsLoopback() {
		fail(w, 403, "CONSOLE_REQUEST_DENIED")
		return
	}
	if q.Host != r.host || len(q.Header.Values("Authorization")) != 0 {
		fail(w, 403, "CONSOLE_REQUEST_DENIED")
		return
	}
	origins := q.Header.Values("Origin")
	if len(origins) > 1 || (len(origins) == 1 && origins[0] != r.origin) {
		fail(w, 403, "ORIGIN_DENIED")
		return
	}
	token, valid := readCookie(q)
	if !valid {
		fail(w, 400, "INVALID_COOKIE")
		return
	}
	expected := "GET"
	if path == "/api/v1/auth/login" || path == "/api/v1/auth/logout" {
		expected = "POST"
	}
	if q.Method != expected {
		w.Header().Set("Allow", expected)
		fail(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	if expected == "POST" && (len(origins) != 1 || origins[0] != r.origin) {
		fail(w, 403, "ORIGIN_REQUIRED")
		return
	}
	if q.URL.RawQuery != "" {
		fail(w, 400, "INVALID_INPUT")
		return
	}
	if path == "/api/v1/auth/login" {
		r.login(w, q, token)
		return
	}
	s, ok := r.authenticate(w, q, token)
	if !ok {
		return
	}
	switch path {
	case "/api/v1/auth/me":
		respond(w, 200, map[string]any{"user": s.user, "csrfToken": s.csrf, "expiresAt": s.expires.UTC().Format(time.RFC3339), "mode": "synthetic"})
	case "/api/v1/auth/logout":
		h := q.Header.Values("X-CSRF-Token")
		if len(h) != 1 || subtle.ConstantTimeCompare([]byte(h[0]), []byte(s.csrf)) != 1 {
			fail(w, 403, "CSRF_DENIED")
			return
		}
		r.mu.Lock()
		delete(r.sessions, sha256.Sum256([]byte(token)))
		r.mu.Unlock()
		setCookie(w, "", -1)
		respond(w, 200, map[string]string{"status": "logged_out"})
	case "/api/v1/devices":
		if s.user.Role != "admin" && s.user.Role != "operator" {
			fail(w, 403, "ROLE_DENIED")
			return
		}
		devices, e := r.store.ListDevices(q.Context(), DeviceAuthority{s.user.Tenant, s.user.ID, s.user.Role, s.fingerprint})
		if errors.Is(e, ErrUnauthenticated) {
			r.mu.Lock()
			delete(r.sessions, sha256.Sum256([]byte(token)))
			r.mu.Unlock()
			setCookie(w, "", -1)
			fail(w, 401, "UNAUTHENTICATED")
			return
		}
		if e != nil {
			fail(w, 503, "STORAGE_UNAVAILABLE")
			return
		}
		if devices == nil {
			devices = []Device{}
		}
		respond(w, 200, map[string]any{"items": devices, "mode": "synthetic"})
	}
}

// Reject malformed or duplicate cookies instead of relying on net/http's
// permissive parser (which silently drops malformed values).
func readCookie(q *http.Request) (string, bool) {
	seen := map[string]bool{}
	token := ""
	for _, line := range q.Header.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			name, value, ok := strings.Cut(part, "=")
			if !ok || name == "" || seen[name] {
				return "", false
			}
			seen[name] = true
			c, e := http.ParseCookie(part)
			if e != nil || len(c) != 1 || c[0].Name != name || c[0].Value != value {
				return "", false
			}
			if name == cookieName {
				if !validToken(value) {
					return "", false
				}
				token = value
			}
		}
	}
	return token, true
}
func validToken(s string) bool {
	b, e := base64.RawURLEncoding.Strict().DecodeString(s)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}
func setCookie(w http.ResponseWriter, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/api/v1", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (r *router) cleanup(now time.Time) {
	for k, s := range r.sessions {
		if !now.Before(s.expires) {
			delete(r.sessions, k)
		}
	}
}
func (r *router) authenticate(w http.ResponseWriter, q *http.Request, token string) (session, bool) {
	key := sha256.Sum256([]byte(token))
	r.mu.Lock()
	r.cleanup(r.now())
	s, ok := r.sessions[key]
	r.mu.Unlock()
	if !ok || token == "" {
		fail(w, 401, "UNAUTHENTICATED")
		return session{}, false
	}
	u, e := r.store.CurrentUser(q.Context(), s.user.Tenant, s.user.ID)
	if e != nil && !errors.Is(e, ErrNotFound) {
		fail(w, 503, "STORAGE_UNAVAILABLE")
		return session{}, false
	}
	if e != nil || !u.Active || u.Tenant != s.user.Tenant || u.ID != s.user.ID || u.Role != s.user.Role || sha256.Sum256([]byte(u.Password)) != s.fingerprint {
		r.mu.Lock()
		delete(r.sessions, key)
		r.mu.Unlock()
		setCookie(w, "", -1)
		fail(w, 401, "UNAUTHENTICATED")
		return session{}, false
	}
	u.Password = ""
	s.user = u
	return s, true
}
func (r *router) login(w http.ResponseWriter, q *http.Request, oldToken string) {
	mt, _, e := mime.ParseMediaType(q.Header.Get("Content-Type"))
	if e != nil || mt != "application/json" {
		fail(w, 415, "JSON_REQUIRED")
		return
	}
	var body struct {
		Tenant   string `json:"tenantId"`
		Username string `json:"loginName"`
		Password string `json:"password"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(q.Body, 4097))
	if readErr != nil || len(raw) > 4096 {
		fail(w, 400, "INVALID_INPUT")
		return
	}
	if _, e := protocol.Decode(raw, 4096); e != nil {
		fail(w, 400, "INVALID_INPUT")
		return
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || len(body.Tenant) < 1 || len(body.Tenant) > 128 || len(body.Username) < 1 || len(body.Username) > 128 || len(body.Password) < 1 || len(body.Password) > 1024 {
		fail(w, 400, "INVALID_INPUT")
		return
	}
	now := r.now()
	r.mu.Lock()
	r.cleanup(now)
	if r.window.IsZero() || now.Sub(r.window) >= time.Minute {
		r.window = now
		r.attempts = 0
	}
	if r.attempts >= 20 {
		r.mu.Unlock()
		fail(w, 429, "LOGIN_RATE_LIMITED")
		return
	}
	r.attempts++
	r.mu.Unlock()
	select {
	case r.derive <- struct{}{}:
		defer func() { <-r.derive }()
	default:
		fail(w, 429, "LOGIN_BUSY")
		return
	}
	u, e := r.store.FindUser(q.Context(), body.Tenant, body.Username)
	if e != nil && !errors.Is(e, ErrNotFound) {
		fail(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	encoded := u.Password
	if e != nil || !u.Active || !validPasswordFormat(encoded) {
		encoded = dummyPassword
	}
	verified := VerifyPassword(encoded, body.Password)
	if !verified || e != nil || !u.Active || encoded != u.Password {
		fail(w, 401, "INVALID_CREDENTIALS")
		return
	}
	// Recheck after expensive derivation, outside all database transactions.
	current, e := r.store.CurrentUser(q.Context(), u.Tenant, u.ID)
	if e != nil && !errors.Is(e, ErrNotFound) {
		fail(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	if e != nil || current != u {
		fail(w, 401, "INVALID_CREDENTIALS")
		return
	}
	token, e := newToken()
	if e != nil {
		fail(w, 503, "SESSION_UNAVAILABLE")
		return
	}
	csrf, e := newToken()
	if e != nil {
		fail(w, 503, "SESSION_UNAVAILABLE")
		return
	}
	fingerprint := sha256.Sum256([]byte(u.Password))
	u.Password = ""
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanup(r.now())
	if oldToken != "" {
		delete(r.sessions, sha256.Sum256([]byte(oldToken)))
	}
	if len(r.sessions) >= maxSessions {
		fail(w, 503, "SESSION_CAPACITY")
		return
	}
	expires := r.now().Add(sessionTTL)
	r.sessions[sha256.Sum256([]byte(token))] = session{u, fingerprint, csrf, expires}
	setCookie(w, token, int(sessionTTL.Seconds()))
	respond(w, 200, map[string]any{"user": u, "csrfToken": csrf, "expiresAt": expires.UTC().Format(time.RFC3339), "mode": "synthetic"})
}
func newToken() (string, error) {
	var b [32]byte
	_, e := rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:]), e
}
func validPasswordFormat(s string) bool {
	if len(s) > 128 {
		return false
	}
	p := strings.Split(s, "$")
	if len(p) != 4 || p[0] != "worktool-pbkdf2-sha256" || p[1] != "600000" {
		return false
	}
	a, e := base64.StdEncoding.Strict().DecodeString(p[2])
	if e != nil || len(a) != 16 || base64.StdEncoding.EncodeToString(a) != p[2] {
		return false
	}
	b, e := base64.StdEncoding.Strict().DecodeString(p[3])
	return e == nil && len(b) == 32 && base64.StdEncoding.EncodeToString(b) == p[3]
}
