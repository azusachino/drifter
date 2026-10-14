package companion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type operationSource struct{ Source }

func (operationSource) Book(_ context.Context, id string) (Book, error) {
	return Book{ID: id, Title: "Synthetic", TranslatorKnown: true}, nil
}
func (operationSource) Shelf(context.Context) (ShelfResult, error) {
	return ShelfResult{Complete: true, Books: []ShelfBook{{ID: "123", Secret: value(0), LastOpened: value(int64(2000000000))}}}, nil
}

type operationTarget struct {
	Target
	reject bool
}

func (t operationTarget) ValidateLink(context.Context, int64, int64) error {
	if t.reject {
		return fmt.Errorf("wrong edition")
	}
	return nil
}

func TestCoreConfirmationAndPendingWithoutCLI(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	reader := operationSource{}
	selection := testMapping()
	if err := ConfirmLink(ctx, reader, operationTarget{reject: true}, path, selection, false); err == nil {
		t.Fatal("invalid target accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("rejected target created state")
	}
	options := PendingOptions{SourceAccount: "source", TargetAccount: "target", Days: 90, Now: time.Unix(2000000000, 0)}
	report, err := PendingWorklist(ctx, reader, path, options)
	if err != nil || len(report.Books) != 1 {
		t.Fatalf("initial pending: %+v %v", report, err)
	}
	if err := ConfirmLink(ctx, reader, operationTarget{}, path, selection, false); err != nil {
		t.Fatal(err)
	}
	selection.WorkID = 99
	if err := ConfirmLink(ctx, reader, operationTarget{}, path, selection, false); err == nil {
		t.Fatal("replacement not refused")
	}
	if err := ConfirmLink(ctx, reader, operationTarget{}, path, selection, true); err != nil {
		t.Fatal(err)
	}
	report, err = PendingWorklist(ctx, reader, path, options)
	if err != nil || len(report.Books) != 0 {
		t.Fatalf("mapped pending: %+v %v", report, err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.List(ctx)
	if err != nil || len(rows) != 1 || rows[0].SourceTitle != "Synthetic" || rows[0].WorkID != 99 {
		t.Fatalf("core confirmation: %+v %v", rows, err)
	}
}
