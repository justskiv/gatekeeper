// Command gatekeeper is a self-hosted Telegram access-control bot.
//
// Migrations are applied separately by the migrate CLI (see cmd/migrate);
// on a non-migrated database the bot fails fast with an instruction to
// run `task migrate:up`.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/justskiv/gatekeeper/internal/admission"
	"github.com/justskiv/gatekeeper/internal/applog"
	commandbot "github.com/justskiv/gatekeeper/internal/bot"
	"github.com/justskiv/gatekeeper/internal/config"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/enforcer"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/operatorlog"
	"github.com/justskiv/gatekeeper/internal/reconcile"
	"github.com/justskiv/gatekeeper/internal/source"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
	"github.com/justskiv/gatekeeper/internal/webhook"
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

//nolint:funlen,wsl_v5 // Startup wiring is intentionally linear.
func run() error {
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
	statusEngine := newStatusEngine(cfg, db, tgClient, sourceChats)

	if err := tgClient.SetMyCommands(ctx, cfg.OwnerTGIDs); err != nil {
		logger.Warn("failed to set bot commands", slog.Any("error", err))
	}

	notifier := notify.New(store.NewUsers(db), tgClient, logger)

	// Construct the operator event-log writer before transport starts and
	// thread it through every emit path (poller/router, reconciler, tribute).
	// It targets EVENT_LOG_CHAT_ID via durable send_dm and never falls back
	// to ADMIN_LOG_CHAT_ID or owner DMs.
	operatorWriter := operatorlog.New(
		cfg.EventLogChatID, messages.RenderOperatorEvent, logger)

	// The event-log chat is monitored by chat-health (posting ability, not
	// admin) but is never treated as a managed/observed resource. A startup
	// probe failure is non-fatal: it records an alert and the bot keeps
	// serving updates.
	healthChats := append(
		telegram.HealthChatsFromConfig(cfg), telegram.EventLogHealthChat(cfg))

	if err := checkStartupHealth(
		ctx, db, tgClient, notifier, healthChats, cfg.OwnerTGIDs, me.ID, logger,
	); err != nil {
		return err
	}

	runtime, runtimeCtx := startRuntime(
		ctx, db, cfg, tgClient, statusEngine, healthChats, operatorWriter, logger)

	if err := prepareInviteStartup(ctx, runtimeCtx, cfg, runtime); err != nil {
		runtime.stopAndWait()

		return err
	}

	if _, err := runtime.reconciler.RunOnce(runtimeCtx); err != nil {
		runtime.stopAndWait()

		return err
	}

	runtime.startPeriodic(runtimeCtx)

	poller := telegram.NewPoller(
		db, tgClient, notifier, healthChats, cfg.OwnerTGIDs, logger,
		telegram.WithPollerStatusEngine(statusEngine),
		telegram.WithPollerSourceChats(sourceChats),
		telegram.WithPollerAdmissionConfig(admissionConfig(cfg)),
		telegram.WithPollerAdminLogChatID(cfg.AdminLogChatID),
		telegram.WithPollerOperatorLog(operatorWriter),
		telegram.WithPollerChatRoles(chatRolesFromConfig(cfg)),
		telegram.WithPollerSubscribeLinks(messages.SubscribeLinks{
			Boosty:     cfg.BoostySubscribeURL,
			TributeRUB: cfg.TributeSubscribeURLRub,
			TributeEUR: cfg.TributeSubscribeURLEur,
		}))

	var telegramWebhookRegistered atomic.Bool
	if shouldStartHTTPServer(cfg) {
		server := newHTTPServer(
			db, cfg, statusEngine, poller, healthChats, operatorWriter,
			func() bool { return true },
			telegramWebhookRegistered.Load,
			logger,
		)
		runtime.group.Go(func() error {
			return server.Run(runtimeCtx)
		})
	}

	switch cfg.TelegramMode {
	case "polling":
		if err := tgClient.DeleteWebhook(ctx); err != nil {
			runtime.stopAndWait()

			return err
		}

		runtime.group.Go(func() error {
			return poller.Run(runtimeCtx)
		})
	case "webhook":
		if err := tgClient.SetWebhook(
			ctx,
			telegramWebhookURL(cfg),
			cfg.TelegramWebhookSecret,
			telegram.DefaultAllowedUpdates,
		); err != nil {
			runtime.stopAndWait()

			return err
		}

		telegramWebhookRegistered.Store(true)
	}

	logger.Info("gatekeeper started",
		slog.String("db_path", cfg.DBPath),
		slog.String("telegram_mode", cfg.TelegramMode),
		slog.String("tribute_mode", cfg.TributeMode))

	return waitRuntime(ctx, runtimeCtx, runtime, logger)
}

