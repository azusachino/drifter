# Drifter: matching worklist in small deliveries

## Intent and authority

Keep [issue #7](https://github.com/azusachino/drifter/issues/7)'s matching
worklist goal, but deliver it through five independently verified slices. The
owner selected smaller slices and confirmed the behavior below through a design
interview on 2026-10-13. That original approval covered recording the plan, not
implementation or merging either PR. The parent has since approved the bounded
catalog slice under the owner's existing authorization. Issue #7 remains the
acceptance and live-work ledger. This document records ordering and rationale,
not completion status. The delivered first slice's [plan](read-only-matching.md) remains
historical; it is not overwritten. The owner replaced the earlier single-file
`--hints` proposal with the
[community catalog contract](matching-catalogs.md). Parent approved the
bounded schema/loader/core/CLI slice under the existing owner authorization;
that slice is implemented and passed its owning gates on this branch. It does
not authorize automatic linking or any deferred behavior. Only
explicit owner selection may create a local mapping. The parent-approved local
export/merge extension for community catalogs is recorded as delivery 6 and is
limited to the privacy, provenance and collision contract in
[matching-catalogs.md](matching-catalogs.md). No LLM calls, new credentials,
remote status writes, catalog contribution service, HTTP server work (#6),
deployment or whole-backlog bootstrap enters this scope. No agent reviewer or
coordination service is needed for this planning pass. Pi-subagents may be
used if agent review is actually needed; no delegation has occurred. Continue in
this session on this device, without a handoff unless the owner explicitly
requests one. Remaining slices and any scope beyond the approved catalog
contract require separate approval.

## Revalidated starting point

Inspected `main` at `111f4678e00223d8e50cdf2470244bff19aa93eb`:

- `OpenStore` creates the mapping table without `PRAGMA user_version`.
- WeRead's operation allowlist has search, book info and progress only; pending
  needs a separately validated shelf reader, not just a CLI filter.
- Book IDs are numeric-only. Imported `CB_` IDs cannot use today's book/link
  validation path.
- `Hardcover.Match` tries valid ISBN variants first, then bounded title search.
  Contributor names are already available. A hint must preserve this path,
  including exact-edition short-circuiting.
- `link` fetches the source, validates target work/edition membership, then calls
  `Store.Confirm`, which enforces idempotency and explicit replacement. Review
  must share that entire confirmation path, not just the store insertion.

The issue's measured matching evidence is in
[PR #8](https://github.com/azusachino/drifter/pull/8), still open when this plan
was drafted. Its diff was inspected; it is not present on the main revision
above. The owner chose to review its evidence and checks, then request merge
authorization; do not merge it automatically. Its merge remains a prerequisite
for implementation. No private shelf captures or live provider calls are needed
for this plan.

## Owner-confirmed behavior

- **Imported books:** classify `CB_` rows as imported, exclude them by default,
  and allow explicit inclusion for worklist inspection only. Keep existing
  numeric-only `match`/`link` validation; review reports imported rows as
  unsupported rather than attempting ISBN matching. Supporting imported links
  would be a separately verified extension. This chooses the exclusion option
  offered by #7 without pretending unsupported IDs are invalid shelf rows.
- **Window/privacy:** use last-opened `readUpdateTime`, not inferred completion.
  Default to 90 days; require valid positive window input. Inject time in tests,
  define cutoff inclusivity, and reject invalid/future timestamps rather than
  invent activity. Private rows are filtered before enrichment or output unless
  explicitly included. Unknown privacy fails closed. Missing translator evidence
  must not silently become native classification.
- **Namespaces:** pending and decisions use the same provider/source-account/
  source-book/target-account key as mappings. Require explicit source and target
  account labels for pending and review. Labels remain user-chosen namespaces,
  not authenticated identities. Other account scopes must not hide a book or
  change its decision.
- **Decisions:** exclude and not-in-catalog suppress future pending results;
  skip survives restart as a recorded choice but does not suppress the next
  invocation. Thus #7's "no saved decision" means no suppressing decision.
  A successful link is hidden by its mapping; lookup failures and EOF/cancel
  save no terminal decision. Provide an explicit local reset of a decision so
  catalog changes or accidental exclusion do not require manual SQL.
- **Hints and catalogs:** the earlier single-file `--hints FILE` proposal is
  superseded by the implemented
  [catalog contract](matching-catalogs.md): explicit TOML-selected local/HTTPS
  documents, stable provider IDs, separate assertions and title/ISBN hints,
  provenance, and conflict reporting. The core match report surfaces untrusted
  evidence without selecting a target or silently replacing `--query`. Preserve
  the uncalibrated-confidence rule if later work uses hints for ranking:
  confidence never filters, ranks or authorizes a link. Delivery 6 extends this
  with explicit local export and offline merge only; its user story and exact
  flags, selection, privacy exclusions, lineage and collision rules are in the
  catalog contract before implementation.
- **Ranking:** stable partition by normalized contributor-name equality
  (trim, collapse whitespace, case-fold), not transliteration or fuzzy scores.
  Keep the same candidate IDs and preserve provider order within both groups.
  Missing authors do not count as matches. Exact-edition proposals are unchanged.
- **Review:** the owner confirms interactively in the terminal, not through an
  agent-approved decision file. Work selection records only the work; edition
  selection is separate even when an exact ISBN edition is suggested. Allow
  manual target work/edition IDs when the top three do not contain the right
  result, through the same validation as standalone `link`.
- **Partial failure:** continue after per-book provider lookup failures, preserve
  successful decisions, summarize failed source book IDs and exit nonzero.
  A failed lookup never means not-in-catalog or exclusion.

The lower-level choices below (version numbers, field decoding, normalization,
request limits and command spelling) are implementation proposals within these
boundaries. Verify the provider contracts before fixing their details; none may
weaken the confirmed behavior or owning gates.

## Ordered deliveries

Each slice gets a small owning PR and a green `make check` checkpoint. Do not
start the next slice until the preceding dependency is integrated and its
actual branch/full commit is recorded. Stop and split further if a slice cannot
be verified in one focused session. These are delivery boundaries, not parallel
agent assignments.

### 1. Version the existing database

**Outcome:** existing CLI users can open their database safely with an explicit
schema version. No new command or decision table yet.

**Acceptance:** new databases initialize at version 1; version-0 databases with
the existing mapping schema migrate transactionally without changing any
mapping; future versions are refused before application schema writes. Failed
migration leaves neither a partial schema nor an advanced version. Existing
permissions, secure-delete and account isolation remain enforced.

**Likely paths:** `internal/companion/store.go`, `store_test.go`, `README.md`.
**Dependency:** owner-approved plan and evidence prerequisite.
**Verification:** synthetic pre-versioning database with multiple namespaces;
reopen/idempotency, future-version refusal and migration rollback tests;
`go test -race ./internal/companion`, then `make check`.

### 2. List recently opened, unmapped books

**Outcome:** `pending` emits a JSON worklist using WeRead plus local mappings;
no Hardcover lookup or decision writes. Initially there are no decisions to
filter; slice 4 extends this same predicate.

**Acceptance:** window and account-scoped mapping filters work; privacy defaults
are safe; translated/native/imported classifications are correct; imported
inclusion does not broaden link validation. Decode only required shelf facts,
validate pagination/completeness, and expose incomplete acquisition instead of
claiming a complete worklist. Filter before per-book enrichment, bound requests,
and report metadata failures without misclassifying rows.

**Likely paths:** `internal/companion/weread.go`, `weread_test.go`,
`internal/cli/cli.go`, `cli_test.go`, `README.md`.
**Dependency:** slice 1; synthetic contract for shelf/privacy/activity fields.
**Verification:** synthetic HTTP fixtures for cutoff, private/unknown privacy,
translator/no-translator/imported, pagination, malformed fields and provider
failure; temp SQLite mappings in different account scopes; focused core/CLI
race tests, then `make check`. No private captures enter fixtures.

### 3. Improve suggestions with external hints

**Outcome:** the catalog slice loads explicitly selected catalogs through the
shared core `Match` operation and surfaces exact assertions and title/ISBN
hints with provenance and conflicts. It does not replace the current search
query or choose a mapping. `pending` and standalone `link` remain usable without
catalogs.

**Acceptance:** use the catalog contract's loader/security and core/CLI tests,
then the existing HTTP/browser regression gates. Preserve legacy matching with
no configured catalogs. Invalid or unavailable sources are reported incomplete
without echoing source URLs or contents; conflicts retain all alternatives; no
hint creates a mapping or provider write. Further use of hints to alter
candidate searches or ranking needs separate parent review and approval.

**Likely paths:** `internal/cli/cli.go`, `cli_test.go`,
`internal/companion/hardcover.go`, `hardcover_test.go`, `README.md`.
**Dependency:** slice 2 for the worklist workflow; catalogs add no stored state.
**Verification:** synthetic core and CLI fixtures prove source-ID matching,
separated assertions/hints, conflict preservation, incomplete-source reporting
and no mapping/provider writes. Catalog transport tests use injected resolver
and fetcher seams; run focused core/CLI race tests, then `make check`.

### 4. Persist review decisions without linking

**Outcome:** introduce a version-1-to-2 migration for the account-scoped decisions
table and a local decision/reset operation. Pending suppresses only exclude and
not-in-catalog. This makes durable choices useful before interactive review.

**Acceptance:** exclude/not-in-catalog remain hidden after restart, skip returns
next invocation, and reset restores eligibility subject to window/privacy and
mapping filters. Decisions in another namespace have no effect. Upgrade from
both version 0 and version 1 preserves every mapping; failed migration rolls
back. Persist only decision kind and minimal keyed metadata, not provider bodies,
hints, progress or credentials.

**Likely paths:** `internal/companion/store.go`, `store_test.go`,
`internal/cli/cli.go`, `cli_test.go`, `README.md`.
**Dependency:** slices 1 and 2. Exact local command syntax is settled before this
slice; avoid a daemon-facing API or general decision framework.
**Verification:** temp SQLite restart/reset/isolation and migration-chain tests,
pending CLI fixtures with each decision type; focused core/CLI race tests, then
`make check`.

### 5. Walk the worklist and explicitly confirm mappings

**Outcome:** `review` walks eligible books and displays up to three work
suggestions, with optional hints, supporting link, skip, exclude and
not-in-catalog, plus manual target ID entry. An empty/bounded result is not proof
of global catalog absence; not-in-catalog is an explicit owner choice.

**Acceptance:** work-only and explicit-edition selection use the exact shared
`link` source/target validation and replacement path. Failed validation stores
no mapping. Lookup failure is reported per book without a suppressing decision;
the batch continues, other decisions remain durable and rerunning retries the
failed book. Report failed book IDs and exit nonzero for partial failure. Work
selection never silently selects an edition. EOF/cancel never means consent.
Titles/authors rendered interactively are terminal-safe;
errors/logs contain no titles, tokens or raw bodies. No provider mutation exists.

**Likely paths:** `internal/cli/cli.go`, a small review helper and its tests,
`cli_test.go`, `README.md`. Extract shared confirmation only as needed here;
do not build a generic workflow engine in earlier slices.
**Dependency:** slices 3 and 4.
**Verification:** scripted input/output with fake providers and temp databases;
wrong edition membership, differing mapping replacement refusal, explicit
replacement, manual target ID entry, separate edition choice, failure in the
middle of a batch with later successes and nonzero exit, rerun, restart and
cancellation; focused core/CLI race tests, then `make check`.

### 6. Export and merge community catalogs locally

**Outcome:** `export` creates a deliberately public, account-scoped v1 catalog
from confirmed local mappings; `merge` combines only explicitly
named local documents into a new v1 catalog. Both write JSON to stdout and make
no provider requests. This is not anonymous sharing: export discloses selected
WeRead source IDs.

**Acceptance:** export requires exact source/target account labels, catalog ID,
publisher, provenance source/reference and accepts optional `--db`. The query
filters both labels and selects only source IDs and confirmed Hardcover work /
optional edition; no account labels, stored title/ISBN, progress, timestamps,
paths or secrets enter output. It uses read-only current-schema store access,
creates/migrates no database, errors without output for absent/unsupported
state, and emits an empty valid catalog for an existing DB with no selected
rows. Merge requires a new ID and publisher plus 2–8 repeated local `--input`
paths, validates inputs and final bytes against bundled v1, preserves all
assertions/hints/conflicts/provenance and source identity, adds lineage without
truncation, and never mutates state. Exact repeated paths, duplicate source
catalog IDs and output/source ID collision fail atomically. Record-ID collision
is resolved deterministically in input order by retaining the first ID and
SHA-256-reidentifying later collisions; same ordered inputs and metadata produce
identical output. Output budget/schema failures reject the complete result. No
schema evolution is included.

**Likely paths:** `internal/catalog`, `internal/companion`,
`internal/cli`, their tests, README, CLI/architecture docs, this contract and the
story-test map.
**Dependency:** implemented v1 catalog contract and read-only current-schema
store.
**Verification:** CLI tests prove exact selection/privacy exclusions, no-state
and empty behavior, no provider calls, atomic malformed/over-limit merge,
lineage and collision/idempotency behavior. Run focused race tests, then
`make check`, changed-Markdown lint and existing browser probe/E2E.

## Coverage and approval

Every slice retains synthetic HTTP/temp SQLite tests, privacy-safe error handling
and the owning gate. Final #7 coverage: pending/classification in 2 and 4;
hints/no automatic links in 3; decision persistence in 4 and 5; exact link
validation and per-book failure recovery in 5; legacy/future database safety in
1 and 4. Full `make check` means formatting, vet, module verification,
race-enabled tests and CLI build, not a reduced substitute.

The owner-confirmed worklist behavior and parent-approved catalog extension
remain separate delivery scopes. This approval covers only delivery 6 and does
not authorize the deferred worklist review UI, HTTP changes, provider writes or
schema evolution. Keep #7 open until all its criteria pass. Each PR records its
actual revision, commands/results and remaining criteria; do not claim later
deliveries. Parent review is required before PR12 publication. No continuing
handoff is needed during this same-session, same-device work unless explicitly
requested.
