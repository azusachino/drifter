package companion

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReadingSnapshotVersion is the stable wire version of the reading snapshot contract.
const ReadingSnapshotVersion = "1.0"

const (
	// DefaultReadingSnapshotLimit is the page size used when a caller does not choose one.
	DefaultReadingSnapshotLimit = 50
	// MaxReadingSnapshotLimit bounds one page so a provider crawl stays paced.
	MaxReadingSnapshotLimit = 100
)

// ErrInvalidSnapshotCursor reports a cursor this server did not issue or cannot parse.
var ErrInvalidSnapshotCursor = errors.New("invalid reading snapshot cursor")

// ErrSnapshotStateUnavailable reports unreadable or uninitialized mapping state.
var ErrSnapshotStateUnavailable = errors.New("reading snapshot state unavailable")

// ReadingSnapshot is one page of provider-neutral reading facts for an account.
// SourceIdentity and TargetIdentity name the provider account behind the local
// account labels; a label is a namespace, never an identity. An identity stays
// empty when no binding and no provider can confirm it.
type ReadingSnapshot struct {
	Version        string                `json:"version"`
	GeneratedAt    time.Time             `json:"generated_at"`
	SourceIdentity string                `json:"source_identity"`
	TargetIdentity string                `json:"target_identity,omitempty"`
	Watermark      string                `json:"watermark"`
	HasMore        bool                  `json:"has_more"`
	Cursor         string                `json:"cursor,omitempty"`
	Books          []ReadingSnapshotBook `json:"books"`
}

// ReadingSnapshotBook is the export view of one source book. Facts the source
// does not report stay absent rather than being inferred.
type ReadingSnapshotBook struct {
	SourceProvider     string                `json:"source_provider"`
	SourceBookID       string                `json:"source_book_id"`
	Title              string                `json:"title"`
	Author             string                `json:"author"`
	Translator         string                `json:"translator,omitempty"`
	Publisher          string                `json:"publisher,omitempty"`
	ISBN               string                `json:"isbn,omitempty"`
	CoverURL           string                `json:"cover_url,omitempty"`
	ReaderURL          string                `json:"reader_url,omitempty"`
	Status             string                `json:"status"`
	ProgressPercent    int                   `json:"progress_percent"`
	ReadingTimeSeconds int                   `json:"reading_time_seconds,omitempty"`
	LastReadAt         *time.Time            `json:"last_read_at,omitempty"`
	FinishedAt         *time.Time            `json:"finished_at,omitempty"`
	StartedOn          *string               `json:"started_on,omitempty"`
	IsCustom           bool                  `json:"is_custom"`
	MatchedTarget      *ReadingMatchedTarget `json:"matched_target,omitempty"`
}

// ReadingMatchedTarget is a confirmed mapping to another provider's catalog.
type ReadingMatchedTarget struct {
	TargetProvider string `json:"target_provider"`
	WorkID         int64  `json:"work_id"`
	EditionID      int64  `json:"edition_id,omitempty"`
	OriginalTitle  string `json:"original_title,omitempty"`
	OriginalAuthor string `json:"original_author,omitempty"`
}

// ReadingSnapshotOptions selects the account pair, page and clock for one snapshot page.
type ReadingSnapshotOptions struct {
	SourceAccount string
	TargetAccount string
	Limit         int
	Cursor        string
	Now           func() time.Time
	// PageBudget bounds the time spent enriching one page; zero derives it
	// from the context deadline.
	PageBudget time.Duration
}

// readingAccounts is the account pair a snapshot reads, together with the
// provider identities bound to those local labels when the store records them.
type readingAccounts struct {
	source         string
	target         string
	sourceIdentity string
	targetIdentity string
}

