# Drifter: actionable matching and status milestones

## Intent, authority and record ownership

Turn the [design review](../research/2026-10-15-design-review.md) and its
user-story coverage assessment into small, testable deliveries. The first
milestone completes this reader journey:

> List recent books, inspect useful identity evidence, explicitly link or record
> a review decision, restart, and resume without losing choices.

The owner requested an actionable plan on 2026-10-15. That authorizes this
planning revision, not implementation, provider mutations, deployment, merging,
new agents or changes to accepted sharing policy. The workstation task playbook
owns this tier-1 planning deliverable. Implementation involving a migration or
changed interface follows intent-to-verified-result at tier 2.
The subsequent owner request selected native Pi subagents with Luna 6 at medium
effort for implementation; the bounded A1 result is recorded in the handoff below.
That checkpoint does not claim completion of later interface changes. The owner
subsequently requested "finish all in this PR": A2-G of milestone 1 are now
approved for implementation in PR 13, including the proposed interface and
migration contracts below. Later status-sync milestones and deferred sharing,
backup/import and deployment decisions remain outside that authorization.

This revises the existing incomplete plan in place. The
[matching worklist issue](https://github.com/azusachino/drifter/issues/7) remains
the acceptance/live-work ledger for matching; the
[server and future sync proposal](https://github.com/azusachino/drifter/issues/6)
owns the related operator-surface decisions. Do not create a parallel local
status checklist or close either issue from this plan alone. Before a new
implementation slice, reconcile its acceptance with those records. Changes to
existing issue bodies/comments require owner authorization; new distinct work
must be checked for duplicates before filing.

The plan defines ordering and acceptance, not live completion status. PRs must
record the exact tested revision and results. Historical approval and test
claims belong to their original PRs, not to a new implementation checkpoint.
The original plan is retained in Git history; the
[first read-only plan](read-only-matching.md) remains historical.

## Historical planning baseline

Planning source: `main` at `ca5b47760cdcd53e45cf028c2762a3d7995fd452`.
The only pre-existing uncommitted work was this session's design-review report.
No source code changed during planning; no remote refs were fetched or updated.

Implemented prerequisites at this source:

- Version-1 SQLite mappings and transactional legacy migration.
- Privacy-filtered, recently opened pending selection with explicit incomplete
  reports and a 100-row enrichment bound.
- Exact-identifier and bounded title candidates, always requiring confirmation.
- Optional untrusted catalog evidence through shared core matching.
- Explicit confirm/replace/unlink in CLI and foreground HTTP, with shared
  validation and process-lifetime server ownership.
- Offline public catalog export/merge. The
  [catalog contract](matching-catalogs.md) remains authoritative and unchanged.

These correspond to the earlier deliveries 1, 2, 3 and 6. Durable review
choices and terminal batch review, earlier deliveries 4 and 5, are not
implemented. Catalog evidence does not yet drive search or author ranking.
The foreground server is not a status-sync daemon. Existing tests and earlier
PR evidence are not a fresh test run for this plan.

## Carry forward accepted behavior

These are existing owner decisions, not new assumptions:

- **Terminal-first review.** The recorded matching decision selects interactive
  terminal confirmation, not an agent-approved file. Do not reopen the adapter
  choice or build a second browser review interface in this milestone.
- **Identity is not authority.** Only explicit owner selection confirms a
  mapping. A work-only link never silently acquires an edition or permission to
  change reading status. Source and target existence/membership validation is
  shared with standalone linking.
- **Privacy and activity.** Use last-opened `readUpdateTime`, default 90 elapsed
  days and inclusive cutoff. Private rows are excluded before enrichment/output
  unless explicitly included; unknown privacy always fails closed. Missing
  translator evidence is not native classification. Future/invalid activity
  stays explicit rather than becoming inferred reading activity.
- **Imported IDs.** `CB_` rows are excluded by default and may be inspected when
  explicitly included. They remain unsupported for matching/linking; review
  reports that limitation instead of fabricating ISBN/work identity.
- **Namespaces.** Mapping and decision keys include provider, source account,
  source book and target account. Labels are operator-selected namespaces, not
  verified account identities. Other namespaces must not affect eligibility.
- **Decisions.** Exclude and not-in-catalog suppress subsequent pending results.
  Skip is recorded durably but returns next invocation. Reset reverses a saved
  choice subject to the ordinary filters. A link is hidden by its mapping.
  Lookup failure, EOF and cancellation never create a terminal decision.
- **Evidence.** Catalog assertions/hints remain distinct and untrusted, with all
  conflicting alternatives and provenance visible. Confidence never selects,
  filters, ranks or authorizes a target. There are no LLM calls or new secrets.
- **Ranking.** If enabled in an approved suggestion slice, stable-partition the
  same candidate IDs by contributor-name equality after trim, whitespace collapse
  and case-folding. Preserve provider order in each group; missing authors do
  not match. Do not transliterate, use fuzzy scores or change exact editions.
- **Failures.** Continue past per-book provider failures, preserve completed
  choices, summarize failed source IDs and exit nonzero. Failed or bounded
  searches never prove catalog absence. Logs/errors omit titles, credentials
  and raw bodies; interactive metadata must be terminal-safe.

## Milestone 1: complete local matching review

### Acceptance map

| ID | Reader outcome | Required evidence | Delivery |
| --- | --- | --- | --- |
| M1 | Reach older eligible books despite recent failures. | More than 100 eligible rows; continuation reaches later rows without linking or excluding failed books. | C |
| M2 | Save and reverse review choices across restart. | Decision persistence, reset, namespace isolation and migration rollback. | D |
| M3 | Inspect suggestions without accepting them accidentally. | Explicit alternate query, untrusted hints, unchanged candidate set under author ranking and no automatic link. | E, F |
| M4 | Confirm the intended work/edition safely. | Shared validation, separate edition choice, manual IDs, wrong-edition rejection and guarded replacement. | B, E |
| M5 | Resume after cancellation or partial failure. | Scripted restart/retry journey with middle failure and later success; EOF saves no choice. | G |
| M6 | Keep the current server responsive and mutations intentional. | Operation deadline, bounded admission, canceled pacing wait and stale-tab conflicts. | A, B |
| M7 | Preserve privacy and exclusive state ownership. | Private/unknown privacy fixtures; writer refusal before provider calls; read-only readers still work. | C, D, E, G |

This milestone advances the original matching, privacy, attention and local
removal stories. It does not complete any status-write acceptance case.

### Dependencies and delivery rule

```text
A1 provider cancellation -> A2 HTTP operation budget
B1 guarded store changes -> B2 guarded HTTP forms/API
C1 continuation core     -> C2 pending CLI/HTTP inputs
D1 decisions/migration   -> D2 local commands/pending filtering
B1 + C + D               -> E terminal review -> F explicit hint use -> G acceptance
```

Execute sequentially in this order: A, B, C, D, E, F, G. These are delivery
boundaries, not parallel assignments. A numbered subdelivery is independently
verified and integrated before its successor; split again if it exceeds one
focused session. Keep source stable at each acceptance checkpoint. No general
workflow engine, background queue, stored shelf archive or new framework.

The command spelling, bounds and interface changes below were proposals during
planning and are now accepted for milestone 1 by the owner's PR 13 completion
request. Historical approval annotations below are satisfied by that request;
they are not a reason to pause again before each accepted delivery. They do not
claim that the capabilities are already implemented or approve later milestones.

### A. Bound provider-backed work

**A1 — cancellation-aware transport.** Replace the uncancelable mutex wait with
a context-aware serialization/pacing gate. Preserve fixed destinations,
per-client pacing, response bounds, no redirects and no automatic retry.

**A2 — HTTP budget.** Propose one active provider-backed operation per server,
no waiting queue, and a 90-second total deadline, below the existing two-minute
response write timeout. Busy requests return sanitized 503 before provider
calls; timed-out operations return a stable timeout outcome, not success. Local
page/list/probe routes do not consume this slot. Pacing still applies across
operations; these limits are not a provider quota guarantee.

- Acceptance: a blocked fake provider plus a canceled waiter finishes promptly;
  excess requests make no provider calls; a timed-out pending report is explicitly
  incomplete and preserves completed rows when still returnable. Never report a
  confirmation as successful without checking its local result.
- Paths: A1 `internal/companion/http.go` and transport tests; A2
  `internal/httpserver/server.go`, handler tests and HTTP contract documentation.
- Verify: A1 `mise exec -- go test -race ./internal/companion`; A2 also
  `mise exec -- go test -race ./internal/httpserver`, then owning gates below.
- Approval: accept the proposed budget/admission/error contract before A2.

### B. Refuse stale mapping intentions

**B1 — core/store precondition.** Add an expected existing work/edition tuple
for guarded replace/unlink. Compare and mutate atomically. A guarded replace
updates an existing row; it must not upsert after another action deleted it.
A changed row, or a vanished row on replacement, reports conflict without
changing state. An already absent unlink remains idempotent; it cannot affect a
new target. Keep ordinary confirmation idempotent. A tuple precondition avoids
adding a second schema migration just for revisions.

**B2 — HTTP adapter.** Include the displayed expected tuple in native forms
and require it for JSON replace/unlink. Return 409 with a reload instruction on
conflict. Never silently fall back to an unconditional write. Keep Host,
Origin, strict body and CSRF checks ahead of provider calls. Existing explicit
standalone CLI replacement/unlink semantics remain documented; terminal review
uses the guarded path because it presents previously observed state.

- Acceptance: two sessions reading A cannot unknowingly replace/delete B; stale
  replacement cannot resurrect a deleted mapping; a fresh change and identical
  confirmation still work. Wrong edition leaves state unchanged.
- Paths: B1 `internal/companion/store.go`, shared confirmation operations and
  focused tests; B2 handlers, template, adapter tests and HTTP payload reference.
- Verify: B1 `mise exec -- go test -race ./internal/companion`; B2 also
  `mise exec -- go test -race ./internal/httpserver` and native-form Playwright.
- Approval: B2 changes the existing JSON mutation contract; record acceptance
  and examples before making fields required. No catalog schema change.

### C. Add bounded worklist continuation

**C1 — selection contract.** Propose a versioned seek cursor binding the fixed
as-of time, window, account scope, inclusion flags and last attempted sort tuple
(last-opened descending, book ID ascending). Validate it as untrusted input.
Apply existing privacy/mapping filters before enrichment; D later extends this
same predicate with decision suppression. Cap each page at 100.
Advance across attempted failures, not just returned books, so one bad batch
cannot hide all older books. Never advance past rows not yet attempted.

This is a live shelf scan, not a snapshot guarantee: a book moved ahead of the
cursor by new activity, or made newly eligible by a decision reset, may require
a fresh scan. No cursor persists provider bodies or authenticates the caller.
Treat source IDs/account scope as personal output; do not log cursor values.
A scan's fixed cutoff does not drift between pages. Incomplete shelf acquisition
remains incomplete even at the final page.

**C2 — adapters.** Add proposed `pending --after CURSOR` and JSON `next_cursor`,
with the matching strict HTTP query field. Preserve the current nonzero CLI
exit/incomplete report behavior when more work or failures remain. Document
continuation and retry separately: continuing may pass a failed row; a new scan
or explicit retry revisits it.

- Acceptance: 101+ rows, equal-time ties, failures in the first page, changed
  shelf activity, wrong-scope/malformed cursor and cancellation before the page
  ends. Later pages do not re-enrich all prior rows or reveal private rows.
- Paths: C1 `internal/companion/pending.go` and selection tests; C2 pending CLI,
  HTTP query parsing, adapter tests and CLI contract.
- Verify: `mise exec -- go test -race ./internal/companion ./internal/cli
  ./internal/httpserver`, then owning gates.
- Approval: accept the cursor/report extension before implementing C2.

### D. Persist decisions and expose local controls

**D1 — state.** Migrate version 1 to version 2 transactionally, retaining legacy
version-0 upgrade support. Store only the namespace key and decision kind plus
necessary schema metadata, never shelf snapshots, titles, hints, progress or
credentials. Preserve every mapping. Refuse future versions before application
schema writes; failed migration rolls back both tables and version.

**D2 — reader-facing controls.** Proposed commands:

```text
drifter decide --book-id ID --account SOURCE --target-account TARGET \
  --decision skip|exclude|not-in-catalog [--db PATH]
drifter decisions --account SOURCE --target-account TARGET [--db PATH]
drifter reset-decision --book-id ID --account SOURCE --target-account TARGET \
  [--db PATH]
```

Shared pending selection suppresses only exclude/not-in-catalog. Skip returns
next invocation. Resetting an absent choice is idempotent. Linking and unlinking
do not silently rewrite an independent decision; if an older suppressing choice
still applies after unlink, surface it with the exact reset command. These local
commands require no providers. Readers use the existing current-schema read-only
path under server ownership; standalone decision writes refuse an active owner.

- Acceptance: fresh/version-0/version-1 upgrade, rollback, reopen, future-schema
  refusal, every decision/reset type, other-account isolation, unknown decision
  rejection and no provider construction. Exclusions may affect pending only
  inside their exact namespace. Old binaries refuse version 2 honestly.
- Paths: D1 store/migration/decision tests; D2 CLI decision handlers, shared
  pending orchestration, focused tests and CLI reference.
- Verify: `mise exec -- go test -race ./internal/companion ./internal/cli`, then
  owning gates. Back up privately before migration; no downgrade promise.
- Approval: migration and exact command contract are accepted before D1/D2.

### E. Deliver the smallest terminal review loop

Proposed entrypoint:

```text
drifter review --account SOURCE --target-account TARGET [--days 90] \
  [--db PATH] [--config PATH]
```

Use the same pending predicate and continuation, and acquire exclusive database
ownership before provider calls or prompts. A running server or another review
causes immediate refusal with a useful instruction, not forwarding or lock
bypass. Stop the foreground server before terminal review. Hold the lease
through review and release it on every exit; read-only CLI listing remains
possible against the current schema. Resolve the database path once and use it
for selection, decisions and confirmation; document its config/flag precedence.

Construct providers lazily. Show terminal-safe source metadata and up to three
work suggestions with visible author/IDs; also show exact-edition proposals as
unconfirmed evidence. Offer link, skip, exclude, not-in-catalog and quit. Permit
manual target work/edition IDs. Choose work-only versus a specific edition
separately, display the final selection and require explicit confirmation. Reuse
the complete shared source/target validation and guarded replacement path.

- Acceptance: script pending -> work-only link -> explicit-edition link ->
  persisted state; rejected membership stores nothing; no-match can be skipped,
  excluded or manually resolved. Imported rows cannot enter linking. EOF,
  default input, provider errors and cancellation never mean consent.
- Paths: small `internal/cli/review.go` and tests, command registration, shared
  operations only where needed, and CLI usage documentation. No review engine.
- Verify: `mise exec -- go test -race ./internal/cli ./internal/companion`, then
  owning gates. Use fake providers and temporary SQLite, not captured accounts.
- Approval: approve the foreground ownership/command contract before E.

### F. Make original-title evidence useful without automatic search

Within review, display configured catalog hints, conflicting assertions and
provenance as unverified evidence. Let the owner explicitly choose a hinted
title/author or enter an alternate query before issuing a new bounded search.
Do not silently replace the native query, turn an assertion into a selection,
search every hint, or use confidence to rank anything. Missing hints preserve
the ordinary matching path. Invalid catalogs remain visibly incomplete.

Place optional author-aware stable partitioning in shared matching logic, not
in a CLI-only matcher. Preserve candidate IDs and within-group provider order;
exact-edition results remain unchanged. Keep standalone `match` unchanged unless
its new explicit option is separately included in the approved contract.

- Acceptance: absent/wrong/conflicting hints never link; a selected title makes
  the same provider query as explicit `match --query`; author reordering neither
  drops/adds candidates nor changes exact editions. Terminal escape sequences in
  source or catalog text cannot execute. No new credential or LLM dependency.
- Paths: shared match options/operations and tests, review prompt and tests;
  catalog schema/loader/security policy remain unchanged.
- Verify: `mise exec -- go test -race ./internal/catalog ./internal/companion
  ./internal/cli`, then owning gates.
- Approval: earlier catalog approval covered surfacing only. Explicit search
  reuse and ranking require acceptance before F, even though their direction
  is recorded in the original matching decisions.

### G. Prove restart, failure and resume as one reader journey

Add scripted end-to-end CLI journeys, not merely a list of unit-test names:

1. Review a synthetic batch: link A, skip B, exclude C, mark D not-in-catalog;
   close all handles and start a fresh invocation against the same private DB.
2. Verify A/C/D are absent and B returns; reset C and verify eligibility returns
   only in that account pair. Reach an older page despite a failed recent row.
3. Fail lookup/validation in the middle, then successfully process a later book.
   Preserve successful choices, report failed source IDs and exit nonzero.
   Retry resolves only pending work; EOF/cancel adds no choice or mapping.

Propose a 60-second deadline for each provider-backed review stage (candidate
lookup or final confirmation), retaining individual call bounds. A stalled
provider must not trap the entire review indefinitely, but time spent waiting
for owner input is not a provider timeout. If cancellation affects later work,
summarize completed and unattempted items honestly. Prove ownership release after
cancellation, provider failure and input failure. No hidden remote writes or
sensitive logs.

- Paths: review orchestration/tests and
  [executable walkthroughs](../walkthroughs/README.md), with exact synthetic
  reproduction commands and expected outcomes. Document existing browser
  journeys as regression coverage, not terminal-review evidence.
- Verify: focused uncached review tests, full `make check`, changed-doc lint,
  `make e2e-probe ROUTE=/`, and `make e2e`. Update the acceptance/story map with
  actual test names and revisions, not proposed names treated as passing tests.
- Exit: owner reviews M1-M7 evidence. Matching work is complete only when the
  owning acceptance record agrees, including its superseded hints contract.

## Execution ownership for PR 13

The remaining implementation is multi-seam. Run the component owners serially
in the isolated `work/pr13-matching-milestone` checkout, handing off a clean,
tested local commit after each stage. The published `fix/provider-cancellation`
checkout is integration-only until those handoffs are ready. Shared files have
one writer at a time; no concurrent writer or issue-wide implementation owner.
The initial route used native Pi subagents with `openai-codex/gpt-6-luna:medium`.
The owner subsequently approved GPT-6.1 Sol medium for remaining E2–G and
integration work; see the historical route-resolution checkpoint below.

| Owner stage | Exclusive behavior / edit scope | Handoff gate |
| --- | --- | --- |
| A2 | HTTP admission/deadline handlers and bounded partial pending result support. | HTTP/core race tests and owning gates. |
| B1 | Atomic guarded mapping mutation in store/shared confirmation. | Core race tests and owning gates. |
| B2 | Required expected target in HTTP JSON/native forms and browser acceptance. | HTTP/core tests, browser journeys and owning gates. |
| C1 | Shared bounded pending seek cursor and selection tests. | Core race tests and owning gates. |
| C2 | Pending CLI/HTTP continuation inputs and output contract. | Adapter/core tests, browser regression and owning gates. |
| D1 | Version-2 decisions state, migration and store tests. | Migration/store race tests and owning gates. |
| D2 | Local decision/reset/list commands and shared pending suppression. | CLI/core tests, browser regression and owning gates. |
| E | Terminal review, owned lifecycle and explicit validated selections. | Scripted CLI/core journeys and owning gates. |
| F1 | Explicit author-aware ranking option in shared matching. | Catalog/matching/core regressions and owning gates. |
| F2 | Terminal hint choice and alternate-query prompts. | Scripted CLI/core journeys and owning gates. |
| G | Terminal restart, cancellation, bounded per-book failures and resume acceptance. | Uncached CLI/core acceptance plus complete owning/browser gates. |
| Integration | Fast-forward verified component commits, final documentation/story evidence and combined acceptance. No new feature ownership. | Complete owning gates on the integrated checkpoint. |

Each owner reads earlier handoffs and the actual current source rather than
assuming a planned interface exists. A failed gate blocks advancement; fix only
within the owned contract or request a bounded supervisor decision. Parent
source/evidence inspection is self-review, not an independent-review claim.

## Verification and iteration

All code checks run from the owning checkout with its pinned mise tools.
Focused checks above are iteration checks, never substitutes for acceptance.
Every implemented subdelivery requires `make check` (format, vet, module
verification, race tests and both executable builds) and pinned Markdown lint
for its changed docs. HTTP/shared mapping changes additionally require
`make e2e-probe ROUTE=/` and `make e2e`; retain these as the milestone's final
browser regression gate. Tests use synthetic HTTP/providers and temp SQLite.

A missing/failing gate blocks that delivery. Do not weaken assertions, skip
failures, change the pinned runtime or push to let CI substitute for local gates.
Record source revision, any working diff and exact commands/results in the
owning PR. The owner is the final reviewer; an independent agent review requires
an explicit separate request. No live credentials are needed for this milestone.

## Later milestones: close the original status stories

These are dependency-gated scopes for later acceptance work, not authorization
or a claim that provider semantics are established. Write each finite contract
before implementation; reuse the original research stories rather than invent a
second definition of sync success.

| Milestone | Concrete outcome / exit evidence | Depends on / original stories |
| --- | --- | --- |
| 2. [Verify account and provider contracts](account-and-provider-contracts.md) | Bind stable account identity/fingerprints to labels; token rotation for the same account works, switching accounts refuses baseline reuse. Establish a documented/verified WeRead identity route for accounts with no own review, or explicitly block those accounts. Obtain a representative sanitized completion/reread contract. Under separate approval, test one target book with status-only mutation, replay/no-op and before/after checks of edition, dates, history, privacy, ratings and reviews. Synthetic tests cover uncertain responses; no blind retry. | Milestone 1 for useful matching; explicit read/write experiment permission. Gates US1/US4/US8/US11. |
| 3. Preview one-book bootstrap | For missing, matching and differing target records, produce create/no-op/conflict proposals with no writes. Define explicit privacy, per-target source authority and policy version; mappings alone grant none. Verify status catalog and target identity. Freeze durable conflict evidence; namespace labels alone are insufficient. | Verified contracts. Covers the decision portion of US1/US5/US6/US8. Completed in Milestone 3. |
| 4. Apply one approved bootstrap safely | Persist intent before send, apply only approved fields, read back, record receipt and accepted baseline. Restart reconciles uncertain outcomes before another mutation. A failed book does not block safe books; unlink remains local-only and mapping correction cannot roll back unrelated target activity. | Approved preview/authority. Completes bounded first-sync US1 and post-link US2; exercises US7/US11. Completed in Milestone 4. |
| 5. Reconcile changes before scheduling | Manually trigger three-way source/target/baseline comparison first. Test rereading, source-unchanged/target-edited, conflicting two-source input, source disappearance and obsolete intents. Persist holds, use Drifter's monotonic run sequence, expose freshness/partial failures and reconcile restarts. Only then choose polling frequency against verified quota, batch target reads and approve scheduling/deployment. | Baselines/receipts and verified reread facts. US3-US8/US10-US11. A synthetic second source tests authority without claiming KU exists. |
| 6. Resolve KU feasibility separately | Inspect an authorized region-appropriate source/export sample. Prove book identity plus actual reading status, distinct from loans; otherwise provide separately approved owner-declared manual input with manual provenance or report unsupported. No scraping/access workaround by assumption. | Separate acquisition approval, not a prerequisite for WeRead-only usefulness. US9 and real second-source US6. |

For US12, Drifter continues to stop at Hardcover. A later approved one-book
update may be observed by Iroha's independently owned job; do not change Iroha,
call it from Drifter or make Drifter success depend on that job.

## Explicitly deferred decisions and non-goals

- **Public sharing consent:** current export publishes every mapping in the
  selected account pair, including manually linked private reading identities.
  Before promoting community sharing, choose explicit per-book selection versus
  an unmistakable account-wide choice, then define the corresponding acceptance
  case. Do not change offline export or frozen catalog v1 implicitly in this
  matching milestone. IDs are not anonymous.
- **Personal backup/restore:** public catalogs are not lossless personal exports.
  Before unattended persistent deployment, document and exercise a private,
  SQLite-consistent backup/restore procedure against synthetic state, including
  mappings, decisions, permissions, ownership and supported schema version.
  Future portable import must retain the accepted account-fingerprint rule;
  lack of identity evidence is a blocker, not permission for fingerprint-free
  import. Never test destructive restore against the owner's live database.
- **Operator onboarding:** retain the accepted friendly-empty-state requirement
  for the future operator-surface round. Current manual-ID forms are not proof
  of that onboarding story. Browser candidate review, run history and holds
  follow their own accepted server scope, not this terminal-first delivery.
- No automatic mapping, LLM integration, shared-catalog write/enrichment,
  provider status mutation, background refresh, notification system, KU adapter,
  public hosting or deployment is part of milestone 1.

## Historical A1 handoff

A1 tested code checkpoint: `fix/provider-cancellation` at
`87006388ebf3a053260c6b43de1373abc39878af`, based on the baseline above.
The owner selected `openai-codex/gpt-6-luna` with medium effort through native
Pi subagents. One worker owned the transport/test seam; the parent inspected
the actual diff and saved gate output. This is implementation plus parent
self-review, not an independent-review claim.

A1 replaces the uncancelable mutex wait with a context-aware per-client gate.
Its synthetic regression proves canceled waiters return `context.Canceled`
without sending a request, and subsequent requests can still acquire the gate.
The initial full gate also exposed a pre-existing HTTP test-helper cleanup race.
The authorized correction closes idle test-client connections before server
cancellation; the one-second assertion and production timeouts remain unchanged.

The [review's implementation checkpoint](../research/2026-10-15-design-review.md#a1-implementation-checkpoint)
records exact commands, baseline comparison and limits. Focused race tests,
`make check` including both builds, the browser probe and all three browser
journeys passed on this code. Documentation lint/whitespace checks apply to the
later documentation-only checkpoint; no provider experiments occurred.

**Historical next action (superseded by the integrated checkpoint below):**
execute the approved A2-G ownership sequence above and finish
milestone 1 in the existing PR 13. At this authorization checkpoint, A2-G remain
undelivered; the plan is not proof of their completion. Preserve each tested
component commit and record final M1-M7 evidence in the owning PR/records. Do not
silently skip prerequisites, weaken gates or claim status synchronization.
Before resuming, compare current Git with the code checkpoint and read
intervening commits; documentation-only commits may follow it. Asobi,
cross-device coordination, provider writes and PR merge remain outside scope.

### Historical partial execution checkpoint: E2 acceptance was open

The isolated `work/pr13-matching-milestone` branch has verified commits through
A2, B1/B2, C1/C2, D1/D2 and E1. The E2 linking implementation checkpoint is
`86b6ab8fc0149e8d963369a00b8c7f620be14bea`. The published PR checkout remains
on its earlier approval checkpoint; these later commits are not integrated or
published as milestone-complete.

At the pause, three lane files contain preserved, uncommitted E2 corrections:
`internal/cli/review.go`, `internal/cli/review_test.go` and `docs/cli.md`.
They add provider-client reuse, unambiguous `work ID` input, Unicode control
filtering, context-cancellation priority, and two focused regression cases.
The worker has ended; no background writer owns this checkout. The parent owns
the recovery handoff pending the owner's execution-route decision.

The worker ran focused uncached CLI/core race tests, `make check`, browser probe,
full browser tests, changed-Markdown lint and whitespace checks successfully on
that working diff. This is not proof that all E2 requirements are met: manual
edition, wrong-membership state preservation, replacement/stale-target cases,
failed lookup followed by later success, exact factory counts/laziness, provider
timeouts versus owner input, imported no-link and cancellation/input races still
need the complete scripted acceptance matrix. An accidental rumdl invocation on
Go files was invalid; the subsequent proper Markdown-only lint passed.

Two E2 attempts ended with incomplete acceptance rather than an external
infrastructure blocker. The parent stopped that retry loop and requested an
owner routing decision; no model or execution mechanism was silently changed.
F1/F2, G, final integration and updated PR publication have not run. All previous
accepted components and the three-file partial diff must be preserved. Resume
by inspecting actual Git, completing E2 acceptance, then following the approved
F/G and integration gates. Do not repeat completed source work or passing checks
unless their inputs change, and do not claim the original sync stories complete.

The owner subsequently approved native `openai-codex/gpt-6.1-sol` workers at
medium effort for the remaining E2–G implementation and tests. This resolves the
routing pause, not the missing acceptance. Continue from the preserved E2 diff
without repeating completed components; retain the same PR, scope and gates.

### G executable acceptance checkpoint

G continues the verified E2/F1/F2 source at parent
`e2dd713794724dc8f1714a707be0f82d7250d560` on
`work/pr13-matching-milestone`. Earlier partial checkpoints above are historical,
not the current next action. The G commit containing this section supplies
executable restart/failure/cancellation acceptance and minimal review pagination
fixes; its full hash and exact gate results belong in the component handoff and
subsequent owning PR record. No remote records are changed by this checkpoint.

The review loop now consumes safe rows from incomplete pages, follows real
continuation, distinguishes normal `worklist_limit` from unresolved issues,
preserves partial rows on the 60-second page deadline and reports omitted failed
metadata IDs. The core retains last-attempted cursor semantics and identifies
interrupted versus unattempted enrichment rows. All provider-backed stages are
bounded independently of owner input. Saved choices survive later failures;
unfinished IDs and unknown later-page work are not presented as completed.

The [walkthrough](../walkthroughs/README.md) contains exact offline reproduction
commands, not CLI examples falsely described as synthetic. Actual test names
map the milestone outcomes as follows; the full gate re-executes predecessor
coverage rather than treating historical test claims as new evidence.

| Outcome | Actual executable evidence |
| --- | --- |
| M1: older eligible books | CLI `TestReviewAcceptanceOlderPageFailuresAndRetry`, `TestReviewAcceptanceNormalPaginationIsNotFailure`, `TestReviewAcceptancePageDeadlinePreservesRowsAndContinues`; core `TestPendingSeekContinuesAcrossFailuresAndTies` |
| M2: persisted reversible choices | CLI `TestReviewAcceptanceRestartAndScopedReset`; core `TestDecisionStoreAccountIsolationResetAndValidation`, `TestDecisionMigrationFromVersionOnePreservesMappingsAndReadOnlySchema`, `TestFailedVersionOneDecisionMigrationRollsBack`, `TestStoreVersionsNewAndLegacyDatabases` |
| M3: suggestions remain unconfirmed | CLI `TestReviewHintQueryActions`, `TestReviewIncompleteSelectionsNeverMeanConsent`; core `TestMatchExpectedAuthorStablePartition`, `TestMatchExpectedAuthorPreservesExactEditions` |
| M4: intended work/edition | CLI `TestReviewManualEditionAndWorkOnlyDespiteExactEdition`, `TestReviewWrongEditionPreservesMappingsAndDecisions`, `TestReviewGuardedReplacementAndStaleExpectedTarget`; core `TestStoreGuardedMutationsRejectStaleTargets` |
| M5: failure, restart and cancellation | CLI `TestReviewAcceptanceOlderPageFailuresAndRetry`, `TestReviewAcceptanceCancellationDuringProviderStages`, `TestReviewAcceptanceInputCancellationPreservesPriorChoices`, `TestReviewCancellationWhileInputBlockedReleasesLease`, `TestReviewAcceptanceInputFailureAndDefaultsReleaseLease`, `TestReviewProviderStageDeadlinesAndOwnerThinkTime`, `TestReviewEOFDoesNotSaveDecisionAndReleasesLease`, `TestReviewAcceptanceMetadataOmissionsRemainHonest`; core `TestPendingSeekCancellationDoesNotSkipUnattemptedRows` |
| M6: bounded intentional HTTP mutations | HTTP `TestBusySlotLeavesReadRoutesAndValidationAvailableThenReuses`, `TestPendingTimeoutPreservesCompletedRowsAndReturns504`, `TestMutationDeadlineIsConsistentForJSONAndNativeForm`, `TestMutationRejectionsPrecedeProviderCallsAndStateWrites`; core `TestCanceledSpacingDoesNotSendRequest`, `TestCanceledSerializationWaitDoesNotSendAndGateCanBeReused`; browser native-form regressions |
| M7: privacy and ownership | CLI `TestReviewAcceptancePrivateAndUnknownRowsAreNotEnriched`, `TestReviewAcquiresOwnershipBeforeProviderFactory`, `TestReviewImportedRowsNeverLink`, `TestReviewSourceMetadataIsTerminalSafe`; core `TestPendingShelfFiltersBeforeMetadataAndClassifies`, `TestReadOnlyStoreUnderOwnershipDoesNotMigrate`, `TestDaemonOwnershipIsExclusiveAndReleasedOnProcessDeath` |

G's initial acceptance tests were red: ordinary pagination/metadata issues
prevented any review, and pending page acquisition had no stage deadline. The
new CLI/core journeys now pass uncached under the race detector. The full gate
also exposed an old HTTP timeout fixture that assumed only one issue; it now
requires both the attempted failed metadata ID and `operation_timeout`, retaining
the completed row, cursor and 504 assertions.

An uncached HTTP run additionally found a pre-existing test-only race: direct
`handlePending` invocation read `s.ownership` while background `Serve` initialized
it. With supervisor approval, that one cancellation fixture now initializes
ownership synchronously. Normal HTTP requests already enter through `Serve`
after initialization; production serving and the idle-client cleanup/unchanged
stop assertion are untouched. This exact focused regression passed 20 uncached
race-enabled repetitions:

```sh
mise exec -- go test -race -count=20 ./internal/httpserver \
  -run 'Test(CanceledPendingReturnsSafeEmptyArrays|PendingTimeout.*)$'
```

The complete owning gate, browser probe (one test), browser suite (four tests),
changed-doc lint and whitespace check pass on G's final source. The component
handoff records exact commands and revision. This is implementation/self-review
evidence, not independent review. The next owner is the parent integration stage:
inspect the component commit, reconcile
M1–M7 with PR 13 and issue 7 under the owner's authority, and record the actual
integrated revision/gates before claiming milestone completion. Status-write
milestones, real account fingerprints, live-provider validation, KU intake,
backup/restore, deployment and sharing-policy changes remain outside this work.
Public catalog v1 and provider destinations/security policies are unchanged.

## Integrated milestone 1 checkpoint

Integration starts from PR 13's `fix/provider-cancellation` at
`b53709791e01bfa78952bf7d8a779e8e9e8aac7c`. Both it and the isolated
`work/pr13-matching-milestone` checkout were clean. The lane is a descendant of
that PR checkpoint and was integrated locally with `git merge --ff-only` to
`4002a6d51e87ca21249f72cfc1383227836ba6e0`. No merge commit, remote action or
provider experiment occurred. The final documentation/acceptance commit follows
that source revision; its hash is discoverable in Git/PR 13, avoiding a
self-referential hash in this record. The isolated lane remains unchanged.

The G table above maps actual M1–M7 tests. Integration additionally supplies:

| Outcome | Combined evidence / limit |
| --- | --- |
| M1 | `TestPendingSeekLiveShelfUsesFixedWindowAndFreshScanRecoversMovedRow` proves fixed cutoff/as-of, no duplicate enrichment on continuation, and moved-ahead recovery by fresh scan. CLI 103/101-row acceptance proves actual terminal pagination rather than only core selection. |
| M2 | `TestReviewAcceptanceRestartAndScopedReset` reopens durable state between invocations and tests scoped reset; v0/v1/v2 migration/rollback cases run in the core suite. |
| M3 | `TestReviewHintQueryActions` and `TestMatchExpectedAuthorStablePartition` prove explicit search reuse/no automatic selection; public catalog v1 is unchanged. |
| M4 | `TestReviewManualEditionAndWorkOnlyDespiteExactEdition`, wrong-membership and guarded replacement tests exercise the shared validation/store paths. |
| M5 | `TestReviewAcceptanceOlderPageFailuresAndRetry` persists 99 exclusions and the older 103 link despite metadata/lookup/validation failures, exits nonzero naming 001/051/052, then retries only those IDs. Cancellation/EOF/partial deadline cases run uncached. |
| M6 | `TestExpectedTargetBoundaryRejectionsPreserveState` covers both expected tuple members on JSON and native replace/unlink, zero provider construction/calls and unchanged seed. Existing HTTP budget/core cancellation and four browser journeys cover runtime regressions including stale tabs. |
| M7 | CLI privacy/ownership tests plus core exclusive-owner/read-only cases run together; no live credentials or personal state are used. |

The malformed expected-tuple matrix was absent from the earlier B2 acceptance
addition; older edition-field tests were not substituted as proof. Integration
adds only acceptance fixtures/tests and documentation, not missing features or
relaxed production checks. The owner approved the bounded changed-live-shelf
acceptance addition. A row moved ahead of the cursor or newly eligible after a
reset can require a fresh scan: continuation is not a snapshot. Failure rows
remain eligible unless a separate explicit mapping/decision suppresses them.

### Combined verification commands

Run from the PR checkout with pinned mise tools. The
[walkthroughs](../walkthroughs/README.md) describe exact scripted assertions.
These commands are the final combined checkpoint, separate from source-only
review and historical A1/component evidence:

```sh
mise exec -- go test -race -count=1 ./internal/cli ./internal/companion \
  ./internal/httpserver ./internal/catalog
make check
mise exec -- rumdl check README.md docs/architecture.md docs/cli.md \
  docs/plans/http-server.md docs/plans/matching-worklist.md \
  docs/research/2026-10-15-design-review.md docs/walkthroughs/README.md
make e2e-probe ROUTE=/
make e2e
git diff --check
```

Final combined results: uncached race suites passed (CLI/core/HTTP/catalog);
`make check` passed formatting, vet, module verification, all race tests and both
builds. Some unchanged packages reused valid Go cache in the full gate. Pinned
rumdl passed all seven changed Markdown files. Browser probe passed one test with
no page errors; all four browser journeys passed. `git diff --check` passed.
Initial documentation lint found wrapping/blank-line issues; they were corrected
without suppressions. The new native malformed-guard fixture was also corrected
to use the actual `work_only` mode and present-empty `edition_id`, with an
explicit valid-baseline assertion; the CLI reference now spells that accurately.
Historical component red/green failures are not new combined failures.
This is integration/self-review; no independent reviewer was requested.

**Next owner:** parent inspects final source/evidence, runs the workstation
pre-publication gate and updates the same PR 13 without merging. This worker
performs no push, GitHub write or merge. Local M1–M7 acceptance does not close
all original sync stories or authorize later identity/status contracts,
public-sharing consent changes, private backup/import, KU or deployment.
