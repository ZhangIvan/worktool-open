package pg

import (
	"errors"
	"fmt"
)

type RolePool struct {
	Role               string
	Replicas, MaxConns int64
}
type PoolBudget struct {
	MaxConnections, SystemReserve, OperationalReserve int64
	Roles                                             []RolePool
}

// Validate returns the total provisioned application+reserve connections. Each
// entry represents a distinct pool allocation: a shared pool must be listed once,
// not once per role. Admission is deployment-wide, not a process-local guess.
func (b PoolBudget) Validate() (int64, error) {
	if b.MaxConnections <= 0 || b.SystemReserve < 0 || b.OperationalReserve < 0 || len(b.Roles) == 0 {
		return 0, errors.New("invalid pool budget")
	}
	remaining := b.MaxConnections
	for _, reserve := range []int64{b.SystemReserve, b.OperationalReserve} {
		if reserve > remaining {
			return 0, errors.New("reserves exceed connection budget")
		}
		remaining -= reserve
	}
	seen := map[string]bool{}
	for _, r := range b.Roles {
		if r.Role != "api" && r.Role != "gateway" && r.Role != "worker" {
			return 0, fmt.Errorf("invalid role %q", r.Role)
		}
		if seen[r.Role] {
			return 0, errors.New("duplicate role allocation")
		}
		seen[r.Role] = true
		if r.Replicas <= 0 || r.MaxConns <= 0 || r.MaxConns > 2147483647 {
			return 0, errors.New("invalid replicas or max connections")
		}
		// Division before multiplication prevents int64 overflow.
		if r.Replicas > remaining/r.MaxConns {
			return 0, errors.New("role pools exceed connection budget")
		}
		remaining -= r.Replicas * r.MaxConns
	}
	return b.MaxConnections - remaining, nil
}
