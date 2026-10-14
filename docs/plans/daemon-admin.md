# Daemon HTTP adapter: incremental plan

## Initial read-only slice

Establish the first incremental slice of the same intended long-running daemon
server, not a separate service: add a foreground HTTP entry point with graceful
context/signal shutdown. It serves existing liveness/readiness checks, a
server-rendered `/admin` page, and read-only JSON access to saved mappings and
the existing pending-worklist core operation. Provider calls occur only when a
client explicitly requests pending data; rendering the admin page and reading
mappings do not contact providers. The adapter calls shared core operations,
never CLI command execution or copied business logic.

The listener binds to loopback by default. Any non-loopback listener must be
explicitly configured as a concrete address; wildcard addresses are rejected
because the request Host allowlist needs a specific configured host. The HTTP
boundary validates `Host` against the
configured listener and rejects browser Origins/cross-site requests, including
for read routes, because unauthenticated local HTTP is exposed to
DNS-rebinding and CSRF risks. No wildcard CORS, auth framework, state mutations
or provider writes are included. Requests are bounded and methods are explicit.
Logs must not include credentials, private metadata or request payloads.

## Initial-slice acceptance

- `drifter serve` starts a persistent foreground HTTP server, uses configured
  database defaults consistently, and shuts down gracefully on cancellation or
  process signals; existing CLI commands and `serve-probes` remain compatible.
- Existing health/readiness routes work on the same server; readiness reflects
  the local mapping database and does not invent synchronization state.
- `/admin` renders a minimal useful page without provider calls. Read-only JSON
  routes expose stored mappings and pending results only through
  `companion.PendingWorklist` and other existing core APIs; pending provider
  work is request-triggered.
- Tests exercise real HTTP handlers/lifecycle/security decisions and core
  integration with synthetic providers and temporary SQLite only. No live
  accounts or credentials are used.
- Configuration and CLI docs describe listener safety and this bounded
  capability. No diagram is generated; the existing workflow diagram still
  describes mapping CLI workflow, while daemon access adds no new business
  workflow.

## Next bounded slice: ownership and lifecycle

Before adding HTTP mutations, the daemon must own one database exclusively.
Use stdlib `flock` on macOS/Linux on a stable sidecar lock. Resolve the
database parent through symlinks before choosing the sidecar, continue
rejecting a symlink database file, and reject hard-linked database files so an
alternate inode path cannot bypass the lease. Never remove an apparently stale
lock file: the kernel releases the lock when its process exits. Unsupported
operating systems fail explicitly rather than silently running without
ownership enforcement.

Daemon startup acquires ownership before opening/migrating state and holds it
through HTTP shutdown. Ordinary store initialization/migration and each
standalone write take a nonblocking writer guard. CLI `link` checks for an
existing owner before provider work and store writes recheck under the guard.
Read-only CLI operations may fall back to a read-only SQLite connection only
when an owner holds the guard and the existing schema is already current; they
must never initialize or migrate under that owner. Core exposes an ownership
capability for daemon-owned stores and confirmation so future HTTP mutations
reuse normal validation while retaining the daemon lease.

Acceptance for this slice: subprocess tests prove one owner, refusal of CLI
link/unlink during ownership, safe alias behavior, release after process exit,
daemon-owned core writes, read-only access without migration, and unchanged
rejected-link/no-state behavior. No HTTP mutation routes are added here.

## Final bounded slice: mapping maintenance

Expose explicit confirm, replace and unlink through server-rendered native
forms and strict JSON routes, backed only by `ConfirmLinkOwned` and the owned
store's validated `Unlink`. Keep new confirmation, replacement and unlink as
distinct actions. Both interfaces require explicit source/target namespaces,
book/work IDs, and a deliberate work-only versus edition choice. A random
process token protects mutation requests; it is CSRF protection, not
authentication. JSON writes require `application/json`, bounded strict decoding,
a same-origin Origin, Host allowlisting and the token. Native forms use distinct
bounded URL-encoded routes with the same Origin/Host defenses and hidden token.
GET/HEAD never mutate. Do not log token or raw provider/store errors.

Acceptance: real-core HTTP/temp-SQLite tests cover rejected editions, idempotent
confirmation, conflict versus explicit replacement, scoped/idempotent unlink,
and malformed or cross-site requests rejected before provider calls/writes. A
synthetic browser journey confirms, replaces and unlinks persisted mappings.
No diagram is generated; the CLI mapping diagram remains a general domain flow,
not an HTTP contract.

## Explicitly deferred

Cross-process ownership and writer locking are implemented as the lifecycle
foundation above. Also excluded: import/export, hints, decisions,
synchronization, polling, run history, holds, daemonization, deployment and an
authentication framework. This server does not authorize provider status writes.
