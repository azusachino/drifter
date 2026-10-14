package matchingcatalog

import (
	"fmt"
	"strings"
	"unicode"
)

// NormalizeISBN validates an ISBN-10/13 checksum and returns ISBN-13 form.
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
		prefix, sum := "978"+isbn[:9], 0
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
