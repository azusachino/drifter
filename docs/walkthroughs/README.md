# Simulated walkthroughs

Reproducible end-to-end examples driven entirely by synthetic fixtures
(in-process fake providers and temporary SQLite files), mirroring
`internal/cli/cli_test.go`. Every walkthrough is **executable today**; sections
marked *illustrated* describe behavior that is not implemented.

Prerequisite: `make build` produces `bin/drifter` and `bin/drifter-server`.

## 1. Search, inspect, match (read-only path)

What the CLI proves on the happy path: candidates are returned, never
selected; progress requires an explicit flag; matching always ends in
`requires_confirmation`.

```sh
bin/drifter search --title '合成の本'          # JSON books + has_more
bin/drifter inspect --book-id 123              # identity only
bin/drifter inspect --book-id 123 --progress   # adds proposed_status
bin/drifter match  --book-id 123 --query 'Original Title'
```

Expected invariants (asserted by `TestCLIReadCommands`):

- `search` returns a non-empty `books` array and boolean `has_more`; the CLI
  never picks a winner.
- `inspect` without `--progress` contains no `progress` key and contacts no
  progress endpoint.
- `match` output carries `"requires_confirmation": true` regardless of how
  confident the ISBN/candidate lookup looked.

## 2. Confirm a work-only mapping, then replace it explicitly

```sh
bin/drifter link --book-id 123 --work-id 42 \
  --account source --target-account target --db /tmp/drifter-walkthrough.sqlite
bin/drifter links --db /tmp/drifter-walkthrough.sqlite
```

- First link succeeds with `"edition_id": 0`: work-only confirmation is valid
  and assigns no edition.
- Re-running the same command is idempotent.
- Linking the same book to work `99` without `--replace` **fails**; with
  `--replace` it succeeds (asserted by
  `TestCLILinkReplacementAndLocalLifecycle`).

## 3. Wrong-edition rejection leaves no state

```sh
bin/drifter link --book-id 123 --work-id 42 --edition-id 100 \
  --account source --target-account target --db /tmp/drifter-walkthrough2.sqlite
```

When the edition does not belong to the work, the command fails and the
database file is **not created** — a rejected confirmation never leaves a
half-mapping (`TestCLIRejectedEditionDoesNotCreateMapping`).

## 4. Credential-free local lifecycle

`links` and `unlink` run with **nil provider factories**: they cannot touch
the network even by accident. Unlinking one account scope leaves other
provider/source/target scopes intact, and removing an absent key is
idempotent (`TestCLILinkReplacementAndLocalLifecycle`).

## 5. Fail-closed provider responses

Not a command walkthrough but a test-backed behavior guarantee
(`internal/companion` failure tests): malformed envelopes, API errors inside
HTTP 200, upgrade notices, oversized bodies, redirects and cancellation all
produce errors — never partial data, and never an echoed response body or
credential.

## 6. Configuration and probes

```sh
bin/drifter serve-probes --config config.toml --db /tmp/drifter-walkthrough.sqlite
curl -s localhost:8080/healthz   # ok
curl -s localhost:8080/readyz    # ready (or 503 with the failing check name)
```

`readyz` re-checks the database on every request; a credential-like key in
`config.toml` (for example `api_key = ...`) aborts startup before any listener
exists (`TestCLIServeProbesWiring`, `TestCLIServeProbesRejectsInvalidConfig`,
`internal/config` tests).

---

## Illustrated: future flows (not implemented)

The research note's full user stories (first library sync, incremental
reconciliation, conflict holds, private-source policy, per-book authority)
are documented and analyzed in
[the research note](../research/reading-status-companion.md) and the
[owner-confirmed direction](../plans/read-only-matching.md). They are **not**
exercised by these walkthroughs or any code path in this branch; no `link`
command grants status authority, and no scheduled or write path exists yet.
