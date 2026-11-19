# Closed callback runtime ownership primitive

This package implements reusable runtime mechanisms, not production callback
startup. It is not imported by startup and has no flag, environment switch,
HTTP route, pool constructor, credential provisioner or network implementation.

## Three distinct claims

1. Actual code implements private component ownership, full content comparison,
   exact key routing, a shared one-way local generation latch, single-use
   Start/Stop, pending-join dependency retention and fixed redacted health.
2. Same-package tests issue a synthetic launch capability before construction,
   then exercise this actual owner, Worker, Sender, CallbackStore and Runner with
   six strictly scripted pgx transactions and a no-socket transport. This proves
   Go control flow and expected SQL/argument sequences, not real SQL effects,
   persistence, restart safety, authenticated providers or production readiness.
3. Trusted production acquisition, complete durable identity/writer exclusion,
   immutable exact-version secret authority, UTC health, approved DNS/egress,
   actual worker role, durable halt/reconciliation, joint lifecycle ownership and
   receiver/deployment acceptance remain unavailable. All nine planner blockers
   are unchanged. Parser acceptance or matching identity cannot clear them.

## Construction and capability boundary

NewClosed validates/copies a plan, exact registration descriptors and runtime
instance/boot/epoch content. It constructs private actual Store/Sender/Worker
instances without calling adapters. It always supplies a closed lease and no
launch capability. Every production-constructible Owner.Start therefore fails
closed. There is no production issuer, Open/Reset, token decoder, provider-returned
READY value or successful alternate constructor. Only `_test.go` initializes a
live lease before private construction. Future issuance requires reviewed code
changes, authentic providers and durable conditional ownership, not a config edit.

A private Go type is containment, not a security boundary against malicious
trusted source changes, unsafe/reflection, or same-package code. A caller retaining
adapter references can violate the exclusive ownership contract. This package
cannot enforce external resource isolation.

Construction failure transfers no resources and invokes no provider/release
callback. Success transfers the supplied dependency bundle exclusively to Owner.
Do not share its Runner/pool, Sender/store, transport or key dependencies with
another independent loop. There is no exported component accessor or Store override.
NewClosed only checks declared budgets; an existing Runner's real pool/budget and
role provenance remain an independent prerequisite.

Owner is a small copyable handle to private synchronized state. Copying it shares
that same lifecycle rather than creating another owner. Nil and zero values fail
closed and are safe to stop/inspect. Default value and pointer formatting are
redacted; deliberate reflection/serialization of private data is not a secrecy
boundary. Health has only fixed classifications, never URLs, keys, identifiers,
body bytes, SQL, raw provider errors or arbitrary adapter codes.

## Full content projection and keys

Every plan destination must correspond to exactly one descriptor, including app,
versions, URL/CIDRs, key reference, profile and revision. The descriptor preserves
provider authority namespace, absent from schema-1 plan. The captured identity
binds plan identity, all descriptor identities and runtime instance/boot/epoch.
The same runtime tuple constructs the existing pg RuntimeAuthority and local gate.
This validates content equality only, not origin, approval or durable immutability.

The cb.Binding projection lacks app/provider namespace/revision. The exact-key
adapter therefore looks up its entire frozen descriptor before calling the
injected ResolveExact. Returned descriptor metadata and cb.Binding must both match.
There is no fallback to key_ref, latest/current aliases, or another revision.
Request CIDRs are copied so providers cannot alter the frozen snapshot. Bytes
returned even with an error/metadata rejection remain owned by Sender and are
cleared by it. A provider panic is redacted as unconfirmed; bytes never returned
remain the provider's responsibility. No production resolver is implemented.
Current DB app checks cannot authenticate the plan's app or complete identity;
that unresolved durable registry/provider connection is not bypassed here.

## One-way generation and lifecycle

The private lease is shared by actual Store and Sender. Closed is terminal. Local
checks compare captured identity and available Handle tenant/owner/epoch; Handle
has no app, boot or descriptor identity, so those are not claimed revalidated from
it. Cancellation and an explicitly elapsed deadline reject even if a delayed
context timer has not yet populated Err. Generation loss is stale/reconciliation,
not explicit delivery cancellation. No new object is itself reconciliation.

