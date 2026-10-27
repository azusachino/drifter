# Milestone 2: verify account and provider contracts

## Intent, authority and record ownership

Milestone 2 turns the account-identity and provider-mutation prerequisites from
the [design review](../research/2026-10-15-design-review.md) and
[research foundation](../research/reading-status-companion.md) into actionable,
testable specifications.

Milestone 1 delivered local matching, terminal review, durable decisions,
and database schema versioning (PR #13, commit `0ac10d6`). While Milestone 1
established which books link together, it deliberately deferred provider status
writes and account binding:

> A mapping confirms identity; it does not grant status authority.
> User-chosen namespace labels are not verified account identities.
> Remote writes need verified mutation contracts, stable account identity,
> and explicit owner permission.

This plan specifies two foundational contracts before any synchronization
service can be built:

1. **Account identity and fingerprints**: bind stable account identities to
   operator labels, verify token rotation, and detect account changes to
   prevent cross-account contamination.
2. **Provider status mutation contract**: specify exact status-only
   mutation semantics, no-op detection, field preservation (rating, review,
   edition, dates), reread transitions, privacy handling, and rate limits
   against synthetic test doubles.

### Authority boundary

The owner authorized this planning deliverable on 2026-10-27. Planning does
**not** authorize live provider mutations, cluster deployment, or unattended
background synchronization.

All verification in Milestone 2 is performed against **in-process synthetic
doubles and temporary SQLite databases**. Live provider contract testing on a
single real test book remains subject to separate, explicit owner authorization.

## Core concepts and problem statement

### 1. Account namespaces are not account identities

In Drifter today, `--account mine` and `--target-account default` are local
operator-chosen labels. They isolate rows in SQLite, but they do not prove which
physical account credentials authenticate against.

Two failure modes exist:

- **Token rotation (safe)**: The owner refreshes an expired session token or
  gateway key for the *same* account. Mappings and baselines remain valid.
- **Account substitution (dangerous)**: The owner switches credentials in
  `secrets.env` to a *different* account under the same label (e.g. spouse or
  secondary account). Without fingerprinting, Drifter would apply user A's
  mappings and baselines to user B's shelf.

**Contract requirement**: Drifter must bind an immutable, provider-verified
account fingerprint to each `(provider, account_label)` pair on first use.
Subsequent operations verify that the active credentials match the recorded
fingerprint. A mismatch must fail closed and refuse baseline or mapping reuse.

### 2. WeRead zero-review identity discovery

Hardcover provides an explicit, documented query for the authenticated user:

```graphql
query Me {
  me {
    id
    username
  }
}
```

The fingerprint is deterministically `hardcover:<id>`.

WeRead's official gateway contract does not expose a documented `me` or `whoami`
endpoint. However, research identified that `/review/list/mine` returns
`review.userVid` and `review.author.userVid`.

- **Limitation**: This endpoint returns records only if the user has written at
  least one book review (even a private note or rating).
- **Zero-review account fallback**: An account with zero reviews returns an
  empty review array, yielding no `userVid`.
- **Policy**:
  1. Inspect `/review/list/mine`. If a review exists, extract `userVid`.
  2. If zero reviews exist, check whether `config.toml` contains an
     explicitly declared `source_vid` in the account profile. If present,
     verify it against available shelf/sync responses.
  3. If no review exists and no verified declaration is provided, fail closed
     with clear guidance to write one review or configure the verified VID,
     rather than running unauthenticated or guessing.

### 3. Hardcover status mutation surface and safety

Hardcover's GraphQL API exposes `insert_user_book` and `update_user_book` under
the `write:library` scope.

To protect library records from corruption:

- **Status only**: Drifter mutates only `status_id`. It must never overwrite
  `rating`, `review`, `notes`, or user-supplied tags.
- **Edition preservation**: A work-only mapping must not overwrite an
  already-selected specific edition on Hardcover. A specific edition mapping
  only updates if it matches or is explicitly authorized.
- **Idempotency and no-op**: If the target record is already at the desired
  status, Drifter executes **zero mutations**, consumes zero write quota, and
  updates the local baseline directly.
- **Rereading transitions**: When a finished book is reopened (Read →
  Currently Reading), Drifter must update `status_id` without wiping or
  duplicating existing read history logs in `user_book_reads`.
- **Privacy preservation**: Books marked private (`secret=1` in WeRead) must
  either be excluded from status sync or set `privacy_setting_id` to Private,
  following explicit owner policy.
- **Quota awareness**: Hardcover Free tier permits 5,000 requests/day, burst 10,
  60/minute. All writes must be paced, serialized, and free of automatic blind
  retries.

## Acceptance map

| ID | Capability | Required evidence | Delivery slice |
| --- | --- | --- | --- |
| C1 | Account fingerprint binding | First run records fingerprint; identical account re-auth succeeds; changed account refuses execution with 409/exit nonzero. | Slice 2A |
| C2 | WeRead identity fallback | Account with reviews resolves `userVid`; zero-review account resolves declared VID or fails closed with guidance. | Slice 2B |
| C3 | Hardcover status-only mutation | Synthetic double accepts status updates; asserts rating, review, and notes remain untouched. | Slice 2C |
| C4 | Idempotent no-op replay | Pre-matching status sends zero GraphQL mutations and updates baseline. | Slice 2C |
| C5 | Reread transition safety | Read → Currently Reading transition preserves historical read logs and updates status. | Slice 2C |
| C6 | Privacy-safe status sync | Private-marked source books either refuse sync or enforce private visibility on the target. | Slice 2C |
| C7 | Bounded error and timeout handling | 401 unauthenticated, 429 rate limit, and 504 uncertain timeouts record holds without retrying blind writes. | Slice 2C |
| C8 | Offline synthetic walkthrough | Reproducible test walkthrough in `docs/walkthroughs/` proving C1–C7 with synthetic providers. | Slice 2D |

## Delivery slices

### Slice 2A: Account fingerprint store and schema v3 migration

Introduce the `account_fingerprints` table via SQLite migration
`PRAGMA user_version = 3`:

```sql
CREATE TABLE IF NOT EXISTS account_fingerprints (
    provider TEXT NOT NULL,
    account TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_verified_at TEXT NOT NULL,
    PRIMARY KEY (provider, account)
);
```

Add core fingerprint operations in `internal/companion/fingerprint.go`:

- `VerifyOrBindAccountFingerprint(...)`
- If no record exists: insert `(provider, account, currentFingerprint)`.
- If record exists and matches: update `last_verified_at`.
- If record exists and differs: return `ErrAccountFingerprintMismatch`.

**Acceptance**:

- Migration from v2 preserves all existing mappings and decisions.
- Synthetic tests prove: first run binds fingerprint; subsequent run with
  same fingerprint succeeds; changed fingerprint fails closed with
  explicit mismatch error.

### Slice 2B: Provider identity resolution

Implement identity discovery in provider adapters:

- `HardcoverClient.ResolveAccountFingerprint(ctx) (string, error)`:
  Queries `me { id }` and returns `hardcover:<id>`.
- `WeReadClient.ResolveAccountFingerprint(ctx, declaredVID string)`:
  Queries `/review/list/mine`. If `userVid` found, returns
  `weread:<userVid>`. If empty reviews array:
  - If `declaredVID` matches a valid non-empty VID format: returns
    `weread:<declaredVID>`.
  - Otherwise: returns actionable error explaining zero-review discovery
    constraint.

**Acceptance**:

- Hardcover synthetic client returns user ID or handles GraphQL errors.
- WeRead synthetic client correctly handles 1-review, 0-review with
  declared VID, and 0-review without declared VID.

### Slice 2C: Hardcover status mutation synthetic double

Specify and test Hardcover mutation operations in
`internal/companion/status_mutation.go`:

1. **Status mapping**:
   - `WantToRead` = 1
   - `CurrentlyReading` = 2
   - `Read` = 3
   - `DidNotFinish` = 4
   - `OnHold` = 5
2. **Mutation execution**:
   - Inspect target's existing `user_book` status.
   - If already equal: return `StatusNoOp` (no mutation sent).
   - If missing: call `insert_user_book` with approved fields
     (`book_id`, `status_id`, `privacy_setting_id`).
   - If exists: call `update_user_book` with only `status_id`.
3. **Field preservation assertions**:
   - Synthetic double verifies that incoming GraphQL mutation variables do
     not contain `rating`, `review`, or custom notes.
4. **Failure modes**:
   - Transient failure or timeout (504): returns
     `ErrUncertainStatusMutation`, requiring baseline hold rather than
     immediate retry.

**Acceptance**:

- Synthetic test suite covering all transitions (Want to Read → Reading →
  Read → Reading).
- Assertions ensuring zero mutations when status is already current.
- Verified absence of non-status fields in mutation payloads.

### Slice 2D: Synthetic walkthrough and documentation

Update `docs/walkthroughs/README.md` with reproducible commands:

```sh
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^TestAccountFingerprint' -v
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^Test(Hardcover|WeRead)ResolveAccountFingerprint' -v
mise exec -- go test -race -count=1 ./internal/companion \
  -run '^TestHardcoverStatusMutation' -v
```

Document the verified contract and requirements for any future live one-book
experiment.

## Story traceability

- **US1 (Safe first status sync)**: Fingerprint binding ensures that the target
  account matches the mapping owner; status-only mutation prevents touching
  existing reviews or ratings.
- **US4 (Rereading)**: Read → Currently Reading transition tested on synthetic
  target without deleting read history.
- **US8 (Private reading)**: Private-flagged books enforce private target
  visibility or are prevented from synchronization.
- **US11 (Nondestructive removal/disconnection)**: Disconnecting or switching
  accounts does not delete target library items; mismatched fingerprints refuse
  accidental overwrite.

## Out of scope

- Live provider write experiments (requires separate explicit owner approval).
- Full three-way continuous reconciliation engine (Milestone 3 / Milestone 5).
- Background polling daemon or scheduler.
- Kindle Unlimited (KU) adapter (deferred to Milestone 6).
- Changes to public catalog format or offline export.
