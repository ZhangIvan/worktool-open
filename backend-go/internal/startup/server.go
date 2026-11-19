package startup

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/consoleapi"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/gateway"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/httpapi"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5/pgxpool"
)

func poolConfig(c Config) (*pgxpool.Config, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	secret, err := readPrivate(c.Database.PasswordFile, 1024)
	if err != nil {
		return nil, errors.New("DATABASE_SECRET_UNAVAILABLE")
	}
	defer clear(secret)
	// Exact bytes, with at most one conventional trailing newline. Never silently
	// trim other whitespace into a different credential.
	password := strings.TrimSuffix(string(secret), "\n")
	if len(password) < 16 || len(password) > 512 || strings.ContainsAny(password, "\x00\r\n") {
		return nil, errors.New("DATABASE_SECRET_INVALID")
	}
	d := c.Database
	u := &url.URL{Scheme: "postgres", User: url.UserPassword(d.User, password), Host: net.JoinHostPort(d.Host, strconv.Itoa(d.Port)), Path: "/" + d.Name}
	q := u.Query()
	q.Set("sslmode", "disable")
	q.Set("connect_timeout", "5")
	u.RawQuery = q.Encode()
	pc, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New("DATABASE_CONFIG_INVALID")
	}
	// Replace inherited connection/runtime options instead of accepting ambient
	// search_path, alternate hosts or TLS/DNS configuration from environment.
	pc.ConnConfig.Host = d.Host
	pc.ConnConfig.Port = uint16(d.Port)
	pc.ConnConfig.Database = d.Name
	pc.ConnConfig.User = d.User
	pc.ConnConfig.Password = password
	pc.ConnConfig.TLSConfig = nil
	pc.ConnConfig.Fallbacks = nil
	pc.ConnConfig.RuntimeParams = map[string]string{
		"search_path":       d.Schema + ",pg_catalog",
		"application_name":  "worktool-isolated-synthetic",
		"statement_timeout": "5000", "lock_timeout": "2000", "idle_in_transaction_session_timeout": "5000",
	}
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	expected := net.JoinHostPort(d.Host, strconv.Itoa(d.Port))
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	pc.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != expected {
			return nil, errors.New("DATABASE_TARGET_REJECTED")
		}
		return dialer.DialContext(ctx, network, address)
	}
	pc.ConnConfig.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
		if ctx.Err() != nil || host != d.Host {
			return nil, errors.New("DATABASE_TARGET_REJECTED")
		}
		return []string{d.Host}, nil
	}
	pc.MaxConns = 2
	pc.MinConns = 0
	pc.MaxConnIdleTime = time.Minute
	pc.MaxConnLifetime = 5 * time.Minute
	pc.HealthCheckPeriod = 30 * time.Second
	return pc, nil
}

type rowScanner interface{ Scan(...any) error }
type queryOne func(context.Context, string, ...any) rowScanner

// Preflight is read-only. It never creates a schema, runtime identity, account,
// secret or default dataset and never repairs a mismatch by rewriting authority.
func preflight(ctx context.Context, c Config, query queryOne) error {
	var database, user, schema string
	var recovery, super, createDB, createRole, replication, bypass, createSchema, member bool
	var version int
	err := query(ctx, `SELECT current_database(),current_user,current_schema(),pg_is_in_recovery(),current_setting('server_version_num')::int,r.rolsuper,r.rolcreatedb,r.rolcreaterole,r.rolreplication,r.rolbypassrls,has_schema_privilege(current_user,current_schema(),'CREATE'),EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid) FROM pg_roles r WHERE r.rolname=current_user`).Scan(&database, &user, &schema, &recovery, &version, &super, &createDB, &createRole, &replication, &bypass, &createSchema, &member)
	if err != nil || database != c.Database.Name || user != c.Database.User || schema != c.Database.Schema || recovery || version != 180006 || super || createDB || createRole || replication || bypass || createSchema || member {
		return errors.New("DATABASE_PREFLIGHT_REJECTED")
	}
	var boot, epoch, state string
	var roles []string
	err = query(ctx, `SELECT boot_id,recovery_epoch,state,roles FROM runtime_instances WHERE instance_id=$1`, c.Runtime.InstanceID).Scan(&boot, &epoch, &state, &roles)
	hasAPI, hasGateway := false, false
	for _, role := range roles {
		if role == "api" {
			hasAPI = true
		}
		if role == "gateway" {
			hasGateway = true
		}
	}
	if err != nil || boot != c.Runtime.BootID || epoch != c.Runtime.RecoveryEpoch || state != "active" || !hasAPI || !hasGateway {
		return errors.New("RUNTIME_PREFLIGHT_REJECTED")
	}
	return nil
}

