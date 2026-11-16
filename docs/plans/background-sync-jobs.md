# Background Sync Jobs: Workflow & Design

> Revised after architecture review (`argon-reviewer`) to address `operationSlot` lock contention, SQLite concurrency, in-memory shelf traversal, cooldown placement, nullable FKs, zero-script UI forms, and shutdown ordering.

## 1. Problem & Goal

Currently, Drifter executes `POST /api/v1/auto-match` and `POST /api/v1/reconcile` as synchronous HTTP requests governed by a 90-second `operationTimeout` and a single-slot `operationSlot` lock. While reading-state reconciliation over existing mappings finishes quickly, **library-wide auto-matching** (calling WeRead book metadata, Hardcover search, and the LLM advisor) takes ~25–35 minutes for a lifetime library (239+ finished books, 1,250+ total shelf books). Running this from outside requires an external cursor-paging script.

**Goal:** Give `drifter-server` an internal, SQLite-backed background job worker so Drifter itself owns the full **Match → Reconcile** workflow:
- **Default (`recent`)**: discovers and auto-matches unmapped books opened in the **last 7 days** (`days=7`), then reconciles all confirmed mappings.
- **`lifetime`**: traverses the **entire WeRead shelf** (including finished, in-progress, and optionally unread books, prioritized `Finished → LastOpened DESC`), with batch checkpointing, cancellation, and cooldown caching, then reconciles all confirmed mappings.
- **No separate `reconcile` workflow needed** for routine sync: a single `POST /api/v1/jobs/sync` triggers both phases asynchronously (`202 Accepted`).
- **Iroha compatibility preserved & unblocked**: `GET /api/v1/reading/snapshot` and `GET /api/v1/reading/changes` are decoupled from `operationSlot` so Iroha polling never gets `503 provider_operation_busy` during a long `lifetime` job, and newly confirmed mappings automatically surface in Iroha's next incremental `changes` poll via `mappings.updated_at`.

**Non-Goals:**
- Distributed/multi-node worker queues (Drifter is single-instance with SQLite ownership).
- Changing `ReconcileAccount`'s core authority/baseline/hold semantics (`CONTEXT.md`).
- Modifying Iroha's `/api/v1/reading/*` contract.

---

## 2. Sync Scopes & Defaults

| Parameter | `recent` (Default) | `lifetime` |
| --- | --- | --- |
| `scope` | `"recent"` | `"lifetime"` |
| Discovery window (`days`) | `7` (replaces old `90` default) | `36500` (or unfiltered shelf) |
| Filter (`finished`) | `false` (all active in last 7d) | configurable (`true` = finished only; `false` = prioritized `Finished DESC, LastOpened DESC, ID ASC`) |
| Phase 1 (Auto-Match) | Unmapped books in 7d window | Entire unmapped shelf in batches of 5 |
| Phase 2 (`ReconcileAccount`) | All confirmed mappings in `mappings` | All confirmed mappings in `mappings` |

`mode` defaults to `"match_and_reconcile"`, with optional `"match_only"` or `"reconcile_only"` for targeted runs.

---

## 3. Data Model (SQLite Migration — `storeVersion = 9`)

Update `storeVersion` to `9` and extend `validateStoreSchema` in `internal/companion/store.go` to validate both new tables. Switch SQLite connection pragmas to `_journal_mode=WAL` (with `_busy_timeout=5000` and `_foreign_keys=on`), and serialize in-process store writes with an exclusive writer mutex on `DatabaseOwnership` (replacing `RLock` for writes) while passing the existing `*Store` into worker helpers instead of opening nested `OpenOwnedStore` handles.

### 3.1 `sync_jobs` Table
Tracks queued, active, and historical background jobs:

