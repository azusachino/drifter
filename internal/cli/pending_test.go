package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

func TestCLIPendingDefaultWindowAndAccountScope(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	store, err := companion.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []companion.Mapping{
		{Provider: "weread", SourceAccount: "source", TargetAccount: "target", BookID: "123", WorkID: 42},
		{Provider: "weread", SourceAccount: "other", TargetAccount: "target", BookID: "124", WorkID: 42},
		{Provider: "weread", SourceAccount: "source", TargetAccount: "other", BookID: "125", WorkID: 42},
	} {
		if err := store.Confirm(ctx, m, false); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	public, private := 0, 1
	recent, old := time.Now().Add(-89*24*time.Hour).Unix(), time.Now().Add(-91*24*time.Hour).Unix()
	reader := &fakeReader{shelf: &companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{
		{ID: "123", Secret: &public, LastOpened: &recent},
		{ID: "124", Secret: &public, LastOpened: &recent},
		{ID: "125", Secret: &public, LastOpened: &recent},
		{ID: "126", Secret: &public, LastOpened: &old},
		{ID: "127", Title: "Private never output", Secret: &private, LastOpened: &recent},
		{ID: "CB_synthetic", Title: "Imported synthetic", Secret: &public, LastOpened: &recent},
	}}}
	// No target factory: pending cannot require Hardcover credentials or calls.
	providers := clients{source: func() (source, error) { return reader, nil }}
	args := []string{"pending", "--account", "source", "--target-account", "target", "--db", path}
	var out, stderr bytes.Buffer
	if err := run(ctx, args, &out, &stderr, providers); err != nil {
		t.Fatal(err)
	}
	var report companion.PendingReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Books) != 2 || report.Books[0].ID != "124" || report.Books[1].ID != "125" || report.Books[0].Classification != "native" {
		t.Fatalf("wrong scope/defaults: %+v", report)
	}
	if !reflect.DeepEqual(reader.calls, []string{"shelf", "book:124", "book:125"}) || strings.Contains(out.String(), "Private never output") {
		t.Fatal("filtered books looked up or leaked", reader.calls)
	}
	out.Reset()
	if err := run(ctx, append(args, "--include-private", "--include-imported", "--days", "100"), &out, &stderr, providers); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Books) != 5 {
		t.Fatalf("opt-in flags not applied: %+v %v", report, err)
	}
	store, err = companion.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.List(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatal("pending changed mappings", rows, err)
	}
}

func TestCLIPendingAfterCursorContinuesAndPreservesIncompleteExit(t *testing.T) {
	public := 0
	now := time.Now().Truncate(time.Second)
	shelf := &companion.ShelfResult{Complete: true}
	for i := 1; i <= 102; i++ {
		opened := now.Add(-time.Duration(i) * time.Hour).Unix()
		shelf.Books = append(shelf.Books, companion.ShelfBook{ID: fmt.Sprint(i), Secret: &public, LastOpened: &opened})
	}
	reader := &fakeReader{shelf: shelf}
	providers := clients{source: func() (source, error) { return reader, nil }}
	base := []string{"pending", "--account", "source", "--target-account", "target", "--days", "90"}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), base, &out, &stderr, providers); err == nil {
		t.Fatal("first page should be incomplete")
	}
	var first companion.PendingReport
	if err := json.Unmarshal(out.Bytes(), &first); err != nil || first.NextCursor == "" || len(first.Books) != 100 {
		t.Fatalf("bad first page: %+v %v", first, err)
	}
	out.Reset()
	args := append(append([]string{}, base...), "--after", first.NextCursor)
	if err := run(context.Background(), args, &out, &stderr, providers); err != nil {
		t.Fatalf("last page should be complete: %v", err)
	}
	var second companion.PendingReport
	if err := json.Unmarshal(out.Bytes(), &second); err != nil || len(second.Books) != 2 || second.NextCursor != "" || second.Books[0].ID != "101" {
		t.Fatalf("bad continuation: %+v %v", second, err)
	}
	secret := "private-cursor-label"
	out.Reset()
	bad := append(append([]string{}, base...), "--after", secret)
	if err := run(context.Background(), bad, &out, &stderr, providers); err == nil || strings.Contains(out.String()+stderr.String(), secret) {
		t.Fatal("invalid cursor accepted or echoed")
	}
}

func TestCLIPendingIncompleteOutputAndInputValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	reader := &fakeReader{shelf: &companion.ShelfResult{Complete: false, Books: []companion.ShelfBook{}}}
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"pending", "--account", "source", "--target-account", "target", "--db", path}, &out, &stderr,
		clients{source: func() (source, error) { return reader, nil }})
	if err == nil || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"complete": false`) || !strings.Contains(out.String(), "incomplete_shelf") {
		t.Fatalf("incomplete output appears successful: %s %v", &out, err)
	}
	for _, args := range [][]string{
		{"pending"},
		{"pending", "--account", "source"},
		{"pending", "--account", "source", "--target-account", "target", "--days", "0"},
		{"pending", "--account", "source", "--target-account", "target", "--days", "36501"},
	} {
		if err := run(context.Background(), args, &out, &stderr, clients{}); err == nil {
			t.Fatal("invalid input contacted providers", args)
		}
	}
	if err := run(context.Background(), []string{"pending", "-h"}, &out, &stderr, clients{}); err != nil {
		t.Fatal(err)
	}
}
