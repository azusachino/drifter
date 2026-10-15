package companion

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestPendingSeekLiveShelfUsesFixedWindowAndFreshScanRecoversMovedRow(t *testing.T) {
	now := time.Unix(2000000000, 0)
	cutoff := now.Add(-90 * 24 * time.Hour).Unix()
	r := &pendingReader{shelf: ShelfResult{Complete: true}}
	for i := 1; i <= 102; i++ {
		r.shelf.Books = append(r.shelf.Books, ShelfBook{ID: fmt.Sprintf("%03d", i), Secret: value(0), LastOpened: value(now.Unix() - 10)})
	}
	options := PendingOptions{SourceAccount: "source", TargetAccount: "target", Days: 90, Now: now}
	first, err := Pending(context.Background(), r, nil, options)
	if err != nil || len(first.Books) != 100 || first.NextCursor == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// This unattempted row moves ahead of the seek boundary but stays before as-of.
	r.shelf.Books[100].LastOpened = value(now.Unix() - 5)
	r.shelf.Books = append(r.shelf.Books,
		ShelfBook{ID: "103", Secret: value(0), LastOpened: value(cutoff - 1)},
		ShelfBook{ID: "104", Secret: value(0), LastOpened: value(cutoff)},
		ShelfBook{ID: "105", Secret: value(0), LastOpened: value(now.Unix() + 1)},
	)
	options.Now = now.Add(100 * time.Second)
	options.After = first.NextCursor
	next, err := Pending(context.Background(), r, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, book := range next.Books {
		ids = append(ids, book.ID)
	}
	if !reflect.DeepEqual(ids, []string{"102", "104"}) || next.NextCursor != "" {
		t.Fatalf("continuation=%+v", next)
	}
	// Advancing wall time cannot drift the inclusive original cutoff or as-of.
	if next.Complete || !reflect.DeepEqual(next.Issues, []PendingIssue{{BookID: "105", Code: "invalid_activity"}}) {
		t.Fatalf("issues=%+v complete=%v", next.Issues, next.Complete)
	}
	if len(r.calls) != 102 || !reflect.DeepEqual(r.calls[100:], []string{"102", "104"}) {
		t.Fatalf("duplicate/prior enrichment: %v", r.calls)
	}

	// A new window/scan can recover 101 (and 105); it is not a snapshot continuation.
	options.After = ""
	r.calls = nil
	fresh, err := Pending(context.Background(), r, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Books) < 2 || fresh.Books[0].ID != "105" || fresh.Books[1].ID != "101" {
		t.Fatalf("fresh=%+v", fresh)
	}
	options.After = fresh.NextCursor
	tail, err := Pending(context.Background(), r, nil, options)
	if err != nil || tail.NextCursor != "" || len(fresh.Books)+len(tail.Books) != 103 {
		t.Fatalf("fresh tail=%+v err=%v", tail, err)
	}
	for _, book := range append(fresh.Books, tail.Books...) {
		if book.ID == "103" || book.ID == "104" {
			t.Fatal("fresh scan retained old cutoff")
		}
	}
}
