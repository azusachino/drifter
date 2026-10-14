# HTTP server: user stories and acceptance

## Scope and routes

Drifter has one foreground `cmd/drifter serve` HTTP server. Its root page is a
local mapping interface; CLI and HTTP are adapters over the same
`internal/companion` operations and SQLite state, not separate products. This
work does not implement status synchronization or write to provider libraries.

Canonical page: `GET /`. Read routes: `GET|HEAD /api/mappings`,
`GET /api/pending`, and `GET /api/csrf`. Mutations: explicit JSON endpoints
(`/api/mappings/confirm`, `/replace`, `/unlink`) and bounded native form routes
(`/mappings/confirm`, `/replace`, `/unlink`). No `/admin` compatibility alias
is retained on this unreleased branch. Confirmation identifies source
account/book and target account/work, and explicitly chooses work-only or a
specific edition. Core validates the source book and target membership; search
candidates never create mappings.

## User stories and executable test map

Approved seams are CLI commands, HTTP requests, browser journeys, and shared
core operations with synthetic providers. Tests use temporary SQLite and
synthetic providers only, never live credentials or provider calls.

| Story and acceptance | Executable evidence |
| --- | --- |
| Start without provider credentials, serve local routes and stop cleanly. | `cmd/drifter`: `TestCLIServeStartsWithoutProviderCredentials`, `TestCLIServeHTTPWiringAndCancellation`. |
| Open the root interface and list saved mappings without provider calls. | `internal/httpserver`: `TestRootRendersMappingInterface`, `TestMappingInterfaceReadsWithoutProviderCalls`. |
| Request pending explicitly; reject unsupported methods/queries and share paced clients. | `TestPendingRouteSharesLazySourceClient`, `TestConcurrentPendingRequestsShareOneProviderClient`, `TestPendingHeadIsRejectedWithoutProviderCalls`; core `TestPendingShelfFiltersBeforeMetadataAndClassifies`. |
| Confirm explicit source/target IDs; reject invalid work/edition membership; require explicit replacement on conflicts. | `TestMappingMutationHTTPUsesCoreValidationAndOwnedStore`, `TestNativeMappingFormsUseExplicitCoreActions`; core `TestDaemonOwnedConfirmationAndUnlinkUseCoreValidation`; CLI `TestCLIRejectedEditionDoesNotCreateMapping`. |
| Unlink only the selected account-scoped mapping; missing unlink is idempotent. | HTTP `TestMappingMutationHTTPUsesCoreValidationAndOwnedStore`, `TestNativeMappingFormsUseExplicitCoreActions`; core `TestStoreAccountScopedUnlink`. |
| Reject malformed/cross-origin mutation requests before provider calls or writes. | HTTP `TestMutationRejectionsPrecedeProviderCallsAndStateWrites`; browser test `native mapping form submits with same-origin Origin and no page script`. |
| Read mappings in the CLI while the server owns SQLite; refuse standalone writers. | CLI `TestCLIWritersRefuseDaemonOwnershipAndReadsRemainAvailable`; subprocess ownership tests. |
| Cancel in-flight provider work, drain requests and retain the database lease through an owned write/close race. | HTTP `TestServeCancellationPropagatesIntoInFlightPendingLookup`; core `TestOwnerCloseWaitsForInFlightOwnedWrite`. |
| Confirm, replace and unlink through the browser; remain usable at narrow widths. | `e2e/mapping-interface.spec.ts`: persisted confirm/replace/unlink journey and narrow-viewport journey. |

## Recorded red-green evidence

Root route, before installing the root handler:

```sh
go test ./internal/admin -run '^TestRootRendersMappingInterface$' -count=1
```

It failed with `status=404`. The handler returned 200 after implementation; the
page-title assertion was aligned to the rendered title, then the focused test
passed.

Native form routes, before registering their new paths:

```sh
go test ./internal/admin -run '^TestNativeMappingFormsUseExplicitCoreActions$' -count=1
```

It failed with `confirm form status=404`. After registering `/mappings/confirm`,
`/replace`, and `/unlink` and redirecting success to `/`, the focused test
passed.

Browser Origin investigation: the first `make e2e` with JavaScript disabled
failed the native form assertion (`expected 303`, `received 403`) under
`Referrer-Policy: no-referrer`; the existing confirm journey also exposed the
then-in-progress redirect from `/` back to `/admin`. Chromium sent
`Origin: null`, which the server correctly rejected. A temporary synthetic
browser experiment varied only the response policy: `no-referrer` produced
`Origin: null`, while `strict-origin-when-cross-origin` produced the exact
local Origin and the form returned 303. After setting that policy and finishing
the canonical native routes, the no-script assertion passed (303 plus exact
Origin, no script request) and the complete three-journey suite passed.

Missing-credential evidence:

```sh
go test ./cmd/drifter -run '^TestCLIServeStartsWithoutProviderCredentials$' -count=1
```

It passed with both credential variables empty; `GET /` and
`GET /api/mappings` returned 200. This is a regression test for existing
lazy-client behavior, not a newly implemented provider capability.

The red commands above ran before the behavior-preserving rename from
`internal/admin` to `internal/httpserver`; the historical paths no longer
resolve. After package rename and HTML extraction, these current-path commands
passed:

```sh
pattern='TestRootRendersMappingInterface|'
pattern+='TestNativeMappingFormsUseExplicitCoreActions'
go test ./internal/httpserver -run "$pattern" -count=1
go test ./cmd/drifter \
  -run '^TestCLIServeStartsWithoutProviderCredentials$' \
  -count=1
make e2e
```

The `no-referrer` policy was not retained as a false assurance: with the
Chromium native form it changed the security-relevant Origin value to opaque
`null`. `strict-origin-when-cross-origin` preserves same-origin Origin checks;
cross-origin requests reveal only the origin as a referrer. A separate JSON
API remains strict about JSON media type, exact Origin, Host and CSRF token.

## HTTP and safety contract

Bind to loopback by default; reject wildcard listeners. Non-loopback binds are
explicit but unauthenticated, so require an independent protected network
boundary. Require the configured Host and reject cross-origin/cross-site
browser requests. Mutation JSON requires `application/json`, a bounded strict
body, no unknown/trailing/duplicate fields, exact Origin, Host and a
per-process CSRF token. Native form posts use bounded URL-encoded routes with
the same origin and token defenses. Never relax JSON media-type checks for
simple cross-site forms. GET and HEAD do not mutate; no CORS is provided.

Responses use no-store, nosniff, CSP/frame protections and restrictive
referrer behavior. The mapping page is embedded HTML with native forms and no
JavaScript asset. Provider errors, tokens, source titles and payloads are not
logged or copied into mutation errors. Provider clients are lazy and shared per
server: page rendering and mapping listing require no credentials/calls;
pending and confirmation call providers only after explicit requests. Shared
core owns validation, conflicts and unlink scope.

The SQLite sidecar `flock` lease is held from initialization through graceful
server drain. Owned initialization/migration and each owned mutation retain an
in-process lease scope for their complete operation; `Close` rejects new scopes
and waits for current operations before releasing the OS lock. Standalone
initialization/writes take a nonblocking lock. A CLI reader under daemon
ownership may use only an already-current read-only schema and never migrates.

## Explicitly not implemented

No provider-status writes, synchronization, polling, import/export, review
hints/decisions, run history, holds, background scheduling, daemonization,
deployment or authentication framework. Do not claim the larger synchronization
milestone complete. The workflow diagram remains parent-owned and has not been
updated in this change.
