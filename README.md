# Drifter

A headless reading-status companion: **WeRead / Kindle Unlimited → Drifter →
Hardcover**. Hardcover → Iroha remains an independent Iroha integration.

This first slice is **remote-read-only**: inspect WeRead books/progress,
discover Hardcover candidates, and explicitly confirm local identity mappings.
It does not synchronize statuses, import/create catalog entries or infer fuzzy
matches. KU intake, TOML configuration, a persistent daemon and deployment are
not yet implemented.

## Build and check

Requires Go 1.26, Make, CGO enabled and a C compiler. SQLite uses the pinned
`github.com/mattn/go-sqlite3` driver; no separate database service is needed.

```sh
make check
make build
bin/drifter help
```

`make check` verifies Go formatting, runs vet and race-enabled offline tests,
and builds the CLI. `make fmt` formats Go; `make test` runs race-enabled tests.
Tests use synthetic provider responses and temporary databases, never real keys
or account captures.

## Credentials

Set `WEREAD_API_KEY` and `HARDCOVER_API_KEY` in the invoking shell environment.
WeRead inspection/search needs only the former; candidate matching and local
confirmation need both. Obtain keys through the providers' official account
flows. Never pass them as flags, put them in TOML or commit them.

Provider destinations are fixed HTTPS endpoints. Redirects are refused; requests
have a 30-second timeout and a 2 MiB response limit. Errors omit response
bodies, headers and credentials. Requests are spaced per client (WeRead one
second, Hardcover 1.1 seconds), without automatic retries. These are client
safeguards, not proof of remaining account quota or a complete rate-limit
policy.

## Inspect and match

```sh
bin/drifter search --title 'Book title'
bin/drifter inspect --book-id 123
bin/drifter inspect --book-id 123 --progress
bin/drifter match --book-id 123
bin/drifter match --book-id 123 --query 'Original-language title'
```

IDs above are placeholders, not sample account records. Output is JSON. Search
returns candidates and a `has_more` flag, not a selected identity. Personal
progress is fetched/displayed only with `--progress`; it is never persisted.
Zero percent plus a started flag proposes reading, not unread. Unsupported or
insufficient completion evidence remains unknown. Proposed statuses are not
remote updates or a verified WeRead completion contract.

Matching validates ISBN checksums and checks ISBN-10/13 equivalents first. A
unique exact edition is still only a proposal; duplicate identifier results are
ambiguous. Without an exact edition, supply an original-language query when
appropriate. Title similarity or search ranking never creates a mapping.

Hardcover title search is limited to the first ten candidates; candidate edition
lists show at most ten editions each. Exact ISBN lookup is capped at 100 rows.
These bounded views do not prove that an absent title/edition is missing from
the whole catalog. Every result requires owner confirmation. No catalog
enrichment or automatic English-edition substitution is attempted.

## Confirm local mappings

After checking the original work, author and any edition evidence yourself:

```sh
bin/drifter link --book-id 123 --work-id 456 \
  --account my-weread --target-account my-hardcover
bin/drifter links
bin/drifter unlink --book-id 123 \
  --account my-weread --target-account my-hardcover
```

`link` verifies that the source book and target work exist before storing your
explicit selection. Add `--edition-id ID` only if that edition is confirmed;
Drifter checks that it belongs to the selected work. Omitting it records a
work-only mapping, not an inferred edition. This validation proves existence and
membership, **not** that two differently titled books represent the same work.

Different existing work/edition mappings are refused unless you specify
`--replace`. Reconfirming the same IDs is idempotent. `links` and `unlink` are
local-only and require neither credentials nor network access. Unlinking a
missing key is also idempotent; it never changes a provider library.

**Account labels are namespaces you choose, not authenticated account IDs.**
Use separate labels for distinct accounts; never reuse a label after switching
credentials. Before any future remote writing, stable account identity and
explicit source authority must be verified. A confirmed identity mapping does
not itself authorize status synchronization.

## Local state and privacy

Default database:

- `$XDG_STATE_HOME/drifter/mappings.sqlite` when `XDG_STATE_HOME` is set to an
  absolute path.
- Otherwise `$HOME/.local/state/drifter/mappings.sqlite`.

Use `--db PATH` on `link`, `links` or `unlink` to override it. Local listing may
initialize an empty database. New application directories are mode 700 and the
database must be a regular, non-symlink mode-600 file. Its immediate parent must
not be writable by other users. Keep the state directory under your control;
these checks do not protect against a hostile process running as you.

Persisted fields are source/provider account namespaces, source book ID,
confirmed target IDs, source title and ISBN. These are personal metadata: keep
them out of Git and public artifacts. Raw provider responses, personal progress,
excerpts, device identifiers and tokens are not stored. JSON progress output is
still personal data; protect redirected output yourself.

Mappings remain until explicitly unlinked. SQLite secure-delete is enabled, but
backups and filesystem recovery are not erased by unlink. Back up the database
only through a private, SQLite-consistent backup procedure. Do not copy only the
main file during an active write or commit a personal database as a fixture.

## Design and roadmap

- [Source-backed research and measured findings](docs/research/reading-status-companion.md)
- [First-slice plan, gates and owner-confirmed direction](docs/plans/read-only-matching.md)
- [Domain language](CONTEXT.md)
- [Acceptance issue #1](https://github.com/azusachino/drifter/issues/1)

Later direction is a persistent daemon in a single-replica k3s Deployment, not a
CronJob. `config.toml` holds operational settings, applied on restart; SQLite
holds mappings and reconciliation state. Future safe status deltas require
mapping/bootstrap approval and one primary source per target work/account.
Conflicts hold the affected book for explicit resolution, with durable CLI
reports and logs. The 30-minute freshness goal still needs quota and acquisition
verification. **Telegram notifications are on the roadmap**, outside this PR.

Remote writes need separately verified mutation contracts, account identity,
privacy/authority policy, durable intents/receipts and restart reconciliation.
KU automation and Japanese coverage remain feasibility questions. This code is
not authorization for live writes or deployment.