// resolveReadingAccounts resolves the mapping namespace pair and the provider
// identities behind it. A namespace label is local bookkeeping, so the identity
// is the recorded provider fingerprint or the live provider's own answer; it
// stays empty rather than being invented from a label.
func resolveReadingAccounts(ctx context.Context, store *Store, source Source, sourceAccount, targetAccount string) (readingAccounts, error) {
	sourceLabel, targetLabel, err := store.ResolveAccounts(ctx, sourceAccount, targetAccount)
	if err != nil {
		return readingAccounts{}, fmt.Errorf("reading snapshot requires valid source and target account labels")
	}
	fingerprints, err := store.ListAccountFingerprints(ctx)
	if err != nil {
		return readingAccounts{}, fmt.Errorf("%w: account fingerprints could not be read", ErrSnapshotStateUnavailable)
	}
	accounts := readingAccounts{source: sourceLabel, target: targetLabel}
	accounts.sourceIdentity = boundFingerprint(fingerprints, DefaultSourceProvider, accounts.source)
	if accounts.sourceIdentity == "" && sourceAccount == "" {
		// No label was demanded and nothing is bound to the inferred label: the
		// single bound source account is the honest answer.
		if bound := singleProviderFingerprint(fingerprints, DefaultSourceProvider); bound != nil {
			accounts.source, accounts.sourceIdentity = bound.Account, bound.Fingerprint
		}
	}
	accounts.targetIdentity = boundFingerprint(fingerprints, DefaultTargetProvider, accounts.target)
	if accounts.sourceIdentity == "" {
		if provider, ok := source.(SourceProvider); ok {
			if resolved, err := provider.ResolveAccountFingerprint(ctx, ""); err == nil && ValidFingerprint(resolved) {
				accounts.sourceIdentity = resolved
			}
		}
	}
	return accounts, nil
}

func boundFingerprint(fingerprints []AccountFingerprint, provider, account string) string {
	for _, fingerprint := range fingerprints {
		if fingerprint.Provider == provider && fingerprint.Account == account {
			return fingerprint.Fingerprint
		}
	}
	return ""
}

// singleProviderFingerprint returns the only account bound to a provider, and
// nil when none or several accounts are bound.
func singleProviderFingerprint(fingerprints []AccountFingerprint, provider string) *AccountFingerprint {
	var bound *AccountFingerprint
	for i := range fingerprints {
		if fingerprints[i].Provider != provider {
			continue
		}
		if bound != nil {
			return nil
		}
		bound = &fingerprints[i]
	}
	return bound
}

// ExportReadingSnapshot emits one page of shelf reading facts for an explicitly
// selected account pair, enriched with confirmed mapping targets. It opens
// state read-only and never creates or migrates the mapping database.
func ExportReadingSnapshot(ctx context.Context, dbPath string, source Source, options ReadingSnapshotOptions) (*ReadingSnapshot, error) {
	if source == nil {
		return nil, fmt.Errorf("reading snapshot requires a source provider")
	}
	if (options.SourceAccount != "" && !ValidLabel(options.SourceAccount)) || (options.TargetAccount != "" && !ValidLabel(options.TargetAccount)) {
		return nil, fmt.Errorf("reading snapshot requires valid source and target account labels")
	}
	limit := options.Limit
	if limit <= 0 {
		limit = DefaultReadingSnapshotLimit
	}
	if limit > MaxReadingSnapshotLimit {
		limit = MaxReadingSnapshotLimit
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
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
		return nil, fmt.Errorf("reading snapshot could not read the source shelf")
	}
	// Page 1 pins the run's instant; every later page repeats it from the cursor.
	pinned := time.Unix(now().Unix(), 0).UTC()
	if after != nil {
		pinned = after.pinned
	}
	targets := make(map[string]Mapping, len(mappings))
	for _, m := range mappings {
		targets[m.BookID] = m
	}
	books, cursor, err := servePage(ctx, source, orderSnapshotBooks(shelf.Books), targets, after, pinned, limit, pageBudget(ctx, options.PageBudget))
	if err != nil {
		return nil, err
	}
	return &ReadingSnapshot{
		Version:        ReadingSnapshotVersion,
		GeneratedAt:    now().UTC(),
		SourceIdentity: accounts.sourceIdentity,
		TargetIdentity: accounts.targetIdentity,
		Watermark:      pinned.Format(time.RFC3339),
		HasMore:        cursor != "",
		Cursor:         cursor,
		Books:          books,
	}, nil
}

