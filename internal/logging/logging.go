// Package logging configures Drifter's structured stdout logging.
//
// JSON lines on stdout suit container log collectors. Log records must never
// contain credentials, tokens or raw provider response bodies; callers log
// operations and outcomes, not payloads.
package logging

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"

	"github.com/azusachino/drifter/internal/config"
)

// New builds the process logger from validated configuration.
func New(level config.Level, out io.Writer) (*slog.Logger, error) {
	var slogLevel slog.Level
	switch level {
	case config.LevelDebug:
		slogLevel = slog.LevelDebug
	case config.LevelWarn:
		slogLevel = slog.LevelWarn
	case config.LevelError:
		slogLevel = slog.LevelError
	case config.LevelInfo, "":
		slogLevel = slog.LevelInfo
	default:
		return nil, fmt.Errorf("unsupported log level")
	}
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slogLevel})), nil
}

// NewRunID returns a random identifier correlating one invocation's log lines.
func NewRunID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("run identifier unavailable")
	}
	return hex.EncodeToString(raw[:]), nil
}
