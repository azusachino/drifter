package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/azusachino/drifter/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const readingGoldenPath = "../../schemas/testdata/reading-snapshot-v1.golden.json"

func compileReadingSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	var schema any
	if err := json.Unmarshal(schemas.ReadingSnapshotV1, &schema); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	const id = "https://github.com/azusachino/drifter/blob/main/schemas/reading-snapshot-v1.schema.json"
	if err := compiler.AddResource(id, schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func validateReadingPage(t *testing.T, schema *jsonschema.Schema, raw []byte) {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("page does not validate against reading-snapshot-v1: %v\n%s", err, raw)
	}
}

// goldenReadingPage is the page the golden fixture records: a first page of a
// run with a completed, mapped book and a custom upload, and more to come.
func goldenReadingPage(t *testing.T) []byte {
	t.Helper()
	source := &snapshotSource{
		shelf: ShelfResult{Complete: true, Books: []ShelfBook{
			snapshotBookRow("100", "shelf-100", snapshotEpoch(1700000300)),
			snapshotBookRow("CB_custom_1", "自定义上传", snapshotEpoch(1700000200)),
			snapshotBookRow("200", "shelf-200", snapshotEpoch(1700000100)),
		}},
		books: map[string]Book{
			"100": {ID: "100", Title: "克拉拉与太阳", Author: "石黑一雄", Translator: "赵松", Publisher: "上海译文出版社", ISBN: "9787532786831", ReaderURL: "https://weread.qq.com/web/bookDetail/100"},
		},
		progress: map[string]Progress{
			"100": toSnapshotProgress(t, ProgressFacts{Percent: 100, Started: 1, FinishedAt: 1700000000, ReadingTimeSeconds: 18240, LastReadAt: 1700000000}),
		},
	}
	db := snapshotDatabase(t, Mapping{Provider: DefaultSourceProvider, SourceAccount: "default", TargetAccount: "default", BookID: "100", WorkID: 4821, EditionID: 9001, SourceTitle: "克拉拉与太阳"})
	snapshotBindFingerprint(t, db, DefaultSourceProvider, "default", "weread:23849102")
	snapshotBindFingerprint(t, db, DefaultTargetProvider, "default", "hardcover:4821")
	page, err := ExportReadingSnapshot(context.Background(), db, source, ReadingSnapshotOptions{
		Limit: 2, Now: func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(page, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func TestReadingSnapshotPageValidatesAgainstPublishedSchema(t *testing.T) {
	schema := compileReadingSchema(t)
	generated := goldenReadingPage(t)
	validateReadingPage(t, schema, generated)

	// A final page and an empty page validate too.
	empty, err := ExportReadingSnapshot(context.Background(), snapshotDatabase(t), &snapshotSource{shelf: ShelfResult{Complete: true}}, ReadingSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	validateReadingPage(t, schema, raw)

	// The schema refuses what the contract does not allow.
	for name, bad := range map[string]string{
		"unknown major":   `{"version":"2.0","generated_at":"2026-11-11T10:00:00Z","source_identity":"","watermark":"2026-11-11T10:00:00Z","has_more":false,"books":[]}`,
		"more, no cursor": `{"version":"1.0","generated_at":"2026-11-11T10:00:00Z","source_identity":"","watermark":"2026-11-11T10:00:00Z","has_more":true,"books":[]}`,
		"unknown field":   `{"version":"1.0","generated_at":"2026-11-11T10:00:00Z","source_identity":"","watermark":"2026-11-11T10:00:00Z","has_more":false,"books":[],"extra":1}`,
	} {
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(bad)))
		if err != nil {
			t.Fatal(err)
		}
		if schema.Validate(instance) == nil {
			t.Fatalf("%s page validated", name)
		}
	}
}

func TestReadingSnapshotGoldenPageIsCurrent(t *testing.T) {
	generated := goldenReadingPage(t)
	if os.Getenv("DRIFTER_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(readingGoldenPath, generated, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile(readingGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	validateReadingPage(t, compileReadingSchema(t), golden)
	var want, got any
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(generated, &got); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatalf("golden page is stale; regenerate with DRIFTER_UPDATE_GOLDEN=1\nwant %s\ngot  %s", wantJSON, gotJSON)
	}
}
