# Drifter architecture

Drifter relates reading facts from source services (WeRead today; Kindle
Unlimited later) to Hardcover reading status. This document describes the
current core and CLI, including the pending-worklist slice; future behavior (status
writes, daemon scheduling, deployment) is labeled as planned, not built.

## Components

| Component | Location | Responsibility |
| --- | --- | --- |
| CLI | `cmd/drifter` | Argument parsing, command routing, JSON output, wiring config/logging/probes/admin server |
| Core operations | `internal/companion/operations.go` | Provider contracts and shared inspect, match, validated confirmation and stored-mapping worklist orchestration |
| Worklist selection | `internal/companion/pending.go` | Privacy/activity/account filtering, bounded enrichment, classification and partial-result reporting |
| Source client (WeRead) | `internal/companion/weread.go` | Read-only gateway calls: shelf, search, book info, personal progress |
| Target client (Hardcover) | `internal/companion/hardcover.go` | Read-only GraphQL: exact-edition lookup, bounded candidate search, work/edition membership validation |
| Transport | `internal/companion/http.go` | Shared hardened HTTP: fixed HTTPS destinations, no redirects, 30 s timeout, 2 MiB cap, per-client pacing, no retries, sanitized errors |
| Identity normalization | `internal/companion/book.go` | ISBN checksum/10↔13 conversion, book-ID validation, conservative status inference |
| Mapping store | `internal/companion/store.go` | Versioned SQLite confirmed mappings; transactional initialization, account-scoped keys, mode-600 file checks, secure-delete |
| Configuration | `internal/config` | Settings-only TOML: full validation at load, unknown keys rejected, credential-like keys refused, restart-applied |
| Logging | `internal/logging` | stdlib `slog` JSON to stdout, level from config, random run IDs, no payloads or secrets |
| Health probes | `internal/probes` | Read-only `/healthz` and `/readyz` over stdlib `net/http`; local dependency checks only |
| Admin HTTP adapter | `internal/admin` | Foreground lifecycle, same-origin/Host boundary, rendered explicit mapping maintenance, read-only mapping/pending routes and core-owned writes |

## Core and adapter responsibilities

Core owns feature operations and their provider contracts (`Source`, `Target`).
`PendingWorklist` reads mappings and selects eligible books; `ConfirmLink` fetches
source metadata, validates work/edition membership and persists only the explicit
selection. CLI and a future server call these same operations, not each other.
The existing `Pending` selector has a narrower internal source dependency for
focused tests; it does not define a separate CLI feature contract.

Adapters own credentials/configuration wiring, argument or request decoding,
output formatting and translation of core results into exit codes or HTTP
responses. Store lifetime for simple local listing/removal is adapter wiring;
`Store.List` and `Store.Unlink` already own those operations. No server, cache,
plugin registry or additional service layer is introduced by this separation.

## Data flow (implemented)

The [interactive workflow diagram](diagrams/cli-mapping-workflow.html) separates
pending, matching and explicit confirmation. Its
[editable specification](diagrams/cli-mapping-workflow.json) pins the implemented
source revision. These are independent commands, not an automatic linking loop.
The [current verification receipt](diagrams/core-workflow-check/cli-mapping-workflow.finalize-summary.json)
binds this specification and HTML to passing artifact and browser checks.
Older adjacent receipts describe the previous diagram; browser checks are
distinct from optional visual review.

