# CI performance round

Source baseline: `482964eba81ea729939bb28cbd9a17a221999b18`.
Latest 50 runs sampled via `gh`; 50 jobs, all successful,
median job 126.5s (min ~90s, max 144s).

## Change

Added `actions/cache` for `~/go/pkg/mod` and `~/.cache/go-build`, keyed on
`go.sum` with runner-OS restore keys. Go itself comes from mise, so
`setup-go`'s cache does not apply. PR docs filter and main-preserving
concurrency groups included in [PR #27](https://github.com/azusachino/drifter/pull/27).
No test, lint or build flag changed.

## Measurements

| Run / attempt | Cache restore | Gate step | govulncheck | Job total | Result |
| --- | --- | --- | --- | --- | --- |
| Baseline median (50 jobs) | none | — | — | 126.5s | — |
| Baseline latest run | none | 124s | 8s | 144s | Passed |
| 41618856368 attempt 1, cold cache (populates) | — | — | — | 147s | Passed |
| 41618856368 attempt 2, warm cache | 3s | 11s | 2s | 38s | Passed |

Run links: baseline latest
[40813865109](https://github.com/azusachino/drifter/actions/runs/40813865109);
cold attempt
[1](https://github.com/azusachino/drifter/actions/runs/41618856368/attempts/1);
warm attempt
[2](https://github.com/azusachino/drifter/actions/runs/41618856368/attempts/2).

Local `make check` (fmt, vet, mod verify, race tests, builds) passed
before push. The cold job pays cache population; the warm job is
**69.9% below the baseline median** with identical race-test coverage.
Sample size is one cold/warm pair on the same revision; hosted-runner
and source variation apply. The cold/warm spread is the expected
cache shape, and the gate step's drop (124s → 11s) is the mechanism.
