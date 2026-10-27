package companion

import (
	"context"
	"fmt"
	"strings"

	"github.com/azusachino/drifter/internal/catalog"
)

const (
	DefaultSourceAccount  = "default"
	DefaultTargetAccount  = "default"
	DefaultSourceProvider = "weread"
	DefaultTargetProvider = "hardcover"
)

// Source and Target are the provider dependencies of core operations. Adapters
// (CLI or server) supply authenticated clients; core never reads credentials.
type Source interface {
	Shelf(context.Context) (ShelfResult, error)
	Search(context.Context, string) (SearchResult, error)
	Book(context.Context, string) (Book, error)
	Progress(context.Context, string) (Progress, error)
}

type Target interface {
	Match(context.Context, Book, string) (MatchReport, error)
	ValidateLink(context.Context, int64, int64) error
}

// SourceProvider represents a complete intake source provider.
type SourceProvider interface {
	Source
	ResolveAccountFingerprint(ctx context.Context, declaredVID string) (string, error)
}

// TargetProvider represents a complete catalog destination provider.
type TargetProvider interface {
	Target
	GetUserBookStatus(ctx context.Context, workID int64) (*UserBookStatus, error)
	ApplyStatusMutation(ctx context.Context, req StatusMutationRequest) (StatusMutationOutcome, error)
	ResolveAccountFingerprint(ctx context.Context) (string, error)
}

var (
	_ SourceProvider = (*WeRead)(nil)
	_ TargetProvider = (*Hardcover)(nil)
)

type Inspection struct {
	Book     Book      `json:"book"`
	Progress *Progress `json:"progress,omitempty"`
}

func Inspect(ctx context.Context, source Source, id string, progress bool) (Inspection, error) {
	book, err := source.Book(ctx, id)
	if err != nil {
		return Inspection{}, err
	}
	result := Inspection{Book: book}
	if progress {
		p, err := source.Progress(ctx, id)
		if err != nil {
			return Inspection{}, err
		}
		result.Progress = &p
	}
	return result, nil
}

type MatchOptions struct {
	CatalogLoader *catalog.Loader
	// ExpectedAuthor optionally moves equal contributor names first without
	// changing candidate membership or confirming identity. Empty preserves order.
	ExpectedAuthor string
}

func Match(ctx context.Context, source Source, target Target, id, query string) (MatchReport, error) {
	return MatchWithOptions(ctx, source, target, id, query, MatchOptions{})
}

func MatchWithOptions(ctx context.Context, source Source, target Target, id, query string, options MatchOptions) (MatchReport, error) {
	book, err := source.Book(ctx, id)
	if err != nil {
		return MatchReport{}, err
	}
	report, err := target.Match(ctx, book, query)
	if err != nil {
		return MatchReport{}, err
	}
	if author := strings.Join(strings.Fields(options.ExpectedAuthor), " "); author != "" && len(report.Candidates) > 1 {
		matched := make([]Candidate, 0, len(report.Candidates))
		var unmatched []Candidate
		for _, candidate := range report.Candidates {
			if candidateHasAuthor(candidate, author) {
				matched = append(matched, candidate)
			} else {
				unmatched = append(unmatched, candidate)
			}
		}
		report.Candidates = append(matched, unmatched...)
	}
	if options.CatalogLoader == nil {
		return report, nil
	}
	snapshot := options.CatalogLoader.Load(ctx)
	if !snapshot.Configured {
		return report, nil
	}
	complete := snapshot.Complete
	report.CatalogsComplete = &complete
	report.CatalogIssues = append([]catalog.Issue(nil), snapshot.Issues...)
	for _, catalog := range snapshot.Catalogs {
		for _, assertion := range catalog.Assertions {
			if assertion.Source.Provider != "weread" || assertion.Source.ID != book.ID {
				continue
			}
			report.CatalogAssertions = append(report.CatalogAssertions, CatalogAssertion{
				SubscriptionID: catalog.SubscriptionID,
				CatalogID:      catalog.CatalogID,
				Publisher:      catalog.Publisher,
				RecordID:       assertion.RecordID,
				Source:         assertion.Source,
				Target:         assertion.Target,
				Provenance:     assertion.Provenance,
			})
		}
		for _, hint := range catalog.Hints {
			if hint.Source.Provider != "weread" || hint.Source.ID != book.ID {
				continue
			}
			report.CatalogHints = append(report.CatalogHints, CatalogHint{
				SubscriptionID: catalog.SubscriptionID,
				CatalogID:      catalog.CatalogID,
				Publisher:      catalog.Publisher,
				RecordID:       hint.RecordID,
				Source:         hint.Source,
				Title:          hint.Title,
				OriginalAuthor: hint.OriginalAuthor,
				ISBN:           hint.ISBN,
				Confidence:     hint.Confidence,
				Provenance:     hint.Provenance,
			})
		}
	}
	if len(report.CatalogAssertions) > 1 {
		targets := make(map[string]struct{}, len(report.CatalogAssertions))
		for _, assertion := range report.CatalogAssertions {
			key := fmt.Sprintf("%d:%d", assertion.Target.WorkID, editionValue(assertion.Target.EditionID))
			targets[key] = struct{}{}
		}
		if len(targets) > 1 {
			report.CatalogConflicts = []CatalogConflict{{
				Source: catalog.SourceRef{Provider: "weread", ID: book.ID},
				Claims: append([]CatalogAssertion(nil), report.CatalogAssertions...),
			}}
		}
	}
	report.RequiresConfirmation = true
	return report, nil
}

