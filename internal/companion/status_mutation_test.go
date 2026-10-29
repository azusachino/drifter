package companion

import (
	"errors"
	"testing"
)

func TestParseHardcoverStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"1", HardcoverStatusWantToRead},
		{"want_to_read", HardcoverStatusWantToRead},
		{"want-to-read", HardcoverStatusWantToRead},
		{"want to read", HardcoverStatusWantToRead},
		{"to-read", HardcoverStatusWantToRead},
		{"2", HardcoverStatusCurrentlyReading},
		{"reading", HardcoverStatusCurrentlyReading},
		{"currently_reading", HardcoverStatusCurrentlyReading},
		{"currently-reading", HardcoverStatusCurrentlyReading},
		{"3", HardcoverStatusRead},
		{"read", HardcoverStatusRead},
		{"finished", HardcoverStatusRead},
		{"4", HardcoverStatusDidNotFinish},
		{"dnf", HardcoverStatusDidNotFinish},
		{"did_not_finish", HardcoverStatusDidNotFinish},
		{"did-not-finish", HardcoverStatusDidNotFinish},
		{"5", HardcoverStatusOnHold},
		{"paused", HardcoverStatusOnHold},
		{"on_hold", HardcoverStatusOnHold},
		{"on-hold", HardcoverStatusOnHold},
		{"hold", HardcoverStatusOnHold},
		// Case and whitespace insensitivity
		{"  READ  ", HardcoverStatusRead},
		{"Currently-Reading", HardcoverStatusCurrentlyReading},
		{"DNF", HardcoverStatusDidNotFinish},
	}

	for _, tc := range tests {
		got, err := ParseHardcoverStatus(tc.input)
		if err != nil || got != tc.expected {
			t.Errorf("ParseHardcoverStatus(%q) = %d, err=%v; want %d", tc.input, got, err, tc.expected)
		}
	}

	// Invalid inputs
	for _, bad := range []string{"", "invalid", "0", "6", "deleted", "abandoned"} {
		if _, err := ParseHardcoverStatus(bad); err == nil || !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("ParseHardcoverStatus(%q) expected ErrInvalidStatus, got err=%v", bad, err)
		}
	}
}