// enrichSnapshotBook reads one shelf row and attaches the confirmed target when
// a mapping exists for the same source identity.
func enrichSnapshotBook(ctx context.Context, source Source, row ShelfBook, mapping Mapping, mapped bool) (ReadingSnapshotBook, error) {
	book, err := snapshotBook(ctx, source, row)
	if err != nil {
		return ReadingSnapshotBook{}, err
	}
	if mapped && mapping.WorkID > 0 {
		book.MatchedTarget = &ReadingMatchedTarget{
			TargetProvider: DefaultTargetProvider,
			WorkID:         mapping.WorkID,
			EditionID:      mapping.EditionID,
		}
	}
	return book, nil
}

// snapshotBook combines shelf identity, optional catalog metadata and progress.
// A failed metadata or progress lookup degrades to shelf facts; it never
// invents identity, completion or an activity instant, and it reports context
// cancellation instead of degrading further.
func snapshotBook(ctx context.Context, source Source, row ShelfBook) (ReadingSnapshotBook, error) {
	custom := strings.HasPrefix(row.ID, "CB_")
	book := ReadingSnapshotBook{
		SourceProvider: DefaultSourceProvider,
		SourceBookID:   row.ID,
		Title:          row.Title,
		Author:         row.Author,
		IsCustom:       custom,
	}
	if !custom {
		metadata, err := source.Book(ctx, row.ID)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ReadingSnapshotBook{}, ctxErr
			}
		} else if metadata.ID == row.ID {
			if title := strings.TrimSpace(metadata.Title); title != "" {
				book.Title = title
			}
			if author := strings.TrimSpace(metadata.Author); author != "" {
				book.Author = author
			}
			book.Translator = strings.TrimSpace(metadata.Translator)
			book.Publisher = strings.TrimSpace(metadata.Publisher)
			book.ISBN = strings.TrimSpace(metadata.ISBN)
			book.ReaderURL = strings.TrimSpace(metadata.ReaderURL)
		}
	}
	progress, err := source.Progress(ctx, row.ID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ReadingSnapshotBook{}, ctxErr
		}
		// Never-opened or unmatched shelf entries have no progress facts.
		progress = Progress{}
	}
	book.Status = snapshotStatus(progress)
	book.ProgressPercent = progress.Percent
	book.ReadingTimeSeconds = progress.ReadingTimeSeconds
	if progress.FinishedAt > 0 {
		finishedAt := time.Unix(progress.FinishedAt, 0).UTC()
		book.FinishedAt = &finishedAt
	}
	if progress.LastReadAt > 0 {
		lastReadAt := time.Unix(progress.LastReadAt, 0).UTC()
		book.LastReadAt = &lastReadAt
	}
	// WeRead reports no true start instant or start day, so StartedOn stays nil.
	return book, nil
}

// snapshotStatus maps progress facts onto the snapshot vocabulary.
func snapshotStatus(progress Progress) string {
	switch {
	case progress.FinishedAt > 0:
		return "completed"
	case progress.Started || progress.Percent > 0:
		return "reading"
	default:
		return "planned"
	}
}