```sql
CREATE TABLE IF NOT EXISTS sync_jobs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    provider         TEXT NOT NULL,
    source_account   TEXT NOT NULL,
    target_account   TEXT NOT NULL,
    scope            TEXT NOT NULL, -- 'recent' | 'lifetime' | 'custom'
    mode             TEXT NOT NULL, -- 'match_and_reconcile' | 'match_only' | 'reconcile_only'
    days             INTEGER NOT NULL,
    finished_only    INTEGER NOT NULL DEFAULT 0,
    min_confidence   REAL NOT NULL DEFAULT 0.80,
    dry_run          INTEGER NOT NULL DEFAULT 0,
    force            INTEGER NOT NULL DEFAULT 0,
    status           TEXT NOT NULL, -- 'queued' | 'running' | 'completed' | 'completed_with_errors' | 'cancelled' | 'failed'
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    total_estimated  INTEGER NOT NULL DEFAULT 0,
    processed_count  INTEGER NOT NULL DEFAULT 0,
    confirmed_count  INTEGER NOT NULL DEFAULT 0,
    skipped_count    INTEGER NOT NULL DEFAULT 0,
    error_count      INTEGER NOT NULL DEFAULT 0,
    sync_run_id      INTEGER REFERENCES sync_runs(id), -- NULL when DryRun or RunID == 0
    error_message    TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    started_at       TEXT NOT NULL DEFAULT '',
    finished_at      TEXT NOT NULL DEFAULT ''
);
```

- `sync_run_id` uses `sql.NullInt64` (`NULL` when `DryRun == true` or `ReconcileAccount` fails before `CreateSyncRun`) so `_foreign_keys=on` never rejects `0`.

### 3.2 `auto_match_attempts` Table (Abstention Cooldown)
Prevents repeated `lifetime` sweeps from re-fetching WeRead metadata and re-checking deterministic non-matches:

```sql
CREATE TABLE IF NOT EXISTS auto_match_attempts (
    provider        TEXT NOT NULL,
    source_account  TEXT NOT NULL,
    book_id         TEXT NOT NULL,
    target_account  TEXT NOT NULL,
    last_status     TEXT NOT NULL, -- 'abstained' | 'low_confidence' | 'no_candidate' | 'ambiguous'
    last_confidence REAL NOT NULL DEFAULT 0,
    last_reason     TEXT NOT NULL DEFAULT '',
    attempted_at    TEXT NOT NULL,
    PRIMARY KEY (provider, source_account, book_id, target_account)
);
```

- **Filter before `source.Book()`**: Cooldown filtering runs against the shelf IDs *before* calling `source.Book(ctx, id)`, eliminating redundant WeRead HTTP calls.
- **Recording rules**: Record only deterministic non-matches (`abstained`, `low_confidence`, `no_candidate`, `ambiguous`) when `dry_run == false`. Never record transient `error` results.
- **Bypass rules**: Skip if `attempted_at` is within 30 days, **unless** `force=true` or (`last_status == 'low_confidence'` and `job.min_confidence < last_confidence`).

---

## 4. Worker Architecture & Execution Workflow

### 4.1 Concurrency & Lock Isolation
- **Read-only `/api/v1/reading/*` routes**: Remove `s.admit()` (`s.operationSlot`) from `handleReadingSnapshot` and `handleReadingChanges` (they use `OpenStoreReadOnly` + WAL). Iroha polling never blocks or gets `503` while a job runs.
- **Per-batch slot acquisition**: The background worker does **not** hold `s.operationSlot` across the entire 30-minute job. Instead, it acquires `s.operationSlot` per small unit of work (or per book/reconcile phase) and yields between batches so interactive `/api/pending` and `/api/mappings/*` requests can interleave. Add `s.admit()` (or store write serialization) to `unlink` and `import` handlers for consistency.
- **Duplicate coalescing**: If a job with matching `(provider, source_account, target_account, scope, mode, finished_only)` is already `queued` or `running`, `POST /api/v1/jobs/sync` returns the existing active job instead of stacking duplicates.

### 4.2 Lifecycle & Graceful Shutdown
- Owned by `httpserver.Server`, started with a server-scoped `serverCtx` and tracked by a `sync.WaitGroup`.
- **Startup recovery**: On boot, mark any `sync_jobs` rows left in `status = 'running'` as `failed` (`"interrupted by server restart"`).
- **Shutdown ordering**: In `Server.Serve`, cancel `serverCtx` and `wg.Wait()` for the worker **before** `owner.Close()`, so the worker never attempts a SQLite write against a closed `DatabaseOwnership`.
- **Cancel vs. Shutdown**: User cancellation (`POST /api/v1/jobs/{id}/cancel` / `POST /jobs/cancel`) sets `cancel_requested = 1` and cancels the per-job context, marking the job `cancelled`. Server shutdown cancels `serverCtx` without setting `cancel_requested`, leaving/marking the job `failed ("interrupted by server restart")`.
- **Per-call timeouts**: Bound each `source.Book`, Hardcover search, advisor call, and `ReconcileAccount` call with a bounded per-step timeout (`60s–90s`) so a hung upstream call cannot block shutdown.

