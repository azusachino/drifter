package companion

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// snapshotSource is a synthetic provider with per-book metadata and progress so
// export behaviour can be asserted without any network provider.
type snapshotSource struct {
	shelf       ShelfResult
	books       map[string]Book
	bookErrors  map[string]error
	progress    map[string]Progress
	progressErr map[string]error
	bookCalls   []string
	readCalls   []string
	shelfCalls  int
}

func (s *snapshotSource) Shelf(context.Context) (ShelfResult, error) {
	s.shelfCalls++
	return s.shelf, nil
}

func (*snapshotSource) Search(context.Context, string) (SearchResult, error) {
	panic("unused")
}

func (s *snapshotSource) Book(_ context.Context, id string) (Book, error) {
	s.bookCalls = append(s.bookCalls, id)
	if err := s.bookErrors[id]; err != nil {
		return Book{}, err
	}
	book, ok := s.books[id]
	if !ok {
		return Book{}, fmt.Errorf("synthetic book not found")
	}
	return book, nil
}

func (s *snapshotSource) Progress(_ context.Context, id string) (Progress, error) {
	s.readCalls = append(s.readCalls, id)
	if err := s.progressErr[id]; err != nil {
		return Progress{}, err
	}
	return s.progress[id], nil
}

// snapshotTempDir returns a private state directory regardless of the caller's
// umask, because the store rejects group- or world-writable parents.
func snapshotTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func snapshotDatabase(t *testing.T, mappings ...Mapping) string {
	t.Helper()
	path := filepath.Join(snapshotTempDir(t), "snapshot.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mapping := range mappings {
		if err := store.Confirm(context.Background(), mapping, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func snapshotBookRow(id, title string, lastOpened *int64) ShelfBook {
	secret := 0
	return ShelfBook{ID: id, Title: title, Author: "作者", LastOpened: lastOpened, Secret: &secret}
}

func snapshotEpoch(value int64) *int64 { return &value }

func TestReadingSnapshotPaginatesWithCursorAndEnrichesMetadata(t *testing.T) {
	ctx := context.Background()
	source := &snapshotSource{
		shelf: ShelfResult{Complete: true, Books: []ShelfBook{
			snapshotBookRow("200", "shelf-200", snapshotEpoch(200)),
			snapshotBookRow("100", "shelf-100", snapshotEpoch(300)),
			{ID: "CB_custom_1", Title: "shelf-custom", Author: "作者", Cover: "https://res.weread.qq.com/custom.jpg", LastOpened: snapshotEpoch(400)},
			snapshotBookRow("300", "shelf-300", nil),
		}},
		books: map[string]Book{
			"100": {ID: "100", Title: "克拉拉与太阳", Author: "石黑一雄", Translator: "赵松", Publisher: "上海译文出版社", ISBN: "9787532786831", ReaderURL: "https://weread.qq.com/web/bookDetail/100", CoverURL: "https://cdn.example.com/cover-100.jpg"},
			"200": {ID: "200", Title: "Kubernetes权威指南", Author: "龚正"},
			"300": {ID: "300", Title: "深入理解Kafka", Author: "朱忠华"},
		},
		progress: map[string]Progress{
			"100": toSnapshotProgress(t, ProgressFacts{Percent: 100, Started: 1, FinishedAt: 1700000000, ReadingTimeSeconds: 18240, LastReadAt: 1700000000}),
			"200": toSnapshotProgress(t, ProgressFacts{Percent: 45, Started: 1, ReadingTimeSeconds: 600, LastReadAt: 1700000100}),
		},
	}
	now := func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	db := snapshotDatabase(t)

	first, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{Limit: 2, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != ReadingSnapshotVersion || !first.GeneratedAt.Equal(now()) || first.Watermark != now().Format(time.RFC3339) {
		t.Fatalf("unexpected snapshot envelope: %+v", first)
	}
	if first.SourceIdentity != "" {
		t.Fatalf("an unbound account produced an invented identity: %q", first.SourceIdentity)
	}
	if len(first.Books) != 2 || first.Books[0].SourceBookID != "CB_custom_1" || first.Books[1].SourceBookID != "100" {
		t.Fatalf("page order is not recency-then-identity: %+v", snapshotIDs(first.Books))
	}
	if !first.HasMore || first.Cursor == "" {
		t.Fatalf("first page did not report more data: %+v", first)
	}
	if custom := first.Books[0]; custom.CoverURL != "https://res.weread.qq.com/custom.jpg" {
		t.Fatalf("shelf cover url was not exported: %+v", custom)
	}
	enriched := first.Books[1]
	if enriched.SourceProvider != "weread" || enriched.Title != "克拉拉与太阳" || enriched.Author != "石黑一雄" ||
		enriched.Translator != "赵松" || enriched.Publisher != "上海译文出版社" || enriched.ISBN != "9787532786831" ||
		enriched.CoverURL != "https://cdn.example.com/cover-100.jpg" ||
		!strings.HasPrefix(enriched.ReaderURL, "https://weread.qq.com/") {
		t.Fatalf("metadata was not enriched from catalog facts: %+v", enriched)
	}
	if enriched.Status != "completed" || enriched.ProgressPercent != 100 || enriched.ReadingTimeSeconds != 18240 || enriched.FinishedAt == nil || enriched.LastReadAt == nil {
		t.Fatalf("completed progress facts were not exported: %+v", enriched)
	}
	if !enriched.FinishedAt.Equal(time.Unix(1700000000, 0)) || !enriched.LastReadAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("activity instants are not UTC epochs: %v %v", enriched.FinishedAt, enriched.LastReadAt)
	}

	second, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{Limit: 2, Cursor: first.Cursor, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if second.HasMore || second.Cursor != "" {
		t.Fatalf("second page reported more data: %+v", second)
	}
	if got := snapshotIDs(second.Books); len(got) != 2 || got[0] != "200" || got[1] != "300" {
		t.Fatalf("second page order=%v", got)
	}
	if reading := second.Books[0]; reading.Status != "reading" || reading.ReadingTimeSeconds != 600 || reading.LastReadAt == nil || reading.FinishedAt != nil {
		t.Fatalf("reading progress facts were not exported: %+v", reading)
	}
	if planned := second.Books[1]; planned.Status != "planned" || planned.ProgressPercent != 0 || planned.LastReadAt != nil || planned.FinishedAt != nil {
		t.Fatalf("absent progress facts were not exported honestly: %+v", planned)
	}

	// A cursor past the shelf yields an empty final page rather than an error.
	beyond, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{Limit: 2, Cursor: encodeSnapshotCursor(10), Now: now})
	if err != nil || len(beyond.Books) != 0 || beyond.HasMore || beyond.Cursor != "" {
		t.Fatalf("cursor beyond the shelf: %+v %v", beyond, err)
	}
}

func TestReadingSnapshotKeepsCustomUploadsWithoutCatalogLookup(t *testing.T) {
	ctx := context.Background()
	custom := snapshotBookRow("CB_custom_1", "自定义上传", snapshotEpoch(400))
	custom.Author = ""
	source := &snapshotSource{
		shelf:    ShelfResult{Complete: true, Books: []ShelfBook{custom}},
		books:    map[string]Book{},
		progress: map[string]Progress{},
	}
	snapshot, err := ExportReadingSnapshot(ctx, snapshotDatabase(t), source, ReadingSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Books) != 1 {
		t.Fatalf("custom upload was dropped: %+v", snapshot.Books)
	}
	book := snapshot.Books[0]
	if !book.IsCustom || book.SourceBookID != "CB_custom_1" || book.Title != "自定义上传" || book.Status != "planned" {
		t.Fatalf("custom upload was not retained honestly: %+v", book)
	}
	if len(source.bookCalls) != 0 {
		t.Fatalf("custom upload triggered a catalog lookup: %v", source.bookCalls)
	}
}

func TestReadingSnapshotAttachesConfirmedMappingOnlyForSelectedAccounts(t *testing.T) {
	ctx := context.Background()
	source := &snapshotSource{
		shelf:    ShelfResult{Complete: true, Books: []ShelfBook{snapshotBookRow("123", "shelf-123", snapshotEpoch(500))}},
		books:    map[string]Book{"123": {ID: "123", Title: "克拉拉与太阳"}},
		progress: map[string]Progress{"123": toSnapshotProgress(t, ProgressFacts{Percent: 30, Started: 1})},
	}
	db := snapshotDatabase(t,
		Mapping{Provider: "weread", SourceAccount: "mine", BookID: "123", TargetAccount: "default", WorkID: 142089, EditionID: 312044, SourceTitle: "克拉拉与太阳"},
		Mapping{Provider: "weread", SourceAccount: "other", BookID: "123", TargetAccount: "default", WorkID: 999, SourceTitle: "unrelated"},
	)
	snapshot, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{SourceAccount: "mine", TargetAccount: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SourceIdentity != "" || len(snapshot.Books) != 1 || snapshot.Books[0].MatchedTarget == nil {
		t.Fatalf("confirmed mapping was not attached: %+v", snapshot)
	}
	target := snapshot.Books[0].MatchedTarget
	if target.TargetProvider != "hardcover" || target.WorkID != 142089 || target.EditionID != 312044 {
		t.Fatalf("matched target=%+v", target)
	}

	unmapped, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{SourceAccount: "unmapped-account"})
	if err != nil {
		t.Fatal(err)
	}
	if unmapped.Books[0].MatchedTarget != nil {
		t.Fatalf("mapping from another source account leaked: %+v", unmapped.Books[0].MatchedTarget)
	}
}

func TestReadingSnapshotNeverFabricatesStartDatesOrActivity(t *testing.T) {
	ctx := context.Background()
	source := &snapshotSource{
		shelf:       ShelfResult{Complete: true, Books: []ShelfBook{snapshotBookRow("123", "shelf-123", snapshotEpoch(500))}},
		books:       map[string]Book{"123": {ID: "123", Title: "shelf-123"}},
		progressErr: map[string]error{"123": fmt.Errorf("synthetic progress failure")},
	}
	snapshot, err := ExportReadingSnapshot(ctx, snapshotDatabase(t), source, ReadingSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	book := snapshot.Books[0]
	if book.StartedOn != nil || book.LastReadAt != nil || book.FinishedAt != nil {
		t.Fatalf("snapshot invented date facts: %+v", book)
	}
	encoded, err := json.Marshal(book)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "started_on") || strings.Contains(string(encoded), "last_read_at") || strings.Contains(string(encoded), "finished_at") {
		t.Fatalf("absent date facts were serialized: %s", encoded)
	}
}

func TestReadingSnapshotRejectsForeignCursors(t *testing.T) {
	ctx := context.Background()
	source := &snapshotSource{shelf: ShelfResult{Complete: true, Books: []ShelfBook{snapshotBookRow("123", "shelf-123", snapshotEpoch(500))}}}
	db := snapshotDatabase(t)
	for _, cursor := range []string{"not-a-cursor", "!!!", encodeSnapshotCursor(-1), "b2Zmc2V0Og", strings.Repeat("A", 129), cursorWithPrefix("offset:abc"), cursorWithPrefix("decode:-1")} {
		if _, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{Cursor: cursor}); !errors.Is(err, ErrInvalidSnapshotCursor) {
			t.Fatalf("cursor %q accepted: %v", cursor, err)
		}
		if err := ValidateReadingSnapshotCursor(cursor); !errors.Is(err, ErrInvalidSnapshotCursor) {
			t.Fatalf("cursor %q passed validation: %v", cursor, err)
		}
	}
	if err := ValidateReadingSnapshotCursor(encodeSnapshotCursor(0)); err != nil {
		t.Fatalf("issued cursor rejected: %v", err)
	}
}

func TestReadingSnapshotRejectsInvalidAccounts(t *testing.T) {
	source := &snapshotSource{shelf: ShelfResult{Complete: true}}
	if _, err := ExportReadingSnapshot(context.Background(), snapshotDatabase(t), source, ReadingSnapshotOptions{SourceAccount: " \n"}); err == nil {
		t.Fatal("invalid account label accepted")
	}
	if _, err := ExportReadingSnapshot(context.Background(), snapshotDatabase(t), nil, ReadingSnapshotOptions{}); err == nil {
		t.Fatal("missing source provider accepted")
	}
}

func TestReadingSnapshotReportsUnreadableState(t *testing.T) {
	source := &snapshotSource{shelf: ShelfResult{Complete: true}}
	_, err := ExportReadingSnapshot(context.Background(), filepath.Join(snapshotTempDir(t), "missing.sqlite"), source, ReadingSnapshotOptions{})
	if !errors.Is(err, ErrSnapshotStateUnavailable) {
		t.Fatalf("unreadable state accepted: %v", err)
	}
}

func toSnapshotProgress(t *testing.T, facts ProgressFacts) Progress {
	t.Helper()
	progress, err := ProgressStatusFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	return progress
}

func snapshotIDs(books []ReadingSnapshotBook) []string {
	ids := make([]string, 0, len(books))
	for _, book := range books {
		ids = append(ids, book.SourceBookID)
	}
	return ids
}

func cursorWithPrefix(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// snapshotBindFingerprint records one account binding in an existing database so
// identity reporting can be asserted without provider credentials.
func snapshotBindFingerprint(t *testing.T, path, provider, account, fingerprint string) {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.VerifyOrBindAccountFingerprint(context.Background(), provider, account, fingerprint, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// snapshotSetMappingUpdatedAt rewrites only the stored link instant so delta
// selection can be asserted without waiting on the wall clock.
func snapshotSetMappingUpdatedAt(t *testing.T, path, bookID string, updatedAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE mappings SET updated_at=? WHERE book_id=?`, updatedAt.UTC().Format(time.RFC3339), bookID); err != nil {
		t.Fatal(err)
	}
}

func TestReadingSnapshotReportsBoundIdentitiesWithoutAccountQuery(t *testing.T) {
	ctx := context.Background()
	source := &snapshotSource{
		shelf:    ShelfResult{Complete: true, Books: []ShelfBook{snapshotBookRow("123", "shelf-123", snapshotEpoch(500))}},
		books:    map[string]Book{"123": {ID: "123", Title: "克拉拉与太阳"}},
		progress: map[string]Progress{"123": toSnapshotProgress(t, ProgressFacts{Percent: 30, Started: 1})},
	}
	db := snapshotDatabase(t,
		Mapping{Provider: "weread", SourceAccount: "mine", BookID: "123", TargetAccount: "default", WorkID: 142089, SourceTitle: "克拉拉与太阳"},
	)
	snapshotBindFingerprint(t, db, "weread", "mine", "weread:23849102")
	snapshotBindFingerprint(t, db, "hardcover", "default", "hardcover:4821")

	snapshot, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SourceIdentity != "weread:23849102" || snapshot.TargetIdentity != "hardcover:4821" {
		t.Fatalf("identities were not reported: %+v", snapshot)
	}
	// The single bound source account is read without asking for a label, so a
	// store that never used "default" still exports its confirmed mapping.
	if len(snapshot.Books) != 1 || snapshot.Books[0].MatchedTarget == nil || snapshot.Books[0].MatchedTarget.WorkID != 142089 {
		t.Fatalf("bound account mapping was not attached: %+v", snapshot.Books)
	}

	other, err := ExportReadingSnapshot(ctx, db, source, ReadingSnapshotOptions{SourceAccount: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if other.SourceIdentity != "" {
		t.Fatalf("an unbound label produced an invented identity: %q", other.SourceIdentity)
	}
}

func TestReadingChangesRefusesWatermarksBeyondTheHorizon(t *testing.T) {
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	source := &snapshotSource{shelf: ShelfResult{Complete: true}}
	db := snapshotDatabase(t)
	for name, since := range map[string]time.Time{
		"missing": {},
		"stale":   now().Add(-MaxChangeHorizon - time.Second),
	} {
		if _, err := ExportReadingChanges(ctx, db, source, ReadingChangesOptions{Since: since, Now: now}); !errors.Is(err, ErrHorizonExceeded) {
			t.Fatalf("%s watermark accepted: %v", name, err)
		}
	}
	if source.shelfCalls != 0 {
		t.Fatalf("a refused horizon reached the provider: %d shelf calls", source.shelfCalls)
	}
	boundary, err := ExportReadingChanges(ctx, db, source, ReadingChangesOptions{Since: now().Add(-MaxChangeHorizon), Now: now})
	if err != nil {
		t.Fatalf("watermark at the horizon edge was rejected: %v", err)
	}
	if boundary.Watermark != now().Format(time.RFC3339) {
		t.Fatalf("boundary watermark=%q", boundary.Watermark)
	}
}

func TestReadingChangesFiltersBySourceReadInstant(t *testing.T) {
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	since := now().Add(-24 * time.Hour)
	source := &snapshotSource{
		shelf: ShelfResult{Complete: true, Books: []ShelfBook{
			snapshotBookRow("100", "opened-recently", snapshotEpoch(since.Unix()+60)),
			snapshotBookRow("200", "opened-before-watermark", snapshotEpoch(since.Unix()-1)),
			snapshotBookRow("300", "never-opened", nil),
		}},
		books:    map[string]Book{"100": {ID: "100", Title: "最近翻开"}},
		progress: map[string]Progress{"100": toSnapshotProgress(t, ProgressFacts{Percent: 20, Started: 1, LastReadAt: since.Unix() + 60})},
	}
	changes, err := ExportReadingChanges(ctx, snapshotDatabase(t), source, ReadingChangesOptions{Since: since, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotIDs(changes.Books); len(got) != 1 || got[0] != "100" {
		t.Fatalf("changed books=%v", got)
	}
	if changes.Watermark != now().Format(time.RFC3339) || !changes.GeneratedAt.Equal(now()) {
		t.Fatalf("envelope=%+v", changes)
	}
	if changes.SourceIdentity != "" || changes.TargetIdentity != "" {
		t.Fatalf("identities were invented: %+v", changes)
	}
	if len(source.bookCalls) != 1 || len(source.readCalls) != 1 {
		t.Fatalf("unchanged books were enriched: books=%v reads=%v", source.bookCalls, source.readCalls)
	}
	if changes.Books[0].Status != "reading" || changes.Books[0].ProgressPercent != 20 {
		t.Fatalf("progress facts were not enriched: %+v", changes.Books[0])
	}
}

func TestReadingChangesFiltersByMappingUpdateInstant(t *testing.T) {
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	since := now().Add(-time.Hour)
	settled := since.Add(-time.Minute)
	db := snapshotDatabase(t,
		Mapping{Provider: "weread", SourceAccount: "default", BookID: "100", TargetAccount: "default", WorkID: 11, EditionID: 12, SourceTitle: "重新关联"},
		Mapping{Provider: "weread", SourceAccount: "default", BookID: "200", TargetAccount: "default", WorkID: 22, SourceTitle: "安静"},
	)
	// "100" changes exactly at the watermark through its mapping only, because
	// its last open precedes the watermark. "200" is quiet on both signals.
	snapshotSetMappingUpdatedAt(t, db, "100", since)
	snapshotSetMappingUpdatedAt(t, db, "200", settled)
	source := &snapshotSource{
		shelf: ShelfResult{Complete: true, Books: []ShelfBook{
			snapshotBookRow("100", "relinked", snapshotEpoch(settled.Unix())),
			snapshotBookRow("200", "quiet", snapshotEpoch(settled.Unix())),
		}},
		books: map[string]Book{
			"100": {ID: "100", Title: "重新关联"},
			"200": {ID: "200", Title: "安静"},
		},
		progress: map[string]Progress{"100": toSnapshotProgress(t, ProgressFacts{Percent: 40, Started: 1})},
	}
	changes, err := ExportReadingChanges(ctx, db, source, ReadingChangesOptions{Since: since, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotIDs(changes.Books); len(got) != 1 || got[0] != "100" {
		t.Fatalf("relinked books=%v", got)
	}
	target := changes.Books[0].MatchedTarget
	if target == nil || target.WorkID != 11 || target.EditionID != 12 {
		t.Fatalf("confirmed target was not attached: %+v", target)
	}
	if len(source.bookCalls) != 1 || source.bookCalls[0] != "100" {
		t.Fatalf("quiet books were enriched: %v", source.bookCalls)
	}
}

func TestReadingChangesPaginatesAndReportsWatermark(t *testing.T) {
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	since := now().Add(-time.Hour)
	books := []ShelfBook{
		snapshotBookRow("100", "one", snapshotEpoch(since.Unix()+30)),
		snapshotBookRow("200", "two", snapshotEpoch(since.Unix()+20)),
		snapshotBookRow("300", "three", snapshotEpoch(since.Unix()+10)),
	}
	source := &snapshotSource{
		shelf:    ShelfResult{Complete: true, Books: books},
		books:    map[string]Book{"100": {ID: "100"}, "200": {ID: "200"}, "300": {ID: "300"}},
		progress: map[string]Progress{},
	}
	first, err := ExportReadingChanges(ctx, snapshotDatabase(t), source, ReadingChangesOptions{Since: since, Limit: 2, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotIDs(first.Books); len(got) != 2 || !first.HasMore || first.Cursor == "" {
		t.Fatalf("first page ids=%v more=%v cursor=%q", got, first.HasMore, first.Cursor)
	}
	second, err := ExportReadingChanges(ctx, snapshotDatabase(t), source, ReadingChangesOptions{Since: since, Limit: 2, Cursor: first.Cursor, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotIDs(second.Books); len(got) != 1 || second.HasMore || second.Cursor != "" {
		t.Fatalf("second page ids=%v more=%v cursor=%q", got, second.HasMore, second.Cursor)
	}
	if first.Watermark != second.Watermark {
		t.Fatalf("pagination shifted the watermark: %q then %q", first.Watermark, second.Watermark)
	}
	if _, err := ExportReadingChanges(ctx, snapshotDatabase(t), source, ReadingChangesOptions{Since: since, Cursor: "not-a-cursor", Now: now}); !errors.Is(err, ErrInvalidSnapshotCursor) {
		t.Fatalf("foreign cursor accepted: %v", err)
	}
}

func TestReadingChangesReportsUnreadableState(t *testing.T) {
	now := time.Now().UTC()
	_, err := ExportReadingChanges(context.Background(), filepath.Join(snapshotTempDir(t), "missing.sqlite"), &snapshotSource{shelf: ShelfResult{Complete: true}},
		ReadingChangesOptions{Since: now.Add(-time.Hour), Now: func() time.Time { return now }})
	if !errors.Is(err, ErrSnapshotStateUnavailable) {
		t.Fatalf("unreadable state accepted: %v", err)
	}
}