Pending reads mappings through core, fetches the WeRead shelf, filters privacy,
activity and mapped identities, then enriches eligible rows. It returns JSON
with explicit completeness and issue codes, never invokes Hardcover and never
creates a mapping. Full limits and opt-in behavior live in the
[CLI reference](cli.md#pending-matching-worklist).

```text
WeRead gateway ──▶ source client ──▶ normalized Book/Progress
                                          │
                                          ▼
                          ISBN variants ─▶ Hardcover exact-edition query
                                          │ no exact match
                                          ▼
                          bounded title/original-language candidate search
                                          │
                                          ▼
                        owner reviews candidates (human decision)
                                          │ explicit confirmation
                                          ▼
              membership validation (work exists; edition belongs to work)
                                          │
                                          ▼
                     SQLite mapping store (local, private)
```

Nothing in this flow writes to either provider. Personal progress is fetched
only on explicit request and never persisted.

## Trust boundaries

- **Providers are untrusted input.** Every response passes field-by-field
  validation; unknown fields are discarded; API-level errors inside HTTP 200
  (GraphQL `errors`, WeRead `errcode`, upgrade notices) fail closed.
- **The owner is the only linking authority.** Search ranking and identifier
  uniqueness produce *candidates*, never mappings. `link` requires an explicit
  work ID and validates membership but does not judge literary identity.
- **The state file is secret-adjacent.** It contains personal tracking
  metadata, is mode 600, must be a regular non-symlink file with a
  non-group/other-writable parent, and enables SQLite `secure_delete`.
- **Credentials never enter TOML, logs, flags or errors.** They exist only in
  the process environment and are sent as bearer headers to fixed HTTPS
  destinations.

## Configuration, probes and admin server

`config.toml` holds settings only (log level, probe address, server listener
address, optional database path). Load is total: missing file → defaults;
unknown key, bad value or credential-like key → error. Callers apply
configuration at startup; the confirmed direction is restart-only. `serve-probes`
remains a separate health-only process. `serve` establishes the first slice of
the same intended long-running daemon server foundation, delivered
incrementally. It serves `/healthz`, `/readyz`, the server-rendered `/admin`,
read-only mapping/pending JSON, and explicit local mapping confirmation,
replacement and unlink routes. Confirmation delegates to `ConfirmLinkOwned`,
which validates the source book and selected target work/edition before storing;
there is no inferred candidate acceptance. Shared source and target provider
clients are lazy, so page rendering and mapping reads require no credentials or
provider calls. Pending calls occur only on explicit request through
`companion.PendingWorklist`. JSON writes require bounded strict
`application/json`, same-origin Origin, Host allowlisting and a process-random
CSRF token. Native forms use separate bounded URL-encoded routes and same-origin
checks. Responses are no-store, nosniff and protected from framing by CSP and
frame headers. No CORS is enabled; unauthenticated non-loopback exposure is not
an authenticated deployment model. The default listener is loopback.

The server holds the core SQLite ownership lease through shutdown; ordinary
store writes check the same lock. Read-only CLI opens fall back to a
current-schema SQLite read-only handle while that lease is held and never
migrate under it. Synchronization and provider-status writes are not
implemented.

## Development tooling

`.mise.toml` pins Go and rumdl. Make executes Go and gofmt through mise;
GitHub Actions installs the same tools with `jdx/mise-action`. The Go language
requirement remains in `go.mod`. Make and a C compiler are host prerequisites
because the SQLite driver requires CGO. The advisory vulnerability scan runs a
pinned govulncheck with the same Go toolchain; it does not select another Go
version. See the [quick start](../README.md#quick-start).

## OSS component inventory and rationale

| Dependency | Why | What would change it |
| --- | --- | --- |
| Go standard library (`net/http`, `encoding/json`, `flag`, `log/slog`, `crypto/rand`) | Small audit surface for a credential-holding tool; no framework churn | A need for middleware-rich routing or OpenTelemetry traces |
| `github.com/mattn/go-sqlite3` (pinned v1.14.52) | Battle-tested driver; CGO cost accepted for single-file durable state | A pure-Go/cross-compilation requirement (`modernc.org/sqlite` tradeoff: larger binaries, different perf profile) |
| `github.com/BurntSushi/toml` (pinned v1.6.0) | Minimal, dependency-free TOML decode with unknown-key detection | None foreseeable; the file is deliberately tiny |
| GitHub Actions + `govulncheck` | Free CI running the owning gate; advisory vulnerability scan | Self-hosted runners if secrets ever enter CI (they do not today) |

Deliberately absent: CLI frameworks (cobra/urfave), validation libraries,
retry/backoff libraries, ORMs, telemetry SDKs. Each is recorded here as a
choice, with the trigger that would justify revisiting.

## Planned, not implemented

The incremental server work and acceptance boundaries are recorded in the
[daemon-admin plan](plans/daemon-admin.md). Status synchronization, per-book
holds/resolution, Telegram notifications and k3s deployment remain future work;
nothing in this repository performs a provider status write.
