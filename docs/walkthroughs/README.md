# Synthetic executable walkthroughs

Run these commands from the repository root with the pinned mise tools. They
execute committed tests using in-process fake providers and private temporary
SQLite databases. No credentials, live services or persistent account data are
needed. Running `bin/drifter review` itself is **not** a synthetic demo: it uses
real provider clients. Browser tests are server regressions, not terminal-review
evidence.

## Restart, decisions and exact scope

```sh
mise exec -- go test -race -count=1 ./internal/cli \
  -run '^TestReviewAcceptanceRestartAndScopedReset$' -v
```

The script links A (`001`) work-only, skips B, excludes C and marks D
not-in-catalog. Every invocation closes its handles; the next invocation reopens
the same temporary database and sees only B. Resetting C in another scope does
nothing; resetting C in the selected scope restores C without restoring A or D.
The test checks durable state and released ownership after each invocation.

## Older pages, middle failures and retry

```sh
mise exec -- go test -race -count=1 ./internal/cli \
  -run '^TestReviewAcceptance' -v
```

The 103-row script fails recent metadata (`001`), middle lookup (`051`) and
middle validation (`052`), but saves 99 exclusions and links the older `103`.
It requires a nonzero result naming all three failed IDs without raw errors.
A fresh invocation enriches only those three pending rows. The separate 101-row
case finishes successfully: ordinary pagination is not a global failure.

A timed-out metadata page preserves safe available rows, records the attempted
failure and continues from its last-attempted cursor; later eligible rows remain
reachable. Missing translator/activity evidence and incomplete shelves retain
honest issue summaries and nonzero results even when other choices succeed.

## Cancellation, input and provider budgets

```sh
mise exec -- go test -race -count=1 ./internal/cli \
  -run '^TestReview' -v
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^TestPendingSeekCancellationDoesNotSkipUnattemptedRows$' -v
```

Cancel shelf acquisition, metadata enrichment, lookup, confirmation and blocked
owner input. A cancellation racing with `yes` saves no link, preserves an earlier
exclusion, reports unfinished IDs and releases ownership. EOF, blank/default
confirmation and input failure never mean consent. Tests shorten the normal
60-second provider budget; owner think time deliberately exceeds that test
budget and still succeeds. The core cursor case checks preserved completed
rows, failed attempted ID, remaining unattempted IDs and exact continuation.

## Identity, privacy and ownership regressions

```sh
mise exec -- go test -race -count=1 ./internal/cli \
  -run '^TestReview' -v
mise exec -- go test -race -count=1 ./internal/companion -v
```

