# Community matching catalogs: contract and acceptance

**Status:** implemented under the approved contract. Schema, bounded loader,
shared-core integration and CLI reporting pass focused tests and owning gates
recorded below. Parent review remains required before publishing PR12.

## Contract

Catalogs are optional, user-selected, data-only JSON documents. Users name local
files or HTTPS locations in Drifter's settings-only TOML; there is no registry,
automatic discovery, Drifter-owned contribution process, signature trust system,
executable plugin, background refresh, or catalog persistence/cache.

A catalog carries two different kinds of untrusted evidence:

- **Assertion:** a community claim that one stable source-provider ID
  corresponds to a Hardcover work and optionally an edition.
- **Hint:** suggested title and/or ISBN information for that source ID, with an
  optional original author. A hint is not an assertion and cannot fill or
  override a target identity.

Example document (illustrative values only):

```json
{
  "catalog_schema_version": 1,
  "catalog_id": "example.weread-crosswalk",
  "publisher": "Example community project",
  "assertions": [
    {
      "record_id": "entry-123",
      "source": {"provider": "weread", "id": "123"},
      "target": {"provider": "hardcover", "work_id": 456, "edition_id": 789},
      "provenance": [
        {"source": "publisher catalog", "reference": "entry 31"}
      ]
    }
  ],
  "hints": [
    {
      "record_id": "hint-123",
      "source": {"provider": "weread", "id": "123"},
      "title": "Suggested translated title",
      "original_author": "Example Author",
      "isbn": "9780306406157",
      "confidence": 0.8,
      "provenance": [
        {"source": "maintainer note", "reference": "crosswalk row 31"}
      ]
    }
  ]
}
```

`source.provider` and the stable provider `id` form the lookup key; titles,
ISBNs, account labels and fuzzy similarity are not source identity. `catalog_id`
is the publisher's stable catalog identifier, and `record_id` is stable within
that catalog. Version 1 supports WeRead IDs and Hardcover targets. `work_id` is
a positive Hardcover work ID; `edition_id` is optional and, when present, is a
positive Hardcover edition ID. Omission means work-only. Zero is not an edition
value. Hints have no target field; assertions have no query-hint fields. A hint
requires a title or ISBN, may include an original author, and has a required
finite confidence from 0 through 1.
Confidence is displayed as uncalibrated metadata; it never filters, ranks or
authorizes candidates. Provenance is bounded plain text, shown as unverified
attribution and never dereferenced.

## Settings and request boundary

A user opts into each source explicitly. Copy the committed synthetic example
catalog to the absolute local path shown in the TOML:

```sh
cp examples/matching-catalog.json /tmp/drifter-matching-catalog.json
```

```toml
[[matching.catalogs]]
id = "synthetic-example"
file = "/tmp/drifter-matching-catalog.json"

[[matching.catalogs]]
id = "community-crosswalk"
url = "https://catalog.example.org/drifter/v1.json"
```

Each entry has a unique safe local `id` and exactly one of `file` or `url`.
Local paths are absolute, read-only inputs. Values are literal: no environment
interpolation, shell expansion, credential forwarding or automatic URL
construction. Empty catalog configuration preserves the existing `match`
behavior and output shape.

Remote catalogs are fetched only during an explicit core `Match` operation—not
at startup, server readiness, page load, pending, link or unlink. The loader
uses no provider client, authorization header, cookie jar or environment proxy.
It accepts HTTPS on port 443 only; rejects URL userinfo, query and fragment;
requires a DNS hostname; and does not follow redirects. Schema references are
resolved only from the bundled versioned schema; input cannot cause a schema
fetch.

Before connecting, resolve all A and AAAA answers. Reject the destination if
any answer is not globally routable unicast, including private, loopback,
link-local, unique-local, carrier-grade NAT, multicast, unspecified,
IPv4-mapped private, documentation and other special-purpose ranges. Pin the
validated address set into the per-fetch dialer to close the DNS-rebinding
gap, while retaining the configured hostname for TLS verification/SNI. Recheck
all answers for each fetch; mixed public/private answers fail closed. Redirects
are errors, not a second URL to validate. Tests inject resolver/dialer seams;
production must not weaken the address policy to accommodate loopback test
servers.

