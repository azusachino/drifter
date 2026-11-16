package companion

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
)

// AutoMatchOptions controls automated matching and confirmation behavior.
type AutoMatchOptions struct {
	Provider      string
	SourceAccount string
	TargetAccount string
	Days          int
	MinConfidence float64
	Limit         int
	BookID        string
	DryRun        bool
	Now           time.Time
}

// AutoMatchReport summarizes the outcome of an automated matching batch.
type AutoMatchReport struct {
	Attempted int             `json:"attempted"`
	Confirmed int             `json:"confirmed"`
	Skipped   int             `json:"skipped"`
	Matches   []AutoMatchItem `json:"matches"`
}

// AutoMatchItem represents the result for a single inspected source book.
type AutoMatchItem struct {
	BookID        string  `json:"book_id"`
	SourceTitle   string  `json:"source_title"`
	SourceAuthor  string  `json:"source_author"`
	MatchedWorkID int64   `json:"matched_work_id,omitempty"`
	MatchedTitle  string  `json:"matched_title,omitempty"`
	Confidence    float64 `json:"confidence"`
	Status        string  `json:"status"` // "confirmed", "dry_run", "ambiguous", "low_confidence", "abstained", "no_candidate", "already_mapped", "error"
	Reason        string  `json:"reason,omitempty"`
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
	if opts.Provider == "" {
		opts.Provider = "weread"
	}
	if opts.SourceAccount == "" {
		opts.SourceAccount = "default"
	}
	if opts.TargetAccount == "" {
		opts.TargetAccount = "default"
	}
	if opts.MinConfidence <= 0 {
		opts.MinConfidence = 0.80
	}
	if opts.Days <= 0 {
		opts.Days = 90
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}

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
			Now:            opts.Now,
			FinishedOnly:   false,
			IncludePrivate: false,
		}
		pendingReport, err := PendingWorklistOwned(ctx, source, dbPath, pendingOpts, owner)
		if err != nil {
			return report, fmt.Errorf("pending worklist failed: %w", err)
		}
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
		item := AutoMatchItem{
			BookID:       book.ID,
			SourceTitle:  book.Title,
			SourceAuthor: book.Author,
		}

		matchReport, err := MatchWithOptions(ctx, source, target, book.ID, "", MatchOptions{
			Advisor: adv,
		})
		if err != nil {
			item.Status = "error"
			item.Reason = err.Error()
			report.Skipped++
			report.Matches = append(report.Matches, item)
			continue
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
				report.Skipped++
				report.Matches = append(report.Matches, item)
				continue
			}
			if len(matchReport.Candidates) == 0 {
				item.Confidence = confidence
				item.Status = "no_candidate"
				item.Reason = "no candidate works found on target"
				report.Skipped++
				report.Matches = append(report.Matches, item)
				continue
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
					report.Skipped++
					report.Matches = append(report.Matches, item)
					continue
				}
			}
		} else {
			item.Status = "abstained"
			if matchReport.AdvisorResult != nil {
				item.Reason = matchReport.AdvisorResult.Rationale
			} else {
				item.Reason = "no exact match and no advisor resolution"
			}
			report.Skipped++
			report.Matches = append(report.Matches, item)
			continue
		}

		item.MatchedWorkID = chosenWorkID
		item.MatchedTitle = chosenTitle
		item.Confidence = confidence

		if opts.DryRun {
			item.Status = "dry_run"
			report.Confirmed++
			report.Matches = append(report.Matches, item)
			continue
		}

		selection := Mapping{
			Provider:      opts.Provider,
			SourceAccount: opts.SourceAccount,
			BookID:        book.ID,
			TargetAccount: opts.TargetAccount,
			WorkID:        chosenWorkID,
			EditionID:     chosenEditionID,
		}
		err = ConfirmLinkOwned(ctx, source, target, dbPath, selection, false, owner)
		if err != nil {
			item.Status = "error"
			item.Reason = err.Error()
			report.Skipped++
		} else {
			item.Status = "confirmed"
			report.Confirmed++
		}
		report.Matches = append(report.Matches, item)
	}

	return report, nil
}