CLI cases prove separate edition consent, wrong-membership refusal, guarded
replacement, imported inspection-only rows, writer refusal before providers,
private/unknown privacy filtering and terminal-control stripping. The core suite
also covers unchanged candidate sets under author ranking, decision migration
rollback and read-only access under exclusive ownership. See the
[M1–M7 evidence map](../plans/matching-worklist.md#g-executable-acceptance-checkpoint).

## Guard boundary and live-shelf continuation

```sh
mise exec -- go test -race -count=1 ./internal/httpserver \
  -run '^TestExpectedTargetBoundaryRejectionsPreserveState$' -v
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^TestPendingSeek' -v
```

The HTTP case submits malformed expected tuples to both replace and unlink:
missing/null members, zero work, negative members, wrong JSON types, unknown
fields and duplicate members. Native forms cover absent/empty/nonnumeric,
negative/duplicate/unknown members and zero work. Every request returns 400;
provider factory/call counts stay zero and the seeded mapping remains unchanged.
Existing fresh work-only/specific-edition and stale-tab cases remain separate.

`TestPendingSeekLiveShelfUsesFixedWindowAndFreshScanRecoversMovedRow` changes the
synthetic shelf between pages. Continuation does not re-enrich attempted rows;
it retains the original inclusive cutoff/as-of despite advancing wall time.
A previously unattempted row moving ahead of the cursor is missed by continuation
and recovered by a fresh scan. This explicitly proves a live-scan limitation,
not a shelf snapshot guarantee. Existing seek cases preserve attempted-failure
and cancellation semantics.

## Account fingerprints and status mutation contracts

```sh
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^TestAccountFingerprint' -v
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^Test(Hardcover|WeRead)ResolveAccountFingerprint' -v
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^TestHardcoverStatusMutation' -v
```

These cases exercise Milestone 2 contracts against in-process synthetic doubles:

1. **Fingerprint binding and re-auth**: First run records `hardcover:<id>` or
   `weread:<vid>`. Re-authentication with the same identity updates
   `last_verified_at`; credential mismatch returns
   `ErrAccountFingerprintMismatch` and fails closed without overwriting state.
2. **Schema v3 migration**: Upgrades v2 SQLite databases, preserving all
   existing mappings and review decisions. Conflicting tables roll back
   transactionally.
3. **WeRead zero-review discovery**: Resolves `userVid` when reviews exist,
   accepts declared VID fallback for 0-review accounts, and fails closed with
   actionable guidance when unconfigured.
4. **Hardcover status mutation**: Proves status-only updates, zero-touch
   preservation of ratings, reviews and historical reading logs during reread
   transitions, idempotent no-op replay (zero mutations sent on matching
   status), and uncertain 504 timeout holds.

## Bootstrap preview and three-way evaluation

```sh
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^Test.*Preview' -v
mise exec -- go test -race -count=1 ./internal/cli \
  -run '^TestCLIPreview' -v
```

These cases exercise Milestone 3 contracts against in-process synthetic doubles:

1. **Schema v4 migration**: Upgrades v3 SQLite databases to v4, introducing
   `status_baselines` and `target_authorities` tables while preserving all
   existing mappings, decisions, and account fingerprints.
2. **Three-way status evaluation (US1, US5, US6, US8)**:
   - Missing target record evaluates to `create` proposal.
   - Matching target record evaluates to zero-mutation `no-op` proposal.
   - Differing target record on bootstrap evaluates to `conflict` with frozen
     immutable `ConflictEvidence` and nil baseline.
   - Manual target edit on Hardcover (target changed while source unchanged)
     evaluates to `conflict` with baseline evidence, protecting manual edits.
   - Source progress advancement with unchanged target evaluates to `update`.
   - Independent convergent progress evaluates to `no-op`.
   - Divergent changes evaluate to `conflict`.
   - Competing source mappings without exclusive `target_authorities` record
     evaluate to `conflict`.
   - Private source reading without explicit `--allow-private` consent is
     held (`private_book_excluded`); private source mapped to public target
     record conflicts to prevent silent exposure.
3. **Zero mutations and fingerprint enforcement**: `drifter preview` binds
   account fingerprints in SQLite on first use, fails closed on credential
   mismatch, and asserts zero mutations are dispatched to target providers.

## Safe bootstrap mutation, intents and restart reconciliation

```sh
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^TestApplySync' -v
mise exec -- go test -race -count=1 ./internal/cli \
  -run '^TestCLISync' -v
```

These cases exercise Milestone 4 contracts against in-process synthetic doubles:

1. **Schema v5 migration**: Upgrades v4 SQLite databases to v5, introducing
   `sync_intents` and `sync_receipts` tables while preserving all existing
   mappings, decisions, account fingerprints, baselines, and target
   authorities.
2. **Durable two-phase write intent (US7)**: Persists an intent record before
   sending status mutations to Hardcover, transitions through `pending` and
   `in_flight`, and reads back target state to verify before marking `applied`.
3. **Approved field boundaries (US1, US2)**: Strictly passes approved status
   fields (`work_id`, `edition_id`, `status_id`) without touching user reviews,
   ratings, dates, or tags.
4. **Restart reconciliation (US7)**: Detects prior `uncertain` or `in_flight`
   intents on next run, queries target state, and reconciles to `applied` with
   an audit receipt and updated baseline if the mutation landed, avoiding
   duplicate remote mutations.
5. **Zero-mutation dry-run**: `drifter sync --dry-run` performs pure three-way
   evaluation identical to `preview` with zero provider writes.

## Account-wide continuous reconciliation and monotonic runs

```sh
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^TestReconcileAccount' -v
mise exec -- go test -race -count=1 ./internal/cli \
  -run '^TestCLIReconcile' -v
```

These cases exercise Milestone 5 contracts against in-process synthetic doubles:

1. **Schema v6 migration**: Upgrades v5 SQLite databases to v6, adding
   `sync_runs` and `sync_holds` tables while preserving all existing mappings,
   decisions, account fingerprints, baselines, authorities, intents, and
   receipts.
2. **Monotonic sequence and run tracking (US10)**: Assigns strictly increasing
   run IDs, tracks execution timestamps, counts applied/no-op/conflict/hold
   outcomes, and records overall status (`completed`, `partial`, `failed`).
3. **Partial failure isolation (US10)**: A failure or provider timeout on one
   work does not abort or roll back other works in the account worklist;
   unaffected books proceed and complete safely.
4. **Persistent holds (US5, US6, US8)**: Books encountering conflicts (such as
   manual target edits or competing mappings) are persisted to `sync_holds`
   with full serialized `ConflictEvidence` for operator inspection.
5. **Dry-run evaluation**: `drifter reconcile --dry-run` previews outcomes
   across all account mappings without mutating remote providers or creating
   live runs.

## Full local gates

```sh
make check
mise exec -- rumdl check README.md docs/architecture.md docs/cli.md \
  docs/plans/http-server.md docs/plans/matching-worklist.md \
  docs/plans/account-and-provider-contracts.md \
  docs/research/2026-10-15-design-review.md docs/walkthroughs/README.md
make e2e-probe ROUTE=/
make e2e
git diff --check
```

`make check` runs vet, module verification, all race-enabled tests and both
builds. The browser commands use the synthetic local server and check existing
HTTP/native-form behavior, not the terminal journey. This is implementation
and self-review evidence, not an independent review or live-provider experiment.

## Later milestones remain unimplemented

Live provider write experiments, sync authority/baselines, bootstrap
reconciliation, conflict holds, scheduling, KU intake, private backup and
restore, deployment and public-sharing policy changes are not exercised or
authorized by these tests. A mapping confirms identity only, not status-write
authority. See the [matching plan](../plans/matching-worklist.md) and
[Milestone 2 plan](../plans/account-and-provider-contracts.md).
