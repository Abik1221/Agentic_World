// Package platform holds leaf-level, cross-cutting primitives (logging, metrics,
// time, IDs, tracing). It imports nothing from internal/* and may be imported by
// everyone. Keeping it dependency-free is what lets every other module stay
// testable and decoupled.
package platform

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a JSON structured logger writing to stdout at the given level.
// In non-prod we add the source location to ease debugging.
func NewLogger(env, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	if env != "prod" && env != "staging" {
		opts.AddSource = true
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
