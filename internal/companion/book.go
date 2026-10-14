package companion

import (
	"fmt"
	"strings"
	"unicode"
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
	if percent == 100 && finishedAt > 0 {
		status = "read"
	} else if percent < 100 && (started == 1 || percent > 0) {
		status = "reading"
	}
	return Progress{percent, started == 1, finishedAt, status}, nil
}

// NormalizeISBN validates identity evidence; an arbitrary ASIN is not an ISBN.
func NormalizeISBN(raw string) (string, error) {
	isbn := strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToUpper(r)
	}, raw)
	if len(isbn) != 10 && len(isbn) != 13 {
		return "", fmt.Errorf("ISBN must have 10 or 13 characters")
	}
	sum := 0
	for i, r := range isbn {
		n := int(r - '0')
		if len(isbn) == 10 && i == 9 && r == 'X' {
			n = 10
		} else if n < 0 || n > 9 {
			return "", fmt.Errorf("invalid ISBN character")
		}
		if len(isbn) == 10 {
			sum += n * (10 - i)
		} else if i%2 == 0 {
			sum += n
		} else {
			sum += n * 3
		}
	}
	if len(isbn) == 10 {
		if sum%11 != 0 {
			return "", fmt.Errorf("invalid ISBN checksum")
		}
		prefix := "978" + isbn[:9]
		sum = 0
		for i, r := range prefix {
			n := int(r - '0')
			if i%2 == 1 {
				n *= 3
			}
			sum += n
		}
		return fmt.Sprintf("%s%d", prefix, (10-sum%10)%10), nil
	}
	if sum%10 != 0 {
		return "", fmt.Errorf("invalid ISBN checksum")
	}
	if !strings.HasPrefix(isbn, "978") && !strings.HasPrefix(isbn, "979") {
		return "", fmt.Errorf("invalid ISBN prefix")
	}
	return isbn, nil
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