Start reserves once, checks the private capability and lease, calls its test-only
acquisition model outside the mutex, then rechecks closure/deadline before starting
the actual Worker. Rejected/late acquisition cannot claim Running. A failed Start
consumes the instance, closes locally and still requires Stop for owned-resource
cleanup. Failed or interrupted acquisition remains a bounded latched halt.

Stop closes the lease first, cancels and joins the actual Worker. An in-flight
send can already have effects; revocation is not an instantaneous network fence.
Only actual join proves no future loop work. A pending Stop retains every owned
dependency and ownership record; call Stop again. Concurrent/reentrant Stop while
another owner operation is reserved returns pending rather than self-waiting.
No owner mutex is held during Worker/provider/record/release calls. There are no
abandoned operation goroutines or forced resource closures.

After join, Stop reads Worker halt evidence, records an exact identity/code in
the private test-model sink, and releases resources at most once. The sink has an
independent one-second cooperative context. Error, panic, wrong/missing receipt,
late return or cancelled context is unconfirmed, never a cleared incident. No
retry of that record occurs. No production storage implementation is provided.
Release happens only after actual join; successful release is independent of
whether the durable outcome is known. Release uses a fresh at-most-one-second cooperative context, capped by the Stop
caller. A late or context-closed return is unconfirmed as well. Release error/panic permanently latches
RELEASE_UNCONFIRMED, ResourcesReleased stays false and all later Stop calls are
nonnil without repeating release. A noncooperative callback can block its current
caller indefinitely; other Stop calls report pending and resources remain owned.
Stop nil does not certify durable settlement: inspect the latched halt/record code.

A natural Worker halt is reflected in Health. Closure/recording is performed by
explicit Stop, not an unimplemented background persistence monitor. This makes
future prewritten durable running intent essential to cover crashes and the gap
before Stop records anything. The test journal demonstrates only process-local
ordering and never clears occupied scope, even for new config/epoch/owner values.
A new journal or process is not restart evidence. Production still has no issuer.

## Future durable authority and manual reconciliation

A production authority must atomically acquire a pre-approved allocation and
persist a running intent under a stable deployment/worker-partition scope before
issuing a capability. That scope must not be keyed by changing plan hash, epoch or
allocation ID. Those values are immutable record contents, so changing them cannot
hide an occupied/halted scope. Storage must be authenticated, revision-fenced and
protected from unmediated writers, including restore/history provenance.

Persistent uncertainty recording must close locally first and condition durable
writes on the exact allocation/binding/revision. Failed/late/lost acknowledgements
must keep the scope blocked. A clean joined receipt cannot erase an incident.
State survives worker objects/processes; missing history and interrupted running
intent prohibit new issuance. No such backend/schema/privileges are added here.

Manual reconciliation examines exact allocation/config/epoch/owner, immutable
claim/prepare/attempt/slot/audit graph, cleanup evidence and possible receiver
effects. No completion row does not prove no receiver effect. Read-only observation
can confirm an exact historical settlement, never a send grant. Old resources must
be joined or independently fenced. An authenticated immutable operator decision
and receiver/deduplication disposition can permit a separately reviewed **new**
allocation; the old gate never reopens. Cross-generation/PITR and incomplete
history remain blocked without their own provenance/recovery design. There is no
operator CLI, automatic retry/repair, halt-clear or deployment path in this slice.

## Verification boundary

The full-chain test performs empty recovery selection, Claim scan, Claim mutation,
Prepare, scripted retry commit/lost ACK and observation. Complete SQL digests and
argument lists reject unknown operations. The observation's actual Runner cleanup
holds a bounded barrier while Owner.Stop closes the shared latch and reports
pending. Release then demonstrates actual post-cleanup rejection, Worker halt,
no further claim/send, no observation business DML and joined resource release.
All commit/snapshot evidence is scripted, not durable PostgreSQL. No PG, DNS,
socket, HTTP listener, production callback, CI, device, deployment or publication
is exercised by these tests.
