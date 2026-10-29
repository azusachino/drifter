# Drifter architecture

Drifter connects personal reading facts from source services (such as WeRead)
with destination catalog works and editions on Hardcover. It operates as a local
companion offering two user-facing interfaces: an interactive command-line tool
(`drifter`) and a foreground HTTP server (`drifter-server`) providing an
unauthenticated, server-rendered web dashboard.

Both interfaces sit atop a shared Go core (`internal/companion`) backed by a
private, versioned SQLite database. Provider integrations follow a clean
hexagonal (ports and adapters) design.

---

## Architectural model

Drifter decouples the domain model, business rules, and state machine from
external services using ports and adapters:

```text
┌────────────────────────────────────────────────────────┐
│                   Driving Adapters                     │
│         CLI (Cobra)       │    HTTP Server (Chi)       │
└───────────────┬───────────────────────────┬────────────┘
                │                           │
                ▼                           ▼
┌────────────────────────────────────────────────────────┐
│                 Companion Domain Core                  │
│                                                        │
│  • Domain Models & Contracts (Source, Target)          │
│  • Worklist Engine (Pending, Review Decisions)         │
│  • Matching Engine (Author Partition, Exact Editions)  │
│  • Three-Way Status Sync & Conflict Evaluator          │
│  • Two-Phase Intent Ledger & Baseline Tracker          │
│  • SQLite Storage Engine (Schema v6)                   │
└───────────────┬───────────────────────────┬────────────┘
                │                           │
                ▼                           ▼
┌────────────────────────────────────────────────────────┐
│                   Driven Adapters                      │
│                                                        │
│  internal/provider/weread   │  internal/provider/      │
│  (SourceProvider adapter)   │  hardcover (Target)      │
│ ────────────────────────────┴────────────────────────  │
│  internal/provider/transport (Rate-limited HTTP)       │
│  internal/isbn (Douban Books & OpenLibrary Resolvers)  │
│  internal/catalog (Community Catalog Assertions)       │
└────────────────────────────────────────────────────────┘
```

The core (`internal/companion`) has **zero dependencies** on external provider
packages. Providers import the core package to satisfy interfaces.

---

## Components

| Component | Package / Path | Responsibility |
| --- | --- | --- |
| CLI executable | `cmd/cli`, `internal/cli` | Thin executable wiring Cobra commands for terminal review, manual linking, status syncing, and catalog export. |
| HTTP server daemon | `cmd/server`, `internal/server` | Signal management, configuration loading, and runner for the local web server. |
| Web dashboard | `internal/httpserver` | Server-rendered, zero-script web admin dashboard (Mappings, Pending, Match, Export, Sync tabs). |
| Domain core | `internal/companion` | Core models (`Book`, `Progress`, `MatchReport`, `UserBookStatus`), state evaluation, and SQLite persistence. |
| WeRead provider | `internal/provider/weread` | Intake adapter implementing `companion.SourceProvider` and `companion.PreviewSource`. |
| Hardcover provider | `internal/provider/hardcover` | Target adapter implementing `companion.TargetProvider`, `companion.PreviewTarget`, and `companion.ApplyTarget`. |
| HTTP transport | `internal/provider/transport` | Token validation, response caps (2 MiB), pacing gates, and error sanitization. |
| ISBN resolution | `internal/isbn` | Bibliographic resolution (Douban, OpenLibrary, custom URL) to bridge the translation gap. |
| Community catalogs | `internal/catalog` | JSON schema validation, offline catalog merge, and untrusted match hints. |
| SQLite storage | `internal/companion/store.go` | Private SQLite database (schema v6) with process locking, baselines, intents, and audit runs. |
| Config & logging | `internal/config`, `internal/logging` | Strict TOML validation, `log/slog` structured logs, and standalone health probes. |

---

## Domain concepts & authority boundaries

1. **Source Book**: A book record identified within an intake source namespace.
   Two source books with similar titles are distinct.
2. **Target Work & Edition**: A Hardcover work entity, which contains zero or
   more specific editions. Drifter supports both work-only and edition-specific
   links.
3. **Mapping**: An explicit, confirmed identity relationship between a source
   book and a target work or edition. A mapping links identity; it does not
   itself execute status changes without explicit invocation.
4. **Account Fingerprint**: Cryptographic binding (`weread:<vid>`,
   `hardcover:<id>`) recorded in SQLite on first run. If credentials change to a
   different user, Drifter fails closed to prevent cross-account pollution.
5. **Pending Worklist**: Recently opened books on the source shelf filtered
   against existing mappings and review exclusions. Cursors scan the live shelf
   without storing personal reading history.

