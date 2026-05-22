// Command gatekeeper is a self-hosted Telegram access-control bot.
//
// This is the foundation build: it loads and validates the
// configuration, opens the SQLite database, applies migrations and
// shuts down cleanly on a signal. Telegram transport and the domain
// logic are added by later phases.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/justskiv/gatekeeper/internal/config"
	"github.com/justskiv/gatekeeper/internal/store"
)

func main() {
	if err := run(); err != nil {
		// The logger may not be configured yet; stderr always works.
		fmt.Fprintln(os.Stderr, "fatal: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	// The database is closed last, once every writer has stopped.
	defer func() {
		if cerr := db.Close(); cerr != nil {
			logger.Error("failed to close database", slog.Any("error", cerr))
		}
	}()

	if err := store.Migrate(db); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("gatekeeper started",
		slog.String("db_path", cfg.DBPath),
		slog.String("telegram_mode", cfg.TelegramMode),
		slog.String("tribute_mode", cfg.TributeMode))

	// This phase runs no background subsystems — just wait for a
	// shutdown signal. Later phases start the poller, Enforcer and
	// Reconciler here under an errgroup.
	<-ctx.Done()

	logger.Info("shutdown signal received, stopping")
	return nil
}

// newLogger builds a slog.Logger from the configured level and format.
// The values are already validated by config.Load.
func newLogger(cfg *config.Config) *slog.Logger {
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
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}