V1 hard limits: eight configured catalogs, 1 MiB raw or decoded bytes per
document, 4 MiB aggregate raw bytes and 4 MiB aggregate decoded catalog bytes
per match, 5,000 records per catalog, 10,000 records total, five seconds per
remote fetch and fifteen seconds total catalog-loading time.
Bounded reads use limit-plus-one detection; context cancellation applies to DNS,
connect, TLS and body reads. These limits are contract defaults, not user
settings.

## Schema, matching and trust

Publish a frozen version-1 JSON Schema at
`schemas/matching-catalog-v1.schema.json`, using JSON Schema Draft 2020-12 and a
versioned stable `$id`. Every object is closed to unknown properties; lengths,
record counts, provider names and positive numeric IDs are bounded. Hints
require at least one of title or ISBN; confidence is finite and in the range
zero to one. Runtime validation also rejects duplicate JSON object keys,
duplicate `record_id` values across both record arrays, invalid/checksum-failing
ISBNs, unsupported providers/schema versions and invalid cross-field
combinations.
Validate a whole document before using any of its rows; a malformed document
contributes no records.

The schema is the public interchange contract, not a plugin API. Freeze v1 once
published. Any new field or changed meaning requires a new versioned schema and
explicit runtime support; never reinterpret an unknown version. Continue to
accept supported older versions rather than mutating their schema. Runtime
validation uses
[`github.com/santhosh-tekuri/jsonschema/v6` v6.0.3](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6@v6.0.3).
Its official project documents Draft 2020-12 support and Go 1.21 compatibility.
Go's standard library does not validate JSON Schema. The schema is bundled and
remote `$ref` resolution is disabled.

The shared core `Match` operation receives the validated catalog snapshot and
matches only exact `(provider, source ID)` keys. It returns catalog assertions,
hints, provenance, and conflicts as separate report sections alongside the
existing Hardcover results. Hints are surfaced as suggested searches; v1 does
not silently replace `--query` or alter the existing provider search. The
operator may explicitly reuse a title with `--query`. Existing `Match` output
and requests remain unchanged when no catalogs are configured. CLI and any
future HTTP match adapter call this same core operation; no HTTP-only matching
logic is introduced.

Do not collapse conflicting claims by source order, publisher, confidence or
catalog ID. Preserve each record's subscription alias, catalog ID, record ID
and provenance. Group records with the same source key; mark the group conflicted
when target work/edition tuples differ, and show every alternative. Different
hints remain distinct. A catalog claim is never a validated provider identity:
only the existing explicit link/confirm core path validates target work and
edition membership, and the operator must still choose the identity. No catalog
record creates a mapping or writes provider status.

## Failure and completeness behavior

Invalid TOML fails before provider requests. A missing/unavailable, redirected,
unsafe, oversized, malformed or unsupported catalog produces a fixed issue
code tied to its local subscription alias; never echo its URL, path, body, DNS
answer, provider error or credential-like content into logs or errors. Reject
that document atomically, continue other configured sources and preserve the
normal Hardcover match result. The report marks `catalogs_complete: false` and
lists safe issues; the CLI prints the JSON report and exits nonzero so missing
catalog evidence cannot look complete. Successful configured catalogs set
`catalogs_complete: true`. No configured catalogs omit catalog fields and keep
today's behavior.

## Explicit local catalog tools: export and merge

These additions keep schema v1 unchanged and add no HTTP endpoint, provider
call, mapping creation, persistence, cache, plugin or background work. User
commands use the `catalog` noun and emit JSON to stdout only; a caller who uses
shell redirection is responsible for the destination file and its permissions.
Neither command accepts remote URLs.