type httpLifecycle interface {
	Shutdown(context.Context) error
	Close() error
}
type gatewayLifecycle interface {
	Shutdown(context.Context) error
	CleanupErrors() int64
}
type poolLifecycle interface{ Close() }

func shutdown(httpServer httpLifecycle, ws gatewayLifecycle, pool poolLifecycle) error {
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	herr := httpServer.Shutdown(hctx)
	hcancel()
	if herr != nil {
		_ = httpServer.Close()
	}
	gctx, gcancel := context.WithTimeout(context.Background(), 7*time.Second)
	gerr := ws.Shutdown(gctx)
	gcancel()
	// Incomplete joins do not authorize pool closure underneath live DB users.
	// The command exits nonzero; it never reports a clean shutdown in that case.
	if herr != nil || gerr != nil {
		return errors.New("SHUTDOWN_INCOMPLETE_POOL_NOT_CLOSED")
	}
	pool.Close()
	if ws.CleanupErrors() != 0 {
		return errors.New("GATEWAY_DURABLE_CLEANUP_FAILED")
	}
	return nil
}

// Run requires explicit invocation. It is a local synthetic candidate, not a
// public deployment or migration command. Configuration alone grants no access:
// database identity and the externally maintained recovery gate must match.
func Run(ctx context.Context, c Config) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("STARTUP_CANCELLED")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	gate := recoveryGate(c, time.Now)
	if err := gate(ctx); err != nil {
		return err
	}
	pc, err := poolConfig(c)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return errors.New("DATABASE_UNAVAILABLE")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = preflight(checkCtx, c, func(ctx context.Context, q string, args ...any) rowScanner { return pool.QueryRow(ctx, q, args...) })
	cancel()
	if err != nil {
		pool.Close()
		return err
	}
	runner, err := pg.NewRunner(pg.PoolAdapter{Pool: pool}, pg.Timeouts{Acquire: 2 * time.Second, Begin: 2 * time.Second, Transaction: 5 * time.Second, Commit: 2 * time.Second, Cleanup: time.Second})
	if err != nil {
		pool.Close()
		return errors.New("STORE_CONFIGURATION_REJECTED")
	}
	runtime, err := pg.RuntimeAuthorityFromTrusted(c.Runtime.InstanceID, c.Runtime.BootID, c.Runtime.RecoveryEpoch)
	if err != nil {
		pool.Close()
		return errors.New("RUNTIME_CONFIGURATION_REJECTED")
	}
	store, err := pg.NewGatewayStore(runner, gate, runtime)
	if err != nil {
		pool.Close()
		return errors.New("STORE_CONFIGURATION_REJECTED")
	}
	controller, err := gateway.New(store)
	if err != nil {
		pool.Close()
		return errors.New("GATEWAY_CONFIGURATION_REJECTED")
	}
	bearer, err := httpapi.NewRouter(httpapi.Options{Store: store, WebSocket: controller})
	if err != nil {
		pool.Close()
		return errors.New("ROUTER_CONFIGURATION_REJECTED")
	}
	browserStore, err := pg.NewBrowserStore(runner, gate, runtime)
	if err != nil {
		pool.Close()
		return errors.New("BROWSER_STORE_CONFIGURATION_REJECTED")
	}
	browser, err := consoleapi.New(consoleapi.Options{Store: browserStore, Origin: c.BrowserOrigin, Fallback: bearer})
	if err != nil {
		pool.Close()
		return errors.New("BROWSER_CONFIGURATION_REJECTED")
	}
	var draining atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if draining.Load() {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "SERVER_DRAINING", http.StatusServiceUnavailable)
			return
		}
		browser.ServeHTTP(w, r)
	})
	server, err := httpapi.NewServer(c.Listen, handler)
	if err != nil {
		pool.Close()
		return errors.New("LISTENER_CONFIGURATION_REJECTED")
	}
	if err = gate(ctx); err != nil {
		pool.Close()
		return err
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		pool.Close()
		return errors.New("LISTENER_UNAVAILABLE")
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-done:
	}
	draining.Store(true)
	if err = shutdown(server, controller, pool); err != nil {
		return err
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("HTTP_SERVER_FAILED")
	}
	return nil
}
