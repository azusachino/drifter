package companion

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// slowSnapshotSource delays every provider call, like the paced WeRead client.
type slowSnapshotSource struct {
	snapshotSource
	delay time.Duration
}

func (s *slowSnapshotSource) Book(ctx context.Context, id string) (Book, error) {
	time.Sleep(s.delay)
	return s.snapshotSource.Book(ctx, id)
}

func TestReadingRunPinsOneInstantAndReportsReopenedBookAsChange(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	books := []ShelfBook{
		snapshotBookRow("100", "one", snapshotEpoch(t0.Unix()-10)),
		snapshotBookRow("200", "two", snapshotEpoch(t0.Unix()-20)),
		snapshotBookRow("300", "three", snapshotEpoch(t0.Unix()-30)),
		snapshotBookRow("400", "four", snapshotEpoch(t0.Unix()-40)),
	}
	source := &snapshotSource{
		shelf:    ShelfResult{Complete: true, Books: books},
		books:    map[string]Book{"100": {ID: "100"}, "200": {ID: "200"}, "300": {ID: "300"}, "400": {ID: "400"}},
		progress: map[string]Progress{},
	}
	db := snapshotDatabase(t)

	first, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{Limit: 2, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotIDs(first.Books); len(got) != 2 || got[0] != "100" || got[1] != "200" || !first.HasMore {
		t.Fatalf("first page=%v more=%v", got, first.HasMore)
	}

	// Book 400 is opened after page 1 was served: it moves to the front of the
	// shelf order and, under an offset cursor, would shift the page boundary.
	opened := t0.Unix() + 90
	source.shelf.Books[3] = snapshotBookRow("400", "four", snapshotEpoch(opened))
	later := func() time.Time { return t0.Add(2 * time.Minute) }

	second, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{Limit: 2, Cursor: first.Cursor, Now: later})
	if err != nil {
		t.Fatal(err)
	}
	if second.Watermark != first.Watermark || second.Watermark != t0.Format(time.RFC3339) {
		t.Fatalf("watermark moved inside a run: %q then %q", first.Watermark, second.Watermark)
	}
	if got := snapshotIDs(second.Books); len(got) != 1 || got[0] != "300" || second.HasMore || second.Cursor != "" {
		t.Fatalf("second page=%v more=%v (a repeat or skip means the boundary shifted)", got, second.HasMore)
	}

	pinned, err := time.Parse(time.RFC3339, first.Watermark)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ExportReadingChanges(ctx, db, source, ReadingChangesOptions{Since: pinned, Now: later})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotIDs(changes.Books); len(got) != 1 || got[0] != "400" {
		t.Fatalf("book opened mid-run is missing from changes at the pinned watermark: %v", got)
	}
}

func TestReadingChangesRunKeepsPinnedWatermarkAcrossPages(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	since := t0.Add(-time.Hour)
	source := &snapshotSource{
		shelf: ShelfResult{Complete: true, Books: []ShelfBook{
			snapshotBookRow("100", "one", snapshotEpoch(since.Unix()+30)),
			snapshotBookRow("200", "two", snapshotEpoch(since.Unix()+20)),
			snapshotBookRow("300", "three", snapshotEpoch(since.Unix()+10)),
		}},
		books:    map[string]Book{"100": {ID: "100"}, "200": {ID: "200"}, "300": {ID: "300"}},
		progress: map[string]Progress{},
	}
	db := snapshotDatabase(t)
	first, err := ExportReadingChanges(ctx, db, source, ReadingChangesOptions{Since: since, Limit: 2, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExportReadingChanges(ctx, db, source, ReadingChangesOptions{Since: since, Limit: 2, Cursor: first.Cursor, Now: func() time.Time { return t0.Add(time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	if first.Watermark != t0.Format(time.RFC3339) || second.Watermark != first.Watermark {
		t.Fatalf("watermarks %q then %q, want %q on both", first.Watermark, second.Watermark, t0.Format(time.RFC3339))
	}
}

func TestReadingPageStopsEnrichingWhenItsBudgetIsSpent(t *testing.T) {
	ctx := context.Background()
	const total = 100
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	shelf := make([]ShelfBook, 0, total)
	catalog := map[string]Book{}
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("%03d", i)
		shelf = append(shelf, snapshotBookRow(id, id, snapshotEpoch(t0.Unix()-int64(i))))
		catalog[id] = Book{ID: id}
	}
	source := &slowSnapshotSource{
		snapshotSource: snapshotSource{shelf: ShelfResult{Complete: true, Books: shelf}, books: catalog, progress: map[string]Progress{}},
		delay:          20 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	began := time.Now()
	page, err := ExportReadingSnapshot(ctx, snapshotDatabase(t), source, ReadingSnapshotOptions{Limit: 100, PageBudget: 100 * time.Millisecond, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Books) == 0 || len(page.Books) >= total || !page.HasMore || page.Cursor == "" {
		t.Fatalf("books=%d more=%v cursor=%q, want a short page with has_more", len(page.Books), page.HasMore, page.Cursor)
	}
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("page took %v against a 100ms budget", elapsed)
	}

	// An exhausted budget still serves one book, so the run always advances.
	next, err := ExportReadingSnapshot(ctx, snapshotDatabase(t), source, ReadingSnapshotOptions{Limit: 100, PageBudget: time.Nanosecond, Cursor: page.Cursor, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Books) != 1 || !next.HasMore || next.Books[0].SourceBookID != fmt.Sprintf("%03d", len(page.Books)) {
		t.Fatalf("zero-budget page=%v more=%v", snapshotIDs(next.Books), next.HasMore)
	}
}