`drifter catalog export` requires `--account LABEL`, `--target-account LABEL`,
`--catalog-id ID`, `--publisher NAME`, `--provenance-source TEXT`, and
`--provenance-reference TEXT`; `--db PATH` is optional and defaults to the
usual state database path. It opens the current-schema database through the
non-creating, non-migrating read-only store path, including while the daemon
owns the database. It filters mappings by both exact selected labels in the
query and emits only the WeRead provider source ID, confirmed Hardcover work ID,
optional edition ID, and the two explicitly supplied public provenance values.
It excludes account labels, stored titles/ISBNs, progress, timestamps, database
paths and secrets held in state. Provenance flags are caller-supplied public
text; callers must not put account aliases, local paths or secrets in them.
Source IDs are explicitly disclosed: exports are not anonymous. No database is
created or migrated. An absent database or unsupported schema is an error with
no output; a current database with no mappings in the selected account pair
produces a valid empty catalog. If the selected rows exceed v1 output limits,
export fails without partial output or truncation.

`drifter catalog merge` requires `--catalog-id ID`, `--publisher NAME`, and
at least two repeated `--input PATH` flags (at most eight). Inputs are local
JSON files, with relative paths resolved literally against the working
directory; there is no shell/environment expansion. Repeated lexically
normalized absolute paths (`filepath.Abs` plus `filepath.Clean`), repeated source
catalog IDs, or an output ID equal to an input ID are rejected.
Every input and the final document are validated against the bundled published
v1 schema and runtime semantic checks. All assertions and hints, including
conflicting assertions, are retained in input order without deduplication;
source record provenance is preserved and receives a lineage entry
`{source:"catalog merge lineage",reference:"<input-catalog-id>/<input-record-id>"}`
when not already present. If adding lineage would exceed v1's eight-entry
provenance limit, merge fails instead of truncating. Input paths and subscription
aliases are never copied into output or exposed in command errors.

Record IDs are unique across both output arrays. The first occurrence in the
explicit input order keeps its ID (assertions precede hints within one input
for collision assignment); each later collision receives
`merge-` plus the full lowercase SHA-256 hex digest of
`<input-catalog-id>\0<input-record-id>`. If that generated ID is already used,
append `-2`, `-3`, and so on to the digest in ascending order. This preserves
the original identity in lineage while providing deterministic output for the
same ordered inputs. Input order therefore intentionally affects which record
keeps a colliding original ID and the ordering of records. Repeating the same
ordered inputs with the same selected output ID and publisher is byte-for-byte
idempotent. Exact repeated input paths and duplicate input catalog IDs are
rejected rather than duplicated. Output record/byte limits are the v1 schema
limits; over-limit results fail atomically, with no partial stdout and no
truncation. Merge is offline, reads no database and contacts no provider.

Any need to change a v1 field or meaning, including lineage representation,
requires a separate schema-evolution decision and a new versioned schema; these
commands do not modify the existing filename, `$id`, or version compatibility.

## Implementation slice and acceptance

The existing v1 slice provides schema validation, bounded TOML-selected
file/HTTPS loading, and exact assertion, hint, provenance and conflict
surfacing through core `Match`. The approved extension adds only the local
export/merge operations described above. It does not add a match HTTP route,
catalog contribution service, database schema, cache, automatic target search
expansion, automatic linking, provider write or background work.

Acceptance tests use the approved seams only:

| Seam | Required evidence |
| --- | --- |
| CLI | `match --config` loads an explicitly selected local catalog; empty config preserves legacy output; malformed/unavailable sources print incomplete JSON and exit nonzero before leaking source URLs or content. `catalog export` proves required explicit selection, safe output, empty/no-state behavior and no provider construction; `catalog merge` proves local-only atomic validated output and sanitized errors. |
| Export core/store | Exact two-label SQL filtering selects only provider/source/work/edition columns; export under daemon ownership is read-only; existing empty state emits an empty catalog, absent or unsupported state is rejected without create/migration; personal metadata is absent. |
| Merge core | Input order, duplicate paths/IDs, original lineage, assertions/hints/conflicts, deterministic collision IDs, byte-for-byte idempotency, provenance overflow, malformed and oversized input, final-output limits and no partial output are covered. |
| HTTP | Existing Host/Origin/CSRF, strict JSON, read-only route and provider-laziness tests remain green; `TestRootRendersMappingInterface` confirms `/api/match` remains 404 without provider calls. The server has no catalog loader; no fetch occurs on `/`, `/api/mappings`, `/readyz` or pending. Any later HTTP match route must call the shared core operation. |
| Browser | Existing synthetic browser journeys remain unchanged; no catalog-fetch UI or browser trust decision is added in this slice. |
| Shared core | Synthetic source/target and catalog snapshots prove exact provider-ID lookup, separated assertions/hints, all alternatives on conflicts, `RequiresConfirmation`, no store mutation and no provider write. |
| Loader/security | Schema fixtures cover valid work-only/edition, unknown version/fields, duplicate keys/records, invalid ISBN and bounds. Injected network tests cover HTTPS-only, userinfo/query/fragment, unsafe/mixed DNS answers, IPv4/IPv6 special ranges, rebinding pinning, no proxy, redirects, timeouts, cancellation, size caps and no credential forwarding. |

