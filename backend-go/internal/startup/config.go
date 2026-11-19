// Package startup composes the isolated synthetic server. Nothing connects or
// listens until Run is explicitly called with a validated private configuration.
package startup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
)

const Mode = "isolated-synthetic"

type Database struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Name         string `json:"name"`
	User         string `json:"user"`
	Schema       string `json:"schema"`
	PasswordFile string `json:"passwordFile"`
}

type Runtime struct {
	InstanceID    string `json:"instanceId"`
	BootID        string `json:"bootId"`
	RecoveryEpoch string `json:"recoveryEpoch"`
}

type Config struct {
	Version          int      `json:"version"`
	Mode             string   `json:"mode"`
	Listen           string   `json:"listen"`
	BrowserOrigin    string   `json:"browserOrigin"`
	Database         Database `json:"database"`
	Runtime          Runtime  `json:"runtime"`
	RecoveryGateFile string   `json:"recoveryGateFile"`
}

func (Config) String() string { return "StartupConfig(redacted)" }

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
var databaseName = regexp.MustCompile(`^wt_test_[a-f0-9]{8,64}$`)
var schemaName = regexp.MustCompile(`^wt_(?:it|test|console)_[a-f0-9]{8,64}$`)

func privatePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func loopbackAddress(address string) bool {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(rawPort)
	return err == nil && ip != nil && ip.IsLoopback() && port >= 1024 && port <= 65535 && strconv.Itoa(port) == rawPort
}

func (c Config) Validate() error {
	if c.Version != 1 || c.Mode != Mode || !loopbackAddress(c.Listen) {
		return errors.New("INVALID_STARTUP_CONFIG")
	}
	u, err := url.Parse(c.BrowserOrigin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || !loopbackAddress(u.Host) || u.String() != c.BrowserOrigin {
		return errors.New("INVALID_BROWSER_ORIGIN")
	}
	d := c.Database
	ip := net.ParseIP(d.Host)
	if ip == nil || !ip.IsLoopback() || d.Port < 1024 || d.Port > 65535 || !databaseName.MatchString(d.Name) || !schemaName.MatchString(d.Schema) || !identifier.MatchString(d.User) {
		return errors.New("ISOLATED_DATABASE_REQUIRED")
	}
	if !identifier.MatchString(c.Runtime.InstanceID) || !identifier.MatchString(c.Runtime.BootID) || !identifier.MatchString(c.Runtime.RecoveryEpoch) {
		return errors.New("INVALID_RUNTIME_IDENTITY")
	}
	if !privatePath(d.PasswordFile) || !privatePath(c.RecoveryGateFile) || d.PasswordFile == c.RecoveryGateFile {
		return errors.New("PRIVATE_FILE_PATH_REQUIRED")
	}
	return nil
}

// readPrivate rejects symlinks, broadly readable files and a replacement race.
// It never includes path, contents or underlying OS errors in returned errors.
func readPrivate(path string, limit int64) ([]byte, error) {
	if !privatePath(path) || limit <= 0 {
		return nil, errors.New("PRIVATE_FILE_REQUIRED")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > limit {
		return nil, errors.New("PRIVATE_FILE_REQUIRED")
	}
	f, err := openPrivate(path)
	if err != nil {
		return nil, errors.New("PRIVATE_FILE_UNAVAILABLE")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 {
		return nil, errors.New("PRIVATE_FILE_CHANGED")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("PRIVATE_FILE_INVALID")
	}
	return b, nil
}

func decodeStrict(raw []byte, target any) error {
	if _, err := protocol.Decode(raw, 32768); err != nil {
		return errors.New("INVALID_CONFIG_JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("INVALID_CONFIG_JSON")
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := readPrivate(path, 32768)
	if err != nil {
		return c, err
	}
	if err = decodeStrict(b, &c); err != nil {
		return Config{}, err
	}
	if err = c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

type gateDocument struct {
	Version       int       `json:"version"`
	Mode          string    `json:"mode"`
	Runtime       Runtime   `json:"runtime"`
	AdmissionOpen bool      `json:"admissionOpen"`
	ValidUntil    time.Time `json:"validUntil"`
}

// recoveryGate is a local, externally maintained fail-closed interlock, not an
// automatic runtime registrar. Missing/expired/closed/mismatched files deny work.
func recoveryGate(c Config, now func() time.Time) func(context.Context) error {
	return func(ctx context.Context) error {
		if ctx == nil || ctx.Err() != nil {
			return errors.New("RECOVERY_GATE_CLOSED")
		}
		b, err := readPrivate(c.RecoveryGateFile, 4096)
		var g gateDocument
		if err != nil || decodeStrict(b, &g) != nil {
			return errors.New("RECOVERY_GATE_CLOSED")
		}
		t := now()
		if g.Version != 1 || g.Mode != Mode || g.Runtime != c.Runtime || !g.AdmissionOpen || !g.ValidUntil.After(t) || g.ValidUntil.After(t.Add(5*time.Minute)) {
			return errors.New("RECOVERY_GATE_CLOSED")
		}
		return nil
	}
}
