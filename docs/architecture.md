# Drifter architecture

Drifter is a local companion between WeRead reading data and Hardcover catalog
identities. The current product is a CLI plus one foreground HTTP server and a
shared Go core. Both interfaces read the same private SQLite mapping store.
Provider clients are read-only: a mapping does not authorize status updates.
Kindle Unlimited intake, status synchronization and deployment are not
implemented.

## Components

| Component | Location | Responsibility |
| --- | --- | --- |
| CLI and wiring | `cmd/drifter` | Parse commands, load settings, construct providers lazily, own foreground server lifecycle, emit JSON and structured logs. |
| Shared feature core | `internal/companion/operations.go`, `pending.go` | Source/target contracts, matching, explicit confirmation, pending selection, account-scoped local operations and owned writes. |
| WeRead client | `internal/companion/weread.go` | Read-only shelf, search, book metadata and explicitly requested personal progress. |
| Hardcover client | `internal/companion/hardcover.go` | Read-only edition lookup, bounded candidate search and work/edition membership validation. |
| Provider transport | `internal/companion/http.go` | Fixed HTTPS endpoints, no redirects, bounded responses, timeouts, per-client pacing and sanitized errors. |
| Mapping store and ownership | `internal/companion/store.go`, `ownership*.go` | Versioned SQLite, private-file validation, schema initialization, confirmed mappings, sidecar process lock and owned-operation lifetime. |
| Local HTTP server | `internal/httpserver` | Foreground listener, server-rendered mapping interface, request boundary, read routes and explicit mapping mutations through shared core. |
| Configuration | `internal/config` | Settings-only TOML with full validation, unknown-key rejection and refusal of credential-like settings. |
| Logging and probes | `internal/logging`, `internal/probes` | JSON `slog` output and local `/healthz`/`/readyz` checks. |

## Domain and authority

A source book is identified inside a source account namespace. A target work may
have multiple editions. `internal/companion` owns validation and mapping
operations; adapters decode inputs, choose outputs and manage lifetimes. The
HTTP server does not call the CLI, and the CLI does not make HTTP requests to
itself.

The owner selects source account/book, target account/work and either work-only
or a specific edition. The core fetches the source book and validates target
existence and edition membership before storing. This proves those records
exist and are related by the provider; it does not prove literary identity.
Search results, ISBN uniqueness, or ranking are candidates, never a mapping.
Replacement must be requested explicitly. Unlink is scoped by both account
labels and source book ID. Labels are user-chosen namespaces, not authenticated
provider account IDs.

`PendingWorklist` reads the selected source shelf, filters private/activity and
already-mapped rows before enrichment, and reports completeness/issues. It does
not contact Hardcover or write a mapping. Personal progress is fetched only by
the explicit inspect option and is not stored. Provider status writes and
synchronization do not exist.

## Data flow

The [workflow diagram](diagrams/cli-mapping-workflow.html) remains focused on
the CLI pending/matching/confirmation sequence and does not yet show the HTTP
interface. The [HTTP user-story/test map](plans/http-server.md) describes the
current server behavior and acceptance evidence.

```text
CLI command ───────┐
                   ├──▶ shared companion operations ──▶ WeRead read client
HTTP request ──────┘                  │
                                      ├──▶ Hardcover read client
                                      └──▶ private SQLite mappings
```

Matching performs bounded exact-edition lookup first, then bounded candidate
search; the operator reviews and confirms. Pending reads use WeRead and SQLite
only. The local page and mapping list read SQLite only; provider construction
is lazy and shared by each server instance.

## CLI and HTTP interfaces

`drifter serve` owns one foreground HTTP process. The default bind is
`127.0.0.1:8081`; wildcard binds are rejected. The root `GET /` serves an
embedded server-rendered mapping page. It uses native forms and no browser
JavaScript asset or frontend build. The page and saved-mapping reads do not
construct or call providers. Provider-backed requests happen only when a user
explicitly asks for pending data or confirms/replaces a mapping.

| Method and route | Behavior |
| --- | --- |
| `GET /`, `HEAD /` | Mapping interface; local store only. |
| `GET /api/mappings`, `HEAD /api/mappings` | Read-only mapping list. |
| `GET /api/pending?account=…&target_account=…[&days=N]` | Explicit WeRead-backed pending request. |
| `GET /api/csrf` | Return the process token for a trusted same-origin JSON client. |
| `POST /api/mappings/confirm` | Strict JSON explicit confirmation. |
| `POST /api/mappings/replace` | Strict JSON explicit replacement. |
| `DELETE /api/mappings/unlink` | Strict JSON account-scoped unlink. |
| `POST /mappings/confirm` | Bounded URL-encoded native form confirmation; success redirects to `/`. |
| `POST /mappings/replace` | Bounded URL-encoded native form replacement; success redirects to `/`. |
| `POST /mappings/unlink` | Bounded URL-encoded native form unlink; success redirects to `/`. |
| `GET /healthz`, `/readyz` | Local liveness/readiness checks. |

