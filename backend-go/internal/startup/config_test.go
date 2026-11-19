package startup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func configFixture(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{Version: 1, Mode: Mode, Listen: "127.0.0.1:3101", BrowserOrigin: "http://127.0.0.1:5173", Database: Database{Host: "127.0.0.1", Port: 5432, Name: "wt_test_0123456789abcdef", User: "wt_store", Schema: "wt_console_0123456789abcdef", PasswordFile: filepath.Join(dir, "password")}, Runtime: Runtime{InstanceID: "instance_test", BootID: "boot_test", RecoveryEpoch: "epoch_test"}, RecoveryGateFile: filepath.Join(dir, "gate.json")}
}

func TestConfigurationRejectsUnboundedTargets(t *testing.T) {
	if err := configFixture(t).Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"mode", func(c *Config) { c.Mode = "production" }},
		{"version", func(c *Config) { c.Version = 2 }},
		{"public bind", func(c *Config) { c.Listen = "0.0.0.0:3101" }},
		{"dns bind", func(c *Config) { c.Listen = "localhost:3101" }},
		{"random port", func(c *Config) { c.Listen = "127.0.0.1:0" }},
		{"origin dns", func(c *Config) { c.BrowserOrigin = "http://localhost:5173" }},
		{"origin credentials", func(c *Config) { c.BrowserOrigin = "http://user@127.0.0.1:5173" }},
		{"origin path", func(c *Config) { c.BrowserOrigin = "http://127.0.0.1:5173/" }},
		{"origin query", func(c *Config) { c.BrowserOrigin = "http://127.0.0.1:5173?x" }},
		{"public db", func(c *Config) { c.Database.Host = "192.0.2.1" }},
		{"dns db", func(c *Config) { c.Database.Host = "localhost" }},
		{"production db", func(c *Config) { c.Database.Name = "production" }},
		{"public schema", func(c *Config) { c.Database.Schema = "public" }},
		{"schema injection", func(c *Config) { c.Database.Schema = "wt_console_01234567,public" }},
		{"runtime missing", func(c *Config) { c.Runtime.BootID = "" }},
		{"relative secret", func(c *Config) { c.Database.PasswordFile = "secret" }},
		{"relative gate", func(c *Config) { c.RecoveryGateFile = "gate" }},
		{"shared gate and secret", func(c *Config) { c.RecoveryGateFile = c.Database.PasswordFile }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := configFixture(t)
			tc.change(&c)
			if c.Validate() == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func TestPrivateConfigParsing(t *testing.T) {
	c := configFixture(t)
	path := filepath.Join(t.TempDir(), "config.json")
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil || got != c {
		t.Fatal("valid private config rejected", err)
	}
	for _, bad := range []string{string(raw) + "{}", strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(raw), `"version":1`, `"version":1,"unknown":true`, 1), strings.Repeat("x", 32769)} {
		if err = os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = LoadConfig(path); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(path); err == nil {
		t.Fatal("public config accepted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(link); err == nil {
		t.Fatal("symlink config accepted")
	}
}

func TestRecoveryGateFailClosed(t *testing.T) {
	c := configFixture(t)
	now := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	gate := recoveryGate(c, func() time.Time { return now })
	if gate(context.Background()) == nil {
		t.Fatal("absent gate accepted")
	}
	valid := gateDocument{Version: 1, Mode: Mode, Runtime: c.Runtime, AdmissionOpen: true, ValidUntil: now.Add(time.Minute)}
	write := func(g gateDocument) {
		t.Helper()
		b, _ := json.Marshal(g)
		if err := os.WriteFile(c.RecoveryGateFile, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(valid)
	if err := gate(context.Background()); err != nil {
		t.Fatal(err)
	}
	cases := []func(*gateDocument){func(g *gateDocument) { g.AdmissionOpen = false }, func(g *gateDocument) { g.Runtime.RecoveryEpoch = "other_epoch" }, func(g *gateDocument) { g.Runtime.BootID = "other_boot" }, func(g *gateDocument) { g.ValidUntil = now }, func(g *gateDocument) { g.ValidUntil = now.Add(5*time.Minute + time.Second) }, func(g *gateDocument) { g.Mode = "production" }}
	for _, change := range cases {
		g := valid
		change(&g)
		write(g)
		if gate(context.Background()) == nil {
			t.Fatal("invalid gate accepted")
		}
	}
	write(valid)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if gate(ctx) == nil {
		t.Fatal("cancelled gate accepted")
	}
	if err := os.Chmod(c.RecoveryGateFile, 0644); err != nil {
		t.Fatal(err)
	}
	if gate(context.Background()) == nil {
		t.Fatal("public gate accepted")
	}
}

func TestPoolConfigurationIsPinnedWithoutConnecting(t *testing.T) {
	c := configFixture(t)
	if err := os.WriteFile(c.Database.PasswordFile, []byte("synthetic-only-password-32-bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGHOST", "external.invalid")
	t.Setenv("PGDATABASE", "production")
	t.Setenv("PGOPTIONS", "-c search_path=public")
	p, err := poolConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxConns != 2 || p.MinConns != 0 || p.ConnConfig.Host != c.Database.Host || p.ConnConfig.Database != c.Database.Name || p.ConnConfig.Fallbacks != nil || p.ConnConfig.TLSConfig != nil || p.ConnConfig.RuntimeParams["search_path"] != c.Database.Schema+",pg_catalog" {
		t.Fatal("ambient or unbounded pool configuration")
	}
	if _, err = p.ConnConfig.DialFunc(context.Background(), "tcp", "192.0.2.1:5432"); err == nil {
		t.Fatal("foreign dial allowed")
	}
	if _, err = p.ConnConfig.LookupFunc(context.Background(), "external.invalid"); err == nil {
		t.Fatal("foreign lookup allowed")
	}
}
