# Drifter

![Drifter book mark](docs/branding/brand-mark.svg)

Drifter is an open-source, local-first reading companion bridging **WeRead and
Hardcover**. It provides assisted book matching, translation gap resolution via
authoritative ISBN lookups, and safe, conflict-aware reading status
synchronization.

A mapping in Drifter represents an explicitly verified identity relationship
between a source electronic book and a Hardcover work or edition. Drifter never
guesses or silently overwrites your library: all writes adhere to strict
two-phase intents, three-way merge evaluation, and privacy guards.

---

## Key Features

- **Assisted Book Matching**: Interactive terminal review and responsive web
  dashboard to discover, inspect, and confirm matches between WeRead books and
  Hardcover works or specific editions.
- **Translation Gap Bridging**: Authoritative ISBN metadata resolution (via
  Douban Books, OpenLibrary, or custom URL templates) that retrieves original
  titles and authors to match localized translations accurately.
- **Safe Three-Way Status Sync**: Synchronizes reading status (Want to Read,
  Currently Reading, Read, Did Not Finish, On Hold) while respecting a three-way
  merge contract that protects manual edits made directly on Hardcover.
- **Two-Phase Write Safety**: Every mutation is staged as a durable intent
  before dispatch, followed by read-back verification and automated restart
  reconciliation to prevent duplicate mutations during network timeouts.
- **Historical Log Preservation**: Re-reading books or updating progress never
  damages existing star ratings, written reviews, or past reading date logs.
- **Privacy by Default**: Books marked private in WeRead are excluded from
  synchronization by default, preventing unintended public exposure.
- **Hexagonal Architecture**: Pluggable provider adapters (`weread`,
  `hardcover`, `transport`) isolated from domain models, interfaces, and state.
- **Zero-Script Web Dashboard**: Clean, server-rendered admin interface with
  dark and light theme support, native form validation, and zero client JS.

---

## Real-World Use Cases

### 1. The Translation Gap

When reading localized books (for example, Chinese editions on WeRead like
《克拉拉与太阳》), Hardcover's search may fail on translated titles or return
hundreds of unrelated works.

Drifter bridges this gap by querying authoritative bibliographic sources
(Douban Books and OpenLibrary) using the book's ISBN. It extracts the original
title (*Klara and the Sun*) and author (*Kazuo Ishiguro*), searches Hardcover
with high-confidence candidates, and short-circuits to exact editions when
available.

```sh
# Resolve ISBN metadata directly
bin/drifter isbn 9787532786831

# Match a localized book using authoritative ISBN resolution
bin/drifter match --book-id 3300108679 --isbn-lookup
```

### 2. Manual Edit Protection (Three-Way Merge)

Suppose you finish reading a physical edition of a book and mark it "Read"
directly in Hardcover. Meanwhile, your WeRead digital copy is still at 60%
(Currently Reading).

A naive synchronization tool would overwrite Hardcover back to "Currently
Reading". Drifter's three-way reconciliation detects that Hardcover advanced
independently since the last known baseline, marks the book as a `conflict`,
and holds synchronization without clobbering your manual edit.

```sh
# Preview evaluation without sending remote mutations
bin/drifter sync --account my-weread --target-account my-hardcover --dry-run
```

### 3. Preserving Re-read History & Reviews

On Hardcover, marking a book "Currently Reading" again shouldn't wipe your
historical read dates or written reviews. Drifter's Hardcover adapter strictly
constrains GraphQL mutations to approved status fields, preserving your existing
`user_book_reads` history entries, star ratings, and personal notes untouched.

### 4. Flaky Network & 504 Timeout Recovery

If an HTTP request times out (HTTP 504) while updating Hardcover, standard tools
risk submitting duplicate reads. Drifter writes a persistent `sync_intents`
record before network dispatch. On the next sync run or restart, it checks the
remote state: if the mutation landed, it records a receipt and advances the
baseline; if not, it retries safely.

### 5. Private Shelf Protection

WeRead allows marking sensitive books private. Drifter's worklist scanner
strictly respects private flags: private books are skipped during normal review
and sync unless explicitly consented to with `--allow-private`.

---

## Architecture at a Glance

Drifter follows hexagonal architecture (ports and adapters), cleanly separating
domain models and state engines from external providers:

```text
┌────────────────────────────────────────────────────────┐
│                   Driving Adapters                     │
│         CLI (Cobra)       │    HTTP Server (Chi)       │
└───────────────┬───────────────────────────┬────────────┘
                │                           │
                ▼                           ▼
┌────────────────────────────────────────────────────────┐
│                    Core Domain                         │
│  • Book & Match Contracts   • Pending Worklists        │
│  • Three-Way Sync Engine    • Two-Phase Intent Ledger  │
│  • SQLite Storage (v6)      • Catalog Match & Merge    │
└───────────────┬───────────────────────────┬────────────┘
                │                           │
                ▼                           ▼
┌────────────────────────────────────────────────────────┐
│                   Driven Adapters                      │
│   internal/provider/weread   │  internal/provider/     │
│   (Intake Source Provider)   │  hardcover (Target)     │
│  ────────────────────────────┴───────────────────────  │
│          internal/provider/transport (HTTP)            │
│          internal/isbn (Douban / OpenLibrary)          │
└────────────────────────────────────────────────────────┘
```

