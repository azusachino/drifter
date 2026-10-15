package companion

import (
	"context"
	"errors"
	"fmt"

	"github.com/azusachino/drifter/internal/matchingcatalog"
)

// CatalogExportOptions records the explicit account and public catalog metadata.
type CatalogExportOptions struct {
	SourceAccount       string
	TargetAccount       string
	CatalogID           string
	Publisher           string
	ProvenanceSource    string
	ProvenanceReference string
}

// ExportCatalog emits only confirmed mapping identities for an explicitly
// selected account pair. It opens state without creating or migrating it.
func ExportCatalog(ctx context.Context, dbPath string, options CatalogExportOptions) ([]byte, error) {
	if !ValidLabel(options.SourceAccount) || !ValidLabel(options.TargetAccount) {
		return nil, fmt.Errorf("valid source and target account labels required")
	}
	store, err := OpenStoreReadOnly(dbPath)
	if err != nil {
		return nil, fmt.Errorf("current mapping database unavailable for read-only catalog export")
	}
	defer store.Close()
	rows, err := store.listCatalogMappings(ctx, options.SourceAccount, options.TargetAccount)
	if errors.Is(err, errCatalogExportLimit) {
		return nil, fmt.Errorf("catalog export exceeds the v1 record limit")
	}
	if err != nil {
		return nil, fmt.Errorf("catalog export could not read selected mappings")
	}
	doc := matchingcatalog.Document{
		CatalogSchemaVersion: 1,
		CatalogID:            options.CatalogID,
		Publisher:            options.Publisher,
		Assertions:           make([]matchingcatalog.Assertion, 0, len(rows)),
		Hints:                []matchingcatalog.Hint{},
	}
	for _, row := range rows {
		if row.Provider != "weread" || !ValidBookID(row.BookID) || row.WorkID <= 0 || row.EditionID < 0 {
			return nil, fmt.Errorf("selected mapping cannot be represented by catalog v1")
		}
		var edition *int64
		if row.EditionID > 0 {
			value := row.EditionID
			edition = &value
		}
		doc.Assertions = append(doc.Assertions, matchingcatalog.Assertion{
			RecordID: "weread-" + row.BookID,
			Source:   matchingcatalog.SourceRef{Provider: "weread", ID: row.BookID},
			Target:   matchingcatalog.TargetRef{Provider: "hardcover", WorkID: row.WorkID, EditionID: edition},
			Provenance: []matchingcatalog.Provenance{{
				Source: options.ProvenanceSource, Reference: options.ProvenanceReference,
			}},
		})
	}
	encoded, err := matchingcatalog.MarshalDocument(doc)
	if err != nil {
		return nil, fmt.Errorf("catalog export metadata or selected mappings exceed v1 limits")
	}
	return encoded, nil
}