## Red-green evidence and completion record

These behavior tests were written against the exported loader/core/CLI
operations with injected resolver/transport seams. They are implementation tests,
not a claim that the prior Chi refactor was developed test-first:

- Loader: `mise exec -- go test ./internal/matchingcatalog` failed before the
  loader implementation (synthetic catalog loading, subscription validation,
  DNS rejection, redirect behavior, gzip limits, cancellation and pinned TLS
  were missing); it now passes after validation, loading and pinned transport
  were implemented.
- Configuration: `mise exec -- go test ./internal/config -run
  'TestLoadMatchingCatalogSubscriptions|TestLoadRejectsInvalidMatchingCatalogSubscriptions'`
  failed while invalid subscriptions were accepted; it now passes after
  `Config.Load` validates literal subscriptions.
- Shared core: `mise exec -- go test ./internal/companion -run
  'TestMatch(CoreSurfacesExactCatalogEvidenceAndConflicts|WithoutCatalogsPreservesLegacyJSONAndRequests|KeepsProviderResultsWhenCatalogIsInvalid)'`
  failed while the core discarded catalog evidence; it now passes with exact-ID
  filtering, conflicts, incomplete reporting and unchanged empty-config output.
- CLI: `mise exec -- go test ./cmd/drifter -run
  'TestCLIMatchLoadsExplicitLocalCatalogThroughCore|TestCLIMatchReportsIncompleteCatalogAndPreservesProviderResult|TestCLIInvalidCatalogConfigurationPrecedesProviderConstruction'`
  failed because `match --config` was not accepted; it now passes with core-owned
  loading and report-then-nonzero incomplete behavior.
- Merge: the new `TestMergeLocal` suite first failed to build because the
  document encoder and merge API did not exist; after implementing those shared
  catalog operations, `mise exec -- go test ./internal/matchingcatalog
  -run TestMergeLocal -count=1` passes, including deterministic ID collisions,
  provenance overflow and input/output size failures.
- Export and CLI tools: `mise exec -- go test -race
  ./internal/matchingcatalog ./internal/companion ./cmd/drifter` passes the
  account-filtered, read-only export, privacy, no-state/empty-state, CLI
  no-provider and atomic-output regressions.

Validation completed on mise-managed Go 1.27.2:

- `mise exec -- make check` passed vet, module verification, full race tests and
  the CLI build.
- Pinned changed-Markdown lint passed:

  ```sh
  mise exec -- rumdl check README.md context.md docs/architecture.md \
    docs/cli.md docs/plans/matching-worklist.md \
    docs/plans/matching-catalogs.md docs/plans/read-only-matching.md
  ```

- `mise exec -- make e2e-probe ROUTE=/` passed; `mise exec -- make e2e` passed all
  three browser journeys.
- Loader, config, shared-core and CLI red-green commands above passed after the
  implementation; the HTTP suite verifies `/api/match` remains absent and all
  existing security/lifecycle behavior remains green.
- `mise exec -- make check` passed again after adding export/merge, including
  gofmt verification, vet, module verification, full race tests and CLI build.
- `mise exec -- rumdl check README.md docs/architecture.md docs/cli.md
  docs/plans/matching-worklist.md docs/plans/matching-catalogs.md` passed.
- `mise exec -- make e2e-probe ROUTE=/` passed, and `mise exec -- make e2e`
  passed all three existing browser journeys.
- Final diff check passed with no staged files. The parent-owned workflow diagram
  was intentionally not edited; its existing CLI-only scope remains accurate.

Parent review remains required before publishing PR12.
