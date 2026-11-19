//go:build worktool_loopback

// A test-only loopback fixture; not a gateway or a deployment artifact.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	w "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol/gatewaywire"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

const (
	tenant  = "tenant_fixture"
	device  = "device_fixture"
	key     = "key_fixture"
	secret  = "secret_fixture"
	cmd     = "cmd_text"
	attempt = "attempt_text"
)

var errFixture = errors.New("fixture assertion failed")

func require(ok bool) error {
	if !ok {
		return errFixture
	}
	return nil
}
func post(r *http.Request, expected map[string]string) bool {
	var v map[string]any
	d := json.NewDecoder(io.LimitReader(r.Body, 4096))
	if d.Decode(&v) != nil {
		return false
	}
	for k, s := range expected {
		if v[k] != s {
			return false
		}
	}
	return true
}
func reply(o http.ResponseWriter, status int, v any) {
	o.Header().Set("Content-Type", "application/json")
	o.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(o).Encode(v)
	}
}
func sessionID(n int32) string {
	if n == 1 {
		return "session_fixture"
	}
	return "session_fixture_2"
}
func token(n int32) string { return fmt.Sprintf("access_fixture_%d", n) }
func send(c *websocket.Conn, ctx context.Context, raw []byte, err error) error {
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, raw)
}
func read(c *websocket.Conn, ctx context.Context) ([]byte, error) {
	typ, b, e := c.Read(ctx)
	if e == nil {
		e = require(typ == websocket.MessageText)
	}
	return b, e
}
func wire(c *websocket.Conn, ctx context.Context, sid string, seq int64, kind string, b w.ServerBody) error {
	raw, e := w.EncodeServerFrame(w.ServerFrame{Header: w.Header{Version: "1.0", Kind: kind, EventID: fmt.Sprintf("evt_fixture_s%d", seq), TenantID: tenant, DeviceID: device, SessionID: sid, Mode: "synthetic", Seq: seq, SentAt: time.Now().UTC().Truncate(time.Second)}, Body: b})
	return send(c, ctx, raw, e)
}

type witness struct{ digest, event string }