### 4.3 Step-by-Step Job Execution
1. **Claim job**: Transition `sync_jobs` from `queued` → `running`, set `started_at`.
2. **Phase 1 — In-Memory Shelf Snapshot & Auto-Match** (if `mode != "reconcile_only"`):
   - Do **not** page `PendingWorklistOwned` with `pendingCursor` (which re-fetches `source.Shelf` every batch, drops `LastOpened == 0` unread books, and breaks on finished-first sorting or `AsOf` drift).
   - Instead, call `source.Shelf(ctx)` **once** at the start of Phase 1.
   - Filter out `CB_` imported books, existing `mappings`, `decisions` (`exclude`, `not-in-catalog`), and active `auto_match_attempts` cooldowns (unless `force=true`), and apply the window (`days=7` for `recent`, or full shelf for `lifetime`; `finished_only` if requested).
   - Sort the in-memory candidate list once: for `lifetime`, sort `(Finished DESC, LastOpened DESC, ID ASC)`. Record `total_estimated = len(candidates)`.
   - Iterate in batches of 5:
     - Fetch `source.Book(stepCtx, id)` **once** per book and pass the already-fetched `Book` into match + confirmation (eliminating the current 3x `source.Book` calls per book).
     - Confirmed matches (`exact` or `confidence >= min_confidence`) persist immediately to `mappings` via the shared `*Store`.
     - Deterministic non-matches update `auto_match_attempts` (when `!dry_run`).
     - Checkpoint `processed_count`, `confirmed_count`, `skipped_count`, `error_count` to `sync_jobs` after each batch.
     - Check `jobCtx.Done()` and `serverCtx.Done()` between books.
3. **Phase 2 — Reconcile** (if `mode != "match_only"` and not cancelled/shutting down):
   - Wire `Advisor` and `WithAdvisor: adv != nil` into `companion.ReconcileOptions`.
   - Run `companion.ReconcileAccount` over all confirmed mappings for `(provider, source_account, target_account)` using the shared `*Store`.
   - Save resulting `sync_runs.id` (as `sql.NullInt64`) on `sync_jobs.sync_run_id`.
4. **Finalize**:
   - Set `finished_at` and terminal status: `completed`, `completed_with_errors` (if `error_count > 0` or reconcile recorded errors), `cancelled`, or `failed`.

---

## 5. Interfaces: HTTP API, Web UI, and k3s CronJob

### 5.1 JSON API (Bearer-authenticated via `DRIFTER_API_TOKEN`)
- `POST /api/v1/jobs/sync`: accepts query parameters (with empty body and no `Content-Type` header for `curl` compatibility) or JSON body:
  - `scope` (`recent` default | `lifetime` | `custom`), `days` (`7` default for `recent`, `36500` for `lifetime`), `mode` (`match_and_reconcile` default | `match_only` | `reconcile_only`), `finished` (`bool`), `min_confidence` (`0.80` default), `force` (`bool`), `dry_run` (`bool`).
  - Returns `202 Accepted` (or `200 OK` if coalesced) with `SyncJob` JSON.
- `GET /api/v1/jobs` — list recent jobs (limit 20).
- `GET /api/v1/jobs/{id}` — inspect single job status, progress counters, and linked `sync_run_id`.
- `POST /api/v1/jobs/{id}/cancel` — cancel a `queued` or `running` job.
- `GET /api/status` — include `latest_job` so external monitors (`suzuran`) can observe async job failures.
- `POST /api/v1/reconcile` — retained for backward compatibility (`reconcile_only` / synchronous calls).

### 5.2 Web UI (`GET /`, zero-script CSP `default-src 'none'`)
Separate browser form routes from Bearer JSON routes, using `parseStrictForm`, `mutationOriginAllowed`, `validCSRFToken`, and `303 See Other` redirects to `/?notice=...`:
- `POST /jobs/sync` — form buttons for **"Sync Recent (7 days)"** (`scope=recent`) and **"Sync Lifetime"** (`scope=lifetime`, optional `finished` checkbox).
- `POST /jobs/cancel` — cancel button for active `queued`/`running` jobs.
- Extend `handleRoot` notice allowlist with `job_started`, `job_coalesced`, and `job_cancelled`, and render an active/recent Sync Jobs table.

