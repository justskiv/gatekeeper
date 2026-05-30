package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunFailsFastForTelegramWebhookMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gatekeeper.db")

	env := validEnv(dbPath)
	env["TELEGRAM_MODE"] = "webhook"
	env["TELEGRAM_WEBHOOK_PUBLIC_URL"] = "https://bot.example.com"
	env["TELEGRAM_WEBHOOK_SECRET"] = "secret"
	for key, value := range env {
		t.Setenv(key, value)
	}

	err := run()
	if err == nil {
		t.Fatal("run returned nil, want webhook not implemented error")
	}
	if !strings.Contains(err.Error(), "telegram webhook mode is not implemented") {
		t.Fatalf("run error = %v, want webhook not implemented", err)
	}
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("webhook mode touched db path: %v", statErr)
	}
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
