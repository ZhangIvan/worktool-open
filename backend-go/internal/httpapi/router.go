// Package httpapi is the bounded loopback-only synthetic P0 router. It has no
// browser login, public admin provisioning, callback or production listener.
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/auth"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

// Upgrader must consume the authenticated session before HTTP101, then invoke
// the gateway with the exact physical nonce. Nil is explicitly unavailable, not
// a mocked successful WebSocket implementation. No RFC6455 code lives here.
type Upgrader interface {
	Upgrade(http.ResponseWriter, *http.Request, pg.SessionAuth)
}
type Options struct {
	Store     *pg.GatewayStore
	WebSocket Upgrader
}
type Router struct {
	store *pg.GatewayStore
	ws    Upgrader
}

func NewRouter(o Options) (http.Handler, error) {
	if o.Store == nil {
		return nil, errors.New("store required")
	}
	return &Router{o.Store, o.WebSocket}, nil
}
func NewServer(address string, h http.Handler) (*http.Server, error) {
	host, _, e := net.SplitHostPort(address)
	if e != nil {
		return nil, errors.New("loopback address required")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("loopback address required")
	}
	return &http.Server{Addr: address, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}, nil
}
func code(err error) string {
	if err == nil {
		return "STORAGE_UNAVAILABLE"
	}
	v := err.Error()
	allowed := map[string]bool{}
	for _, c := range []string{"UNAUTHENTICATED", "ROLE_DENIED", "SCOPE_DENIED", "NOT_FOUND", "SCHEMA_INVALID", "INVALID_INPUT", "NOT_IMPLEMENTED", "WS_PROVIDER_UNAVAILABLE", "CAPABILITY_UNAVAILABLE", "IDEMPOTENCY_CONFLICT", "SESSION_ALREADY_USED", "AUTHORITY_REVOKED_OR_CHANGED", "COMMIT_OUTCOME_UNKNOWN", "STORE_TIMEOUT", "RESOURCE_LIMIT", "CAPACITY_EXHAUSTED", "STORAGE_UNAVAILABLE"} {
		allowed[c] = true
	}
	if allowed[v] {
		return v
	}
	return "STORAGE_UNAVAILABLE"
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, err error) {
	c := code(err)
	status := 503
	switch c {
	case "SCHEMA_INVALID", "INVALID_INPUT":
		status = 400
	case "UNAUTHENTICATED", "AUTHORITY_REVOKED_OR_CHANGED":
		status = 401
	case "ROLE_DENIED", "SCOPE_DENIED":
		status = 403
	case "NOT_FOUND":
		status = 404
	case "IDEMPOTENCY_CONFLICT", "SESSION_ALREADY_USED":
		status = 409
	case "CAPABILITY_UNAVAILABLE":
		status = 422
	case "RESOURCE_LIMIT", "CAPACITY_EXHAUSTED":
		status = 429
	case "NOT_IMPLEMENTED":
		status = 501
	}
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		respond(w, 503, map[string]any{"error": map[string]any{"code": "STORAGE_UNAVAILABLE", "message": "STORAGE_UNAVAILABLE", "requestId": "req_unavailable", "retryable": true, "details": []string{}}})
		return
	}
	respond(w, status, map[string]any{"error": map[string]any{"code": c, "message": c, "requestId": "req_" + hex.EncodeToString(b[:]), "retryable": status == 503 || status == 429, "details": []string{}}})
}
func invalid() error { return errors.New("SCHEMA_INVALID") }
func body(r *http.Request, required []string) (map[string]any, error) {
	mt, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || mt != "application/json" {
		return nil, invalid()
	}
	for k, v := range params {
		if k != "charset" || strings.ToLower(v) != "utf-8" {
			return nil, invalid()
		}
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 65537))
	if e != nil || len(b) > 65536 {
		return nil, invalid()
	}
	v, e := protocol.Decode(b, 65536)
	if e != nil {
		return nil, invalid()
	}
	m, e := protocol.ExactObject(v, required, nil)
	if e != nil {
		return nil, invalid()
	}
	return m, nil
}
func stringField(m map[string]any, k string) (string, error) {
	v, ok := m[k].(string)
	if !ok {
		return "", invalid()
	}
	return v, nil
}
func requiredStrings(m map[string]any, keys ...string) ([]string, error) {
	v := make([]string, len(keys))
	for i, k := range keys {
		var e error
		v[i], e = stringField(m, k)
		if e != nil {
			return nil, e
		}
	}
	return v, nil
}
func idOK(s string) bool { return protocol.ValidID(s) }
func (a *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		failure(w, errors.New("ROLE_DENIED"))
		return
	}
	if r.URL.RawQuery != "" || r.Header.Get("Origin") != "" || len(r.Header.Values("Origin")) > 0 || r.Header.Get("Cookie") != "" {
		failure(w, errors.New("ROLE_DENIED"))
		return
	}
	path := r.URL.Path
	if r.Method == "GET" && path == "/health" {
		transport := "unavailable"
		if a.ws != nil {
			transport = "configured_not_verified"
		}
		respond(w, 200, map[string]any{"status": "ok", "mode": "synthetic", "websocket": transport})
		return
	}
	if r.Method == "GET" && path == "/api/v1/device-sessions" {
		if a.ws == nil {
			failure(w, errors.New("WS_PROVIDER_UNAVAILABLE"))
			return
		}
		raw, e := auth.Bearer(r, false)
		if e != nil {
			failure(w, e)
			return
		}
		session, e := a.store.AuthenticateSession(r.Context(), raw)
		if e != nil {
			failure(w, e)
			return
		}
		a.ws.Upgrade(w, r, session)
		return
	}
	if r.Method == "POST" && path == "/api/v1/devices/enroll" {
		m, e := body(r, []string{"enrollmentId", "enrollmentToken", "deviceLabel", "localConfirmed"})
		if e != nil {
			failure(w, e)
			return
		}
		v, e := requiredStrings(m, "enrollmentId", "enrollmentToken", "deviceLabel")
		if e != nil || m["localConfirmed"] != true {
			failure(w, invalid())
			return
		}
		out, e := a.store.ConsumeEnrollment(r.Context(), pg.EnrollmentExchange{EnrollmentID: v[0], EnrollmentToken: v[1], DeviceLabel: v[2], LocalConfirmed: true})
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 201, map[string]any{"deviceId": out.DeviceID, "keyId": out.KeyID, "deviceSecret": out.DeviceSecret, "policyVersion": out.PolicyVersion})
		return
	}
	if r.Method == "POST" && path == "/api/v1/device-sessions" {
		m, e := body(r, []string{"deviceId", "keyId"})
		if e != nil {
			failure(w, e)
			return
		}
		v, e := requiredStrings(m, "deviceId", "keyId")
		if e != nil {
			failure(w, e)
			return
		}
		raw, e := auth.Bearer(r, false)
		if e != nil {
			failure(w, e)
			return
		}
		out, e := a.store.RotateDeviceSession(r.Context(), v[0], v[1], raw)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 201, map[string]any{"sessionId": out.SessionID, "accessToken": out.AccessToken, "expiresAt": out.ExpiresAt, "revocationEpoch": out.RevocationEpoch})
		return
	}
	if r.Method == "POST" && path == "/api/developer/v1/messages" {
		raw, e := auth.Bearer(r, true)
		if e != nil {
			failure(w, e)
			return
		}
		p, e := a.store.AuthenticateApp(r.Context(), raw, "commands:write")
		if e != nil {
			failure(w, e)
			return
		}
		m, e := body(r, []string{"type", "deviceId", "targetId", "text", "expiresInSeconds"})
		if e != nil {
			failure(w, e)
			return
		}
		typ, e := protocol.SafeInteger(m["type"], 0)
		if e != nil {
			failure(w, invalid())
			return
		}
		if typ != 203 {
			failure(w, errors.New("NOT_IMPLEMENTED"))
			return
		}
		v, e := requiredStrings(m, "deviceId", "targetId", "text")
		if e != nil || !idOK(v[0]) {
			failure(w, invalid())
			return
		}
		ttl, e := protocol.SafeInteger(m["expiresInSeconds"], 30)
		if e != nil || ttl > 300 {
			failure(w, invalid())
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 {
			failure(w, invalid())
			return
		}
		accepted, e := a.store.AdmitText(r.Context(), p, pg.AppTextRequest{IdempotencyKey: keys[0], DeviceID: v[0], TargetID: v[1], Text: v[2], Mode: "synthetic", Type: 203, ExpiresInSeconds: ttl})
		if e != nil {
			failure(w, e)
			return
		}
		out, e := a.store.SubmissionReceipt(r.Context(), p, accepted.MessageID)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 202, messageJSON(out, true))
		return
	}
	if r.Method == "GET" && strings.HasPrefix(path, "/api/developer/v1/messages/") {
		id := strings.TrimPrefix(path, "/api/developer/v1/messages/")
		if !idOK(id) {
			failure(w, errors.New("NOT_FOUND"))
			return
		}
		raw, e := auth.Bearer(r, true)
		if e != nil {
			failure(w, e)
			return
		}
		p, e := a.store.AuthenticateApp(r.Context(), raw, "results:read")
		if e != nil {
			failure(w, e)
			return
		}
		out, e := a.store.Message(r.Context(), p, id)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, messageJSON(out, false))
		return
	}
	failure(w, errors.New("NOT_IMPLEMENTED"))
}
func messageJSON(r pg.MessageReceipt, accepted bool) map[string]any {
	var result any
	if r.Result != nil {
		result = json.RawMessage(*r.Result)
	}
	v := map[string]any{"batchId": r.BatchID, "messageId": r.MessageID, "mode": "synthetic", "children": []any{map[string]any{"childId": r.ChildID, "commandId": r.CommandID, "status": r.Status, "effectState": r.EffectState, "result": result}}}
	if accepted {
		v["accepted"] = true
	}
	return v
}