JSON confirmation/replacement requires source account, source book ID, target
account, target work ID and an explicit edition choice (`edition_id: 0` means
work-only; a positive ID selects an edition). Form actions require the same
explicit work-only or specific-edition choice. The HTTP adapter delegates
validation, conflict rules and writes to the same shared core as CLI linking;
there is no parallel mapping policy in the handler.

## Browser and request boundary

The server checks Host against its configured listener and rejects cross-origin
Origin and cross-site Fetch Metadata on browser requests. Mutation JSON must
use `application/json`, stay within the body bound, decode strictly (unknown,
duplicate, trailing and malformed values are refused), and provide the
per-process CSRF token. Native forms use separate size-bounded
`application/x-www-form-urlencoded` endpoints with the same Host, Origin and
token checks. GET and HEAD never mutate. No CORS is provided. The token is a
cross-site request defense, not authentication.

The response uses no-store, nosniff, frame denial and a restrictive CSP. It
loads no script. The page uses
`Referrer-Policy: strict-origin-when-cross-origin`: Chromium sent
`Origin: null` for the native form when the previous `no-referrer` policy was
set, so the correct same-origin form was rejected by the server. A browser
experiment confirmed this policy sends the exact same-origin Origin while
cross-origin referrers contain only the origin. The native-form Playwright test
locks down the behavior; request validation was not weakened.

Default loopback binding is the supported safe mode. The server has no user
authentication. Explicit non-loopback binding is unauthenticated and requires
an independently protected network boundary; the CSRF token does not change
that fact. Provider errors and mutation responses are sanitized. Request
payloads, credentials, source titles and CSRF tokens are not logged.

## SQLite and ownership lifecycle

The SQLite database is local state, not an identity service. Its schema is
versioned; initialization is transactional, mapping keys include both account
namespaces, and secure-delete is enabled. The database must be a regular
non-symlink file with restrictive permissions and a safe parent directory.

Before serving, the HTTP server acquires a sidecar OS lock (`flock` on supported
macOS/Linux systems), canonicalizes the database path, rejects hard-link
ambiguity and initializes/opens the owned store. Owned initialization and every
owned mutation retain an in-process lease scope for the complete operation.
Closing the owner rejects new scopes and waits for active operations before
releasing the process lock. Shutdown cancels provider work, drains HTTP requests,
closes the store and then releases the lease. Standalone CLI initialization and
writes use a nonblocking ownership check; they fail while the server owns the
database. A CLI reader may use an already-current schema read-only under the
lease, but does not migrate it.

## Configuration and credentials

`config.toml` contains validated settings (log level, probe address, HTTP
listener address and optional database path), never secrets. Missing settings
use defaults; unknown keys and credential-like keys fail closed. Changes apply
at process startup, not live reload. Credentials come from
`WEREAD_API_KEY` and `HARDCOVER_API_KEY` in the invoking process environment.
Provider endpoints are fixed HTTPS URLs. The clients reject redirects, bound
response size and timeout, pace their own calls, and do not automatically
retry. These safeguards are not a provider quota guarantee.

The standalone `serve-probes` command remains separate from the mapping HTTP
server. `/readyz` checks local database access and makes no provider requests.

## Development and verification

The supported local gates are `make check`, pinned Markdown lint for changed
docs, and synthetic Playwright journeys via `make e2e`. Make and a C compiler
are required because the SQLite driver uses CGO. `.mise.toml` pins development
tools; `go.mod` declares the Go version. Tests use synthetic provider
implementations and temporary SQLite, never live credentials.

The [HTTP acceptance plan](plans/http-server.md) maps user stories to CLI, HTTP,
browser and shared-core tests. The larger matching work is in the
[matching-worklist plan](plans/matching-worklist.md); neither plan is a claim
that status synchronization or the full broader milestone is complete.

## Dependency choices

| Dependency | Rationale |
| --- | --- |
| Go standard library (`net/http`, JSON, `flag`, `log/slog`, crypto) | Small audit surface for a credential-holding local tool. |
| `github.com/mattn/go-sqlite3` | Durable local state with explicit CGO tradeoff. |
| `github.com/BurntSushi/toml` | Small settings-only configuration with unknown-key detection. |
| GitHub Actions and pinned `govulncheck` | Owning CI checks and advisory vulnerability scanning. |

Deliberately absent are a CLI framework, ORM, frontend framework/build chain,
retry library, telemetry SDK, user-authentication system and background job
runner.
