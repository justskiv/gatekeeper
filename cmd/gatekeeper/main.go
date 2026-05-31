// Command gatekeeper is a self-hosted Telegram access-control bot.
//
// Migrations are applied separately by the migrate CLI (see cmd/migrate);
// on a non-migrated database the bot fails fast with an instruction to
// run `task migrate:up`.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/justskiv/gatekeeper/internal/applog"
	"github.com/justskiv/gatekeeper/internal/config"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/source"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
	"golang.org/x/sync/errgroup"
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

	logger := applog.New(cfg)
	slog.SetDefault(logger)

	if cfg.TelegramMode == "webhook" {
		return fmt.Errorf("telegram webhook mode is not implemented in this phase")
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	// The database is closed last, once every writer has stopped.
	defer func() {
		if cerr := db.Close(); cerr != nil {
			logger.Error("failed to close database", slog.Any("error", cerr))
		}
	}()

	if err := store.CheckSchema(ctx, db); err != nil {
		return err
	}

	tgClient, err := telegram.NewClient(cfg.BotToken)
	if err != nil {
		return err
	}
	me, err := tgClient.GetMe(ctx)
	if err != nil {
		return err
	}
	logger.Info("getMe ok",
		slog.Int64("bot_id", me.ID),
		slog.String("username", me.Username))

	sourceChats := telegram.SourceChats{
		BoostyGroupID:      cfg.BoostyGroupID,
		TributeChannelID:   cfg.TributeChannelID,
		TributeObservation: cfg.TributeMode == "observation",
	}
	sources := []engine.SubscriptionSource{
		source.NewMembership(domain.PlatformBoosty, cfg.BoostyGroupID, tgClient),
	}
	if sourceChats.TributeObservation {
		sources = append(sources, source.NewMembership(
			domain.PlatformTribute,
			cfg.TributeChannelID,
			tgClient,
			source.WithLedger(store.NewSubscriptions(db)),
		))
	}
	sources = append(sources,
		source.NewManual(store.NewWhitelist(db), store.NewSubscriptions(db)))
	statusEngine := engine.New(sources)

	if err := tgClient.SetMyCommands(ctx, cfg.OwnerTGIDs); err != nil {
		logger.Warn("failed to set bot commands", slog.Any("error", err))
	}

	notifier := notify.New(store.NewUsers(db), tgClient, logger)
	healthChats := telegram.HealthChatsFromConfig(cfg)
	if err := telegram.CheckStartupHealth(
		ctx, db, tgClient, notifier, healthChats, cfg.OwnerTGIDs, me.ID, logger,
	); err != nil {
		return err
	}

	group, groupCtx := errgroup.WithContext(ctx)
	poller := telegram.NewPoller(
		db, tgClient, notifier, healthChats, cfg.OwnerTGIDs, logger,
		telegram.WithPollerStatusEngine(statusEngine),
		telegram.WithPollerSourceChats(sourceChats))
	group.Go(func() error {
		return poller.Run(groupCtx)
	})

	logger.Info("gatekeeper started",
		slog.String("db_path", cfg.DBPath),
		slog.String("telegram_mode", cfg.TelegramMode),
		slog.String("tribute_mode", cfg.TributeMode))

	<-groupCtx.Done()

	if ctx.Err() != nil {
		logger.Info("shutdown signal received, stopping")
	}
	if err := group.Wait(); err != nil {
		return err
	}
	return nil
}
