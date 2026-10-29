package companion

import (
	"fmt"
	"strings"

	"github.com/azusachino/drifter/internal/catalog"
)

// Book contains identity evidence, not a confirmed cross-provider mapping.
type Book struct {
	ID         string `json:"book_id"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	Translator string `json:"translator,omitempty"`
	// TranslatorKnown distinguishes an explicit empty field from missing evidence.
	TranslatorKnown bool   `json:"-"`
	ISBN            string `json:"isbn,omitempty"`
	Publisher       string `json:"publisher,omitempty"`
	ReaderURL       string `json:"reader_url,omitempty"`
}

type Progress struct {
	Percent    int    `json:"percent"`
	Started    bool   `json:"started"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	Status     string `json:"proposed_status"`
}

func progressStatus(percent, started int, finishedAt int64) (Progress, error) {
	if percent < 0 || percent > 100 || (started != 0 && started != 1) || finishedAt < 0 {
		return Progress{}, fmt.Errorf("invalid WeRead progress fields")
	}
	status := "unknown"
	if finishedAt > 0 {
		status = "read"
	} else if percent < 100 && (started == 1 || percent > 0) {
		status = "reading"
	}
	return Progress{percent, started == 1, finishedAt, status}, nil
}

// ProgressStatus computes the proposed reading status from progress facts.
func ProgressStatus(percent, started int, finishedAt int64) (Progress, error) {
	return progressStatus(percent, started, finishedAt)
}

// SearchResult holds candidate books and pagination state from a source catalog search.
type SearchResult struct {
	Books   []Book `json:"books"`
	HasMore bool   `json:"has_more"`
}

// NormalizeISBN validates identity evidence; an arbitrary ASIN is not an ISBN.
func NormalizeISBN(raw string) (string, error) {
	return catalog.NormalizeISBN(raw)
}

// ISBNVariants returns valid ISBN-13 and calculated ISBN-10 variants for lookup.
func ISBNVariants(raw string) ([]string, error) {
	isbn, err := NormalizeISBN(raw)
	if err != nil {
		return nil, err
	}
	ids := []string{isbn}
	if strings.HasPrefix(isbn, "978") {
		prefix, sum := isbn[3:12], 0
		for i, r := range prefix {
			sum += int(r-'0') * (10 - i)
		}
		check := (11 - sum%11) % 11
		last := fmt.Sprint(check)
		if check == 10 {
			last = "X"
		}
		ids = append(ids, prefix+last)
	}
	return ids, nil
}

func ValidBookID(id string) bool {
	if len(id) == 0 || len(id) > 32 || strings.Trim(id, "0") == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
