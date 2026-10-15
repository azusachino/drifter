# Drifter: read-only matching and local confirmation

## Intent and sequence

Drifter is a Go CLI/core, eventually runnable as a daemon, for WeRead/KU →
Hardcover status synchronization. Hardcover → Iroha stays inside Iroha jobs. The
immediate deliverable is a **read-only first vertical slice**, not a claim that
status synchronization is implemented.

Sequence: [saved research](../research/reading-status-companion.md) → this
finite plan →
[acceptance issue #1](https://github.com/azusachino/drifter/issues/1) →
implementation slices and verification → owning PR for one final owner review.
GitHub Issues is the task ledger; this document explains scope and dependencies,
not a second live checklist. No Asobi, Herdr, agent review or deployment is
authorized.

Workflow correction: initial model/provider code and focused tests were written
before this plan and acceptance issue. At that checkpoint those files were
uncommitted and work paused for this correction; it must not be retrospectively
described as plan-first or fully verified. Research and plan were then published,
issue #1 created, and implementation resumed against its criteria. Delivery
verification belongs in the owning PR, not this historical account.

## Owner-confirmed operating direction

The owner confirmed the following after a design interview. These are future
operating decisions, not an expansion of this PR's read-only acceptance scope.
See [the domain glossary](../../CONTEXT.md) for mapping, authority, baseline and
hold; these concepts must not be conflated.

- **Execution:** a persistent daemon in a single-replica k3s Deployment, not a
  Kubernetes CronJob or an internally spawned background process. The CLI/core
  remains the first slice. Deployment is separately approved work.
- **Configuration:** `config.toml` contains operational settings only. SQLite
  owns confirmed mappings and future reconciliation state. Credentials remain
  outside TOML. Validate the full configuration at startup; changes take effect
  on restart, with no file watcher or live reload.
- **Freshness:** aim to reflect source status changes within 30 minutes under
  healthy conditions. This is a requirement to validate against actual quotas,
  acquisition capability and library size, not an established service guarantee
  or a promise that one full poll may run every 30 minutes regardless of size.
- **Authority:** explicitly approve one primary source per Hardcover work and
  target account. Another source mapping does not acquire write authority merely
  because identity is confirmed. Work-only confirmation remains valid.
- **Autonomy:** following mapping and bootstrap approval, future unattended runs
  may apply unambiguous status deltas. Concurrent source/target changes or
  uncertain write outcomes hold the affected book; unrelated safe books
  continue. For example, source Read versus independently selected target Paused
  preserves Paused and opens a hold rather than overwriting the manual edit.
- **Exceptions:** durable exception state is exposed through CLI reports and
  structured daemon logs. Telegram notifications belong on the roadmap as a
  separately scoped integration, not in the first deliverable. Delivery failure
  must not lose an exception or block reconciliation; destination, credentials,
  privacy-safe payloads and deduplication remain to be specified. Resolve
  conflicts through an explicit CLI choice to accept-source or keep-target,
  against freshly fetched facts; reject stale resolutions and record the
  decision. Neither resolution commands nor these reports are implemented by the
  initial inspection/mapping CLI.
- **First deliverable:** keep issue #1's read-only CLI scope. TOML, daemon
  polling, status writes and deployment follow in separately verified slices.

Future write contracts, verified account identity, privacy policy, quota tests,
restart/single-writer safety and resolution semantics still require their owning
acceptance work. This interview does not authorize live mutation experiments,
release or deployment. An ADR is not needed merely to repeat these reversible
operating choices; keep their rationale here and domain language in the
glossary.

## Evidence driving the slice

The live five-title WeRead sample expanded into ten candidates. Eight non-empty
native ISBNs had zero exact Hardcover edition matches. Original-language
searches found plausible works, including multiple/duplicate-looking records.
WeRead also returned zero-percent progress with its started flag set. These are
measured findings; author/original-title similarity is not a confirmed identity,
and percentage alone is not unread status.

The official Tencent skill/gateway and Hardcover query APIs work with locally
provisioned keys. Import/write permissions, catalog creation, status mutation
side effects and Japanese KU acquisition are not verified. Therefore this slice
cannot contain hidden remote writes, automatic fuzzy linking, CSV re-import or
catalog creation.

## Scope and interfaces

- `internal/companion`: small source and target clients, native-fact
  normalization, candidate discovery and durable confirmed mappings. No plugin
  registry or public SDK contract.
- `internal/cli`: Cobra command tree using the shared core; JSON output for
  manual inspection and later daemon operations. Cobra owns flags/help only;
  strict settings-only TOML remains `internal/config` (no Viper binding).
- SQLite: mapping records only, scoped by provider, source-account label,
  source-book ID and target-account label. No raw responses, excerpts, device
  identifiers, personal progress archive or credentials persisted.
- Go 1.27; one pinned SQLite driver, `github.com/mattn/go-sqlite3`. It requires
  CGO and a C compiler at build time. Choose this small dependency footprint
  explicitly; pure-Go/cross-compilation is deferred, not silently promised.
- Credentials: environment variables `WEREAD_API_KEY` and `HARDCOVER_API_KEY`;
  no credential flags/files managed by Drifter. HTTPS destinations fixed in
  production; redirects forbidden; bounded responses and request timeouts.

Commands in this slice:

| Command | Outcome |
| --- | --- |
| `search --title ...` | WeRead book candidates and an explicit more-results flag; no first-result selection |
| `inspect --book-id ... [--progress]` | Identity metadata; personal native progress queried only with the explicit flag |
| `match --book-id ... [--query ...]` | Exact ISBN edition proposals, otherwise bounded title/original-language candidates; all require confirmation |
| `link --book-id ... --work-id ... --account ... --target-account ... [--edition-id ...] [--replace]` | Fetch/validate source and target identities, then record the explicit local owner selection; no Hardcover library writes |
| `links [--db ...]` | Inspect persisted mappings without network/authentication |
| `unlink --book-id ... --account ... --target-account ... [--db ...]` | Remove that local mapping only; never delete or change a source/target book |

Account labels must be supplied for confirmation and removal. They are
operator-selected namespaces, **not authenticated account IDs**. Reusing labels
after switching credentials is unsafe; stable account-identity verification is a
prerequisite for future remote writes.

## Acceptance criteria

| ID | Falsifiable requirement | Evidence |
| --- | --- | --- |
| A1 | WeRead title search returns candidate IDs/authors, accepts response groups whose scope differs from the request, and exposes incomplete pagination. | Synthetic HTTP tests; read-only source smoke test |
| A2 | Metadata validates returned book identity. Optional progress preserves 0% + started as reading, leaves unsupported/unstarted status unknown, and does not invent completion dates. Extra fields are discarded. | Synthetic identity/field/status tests; bounded live inspect |
| A3 | ISBN checksums and 10/13 equivalents are handled; missing/invalid ISBN is not a false exact match. Duplicate identifier results are ambiguous. Title searches remain unconfirmed and bounded; ranking is not permission to link. | Normalization and target-query tests; live candidate smoke test |
| A4 | Explicit work-only confirmation succeeds without assigning a false edition. Edition confirmation rejects an edition belonging to another work. Confirmed mappings survive restart and are account-scoped. Different mappings cannot silently overwrite existing ones. | Local SQLite and mocked target-membership tests |
| A5 | Listing/removing local mappings needs no credentials. Removing one mapping leaves other account scopes untouched and sends no remote request. | Store/CLI tests with isolated temp databases |
| A6 | No command sends a remote mutation, follows credential-bearing redirects, prints credentials/server error bodies or persists raw provider payloads. Errors fail visibly; there are no blind retries. SQLite state is a private regular file; symlink/insecure-file handling is tested. | Operation allowlist, HTTP/CLI safety tests, state permissions and diff inspection |
| A7 | A clean checkout supports documented formatting, vet, race-enabled tests and build through `make check`; usage explains missing capabilities and exact credential/state behavior. | Owning gate output and README walkthrough |

The sample's eight missing exact editions are an expected unresolved result, not
a failed implementation test. No particular public catalog coverage percentage
is promised.

## Ordered slices

1. **Research/acceptance checkpoint:** save source and live findings, this plan,
   and an owning issue. Preserve the initial uncommitted implementation
   honestly. This is the prerequisite for resuming code.
2. **Source inspection slice (A1, A2, A6):** core facts, read-only HTTP
   transport and WeRead commands. Verify synthetic errors/upgrade notices,
   identity mismatch, pagination and safe status interpretation.
3. **Candidate matching slice (A3, A6):** validated ISBN variants and Hardcover
   candidate queries. Verify identifier ambiguity, no-result cases, query/action
   failures and candidate ordering without auto-selection.
4. **Confirmation slice (A4, A5, A6):** SQLite mappings and CLI
   confirmation/list/removal. Validate work/edition membership before local
   confirmation; test persistence, account isolation, replacement refusal and
   file security.
5. **Delivery checkpoint (A7 plus all prior criteria):**
   format/vet/test/race/build, inspect secrets/diff, run a bounded read-only
   sample, commit on `feat/read-only-matching`, push that feature branch over
   SSH and create the owning PR. Owner performs final review; no automatic agent
   review.

Every criterion has a slice. No slice introduces remote writes or deployment.
Gates must pass before the implementation PR; a failed gate is a blocker, not
permission to substitute a weaker check. Root catalog publication is
independently gated by the workstation and may remain local without claiming
onboarding is published.

## Owning gates and verification boundaries

The project will expose `make fmt`, `make fmt-check`, `make test`, `make check`
and `make build`. `make check` requires a clean formatting check,
`go vet ./...`, `go test -race ./...` and compilation. Tests use synthetic local
HTTP servers and temporary SQLite databases, not real keys or account data. A
live smoke test is separately labeled and must use read-only provider calls; it
is not needed for offline test repeatability.

Test branches include malformed/missing fields, API-level errors with HTTP 200,
upgrade notices, 401/429/redirects, oversized responses, cancellation,
missing/invalid identifiers, duplicate candidates, wrong edition membership,
persistence, explicit replacement and account-scoped deletion. No real source
captures go into fixtures or Git.

Before creating the acceptance issue, research/plan Markdown is linted and the
scope is checked for contradictions. Those checks validate the intent records,
**not the partial implementation**. Full code-gate results will be recorded in
the delivered PR.

## Deferred outcomes and blockers

- First library sync, continuous incremental status writes, three-way target
  conflict handling, durable intents/receipts and daemon scheduling are later
  slices, not fulfilled by this prototype. The research contains their proposed
  stories and semantics.
- Automatic KU input and Japanese matching require a representative sample and a
  verified lawful acquisition route.
- Catalog/edition enrichment (`upsert_book`, identifier import or direct
  creation) needs separate write approval and mutation contract tests.
  Enumerated platforms do not prove importer success.
- Stable source/target account identity, private-source policy, per-target field
  authority and restart reconciliation must precede status writing. CLI account
  labels alone are insufficient.
- The workstation research note was moved to this project to eliminate
  duplicated authority. Root catalog files remain separate changes; root
  documentation gate failures are not waived.

## Data lifecycle and security

Confirmed mappings are personal tracking metadata, retained until explicitly
unlinked. Store only source identity facts and confirmed target IDs. The
database defaults to a private application state directory; explicit database
paths are supported with permission/symlink checks. SQLite secure-delete is
enabled, but backups/filesystem recovery are the owner's responsibility, not a
promise of forensic erasure.

Raw progress is displayed on explicit request and is not stored. Credentials
remain in the invoking environment; errors never echo response bodies or request
headers. Untrusted titles/authors are emitted as JSON, not executed as shell
commands or interpreted as instructions. No telemetry or third-party proxy
receives credentials.
