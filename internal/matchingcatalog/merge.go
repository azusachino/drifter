package matchingcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
)

// Document is the published v1 catalog interchange format.
type Document struct {
	CatalogSchemaVersion int         `json:"catalog_schema_version"`
	CatalogID            string      `json:"catalog_id"`
	Publisher            string      `json:"publisher"`
	Assertions           []Assertion `json:"assertions"`
	Hints                []Hint      `json:"hints"`
}

// MarshalDocument validates a complete v1 document before returning JSON.
func MarshalDocument(doc Document) ([]byte, error) {
	if doc.Assertions == nil {
		doc.Assertions = []Assertion{}
	}
	if doc.Hints == nil {
		doc.Hints = []Hint{}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("catalog document invalid")
	}
	var totalRecords int
	if _, code := decodeDocument(raw, &totalRecords); code != "" {
		return nil, fmt.Errorf("catalog document invalid")
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil || int64(len(encoded)) > maxDocumentBytes {
		return nil, fmt.Errorf("catalog document exceeds v1 output limits")
	}
	return encoded, nil
}

// MergeLocal combines explicitly named local v1 catalogs without exposing
// their paths or subscription aliases in the resulting document.
func MergeLocal(ctx context.Context, paths []string, catalogID, publisher string) ([]byte, error) {
	if len(paths) < 2 || len(paths) > maxCatalogs {
		return nil, fmt.Errorf("catalog merge requires between two and eight inputs")
	}
	seenPaths := make(map[string]struct{}, len(paths))
	subscriptions := make([]Subscription, 0, len(paths))
	for i, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("catalog merge inputs invalid")
		}
		absolute = filepath.Clean(absolute)
		if _, exists := seenPaths[absolute]; exists {
			return nil, fmt.Errorf("catalog merge inputs invalid")
		}
		seenPaths[absolute] = struct{}{}
		subscriptions = append(subscriptions, Subscription{ID: fmt.Sprintf("input-%d", i+1), File: absolute})
	}
	loader, err := NewLoader(subscriptions, LoaderOptions{})
	if err != nil {
		return nil, fmt.Errorf("catalog merge inputs invalid")
	}
	snapshot := loader.Load(ctx)
	if !snapshot.Complete || len(snapshot.Catalogs) != len(paths) {
		return nil, fmt.Errorf("catalog merge inputs invalid")
	}

	sourceIDs := make(map[string]struct{}, len(snapshot.Catalogs))
	for _, catalog := range snapshot.Catalogs {
		if _, exists := sourceIDs[catalog.CatalogID]; exists || catalog.CatalogID == catalogID {
			return nil, fmt.Errorf("catalog merge identities invalid")
		}
		sourceIDs[catalog.CatalogID] = struct{}{}
	}

	merged := Document{
		CatalogSchemaVersion: 1,
		CatalogID:            catalogID,
		Publisher:            publisher,
		Assertions:           []Assertion{},
		Hints:                []Hint{},
	}
	usedRecordIDs := make(map[string]struct{})
	for _, catalog := range snapshot.Catalogs {
		for _, assertion := range catalog.Assertions {
			originalID := assertion.RecordID
			assertion.RecordID = mergedRecordID(originalID, catalog.CatalogID, usedRecordIDs)
			provenance, ok := appendLineage(assertion.Provenance, catalog.CatalogID, originalID)
			if !ok {
				return nil, fmt.Errorf("catalog merge provenance exceeds v1 limits")
			}
			assertion.Provenance = provenance
			merged.Assertions = append(merged.Assertions, assertion)
		}
		for _, hint := range catalog.Hints {
			originalID := hint.RecordID
			hint.RecordID = mergedRecordID(originalID, catalog.CatalogID, usedRecordIDs)
			provenance, ok := appendLineage(hint.Provenance, catalog.CatalogID, originalID)
			if !ok {
				return nil, fmt.Errorf("catalog merge provenance exceeds v1 limits")
			}
			hint.Provenance = provenance
			merged.Hints = append(merged.Hints, hint)
		}
	}
	encoded, err := MarshalDocument(merged)
	if err != nil {
		return nil, fmt.Errorf("catalog merge output exceeds v1 limits or is invalid")
	}
	return encoded, nil
}

func mergedRecordID(recordID, catalogID string, used map[string]struct{}) string {
	if _, exists := used[recordID]; !exists {
		used[recordID] = struct{}{}
		return recordID
	}
	digest := sha256.Sum256([]byte(catalogID + "\x00" + recordID))
	base := "merge-" + hex.EncodeToString(digest[:])
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		candidate = base + "-" + strconv.Itoa(suffix)
	}
}

func appendLineage(provenance []Provenance, catalogID, originalID string) ([]Provenance, bool) {
	lineage := Provenance{Source: "catalog merge lineage", Reference: catalogID + "/" + originalID}
	for _, item := range provenance {
		if item == lineage {
			return append([]Provenance(nil), provenance...), true
		}
	}
	if len(provenance) >= 8 || len(lineage.Reference) > 512 {
		return nil, false
	}
	result := append([]Provenance(nil), provenance...)
	return append(result, lineage), true
}
