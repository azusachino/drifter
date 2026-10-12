# Drifter

Drifter relates reading facts from source services to Hardcover reading status.
This glossary separates confirmed identity from permission to change status.

## Language

**Source book**:
A book record identified within a source service and account. Two source books
with similar titles need not represent the same work.
_Avoid_: Universal book ID

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