### 5.3 k3s CronJob (`vendor/harus-k3s/04-media/drifter/cronjob.yaml`)
- Update `drifter-reconcile` (`15 4,16 * * *`) command to:
  `POST http://drifter.harus-media:80/api/v1/jobs/sync?scope=recent`
- Exits `0` in `<100ms` upon `202 Accepted` / `200 OK`, while `GET /api/status` surfaces async failures.

---

## 6. Feasible Implementation Tasks

| # | Task | Files / Packages | Verification |
| --- | --- | --- | --- |
| **T1** | **Schema v9 & Store Concurrency**: Switch to `_journal_mode=WAL`, make `DatabaseOwnership` serialize writes with an exclusive mutex, add `sync_jobs` and `auto_match_attempts` DDL + migration + `validateStoreSchema`, and add `Store` CRUD/checkpoint methods (using `sql.NullInt64` for `sync_run_id`). | `internal/companion/store.go`, `ownership.go`, `jobs.go` (new), `migration_test.go` | `go test ./internal/companion -run 'TestStore\|TestMigration\|TestSyncJob'` |
| **T2** | **Single-Shelf Batch Auto-Match & Shared Store**: Refactor `AutoMatchOwned` (or add `AutoMatchCandidate` taking an already-fetched `Book` and shared `*Store`) so Phase 1 fetches `Shelf` once, filters `mappings`/`decisions`/`auto_match_attempts` before `source.Book()`, sorts `Finished DESC, LastOpened DESC, ID ASC`, records deterministic non-matches, and supports `days=7` default. | `internal/companion/automatch.go`, `operations.go`, `automatch_test.go` | `go test ./internal/companion -run 'TestAutoMatch'` |
| **T3** | **In-Process Job Worker & Lifecycle**: Implement the FIFO background worker in `internal/httpserver` (or `internal/companion/worker.go`), startup stale-job recovery, per-batch `operationSlot` yielding, duplicate coalescing, `cancel_requested` vs `serverCtx` shutdown ordering before `owner.Close()`, and Phase 1 → Phase 2 (`ReconcileAccount` with `Advisor`) handoff. | `internal/httpserver/jobs_worker.go` (new), `server.go` | `go test ./internal/httpserver -run 'TestJobWorker\|TestServeCancellation'` |
| **T4** | **Lock-Free Reading Gateway & JSON Job API**: Remove `s.admit()` from `handleReadingSnapshot` and `handleReadingChanges`; add `POST /api/v1/jobs/sync` (empty-body query + JSON), `GET /api/v1/jobs`, `GET /api/v1/jobs/{id}`, `POST /api/v1/jobs/{id}/cancel`, and `latest_job` in `/api/status`. | `internal/httpserver/server.go`, `requestlog.go`, `jobs_test.go` (new) | `go test ./internal/httpserver -run 'TestJobsAPI\|TestReading.*DuringJob'` |
| **T5** | **Zero-Script Web UI Sync Jobs Panel**: Add `POST /jobs/sync` and `POST /jobs/cancel` CSRF/Origin-guarded form handlers, extend `handleRoot` notices, and render the Sync Recent / Sync Lifetime buttons + Jobs history table in the dashboard template. | `internal/httpserver/server.go`, `templates.go` (or HTML template), `e2e/` | `go test ./internal/httpserver` + `make e2e` |
| **T6** | **CLI & k3s CronJob Rollout**: Update `drifter-reconcile` in `vendor/harus-k3s/04-media/drifter/cronjob.yaml` to `POST .../api/v1/jobs/sync?scope=recent`, bump image version (`v0.1.5`), run `make check test` in `vendor/drifter`, build & import into k3s (`-n k8s.io`), and verify live job execution. | `vendor/harus-k3s/04-media/drifter/cronjob.yaml`, `deployment.yaml`, `SERVICE_VERSIONS.yaml` | `make check test` (`drifter`) + live `POST /api/v1/jobs/sync?scope=recent` |
