package pg

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/consoleapi"
	"github.com/jackc/pgx/v5"
)

// BrowserStore exposes only bounded read operations, never browser_sessions or
// device secrets. Password derivation belongs outside the Runner transaction.
type BrowserStore struct {
	runner  *Runner
	gate    func(context.Context) error
	runtime RuntimeAuthority
}

func NewBrowserStore(r *Runner, gate func(context.Context) error, runtime RuntimeAuthority) (*BrowserStore, error) {
	if r == nil || gate == nil || !runtime.trusted {
		return nil, errors.New("browser runner and recovery gate required")
	}
	return &BrowserStore{r, gate, runtime}, nil
}
func (s *BrowserStore) run(ctx context.Context, f func(context.Context, pgx.Tx) error) error {
	if ctx == nil {
		return errors.New("context required")
	}
	if e := s.gate(ctx); e != nil {
		return e
	}
	return s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		if e := s.gate(ctx); e != nil {
			return e
		}
		var boot, epoch, state string
		var capable bool
		e := tx.QueryRow(ctx, `SELECT boot_id,recovery_epoch,state,'api'=ANY(roles) FROM runtime_instances WHERE instance_id=$1`, s.runtime.instance).Scan(&boot, &epoch, &state, &capable)
		if errors.Is(e, pgx.ErrNoRows) {
			return errors.New("browser runtime unavailable")
		}
		if e != nil {
			return e
		}
		if boot != s.runtime.boot || epoch != s.runtime.epoch || state != "active" || !capable {
			return errors.New("browser runtime unavailable")
		}
		if e := f(ctx, tx); e != nil {
			return e
		}
		return s.gate(ctx)
	})
}
func (s *BrowserStore) FindUser(ctx context.Context, tenant, login string) (consoleapi.User, error) {
	return s.user(ctx, tenant, login, false)
}
func (s *BrowserStore) CurrentUser(ctx context.Context, tenant, id string) (consoleapi.User, error) {
	return s.user(ctx, tenant, id, true)
}
func (s *BrowserStore) user(ctx context.Context, tenant, key string, byID bool) (consoleapi.User, error) {
	var out consoleapi.User
	if !boundedID(tenant) || !boundedID(key) {
		return out, consoleapi.ErrNotFound
	}
	e := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		query := `SELECT tenant,id,login,role,password,active FROM users WHERE tenant=$1 AND login=$2 LIMIT 1`
		if byID {
			query = `SELECT tenant,id,login,role,password,active FROM users WHERE tenant=$1 AND id=$2 LIMIT 1`
		}
		err := tx.QueryRow(ctx, query, tenant, key).Scan(&out.Tenant, &out.ID, &out.Username, &out.Role, &out.Password, &out.Active)
		if errors.Is(err, pgx.ErrNoRows) {
			return consoleapi.ErrNotFound
		}
		return err
	})
	if e != nil {
		return consoleapi.User{}, e
	}
	return out, nil
}
func (s *BrowserStore) ListDevices(ctx context.Context, a consoleapi.DeviceAuthority) ([]consoleapi.Device, error) {
	if !boundedID(a.Tenant) || !boundedID(a.ID) || (a.Role != "admin" && a.Role != "operator") {
		return nil, consoleapi.ErrUnauthenticated
	}
	out := []consoleapi.Device{}
	e := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var u consoleapi.User
		e := tx.QueryRow(ctx, `SELECT tenant,id,login,role,password,active FROM users WHERE tenant=$1 AND id=$2 LIMIT 1`, a.Tenant, a.ID).Scan(&u.Tenant, &u.ID, &u.Username, &u.Role, &u.Password, &u.Active)
		if errors.Is(e, pgx.ErrNoRows) {
			return consoleapi.ErrUnauthenticated
		}
		if e != nil {
			return e
		}
		if !u.Active || u.Tenant != a.Tenant || u.ID != a.ID || u.Role != a.Role || sha256.Sum256([]byte(u.Password)) != a.PasswordFingerprint {
			return consoleapi.ErrUnauthenticated
		}
		rows, e := tx.Query(ctx, `SELECT id,name,state,policy,epoch,capabilities FROM devices WHERE tenant=$1 ORDER BY id COLLATE "C" LIMIT 201`, a.Tenant)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var d consoleapi.Device
			if e = rows.Scan(&d.ID, &d.Name, &d.State, &d.PolicyVersion, &d.RevocationEpoch, &d.Capabilities); e != nil {
				return e
			}
			if len(out) >= 200 || len(d.ID) > 128 || len(d.Name) > 1024 || len(d.State) > 64 || len(d.Capabilities) > 32 {
				return errors.New("browser device resource limit")
			}
			for _, c := range d.Capabilities {
				if len(c) > 128 {
					return errors.New("browser capability resource limit")
				}
			}
			if d.Capabilities == nil {
				d.Capabilities = []string{}
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}
