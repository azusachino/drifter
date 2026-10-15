// Package schemas embeds public, versioned interchange schemas.
package schemas

import _ "embed"

// MatchingCatalogV1 is the frozen Draft 2020-12 catalog interchange schema.
//
//go:embed matching-catalog-v1.schema.json
var MatchingCatalogV1 []byte
