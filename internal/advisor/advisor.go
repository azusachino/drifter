// Package advisor defines the interfaces and data structures for the Drifter
// LLM advisor: a bounded, abstention-first helper for explaining reading status
// conflicts and resolving the foreign original of a translated work.
package advisor

import (
	"context"
	"time"
)

// ConflictExplanationRequest carries the two diverging reading states and the
// evidence that triggered the divergence.
type ConflictExplanationRequest struct {
	SourceTitle    string    `json:"source_title"`
	SourceAuthor   string    `json:"source_author"`
	SourceStatus   string    `json:"source_status"`
	TargetStatus   string    `json:"target_status"`
	BaselineStatus string    `json:"baseline_status,omitempty"`
	Reason         string    `json:"reason"`
	Message        string    `json:"message"`
	ObservedAt     time.Time `json:"observed_at"`
}

// ConflictExplanationResponse is a bounded recommendation for resolving a
// status conflict. RecommendedAction is one of "adopt_source",
// "adopt_target", or "keep_separate".
type ConflictExplanationResponse struct {
	Summary           string `json:"summary"`            // Single clear sentence explaining what diverged
	RecommendedAction string `json:"recommended_action"` // "adopt_source", "adopt_target", "keep_separate"
	Rationale         string `json:"rationale"`          // Detailed reasoning why this action is safe
}

// OriginalWorkRequest carries the bibliographic evidence for one localized
// edition. A missing Translator is meaningful evidence that the work may be
// native rather than a translation.
type OriginalWorkRequest struct {
	Title               string `json:"title"`
	Author              string `json:"author"`
	Translator          string `json:"translator,omitempty"`
	Publisher           string `json:"publisher,omitempty"`
	ISBN                string `json:"isbn,omitempty"`
	DoubanOriginalTitle string `json:"douban_original_title,omitempty"`
}

// OriginalWorkResponse is the resolved foreign original. Abstained is true
// when the work is native (for example a domestic technical manual or a
// domestic novel) or the evidence is insufficient to name an original.
type OriginalWorkResponse struct {
	OriginalTitle  string  `json:"original_title,omitempty"`
	OriginalAuthor string  `json:"original_author,omitempty"`
	Language       string  `json:"language,omitempty"`
	Confidence     float64 `json:"confidence"` // 0.0 to 1.0
	Abstained      bool    `json:"abstained"`  // true if domestic book with no foreign original or insufficient evidence
	Rationale      string  `json:"rationale"`
}

// Advisor is the small, deliberately bounded surface the advisor offers.
type Advisor interface {
	ExplainConflict(ctx context.Context, req ConflictExplanationRequest) (ConflictExplanationResponse, error)
	ResolveOriginalWork(ctx context.Context, req OriginalWorkRequest) (OriginalWorkResponse, error)
}
