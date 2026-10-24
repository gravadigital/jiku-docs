// Package logging builds the JSON logger. Logs carry method, caller, correlationId, duration
// and errorCode — never payloads, document content, tokens or presigned URLs.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON logger at the given level (debug, info, warn, error).
func New(level string) *slog.Logger {
	return NewTo(os.Stderr, level)
}

// NewTo writes to w.
func NewTo(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l})).With("service", "jiku-docs")
}

// Discard is a logger that drops everything, for tests.
func Discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
