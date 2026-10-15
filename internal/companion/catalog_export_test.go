package companion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/catalog"
)

func TestExportCatalogSelectsOnlyRequestedAccountsAndSafeFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	selected := testMapping()
	selected.SourceAccount = "private-source-alias"
	selected.TargetAccount = "private-target-alias"
	selected.SourceTitle = "private stored title"
	selected.SourceISBN = "private stored isbn"
	selected.EditionID = 77
	if err := store.Confirm(context.Background(), selected, false); err != nil {
		t.Fatal(err)
	}
	workOnly := selected
	workOnly.BookID = "124"
	workOnly.WorkID = 88
	workOnly.EditionID = 0
	if err := store.Confirm(context.Background(), workOnly, false); err != nil {
		t.Fatal(err)
	}
	other := selected
	other.SourceAccount = "other-source"
	other.TargetAccount = "other-target"
	other.BookID = "456"
	other.WorkID = 99
	if err := store.Confirm(context.Background(), other, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	encoded, err := ExportCatalog(context.Background(), path, CatalogExportOptions{
		SourceAccount: selected.SourceAccount, TargetAccount: selected.TargetAccount,
		CatalogID: "public-crosswalk", Publisher: "Example publisher",
		ProvenanceSource: "public source", ProvenanceReference: "public reference",
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc catalog.Document
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.CatalogSchemaVersion != 1 || doc.CatalogID != "public-crosswalk" || doc.Publisher != "Example publisher" || len(doc.Assertions) != 2 || len(doc.Hints) != 0 {
		t.Fatalf("unexpected export document: %+v", doc)
	}
	assertion := doc.Assertions[0]
	if assertion.Source.ID != selected.BookID || assertion.Target.WorkID != selected.WorkID || assertion.Target.EditionID == nil || *assertion.Target.EditionID != selected.EditionID {
		t.Fatalf("exported wrong mapping: %+v", assertion)
	}
	if assertion.Provenance[0] != (catalog.Provenance{Source: "public source", Reference: "public reference"}) {
		t.Fatalf("exported wrong provenance: %+v", assertion.Provenance)
	}
	if doc.Assertions[1].Source.ID != workOnly.BookID || doc.Assertions[1].Target.EditionID != nil {
		t.Fatalf("work-only mapping gained an edition: %+v", doc.Assertions[1])
	}
	for _, secret := range []string{selected.SourceAccount, selected.TargetAccount, selected.SourceTitle, selected.SourceISBN, other.BookID} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("export disclosed unselected or private value %q", secret)
		}
	}
}

func TestExportCatalogUnderDaemonOwnershipIsReadOnlyAndEmptyStateIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	owner, err := AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	store, err := OpenOwnedStore(path, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ExportCatalog(context.Background(), path, CatalogExportOptions{
		SourceAccount: "source", TargetAccount: "target", CatalogID: "empty", Publisher: "publisher",
		ProvenanceSource: "source", ProvenanceReference: "reference",
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc catalog.Document
	if err := json.Unmarshal(encoded, &doc); err != nil || len(doc.Assertions) != 0 || len(doc.Hints) != 0 {
		t.Fatalf("existing empty state did not export an empty catalog: %+v %v", doc, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() {
		t.Fatalf("read-only export changed database file: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestExportCatalogRefusesLegacySchemaWithoutMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db := fixtureDB(t, path)
	if _, err := db.Exec(legacyMappingSchema); err != nil {
		t.Fatal(err)
	}
	if schemaVersion(t, db) != 0 {
		t.Fatal("legacy fixture must be unversioned")
	}
	if encoded, err := ExportCatalog(context.Background(), path, CatalogExportOptions{
		SourceAccount: "source", TargetAccount: "target", CatalogID: "catalog", Publisher: "publisher",
		ProvenanceSource: "source", ProvenanceReference: "reference",
	}); err == nil || len(encoded) != 0 {
		t.Fatalf("legacy schema unexpectedly exported: %d %v", len(encoded), err)
	}
	if schemaVersion(t, db) != 0 {
		t.Fatal("catalog export migrated an unsupported schema")
	}
}

func TestExportCatalogMissingDatabaseDoesNotCreateState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "mappings.sqlite")
	if encoded, err := ExportCatalog(context.Background(), path, CatalogExportOptions{
		SourceAccount: "source", TargetAccount: "target", CatalogID: "empty", Publisher: "publisher",
		ProvenanceSource: "source", ProvenanceReference: "reference",
	}); err == nil || len(encoded) != 0 {
		t.Fatalf("absent database unexpectedly exported: bytes=%d err=%v", len(encoded), err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("export created state directory: %v", err)
	}
}
