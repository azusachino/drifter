# Drifter design review: 2026-10-15

## Scope and evidence

Source-only assessment using the workstation proposal-audit playbook, at tier 0.
Reviewed `main` at `ca5b47760cdcd53e45cf028c2762a3d7995fd452`, with a clean
working tree before this report. This report is the only intended change; no
implementation, accepted contract, issue state or remote data was changed.

Inputs: [architecture](../architecture.md), [domain glossary](../../CONTEXT.md),
[provider research](reading-status-companion.md), the matching worklist,
catalog and HTTP plans, shared core/store/provider code, HTTP handlers/templates,
and existing synthetic tests. Read the full bodies of the repository's listed
issues and the comments on [the server proposal](https://github.com/azusachino/drifter/issues/6)
and [the worklist proposal](https://github.com/azusachino/drifter/issues/7).
[The merged server PR](https://github.com/azusachino/drifter/pull/12) describes
its bounded foundation, not completion of synchronization or review.

No live provider calls, credentials, personal state, browser session, build or
runtime tests were used. Findings below follow from source inspection, not
fresh runtime reproductions. Prior test evidence is not a new verification run.

## Assessment

The overall shape is appropriate: CLI and HTTP adapters share the same core;
identity confirmation is separate from status authority; work-only selection is
explicit; provider clients are read-only; browser mutations have Host, Origin
and CSRF checks; SQLite has versioning and process ownership. No framework,
plugin system, event bus or background worker is needed to close these gaps.

### 1. P2: define a whole-request budget and bounded provider admission

Evidence: `internal/httpserver/server.go:134-135` sets a two-minute socket write
timeout, but `handlePending` passes the request context without an operation
deadline (`:280-281`). `internal/companion/pending.go:101,156-184` permits 100
sequential metadata calls. `internal/companion/http.go:31,39-54` applies a
30-second timeout per HTTP call and holds a mutex across pacing and the request;
waiting for that mutex is not context-aware.

One slow worklist can exceed the response write deadline, and concurrent
worklists share a serialized provider client without a bounded queue. A write
timeout is not an overall work deadline. Cancellation while queued cannot be
observed until the mutex becomes available. Repeated local submissions can
therefore consume work that cannot be returned usefully and delay confirmation.

Recommendation: define a small, explicit provider-backed operation deadline
shorter than the response deadline, use context-aware bounded admission, and
return a sanitized busy/timeout outcome. Define whether a timed-out worklist
returns completed rows with an incomplete marker or no report. Keep per-call
limits; do not solve this by merely raising the socket timeout or adding jobs.

Acceptance: with a blocked synthetic provider and a second queued request,
cancellation/deadlines finish promptly, excess work is refused predictably,
provider-call counts stay bounded, and local listing/probes remain usable.

### 2. P2: explicit replace/unlink still needs stale-state protection

Evidence: `internal/companion/store.go:221-255` accepts a boolean replacement
and performs an unconditional upsert; `:298-307` unlinks by namespace key only.
`internal/httpserver/templates/index.html:26-39` submits neither the displayed
target tuple nor a mapping revision with replacement/unlink forms.

Two tabs can display mapping A. After one changes it to B, the stale tab can
replace or unlink B while the owner is still acting on A. A stale replace can
also recreate a mapping that was deliberately unlinked. The process lease
protects against competing processes, not stale intentions within one server.

Recommendation: carry the expected existing work/edition tuple, or a mapping
revision, through core and store operations. Check it atomically and return a
conflict for a changed target or a vanished replacement. An already absent
unlink and identical confirmation can remain idempotent. The guarded contract
needs owner acceptance before implementation.

Acceptance: two synthetic sessions cannot overwrite/delete a changed mapping;
a stale replace cannot resurrect a deleted row; a fresh explicit change works.

### 3. P2: bounded pending selection has no continuation path

Evidence: `internal/companion/pending.go:145-155` sorts newest-first and cuts to
100 before enrichment. Failed metadata and unknown translator rows are omitted
from books but remain eligible (`:167-175`). Neither `PendingOptions` nor the
CLI/HTTP input has an offset, cursor or upper activity bound.

With more than 100 eligible rows, repeatedly unresolvable recent books can
occupy the same batch forever. Narrowing the window cannot expose older rows.
The incomplete marker honestly reports truncation but does not provide progress.
Saved decisions will help with explicitly excluded books, but are still deferred
and do not themselves specify continuation across failed enrichment.

Recommendation: add an explicit bounded continuation or older-than selection
with documented behavior if the live shelf changes. Retain filtering before
enrichment and the privacy defaults; no persisted shelf archive is necessary.

Acceptance: with 101 eligible books and failures in the first batch, an explicit
next request reaches the older book without mapping/excluding failed books or
re-fetching every preceding row's metadata.

### 4. Owner decision: public export consent is account-wide, not per book

Evidence: `internal/companion/store.go:275-295` selects all mappings for the
chosen account pair. `internal/companion/catalog_export.go:23-64` publishes
every selected source ID and target tuple. `Mapping` (`store.go:31-40`) has no
privacy or sharing-consent field; confirmation reads book metadata, not shelf
privacy (`operations.go:146-170`).

A privately read book can be manually linked. A later account-wide catalog
export includes that identity alongside public mappings; omission of titles
does not hide which book it is. The current
[catalog contract](../plans/matching-catalogs.md#explicit-local-catalog-tools-export-and-merge)
explicitly approves whole-pair export and warns that IDs are public. This is a
trade-off in the accepted design, not an implementation violation or a claim
that export runs automatically.

Decide whether that consent granularity is sufficient. Safer sharing could
require an explicit source-ID allowlist or reviewed selection, with account-wide
sharing remaining a clearly named deliberate choice. Do not silently add live
privacy lookups to the approved offline export. If retained, make the
all-mappings scope prominent before the command example.

Acceptance for a narrower policy: an unselected privately linked book never
appears in a synthetic export; explicitly selected identities still export
offline with the existing provenance and schema guarantees.

## Already recorded work, not new omissions

- **Finish the matching loop before status automation.** Durable decisions/reset
  and interactive review are specified in deliveries 4 and 5 of the
  [worklist plan](../plans/matching-worklist.md). The browser currently returns
  pending JSON and requires manually supplied target IDs; it has no matching or
  candidate-review route. This is an unfinished product journey, not evidence
  that those requirements were forgotten. The owner-confirmed worklist plan
  already selects terminal-first review; retain that choice rather than
  implementing a second browser review interface in the same milestone.
- **Account identity remains a write prerequisite.** The server proposal's
  owner comments already identify the review-free WeRead account fallback as
  unresolved. Choose a verified alternative or fail closed for that account;
  a user-chosen label is not a fallback identity proof. This is a known open
  decision, not a reason to weaken fingerprint requirements silently.
- **Synchronization safety is described but not delivered.** The research and
  server decisions already cover per-target authority, durable holds, monotonic
  run ordering, baselines, privacy, durable intents/receipts, uncertain outcomes,
  three-way conflicts, quota-aware reads and local-only unlink. They still need
  a finite accepted write contract and authorized provider experiments. None is
  required to call the present local mapping slice complete.
- **Public catalogs are not backups.** A catalog deliberately omits personal
  namespaces and other state. Lossless fingerprinted mapping import/export is
  deferred in the server proposal. The CLI reference warns about consistent
  private SQLite backups but supplies no concrete restore walkthrough. Add and
  exercise that procedure before relying on unattended persistent deployment;
  do not imply the catalog format restores personal state.

## Historical user-story coverage follow-up

The denominator matters. The original research defines twelve reading-status
stories; the current HTTP plan defines nine much smaller local-mapping stories.
Coverage of the latter is not completion of the former. The source revision is
unchanged for this follow-up, and runtime tests were not rerun.

### Original reading-status stories

Assess each story against its complete concrete acceptance case, not just its
headline. None of the twelve has a complete delivered end-to-end journey yet:
four have useful supporting functionality, one has its architectural separation
in place, and seven have no delivered outcome.

| Story | Coverage now | Remaining acceptance |
| --- | --- | --- |
| US1: safe first status sync | Not delivered | Bootstrap preview, account identity, approved writes, unrelated-field verification and baseline. |
| US2: correct multilingual matching | Partial | Explicit work/edition matching and persisted mappings exist; no post-link status bootstrap or fresh target comparison for a changed status-sync mapping. |
| US3: incremental updates | Not delivered | Observation comparison, no-op replay, writes and new-book bootstrap. |
| US4: rereading | Not delivered | Verified reread evidence, ordering policy and safe Read-to-Reading transition. |
| US5: protect manual target edits | Not delivered | Target snapshots, three-way comparison, holds and approved resolution. |
| US6: prevent two-source conflict | Not delivered | KU intake and per-target status authority enforcement. Namespace isolation alone is not authority. |
| US7: interrupted-sync recovery | Not delivered | Durable write intents, uncertain-outcome reconciliation and verified receipts. Local SQLite durability is not remote-write recovery. |
| US8: private reading | Partial | Pending privacy filtering, private state and sanitized transport/logging exist; private-target exception and outbound status privacy policy do not. |
| US9: KU loans versus reading | Not delivered | No KU/manual status adapter or synchronized owner-declared status. Honest non-support is documented. |
| US10: freshness and attention | Partial | Incomplete pending/catalog reports and local probes exist; no sync freshness, retry timing, run outcomes or holds. |
| US11: nondestructive removal/disconnection | Partial | Unlink is local-only; no account disconnect/reconnect identity and baseline reconciliation journey. |
| US12: independent Iroha boundary | Architecture in place | No Iroha call or coupling was introduced; no verified Drifter status update exists to exercise downstream observation. |

Evidence for the partial work includes core
`TestCoreConfirmationAndPendingWithoutCLI`, store persistence/account-scoped
unlink tests, pending privacy/partial-result tests, catalog incomplete-result
tests and HTTP sanitized-request-log tests. There are no status-write, baseline,
authority, hold or remote-recovery implementations to test. These are intentionally
deferred capabilities, not regressions in the read-only slice.

### Current smaller deliveries

- **Local HTTP mapping slice:** all nine stories in the
  [HTTP acceptance map](../plans/http-server.md#user-stories-and-executable-test-map)
  have implementations and named synthetic tests. They cover startup/shutdown,
  credential-free listing, explicit pending, confirm/replace/unlink, browser
  request protection, CLI/server ownership and in-flight cancellation. This is
  source-backed coverage, not a fresh nine-of-nine runtime pass or proof against
  the additional deadline/stale-tab cases identified above.
- **Browser evidence:** three committed journeys in
  `e2e/mapping-interface.spec.ts` cover mapping maintenance, native-form Origin
  with no script requests, and narrow layout. They do not exercise the full
  pending-to-search-to-hint-to-candidate-to-confirm journey, failed candidate
  validation in the browser, restart persistence, decisions or partial-batch
  recovery. API/core tests cover some of those lower-level behaviors; they are
  not browser journey evidence.
- **Matching plan:** schema versioning, pending selection, catalog evidence and
  local catalog export/merge are implemented: deliveries 1, 2, 3 and 6 under the
  current contracts. Deliveries 4 and 5, durable decisions/reset and interactive
  batch review, remain missing. That is four delivered slices out of six, not a
  meaningful percentage of user value or of the original issue's acceptance.
  Catalog evidence is surfaced; hint-driven search/ranking is not implemented.

The most valuable next acceptance journey is: open pending books, inspect useful
original-title/candidate evidence, explicitly choose work-only or an edition,
link or record skip/exclude/not-in-catalog, restart, and resume without losing
choices. Include wrong-edition, unavailable provider, empty search, cancellation
and partial-batch failure branches. Follow the recorded terminal-first decision
and retain the shared core. A public catalog tool and mapping maintenance form
do not complete that review journey.

## Documentation drift

The catalog plan still says parent review is required before publishing PR12,
even though that PR is merged. The worklist plan also retains branch-local
approval text. The HTTP plan's final non-implementation list says no import/export
or hints without qualifying HTTP scope, while catalog export/merge and hints now
exist in the CLI. Preserve historical evidence, but distinguish current
capabilities, historical approvals and deferred HTTP/personal-state features.
These are bookkeeping corrections, not grounds for new architectural work.

## Recommendation and verification

The [actionable matching and status plan](../plans/matching-worklist.md) turns
these findings into delivery dependencies, falsifiable user-story acceptance,
approval boundaries and later status-sync gates. That plan owns next actions;
this report remains point-in-time evidence, not a second task ledger.

Prioritize bounded provider-backed HTTP work, then stale-state guards and a
pending continuation path. Finish the already planned review/decision slice
before adding status automation. Settle export consent before encouraging
community sharing. Keep live writes and deployment separately gated.

Report checks: `mise exec -- rumdl check
 docs/research/2026-10-15-design-review.md` and `git diff --check` passed.
Code and runtime gates were not run for that source-only review because no code,
configuration or behavior changed. The subsequent A1 implementation below has
separate runtime evidence. Neither phase claims independent agent verification.

## A1 implementation checkpoint

The owner subsequently selected native Pi subagents using
`openai-codex/gpt-6-luna` at medium effort for the work. The bounded A1 transport
slice was implemented by one worker and inspected by the parent. Tested code:
`fix/provider-cancellation` at `87006388ebf3a053260c6b43de1373abc39878af`, based
on `ca5b47760cdcd53e45cf028c2762a3d7995fd452`.

Owned code paths:

- `internal/companion/http.go`: replace the uncancelable mutex wait with a
  context-aware per-client gate, retaining serialized pacing, cancellation
  checks, response bounds, timeouts, sanitized errors and no automatic retry.
- `internal/companion/http_test.go`: hold one synthetic request in flight,
  cancel another, assert `context.Canceled` and no second provider call, then
  prove gate reuse. Cleanup always releases the held handler on failure.
- `internal/httpserver/server_test.go`: close idle test-client connections
  before canceling the server in fixture cleanup. Existing stop/security/state
  assertions and production behavior are unchanged.

### Failure classification and correction

The first `make check` stopped at
`TestMutationRejectionsPrecedeProviderCallsAndStateWrites`: the helper reported
`server did not stop` after one second. The test uses fake source/target
implementations and asserts zero provider calls; it does not exercise the
changed concrete transport. A bounded race-enabled reproduction failed 3/20
runs on the candidate and 3/20 with an overlay restoring the exact baseline
transport. This establishes that A1 is not required to reproduce the failure.

A diagnostic connection-state overlay observed a connection in `StateNew` at
the failed one-second check. Production retains its five-second read-header
and ten-second shutdown bounds. A separate eleven-second observation passed
20/20 but was diagnostic only, never an accepted timeout relaxation.

The parent authorized test-client idle-resource cleanup, not production changes
or relaxed assertions. After that minimal correction, the targeted test passed
20/20 at its unchanged one-second assertion. Diagnostic snapshots can still
observe transient `StateNew` connections; the precise origin/lifetime of every
such connection was not established. The evidence supports a pre-existing
fixture cleanup race and a useful resource-lifecycle correction, not proof that
all possible HTTP shutdown races are solved.

### Commands and outcomes

All commands ran from Drifter with its pinned toolchain. `$overlay` below means
a local diagnostic Go overlay under workstation
`.tmp/drifter-provider-cancellation/`, restoring only baseline transport;
it is not a committed configuration or required future dependency.

| Check | Outcome |
| --- | --- |
| `mise exec -- go test -race -overlay "$overlay" ./internal/companion -run '^TestCanceledSerializationWaitDoesNotSendAndGateCanBeReused$' -count=1 -timeout=10s` | Expected red against baseline: canceled waiter remained blocked; hardened cleanup let the test fail cleanly rather than hang. |
| `mise exec -- go test -race ./internal/companion -run '^(TestCanceledSerializationWaitDoesNotSendAndGateCanBeReused\|TestCanceledSpacingDoesNotSendRequest)$' -count=1 -timeout=20s` | Passed against candidate. |
| `mise exec -- go test -race ./internal/companion` | Passed during the first worker run and again through the final full gate. |
| `mise exec -- go test -race ./internal/httpserver -run '^TestMutationRejectionsPrecedeProviderCallsAndStateWrites$' -count=20 -timeout=60s` | Before fixture correction: 3/20 failures; after correction: 20/20 passed. Baseline-transport overlay also reproduced 3/20 before correction. |
| `make check` | Final run passed formatting, vet, module verification, race suite and both builds. Some unchanged packages reused Go's valid cache. Initial failing run remains recorded above. |
| `make e2e-probe ROUTE=/` | Passed one synthetic browser probe with no page errors. |
| `make e2e` | Passed all three existing synthetic browser journeys. |

The parent inspected the source diff and saved worker command output, rather
than rerunning unchanged passing suites. Provider calls were synthetic; no live
credentials, personal state, provider mutations or deployment were used. The
worker made no commits; the parent committed the inspected code checkpoint.
The later plan/report changes are documentation only and use pinned Markdown
lint plus `git diff --check`.

A1 is complete at this bounded checkpoint. A2's total HTTP deadline/admission
policy and the rest of the matching milestone remain separate work. The full
user-story coverage assessment above is not upgraded to completed sync merely
because this transport prerequisite passed.

## Integrated matching follow-up

The source-only assessment above and A1's separate historical runtime evidence
remain unchanged in meaning. They are not the current completion ledger. PR 13's
component source through `4002a6d51e87ca21249f72cfc1383227836ba6e0` now supplies
HTTP admission/deadlines, atomic expected-target guards, pending continuation,
v2 decisions/reset, terminal explicit linking and opt-in hint query/author use.
The [integrated acceptance record](../plans/matching-worklist.md#integrated-milestone-1-checkpoint)
records current combined commands and M1–M7 evidence. These are synthetic
implementation/self-review results, not independent or live-provider verification.

Findings 1–3 have bounded implementations and executable acceptance, including
malformed new expected-tuple fields and changed-live-shelf limitations. Durable
choices and terminal review complete the local matching journey, not all twelve
original status stories. US2 has stronger matching evidence; post-link bootstrap
and every status-write acceptance case remain gated. Browser coverage is still
mapping maintenance, not terminal-review evidence. Public sharing consent in
finding 4, account identity/status contracts, private backup/restore/import,
KU and deployment remain unresolved/deferred under their owning plan. Catalog v1
and offline account-wide export consent have not changed.