func exchange(ctx context.Context, c *websocket.Conn, n int32, ready <-chan struct{}, reconnect bool, recorded *atomic.Pointer[witness]) error {
	sid := sessionID(n)
	raw, e := read(c, ctx)
	if e != nil {
		return e
	}
	hello, e := w.DecodeHello(raw)
	if e != nil {
		return e
	}
	capable := false
	for _, cap := range hello.Capabilities {
		if cap == "message.send_text" {
			capable = true
		}
	}
	if !capable {
		return errFixture
	}
	raw, e = w.EncodeHelloAck(w.HelloAck{TenantID: tenant, DeviceID: device, SessionID: sid, PolicyVersion: 7, ServerTime: time.Now().UTC().Truncate(time.Second)})
	if e = send(c, ctx, raw, e); e != nil {
		return e
	}
	ss := int64(0)
	if n == 1 {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		b := w.TextCommandBody{CommandID: cmd, AttemptID: attempt, ExpiresAt: time.Now().UTC().Truncate(time.Second).Add(2 * time.Minute), PolicyVersion: 7, Payload: w.TextPayload{TargetID: "synthetic_target_001", Text: "isolated loopback fixture"}}
		b.PayloadDigest, e = w.CommandDigest(device, b)
		if e != nil {
			return e
		}
		ss++
		if e = wire(c, ctx, sid, ss, "command", w.ServerBody{Command: &b}); e != nil {
			return e
		}
	}
	accepted, started := false, false
	previous := int64(0)
	for count := 0; count < 12; count++ {
		raw, e = read(c, ctx)
		if e != nil {
			return e
		}
		f, e := w.DecodeDeviceFrame(raw)
		if e != nil {
			return e
		}
		h := f.Header
		if h.TenantID != tenant || h.DeviceID != device || h.SessionID != sid || h.Seq != previous+1 {
			return errFixture
		}
		previous = h.Seq
		switch h.Kind {
		case "accepted", "started":
			ref := f.Body.Accepted
			if h.Kind == "started" {
				ref = f.Body.Started
			}
			if n != 1 || ref == nil || ref.CommandID != cmd || ref.AttemptID != attempt {
				return errFixture
			}
			if h.Kind == "accepted" && !accepted && !started {
				accepted = true
			} else if h.Kind == "started" && accepted && !started {
				started = true
			} else {
				return errFixture
			}
			ss++
			if e = wire(c, ctx, sid, ss, "transport_ack", w.ServerBody{TransportAck: &w.TransportAck{AckEventID: h.EventID}}); e != nil {
				return e
			}
		case "execution_result":
			v := f.Body.Result
			if (n == 1 && (!accepted || !started)) || v == nil || v.CommandID != cmd || v.AttemptID != attempt || v.Code != "APP_LOCAL_SYNTHETIC_APPLIED" || v.Status != "succeeded" || v.EffectState != "applied" {
				return errFixture
			}
			if reconnect && n == 1 {
				recorded.Store(&witness{f.ResultDigest, h.EventID})
				fmt.Fprintln(os.Stderr, "RESULT_RECEIVED_WITHOUT_ACK")
				return nil
			}
			if n == 2 {
				prior := recorded.Load()
				if prior == nil || prior.digest != f.ResultDigest || prior.event == h.EventID {
					return errFixture
				}
			}
			ss++
			if e = wire(c, ctx, sid, ss, "result_ack", w.ServerBody{ResultAck: &w.ResultAck{AckEventID: h.EventID, CommandID: cmd}}); e != nil {
				return e
			}
			fmt.Fprintln(os.Stderr, "RESULT_ACK_SENT")
			return nil
		case "transport_ack", "heartbeat":
		default:
			return errFixture
		}
	}
	return errFixture
}
func main() {
	reconnect := flag.Bool("reconnect", false, "omit first result ACK and expect second-session replay")
	flag.Parse()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	var paired atomic.Bool
	var sessions, sockets atomic.Int32
	var recorded atomic.Pointer[witness]
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	nmax := int32(1)
	if *reconnect {
		nmax = 2
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/devices/enroll", func(o http.ResponseWriter, r *http.Request) {
		if !post(r, map[string]string{"enrollmentId": "enrollment_fixture", "enrollmentToken": "token_fixture"}) || !paired.CompareAndSwap(false, true) {
			reply(o, 400, nil)
			return
		}
		reply(o, 201, map[string]string{"deviceId": device, "keyId": key, "deviceSecret": secret})
	})
	mux.HandleFunc("POST /api/v1/device-sessions", func(o http.ResponseWriter, r *http.Request) {
		if !paired.Load() || r.Header.Get("Authorization") != "Bearer "+secret || !post(r, map[string]string{"deviceId": device, "keyId": key}) {
			reply(o, 400, nil)
			return
		}
		n := sessions.Add(1)
		if n > nmax {
			reply(o, 409, nil)
			return
		}
		reply(o, 201, map[string]string{"sessionId": sessionID(n), "accessToken": token(n)})
	})
	mux.HandleFunc("POST /ready", func(o http.ResponseWriter, r *http.Request) {
		if sockets.Load() != 1 {
			reply(o, 409, nil)
			return
		}
		select {
		case ready <- struct{}{}:
		default:
		}
		reply(o, 204, nil)
	})
	mux.HandleFunc("GET /api/v1/device-sessions", func(o http.ResponseWriter, r *http.Request) {
		n := sessions.Load()
		if n < 1 || n > nmax || r.Header.Get("Authorization") != "Bearer "+token(n) || !sockets.CompareAndSwap(n-1, n) {
			reply(o, 400, nil)
			return
		}
		c, e := websocket.Accept(o, r, nil)
		if e != nil {
			done <- e
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(w.MaxRawFrameBytes)
		sub, end := context.WithTimeout(ctx, 12*time.Second)
		defer end()
		if e = exchange(sub, c, n, ready, *reconnect, &recorded); e != nil {
			done <- e
			return
		}
		if n == nmax {
			done <- nil
			return
		}
	})
	s := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	fmt.Printf("http://%s\n", l.Addr())
	go func() {
		if e := s.Serve(l); e != nil && !errors.Is(e, http.ErrServerClosed) {
			done <- e
		}
	}()
	select {
	case e = <-done:
	case <-ctx.Done():
		e = ctx.Err()
	}
	_ = s.Close()
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
