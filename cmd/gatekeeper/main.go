// Command gatekeeper is a self-hosted Telegram access-control bot.
//
// Migrations are applied separately by the migrate CLI (see cmd/migrate);
// on a non-migrated database the bot fails fast with an instruction to
// run `task migrate:up`.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/justskiv/gatekeeper/internal/applog"
	"github.com/justskiv/gatekeeper/internal/config"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/enforcer"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/source"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
	"golang.org/x/sync/errgroup"
)

const startupInviteTimeout = 30 * time.Second

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
		return errors.New("telegram webhook mode is not implemented in this phase")
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

	runtime, runtimeCtx := startRuntime(ctx, db, cfg, tgClient, logger)

	if err := prepareInviteStartup(ctx, runtimeCtx, cfg, runtime); err != nil {
		runtime.stopAndWait()

		return err
	}

	poller := telegram.NewPoller(
		db, tgClient, notifier, healthChats, cfg.OwnerTGIDs, logger,
		telegram.WithPollerStatusEngine(statusEngine),
		telegram.WithPollerSourceChats(sourceChats))

	runtime.group.Go(func() error {
		return poller.Run(runtimeCtx)
	})

	logger.Info("gatekeeper started",
		slog.String("db_path", cfg.DBPath),
		slog.String("telegram_mode", cfg.TelegramMode),
		slog.String("tribute_mode", cfg.TributeMode))

	<-runtimeCtx.Done()

	if ctx.Err() != nil {
		logger.Info("shutdown signal received, stopping")
	}

	runtime.cancel()

	if err := runtime.group.Wait(); err != nil {
		return err
	}

	return nil
}

type runtimeGroup struct {
	group         *errgroup.Group
	cancel        context.CancelFunc
	outbox        *store.Outbox
	alerts        *store.Alerts
	inviteChecker sharedInviteReadiness
}

func startRuntime(
	ctx context.Context,
	db *sql.DB,
	cfg config.Config,
	tgClient *telegram.Client,
	logger *slog.Logger,
) (runtimeGroup, context.Context) {
	outbox := store.NewOutbox(db)
	invites := store.NewInvites(db)
	inviteService := invite.New(tgClient, invites, invite.Config{
		Mode:          domain.InviteMode(cfg.InviteMode),
		TTL:           cfg.InviteTTL,
		ClubChatID:    cfg.ClubChatID,
		ClubChannelID: cfg.ClubChannelID,
	}, invite.WithLogger(logger))

	enforcerRunner := enforcer.New(enforcer.Stores{
		Outbox: outbox,
		Users:  store.NewUsers(db),
		Alerts: store.NewAlerts(db),
	}, tgClient, inviteService, enforcer.Config{
		Workers:       cfg.EnforcerWorkers,
		ClubChatID:    cfg.ClubChatID,
		ClubChannelID: cfg.ClubChannelID,
	}, enforcer.WithLogger(logger))

	runtimeCtx, cancelRuntime := context.WithCancel(ctx)
	group, groupCtx := errgroup.WithContext(runtimeCtx)

	group.Go(func() error {
		return enforcerRunner.Run(groupCtx)
	})

	return runtimeGroup{
		group:         group,
		cancel:        cancelRuntime,
		outbox:        outbox,
		alerts:        store.NewAlerts(db),
		inviteChecker: inviteService,
	}, groupCtx
}

func (r runtimeGroup) stopAndWait() {
	r.cancel()
	_ = r.group.Wait()
}

func prepareInviteStartup(
	ctx context.Context,
	runtimeCtx context.Context,
	cfg config.Config,
	runtime runtimeGroup,
) error {
	switch cfg.InviteMode {
	case string(domain.InviteDirect):
		return createDirectModeAlert(ctx, runtime.alerts)
	case string(domain.InviteSharedJoinRequest):
		if err := enqueueSharedInvites(ctx, runtime.outbox); err != nil {
			return err
		}

		return waitForSharedInvites(
			runtimeCtx, runtime.inviteChecker, startupInviteTimeout)
	default:
		return nil
	}
}

type sharedInviteReadiness interface {
	ActiveShared(
		ctx context.Context,
		resource domain.Resource,
	) (domain.InviteLink, bool, error)
}

func createDirectModeAlert(ctx context.Context, alerts *store.Alerts) error {
	_, _, err := alerts.CreateOpenIfMissing(ctx, store.AlertInput{
		Severity: "warning",
		Kind:     "invite_mode_degraded",
		Title:    "invite mode degraded",
		Detail:   "INVITE_MODE=direct is enabled",
	})

	return err
}

func enqueueSharedInvites(ctx context.Context, outbox *store.Outbox) error {
	for _, resource := range []domain.Resource{
		domain.ResourceChat,
		domain.ResourceChannel,
	} {
		if err := enqueueSharedInvite(ctx, outbox, resource); err != nil {
			return err
		}
	}

	return nil
}

func enqueueSharedInvite(
	ctx context.Context,
	outbox *store.Outbox,
	resource domain.Resource,
) error {
	_, _, err := outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     domain.ActionEnsureInvite,
		Resource: &resource,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionEnsureInvite,
			nil,
			&resource,
			"startup:shared_join_request:"+string(resource),
		),
		PayloadJSON: []byte(`{}`),
	})

	return err
}

func waitForSharedInvites(
	ctx context.Context,
	readiness sharedInviteReadiness,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		ok, err := sharedInvitesReady(ctx, readiness)
		if err != nil {
			return err
		}

		if ok {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for shared invite links: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func sharedInvitesReady(
	ctx context.Context,
	readiness sharedInviteReadiness,
) (bool, error) {
	for _, resource := range []domain.Resource{
		domain.ResourceChat,
		domain.ResourceChannel,
	} {
		link, ok, err := readiness.ActiveShared(ctx, resource)
		if err != nil {
			return false, err
		}

		if !ok || !link.CreatesJoinRequest {
			return false, nil
		}
	}

	return true, nil
}
