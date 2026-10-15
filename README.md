# Drifter

Drifter is a local reading-status companion for **WeRead and Hardcover**. It
helps you inspect source reading data, review Hardcover candidates, and keep an
explicit local mapping between a WeRead book and a Hardcover work or edition.
A mapping records identity only: it does not authorize or perform a provider
status write. Kindle Unlimited intake and status synchronization are not
implemented.

## What it does today

- Inspect WeRead books, search the source catalog, and explicitly request
  personal progress.
- List recently opened, eligible WeRead books without contacting Hardcover.
  Private books are excluded by default; incomplete responses remain explicit.
- Compare a source book with bounded Hardcover candidates. Candidates are
  suggestions, never automatic matches.
- Optionally surface user-selected community catalog assertions and title/ISBN
  hints with provenance; they never select or confirm a mapping.
- Confirm, explicitly replace, list, and unlink local mappings through the CLI
  or a foreground HTTP server with a local mapping interface.
- Export explicitly selected confirmed mappings or merge named local community
  catalogs with the offline `catalog` CLI commands.
- Keep confirmed mappings in a private, versioned SQLite database shared by
  both interfaces.

The project does not synchronize reading status, provide a catalog contribution
service, poll in background, import Kindle Unlimited books, or deploy a service.
See the [matching-worklist plan](docs/plans/matching-worklist.md) for later matching
work and the [HTTP server acceptance map](docs/plans/http-server.md) for the
current server stories and executable evidence. See the
[community matching catalog contract](docs/plans/matching-catalogs.md) for
source configuration, trust boundaries and limitations.

## Quick start

Prerequisites: [mise](https://mise.jdx.dev/), Make, and a C compiler for the
CGO-backed SQLite driver. The repository pins Go 1.27.2 and Node for development;
the pinned Go toolchain is required to build. Node and the Playwright browser
are needed only for browser tests.

```sh
mise trust
mise install
make check
bin/drifter help
```

`make check` formats/checks Go, runs vet and offline race-enabled tests, verifies
modules, and builds `bin/drifter`. No database service is needed.

Set provider keys in your shell only when needed:

```sh
export WEREAD_API_KEY='…'       # WeRead inspection and pending requests
export HARDCOVER_API_KEY='…'    # matching and explicit mapping confirmation
```

Credentials are never accepted as flags or TOML settings. Use your provider's
official account flow to obtain them; do not paste them into issues, logs or
Git. Read-only and local mapping operations can run without provider
credentials.

```sh
bin/drifter pending --account my-weread --target-account my-hardcover
bin/drifter match --book-id 123 --query 'Original-language title'
bin/drifter link --book-id 123 --work-id 456 \
  --account my-weread --target-account my-hardcover
bin/drifter links
```

Numbers and account labels above are examples, not captured account data. The
labels are namespaces you choose, not authenticated provider identities. Always
review the source and target yourself before confirming a mapping. Catalog
exports intentionally disclose selected WeRead source IDs and are not
anonymous; see the [catalog CLI reference](docs/cli.md#local-catalog-export-and-merge)
before sharing one. Export requires explicit source/target labels and public
provenance, and uses read-only current-schema state:

```sh
bin/drifter catalog export --account my-weread --target-account my-hardcover \
  --catalog-id my-crosswalk --publisher 'My community project' \
  --provenance-source 'public review' \
  --provenance-reference 'https://example.org/ref' \
  > catalog.json
```

`bin/drifter catalog merge --catalog-id combined --publisher 'Selected publisher'`
accepts two or more repeated `--input PATH` local documents. It runs offline and
preserves evidence without truncation; both commands write JSON to stdout, so
protect redirected files yourself.

## Local HTTP server

Start the foreground server with the optional settings-only TOML file:

```sh
bin/drifter serve --config config.toml
```

The default listener is `127.0.0.1:8081`. Open <http://127.0.0.1:8081/> for the
mapping interface. The page is server-rendered, embedded HTML and uses native
forms; it makes no provider calls just by loading or listing mappings. Pending
work is fetched only when explicitly requested. The server also provides
`/healthz`, `/readyz`, a read-only JSON mappings route, and protected JSON
mutation endpoints. See the [HTTP server and CLI reference](docs/cli.md) for
routes, payloads, configuration and database ownership behavior.

The server and CLI share validation and local state. Confirming or replacing a
mapping fetches source metadata and validates that the selected work and
optional edition exist and belong together. It does not prove that two records
are the same literary work; that remains your decision. Work-only and specific
edition selections are distinct. Unlinking changes only the local mapping.

**Security boundary:** the server is unauthenticated. Keep it on loopback. A
non-loopback listener is possible only on an explicit concrete address and
still has no authentication; do not expose it to an untrusted network or put it
behind a public reverse proxy. The per-process CSRF token is not authentication.

## Privacy and state

WeRead and Hardcover operations are read-only. Requests use fixed HTTPS provider
destinations, reject redirects, bound response sizes, and avoid logging provider
payloads or credentials. The local SQLite database contains personal mapping
metadata; protect its directory and backups. Pending JSON may contain personal
book metadata, especially when private books are explicitly included. Drifter
does not persist shelf captures, progress, provider response bodies or
credentials.

## Development

```sh
make check
mise exec -- rumdl check README.md docs/architecture.md docs/cli.md docs/plans/http-server.md
make e2e-install
make e2e-probe ROUTE=/
make e2e
```

The checks use temporary databases and synthetic provider responses; they do
not use real credentials or live provider services. `make e2e` runs the browser
journeys against a synthetic local server. See [architecture](docs/architecture.md),
[CLI reference](docs/cli.md), [test walkthroughs](docs/walkthroughs/README.md),
[research and provider evidence](docs/research/reading-status-companion.md), and
the [domain glossary](CONTEXT.md).

## License

[BSD 2-Clause](LICENSE).
