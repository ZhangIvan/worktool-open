package runtimeassembly

import (
	"context"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"reflect"
)

// ExactKey transfers Key.Bytes even on error. Descriptor metadata must describe
// the exact request. Equality does not authenticate a provider or its revision.
type ExactKey struct {
	Descriptor registration.Value
	Key        cb.Key
}

func (ExactKey) String() string   { return "ExactCallbackKey(redacted)" }
func (ExactKey) GoString() string { return "ExactCallbackKey(redacted)" }

type ExactKeyResolver interface {
	ResolveExact(context.Context, registration.Value) (ExactKey, error)
}
type exactKeys struct {
	snapshot *bindingSnapshot
	resolver ExactKeyResolver
}

func (k exactKeys) Resolve(ctx context.Context, b cb.Binding) (key cb.Key, err error) {
	defer func() {
		if recover() != nil {
			key = cb.Key{}
			err = ErrKey
		}
	}()
	v, ok := k.snapshot.exact[b]
	if !ok {
		return cb.Key{}, ErrKey
	}
	// Do not expose the snapshot's backing CIDR slice to an adapter.
	request := v
	request.CIDRs = append([]string(nil), v.CIDRs...)
	result, err := k.resolver.ResolveExact(ctx, request)
	if err != nil || !reflect.DeepEqual(result.Descriptor, v) || result.Key.Binding != b {
		return result.Key, ErrKey
	}
	return result.Key, nil
}
