// Package log configures the process-wide structured logger.
//
// On OpenWrt the daemon is supervised by procd with `procd_set_param stdout 1`
// and `stderr 1`, which pipes both streams straight into syslog. That makes
// plain text on stdout the cheapest possible log sink for the LuCI status view,
// so text is the default format and JSON is opt-in.
package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Format selects the on-wire encoding of log records.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Options configures the logger built by Setup.
type Options struct {
	// Level is one of debug, info, warn, error. Empty means info.
	Level string
	// Format is text or json. Empty means text.
	Format Format
	// Output defaults to os.Stdout when nil.
	Output io.Writer
	// AddSource attaches file:line to every record. Expensive; debug only.
	AddSource bool
}

// Setup builds a logger from opts and installs it as the slog default.
// It returns the logger so callers can hold a direct reference.
func Setup(opts Options) (*slog.Logger, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, err
	}

	out := opts.Output
	if out == nil {
		out = os.Stdout
	}

	handlerOpts := &slog.HandlerOptions{
		Level:     level,
		AddSource: opts.AddSource,
	}

	var handler slog.Handler
	switch f := Format(strings.ToLower(string(opts.Format))); f {
	case FormatJSON:
		handler = slog.NewJSONHandler(out, handlerOpts)
	case FormatText, "":
		handler = slog.NewTextHandler(out, handlerOpts)
	default:
		return nil, fmt.Errorf("unknown log format %q (want text or json)", opts.Format)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger, nil
}

// ParseLevel maps a human-written level name onto a slog.Level.
// An empty string yields info so that a zero-valued config still works.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", name)
	}
}

// Discard returns a logger that throws everything away. Useful in tests.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 1,
	}))
}
