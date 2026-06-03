package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/config"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

func TestRunFailsFastForDirectWithoutAllowBeforeDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gatekeeper.db")

	env := validEnv(dbPath)
	env["INVITE_MODE"] = "direct"

	env["ALLOW_DIRECT_INVITES"] = "false"
	for key, value := range env {
		t.Setenv(key, value)
	}

	err := run()
	require.Error(t, err, "run must reject direct invites without the allow flag")
	assert.Contains(t, err.Error(), "ALLOW_DIRECT_INVITES")

	_, statErr := os.Stat(dbPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist,
		"invalid direct config must not touch the db path")
}

func TestShouldStartHTTPServer(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{"default polling", config.Config{
			TelegramMode: "polling",
			TributeMode:  "observation",
		}, false},
		{"metrics", config.Config{
			TelegramMode:   "polling",
			TributeMode:    "observation",
			MetricsEnabled: true,
		}, true},
		{"tribute webhook", config.Config{
			TelegramMode: "polling",
			TributeMode:  "webhook",
		}, true},
		{"telegram webhook", config.Config{
			TelegramMode: "webhook",
			TributeMode:  "observation",
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldStartHTTPServer(tt.cfg))
		})
	}
}

func TestCreateDirectModeAlert(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	require.NoError(t, createDirectModeAlert(ctx, store.NewAlerts(db)),
		"first createDirectModeAlert")
	require.NoError(t, createDirectModeAlert(ctx, store.NewAlerts(db)),
		"second createDirectModeAlert")

	var alerts int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM admin_alerts
		WHERE kind = 'invite_mode_degraded'`,
	).Scan(&alerts), "count alerts")
	assert.Equal(t, 1, alerts, "only one open degraded alert must exist")
}

func TestEnqueueSharedInvitesIsIdempotent(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	outbox := store.NewOutbox(db)

	require.NoError(t, enqueueSharedInvites(ctx, outbox), "first enqueueSharedInvites")
	require.NoError(t, enqueueSharedInvites(ctx, outbox), "second enqueueSharedInvites")

	var actions int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM access_actions
		WHERE action_type = 'ensure_invite'`,
	).Scan(&actions), "count actions")
	assert.Equal(t, 2, actions, "expected chat and channel ensure_invite actions")
}

func TestSharedInvitesReadyRequiresChatAndChannel(t *testing.T) {
	ctx := context.Background()
	readiness := fakeReadiness{
		domain.ResourceChat: {
			CreatesJoinRequest: true,
		},
	}

	ok, err := sharedInvitesReady(ctx, readiness)
	require.NoError(t, err, "sharedInvitesReady")
	assert.False(t, ok, "readiness must fail with the channel link missing")

	readiness[domain.ResourceChannel] = domain.InviteLink{
		CreatesJoinRequest: true,
	}

	ok, err = sharedInvitesReady(ctx, readiness)
	require.NoError(t, err, "sharedInvitesReady complete")
	assert.True(t, ok, "readiness must succeed with both links present")
}

func TestRuntimeInitialReconcileAndGracefulShutdown(t *testing.T) {
	db := testutil.NewDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	client, err := telegram.NewClient("123:ABC")
	require.NoError(t, err, "NewClient")

	runtime, runtimeCtx := startRuntime(
		ctx,
		db,
		runtimeTestConfig(),
		client,
		engine.New(nil),
		nil,
		slog.Default(),
	)

	_, err = runtime.reconciler.RunOnce(runtimeCtx)
	if err != nil {
		runtime.stopAndWait()
	}

	require.NoError(t, err, "initial RunOnce")

	_, ok, err := store.NewMeta(db).Get(context.Background(), "reconcile.last_run_at")
	if err != nil || !ok {
		runtime.stopAndWait()
	}

	require.NoError(t, err, "Get last_run_at")
	require.True(t, ok, "last_run_at must be present after initial reconcile")

	runtime.startPeriodic(runtimeCtx)
	runtime.stopAndWait()

	select {
	case <-runtimeCtx.Done():
	default:
		t.Fatal("runtime context is not cancelled after stopAndWait")
	}
}

type fakeReadiness map[domain.Resource]domain.InviteLink

func (r fakeReadiness) ActiveShared(
	_ context.Context,
	resource domain.Resource,
) (domain.InviteLink, bool, error) {
	link, ok := r[resource]

	return link, ok, nil
}

func validEnv(dbPath string) map[string]string {
	return map[string]string{
		"BOT_TOKEN":                   "123456:ABC-DEF",
		"OWNER_TG_IDS":                "11111111,22222222",
		"DB_PATH":                     dbPath,
		"BOOSTY_GROUP_ID":             "-1001111111111",
		"TRIBUTE_CHANNEL_ID":          "-1002222222222",
		"CLUB_CHAT_ID":                "-1003333333333",
		"CLUB_CHANNEL_ID":             "-1004444444444",
		"ADMIN_LOG_CHAT_ID":           "",
		"BOOSTY_SUBSCRIBE_URL":        "https://boosty.to/author",
		"TRIBUTE_SUBSCRIBE_URL":       "https://t.me/tribute/app",
		"INVITE_MODE":                 "shared_join_request",
		"INVITE_TTL":                  "24h",
		"ALLOW_DIRECT_INVITES":        "false",
		"TRIBUTE_MODE":                "observation",
		"TRIBUTE_API_KEY":             "",
		"TRIBUTE_CANCEL_IS_IMMEDIATE": "false",
		"WEBHOOK_LISTEN_ADDR":         ":8080",
		"TRIBUTE_WEBHOOK_PATH":        "/webhooks/tribute",
		"TELEGRAM_MODE":               "polling",
		"TELEGRAM_WEBHOOK_PUBLIC_URL": "",
		"TELEGRAM_WEBHOOK_PATH":       "/webhooks/telegram",
		"TELEGRAM_WEBHOOK_SECRET":     "",
		"EXPIRY_MODE":                 "grace",
		"GRACE_PERIOD":                "72h",
		"RECONCILE_INTERVAL":          "1h",
		"CLEANUP_INTERVAL":            "24h",
		"RAW_RETENTION":               "720h",
		"AUDIT_RETENTION":             "8760h",
		"ENFORCER_WORKERS":            "2",
		"TIMEZONE":                    "UTC",
		"LOG_LEVEL":                   "info",
		"LOG_FORMAT":                  "json",
		"METRICS_ENABLED":             "false",
	}
}

func runtimeTestConfig() config.Config {
	return config.Config{
		OwnerTGIDs:        []int64{11111111},
		BoostyGroupID:     -1001111111111,
		TributeChannelID:  -1002222222222,
		ClubChatID:        -1003333333333,
		ClubChannelID:     -1004444444444,
		InviteMode:        string(domain.InviteDirect),
		InviteTTL:         time.Hour,
		ExpiryMode:        "grace",
		GracePeriod:       time.Hour,
		ReconcileInterval: time.Hour,
		CleanupInterval:   time.Hour,
		RawRetention:      24 * time.Hour,
		AuditRetention:    24 * time.Hour,
		EnforcerWorkers:   1,
	}
}
