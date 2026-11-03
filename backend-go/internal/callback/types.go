// Package callback contains dependency-injected delivery orchestration.
// No default worker, key provider, DNS service or network task is registered.
package callback

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

var (
	ErrCommitUnknown   = errors.New("COMMIT_OUTCOME_UNKNOWN")
	ErrStale           = errors.New("STALE_LEASE")
	ErrDenied          = errors.New("AUTHORITY_DENIED")
	ErrTransportPolicy = errors.New("TRANSPORT_POLICY_DENIED")
)

type Handle struct {
	Tenant, DeliveryID, Owner, Recovery string
	Fence                               int64
	Attempt, MaxAttempts                int
}

func (Handle) String() string { return "CallbackHandle(redacted)" }

type Binding struct {
	Tenant, Destination, URL, KeyRef string
	PolicyVersion, KeyVersion        int64
}

func (Binding) String() string { return "CallbackBinding(redacted)" }

type Delivery struct {
	Handle                              Handle
	Binding                             Binding
	EventID, Digest                     string
	Body                                []byte
	LeaseUntil, HardDeadline, ExpiresAt time.Time
}

func (Delivery) String() string { return "CallbackDelivery(redacted)" }

// Prepared is the exact authorization assertion the trusted store must compare.
// It includes no key material. Deadline cannot exceed any captured authority.
type Prepared struct {
	Handle          Handle
	Binding         Binding
	EventID, Digest string
	IP              netip.Addr
	Deadline        time.Time
}

type Permit struct {
	Prepared Prepared
	Fresh    bool // false means no new send permission, even for an exact replay
}

// Completion carries no receiver body, URL, raw error or secret. State is ready,
// delivered, dead_letter or cancelled. Outcome records the attempt, not effects.
type Completion struct {
	Handle               Handle
	Outcome, State, Code string
	Status               int
	NextAt               time.Time
}

type Store interface {
	Claim(context.Context) (Delivery, bool, error)
	Prepare(context.Context, Prepared) (Permit, error)
	Complete(context.Context, Completion) error
}

type Clock interface {
	Now() time.Time
	// Deadline must preserve cancellation and provide a monotonic deadline. An
	// injected fake may advance it deterministically; adapters must honor ctx.
	Deadline(context.Context, time.Time) (context.Context, context.CancelFunc)
}

// LocalGate distinguishes explicit ErrDenied revocation, ErrStale generation
// loss, and temporary/context failures. Temporary closure is not cancellation of
// the delivery itself. It must neither do network work nor mutate the Handle.
type LocalGate interface {
	Check(context.Context, Handle) error
}
type DNS interface {
	Resolve(context.Context, string) ([]netip.Addr, error)
}

type Key struct {
	Binding   Binding
	Bytes     []byte
	ExpiresAt time.Time
}

func (Key) String() string { return "CallbackKey(redacted)" }

type KeyProvider interface {
	// Resolve transfers ownership of returned Bytes, including on error. The
	// orchestrator clears them before returning. Never generate a fallback key.
	Resolve(context.Context, Binding) (Key, error)
}

type Request struct {
	URL, Host                     string
	IP                            netip.Addr
	Method, ContentType           string
	ContentLength                 int
	EventID, Timestamp, Signature string
	Body                          []byte
}

func (Request) String() string { return "CallbackRequest(redacted)" }

type Response struct{ Status int }
type Transport interface {
	// Send performs at most one POST to the supplied literal IP:443, retaining
	// Host/SNI. No proxy, redirect, retry or new DNS lookup. It owns the body copy
	// only for this call and must not mutate it or retain it afterward.
	Send(context.Context, Request) (Response, error)
}

// Policy is trusted reviewed configuration, never a request-provided allowlist.
type Policy struct {
	Binding Binding
	CIDRs   []string
}

type Dependencies struct {
	Store     Store
	Clock     Clock
	Gate      LocalGate
	DNS       DNS
	Keys      KeyProvider
	Transport Transport
}

// Result intentionally carries only bounded classifications.
type Result struct {
	Claimed, TransportInvoked, Reconcile bool
	State, Outcome, Code                 string
}