// orderSnapshotBooks sorts by recency of last open, then by stable identity.
func orderSnapshotBooks(books []ShelfBook) []ShelfBook {
	ordered := append([]ShelfBook(nil), books...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := snapshotLastOpened(ordered[i]), snapshotLastOpened(ordered[j])
		if left != right {
			return left > right
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

func snapshotLastOpened(book ShelfBook) int64 {
	if book.LastOpened == nil {
		return 0
	}
	return *book.LastOpened
}

// snapshotCursor is the position of one feed run: the instant the run pinned on
// its first page, and the key of the last book already served. The next page
// starts strictly after that key, so a book opened mid-run (which moves to the
// front of the order) neither repeats nor shifts the page boundary.
type snapshotCursor struct {
	pinned     time.Time
	lastOpened int64
	bookID     string
}

const snapshotCursorPrefix = "run1:"

func encodeSnapshotCursor(c snapshotCursor) string {
	raw := fmt.Sprintf("%s%d:%d:%s", snapshotCursorPrefix, c.pinned.Unix(), c.lastOpened, c.bookID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ValidateReadingSnapshotCursor reports whether a cursor was issued by this contract.
func ValidateReadingSnapshotCursor(raw string) error {
	_, err := decodeSnapshotCursor(raw)
	return err
}

// decodeSnapshotCursor returns nil for an empty cursor (the first page of a
// run). A cursor without a pinned instant, such as the former offset form, is
// invalid.
func decodeSnapshotCursor(raw string) (*snapshotCursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 256 {
		return nil, ErrInvalidSnapshotCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidSnapshotCursor
	}
	rest, ok := strings.CutPrefix(string(decoded), snapshotCursorPrefix)
	if !ok {
		return nil, ErrInvalidSnapshotCursor
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 || parts[2] == "" {
		return nil, ErrInvalidSnapshotCursor
	}
	pinned, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || pinned <= 0 {
		return nil, ErrInvalidSnapshotCursor
	}
	lastOpened, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || lastOpened < 0 {
		return nil, ErrInvalidSnapshotCursor
	}
	return &snapshotCursor{pinned: time.Unix(pinned, 0).UTC(), lastOpened: lastOpened, bookID: parts[2]}, nil
}

// DefaultReadingPageBudget caps the time one page spends enriching books. It
// sits well under the server's operation timeout and a consumer's client timeout.
const DefaultReadingPageBudget = 40 * time.Second

// pageBudget returns the enrichment time budget for one page: the explicit
// option, else half the context's remaining time, never above the default.
func pageBudget(ctx context.Context, option time.Duration) time.Duration {
	if option > 0 {
		return option
	}
	budget := DefaultReadingPageBudget
	if deadline, ok := ctx.Deadline(); ok {
		if half := time.Until(deadline) / 2; half < budget {
			budget = half
		}
	}
	return budget
}

// servePage enriches the books of one page. It starts strictly after the
// cursor's key, stops at limit books or when the budget is spent (always
// serving at least one), and returns the cursor of the next page when books
// remain.
func servePage(ctx context.Context, source Source, ordered []ShelfBook, targets map[string]Mapping, after *snapshotCursor, pinned time.Time, limit int, budget time.Duration) ([]ReadingSnapshotBook, string, error) {
	start := time.Now()
	remaining := ordered
	if after != nil {
		for len(remaining) > 0 && !snapshotBefore(after.lastOpened, after.bookID, remaining[0]) {
			remaining = remaining[1:]
		}
	}
	books := []ReadingSnapshotBook{}
	for _, row := range remaining {
		if len(books) >= limit || (len(books) > 0 && time.Since(start) >= budget) {
			last := remaining[len(books)-1]
			return books, encodeSnapshotCursor(snapshotCursor{pinned: pinned, lastOpened: snapshotLastOpened(last), bookID: last.ID}), nil
		}
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		mapping, mapped := targets[row.ID]
		book, err := enrichSnapshotBook(ctx, source, row, mapping, mapped)
		if err != nil {
			return nil, "", err
		}
		books = append(books, book)
	}
	return books, "", nil
}

// snapshotBefore reports whether the key (lastOpened, id) orders before book.
func snapshotBefore(lastOpened int64, id string, book ShelfBook) bool {
	opened := snapshotLastOpened(book)
	if opened != lastOpened {
		return opened < lastOpened
	}
	return book.ID > id
}
