package companion

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
)

// ShelfRow aliases ShelfBook for candidate discovery and batch auto-matching.
type ShelfRow = ShelfBook

// AutoMatchOptions controls automated matching and confirmation behavior.
type AutoMatchOptions struct {
	Provider        string
	SourceAccount   string
	TargetAccount   string
	Days            int
	MinConfidence   float64
	Limit           int
	BookID          string
	FinishedOnly    bool
	DryRun          bool
	Force           bool
	Now             time.Time
	After           string
	AttemptCooldown time.Duration
}

// AutoMatchReport summarizes the outcome of an automated matching batch.
type AutoMatchReport struct {
	Attempted  int             `json:"attempted"`
	Confirmed  int             `json:"confirmed"`
	Skipped    int             `json:"skipped"`
	Matches    []AutoMatchItem `json:"matches"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// AutoMatchItem represents the result for a single inspected source book.
type AutoMatchItem struct {
	BookID        string        `json:"book_id"`
	SourceTitle   string        `json:"source_title"`
	SourceAuthor  string        `json:"source_author"`
	MatchedWorkID int64         `json:"matched_work_id,omitempty"`
	MatchedTitle  string        `json:"matched_title,omitempty"`
	Confidence    float64       `json:"confidence"`
	Status        string        `json:"status"` // "confirmed", "dry_run", "ambiguous", "low_confidence", "abstained", "no_candidate", "already_mapped", "error"
	Reason        string        `json:"reason,omitempty"`
	RateLimited   bool          `json:"rate_limited,omitempty"`
	RetryAfter    time.Duration `json:"retry_after,omitempty"`
}

func normalizeAutoMatchOptions(opts *AutoMatchOptions) {
	if opts.Provider == "" {
		opts.Provider = DefaultSourceProvider
	}
	if opts.SourceAccount == "" {
		opts.SourceAccount = DefaultSourceAccount
	}
	if opts.TargetAccount == "" {
		opts.TargetAccount = DefaultTargetAccount
	}
	if opts.MinConfidence <= 0 {
		opts.MinConfidence = DefaultMinConfidence
	}
	if opts.Days <= 0 {
		opts.Days = DefaultRecentJobDays
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.AttemptCooldown <= 0 {
		opts.AttemptCooldown = AutoMatchAttemptCooldown
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}
}

// DiscoverAutoMatchCandidates fetches source.Shelf(ctx) once, filters out CB_ imported books,
// private books, existing mappings, decisions ('exclude', 'not-in-catalog'), and active
// auto_match_attempts cooldowns (unless opts.Force is true), applies the Days window
// (>= 36500 includes LastOpened == 0 unread books; FinishedOnly filters to finished books),
// and sorts candidates by (Finished DESC, LastOpened DESC, ID ASC).
func DiscoverAutoMatchCandidates(
	ctx context.Context,
	store *Store,
	source Source,
	opts AutoMatchOptions,
) ([]ShelfRow, error) {
	normalizeAutoMatchOptions(&opts)
	if !ValidLabel(opts.Provider) || !ValidLabel(opts.SourceAccount) || !ValidLabel(opts.TargetAccount) {
		return nil, ErrInvalidMappingKey
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	shelf, err := source.Shelf(ctx)
	if err != nil {
		return nil, err
	}

	excluded := make(map[string]bool)
	if store != nil {
		mappings, err := store.ListMappingsForAccount(ctx, opts.Provider, opts.SourceAccount, opts.TargetAccount)
		if err != nil {
			return nil, err
		}
		for _, m := range mappings {
			excluded[m.BookID] = true
		}
		decisions, err := store.ListDecisions(ctx, opts.Provider, opts.SourceAccount, opts.TargetAccount)
		if err != nil {
			return nil, err
		}
		for _, d := range decisions {
			if d.Kind == "exclude" || d.Kind == "not-in-catalog" {
				excluded[d.BookID] = true
			}
		}
		if !opts.Force {
			cooldowns, err := store.ListCooldownBookIDsWithWindow(ctx, opts.Provider, opts.SourceAccount, opts.TargetAccount, opts.MinConfidence, opts.Force, opts.Now, opts.AttemptCooldown)
			if err != nil {
				return nil, err
			}
			for id := range cooldowns {
				excluded[id] = true
			}
		}
	}

	includeUnread := opts.Days >= MaxLookbackDays
	cutoff := opts.Now.Add(-time.Duration(opts.Days) * 24 * time.Hour).Unix()
	nowUnix := opts.Now.Unix()

	candidates := make([]ShelfRow, 0, len(shelf.Books))
	for _, row := range shelf.Books {
		if row.Secret == nil || *row.Secret != 0 {
			continue
		}
		if strings.HasPrefix(row.ID, "CB_") || !ValidBookID(row.ID) {
			continue
		}
		if excluded[row.ID] {
			continue
		}
		if opts.FinishedOnly && (row.FinishReading == nil || *row.FinishReading != 1) {
			continue
		}
		var lastOpened int64
		if row.LastOpened != nil {
			lastOpened = *row.LastOpened
		}
		if lastOpened < 0 || lastOpened > nowUnix {
			continue
		}
		if lastOpened == 0 {
			if !includeUnread {
				continue
			}
		} else if !includeUnread && lastOpened < cutoff {
			continue
		}
		candidates = append(candidates, row)
	}

	sort.Slice(candidates, func(i, j int) bool {
		finI := candidates[i].FinishReading != nil && *candidates[i].FinishReading == 1
		finJ := candidates[j].FinishReading != nil && *candidates[j].FinishReading == 1
		if finI != finJ {
			return finI
		}
		var loI, loJ int64
		if candidates[i].LastOpened != nil {
			loI = *candidates[i].LastOpened
		}
		if candidates[j].LastOpened != nil {
			loJ = *candidates[j].LastOpened
		}
		if loI != loJ {
			return loI > loJ
		}
		return candidates[i].ID < candidates[j].ID
	})

	return candidates, nil
}

// AutoMatchBookWithStore fetches source.Book(ctx, row.ID) once, reuses that Book and the
// shared *Store for matching and link confirmation (avoiding nested OpenOwnedStore handles
// and redundant source.Book calls), and records deterministic non-matches in auto_match_attempts
// when !opts.DryRun.
func AutoMatchBookWithStore(
	ctx context.Context,
	store *Store,
	source Source,
	target Target,
	adv advisor.Advisor,
	row ShelfRow,
	opts AutoMatchOptions,
) AutoMatchItem {
	normalizeAutoMatchOptions(&opts)
	book, err := source.Book(ctx, row.ID)
	if err != nil {
		return AutoMatchItem{
			BookID:       row.ID,
			SourceTitle:  row.Title,
			SourceAuthor: row.Author,
			Status:       "error",
			Reason:       err.Error(),
		}
	}
	return autoMatchLoadedBookWithStore(ctx, store, book, target, adv, opts)
}

type rateLimitTrackingAdvisor struct {
	inner        advisor.Advisor
	rateLimitErr error
}

func (a *rateLimitTrackingAdvisor) ExplainConflict(ctx context.Context, req advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
	return a.inner.ExplainConflict(ctx, req)
}

func (a *rateLimitTrackingAdvisor) ResolveOriginalWork(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
	resp, err := a.inner.ResolveOriginalWork(ctx, req)
	if err != nil && errors.Is(err, advisor.ErrRateLimited) {
		a.rateLimitErr = err
	}
	return resp, err
}

func autoMatchLoadedBookWithStore(
	ctx context.Context,
	store *Store,
	book Book,
	target Target,
	adv advisor.Advisor,
	opts AutoMatchOptions,
) AutoMatchItem {
	item := AutoMatchItem{
		BookID:       book.ID,
		SourceTitle:  book.Title,
		SourceAuthor: book.Author,
	}

	recordNonMatch := func(it AutoMatchItem) AutoMatchItem {
		if !opts.DryRun && store != nil && validDeterministicNonMatchStatus(it.Status) {
			_ = store.RecordAutoMatchAttempt(ctx, AutoMatchAttempt{
				Provider:       opts.Provider,
				SourceAccount:  opts.SourceAccount,
				BookID:         book.ID,
				TargetAccount:  opts.TargetAccount,
				LastStatus:     it.Status,
				LastConfidence: it.Confidence,
				LastReason:     it.Reason,
				AttemptedAt:    opts.Now,
			})
		}
		return it
	}

	var matchAdv advisor.Advisor
	var trackedAdv *rateLimitTrackingAdvisor
	if adv != nil {
		trackedAdv = &rateLimitTrackingAdvisor{inner: adv}
		matchAdv = trackedAdv
	}

	matchReport, err := MatchBookWithOptions(ctx, book, target, "", MatchOptions{
		Advisor: matchAdv,
	})
	if err == nil && trackedAdv != nil && trackedAdv.rateLimitErr != nil {
		err = trackedAdv.rateLimitErr
	}
	if err != nil {
		item.Status = "error"
		item.Reason = err.Error()
		if errors.Is(err, advisor.ErrRateLimited) {
			item.RateLimited = true
			var rle *advisor.RateLimitError
			if errors.As(err, &rle) && rle != nil {
				item.RetryAfter = rle.RetryAfter
			}
		}
		return item
	}

	var chosenWorkID int64
	var chosenEditionID int64
	var chosenTitle string
	var confidence float64

	if len(matchReport.ExactEditions) > 0 {
		chosenWorkID = matchReport.ExactEditions[0].WorkID
		chosenEditionID = matchReport.ExactEditions[0].ID
		chosenTitle = matchReport.ExactEditions[0].Title
		confidence = 1.0
	} else if matchReport.AdvisorResult != nil && !matchReport.AdvisorResult.Abstained {
		confidence = matchReport.AdvisorResult.Confidence
		if confidence < opts.MinConfidence {
			item.Confidence = confidence
			item.Status = "low_confidence"
			item.Reason = fmt.Sprintf("confidence %.2f below threshold %.2f", confidence, opts.MinConfidence)
			return recordNonMatch(item)
		}
		if len(matchReport.Candidates) == 0 {
			item.Confidence = confidence
			item.Status = "no_candidate"
			item.Reason = "no candidate works found on target"
			return recordNonMatch(item)
		}
		if len(matchReport.Candidates) == 1 {
			chosenWorkID = matchReport.Candidates[0].ID
			chosenEditionID = 0
			chosenTitle = matchReport.Candidates[0].Title
		} else {
			var authorMatches []Candidate
			for _, c := range matchReport.Candidates {
				if candidateHasAuthor(c, matchReport.AdvisorResult.OriginalAuthor) || (book.Author != "" && candidateHasAuthor(c, book.Author)) {
					authorMatches = append(authorMatches, c)
				}
			}
			if len(authorMatches) == 1 {
				chosenWorkID = authorMatches[0].ID
				chosenEditionID = 0
				chosenTitle = authorMatches[0].Title
			} else if len(authorMatches) == 0 && strings.EqualFold(strings.TrimSpace(matchReport.Candidates[0].Title), strings.TrimSpace(matchReport.AdvisorResult.OriginalTitle)) {
				chosenWorkID = matchReport.Candidates[0].ID
				chosenEditionID = 0
				chosenTitle = matchReport.Candidates[0].Title
			} else {
				item.Confidence = confidence
				item.Status = "ambiguous"
				item.Reason = fmt.Sprintf("%d candidate works found without single author/title resolution", len(matchReport.Candidates))
				return recordNonMatch(item)
			}
		}
	} else {
		item.Status = "abstained"
		if matchReport.AdvisorResult != nil {
			item.Reason = matchReport.AdvisorResult.Rationale
		} else {
			item.Reason = "no exact match and no advisor resolution"
		}
		return recordNonMatch(item)
	}

	item.MatchedWorkID = chosenWorkID
	item.MatchedTitle = chosenTitle
	item.Confidence = confidence

	if opts.DryRun {
		item.Status = "dry_run"
		return item
	}

	selection := Mapping{
		Provider:      opts.Provider,
		SourceAccount: opts.SourceAccount,
		BookID:        book.ID,
		TargetAccount: opts.TargetAccount,
		WorkID:        chosenWorkID,
		EditionID:     chosenEditionID,
	}
	if err := ConfirmLinkWithStore(ctx, store, book, target, selection, false); err != nil {
		item.Status = "error"
		item.Reason = err.Error()
		return item
	}
	item.Status = "confirmed"
	return item
}

// AutoMatchOwned inspects pending reading books and automatically confirms high-confidence
// matches (by exact ISBN or LLM advisor resolution meeting min_confidence).
func AutoMatchOwned(
	ctx context.Context,
	store *Store,
	source Source,
	target Target,
	dbPath string,
	owner *DatabaseOwnership,
	adv advisor.Advisor,
	opts AutoMatchOptions,
) (AutoMatchReport, error) {
	normalizeAutoMatchOptions(&opts)

	report := AutoMatchReport{
		Matches: []AutoMatchItem{},
	}

	var books []Book
	if opts.BookID != "" {
		existing, err := store.GetMapping(ctx, opts.Provider, opts.SourceAccount, opts.BookID, opts.TargetAccount)
		if err == nil && existing != nil && existing.WorkID > 0 {
			report.Attempted = 1
			report.Skipped = 1
			report.Matches = append(report.Matches, AutoMatchItem{
				BookID:        opts.BookID,
				SourceTitle:   existing.SourceTitle,
				MatchedWorkID: existing.WorkID,
				Status:        "already_mapped",
				Reason:        "mapping already exists in database",
			})
			return report, nil
		}
		book, err := source.Book(ctx, opts.BookID)
		if err != nil {
			return report, fmt.Errorf("source book %s unavailable: %w", opts.BookID, err)
		}
		books = []Book{book}
	} else {
		pendingOpts := PendingOptions{
			SourceAccount:  opts.SourceAccount,
			TargetAccount:  opts.TargetAccount,
			Days:           opts.Days,
			Limit:          opts.Limit,
			Now:            opts.Now,
			FinishedOnly:   opts.FinishedOnly,
			IncludePrivate: false,
			After:          opts.After,
		}
		pendingReport, err := PendingWorklistOwned(ctx, source, dbPath, pendingOpts, owner)
		if err != nil {
			return report, fmt.Errorf("pending worklist failed: %w", err)
		}
		report.NextCursor = pendingReport.NextCursor
		for _, pb := range pendingReport.Books {
			if strings.HasPrefix(pb.ID, "CB_") {
				continue // skip imported books
			}
			books = append(books, pb.Book)
			if len(books) >= opts.Limit {
				break
			}
		}
	}

	for _, book := range books {
		report.Attempted++
		item := autoMatchLoadedBookWithStore(ctx, store, book, target, adv, opts)
		if item.Status == "confirmed" || item.Status == "dry_run" {
			report.Confirmed++
		} else {
			report.Skipped++
		}
		report.Matches = append(report.Matches, item)
	}

	return report, nil
}
