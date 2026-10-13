# Drifter: matching worklist in small deliveries

## Intent and authority

Keep [issue #7](https://github.com/azusachino/drifter/issues/7)'s matching
worklist goal, but deliver it through five independently verified slices. The
owner selected smaller slices on 2026-10-13; this is a proposed execution plan,
not approval of every design choice below. Issue #7 remains the acceptance and
live-work ledger. This document records ordering and rationale, not completion
status. The delivered first slice's [plan](read-only-matching.md) remains
historical; it is not overwritten.

Matching hints are search inputs. Only explicit owner selection may create a
local mapping. No LLM calls, new credentials, automatic linking, remote status
writes, catalog creation, daemon/admin work (#6), deployment or whole-backlog
bootstrap enters this scope. No agent reviewer or coordination service is
requested. Owner review of this plan precedes implementation.

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
above. Preserve the handoff's requirement to merge that evidence before
implementation, or obtain an explicit owner change to that prerequisite. No
private shelf captures or live provider calls are needed for this plan.

## Proposed behavior to settle before the affected slice

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
  source-book/target-account key as mappings. Labels remain user-chosen
  namespaces, not authenticated identities. Other account scopes must not hide
  a book or change its decision.
- **Decisions:** exclude and not-in-catalog suppress future pending results;
  skip survives restart as a recorded choice but does not suppress the next
  invocation. Thus #7's "no saved decision" means no suppressing decision.
  A successful link is hidden by its mapping; lookup failures and EOF/cancel
  save no terminal decision. Provide an explicit local reset of a decision so
  catalog changes or accidental exclusion do not require manual SQL.
- **Hints:** one bounded JSON file with unique book IDs and typed fields
  `book_id`, `original_title`, `original_author`, `confidence`. Validate the file
  before provider requests. Missing hints fall back to normal matching; wrong
  hints remain untrusted suggestions. Confidence never authorizes a link.
  Define the confidence representation and `--query`/`--hints` conflict policy
  before implementing slice 3.
- **Ranking:** stable partition by normalized contributor-name equality
  (trim, collapse whitespace, case-fold), not transliteration or fuzzy scores.
  Keep the same candidate IDs and preserve provider order within both groups.
  Missing authors do not count as matches. Exact-edition proposals are unchanged.

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
`cmd/drifter/cli.go`, `cli_test.go`, `README.md`.
**Dependency:** slice 1; synthetic contract for shelf/privacy/activity fields.
**Verification:** synthetic HTTP fixtures for cutoff, private/unknown privacy,
translator/no-translator/imported, pagination, malformed fields and provider
failure; temp SQLite mappings in different account scopes; focused core/CLI
race tests, then `make check`. No private captures enter fixtures.

### 3. Improve suggestions with external hints

**Outcome:** `match --hints FILE` uses the selected book's hinted title and
stably re-ranks only the resulting work candidates by author match. `pending`
and standalone `link` remain usable without hints.

**Acceptance:** the candidate set equals `match --query HINTED_TITLE`, including
ISBN-first behavior; only author grouping changes order. Missing/wrong hints
never link anything. Malformed, duplicate, oversized and conflicting inputs
fail safely without echoing file contents; low confidence is not authority.

**Likely paths:** `cmd/drifter/cli.go`, `cli_test.go`,
`internal/companion/hardcover.go`, `hardcover_test.go`, `README.md`.
**Dependency:** slice 2 for the worklist workflow; the matcher itself needs no
new stored state.
**Verification:** compare hinted and explicit-query reports against identical
synthetic HTTP responses, including missing author, equal-rank stability,
ambiguous ISBN and same-title/different-author cases; assert no local mapping
writes; focused core/CLI race tests, then `make check`.

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
`cmd/drifter/cli.go`, `cli_test.go`, `README.md`.
**Dependency:** slices 1 and 2. Exact local command syntax is settled before this
slice; avoid a daemon-facing API or general decision framework.
**Verification:** temp SQLite restart/reset/isolation and migration-chain tests,
pending CLI fixtures with each decision type; focused core/CLI race tests, then
`make check`.

### 5. Walk the worklist and explicitly confirm mappings

**Outcome:** `review` walks eligible books and displays up to three work
suggestions, with optional hints, supporting link, skip, exclude and
not-in-catalog. An empty/bounded result is not proof of global catalog absence;
not-in-catalog is an explicit owner choice.

**Acceptance:** work-only and explicit-edition selection use the exact shared
`link` source/target validation and replacement path. Failed validation stores
no mapping. Lookup failure is reported per book without a suppressing decision;
other decisions remain durable and rerunning retries the failed book. EOF/cancel
never means consent. Titles/authors rendered interactively are terminal-safe;
errors/logs contain no titles, tokens or raw bodies. No provider mutation exists.

**Likely paths:** `cmd/drifter/cli.go`, a small review helper and its tests,
`cli_test.go`, `README.md`. Extract shared confirmation only as needed here;
do not build a generic workflow engine in earlier slices.
**Dependency:** slices 3 and 4.
**Verification:** scripted input/output with fake providers and temp databases;
wrong edition membership, differing mapping replacement refusal, explicit
replacement, failure in the middle of a batch, rerun, restart and cancellation;
focused core/CLI race tests, then `make check`.

## Coverage and handoff

Every slice retains synthetic HTTP/temp SQLite tests, privacy-safe error handling
and the owning gate. Final #7 coverage: pending/classification in 2 and 4;
hints/no automatic links in 3; decision persistence in 4 and 5; exact link
validation and per-book failure recovery in 5; legacy/future database safety in
1 and 4. Full `make check` means formatting, vet, module verification,
race-enabled tests and CLI build, not a reduced substitute.

Before code, settle the proposed choices above with the owner. Keep #7 open until
all its criteria pass. Each PR records its actual revision, commands/results,
remaining criteria and next dependency; no delivery claims for later slices.
The old handoff's single `feat/matching-worklist` branch becomes successive
slice branches after approval, starting with schema versioning. Never reuse
unverified revisions or silently merge the outstanding evidence PR.
