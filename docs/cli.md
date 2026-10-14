# Drifter CLI reference

A headless reading-status companion: **WeRead / Kindle Unlimited → Drifter →
Hardcover**. Hardcover → Iroha remains an independent Iroha integration.

Provider operations are **read-only**: inspect WeRead books/progress and
validate explicitly selected Hardcover identities. Local mappings can be
confirmed, replaced and unlinked with CLI commands or the foreground `serve`
HTTP server and mapping interface. No provider status writes occur. The server
does not synchronize statuses, import catalog entries or infer fuzzy matches.
KU intake and deployment are not implemented. Settings-only TOML and
standalone health probes remain available.

## Build and check

Use the mise-pinned tools from the [quick start](../README.md#quick-start).
Requires Make, CGO enabled and a C compiler. SQLite uses the pinned
`github.com/mattn/go-sqlite3` driver; no separate database service is needed.

```sh
make check
make build
bin/drifter help
make e2e-install  # once, for the mapping-interface browser check
make e2e-probe ROUTE=/  # inspect the rendered page
make e2e
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

## Configuration and health probes

Optional settings file (never credentials; validated fully at startup;
applied by the invoking process — no live reload):

```toml
# config.toml — settings only
[log]
level = "info"        # debug | info | warn | error

[probes]
enabled = true
addr = "127.0.0.1:8080"  # standalone read-only health endpoint

[server]
addr = "127.0.0.1:8081"  # local HTTP server; loopback by default

[database]
# path = "/var/lib/drifter/mappings.sqlite"  # optional absolute override
```

Credential-like keys (`api_key`, `token`, `secret`, `password`, `credential`)
are rejected outright; unknown keys fail validation so typos cannot silently
disable behavior. A missing file yields built-in defaults.

### Health probes and HTTP server

The standalone health-only process remains available:

```sh
bin/drifter serve-probes --config config.toml [--db PATH] [--require-providers]
```

It serves `/healthz` and `/readyz` without provider network calls or synchronization.
The foreground `serve` command establishes the first slice of the same intended
long-running daemon server foundation, delivered incrementally; it is not a
separate server product. Synchronization is not implemented:

```sh
bin/drifter serve --config config.toml [--db PATH]
```

`serve` uses `server.addr` (default `127.0.0.1:8081`), provides the same health
routes, a server-rendered mapping interface at `/`, read-only `/api/mappings`
and `/api/pending?account=LABEL&target_account=LABEL[&days=N]`, and explicit
local mapping maintenance. There is no `/admin` compatibility route. New
confirmations use `ConfirmLinkOwned`, including source book lookup and target
work/edition membership validation. The HTML form and
JSON routes support separate confirm, replace and scoped unlink actions; neither
accepts inferred matches. Work-only (`edition_id: 0` in JSON) and an explicit
edition choice remain separate selections. `/api/mappings/confirm` and
`/api/mappings/replace` require POST; `/api/mappings/unlink` requires DELETE.
JSON writes require `application/json`, a bounded strictly decoded body, same-
origin Origin, Host allowlisting and the server's per-process CSRF token. This
token prevents cross-site requests; it is not authentication. Obtain it from
the protected `/api/csrf` response or the `/` page's
`drifter-csrf-token` meta value; send it as `X-Drifter-CSRF`. Confirm and replace
POST JSON fields `source_account`, `book_id`, `target_account`, `work_id` and
`edition_id` to `/api/mappings/confirm` or `/api/mappings/replace`. `edition_id`
is required: use `0` only for a deliberate work-only mapping, or the selected
positive edition ID. Unlink with DELETE `/api/mappings/unlink` and JSON fields
`source_account`, `book_id` and `target_account`. Native form posts use the
bounded URL-encoded `/mappings/confirm`, `/mappings/replace` and
`/mappings/unlink` routes with the same Origin/Host and hidden-token checks. The
page uses native forms and loads no JavaScript. Chromium sends the exact
same-origin Origin with `Referrer-Policy: strict-origin-when-cross-origin`; with
`no-referrer` it sends `Origin: null`, which is correctly rejected. GET/HEAD
never mutate, no CORS is enabled, and invalid/provider errors do not disclose
raw details.

It can start without provider credentials for local mapping listing; WeRead and
Hardcover credentials are needed only when a user explicitly confirms/replaces
a mapping, and WeRead credentials are needed for an explicit pending request.
Pending calls use the existing core worklist operation. The root mapping
interface and mapping listing do not call providers. Requests are bounded;
Host is checked against the configured listener, and cross-site browser
Origin/Fetch Metadata requests are rejected. No CORS is enabled. Keep the
listener on loopback unless an explicit configuration opts into another
concrete host; wildcard listeners
are rejected because they cannot define the Host allowlist. Exposing
unauthenticated HTTP beyond the trusted local host is not an authenticated
deployment model.
The server takes exclusive ownership of its SQLite state before initialization
and keeps it until shutdown. While it runs, standalone `link` and `unlink`
refuse with an ownership error; `links` and pending reads may use the existing
current schema read-only. If that schema needs migration, a reader fails rather
than migrating under the owner. HTTP mapping maintenance uses the same owned
capability; standalone `link` and `unlink` remain refused while `serve` owns the
state. See the [architecture](architecture.md) and the
[HTTP user-story and test map](plans/http-server.md).

## Inspect and match

```sh
bin/drifter search --title 'Book title'
bin/drifter inspect --book-id 123
bin/drifter inspect --book-id 123 --progress
bin/drifter match --book-id 123
bin/drifter match --book-id 123 --query 'Original-language title'
bin/drifter pending --account my-weread --target-account my-hardcover
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

## Pending matching worklist

`pending` lists recently opened **ebooks**, not albums, article collections or
proof that you are actively reading. It needs only `WEREAD_API_KEY`; it makes no
Hardcover calls and creates no mapping. Account labels and optional `--db PATH`
select the same mapping namespace as `link`. A mapping for another source or
target account does not hide a book.

The default window is 90 elapsed 24-hour days, with an inclusive cutoff and
newest-first ordering (book ID breaks ties). Use `--days N` from 1 through 36500.
Books with no opening activity are omitted; missing, negative or future activity
is reported as an issue, not guessed. Private books are excluded **before**
metadata lookup or output unless `--include-private` is supplied. Missing or
invalid privacy always stays excluded, including with that flag.

Rows are `translated` when book-info explicitly names a translator, or `native`
when that field is explicitly empty. Missing translator evidence is reported,
not silently classified as native. Imported `CB_` books are excluded by default;
`--include-imported` shows them as `imported` using shelf metadata only. They
remain unsupported by `match` and `link`; this flag is inspection, not matching
approval. JSON containing opted-in private metadata must be protected by you.

Output has `books`, `complete` and privacy-safe `issues` (book IDs and fixed
codes, never provider error bodies). Shelf count mismatch/missing count or a
more-results flag prevents claiming completeness. Eligible metadata lookups are
capped at 100 books per invocation. A lookup failure leaves other valid books in
the output; narrowing the window or confirming mappings makes reruns useful.
Incomplete output exits nonzero so a partial list cannot look like success.
Shelf facts, progress and raw responses are never persisted. Opening the local
mapping database may initialize or migrate it as documented below.

Saved review decisions, external hints and interactive review are subsequent
issue #7 slices; this command does not yet filter decisions or synchronize status.

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
database must be a regular, non-symlink mode-600 file. The daemon also leaves a
mode-600 `.owner.lock` sidecar; the kernel releases its lock on process exit,
but do not remove the file while Drifter is running. Parent-directory symlink
aliases resolve to the same sidecar, and hard-linked database files are
rejected. The database's immediate parent must
not be writable by other users. Keep the state directory under your control;
these checks do not protect against a hostile process running as you.

Persisted fields are source/provider account namespaces, source book ID,
confirmed target IDs, source title and ISBN. These are personal metadata: keep
them out of Git and public artifacts. The mapping JSON route exposes the
same saved fields and must be protected like the local database. Raw provider
responses, personal progress,
excerpts, device identifiers and tokens are not stored. JSON progress output is
still personal data; protect redirected output yourself.

The database uses SQLite `PRAGMA user_version` (currently version 1). Opening
an unversioned database from the first release upgrades it transactionally,
without rewriting mappings. Unsupported versions and missing required mapping
columns are refused; a failed upgrade does not advance the version. Do not
manually lower the version or use an older binary against newer state. Back up
privately before upgrading; schema versioning is not a downgrade mechanism.

Mappings remain until explicitly unlinked. SQLite secure-delete is enabled, but
backups and filesystem recovery are not erased by unlink. Back up the database
only through a private, SQLite-consistent backup procedure. Do not copy only the
main file during an active write or commit a personal database as a fixture.

## Design and roadmap

- [Source-backed research and measured findings](research/reading-status-companion.md)
- [First-slice plan, gates and owner-confirmed direction](plans/read-only-matching.md)
- [Matching-worklist plan and confirmed review behavior (#7)](plans/matching-worklist.md)
- [Domain language](../CONTEXT.md)
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
