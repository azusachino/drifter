# Drifter

Drifter relates reading facts from source services to Hardcover reading status.
This glossary separates confirmed identity from permission to change status.

## Language

**Source book**:
A book record identified within a source service and account. Two source books
with similar titles need not represent the same work.
_Avoid_: Universal book ID

**Recently opened book**:
A source book opened within the selected review window. Opening may be browsing
and does not establish reading status or completion.
_Avoid_: Currently reading book, recently finished book

**Imported source book**:
A user-imported source record rather than a source catalog record. Its identity
alone does not establish an ISBN, target work or edition.
_Avoid_: Invalid book, confirmed edition

**Target work**:
The Hardcover work selected as the destination for a source book's status.
A work may have multiple editions.
_Avoid_: Target edition when only the work is confirmed

**Edition**:
A particular publication of a work, such as a translation or format. Work
identity alone does not establish edition identity or equivalent page progress.
_Avoid_: Work used interchangeably with edition

**Mapping**: An explicitly confirmed identity relationship between a source book
and a target work, optionally including an edition. A mapping does not grant
status authority. _Avoid_: Automatic match, write approval

**Local HTTP server and mapping interface**:
The foreground `serve` process provides a local browser interface and HTTP API
for reading or explicitly maintaining confirmed mappings. It is an adapter over
the same core and SQLite state as the CLI, not a separate administrator role or
an authority to write provider status.
_Avoid_: Admin service, status-sync daemon

**Pending source book**:
An eligible recently opened source book with neither a mapping nor a suppressing
review decision for the selected source and target accounts.
_Avoid_: Currently reading book, unconfirmed automatic match

**Exclusion**:
The owner's reversible choice not to review a source book for a selected source
and target account pair. It makes no claim about target catalog coverage.
_Avoid_: Deleted source book, global exclusion

**Matching hint**:
An unconfirmed suggestion about a source book's original title, author or ISBN.
Provenance records its unverified origin; confidence describes uncertainty, not
identity confirmation or permission to link.
_Avoid_: Verified metadata, automatic mapping

**Skip**:
A choice to pass over a source book during the current review. It remains
eligible for the next review.
_Avoid_: Exclusion, snooze

**Not-in-catalog decision**:
The owner's reversible judgment that no suitable target is available for a
source book. It is not proof that the entire target catalog lacks the work.
_Avoid_: Verified catalog absence, failed lookup

**Authority**:
Explicit permission for one primary source to control a target work's reading
status within a target account. Additional identity mappings do not share that
permission automatically.
_Avoid_: Latest source wins, authority inferred from mapping

**Baseline**:
The last verified source facts and target status accepted for subsequent
comparison. A baseline is not a complete reading history or proof that every
later change belongs to the source.
_Avoid_: Latest poll, complete history

**Hold**:
A per-book suspension of automatic status changes pending resolution of a
conflict or uncertain outcome. A hold does not suspend unrelated safe books.
_Avoid_: Global sync failure, deleted mapping
