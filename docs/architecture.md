# Drifter architecture

Drifter is a local companion between WeRead reading data and Hardcover catalog
identities. The current product is a CLI plus one foreground HTTP server and a
shared Go core. Both interfaces read the same private SQLite mapping/decision store.
Provider clients are read-only: a mapping does not authorize status updates.
Kindle Unlimited intake, status synchronization and deployment are not
implemented.

## Components

| Component | Location | Responsibility |
| --- | --- | --- |
| CLI and wiring | `cmd/cli`, `internal/cli` | Thin CLI executable and Cobra command tree; handlers load strict settings, construct providers lazily, and emit JSON/structured logs or run explicit terminal review. |
| Server command runner | `cmd/server`, `internal/server` | Thin signal/environment entrypoint and flag/configuration runner for the standalone HTTP server. |
| Shared feature core | `internal/companion/operations.go`, `pending.go`, `decision.go` | Source/target contracts, matching, explicit confirmation, pending selection, account-scoped local operations and owned writes. |
| WeRead client | `internal/companion/weread.go` | Read-only shelf, search, book metadata and explicitly requested personal progress. |
| Hardcover client | `internal/companion/hardcover.go` | Read-only edition lookup, bounded candidate search and work/edition membership validation. |
| Provider transport | `internal/companion/http.go` | Fixed HTTPS endpoints, no redirects, bounded responses, timeouts, per-client pacing and sanitized errors. |
| Mapping store and ownership | `internal/companion/store.go`, `ownership*.go` | Versioned SQLite, private-file validation, schema initialization, confirmed mappings and scoped decisions, sidecar process lock and owned-operation lifetime. |
| Local HTTP server | `internal/httpserver` | Foreground listener, server-rendered mapping interface, request boundary, read routes and explicit mapping mutations through shared core. |
| Configuration | `internal/config` | Settings-only TOML with full validation, unknown-key rejection and refusal of credential-like settings. |
| Matching catalogs | `internal/catalog`, called by `internal/companion` | Bounded, request-triggered local/HTTPS loading, v1 schema validation, untrusted match evidence, offline local merge and validated output encoding. |
| Catalog export | `internal/companion/catalog_export.go`, `internal/cli/catalog.go` | Read-only account-filtered projection of confirmed mappings to public v1 assertions; no database creation/migration or provider calls. |
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
already-mapped and suppressing-decision rows before enrichment, and reports
completeness/issues. Its versioned seek cursor binds the original window, scope,
inclusion flags and last attempted tuple; each page attempts at most 100
metadata rows. It does
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
                                      └──▶ private SQLite mappings/decisions
