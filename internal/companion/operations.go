package companion

import (
	"context"
	"fmt"
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

func Match(ctx context.Context, source Source, target Target, id, query string) (MatchReport, error) {
	book, err := source.Book(ctx, id)
	if err != nil {
		return MatchReport{}, err
	}
	return target.Match(ctx, book, query)
}

// ConfirmLink is the shared explicit-confirmation path, not an automatic match.
// Validate providers before opening state so a rejected selection creates no DB.
func ConfirmLink(ctx context.Context, source Source, target Target, path string, selection Mapping, replace bool) error {
	return confirmLink(ctx, source, target, path, selection, replace, nil)
}

// ConfirmLinkOwned performs the same validation while using the daemon's
// existing ownership capability instead of acquiring a competing writer lock.
func ConfirmLinkOwned(ctx context.Context, source Source, target Target, path string, selection Mapping, replace bool, owner *DatabaseOwnership) error {
	if !owner.owns(path) {
		return fmt.Errorf("database ownership capability does not match this path")
	}
	return confirmLink(ctx, source, target, path, selection, replace, owner)
}

func confirmLink(ctx context.Context, source Source, target Target, path string, selection Mapping, replace bool, owner *DatabaseOwnership) error {
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
	return Pending(ctx, source, mappings, options)
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
	return Pending(ctx, source, mappings, options)
}
