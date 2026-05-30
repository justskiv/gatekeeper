// Package applog wires slog to the project's logging configuration so
// every binary (gatekeeper, migrate) emits logs in the same shape.
package applog

import (
	"io"
	"log/slog"
	"os"

	"github.com/justskiv/gatekeeper/internal/config"
)

// New builds a slog.Logger from the configured level and format. The
// values are already validated by config.Load.
func New(cfg config.Config) *slog.Logger {
	return newWithWriter(cfg, os.Stdout, shouldColor(os.Stdout))
}

func newWithWriter(cfg config.Config, out io.Writer, color bool) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "text" {
		handler = newConsoleHandler(out, consoleHandlerOptions{
			Level: level,
			Color: color,
		})
	} else {
		handler = slog.NewJSONHandler(out, opts)
	}
	return slog.New(handler)
}
