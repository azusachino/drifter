---
title: 2026-10-12 reading status companion routes
created: 2026-10-12T14:20
modified: 2026-10-12T15:52
type: research
status: maintained
maturity: developing
diataxis: explanation
tags:
  - personal-data
---

## 2026-10-12 reading status companion routes

This investigation asks how Drifter, a provider-neutral companion, could
synchronize Kindle Unlimited (KU) and WeRead reading status into Hardcover. The
owner created `azusachino/drifter`; this research was moved here from its
uncommitted workstation note on 2026-10-12 so the owning project has one
authoritative copy. Findings remain source-backed recommendations, not approval
for live writes. The implementation slice and acceptance sequence live in
[the read-only matching plan](../plans/read-only-matching.md). Scratch paths
cited below are relative to the workstation root, not this repository; they are
disposable caches, not substitutes for these findings or the public source
citations.

The boundary is **KU / WeRead → companion → Hardcover**. **Hardcover → Iroha**
remains an independent Iroha job. Neither BookOrbit nor a replacement reader is
necessary. Earlier Iroha research in
[PR #149](https://github.com/azusachino/iroha/pull/149) used a different
boundary; its proposed ADR is not approval for the companion architecture.

### what problem it solves, for whom

The owner's actual collection is primarily
**Chinese books in WeRead and Japanese books in Kindle Unlimited**, so
English-catalog/title coverage cannot be assumed. Keep reading in WeRead and
Kindle while avoiding duplicate tracking work in Hardcover. The first useful
outcome is a correctly matched book acquiring the right current reading status,
without silently changing its rating, review, privacy, edition or reading
history.

Status synchronization is narrower than progress synchronization or historical
migration. A source percentage can help establish that reading has started; the
companion need not convert it into Hardcover pages. Loan possession, return and
expiry are access events, not reading outcomes.

### conclusion and evidence boundary

| Question | Finding | Evidence level |
| --- | --- | --- |
| Can WeRead expose personal status? | Tencent documents an account-bound gateway, bookshelf completion flags and per-book reading progress. This is the strongest first adapter candidate. | First-party documented; later owner-authorized read-only metadata/progress access verified below, not shelf completeness or completion semantics [W1–W3] |
| Can Hardcover accept status changes? | Its GraphQL schema exposes `insert_user_book` and `update_user_book`, with `status_id`; documented permission is `write:library`. | First-party schema and scopes; third-party implementations corroborate usage, not live correctness [H1–H4, X1] |
| Is supported automatic KU status acquisition established? | No. Amazon documents Kindle portability data and regional eligibility, but the inspected schemas do not establish a personal current-reading/read-status feed suitable for this task. | First-party documentation inspected; no account export or authorization tested [A1–A4] |
| Is an existing bridge sufficient? | Inspected projects cover Calibre or KOReader → Hardcover, not the required KU/WeRead intake. They are design references rather than drop-in solutions. | Repository documentation/code inspection, not installation or exhaustive ecosystem proof [X1–X2] |

The recommended sequence is **WeRead → Hardcover first**, plus an explicit
manual KU input path. Keep KU automation as a separate feasibility question.
Manual input is a fallback, not a claim that the automatic KU goal has been
achieved.

The initial desk-research phase used no account credentials or personal reading
records. Later owner-authorized read-only experiments and their limits are
recorded below; no remote mutations or reader deployments were performed.
Drifter was subsequently created and partial code written; the owning plan
records the premature implementation and its unfinished state. Source
inspection alone cannot establish actual token permissions, service
eligibility, quotas, mutation side effects or account-specific matching quality.

### source acquisition routes

#### weread: first-party gateway before cookie scraping

Tencent publishes instructions to obtain an account-bound API key at
`https://weread.qq.com/r/weread-skills`. Its skill contract calls
`POST https://i.weread.qq.com/api/agent/gateway` with a Bearer key, a flat
`api_name` and `skill_version`. The pinned skill declares version `1.0.5`; its
README still says `1.0.4`. Follow the endpoint contract/version declaration
rather than hardcoding the README's stale summary. Nonzero `errcode` is failure;
`upgrade_info` requires pausing for compatibility review. [W1–W2]

The useful intake is:

1. `/shelf/sync` enumerates electronic-book entries with `bookId`, title,
   author, timestamps, `finishReading` and `secret`.
2. `/book/info` can supply ISBN and additional identity metadata for matching.
3. `/book/getprogress` supplies integer percentage, `isStartReading`, last
   reading time, cumulative reading seconds and an optional finish time. Its
   **1 means 1%, not completion**. [W3]

Use shelf completion as explicit status evidence. For incomplete or
contradictory entries, inspect per-book progress instead of inventing a status.
The source guide associates completed progress with `100` and `finishTime`;
missing finish time cannot justify inventing a finish date. Shelf audio albums
are separate: `albumInfo.finish` means publication completion, not personal
listening completion. Exclude albums and article collections from the first
book-status slice. [W3]

This is better grounded than reverse-engineering WeRead website cookies.
However, publication as an agent skill does not itself establish unrestricted
daemon use. Before scheduled polling, verify the owner's key availability,
permitted usage, quotas, bookshelf completeness, timestamp semantics and
compatibility behavior. An Apache-licensed documentation repository is not a
blanket service-access license.

#### kindle unlimited: separate access from reading status

Amazon's own Kindle guide confirms Reading Insights is visible in its app. Its
Your Books announcement describes purchased and borrowed books. Neither source
documents a supported programmatic reading-status export. Borrowed-book
inventory can help identify candidates but cannot prove completion. [A5–A6]

There are three plausible acquisition routes:

| Route | What it could offer | Limitation / next evidence |
| --- | --- | --- |
| Amazon Data Portability | Official authorized data transfer, with Kindle activity/usage/engagement scopes | Eligibility depends on registered location and regulations. Application onboarding includes identity/security assessment; transfer is asynchronous and requires HTTPS. Inspect an authorized sample for actual per-book status before designing an adapter. [A1–A4] |
| Owner-supplied records or lawful account export | Known ASIN/ISBN, explicit status and optionally an actual date | Lowest access risk; still manual or delayed. A real export format and status fields have not been verified. Do not advertise a ready-made Amazon CSV exporter. |
| Owner-authorized browser helper or authenticated automation | Might observe status already displayed in the owner's Kindle account | Exploratory only: no endpoint, parser or permitted automation contract verified here. Needs separate approval, terms review and sanitized evidence; session expiry, page changes and bot protections raise maintenance cost. No CAPTCHA/access-control bypass or DRM extraction. |

Amazon portability is not just a convenient personal API key: its onboarding and
delivery model may be disproportionate for a one-user companion. Conversely,
“Kindle has no API” would be inaccurate. The narrower finding is that
**usable KU reading-status access remains unproven**. Kindle device engagement,
purchases, highlights and loan returns must not be treated as equivalent status
feeds. [A1–A4]

A useful manual adapter accepts an explicit book identifier and owner-declared
reading status through the companion. It should label that origin `manual`, not
pretend Amazon emitted the observation. If the owner requires fully automatic KU
intake from day one, acquisition feasibility is a blocking prerequisite rather
than a small remaining implementation task.

### hardcover: a narrow status sink

Hardcover's published action table assigns `write:library` to `insert_user_book`
and `update_user_book`. Library reads use `read:library` and catalog/status
lookups use `read:catalog`; establish the exact scope set for the chosen account
query. Do not request review, social or catalog-write permissions for
status-only synchronization. [H1]

The schema separates a work-level library record (`user_books`) from reading
attempts (`user_book_reads`). Creation accepts `book_id`, optional
edition/privacy and status fields. Update accepts `status_id` plus many
unrelated fields; the companion should send only its approved fields. Both
action result types include an application-level `error`, not just a returned
identifier. [H2]

The status vocabulary observed in existing integrations is Want to Read,
Currently Reading, Read, Paused, Did Not Finish and Ignored, with IDs 1–6
respectively. The official guide explicitly identifies 2 as Currently Reading.
Resolve/validate the target status catalog before writing rather than treating
third-party numeric constants as an eternal API guarantee. [H3, X1–X3]

Proposed write flow:

1. Resolve the Hardcover account and an approved source-book mapping.
2. Read the account's existing user-book record and status.
3. Compute a status-only diff. A matching status needs no mutation.
4. For an existing record, update only `status_id`. For a new record, create it
   with approved identity and an explicit privacy policy; do not rely on an
   unknown default.
5. Inspect HTTP failure, GraphQL errors and the action's own `error` result.
6. Read back and retain a receipt. A timeout is an uncertain outcome: reconcile
   before retrying creation or marking success.

Do not insert read-attempt rows to simulate a status change, manufacture dates,
or copy BookOrbit's wider rating/privacy/history updates. Its inspected helper
catches a status update failure as a warning; that behavior is unsuitable as the
companion's definition of successful synchronization. This is a source-level
design warning, not a live failure report. [X3]

A status mutation could have server-side date, activity or read-history effects.
The schema does not prove their absence. An approved contract test must inspect
before/after status, read rows, dates, privacy, ratings and reviews. There is
also no demonstrated conditional-update guarantee: rereading immediately before
a write reduces conflict risk but does not make concurrent manual edits
race-free. Expose that limit rather than claiming strict exactly-once or
transactional synchronization.

For a personal first slice, a narrowly scoped Personal Access Token is simpler
than an OAuth application. OAuth becomes relevant if other users connect their
accounts. Hardcover documents PKCE and device authorization; refresh tokens
rotate, and replaying a refresh token can revoke its chain. A generic retry
wrapper must not blindly retry token refresh. [H4–H5]

Respect the current account's rate-limit policy and response headers. Read-only
polling, matching and reconciliation also consume budget. Throttling constants
from another client are not the service's quota definition. [H4, X3]

### proposed status mapping, not inferred reading history

| Source fact | Candidate Hardcover result | Guardrail |
| --- | --- | --- |
| WeRead `finishReading=1` | Read | Explicit completion flag; no invented dates |
| WeRead started flag or partial progress, without completion evidence | Currently Reading | A proposed mapping; resolve contradictory flags and never infer Paused from inactivity |
| WeRead shelf entry with zero/unknown progress | No automatic update initially | Shelf membership alone does not establish Want to Read |
| Explicit owner KU/manual status | Corresponding supported status | Preserve manual origin and the exact declared meaning |
| KU loan borrowed, returned or expired | No reading-status update | Access lifecycle is not completion or DNF |
| Missing book in a source snapshot | No deletion or status clearing | Absence may be removal, incomplete enumeration or access loss |
| Audio publication finished, highlights present or aggregate reading time | No completion inference | These facts do not prove a personal completed read |

Paused, DNF and Want to Read can be supported when explicitly supplied, without
forcing every source adapter to produce them. Unknown remains unknown. The same
book may be reread; avoid a universal “status only moves forward” rule that
would prevent legitimate Read → Currently Reading transitions.

### abstraction and book matching

Keep the abstraction small: **source adapters → status observations → identity
resolution and policy → Hardcover sink → receipts**. The first target is
Hardcover; a general plugin platform, event bus and arbitrary bidirectional
connectors are unnecessary.

A source observation needs provider, account reference, source-book ID, native
identity/status fields, observation time and a trustworthy source timestamp when
supplied. Keep native facts separate from the derived target status.
Provider-specific details belong in adapters, not in an expanding universal book
object.

Matching order should be:

1. A saved owner-confirmed mapping from `(provider, account, source-book ID)` to
   Hardcover work and optional edition.
2. Exact ISBN or ASIN candidates, checked against edition language,
   translator/author and format as available. Duplicate or inconsistent matches
   still need resolution.
3. Title/author searches as suggestions for owner confirmation, especially for
   Chinese works, translations and ambiguous editions.

An ASIN is not an ISBN, a WeRead ID is not a Hardcover ID, and a title match is
not permission to update a record. Work-level status can be useful without a
confirmed edition; cross-edition page progress cannot. Catalog absence becomes
an unresolved item, not an automatically created Hardcover catalog entry.

#### matching is a two-level decision

First answer **which work is this?**, then **is this exact edition known?**
Return an explicit resolution: confirmed work plus edition, confirmed work only,
ambiguous candidates, no candidate found, or deliberately excluded. Do not hide
uncertainty behind a numeric similarity score.

For status-only sync, an owner-confirmed work-only link is sufficient. For
example, a Chinese translation can be linked to its confirmed original work
without pretending that the reader consumed an English edition. Keep the missing
edition explicit and do not overwrite an existing Hardcover edition field.
Whether an omitted edition causes Hardcover to choose a default, and how that is
displayed, must be checked in the live contract test.

A different volume, abridgment, adaptation, commentary, anthology or omnibus may
be a different work, not merely another edition. A one-book-to-many-works
mapping is outside the first slice; do not mark every constituent work Read
because a collected edition has a completion flag.

#### candidate discovery and confirmation

1. **Gather native identity evidence.** WeRead `/book/info` documents ISBN,
   title, author, translator, publisher and publication time. Capture those
   fields when present; do not assume language or format is supplied by this
   endpoint. KU may supply ASIN only if the chosen lawful acquisition route
   actually exposes it. Preserve the original values and their source. [W3]
2. **Validate identifiers.** Normalize ISBN separators and check ISBN-10/13
   checksums; convert a valid ISBN-10 to its corresponding ISBN-13 when useful.
   Never reinterpret an arbitrary ten-character ASIN as an ISBN. Missing or
   invalid identifiers fall back to candidate discovery, not a guessed
   identifier.
3. **Query edition identifiers exactly.** Retrieve enough results to expose
   duplicates and inspect their parent works. An exact unique identifier with
   consistent available metadata is eligible for deterministic linking under an
   approved policy; conflicting or duplicate results require review. A
   `limit: 1` result is not proof of uniqueness. ASIN lookup is useful only
   where Hardcover has that ASIN indexed; zero results do not prove the work is
   absent. [H2, X3]
4. **Search for candidate works.** Search title/author and available alternative
   titles, then inspect editions. Hardcover's search documents title, ISBN,
   author and alternative-title fields, but ranks partly by popularity and does
   not establish semantic identity. Paginate a bounded search; do not call “not
   found” just because the first five results were poor. [H6]
5. **Rank for review, not automatic identity.** Title/subtitle, author, volume
   number, translator, publisher, date, language and format provide supporting
   or contradicting evidence. Punctuation normalization, simplified/traditional
   variants and translated titles may improve discovery, but must not erase
   volume or adaptation distinctions. Covers and page counts are clues, not
   unique identifiers.
6. **Confirm and persist.** The reader can choose a candidate, paste a Hardcover
   book/edition URL or ID, defer, or exclude the source book. Validate that a
   supplied edition belongs to the selected work. Save the mapping method,
   evidence, confirmation and work-only/edition distinction; a cache entry from
   an unreviewed fuzzy guess is not a confirmed mapping.

Recommended first release: review all bootstrap proposals, then reuse those
mappings continuously. Deterministic exact-identifier auto-linking can be
enabled separately after representative fixtures establish its behavior. Fuzzy
title/author matching remains a suggestion even if its score is high; an LLM is
not needed to authorize identity.

#### when nothing matches

“No match” is a normal per-book outcome, not permission to create a fake target
or fail the entire account sync. Separate **no candidate found** from a query
failure, an incomplete search and conflicting candidates.

| Situation | What the companion does | Reader's resolution |
| --- | --- | --- |
| Work exists, exact source edition missing | Offer an explicit work-only link; no fabricated edition/page count | Confirm work-only status sync, or add/request the correct edition through Hardcover and then link it |
| Search cannot locate a known work | Retain a pending item with source metadata and searches attempted | Paste its Hardcover URL/ID, correct identity metadata, or widen discovery manually |
| Several plausible works or duplicate identifier records | Explain the ambiguity and suppress outbound status writes for this item | Choose the right work/edition or request catalog correction |
| Work genuinely missing from the catalog | Keep it local as pending; no automatic catalog mutation | Use Hardcover's add-book/request workflow, then attach the resulting resolvable record |
| Source item is a serial, article collection, adaptation or omnibus without a suitable target | Mark unresolved/unsupported without inventing equivalence | Exclude it, or supply an explicitly justified work link if appropriate |
| Book is intentionally not tracked | Save an explicit exclusion, separate from a search miss | Re-enable it later if desired |

Hardcover's team reported manual new-book addition as released, with librarian
approval before wider visibility. Its edition announcement describes
identifier-based addition and manual routes for Supporters/librarians, plus
`#book-data-requests`. These are first-party dated announcements, not verified
present-day permissions for the owner's account. Check the current account
workflow before promising immediate catalog availability. The companion's
status-only `write:library` permission is not permission to create global
catalog records. Automated catalog creation would be a separate, explicitly
authorized feature with its own quality and duplication checks. [H1, H7–H8]

A pending item keeps its source/account/book ID, current native status facts,
identity evidence, reason and next possible action. Later observations refresh
that current state without attempting writes to an invented target. Retry
discovery on a bounded schedule or when identifiers/catalog evidence change;
explicit exclusions are not searched repeatedly. Once resolved, perform per-book
bootstrap against the **latest** source and target state, not a backlog of
obsolete status writes.

#### supported imports and automatic catalog enrichment

The owner's multilingual collection changes the practical question: not just
“can an existing entry be matched?”, but “can a missing entry be populated
through Hardcover's supported metadata sources?” A pending/manual queue alone
could leave too much recurring work. Investigate enrichment as a separate stage
before giving up, while keeping library-status permission separate from
shared-catalog permission.

**A concrete API candidate exists.** The pinned official schema exposes
`upsert_book(book: CreateBookFromPlatformInput!)`. Its input has required
`platform_id` and `external_id`, plus optional `book_id`; the result exposes
book/edition IDs, `status` and `errors`. This is a stronger lead for
platform-backed catalog intake than fabricating Goodreads CSV rows from
title/author. The schema alone does not prove which platforms/identifiers are
accepted, the meaning of each result status, synchronous completion, duplicate
handling, locale coverage or the owner's permissions. Query/verify supported
platforms rather than inventing numeric platform IDs. [H1–H2, H9]

| Route | What is evidenced | Suitability and unresolved contract |
| --- | --- | --- |
| Goodreads / StoryGraph library import | Hardcover's official changelog confirms both import paths and recent status-handling changes. | Useful for a real exported library. It does not establish that every missing Chinese/Japanese book is correctly created, nor a supported continuous-sync interface. Do not require accounts on both services merely to shuttle statuses. [H10] |
| Custom CSV import | An administrator's public response points to the account import page with a custom CSV format/sample. | Possible bootstrap/fallback export from the companion. Actual template, creation versus matching behavior, duplicate handling and re-import side effects remain untested; CSV is a transport, not an identity database. [H11] |
| Platform-backed `upsert_book` | Official schema accepts platform/external identity and returns catalog IDs/status/errors. | Most promising automation experiment: resolve external identity, request supported intake, verify the resulting work/edition, then save a mapping and sync status. Exact provider and locale support must be established. [H1–H2, H9] |
| Direct `insert_book` / `insert_edition` | Official schema provides edition DTOs with ISBN, ASIN, title, language and other metadata. | Possible metadata-backed creation fallback, but broader responsibility for duplicate avoidance, authors/contributors, language, editions and shared catalog quality. A minimal title-only record is not automatically an acceptable fallback. [H1–H2] |

The official action table associates catalog creation/upsert with catalog
scopes, not `write:library`. The capabilities document describes
`write:catalog:append` as filling empty fields and explicitly marks
`write:catalog:edit` as requiring librarian status. Do not assume all catalog
operations require a librarian, or conversely that selecting an append scope
grants unrestricted creation. Check the actual role and action authorization
independently; do not ask for `all` to make errors disappear. [H1, H9]

For **Chinese WeRead books**, begin with its documented ISBN and
publisher/translator metadata, then attempt Hardcover's verified identifier
intake. If an upstream catalog record is needed, use its real supported external
ID—not a fake Goodreads ID or a guessed English-title equivalent. Native titles
must be retained; a Chinese book need not have an English edition to be valid.

For **Japanese KU books**, a Kindle ASIN is preferable to loose title matching
when the authorized source exposes it. Hardcover documents ASIN and edition
metadata, but the inspected material does not establish that Japanese Kindle
ASINs can be fetched/imported automatically. Amazon.co.jp support is a specific
contract-test question. Kindle eBooks may omit ISBN on their Amazon page; a
publisher may expose a distinct eBook ISBN. Never substitute a print ISBN and
claim an exact Kindle-edition match. [W3, H2, H12]

A proposed enrichment path is:

1. Search existing Hardcover identities and mappings first.
2. If missing, resolve a trustworthy identifier on a platform Hardcover actually
   supports for intake.
3. Under separately approved catalog-write policy/permissions, request
   platform-backed import or metadata-backed creation.
4. Handle returned errors, pending completion and uncertain outcomes. Re-query
   before retrying creation; the name `upsert` is not proof of exactly-once
   behavior.
5. Verify the resulting work, edition, native title, contributors, volume and
   language as available. Wrong-language/wrong-volume import remains unresolved,
   not a successful match.
6. Save the confirmed mapping, then perform the status-only per-book bootstrap.
   Subsequent cycles use that mapping; they do not re-import the catalog or
   replay CSV files every time.
7. Only unsupported, ambiguous or failed enrichment falls back to owner
   resolution/exclusion.

Hardcover's library import input includes separate override flags for dates,
ratings and shelves, plus `contents_key` and `platform_id`. This supports
caution, not an implementation promise: the public upload/file-key flow,
accepted platform IDs and exact flag behavior are not established here. Repeated
bulk import may change much more than status. Test a genuine missing Chinese
book and a Japanese KU edition, inspect returned catalog records and library
side effects, and repeat once before deciding whether imports are a safe
bootstrap path. [H2]

The revised recommendation is **match → supported enrichment when missing →
verify → status sync → manual exceptions**, not “all unmatched books must
forever be added by hand.” Automation is plausible from the published schema;
unattended multilingual coverage remains unverified. This is an additional
feasibility slice, not permission to create public catalog entries now.

#### mapping lifecycle and matching acceptance

Confirmed mappings survive polls and restarts. Revalidate target existence and
work/edition membership before affected writes. A catalog merge/deletion,
material identity change or owner correction suspends that mapping until
resolved; do not silently select another search result. Correcting a mistaken
link stops future writes to the old record, but does not automatically delete or
roll back that record because other reading/manual activity may exist there.

Representative matching tests should include: a valid unique ISBN; an invalid
ISBN; duplicate ISBN records; a KU ASIN with no indexed result; a Chinese
translation with a confirmed work but missing edition; two volumes with similar
titles; an anthology; an unresolved book becoming available later; and a
corrected mapping after an earlier status write. Measure automatic-resolution
coverage and review burden on sanitized representative books before claiming
unattended matching is practical. A public API schema alone cannot establish
catalog coverage for the reader's collection.

Two source editions may map to one Hardcover work/user-book. Assign one approved
status authority per target account/record; otherwise KU and WeRead can fight
over the same status. No evidence establishes a sensible universal
KU-versus-WeRead priority. A conflicting Hardcover manual change should suspend
automatic overwrite unless the owner has expressly chosen authoritative
mirroring.

For WeRead private books, default to exclusion from outbound writes or an
explicitly approved private-target policy. Never infer that an existing public
Hardcover record may safely be reused for private source activity. Secret tokens
and native personal evidence stay out of logs, Git and public research.

### first sync: establish a baseline without assuming ownership

The first sync is a bootstrap/reconciliation operation, not a replay of every
past reading event. Its output is current approved status plus a durable
baseline for future comparisons. These behaviors are proposed requirements, not
capabilities already verified against accounts.

1. **Select scope:** connect a source account and the Hardcover target; choose
   included books, privacy behavior and supported status mappings. An
   unavailable KU adapter is reported separately, not hidden inside a successful
   WeRead run.
2. **Capture current source state:** enumerate the selected scope, distinguish a
   complete snapshot from a partial/failed fetch, and retain the native facts
   needed to explain derived status. No invented historical starts, finishes or
   read attempts.
3. **Resolve identity:** reuse existing mappings, propose identifier matches and
   leave ambiguous or absent catalog entries unresolved. A resolved book may
   proceed without blocking unrelated unresolved books.
4. **Compare Hardcover:** classify each resolved item as missing target record,
   already matching, differing status, excluded or unresolved. There is no
   previous baseline on first sync, so a differing existing Hardcover status is
   not automatically a stale value the source may overwrite.
5. **Preview and approve a bootstrap policy:** show the proposed creates/updates
   and skips. Recommended default: preserve differing existing Hardcover values
   until explicitly resolved; adopt matching records for future sync only within
   the approved ownership scope. The owner can instead approve
   source-authoritative mirroring for selected records.
6. **Apply and verify:** persist each intended change before sending it, write
   only approved fields, read back and record the result. Interrupted runs
   resume by reconciling existing intents rather than restarting blind inserts.
7. **Save the baseline:** keep the accepted source facts, confirmed mapping,
   target status and ownership decision per item. The summary separates verified
   changes, no-ops, conflicts, unresolved matches and failures. A run with
   outstanding items is partial, not “everything synced.”

A bootstrap has finished successfully only for its accepted scope. Resolving a
previously unmatched book later runs this same bootstrap comparison for that
book; it does not inherit automatic overwrite permission from the rest of the
library.

### continuous incremental sync: reconcile changes, not just poll

“Incremental” has two meanings: fetching only source changes, and applying only
target changes. The first depends on provider support; the second is a companion
requirement. The inspected WeRead contract does not establish a durable delta
cursor, webhook or reliable modified-since filter. Start with bounded snapshot
polling and locally computed differences, not a promise of incremental network
retrieval. KU acquisition remains a separate prerequisite. [W2–W3, A1–A4]

For each scheduled or manually triggered cycle:

1. Acquire the single-writer lease/lock for the account pairing; overlapping
   runs must not send competing writes.
2. Fetch source observations. Use a provider cursor only if its semantics are
   verified. A locally computed fingerprint of relevant native status facts can
   identify unchanged items; exclude poll time and unrelated metadata from that
   comparison.
3. Reconcile unfinished intents from earlier runs against Hardcover before
   issuing another write. If the desired source state has since changed, do not
   blindly replay an obsolete intent.
4. Compare changed facts with their accepted baseline, resolve new books, and
   check the target's current state for candidate writes. Periodic target
   reconciliation must also detect target changes when the source is unchanged.
5. Apply the conflict/authority policy, send only required updates, verify them,
   and advance each item's accepted baseline only for resolved outcomes. Fetch
   progress and write completion are separate checkpoints; a cursor must never
   discard an unprocessed observation.
6. Report freshness, last verified success, pending retries, blocked matches and
   conflicts. Retry temporary failures within bounded backoff and actual quotas;
   expired credentials or upgrade incompatibility need visible action rather
   than endless retries.

The comparison is three-way: current source status, current Hardcover status,
and the last accepted source/target baseline.

| Change since baseline | Default proposed behavior |
| --- | --- |
| Neither side changed | No mutation |
| Source changed; target still matches its baseline | Update under the approved source authority and verify |
| Target changed; source did not | Preserve the manual target edit; flag/rebaseline only by explicit policy, never copy it back into the source |
| Both changed to the same status | Reconcile as agreement without another mutation |
| Both changed to different statuses | Conflict; pause this item's writes unless an explicit authoritative-source policy resolves it |
| Mapping, authority, account or status-policy version changed | Re-plan/rebaseline affected items; do not reuse an old receipt as permission for a different target |

An explicitly authoritative mode may restore source status after a target edit,
but that behavior must be chosen rather than concealed as a retry. Neither
source timestamps nor local observation times establish a total ordering across
providers. Reject known older revisions when a trustworthy ordering exists;
quarantine ambiguous conflicting observations instead of asserting that the
latest poll is always the latest reading event.

Partial fetches never authorize deletions. A book disappearing even from a
complete shelf snapshot does not clear Hardcover status under the initial
policy. Removing a mapping, changing accounts or deleting local state also does
not delete the target library record. A deliberate reset requires bootstrap
again and reconciliation of possible outstanding writes.

Polling can mirror current state eventually; it cannot recover transitions that
occur between polls. If a book goes Reading → Read → Reading before the next
poll, status-only sync may see only Reading. Capturing every read attempt would
require a stronger source event/history contract and is outside this initial
goal. Scheduling frequency and expected freshness must be chosen against actual
quotas, not advertised as real-time sync.

### user stories and acceptance cases

A **user story** explains the outcome; a **case** makes its behavior testable.
The following stories cover both normal operation and the failures most likely
to damage a personal library. They are candidate acceptance criteria for the new
repository, not executed tests.

| ID | User story | Concrete acceptance case |
| --- | --- | --- |
| US1 | As a reader, I want my current source statuses brought into Hardcover without damaging what I already tracked there. | Given one missing target, one matching status and one differing existing status, first-sync preview proposes one create, one no-op and one conflict. Only approved items are applied; ratings, reviews, privacy and history are checked for unintended changes. |
| US2 | As a reader of Chinese books and translations, I want each source book linked to the right Hardcover work. | Given ambiguous title matches, no write occurs until a mapping is confirmed. A confirmed work with no source edition can sync status without assigning a false edition; a genuinely missing work stays pending. After manual catalog addition and linking, the latest status follows per-book bootstrap. The mapping survives restart; changing it requires a fresh target comparison. |
| US3 | As a reader, I want normal reading changes to synchronize without repeating setup. | Given an approved WeRead mapping, a source change from Currently Reading to Read updates that target once. Repeating the same observation produces no mutation; a newly discovered book follows per-book bootstrap. |
| US4 | As a reader, I want rereading to work rather than be blocked as backward progress. | Given a previously Read book and trustworthy new evidence that a reread has started, Currently Reading is allowed by policy. No historical read row or date is fabricated. A known older observation cannot undo a newer accepted state. |
| US5 | As a Hardcover user, I want manual edits protected or handled according to a policy I understand. | Given a manual target change since the baseline, default sync preserves it and reports a conflict. Explicit authoritative mode has a separately tested overwrite behavior; it is not enabled by bootstrap implicitly. |
| US6 | As a KU and WeRead reader, I want two copies of the same work not to fight over one Hardcover status. | Given both sources mapped to the same target with different statuses, only the approved authority can write. Without an authority decision, the item remains conflicted regardless of poll order. |
| US7 | As a reader, I want a failed or interrupted sync to recover without duplicate records. | Given a create request whose response times out, restart queries the target before resending. A verified existing record is reused; an unresolved outcome remains pending, not counted as successful. |
| US8 | As a private reader, I want private books and credentials kept private. | Given `secret=1`, default sync excludes the book. An approved private-target exception verifies privacy and refuses to reuse a public record silently. Logs and exported summaries contain no tokens. |
| US9 | As a KU reader, I want the tool to distinguish reading completion from loan management. | Given a returned or expired loan, no Read/DNF update is inferred. Explicit manual status can synchronize with manual provenance. Unavailable automatic intake is reported honestly. |
| US10 | As a user, I want to know whether sync is current and what needs my attention. | Given an expired token, provider error, partial fetch or quota response, the summary exposes stale/partial state, retry timing and blocked items. Unaffected items can succeed; no global “all synced” result hides failures. |
| US11 | As a user, I want removing a source book or disconnecting the tool not to destroy my Hardcover library. | Given source disappearance or disconnection, no target delete, status clear or credential-dependent retry occurs. Reconnecting the same account resumes safe reconciliation; a different account cannot reuse its baselines. |
| US12 | As an Iroha user, I want Hardcover to remain the boundary for my personal-data jobs. | Given a verified companion update, Iroha's independently scheduled Hardcover job can observe it later. The companion neither calls Iroha nor declares success/failure based on that job's completion. |

Additional contract cases should exercise contradictory WeRead completion facts,
missing timestamps, unchanged-source/changed-target detection, authentication
upgrade notices, overlapping runs, obsolete pending intents, a catalog record
being merged/deleted, and server-side mutation side effects. A source-first
success story does not waive these cases.

### implementation route and tradeoffs

| Route | Fit | Recommendation |
| --- | --- | --- |
| Small headless CLI/job with durable local state | Direct WeRead intake, manual KU input, inspectable diffs and scheduled synchronization | Best starting shape; SQLite is a reasonable option for mappings, pending writes and receipts |
| Calibre plus Hardcover Sync plugin | Existing library/status editor, manual matching and preview UI | Useful alternative if Calibre is already the desired hub; it does not solve KU/WeRead acquisition [X1] |
| KOReader plus ShelfSync | Reader-originated status/progress to several tracking services | Technically relevant reference, but does not solve WeRead/KU intake or the owner's KOReader usability objection [X2] |
| BookOrbit as mandatory transit | Couples tracking to another library/reader system | Unnecessary for this clarified goal; leave its existing deployment alone |
| Mobile/web companion from the outset | Convenient manual entry and matching | Additional UI/auth/hosting scope before source contracts are established; defer unless manual KU entry proves this necessary |

The headless recommendation does not select a language or runtime. Python offers
a short HTTP/SQLite implementation path; Go suits a single executable;
TypeScript suits existing web tooling. Source feasibility and conflict behavior
matter more than the stack. Choose after the owner creates the repository; no
application server, shared database or Kubernetes deployment is required to
establish the first vertical slice.

Durable state is useful even for a small tool: confirmed mappings, last source
facts, target snapshots, pending intents and verified receipts prevent blind
repeats. A single account/single writer keeps this manageable. Retain only
necessary personal fields and establish retention before live capture; a full
historical evidence warehouse is outside this status-first goal.

### read-only chinese sample: preliminary results

The owner supplied five titles, without source book IDs, authors or editions:
我的人生提问书, 体验派人生, 哲学简史, 星座全书 and 爱的终结. Public identity
discovery was performed on 2026-10-12; none of the candidate editions below is
yet confirmed as the owner's WeRead edition.

| Supplied title | Candidate identity / ambiguity found | Candidate ISBN, not confirmed source ISBN |
| --- | --- | --- |
| 我的人生提问书 | Vicki Tan / 谭竹青; the fetched Chinese record names 靳婷婷 as translator and gives original title *Ask This Book a Question*. Original author/title corroborated by the English publisher. | 9787512523821 |
| 体验派人生 | At least two distinct works: 闫晓雨's interviews/life stories, and Bridget Hilton / Joe Huff's experience-investment book. These must not be merged just because the Chinese title matches. | 9787505755864 for the 闫晓雨 candidate; 9787551334280 for the Hilton/Huff candidate; ISBNs from search/library/retailer leads, not a captured WeRead record |
| 哲学简史 | Bertrand Russell editions were found; the owner's author/edition is unknown. Do not automatically equate this with *A History of Western Philosophy* or another English philosophy history. | 9787568287739 for one Russell edition; search lead only |
| 星座全书 | Giles Sparrow astronomy candidate versus Susan Miller astrology-related title records; identity is not established by the supplied title. | 9787559613547 for the Sparrow candidate; search lead only |
| 爱的终结 | Eva Illouz / 伊娃·易洛思, subtitle 消极关系的社会学, is a candidate confirmed in the fetched ebook distributor record. The source edition is still unconfirmed. | 9787553819136 for a Chinese edition; retailer/search lead only |

**Hardcover coverage remains unknown, not “five books missing.”** Public web
searches produced no usable Hardcover listings for the queried Chinese
titles/authors and selected original titles. A direct public Hardcover search
fetch returned HTTP 403. A single unauthenticated, read-only GraphQL query for
platform names returned HTTP 400 with `No Authorization header`. No token was
supplied, so no authenticated catalog search, platform enumeration or import
test occurred. Search-engine misses are not proof of catalog absence.

As an upstream metadata check, direct Open Library ISBN JSON requests returned
404 for the five candidate identifiers `9787512523821`, `9787551334280`,
`9787568287739`, `9787559613547` and `9787553819136`. This establishes only that
those exact ISBN endpoints did not resolve during the probe; it does not rule
out different editions, work records or other upstream providers. A search also
surfaced an Open Library work for the title 体验派人生, illustrating why
title/work presence must not be treated as exact-edition availability.

The immediate blocker is twofold: establish the actual source identities
(through Tencent's authenticated search/book-info gateway, or WeRead share
links/author details), then provision a local **catalog-read-only** Hardcover
credential if the owner wants authoritative catalog queries rather than
public-search leads. Catalog reads need no library/catalog write authorization.
Credentials must not be pasted into conversation, committed or logged. Even with
read authorization, `upsert_book`/CSV creation behavior remains untested until a
separate write experiment is approved.

This sample already demonstrates a practical failure mode: search summaries can
mislabel authors/translators or select a different same-title book. The fetched
records, not a generated search answer, must anchor a candidate. The next step
is an authenticated exact-identifier/work lookup after source identity
confirmation, not automatically adding five guessed records.

The owner subsequently requested checking Tencent/WeRead MCP and status support.
The first-party `Tencent/WeChatReading` repository remains at the recorded
revision and publishes an **agent skill plus HTTP gateway**, not an MCP server.
No WeRead-specific MCP tool is exposed in this session. Community MCP wrappers
are not evidence of Tencent ownership and are unnecessary to call the official
gateway.

Credential-presence checks printed booleans only: `WEREAD_API_KEY`,
`HARDCOVER_API_TOKEN` and `HARDCOVER_TOKEN` were not set. A live,
unauthenticated read-only gateway search for 我的人生提问书, with the documented
search scope and skill version, returned **HTTP 401**, `errcode=-2010`,
`errmsg=用户不存在`. This is an authentication failure, not a book-search miss.
No book IDs, metadata or personal statuses were returned.

Once a WeRead key is securely provisioned, the documented verification sequence
is `/store/search` for the supplied titles → `/book/info` for candidate IDs →
source-identity confirmation → `/book/getprogress` for confirmed sample books.
The latter documents `progress`, `isStartReading`, `updateTime`, cumulative
seconds and optional `finishTime`; these remain documented rather than
live-verified. Search identifies catalog candidates, not necessarily which
same-title book the owner reads; narrowly scoped bookshelf evidence can resolve
that if separately used under the owner's read-only request. Do not run
whole-library exports, obtain browser cookies or send the key to third-party MCP
endpoints as a workaround.

Sample evidence:

- [Chinese 我的人生提问书 record](https://book.douban.com/subject/38591879/),
  fetched full metadata;
  [original publisher](https://www.penguinrandomhouse.com/books/746335/ask-this-book-a-question-by-vicki-tan/9780593716960/),
  fetched. Chinese distributor/catalog metadata is identity evidence, not
  confirmation of the owner's digital edition.
- [Hilton/Huff 体验派人生 ebook](https://read.douban.com/ebook/787529248/),
  fetched;
  [闫晓雨's distinct ebook](https://www.dedao.cn/ebook/detail?id=V5R16yPmaYOMqGRAv82jkX4KDe175w7jL2E3rbx6pNgznl9VZPLJQyEBodb89mqo),
  search-result lead;
  [library candidate](https://opac.lib.uibe.edu.cn/opac/book/a02f6c8a8bed1c6c20495bafcd717285),
  search-result metadata, direct fetch unavailable.
- [Russell 哲学简史 candidate](https://book.douban.com/subject/35282882/),
  search-result lead.
- [Sparrow 星座全书 retailer candidate](https://www.megbook.com.tw/mall/detail.jsp?proID=3119088),
  search-result metadata; direct readable extraction failed.
  [Susan Miller archive title](https://sema.seoul.go.kr/semaaa/front/archive/view.do?iId=50058),
  search-result lead, not a confirmed source-book identity.
- [Illouz 爱的终结 ebook](https://read.douban.com/ebook/453812216/), fetched;
  [retailer edition candidate](https://www.megbook.com.tw/mall/detail.jsp?proID=3913149),
  search-result metadata.
- Open Library probes used `https://openlibrary.org/isbn/<candidate-isbn>.json`;
  all five returned HTTP 404. These probes made no writes.
- Hardcover probe: `POST https://api.hardcover.app/v1/graphql`, JSON query
  `query { platforms { id name } }`, no Authorization header; HTTP 400. No
  mutations or personal library queries were sent.

### authenticated read-only follow-up

At the owner's request, the experiment continued after `WEREAD_API_KEY` and
`HARDCOVER_API_KEY` were configured in local fish `conf.d`. Presence was checked
without printing values. Requests went directly to the documented Tencent
gateway and Hardcover GraphQL endpoint; no third-party MCP proxy, catalog
mutation, import, user-library write or account configuration change was used.

**WeRead live verification:** all five title searches succeeded. Ten exact-title
or narrowly selected 哲学简史 variant candidates were inspected with
`/book/info` and `/book/getprogress`. This was a bounded search slice, not a
whole-library export. The philosophical search reported `hasMore=1`; its
candidate list is not exhaustive. Actual metadata confirms the same-title
collision between the two 体验派人生 works and multiple Russell
translations/editions.

| Source sample | Live WeRead identity evidence | Hardcover catalog result |
| --- | --- | --- |
| 我的人生提问书 | Book `3300228952`; 谭竹青; 靳婷婷 translation; ISBN `9787512523821` | Exact ISBN absent. Work candidate [Ask This Book a Question](https://hardcover.app/books/ask-this-book-a-question), ID `1961757`, Vicki Tan; inspected edition is English. |
| 体验派人生 | Book `3300225519`, Hilton/Huff, ISBN `9787551334280`; separately `3300066684`, 闫晓雨, ISBN `9787505755864` | Both ISBNs absent. [Experiential Billionaire](https://hardcover.app/books/experiential-billionaire), ID `1822814`, is a candidate for Hilton/Huff only; it cannot match 闫晓雨's distinct work. Owner's source selection still needs confirmation. |
| 哲学简史 | Five Russell candidates inspected: one has no ISBN; four have `9787516812693`, `9787559471963`, `9787547058619`, `9787568287739` with differing translators/publishers | All four ISBNs absent. [Wisdom of the West](https://hardcover.app/books/wisdom-of-the-west), ID `631962`, exists, but equivalence to the selected Chinese edition has not been proven. Do not substitute Russell's better-known 西方哲学史. |
| 星座全书 | Book `24282906`; Giles Sparrow; 董乐乐 translation; ISBN `9787559613547` | Exact ISBN absent. Two same-author candidate works exist: [Atlas of the Constellations](https://hardcover.app/books/atlas-of-the-constellations), ID `1407500`, and [Constellations: A Field Guide To The Night Sky](https://hardcover.app/books/constellations-a-field-guide-to-the-night-sky), ID `2792624`. Original-work identity needs verification, not first-result selection. |
| 爱的终结 | Book `3300079339`; Eva Illouz; 叶晗 translation; live ISBN and publisher fields are empty | Candidate records [The End of Love](https://hardcover.app/books/the-end-of-love-2019), ID `726723`, and [The End of Love: A Sociology of Negative Relations](https://hardcover.app/books/the-end-of-love-a-sociology-of-negative-relations), ID `2788067`, both exist. The duplicate-looking catalog records need resolution; the external Chinese ISBN lead was not emitted by WeRead. |

The exact-edition query used `_in` filters on both `isbn_13` and `isbn_10`, with
the eight non-empty ISBNs actually returned by WeRead. Hardcover returned an
empty `editions` array. Thus **0/8 queried ISBNs matched**. This is not an
overall 0/5 work-coverage result, and not proof that every Chinese edition is
absent under other identifiers.

All five native Chinese title searches returned no IDs. Six
original-language/author-assisted candidate searches were also made, with a
first-page limit of ten each. They produced seven shortlisted catalog records;
none was automatically linked. Broader search for
*The Universe and How to See It* returned unrelated works, illustrating why
search-result rank must never authorize a match.

The shortlisted records were queried for editions (cap 100 per work) and
platform mappings (cap 30 per work). No returned edition was identified as
Chinese; some lacked language metadata. ISBN matching is stronger evidence than
this bounded language view. Mappings were observed to actual Goodreads, Open
Library and Google IDs; some had `verified=true`, others `null`. These existing
**English-edition** mappings can anchor identity research, but importing them
again would not create a missing Chinese translation automatically.

**Status endpoint verified, semantics still need care:** all ten selected
`/book/getprogress` calls succeeded. Observed responses include nonzero partial
progress, zero progress with `isStartReading=1`, and zero progress with
`isStartReading=0`. Percentage alone cannot establish unread status.
Completion/finish-time behavior was not established by this sample, and no
shelf-completion cross-check or status write was performed. Native responses
also contained extra fields beyond the guide's listed summary, including
device/session identifiers and snippets; a production adapter should whitelist
necessary fields rather than persist them wholesale. Private reading
values/excerpts are intentionally not copied into this research note.

**Platform enumeration verified:** Hardcover returned Goodreads (`1`), Open
Library (`3`), Amazon (`19`), Google (`20`), StoryGraph (`27`), Hardcover CSV
(`30`) and other platforms. No explicitly named Amazon.co.jp or Douban platform
appeared in this response. This does not prove that generic Amazon intake
rejects Japanese ASINs. Platform presence and existing normalized mappings do
not prove `upsert_book` eligibility or creation behavior; no mutation was made.

An extra catalog query initially requested unsupported
`books.editions_aggregate`; Hardcover returned an HTTP-200 GraphQL validation
error. Removing that field allowed the read query to succeed. The probe treated
GraphQL errors as failures, not HTTP-200 successes; this also shows why
pinned-schema assumptions must be verified against the actual accessible API.

Practical outcome: **matching is a real unresolved engineering requirement, not
an authentication problem anymore**. The viable next path is saved work mappings
plus a separately approved Chinese-edition enrichment experiment. Direct source
metadata is useful but has missing identifiers; native title search is
insufficient; original-language discovery finds candidates but can expose
multiple works/duplicate records. No automatic-link accuracy or catalog-import
success rate has been established.

Evidence/checkpoints:

- Read-only probe source: `.tmp/book-reading-practices/read-only-probe.py` and
  `catalog-crosslinks.py`; fixed operation allowlist for Tencent, query-only
  Hardcover calls, no retries that could hide failures, credentials only in
  environment/HTTP headers.
- Catalog artifacts: `hardcover-platforms.json`,
  `hardcover-exact-editions.json`, `hardcover-work-candidates.json`,
  `hardcover-shortlist-crosslinks.json` in the same scratch directory.
- Source artifacts: five `weread-search-*.json`, ten `weread-info-*.json` and
  ten `weread-progress-*.json`. Private captures use mode 600; permissions were
  checked for all 20 metadata/progress files. Scratch remains gitignored; no
  token values are stored. Retain necessary facts only when implementing a
  durable project-owned evidence policy.
- Successful bounded runs: `bd4eeed2b` (platforms/search), `bc65c6239` (source
  details/progress and exact ISBN lookup), `b6de36456` (candidate work
  searches). Cross-link query succeeded after the explicit field correction.
- No imports, mappings or statuses were changed. Japanese KU matching and
  automatic KU acquisition were not tested because no Japanese sample/source
  access was supplied.

### first experiments for the new repository

These are later experiments requiring appropriate account/write authorization,
not operations performed by this research:

1. **WeRead read-only fixture:** capture a small sanitized set of unread,
   partial and completed books; check bookshelf completeness, key access,
   timestamps, privacy and contradictory completion evidence. Confirm allowed
   automated usage and quotas.
2. **Hardcover status contract:** use one approved test book; query the status
   catalog, change status only, read back and inspect unrelated fields/history.
   Test replay and uncertain-outcome reconciliation without creating duplicate
   library records.
3. **Matching exercise:** manually link a few representative Chinese books,
   translations and KU ASINs; measure unresolved candidates rather than silently
   broadening automatic matching.
4. **KU feasibility:** establish account region/onboarding eligibility and
   inspect one authorized portability/export sample. Continue only if it
   contains usable per-book status, identity and ordering evidence. Otherwise
   keep explicit manual input or separately approve browser-route investigation.
5. **Scheduled single-writer slice:** only after those contracts pass, add
   bounded polling, a diff preview, approved apply mode and visible
   unresolved/conflict/error receipts.

The first useful acceptance journey is one confirmed WeRead book updating the
intended Hardcover record, replaying as a no-op, and leaving unrelated fields
untouched. Manual target edits, source removal and two-source collisions must
produce visible conflicts or skips rather than silent damage. Automatic KU
success is a separate criterion; an adapter interface or manually entered status
does not satisfy it.

### what's actually transferable

Transfer the separation of **observed source facts**, **confirmed identity**,
**approved field authority** and **verified target results**. Existing Hardcover
integrations demonstrate that a reader-neutral status sink is plausible. They do
not remove the acquisition gap on KU, the Chinese-catalog matching problem or
the need to validate actual mutation behavior.

The next project can begin small without routing through Iroha or changing
readers. Keep the uncertainty attached to the source adapter where it belongs,
rather than building an elaborate generic platform around an unverified Kindle
feed.

### sources and checkpoints

Sources were inspected on 2026-10-12. GitHub `main` revisions for Tencent and
Hardcover were rechecked with
`gh api repos/<owner>/<repo>/commits/main --jq .sha` and matched the pins below.
Local copies are disposable; the public revision links are the durable
citations.

- **W1:**
  [Tencent WeChatReading README](https://github.com/Tencent/WeChatReading/blob/d98e66ee60a8c7bcd35da549d0643c4470439643/README.md):
  account-key provisioning and identity binding.
- **W2:**
  [Tencent gateway skill](https://github.com/Tencent/WeChatReading/blob/d98e66ee60a8c7bcd35da549d0643c4470439643/skills/SKILL.md):
  endpoint, authentication, flat parameters, version and upgrade handling.
- **W3:** Tencent
  [books](https://github.com/Tencent/WeChatReading/blob/d98e66ee60a8c7bcd35da549d0643c4470439643/skills/book.md)
  and
  [shelf](https://github.com/Tencent/WeChatReading/blob/d98e66ee60a8c7bcd35da549d0643c4470439643/skills/shelf.md)
  contracts. Local inspection: `.tmp/book-integration/weread/` at the same pin.
- **H1:**
  [Hardcover GraphQL actions and permission table](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/src/content/docs/api/GraphQL/Actions.mdx).
- **H2:**
  [Hardcover schema](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/schema.graphql),
  especially `UserBookCreateInput`, `UserBookUpdateInput`, `UserBookIdType` and
  the mutation root;
  [user-book documentation](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/src/content/docs/api/GraphQL/Schemas/UserBooks.mdx).
- **H3:**
  [Hardcover books-with-status guide](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/src/content/docs/api/guides/GettingBooksWithStatus.mdx).
- **H4:**
  [Hardcover getting started](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/src/content/docs/api/Getting-Started.mdx):
  API/authentication, beta warning and quota policy. Local inspection:
  `.tmp/book-reading-practices/hardcover-source/` at the same pin.
- **H5:**
  [Hardcover OAuth](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/src/content/docs/api/OAuth.mdx):
  PAT versus OAuth, PKCE/device flows and rotating-refresh-token replay
  consequences.
- **H6:**
  [Hardcover search guide](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/src/content/docs/api/guides/Searching.mdx):
  search fields, pagination, alternative titles and ranking, not an
  identity-confidence contract.
- **H7:**
  [Hardcover team: add books manually](https://roadmap.hardcover.app/feature-requests/posts/add-books-manually),
  released announcement on 2024-03-15: logged-in book addition and librarian
  approval for wider visibility.
- **H8:**
  [Hardcover: choose editions](https://roadmap.hardcover.app/feature-requests/posts/choose-which-edition-you-are-reading),
  team response on 2024-07-29: identifier-based edition addition, manual
  Supporter/librarian paths and catalog-data requests. Current account
  permissions were not tested.
- **H9:**
  [Hardcover capabilities](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/capabilities.json):
  operation classification and catalog-scope descriptions. Read with H1/H2,
  including `CreateBookFromPlatformInput`, `NewBookIdType` and `upsert_book`;
  not proof of account authorization or runtime idempotency.
- **H10:**
  [Hardcover official changelog](https://roadmap.hardcover.app/changelog),
  2026-09-15 import adjustment: Goodreads/StoryGraph importing of
  currently-reading and paused books. Its mapping to “in progress” warrants
  explicit status-preservation testing.
- **H11:**
  [Hardcover custom CSV response](https://roadmap.hardcover.app/feature-requests/posts/files-transfer-option-from-bookmory),
  administrator Govind's 2025-09-09 response: custom CSV and sample at the
  account import page. The readable extraction omitted comments; the raw HTML
  included the public comment and administrator attribution. No authenticated
  account/template was accessed.
- **H12:**
  [Hardcover ISBN and ASIN guide](https://github.com/hardcoverapp/hardcover-docs/blob/6ee717ec9a2c810d859a2a0b09a0a95354ac2a27/src/content/docs/librarians/Resources/ISBNAndASIN.mdx):
  automatic ISBN metadata intake, ASIN distinction and publisher-side eBook ISBN
  discovery. Does not verify Japanese Amazon intake.
- **A1:**
  [Amazon Data Portability overview](https://developer.amazon.com/docs/amazon-data-portability/overview.html):
  regional/account eligibility and onboarding.
- **A2:**
  [Available portability scopes](https://developer.amazon.com/docs/amazon-data-portability/available-scopes.html#kindle-device-usage-and-settings):
  Kindle activity, usage and engagement schemas. Local inspection included
  `.tmp/book-reading-practices/amazon-kindle-activity.txt` and
  `amazon-scopes.html`, alongside a fresh fetch.
- **A3:**
  [Amazon portability workflow](https://developer.amazon.com/docs/amazon-data-portability/intro-workflow.html):
  voluntary consent, asynchronous import and HTTPS requirement.
- **A4:**
  [Amazon identity and security assessment](https://developer.amazon.com/docs/amazon-data-portability/identity-and-security.html):
  onboarding case and security profile prerequisite.
- **A5:**
  [Amazon Kindle app guide](https://www.aboutamazon.com/news/devices/kindle-app-guide):
  Reading Insights in the official app, not a developer status interface.
- **A6:**
  [Amazon Your Books announcement](https://www.aboutamazon.com/news/books-and-authors/amazon-your-books-feature):
  purchased/borrowed inventory, not personal completion evidence.
- **X1:**
  [bps/hardcover-sync](https://github.com/bps/hardcover-sync/tree/a3e25b94785f1d936810372cd428d81af2765802):
  README and `src/hardcover_sync/queries.py`; Calibre linking, previews and
  status mutation examples. Source inspection only, not a license or security
  review.
- **X2:**
  [Lyfts/ShelfSync](https://github.com/Lyfts/ShelfSync/tree/b6042f52842fc64b82b568a7205aec309ca13344):
  README and repository structure; KOReader integration and Hardcover
  authentication. Source inspection only, not device acceptance.
- **X3:**
  [BookOrbit Hardcover module](https://github.com/bookorbit/bookorbit/tree/e767358443419253eeed782518d6fc4f675d69fd/server/src/modules/hardcover):
  `hardcover.constants.ts`, `hardcover-sync.service.ts`,
  `hardcover-client.service.ts` and matching/policy helpers. Local source:
  `.tmp/bookorbit-companion/source/`, pin from the earlier evaluation. Check
  licensing before any code reuse; this note borrows behavior lessons, not
  implementation.

Direct hosted Hardcover action/status documentation returned HTTP 403, so the
corresponding official, revision-pinned GitHub sources were inspected instead.
Two guessed Amazon documentation URLs returned 404; they are not evidence.
Public repository fetching used the web tool's temporary clones; no new
reference/catalog entry was created. All findings remain
documentation/source-level unless expressly labelled otherwise.
