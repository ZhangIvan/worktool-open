package startup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fixtureRow struct {
	values []any
	err    error
}

func (r fixtureRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("field mismatch")
	}
	for i, v := range r.values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}
func TestPreflightReadOnlyRuntimeBinding(t *testing.T) {
	c := configFixture(t)
	base := []any{c.Database.Name, c.Database.User, c.Database.Schema, false, 180006, false, false, false, false, false, false, false}
	runtime := []any{c.Runtime.BootID, c.Runtime.RecoveryEpoch, "active", []string{"api", "gateway"}}
	run := func(first, second []any) error {
		calls := 0
		return preflight(context.Background(), c, func(ctx context.Context, q string, args ...any) rowScanner {
			calls++
			if !strings.HasPrefix(q, "SELECT ") {
				t.Fatal("non-read preflight")
			}
			if calls == 1 {
				if len(args) != 0 {
					t.Fatal("unexpected parameters")
				}
				return fixtureRow{values: first}
			}
			if len(args) != 1 || args[0] != c.Runtime.InstanceID {
				t.Fatal("runtime identity not parameterized")
			}
			return fixtureRow{values: second}
		})
	}
	if err := run(base, runtime); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{3, 5, 6, 7, 8, 9, 10, 11} {
		bad := append([]any(nil), base...)
		bad[index] = true
		if run(bad, runtime) == nil {
			t.Fatal("privileged/recovery identity accepted", index)
		}
	}
	for _, index := range []int{0, 1, 2} {
		bad := append([]any(nil), base...)
		bad[index] = "untrusted"
		if run(bad, runtime) == nil {
			t.Fatal("wrong database identity accepted")
		}
	}
	for _, index := range []int{0, 1, 2} {
		bad := append([]any(nil), runtime...)
		bad[index] = "mismatch"
		if run(base, bad) == nil {
			t.Fatal("wrong runtime accepted")
		}
	}
	bad := append([]any(nil), runtime...)
	bad[3] = []string{"gateway"}
	if run(base, bad) == nil {
		t.Fatal("missing api role accepted")
	}
}

type fakeHTTP struct {
	events *[]string
	err    error
}

func (f fakeHTTP) Shutdown(context.Context) error {
	*f.events = append(*f.events, "http")
	return f.err
}
func (f fakeHTTP) Close() error { *f.events = append(*f.events, "http-close"); return nil }

type fakeGateway struct {
	events  *[]string
	err     error
	cleanup int64
}

func (f fakeGateway) Shutdown(context.Context) error {
	*f.events = append(*f.events, "gateway")
	return f.err
}
func (f fakeGateway) CleanupErrors() int64 { return f.cleanup }

type fakePool struct{ events *[]string }

func (f fakePool) Close() { *f.events = append(*f.events, "pool") }

func TestShutdownJoinsBeforePoolClose(t *testing.T) {
	for _, tc := range []struct {
		name           string
		httpErr, gwErr error
		cleanup        int64
		want           []string
		fail           bool
	}{
		{"clean", nil, nil, 0, []string{"http", "gateway", "pool"}, false},
		{"http incomplete", errors.New("timeout"), nil, 0, []string{"http", "http-close", "gateway"}, true},
		{"gateway incomplete", nil, errors.New("timeout"), 0, []string{"http", "gateway"}, true},
		{"durable cleanup failure", nil, nil, 1, []string{"http", "gateway", "pool"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			err := shutdown(fakeHTTP{&events, tc.httpErr}, fakeGateway{&events, tc.gwErr, tc.cleanup}, fakePool{&events})
			if (err != nil) != tc.fail || !reflect.DeepEqual(events, tc.want) {
				t.Fatal(events, err)
			}
		})
	}
}

func TestCancelledStartupCannotConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Run(ctx, configFixture(t)) == nil {
		t.Fatal("cancelled startup accepted")
	}
}
