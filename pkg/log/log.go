package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

type Options struct {
	Level string

	Format Format

	Output io.Writer

	AddSource bool
}

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

func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 1,
	}))
}
