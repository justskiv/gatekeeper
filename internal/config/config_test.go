package config

import "testing"

// baseEnv is a complete, valid environment. Every variable the loader
// reads is set explicitly so the tests do not depend on the ambient
// shell environment.
func baseEnv() map[string]string {
	return map[string]string{
		"BOT_TOKEN":                   "123456:ABC-DEF",
		"OWNER_TG_IDS":                "11111111,22222222",
		"DB_PATH":                     "./data/gatekeeper.db",
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

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		mutate  map[string]string // keys to override (empty value clears)
		wantErr bool
	}{
		{"valid baseline", nil, false},
		{"missing BOT_TOKEN", map[string]string{"BOT_TOKEN": ""}, true},
		{"missing required chat ID", map[string]string{"CLUB_CHAT_ID": ""}, true},
		{"non-negative chat ID", map[string]string{"CLUB_CHAT_ID": "1003333333333"}, true},
		{"non-integer chat ID", map[string]string{"CLUB_CHAT_ID": "not-a-number"}, true},
		{"duplicate chat IDs", map[string]string{"CLUB_CHANNEL_ID": "-1003333333333"}, true},
		{"invalid owner IDs", map[string]string{"OWNER_TG_IDS": "abc"}, true},
		{"direct without allow flag", map[string]string{"INVITE_MODE": "direct"}, true},
		{
			"direct with allow flag",
			map[string]string{"INVITE_MODE": "direct", "ALLOW_DIRECT_INVITES": "true"},
			false,
		},
		{"tribute webhook without API key", map[string]string{"TRIBUTE_MODE": "webhook"}, true},
		{
			"tribute webhook with API key",
			map[string]string{"TRIBUTE_MODE": "webhook", "TRIBUTE_API_KEY": "secret"},
			false,
		},
		{"telegram webhook without secrets", map[string]string{"TELEGRAM_MODE": "webhook"}, true},
		{
			"telegram webhook with secrets",
			map[string]string{
				"TELEGRAM_MODE":               "webhook",
				"TELEGRAM_WEBHOOK_PUBLIC_URL": "https://bot.example.com",
				"TELEGRAM_WEBHOOK_SECRET":     "long-secret",
			},
			false,
		},
		{"invalid duration", map[string]string{"GRACE_PERIOD": "abc"}, true},
		{"non-positive duration", map[string]string{"INVITE_TTL": "0s"}, true},
		{"invalid timezone", map[string]string{"TIMEZONE": "Mars/Olympus"}, true},
		{"invalid invite mode", map[string]string{"INVITE_MODE": "carrier-pigeon"}, true},
		{"invalid expiry mode", map[string]string{"EXPIRY_MODE": "whenever"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			for k, v := range tt.mutate {
				env[k] = v
			}
			for k, v := range env {
				t.Setenv(k, v)
			}

			cfg, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (cfg=%+v)", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg == nil {
				t.Fatal("expected a config, got nil")
			}
		})
	}
}

func TestLoadParsesValues(t *testing.T) {
	for k, v := range baseEnv() {
		t.Setenv(k, v)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.BotToken != "123456:ABC-DEF" {
		t.Errorf("BotToken = %q", cfg.BotToken)
	}
	if len(cfg.OwnerTGIDs) != 2 || cfg.OwnerTGIDs[0] != 11111111 {
		t.Errorf("OwnerTGIDs = %v", cfg.OwnerTGIDs)
	}
	if cfg.ClubChatID != -1003333333333 {
		t.Errorf("ClubChatID = %d", cfg.ClubChatID)
	}
	if cfg.GracePeriod.Hours() != 72 {
		t.Errorf("GracePeriod = %v", cfg.GracePeriod)
	}
	if cfg.Location == nil || cfg.Location.String() != "UTC" {
		t.Errorf("Location = %v", cfg.Location)
	}
}

func TestLoadCapsDirectInviteTTL(t *testing.T) {
	for k, v := range baseEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("INVITE_MODE", "direct")
	t.Setenv("ALLOW_DIRECT_INVITES", "true")
	t.Setenv("INVITE_TTL", "24h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.InviteTTL.Hours() != 1 {
		t.Errorf("InviteTTL not capped to 1h for direct mode: %v", cfg.InviteTTL)
	}
}