---

## Quick Start

### Prerequisites

- [mise](https://mise.jdx.dev/) (recommended) or Go 1.27+ with CGO enabled.
- A C compiler (GCC or Clang) for the CGO-backed SQLite driver.
- Node.js (only required for running Playwright end-to-end browser tests).

### Installation & Build

```sh
git clone https://github.com/azusachino/drifter.git
cd drifter

# Install pinned toolchain via mise
mise trust && mise install

# Verify code quality, test suite, and build binaries
make check

# Inspect the built CLI executable
bin/drifter help
```

Both binaries will be placed in `bin/`:

- `bin/drifter`: Command-line tool for review, matching, and syncing.
- `bin/drifter-server`: Local web dashboard and HTTP daemon.

---

## Configuration & Credentials

Set provider tokens in your invoking shell environment:

```sh
export WEREAD_API_KEY='your-weread-token'
export HARDCOVER_API_KEY='your-hardcover-token'
```

Credentials are never accepted as command-line flags or written to config files.

You can configure operational settings using an optional `config.toml`:

```toml
[log]
level = "info" # debug | info | warn | error

[server]
addr = "127.0.0.1:8081" # Loopback listener
allowed_hosts = ["localhost:8081"]

[database]
# path = "/var/lib/drifter/mappings.sqlite" # Optional custom database path

[matching.isbn]
enabled = true
provider = "default" # "default", "douban", "openlibrary", "custom"
# url = "https://example.com/books/{isbn}" # Required if provider = "custom"
```

---

## CLI Usage Guide

### 1. Interactive Terminal Review

Discover recently opened WeRead books and match them interactively:

```sh
bin/drifter review --account my-weread --target-account my-hardcover
```

During review, you can:

- Confirm an exact edition or work-only link.
- Enter an override search query (e.g. original English title).
- Trigger authoritative ISBN lookup to resolve foreign titles.
- Skip, exclude, or mark books as not-in-catalog.

### 2. Manual Linking & Inspection

```sh
# Search WeRead source catalog
bin/drifter search "三体"

# Inspect source metadata and reading progress
bin/drifter inspect 842345 --progress

# Match Hardcover candidates
bin/drifter match --book-id 842345 --isbn-lookup

# Link source book to target Hardcover work (and optional edition)
bin/drifter link --book-id 842345 --work-id 109283 --edition-id 405912 \
  --account my-weread --target-account my-hardcover

# List confirmed mappings
bin/drifter links
```

### 3. Reading Status Synchronization

```sh
# Preview status evaluation across an account without making changes
bin/drifter sync --account my-weread --target-account my-hardcover --dry-run

# Synchronize status for a single confirmed work
bin/drifter sync --account my-weread --target-account my-hardcover --work-id 109283

# Reconcile status across all confirmed mappings in an account
bin/drifter reconcile --account my-weread --target-account my-hardcover
```

### 4. Community Catalog Export & Merge

Export your mappings as public catalog assertions with provenance, or merge
catalogs offline:

```sh
# Export confirmed mappings to share with community members
bin/drifter export --account my-weread --target-account my-hardcover \
  --catalog-id my-crosswalk --publisher 'Community Books' \
  --provenance-source 'manual review' > catalog.json

# Merge multiple community catalogs offline
bin/drifter merge --catalog-id combined --publisher 'Aggregator' \
  --input cat1.json --input cat2.json > merged.json
```

---

## Local Web Dashboard

Start the local dashboard server:

```sh
bin/drifter-server --config config.toml
```

Open <http://127.0.0.1:8081/> in your browser. The dashboard provides:

1. **Mappings**: Browse active links, inspect work IDs, and unlink mappings.
2. **Pending Worklist**: Scan recently opened books and review matches directly.
3. **Confirm a Mapping**: Manual form to establish or replace links with
   stale-target protection.
4. **Export**: One-click download of confirmed mappings in JSON format.
5. **Sync**: View recent synchronization runs, active baselines, and conflict
   holds.

---

## Security & Privacy Boundary

- **Local-First Storage**: Mappings and sync history live in your private SQLite
  database (`0600` permissions), protected by sidecar process locks (`flock`).
- **Network Safety**: All provider communication uses strict TLS HTTPS with
  pacing, 30s timeouts, 2 MiB payload caps, and sanitized error reporting.
  Redirects are denied.
- **Unauthenticated Loopback Design**: The web server is meant for loopback
  (`127.0.0.1`). If exposed to a private tailnet, always enforce Host
  allowlisting via `allowed_hosts`.
- **Zero Credential Leakage**: Secrets reside only in process memory; they are
  never printed to logs, written to databases, or included in JSON exports.

---

## Development & Verification

Drifter maintains strict quality gates:

```sh
# Full quality check: formatting, linting, race tests, module verification
make check

# Verify documentation formatting and 80-char boundaries
mise exec -- rumdl check README.md docs/*.md docs/**/*.md

# Run Playwright end-to-end browser tests
make e2e
```

---

## License

[BSD 2-Clause License](LICENSE).
