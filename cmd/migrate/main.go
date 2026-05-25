// Command migrate is the operational entry point for SQLite schema
// changes. The gatekeeper binary itself never applies DDL — it only
// verifies that the schema is in place. Splitting the responsibility
// keeps the serve path free of migration logic.
//
// Usage:
//
//	migrate [--migrations-dir DIR] <up|status>
//
// The migrate CLI reads the same configuration as the bot, so DBPath
// (and every other validated value) come from the same source.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/applog"
	"github.com/justskiv/gatekeeper/internal/config"
	"github.com/justskiv/gatekeeper/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("migrations-dir", "./migrations",
		"directory containing goose SQL migrations")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	cmd := flag.Arg(0)

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := applog.New(cfg)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			logger.Error("close database", slog.Any("error", cerr))
		}
	}()

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(*dir))
	if err != nil {
		return fmt.Errorf("init goose provider: %w", err)
	}

	switch cmd {
	case "up":
		return cmdUp(ctx, provider, logger)
	case "status":
		return cmdStatus(ctx, provider, logger)
	default:
		usage()
		os.Exit(2)
	}
	return nil
}

func cmdUp(ctx context.Context, p *goose.Provider, log *slog.Logger) error {
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	if len(results) == 0 {
		log.Info("migrations: already up to date")
		return nil
	}
	for _, r := range results {
		log.Info("migration applied",
			slog.Int64("version", r.Source.Version),
			slog.String("source", r.Source.Path))
	}
	return nil
}

func cmdStatus(ctx context.Context, p *goose.Provider, log *slog.Logger) error {
	version, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	log.Info("schema version", slog.Int64("version", version))

	statuses, err := p.Status(ctx)
	if err != nil {
		return fmt.Errorf("read migration status: %w", err)
	}
	for _, s := range statuses {
		log.Info("migration",
			slog.Int64("version", s.Source.Version),
			slog.String("source", s.Source.Path),
			slog.String("state", string(s.State)))
	}
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr,
		"usage: migrate [--migrations-dir DIR] <up|status>")
}
