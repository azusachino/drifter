package opencode

import (
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
)

//go:embed prompts/persona.md
var baseAdvisorPersona string

//go:embed prompts/conflict.md
var conflictTaskPrompt string

//go:embed prompts/original_work.md
var originalWorkTaskPrompt string

var (
	conflictSystemPrompt     = strings.TrimSpace(baseAdvisorPersona) + "\n\n" + strings.TrimSpace(conflictTaskPrompt)
	originalWorkSystemPrompt = strings.TrimSpace(baseAdvisorPersona) + "\n\n" + strings.TrimSpace(originalWorkTaskPrompt)
)

// BaseAdvisorPersona returns the embedded core advisor persona and operating principles.
func BaseAdvisorPersona() string {
	return strings.TrimSpace(baseAdvisorPersona)
}

func conflictPrompt(req advisor.ConflictExplanationRequest) string {
	var b strings.Builder
	b.WriteString("Source provider state:\n")
	fmt.Fprintf(&b, "  title: %s\n", req.SourceTitle)
	fmt.Fprintf(&b, "  author: %s\n", req.SourceAuthor)
	fmt.Fprintf(&b, "  status: %s\n", req.SourceStatus)
	b.WriteString("\nTarget provider state:\n")
	fmt.Fprintf(&b, "  status: %s\n", req.TargetStatus)
	if strings.TrimSpace(req.BaselineStatus) != "" {
		fmt.Fprintf(&b, "\nPreviously recorded baseline status: %s\n", req.BaselineStatus)
	}
	fmt.Fprintf(&b, "\nDivergence trigger: %s\n", req.Reason)
	if strings.TrimSpace(req.Message) != "" {
		fmt.Fprintf(&b, "System message: %s\n", req.Message)
	}
	if !req.ObservedAt.IsZero() {
		fmt.Fprintf(&b, "\nObserved at: %s\n", req.ObservedAt.Format(time.RFC3339))
	}
	b.WriteString("\nExplain the divergence and recommend the safest resolution.")
	return b.String()
}

func originalWorkPrompt(req advisor.OriginalWorkRequest) string {
	var b strings.Builder
	b.WriteString("Localized edition:\n")
	fmt.Fprintf(&b, "  title: %s\n", req.Title)
	fmt.Fprintf(&b, "  author: %s\n", req.Author)
	if strings.TrimSpace(req.Translator) != "" {
		fmt.Fprintf(&b, "  translator: %s\n", req.Translator)
	} else {
		b.WriteString("  translator: (not recorded)\n")
	}
	if strings.TrimSpace(req.Publisher) != "" {
		fmt.Fprintf(&b, "  publisher: %s\n", req.Publisher)
	}
	if strings.TrimSpace(req.ISBN) != "" {
		fmt.Fprintf(&b, "  isbn: %s\n", req.ISBN)
	}
	if strings.TrimSpace(req.DoubanOriginalTitle) != "" {
		fmt.Fprintf(&b, "  catalog original title: %s\n", req.DoubanOriginalTitle)
	}
	b.WriteString("\nResolve the original foreign work, or abstain if the work is native or the evidence is insufficient.")
	return b.String()
}
