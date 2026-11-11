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
	Percent    int   `json:"percent"`
	Started    bool  `json:"started"`
	FinishedAt int64 `json:"finished_at,omitempty"`
	// ReadingTimeSeconds is cumulative reading time, never a session interval.
	ReadingTimeSeconds int `json:"reading_time_seconds,omitempty"`
	// LastReadAt is the last reading instant in epoch seconds; zero means absent.
	LastReadAt int64  `json:"last_read_at,omitempty"`
	Status     string `json:"proposed_status"`
}

// ProgressFacts are the raw per-book progress facts a source reports. Reading
// time is cumulative seconds and LastReadAt is epoch seconds; zero means the
// source did not report that fact.
type ProgressFacts struct {
	Percent            int
	Started            int
	FinishedAt         int64
	ReadingTimeSeconds int
	LastReadAt         int64
}

func progressStatus(percent, started int, finishedAt int64) (Progress, error) {
	return ProgressStatusFacts(ProgressFacts{Percent: percent, Started: started, FinishedAt: finishedAt})
}

// ProgressStatusFacts computes the proposed reading status from progress facts,
// including cumulative reading time and the last reading instant when reported.
func ProgressStatusFacts(facts ProgressFacts) (Progress, error) {
	if facts.Percent < 0 || facts.Percent > 100 || (facts.Started != 0 && facts.Started != 1) || facts.FinishedAt < 0 || facts.ReadingTimeSeconds < 0 || facts.LastReadAt < 0 {
		return Progress{}, fmt.Errorf("invalid WeRead progress fields")
	}
	status := "unknown"
	if facts.FinishedAt > 0 {
		status = "read"
	} else if facts.Started == 1 || facts.Percent > 0 {
		status = "reading"
	}
	return Progress{
		Percent:            facts.Percent,
		Started:            facts.Started == 1,
		FinishedAt:         facts.FinishedAt,
		ReadingTimeSeconds: facts.ReadingTimeSeconds,
		LastReadAt:         facts.LastReadAt,
		Status:             status,
	}, nil
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