func waitRuntime(
	rootCtx context.Context,
	runtimeCtx context.Context,
	runtime runtimeGroup,
	logger *slog.Logger,
) error {
	groupDone := make(chan error, 1)
	go func() {
		groupDone <- runtime.group.Wait()
	}()

	select {
	case <-runtimeCtx.Done():
		if rootCtx.Err() != nil {
			logger.Info("shutdown signal received, stopping")
		}

		runtime.cancel()

		return <-groupDone
	case err := <-groupDone:
		runtime.cancel()

		return err
	}
}

func shouldStartHTTPServer(cfg config.Config) bool {
	return cfg.TributeMode == "webhook" ||
		cfg.TelegramMode == "webhook" ||
		cfg.MetricsEnabled
}

func newHTTPServer(
	db *sql.DB,
	cfg config.Config,
	statusEngine *engine.Engine,
	poller *telegram.Poller,
	healthChats []telegram.HealthChat,
	operatorWriter *operatorlog.Writer,
	getMeOK func() bool,
	telegramWebhookRegistered func() bool,
	logger *slog.Logger,
) *webhook.Server {
	var tributeHandler http.Handler
	if cfg.TributeMode == "webhook" {
		tributeHandler = &webhook.TributeHandler{
			DB:              db,
			APIKey:          cfg.TributeAPIKey,
			Engine:          statusEngine,
			CancelImmediate: cfg.TributeCancelIsImmediate,
			OwnerIDs:        cfg.OwnerTGIDs,
			AdminLogChatID:  cfg.AdminLogChatID,
			OperatorLog:     operatorWriter,
			Logger:          logger,
		}
	}

	var telegramHandler http.Handler
	if cfg.TelegramMode == "webhook" {
		telegramHandler = webhook.NewTelegramHandler(
			cfg.TelegramWebhookSecret, poller)
	}

	return webhook.NewServer(webhook.Config{
		ListenAddr:      cfg.WebhookListenAddr,
		MetricsEnabled:  cfg.MetricsEnabled,
		TributeEnabled:  cfg.TributeMode == "webhook",
		TelegramEnabled: cfg.TelegramMode == "webhook",
		TributePath:     cfg.TributeWebhookPath,
		TelegramPath:    cfg.TelegramWebhookPath,
		Readiness: webhook.Readiness{
			DB:                                 db,
			HealthKeys:                         healthKeys(healthChats),
			ReconcileInterval:                  cfg.ReconcileInterval,
			GetMeOK:                            getMeOK,
			RequireTelegramWebhookRegistration: cfg.TelegramMode == "webhook",
			TelegramWebhookRegistered:          telegramWebhookRegistered,
		},
		Metrics:  &webhook.Metrics{Ops: store.NewOps(db)},
		Tribute:  tributeHandler,
		Telegram: telegramHandler,
		Logger:   logger,
	})
}

func telegramWebhookURL(cfg config.Config) string {
	return strings.TrimRight(cfg.TelegramWebhookPublicURL, "/") +
		cfg.TelegramWebhookPath
}

func healthKeys(chats []telegram.HealthChat) []string {
	keys := make([]string, 0, len(chats))
	for _, chat := range chats {
		// The operator event-log feed is observability: its health is
		// recorded and alerted, but it MUST NOT gate readiness of the
		// access-control core.
		if chat.PostingOnly {
			continue
		}

		keys = append(keys, chat.Key)
	}

	return keys
}

func chatRolesFromConfig(cfg config.Config) []commandbot.ChatRole {
	roles := []commandbot.ChatRole{
		{Role: "Boosty group", ChatID: cfg.BoostyGroupID},
		{Role: "Tribute channel", ChatID: cfg.TributeChannelID},
		{Role: "club chat", ChatID: cfg.ClubChatID},
		{Role: "club channel", ChatID: cfg.ClubChannelID},
	}

	if cfg.AdminLogChatID != nil {
		roles = append(roles, commandbot.ChatRole{
			Role:   "admin log chat",
			ChatID: *cfg.AdminLogChatID,
		})
	}

	return roles
}

func checkStartupHealth(
	ctx context.Context,
	db *sql.DB,
	tgClient *telegram.Client,
	notifier *notify.Notifier,
	healthChats []telegram.HealthChat,
	ownerIDs []int64,
	botID int64,
	logger *slog.Logger,
) error {
	return telegram.CheckStartupHealth(
		ctx, db, tgClient, notifier, healthChats, ownerIDs, botID, logger,
	)
}