func candidateHasAuthor(candidate Candidate, expected string) bool {
	for _, contribution := range candidate.Contributions {
		name := strings.Join(strings.Fields(contribution.Author.Name), " ")
		if name != "" && strings.EqualFold(name, expected) {
			return true
		}
	}
	return false
}

func editionValue(editionID *int64) int64 {
	if editionID == nil {
		return 0
	}
	return *editionID
}

// ConfirmLink is the shared explicit-confirmation path, not an automatic match.
// Validate providers before opening state so a rejected selection creates no DB.
func ConfirmLink(ctx context.Context, source Source, target Target, path string, selection Mapping, replace bool) error {
	return confirmLink(ctx, source, target, path, selection, replace, nil, nil)
}

// ConfirmLinkExpected validates the complete selection, then replaces only the previously observed target.
func ConfirmLinkExpected(ctx context.Context, source Source, target Target, path string, selection Mapping, expected MappingTarget) error {
	return confirmLink(ctx, source, target, path, selection, true, nil, &expected)
}

// ConfirmLinkOwned performs the same validation while using the daemon's
// existing ownership capability instead of acquiring a competing writer lock.
func ConfirmLinkOwned(ctx context.Context, source Source, target Target, path string, selection Mapping, replace bool, owner *DatabaseOwnership) error {
	if !owner.owns(path) {
		return fmt.Errorf("database ownership capability does not match this path")
	}
	return confirmLink(ctx, source, target, path, selection, replace, owner, nil)
}

// ConfirmLinkExpectedOwned is the guarded confirmation path for the daemon owner.
func ConfirmLinkExpectedOwned(ctx context.Context, source Source, target Target, path string, selection Mapping, expected MappingTarget, owner *DatabaseOwnership) error {
	if !owner.owns(path) {
		return fmt.Errorf("database ownership capability does not match this path")
	}
	return confirmLink(ctx, source, target, path, selection, true, owner, &expected)
}

func confirmLink(ctx context.Context, source Source, target Target, path string, selection Mapping, replace bool, owner *DatabaseOwnership, expected *MappingTarget) error {
	if selection.Provider != "weread" || !selection.valid() {
		return ErrInvalidMapping
	}
	if owner == nil {
		if err := CheckDatabaseWriteAvailable(path); err != nil {
			return err
		}
	}
	book, err := source.Book(ctx, selection.BookID)
	if err != nil {
		return err
	}
	if book.ID != selection.BookID {
		return fmt.Errorf("source identity differs from selection")
	}
	if err := target.ValidateLink(ctx, selection.WorkID, selection.EditionID); err != nil {
		return err
	}
	var store *Store
	if owner == nil {
		store, err = OpenStore(path)
	} else {
		store, err = OpenOwnedStore(path, owner)
	}
	if err != nil {
		return err
	}
	defer store.Close()
	selection.SourceTitle, selection.SourceISBN = book.Title, book.ISBN
	if expected != nil {
		return store.ReplaceExpected(ctx, selection, *expected)
	}
	return store.Confirm(ctx, selection, replace)
}

// PendingWorklist owns the mapping read as well as worklist selection. Callers
// choose state location and time; they need no knowledge of filtering internals.
func PendingWorklist(ctx context.Context, source Source, path string, options PendingOptions) (PendingReport, error) {
	store, err := OpenStoreForRead(path)
	if err != nil {
		return PendingReport{}, err
	}
	defer store.Close()
	mappings, err := store.List(ctx)
	if err != nil {
		return PendingReport{}, err
	}
	decisions, err := store.ListDecisions(ctx, "weread", options.SourceAccount, options.TargetAccount)
	if err != nil {
		return PendingReport{}, err
	}
	return PendingWithDecisions(ctx, source, mappings, decisions, options)
}

// PendingWorklistOwned reads mappings through a daemon-owned store while using
// the same core selection and provider orchestration as the CLI operation.
func PendingWorklistOwned(ctx context.Context, source Source, path string, options PendingOptions, owner *DatabaseOwnership) (PendingReport, error) {
	store, err := OpenOwnedStore(path, owner)
	if err != nil {
		return PendingReport{}, err
	}
	defer store.Close()
	mappings, err := store.List(ctx)
	if err != nil {
		return PendingReport{}, err
	}
	decisions, err := store.ListDecisions(ctx, "weread", options.SourceAccount, options.TargetAccount)
	if err != nil {
		return PendingReport{}, err
	}
	return PendingWithDecisions(ctx, source, mappings, decisions, options)
}