---

## Status synchronization lifecycle

Drifter's reading status synchronization engine guarantees safety through
four sequential stages:

```text
┌─────────────────────────┐     ┌─────────────────────────┐
│ 1. Verify Fingerprint   │ ──▶ │ 2. Three-Way Merge      │
│ Check SQLite account    │     │ Source vs Target vs Base│
└───────────┬─────────────┘     └───────────┬─────────────┘
            │                               │
            ▼                               ▼
┌─────────────────────────┐     ┌─────────────────────────┐
│ 3. Two-Phase Intent     │ ──▶ │ 4. Verify & Record      │
│ Staged intent & mutate  │     │ Read-back & baseline    │
└─────────────────────────┘     └─────────────────────────┘
```

### 1. Three-way merge evaluation

Before any remote mutation is dispatched, Drifter evaluates the source status,
current target status, and last known baseline:

- **Create**: Target record is absent; propose initializing target status.
- **No-Op**: Target status already matches source status; zero mutations sent.
- **Update**: Source reading progress advanced while target remained unchanged
  since last baseline; propose updating target.
- **Conflict (Manual Target Edit)**: Target changed on Hardcover while source
  was unchanged. Drifter refuses to overwrite the manual edit and records a
  hold.
- **Conflict (Divergent)**: Both source and target changed to different values.
- **Hold (Private Book)**: Book is marked private in WeRead; excluded from
  sync unless `--allow-private` is explicitly set.

### 2. Two-phase mutation & recovery

To guard against network drops and duplicate remote writes:

1. **Staged Intent**: A record is written to `sync_intents` with status
   `pending`, then transitioned to `in_flight`.
2. **Target Mutation**: An approved GraphQL mutation updates only the
   `status_id` field on Hardcover. Ratings, reviews, and reading logs are never
   modified.
3. **Read-back Verification**: Drifter queries Hardcover to verify the mutation
   succeeded before marking the intent `applied`.
4. **Baseline & Audit**: On verification, `status_baselines` is updated and a
   `sync_receipts` entry is recorded.
5. **Restart Reconciliation**: If an operation times out (e.g. HTTP 504), the
   intent remains `uncertain`. On restart, Drifter reads remote state and
   resolves the intent without issuing duplicate writes.

### 3. Continuous account reconciliation

`drifter reconcile` processes all confirmed mappings for an account:

- Sequential execution with monotonic run IDs recorded in `sync_runs`.
- **Partial Failure Isolation**: A timeout or failure on one book does not abort
  unrelated books in the run.
- **Persistent Holds**: Conflicts are saved in `sync_holds` with full serialized
  evidence for inspection.
- `--dry-run` performs full three-way evaluation without issuing writes or
  creating runs.

---

## Web dashboard & security boundary

The web interface provided by `drifter-server` is designed with minimal attack
surface:

- **Loopback Default**: Listens on `127.0.0.1:8081` by default. Wildcard
  bindings are rejected.
- **Host & Origin Validation**: Strict Host header validation and Origin
  checking to prevent DNS rebinding and cross-origin attacks.
- **Zero Client Scripts**: All views are server-rendered with native HTML forms;
  no client JavaScript or third-party frontend dependencies.
- **CSRF Defense**: Per-process CSRF tokens defend state mutations.
- **Data Protection**: Provider responses, shelf captures, and authentication
  tokens are never stored in browser cookies or persistent web caches.

---

## Database schema & storage

Drifter stores local state in SQLite (schema version 6):

- `mappings`: Confirmed source-to-target links.
- `review_decisions`: Account-scoped skip, exclude, and not-in-catalog choices.
- `account_fingerprints`: Verified provider account bindings.
- `status_baselines`: Last observed source/target status snapshots.
- `target_authorities`: Explicit source authority assignments for target works.
- `sync_intents` & `sync_receipts`: Two-phase commit ledger.
- `sync_runs` & `sync_holds`: Audit run logs and unresolved conflict holds.

File safety is enforced at startup: the database must be a regular file with
`0600` permissions and a non-writable parent directory. Exclusive process
locks (`flock`) prevent concurrent write contention between the CLI and server.

---

## Development & quality gates

Every change to Drifter must pass four gates:

1. `make check`: `go vet`, `go mod verify`, `go test -race ./...`, and binary
   builds.
2. `make e2e`: Headless Playwright browser journey tests against the web
   dashboard.
3. `rumdl check`: Markdown standards compliance with strict 80-character line
   limits.
4. Workstation integration checks (`make check` at repository root).
