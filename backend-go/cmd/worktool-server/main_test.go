package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/startup"
)

func TestCommandRequiresExplicitModeAndRedactsErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"--serve"}, {"--serve", "--check-config", "--config", "/not-used"}, {"--unknown=secret-do-not-log"}, {"--serve=secret-do-not-log"}} {
		var out, diagnostic bytes.Buffer
		code := execute(context.Background(), args, &out, &diagnostic, func(context.Context, startup.Config) error { t.Fatal("unexpected serve"); return nil })
		if code != 2 || strings.Contains(out.String()+diagnostic.String(), "secret-do-not-log") {
			t.Fatal("argument leak or implicit serve")
		}
	}
}

func TestConfigOnlyDoesNotConnect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{"version":1,"mode":"isolated-synthetic","listen":"127.0.0.1:3101","browserOrigin":"http://127.0.0.1:5173","database":{"host":"127.0.0.1","port":5432,"name":"wt_test_0123456789abcdef","user":"wt_store","schema":"wt_console_0123456789abcdef","passwordFile":"/not-read/password"},"runtime":{"instanceId":"instance_test","bootId":"boot_test","recoveryEpoch":"epoch_test"},"recoveryGateFile":"/not-read/gate.json"}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	calls := 0
	serve := func(context.Context, startup.Config) error { calls++; return errors.New("dsn-secret-must-not-escape") }
	if code := execute(context.Background(), []string{"--config", path, "--check-config"}, &out, &diagnostic, serve); code != 0 || calls != 0 || !strings.Contains(out.String(), "NOT_RUN") {
		t.Fatal("config check performed external work")
	}
	out.Reset()
	diagnostic.Reset()
	if code := execute(context.Background(), []string{"--config", path, "--serve"}, &out, &diagnostic, serve); code != 1 || calls != 1 || strings.Contains(diagnostic.String(), "dsn-secret") {
		t.Fatal("serve result/error leak")
	}
}
