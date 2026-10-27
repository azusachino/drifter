package companion

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStateExportAndImportRoundTrip(t *testing.T) {
	dbPath1 := filepath.Join(t.TempDir(), "source.sqlite")
	store1, err := OpenStore(dbPath1)
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()

	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	// Seed mapping, fingerprint, and baseline
	m := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Export Book",
		SourceISBN:    "9780123456789",
	}
	if err := store1.Confirm(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store1.VerifyOrBindAccountFingerprint(ctx, "weread", "mine", "weread:10001", now); err != nil {
		t.Fatal(err)
	}
	b := StatusBaseline{
		Provider:          "weread",
		SourceAccount:     "mine",
		BookID:            "123",
		TargetAccount:     "default",
		WorkID:            500,
		EditionID:         600,
		SourceStatusID:    HardcoverStatusCurrentlyReading,
		TargetStatusID:    HardcoverStatusCurrentlyReading,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:10001",
		TargetFingerprint: "hardcover:20002",
		LastSyncedAt:      now,
	}
	if err := store1.SaveStatusBaseline(ctx, b); err != nil {
		t.Fatal(err)
	}

	// 1. Export
	exported, err := ExportState(ctx, store1)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	if exported.FormatVersion != StateExportFormatVersion || len(exported.Mappings) != 1 || len(exported.AccountFingerprints) != 1 || len(exported.StatusBaselines) != 1 {
		t.Fatalf("unexpected export data: %+v", exported)
	}

	// 2. Import into empty store
	dbPath2 := filepath.Join(t.TempDir(), "target.sqlite")
	store2, err := OpenStore(dbPath2)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	report, err := ImportState(ctx, store2, exported, true)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if report.MappingsImported != 1 || report.FingerprintsBound != 1 || report.BaselinesImported != 1 {
		t.Fatalf("unexpected import report: %+v", report)
	}

	// Verify imported data in store2
	mGot, err := store2.GetMapping(ctx, "weread", "mine", "123", "default")
	if err != nil || mGot == nil || mGot.WorkID != 500 {
		t.Fatalf("mapping not imported correctly: %+v, err %v", mGot, err)
	}
	fpGot, err := store2.GetAccountFingerprint(ctx, "weread", "mine")
	if err != nil || fpGot == nil || fpGot.Fingerprint != "weread:10001" {
		t.Fatalf("fingerprint not imported: %+v, err %v", fpGot, err)
	}
	bGot, err := store2.GetStatusBaseline(ctx, "weread", "mine", "123", "default")
	if err != nil || bGot == nil || bGot.TargetStatusID != HardcoverStatusCurrentlyReading {
		t.Fatalf("baseline not imported: %+v, err %v", bGot, err)
	}

	// 3. Re-importing exact same data skips without error
	report2, err := ImportState(ctx, store2, exported, true)
	if err != nil {
		t.Fatalf("re-import failed: %v", err)
	}
	if report2.MappingsImported != 0 || report2.MappingsSkipped != 1 {
		t.Fatalf("expected 1 skipped mapping, got %+v", report2)
	}

	// 4. Conflicting mapping with rejectOnConflict=true fails closed
	badExport := exported
	badExport.Mappings = []Mapping{
		{
			Provider:      "weread",
			SourceAccount: "mine",
			BookID:        "123",
			TargetAccount: "default",
			WorkID:        999, // Conflicting work ID!
			SourceTitle:   "Conflict",
		},
	}
	_, err = ImportState(ctx, store2, badExport, true)
	if err == nil {
		t.Fatal("expected error on conflicting mapping import")
	}

	// 5. Conflicting mapping with rejectOnConflict=false records conflict and skips
	report3, err := ImportState(ctx, store2, badExport, false)
	if err != nil {
		t.Fatalf("unexpected error when rejectOnConflict=false: %v", err)
	}
	if len(report3.Conflicts) != 1 || report3.MappingsSkipped != 1 {
		t.Fatalf("expected conflict recorded and skipped, got %+v", report3)
	}
}
