package companion

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// MaxChangeHorizon bounds how far a consumer's watermark may lag before a
	// delta read is no longer trustworthy and a full snapshot is required.
	MaxChangeHorizon = 14 * 24 * time.Hour
	// DefaultReadingChangesLimit is the delta page size used when none is chosen.
	DefaultReadingChangesLimit = DefaultReadingSnapshotLimit
	// MaxReadingChangesLimit bounds one delta page so a provider crawl stays paced.
	MaxReadingChangesLimit = MaxReadingSnapshotLimit
)

// ErrHorizonExceeded reports a watermark this contract will not serve deltas
// for: the consumer must take a full snapshot instead.
var ErrHorizonExceeded = errors.New("horizon exceeded: watermark older than 14 days, full snapshot required")

// ReadingChangesOptions selects the account pair, watermark, page and clock for
// one delta page.
type ReadingChangesOptions struct {
	SourceAccount string
	TargetAccount string
	// Since is the consumer's watermark, normally the previous page's watermark.
	Since  time.Time
	Limit  int
	Cursor string
	Now    func() time.Time
	// PageBudget bounds the time spent enriching one page; zero derives it
	// from the context deadline.
	PageBudget time.Duration
}

// ExportReadingChanges emits one page of shelf reading facts that changed at or
// after the consumer's watermark. A book changes when either the source reports
// a read instant at or after the watermark or its confirmed mapping was updated
// then, so a re-link is visible even when nothing was read. It opens state
// read-only and never creates or migrates the mapping database.
func ExportReadingChanges(ctx context.Context, dbPath string, source Source, options ReadingChangesOptions) (*ReadingSnapshot, error) {
	if source == nil {
		return nil, fmt.Errorf("reading changes requires a source provider")
	}
	if (options.SourceAccount != "" && !ValidLabel(options.SourceAccount)) || (options.TargetAccount != "" && !ValidLabel(options.TargetAccount)) {
		return nil, fmt.Errorf("reading changes requires valid source and target account labels")
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	current := now().UTC()
	if options.Since.IsZero() || current.Sub(options.Since.UTC()) > MaxChangeHorizon {
		return nil, ErrHorizonExceeded
	}
	since := options.Since.UTC()
	limit := options.Limit
	if limit <= 0 {
		limit = DefaultReadingChangesLimit
	}
	if limit > MaxReadingChangesLimit {
		limit = MaxReadingChangesLimit
	}
	after, err := decodeSnapshotCursor(options.Cursor)
	if err != nil {
		return nil, err
	}
	store, err := OpenStoreReadOnly(dbPath)
	if err != nil {
		return nil, fmt.Errorf("%w: confirmed mappings could not be opened", ErrSnapshotStateUnavailable)
	}
	defer store.Close()
	accounts, err := resolveReadingAccounts(ctx, store, source, options.SourceAccount, options.TargetAccount)
	if err != nil {
		return nil, err
	}
	mappings, err := store.ListMappingsForAccount(ctx, DefaultSourceProvider, accounts.source, accounts.target)
	if err != nil {
		return nil, fmt.Errorf("%w: confirmed mappings could not be read", ErrSnapshotStateUnavailable)
	}
	shelf, err := source.Shelf(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading changes could not read the source shelf")
	}
	targets := make(map[string]Mapping, len(mappings))
	for _, mapping := range mappings {
		targets[mapping.BookID] = mapping
	}
	changed := make([]ShelfBook, 0, len(shelf.Books))
	// Page 1 pins the run's watermark: never earlier than now, and not before
	// any change it reports. Later pages repeat it from the cursor.
	watermark := time.Unix(current.Unix(), 0).UTC()
	for _, row := range orderSnapshotBooks(shelf.Books) {
		mapping, mapped := targets[row.ID]
		if !bookChangedSince(row, mapping, mapped, since) {
			continue
		}
		changed = append(changed, row)
		if observed := observedChangeInstant(row, mapping, mapped); observed.After(watermark) {
			watermark = observed
		}
	}
	if after != nil {
		watermark = after.pinned
	}
	books, cursor, err := servePage(ctx, source, changed, targets, after, watermark, limit, pageBudget(ctx, options.PageBudget))
	if err != nil {
		return nil, err
	}
	return &ReadingSnapshot{
		Version:        ReadingSnapshotVersion,
		GeneratedAt:    current,
		SourceIdentity: accounts.sourceIdentity,
		TargetIdentity: accounts.targetIdentity,
		Watermark:      watermark.Format(time.RFC3339),
		HasMore:        cursor != "",
		Cursor:         cursor,
		Books:          books,
	}, nil
}

// bookChangedSince reports whether either observed change signal lands at or
// after the watermark. A missing signal is not evidence of change.
func bookChangedSince(row ShelfBook, mapping Mapping, mapped bool, since time.Time) bool {
	if row.LastOpened != nil && *row.LastOpened >= since.Unix() {
		return true
	}
	return mapped && !mapping.UpdatedAt.IsZero() && !mapping.UpdatedAt.Before(since)
}

// observedChangeInstant returns the latest instant this book is known to have
// changed at, for watermark advancement.
func observedChangeInstant(row ShelfBook, mapping Mapping, mapped bool) time.Time {
	var observed time.Time
	if row.LastOpened != nil && *row.LastOpened > 0 {
		observed = time.Unix(*row.LastOpened, 0).UTC()
	}
	if mapped && mapping.UpdatedAt.After(observed) {
		observed = mapping.UpdatedAt
	}
	return observed
}
