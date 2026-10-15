package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeCatalog(t *testing.T, dir, name string, doc Document) string {
	t.Helper()
	data, err := MarshalDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sampleAssertion(id string, workID int64) Assertion {
	return Assertion{
		RecordID:   id,
		Source:     SourceRef{Provider: "weread", ID: "123"},
		Target:     TargetRef{Provider: "hardcover", WorkID: workID},
		Provenance: []Provenance{{Source: "source", Reference: "row"}},
	}
}

func readDocument(t *testing.T, raw []byte) Document {
	t.Helper()
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestMergeLocalPreservesEvidenceAndDeterministicallyResolvesRecordIDCollisions(t *testing.T) {
	dir := t.TempDir()
	first := writeCatalog(t, dir, "first.json", Document{
		CatalogSchemaVersion: 1, CatalogID: "first", Publisher: "first publisher",
		Assertions: []Assertion{sampleAssertion("shared", 10)},
		Hints:      []Hint{{RecordID: "hint", Source: SourceRef{Provider: "weread", ID: "123"}, Title: "suggestion", Confidence: 0.5, Provenance: []Provenance{{Source: "hint source", Reference: "hint row"}}}},
	})
	second := writeCatalog(t, dir, "second.json", Document{
		CatalogSchemaVersion: 1, CatalogID: "second", Publisher: "second publisher",
		Assertions: []Assertion{sampleAssertion("shared", 11)},
	})
	args := []string{first, second}
	got, err := MergeLocal(context.Background(), args, "combined", "chosen publisher")
	if err != nil {
		t.Fatal(err)
	}
	again, err := MergeLocal(context.Background(), args, "combined", "chosen publisher")
	if err != nil || string(got) != string(again) {
		t.Fatalf("same ordered inputs are not byte-for-byte idempotent: %v", err)
	}
	doc := readDocument(t, got)
	if doc.CatalogID != "combined" || doc.Publisher != "chosen publisher" || len(doc.Assertions) != 2 || len(doc.Hints) != 1 {
		t.Fatalf("merge lost evidence or selected metadata: %+v", doc)
	}
	if doc.Assertions[0].RecordID != "shared" {
		t.Fatalf("unexpected first collision record ID: %+v", doc.Assertions)
	}
	digest := sha256.Sum256([]byte("second\x00shared"))
	if doc.Assertions[1].RecordID != "merge-"+hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected collision strategy: %+v", doc.Assertions)
	}
	if len(doc.Assertions[0].Provenance) != 2 || doc.Assertions[0].Provenance[1] != (Provenance{Source: "catalog merge lineage", Reference: "first/shared"}) {
		t.Fatalf("original identity lineage missing: %+v", doc.Assertions[0].Provenance)
	}
	if doc.Assertions[1].Target.WorkID != 11 || doc.Assertions[1].Provenance[1].Reference != "second/shared" {
		t.Fatalf("conflicting assertion or identity was discarded: %+v", doc.Assertions[1])
	}
}

func TestMergeLocalRejectsRepeatedInputsDuplicateCatalogIDsAndOutputCollision(t *testing.T) {
	dir := t.TempDir()
	first := writeCatalog(t, dir, "first.json", Document{CatalogSchemaVersion: 1, CatalogID: "same", Publisher: "p", Assertions: []Assertion{}, Hints: []Hint{}})
	second := filepath.Join(dir, "sub", "..", "first.json")
	other := writeCatalog(t, dir, "other.json", Document{CatalogSchemaVersion: 1, CatalogID: "same", Publisher: "p", Assertions: []Assertion{}, Hints: []Hint{}})
	for name, tc := range map[string]struct {
		paths []string
		id    string
	}{
		"repeated path":               {[]string{first, second}, "combined"},
		"duplicate catalog id":        {[]string{first, other}, "combined"},
		"output catalog id collision": {[]string{first, writeCatalog(t, dir, "third.json", Document{CatalogSchemaVersion: 1, CatalogID: "third", Publisher: "p", Assertions: []Assertion{}, Hints: []Hint{}})}, "same"},
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := MergeLocal(context.Background(), tc.paths, tc.id, "publisher"); err == nil || len(out) != 0 {
				t.Fatalf("invalid merge was not rejected atomically: bytes=%d err=%v", len(out), err)
			}
		})
	}
}

func TestMergeLocalRejectsMalformedInputAndLineageOverflowAtomically(t *testing.T) {
	dir := t.TempDir()
	valid := writeCatalog(t, dir, "valid.json", Document{CatalogSchemaVersion: 1, CatalogID: "valid", Publisher: "p", Assertions: []Assertion{sampleAssertion("one", 10)}})
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"catalog_schema_version":1,"catalog_id":"broken"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := MergeLocal(context.Background(), []string{valid, bad}, "combined", "p"); err == nil || len(out) != 0 {
		t.Fatalf("malformed input was not rejected atomically: %d %v", len(out), err)
	}
	provenance := make([]Provenance, 8)
	for i := range provenance {
		provenance[i] = Provenance{Source: "original", Reference: "reference"}
	}
	full := writeCatalog(t, dir, "full.json", Document{CatalogSchemaVersion: 1, CatalogID: "full", Publisher: "p", Assertions: []Assertion{{RecordID: "full-record", Source: SourceRef{Provider: "weread", ID: "123"}, Target: TargetRef{Provider: "hardcover", WorkID: 12}, Provenance: provenance}}})
	if out, err := MergeLocal(context.Background(), []string{full, valid}, "combined", "p"); err == nil || len(out) != 0 {
		t.Fatalf("lineage overflow was not rejected atomically: %d %v", len(out), err)
	}
}

func TestMergeLocalRejectsOversizedInputsAndCombinedOutputWithoutTruncation(t *testing.T) {
	dir := t.TempDir()
	valid := writeCatalog(t, dir, "valid.json", Document{CatalogSchemaVersion: 1, CatalogID: "valid", Publisher: "p", Assertions: []Assertion{}, Hints: []Hint{}})
	oversized := filepath.Join(dir, "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, maxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := MergeLocal(context.Background(), []string{valid, oversized}, "combined", "p"); err == nil || len(out) != 0 {
		t.Fatalf("oversized input was not rejected atomically: %d %v", len(out), err)
	}

	makeLarge := func(catalogID, prefix string) string {
		hints := make([]Hint, 800)
		for i := range hints {
			hints[i] = Hint{
				RecordID: prefix + "-" + strconv.Itoa(i),
				Source:   SourceRef{Provider: "weread", ID: "123"},
				Title:    strings.Repeat("x", 700), Confidence: 0.5,
				Provenance: []Provenance{{Source: "source", Reference: "ref"}},
			}
		}
		return writeCatalog(t, dir, catalogID+".json", Document{CatalogSchemaVersion: 1, CatalogID: catalogID, Publisher: "p", Assertions: []Assertion{}, Hints: hints})
	}
	first, second := makeLarge("large-one", "one"), makeLarge("large-two", "two")
	if out, err := MergeLocal(context.Background(), []string{first, second}, "combined", "p"); err == nil || len(out) != 0 {
		t.Fatalf("oversized combined output was not rejected atomically: %d %v", len(out), err)
	}
}

func TestMergeLocalDoesNotExposeInputPathsInErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-alias.json")
	_, err := MergeLocal(context.Background(), []string{path, path}, "combined", "publisher")
	if err == nil || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "private-alias") {
		t.Fatalf("input path leaked or duplicate accepted: %v", err)
	}
}