func newStatusEngine(
	cfg config.Config,
	db store.DBTX,
	tgClient *telegram.Client,
	sourceChats telegram.SourceChats,
) *engine.Engine {
	sources := []engine.SubscriptionSource{
		source.NewMembership(domain.PlatformBoosty, cfg.BoostyGroupID, tgClient),
	}

	if cfg.TributeMode == "webhook" {
		sources = append(sources, source.NewMembership(
			domain.PlatformTribute,
			cfg.TributeChannelID,
			tgClient,
			source.WithLedger(store.NewSubscriptions(db)),
		))
	} else if sourceChats.TributeObservation {
		sources = append(sources, source.NewMembership(
			domain.PlatformTribute,
			cfg.TributeChannelID,
			tgClient,
			source.WithLedger(store.NewSubscriptions(db)),
		))
	}

	sources = append(sources,
		source.NewManual(store.NewWhitelist(db), store.NewSubscriptions(db)))

	return engine.New(sources, engine.WithRevocationConfig(
		engine.RevocationConfig{
			ExpiryMode:  cfg.ExpiryMode,
			GracePeriod: cfg.GracePeriod,
		},
	))
}

func admissionConfig(cfg config.Config) admission.Config {
	return admission.Config{
		InviteMode:         domain.InviteMode(cfg.InviteMode),
		FallbackMaxAge:     cfg.AdmissionFallbackMaxAge,
		ClubChatID:         cfg.ClubChatID,
		ClubChannelID:      cfg.ClubChannelID,
		JoinRequestRetries: cfg.AdmissionJoinRequestRetries,
		Resources: []admission.ResourceConfig{
			{Resource: domain.ResourceChat, ChatID: cfg.ClubChatID},
			{Resource: domain.ResourceChannel, ChatID: cfg.ClubChannelID},
		},
	}
}

type runtimeGroup struct {
	group         *errgroup.Group
	cancel        context.CancelFunc
	outbox        *store.Outbox
	alerts        *store.Alerts
	inviteChecker sharedInviteReadiness
	reconciler    *reconcile.Reconciler
	cleanup       *reconcile.CleanupService
}

func startRuntime(
	ctx context.Context,
	db *sql.DB,
	cfg config.Config,
	tgClient *telegram.Client,
	statusEngine *engine.Engine,
	healthChats []telegram.HealthChat,
	operatorWriter *operatorlog.Writer,
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
		Outbox:        outbox,
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Audit:         store.NewAudit(db),
		Revocations:   store.NewRevocations(db),
		Whitelist:     store.NewWhitelist(db),
		Alerts: store.NewAlertsWithDelivery(
			db, outbox, cfg.OwnerTGIDs, cfg.AdminLogChatID),
		StatusEngine: statusEngine,
		OperatorLog:  operatorWriter,
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

	reconcileCfg := reconcileConfig(cfg)
	reconciler := reconcile.New(
		db,
		statusEngine,
		inviteService,
		reconcileCfg,
		logger,
		reconcile.WithHealthCheck(func(ctx context.Context) error {
			return telegram.CheckStartupHealth(
				ctx,
				db,
				tgClient,
				notify.NewDurable(store.NewUsers(db), outbox, logger),
				healthChats,
				cfg.OwnerTGIDs,
				tgClient.BotID(),
				logger,
			)
		}),
		reconcile.WithMemberChecker(telegram.NewClubMemberChecker(
			tgClient, cfg.ClubChatID, cfg.ClubChannelID)),
		reconcile.WithOperatorLog(operatorWriter),
	)

	return runtimeGroup{
		group:  group,
		cancel: cancelRuntime,
		outbox: outbox,
		alerts: store.NewAlertsWithDelivery(
			db, outbox, cfg.OwnerTGIDs, cfg.AdminLogChatID),
		inviteChecker: inviteService,
		reconciler:    reconciler,
		cleanup:       reconcile.NewCleanupService(db, reconcileCfg, logger),
	}, groupCtx
}

func (r runtimeGroup) stopAndWait() {
	r.cancel()
	_ = r.group.Wait()
}

func (r runtimeGroup) startPeriodic(ctx context.Context) {
	r.group.Go(func() error {
		return r.reconciler.Run(ctx)
	})
	r.group.Go(func() error {
		return r.cleanup.Run(ctx)
	})
}

func reconcileConfig(cfg config.Config) reconcile.Config {
	return reconcile.Config{
		Interval:        cfg.ReconcileInterval,
		CleanupInterval: cfg.CleanupInterval,
		RawRetention:    cfg.RawRetention,
		AuditRetention:  cfg.AuditRetention,
		InviteMode:      domain.InviteMode(cfg.InviteMode),
		OwnerIDs:        cfg.OwnerTGIDs,
		AdminLogChatID:  cfg.AdminLogChatID,
		Sources: []reconcile.SourceChat{
			{
				Platform: domain.PlatformBoosty,
				ChatID:   cfg.BoostyGroupID,
				Enabled:  true,
			},
			{
				Platform: domain.PlatformTribute,
				ChatID:   cfg.TributeChannelID,
				Enabled:  cfg.TributeMode == "observation",
			},
		},
		Resources: []reconcile.ResourceChat{
			{Resource: domain.ResourceChat, ChatID: cfg.ClubChatID},
			{Resource: domain.ResourceChannel, ChatID: cfg.ClubChannelID},
		},
	}
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
