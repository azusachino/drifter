# Drifter

A headless reading-status companion: **WeRead / Kindle Unlimited → Drifter →
Hardcover**. Hardcover → Iroha remains an independent Iroha integration.

Today Drifter inspects WeRead books, lists recent unmapped ebooks, proposes
Hardcover candidates and stores explicitly confirmed local mappings. Provider
operations are read-only. Status sync, Kindle Unlimited intake, interactive
review, a persistent daemon and deployment are not implemented.

## Quick start

Install [mise](https://mise.jdx.dev/), Make and a C compiler (SQLite requires
CGO). `.mise.toml` pins Go and Markdown lint tooling for local work and CI;
`go.mod` declares the Go language requirement.

```sh
mise trust
mise install
make check
bin/drifter help
```

`make check` checks formatting, vets, verifies modules, runs offline race-enabled
tests and builds `bin/drifter`. `make fmt`, `make test` and `make build` use the
same mise-selected Go toolchain.

Set `WEREAD_API_KEY` in your shell for inspection and pending; matching and
confirmation also need `HARDCOVER_API_KEY`. Never put credentials in flags,
TOML, logs or Git.

```sh
bin/drifter pending --account my-weread --target-account my-hardcover
bin/drifter match --book-id 123 --query 'Original-language title'
bin/drifter link --book-id 123 --work-id 456 \
  --account my-weread --target-account my-hardcover
```

IDs are placeholders. Candidates never create mappings automatically. Account
labels are namespaces you choose, not authenticated identities. Private books
are excluded from pending by default; JSON output and local mappings can contain
personal metadata. A confirmed mapping does not authorize status writes.

## Documentation

- [CLI reference: commands, configuration, privacy and local state](docs/cli.md)
- [Architecture and shared core operations](docs/architecture.md)
- [Workflow diagram](docs/diagrams/cli-mapping-workflow.html)
- [Synthetic test walkthroughs](docs/walkthroughs/README.md)
- [Matching-worklist plan (#7)](docs/plans/matching-worklist.md)
- [Research and provider evidence](docs/research/reading-status-companion.md)
- [Domain glossary](CONTEXT.md)