```

Matching performs bounded exact-edition lookup first, then bounded candidate
search; configured catalogs load only during the shared core `Match` operation
and return separate, untrusted assertions and hints. They never confirm a mapping.
Terminal review may explicitly reuse a hint title
or author in a new bounded shared match; author equality stable-partitions the
same candidates without changing exact editions. Standalone matching is
unchanged. `export` uses a current-schema SQLite read-only
query filtered by both explicitly supplied account labels and selects only the
catalog-eligible identity columns; it never creates or migrates state. The
standalone `merge` operation loads only explicitly named local documents
through the shared bounded loader and validates the complete output against the
bundled v1 schema. Neither command contacts providers or mutates personal state.
Pending reads use WeRead and SQLite only. The local page and mapping list read
SQLite only; provider construction is lazy and shared by each server instance.

## CLI and HTTP interfaces

`drifter-server` owns one foreground HTTP process, independently of the CLI.
`cmd/server` wires signals and environment-provided credentials into the
`internal/server` runner; HTTP routes and templates remain in
`internal/httpserver`. The default bind is `127.0.0.1:8081`;
wildcard binds are rejected. The root `GET /` serves an
embedded server-rendered mapping page. It uses native forms and no browser
JavaScript asset or frontend build. The page and saved-mapping reads do not
construct or call providers. Provider-backed requests happen only when a user
explicitly asks for pending data or confirms/replaces a mapping.

| Method and route | Behavior |
| --- | --- |
| `GET /`, `HEAD /` | Mapping interface; local store only. |
| `GET /api/mappings`, `HEAD /api/mappings` | Read-only mapping list. |
| `GET /api/pending?account=…&target_account=…[&days=N&after=CURSOR]` | Explicit WeRead-backed pending request. |
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
there is no parallel mapping policy in the handler. Replace/unlink carry the
displayed `expected_work_id` and `expected_edition_id`; the store compares them
atomically. Stale targets conflict, and replacement cannot resurrect a deleted
row. Standalone CLI `link --replace`/`unlink` retain their explicit unguarded
semantics. See the [wire contract](cli.md#health-probes-and-http-server).

The server admits one provider-backed operation at a time with no waiting queue
and a 90-second deadline. Busy/timeout outcomes are sanitized 503/504; timed-out
pending reports preserve completed rows and remain incomplete. Local reads,
probes and unlink do not consume the provider slot.

## Browser and request boundary

Chi routes are grouped into page/probe, API and native mapping-action routes;
handlers remain ordinary `net/http` handlers over the shared core. One
`httplog` request middleware wraps the Host/Origin boundary and router, so
security rejections are observed without adding a second request logger.

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
records contain only method, allowlisted route pattern, status and processing
duration, in addition to standard time/level fields and a constant message. The
logger replaces the middleware's URL-bearing message and drops all other
attributes, including headers, remote address, body, query, provider errors,
and panic text/stack. Request and response body capture is off.

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

### Terminal review and decisions

`internal/cli/review.go` owns terminal input/output and uses shared pending,
matching and validated confirmation. It acquires database ownership before
providers or prompts and releases it on every exit. Page acquisition, candidate
lookup and final confirmation each have a 60-second budget; owner input has none.
Only explicit `yes` links. EOF/cancellation never mean consent. Per-book failures
preserve prior choices, continue safe work and return nonzero failed-ID summaries.
Fresh review invocations retry still-pending rows; continuation is a live shelf
scan, not a snapshot, so moved-ahead activity may require a fresh scan.

Schema v2 adds decisions keyed by provider/source account/book/target account.
Exclude/not-in-catalog suppress only that scope; skip returns next invocation.
Link/unlink leave independent decisions intact. Legacy v0/v1 upgrades preserve
mappings transactionally; rollback/future-version refusal and read-only current
schema access remain required. Public catalog v1 is independent and unchanged.

## Configuration and credentials

`config.toml` contains validated settings (log level, probe address, HTTP
listener address, optional database path and explicit matching catalog sources),
never secrets. Missing settings use defaults; unknown keys and credential-like
keys fail closed. Subscribed catalog files/URLs are literal and load only during
`match` or explicit terminal review lookup; export/merge are separately explicit
offline CLI operations. Changes apply at process startup, not live reload.
Credentials come from
`WEREAD_API_KEY` and `HARDCOVER_API_KEY` in the invoking process environment.
Provider endpoints are fixed HTTPS URLs. The clients reject redirects, bound
response size and timeout, pace their own calls, and do not automatically
retry. These safeguards are not a provider quota guarantee.

The standalone `serve-probes` command remains separate from the mapping HTTP
server. `/readyz` checks local database access and makes no provider requests.

## Development and verification

The supported local gates are `make check`, pinned Markdown lint for changed
docs, and synthetic Playwright journeys via `make e2e`. Make and a C compiler
are required because the SQLite driver uses CGO. `.mise.toml` pins Go 1.27.2,
Node and rumdl; `go.mod` requires Go 1.27.0. CI's mise action installs the same
toolchain before running Make gates. Tests use synthetic provider
implementations and temporary SQLite, never live credentials.

The [HTTP acceptance plan](plans/http-server.md) maps user stories to CLI, HTTP,
browser and shared-core tests. The larger matching work is in the
[matching-worklist plan](plans/matching-worklist.md); neither plan is a claim
that status synchronization or the full broader milestone is complete.

## Dependency choices

| Dependency | Rationale |
| --- | --- |
| Go standard library (`net/http`, JSON, `log/slog`, crypto) | Small audit surface and standard-handler boundary. |
| [`github.com/go-chi/chi/v5` v5.5.0](https://pkg.go.dev/github.com/go-chi/chi/v5@v5.5.0) | Method-aware route groups while retaining standard `http.Handler` composition. |
| [`github.com/go-chi/httplog/v3` v3.5.0](https://pkg.go.dev/github.com/go-chi/httplog/v3@v3.5.0) | Structured request logging on `log/slog`; Drifter wraps it with a strict field/message allowlist. |
| [`github.com/santhosh-tekuri/jsonschema/v6` v6.0.3](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6@v6.0.3) | Draft 2020-12 validation for the closed, versioned catalog schema; schema references remain bundled. |
| [`github.com/spf13/cobra` v1.10.2](https://pkg.go.dev/github.com/spf13/cobra@v1.10.2) | Nested command/help tree, flag validation and per-command handlers; Go 1.15 module baseline supports the pinned Go 1.27.2 toolchain. |
| `github.com/mattn/go-sqlite3` | Durable local state with explicit CGO tradeoff. |
| `github.com/BurntSushi/toml` | Small settings-only configuration with unknown-key detection. |
| GitHub Actions and pinned `govulncheck` | Owning CI checks and advisory vulnerability scanning. |

Chi documents route composition and `http.Handler` interoperability in its
[official README](https://github.com/go-chi/chi/tree/v5.5.0). The request
logger's defaults include URL and panic fields; its [official options/API
documentation](https://pkg.go.dev/github.com/go-chi/httplog/v3@v3.5.0) informed
the explicit safe schema and handler filter in `internal/httpserver/requestlog.go`.

Cobra is limited to command parsing and help; it does not manage settings or
credentials. Configuration remains strict `BurntSushi/toml` with explicit
unknown-key and credential-like-key rejection. Viper, an ORM, frontend
framework/build chain, retry library, telemetry SDK, user-authentication system
and background job runner remain absent.

The command-tree implementation follows Cobra's official
[user guide](https://github.com/spf13/cobra/blob/v1.10.2/site/content/user_guide.md)
for `AddCommand`, `RunE`, required flags and repeated `StringArray` flags, and its
[`Command` API](https://github.com/spf13/cobra/blob/v1.10.2/command.go) for
`ExecuteContext`, `SetOut`/`SetErr`, `SilenceErrors` and `SilenceUsage`. The
pinned [v1.10.2 release](https://github.com/spf13/cobra/releases/tag/v1.10.2)
(published 2025-12-04, latest listed at selection) declares Go 1.15 in its
[versioned module file](https://github.com/spf13/cobra/blob/v1.10.2/go.mod). Cobra is not a settings manager;
TOML remains loaded directly by `internal/config`, and Viper is not included.
