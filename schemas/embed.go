// Package schemas embeds public, versioned interchange schemas.
package schemas

import _ "embed"

// MatchingCatalogV1 is the frozen Draft 2020-12 catalog interchange schema.
//
//go:embed matching-catalog-v1.schema.json
var MatchingCatalogV1 []byte

// ReadingSnapshotV1 is the Draft 2020-12 schema of one reading snapshot or
// changes page, version "1.0".
//
//go:embed reading-snapshot-v1.schema.json
var ReadingSnapshotV1 []byte
