package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/config"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
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
	if err == nil {
		t.Fatal("run returned nil, want direct invite config error")
	}

	if !strings.Contains(err.Error(), "ALLOW_DIRECT_INVITES") {
		t.Fatalf("run error = %v, want ALLOW_DIRECT_INVITES", err)
	}

	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid direct config touched db path: %v", statErr)
	}
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
			if got := shouldStartHTTPServer(tt.cfg); got != tt.want {
				t.Fatalf("shouldStartHTTPServer = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateDirectModeAlert(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := createDirectModeAlert(ctx, store.NewAlerts(db)); err != nil {
		t.Fatalf("createDirectModeAlert: %v", err)
	}

	if err := createDirectModeAlert(ctx, store.NewAlerts(db)); err != nil {
		t.Fatalf("createDirectModeAlert second call: %v", err)
	}

	var alerts int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM admin_alerts
		WHERE kind = 'invite_mode_degraded'`,
	).Scan(&alerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}

	if alerts != 1 {
		t.Fatalf("alerts = %d, want one open degraded alert", alerts)
	}
}

func TestEnqueueSharedInvitesIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	outbox := store.NewOutbox(db)

	if err := enqueueSharedInvites(ctx, outbox); err != nil {
		t.Fatalf("enqueueSharedInvites: %v", err)
	}

	if err := enqueueSharedInvites(ctx, outbox); err != nil {
		t.Fatalf("enqueueSharedInvites second call: %v", err)
	}

	var actions int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM access_actions
		WHERE action_type = 'ensure_invite'`,
	).Scan(&actions); err != nil {
		t.Fatalf("count actions: %v", err)
	}

	if actions != 2 {
		t.Fatalf("ensure_invite actions = %d, want chat and channel", actions)
	}
}

func TestSharedInvitesReadyRequiresChatAndChannel(t *testing.T) {
	ctx := context.Background()
	readiness := fakeReadiness{
		domain.ResourceChat: {
			CreatesJoinRequest: true,
		},
	}

	ok, err := sharedInvitesReady(ctx, readiness)
	if err != nil {
		t.Fatalf("sharedInvitesReady: %v", err)
	}

	if ok {
		t.Fatal("shared readiness succeeded with channel missing")
	}

	readiness[domain.ResourceChannel] = domain.InviteLink{
		CreatesJoinRequest: true,
	}

	ok, err = sharedInvitesReady(ctx, readiness)
	if err != nil {
		t.Fatalf("sharedInvitesReady complete: %v", err)
	}

	if !ok {
		t.Fatal("shared readiness failed with both links present")
	}
}

func TestRuntimeInitialReconcileAndGracefulShutdown(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	client, err := telegram.NewClient("123:ABC")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	runtime, runtimeCtx := startRuntime(
		ctx,
		db,
		runtimeTestConfig(),
		client,
		engine.New(nil),
		nil,
		slog.Default(),
	)

	if _, err := runtime.reconciler.RunOnce(runtimeCtx); err != nil {
		runtime.stopAndWait()
		t.Fatalf("initial RunOnce: %v", err)
	}

	if _, ok, err := store.NewMeta(db).Get(
		context.Background(), "reconcile.last_run_at",
	); err != nil || !ok {
		runtime.stopAndWait()
		t.Fatalf("last_run_at = (_, %v, %v), want present", ok, err)
	}

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

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(migrationsDir(t)))
	if err != nil {
		t.Fatalf("new goose provider: %v", err)
	}

	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return db
}

func migrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
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
