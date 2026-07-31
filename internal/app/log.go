package app

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// ParseLevel maps `log.level` (and `--log-level`) onto a slog level.
//
// The four names are the ones arch §9 documents. Anything else is an error
// rather than a silent fallback to info: a typo in a level is exactly the kind
// of thing that is discovered six weeks later when the log that should have
// explained an incident turns out to have been at the wrong verbosity.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q; use debug, info, warn or error", name)
	}
}

// NewLogger builds the process logger (NFR-OPS-005).
//
// format is `log.format`: "text" for a human at a terminal, "json" for anything
// that ships logs somewhere. level is already resolved — `--log-level` wins over
// the config file, and cmd/shelf does that resolution so the precedence is
// visible at the one place a reader looks for it.
func NewLogger(w io.Writer, format string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}
