package companion

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/catalog"
)

func seedCatalogExportRows(t *testing.T, path string, selected, other int) {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	insert := `INSERT INTO mappings VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	for i := 1; i <= selected; i++ {
		if _, err := tx.Exec(insert, "weread", "selected-source", fmt.Sprint(i), "selected-target", i, 0, "private-title", "private-isbn"); err != nil {
			tx.Rollback()
			store.Close()
			t.Fatal(err)
		}
	}
	for i := 1; i <= other; i++ {
		if _, err := tx.Exec(insert, "weread", "other-source", fmt.Sprint(i), "other-target", i, 0, "private-title", "private-isbn"); err != nil {
			tx.Rollback()
			store.Close()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogExportBoundsSelectedRowsAndPreservesAtLimitAccountScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	seedCatalogExportRows(t, path, catalog.MaxRecordsPerCatalog, 1)

	store, err := OpenStoreReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.listCatalogMappings(context.Background(), "selected-source", "selected-target")
	if err != nil || len(rows) != catalog.MaxRecordsPerCatalog {
		t.Fatalf("at-limit selected query failed or included another account: rows=%d err=%v", len(rows), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	writer, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.Exec(`INSERT INTO mappings VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "weread", "selected-source", "5001", "selected-target", 5001, 0, "private-title", "private-isbn"); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStoreReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = store.listCatalogMappings(context.Background(), "selected-source", "selected-target")
	if !errors.Is(err, errCatalogExportLimit) || len(rows) != 0 {
		t.Fatalf("over-limit query did not reject at the bound: rows=%d err=%v", len(rows), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	output, err := ExportCatalog(context.Background(), path, CatalogExportOptions{
		SourceAccount: "selected-source", TargetAccount: "selected-target",
		CatalogID: "bounded-export", Publisher: "test publisher",
		ProvenanceSource: "public source", ProvenanceReference: "public reference",
	})
	if err == nil || len(output) != 0 || !strings.Contains(err.Error(), "record limit") {
		t.Fatalf("over-limit export did not fail explicitly without output: bytes=%d err=%v", len(output), err)
	}
}
