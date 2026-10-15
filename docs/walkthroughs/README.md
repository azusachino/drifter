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

## Full local gates

```sh
make check
mise exec -- rumdl check README.md docs/architecture.md docs/cli.md \
  docs/plans/http-server.md docs/plans/matching-worklist.md \
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

Account fingerprint binding, provider status writes, sync authority/baselines,
bootstrap reconciliation, conflict holds, scheduling, KU intake, private backup
and restore, deployment and public-sharing policy changes are not exercised or
authorized by these tests. A mapping confirms identity only, not status-write
authority. See the [owning milestone plan](../plans/matching-worklist.md).
