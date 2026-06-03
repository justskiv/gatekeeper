package config

import (
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baseEnv is a complete, valid environment. Every variable the loader
// reads is set explicitly so the tests do not depend on the ambient
// shell environment.
func baseEnv() map[string]string {
	return map[string]string{
		"BOT_TOKEN":                      "123456:ABC-DEF",
		"OWNER_TG_IDS":                   "11111111,22222222",
		"DB_PATH":                        "./data/gatekeeper.db",
		"BOOSTY_GROUP_ID":                "-1001111111111",
		"TRIBUTE_CHANNEL_ID":             "-1002222222222",
		"CLUB_CHAT_ID":                   "-1003333333333",
		"CLUB_CHANNEL_ID":                "-1004444444444",
		"ADMIN_LOG_CHAT_ID":              "",
		"EVENT_LOG_CHAT_ID":              "-1006666666666",
		"BOOSTY_SUBSCRIBE_URL":           "https://boosty.to/author",
		"TRIBUTE_SUBSCRIBE_URL":          "https://t.me/tribute/app",
		"INVITE_MODE":                    "shared_join_request",
		"INVITE_TTL":                     "24h",
		"ALLOW_DIRECT_INVITES":           "false",
		"ADMISSION_FALLBACK_MAX_AGE":     "1h",
		"ADMISSION_JOIN_REQUEST_RETRIES": "2",
		"TRIBUTE_MODE":                   "observation",
		"TRIBUTE_API_KEY":                "",
		"TRIBUTE_CANCEL_IS_IMMEDIATE":    "false",
		"WEBHOOK_LISTEN_ADDR":            ":8080",
		"TRIBUTE_WEBHOOK_PATH":           "/webhooks/tribute",
		"TELEGRAM_MODE":                  "polling",
		"TELEGRAM_WEBHOOK_PUBLIC_URL":    "",
		"TELEGRAM_WEBHOOK_PATH":          "/webhooks/telegram",
		"TELEGRAM_WEBHOOK_SECRET":        "",
		"EXPIRY_MODE":                    "grace",
		"GRACE_PERIOD":                   "72h",
		"RECONCILE_INTERVAL":             "1h",
		"CLEANUP_INTERVAL":               "24h",
		"RAW_RETENTION":                  "720h",
		"AUDIT_RETENTION":                "8760h",
		"ENFORCER_WORKERS":               "2",
		"TIMEZONE":                       "UTC",
		"LOG_LEVEL":                      "info",
		"LOG_FORMAT":                     "json",
		"METRICS_ENABLED":                "false",
	}
}

// lookup adapts an environment map to the func(string) (string, bool)
// signature LoadFromLookup expects, so tests never touch process state.
func lookup(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]

		return v, ok
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
		{"admin log collides with club chat",
			map[string]string{"ADMIN_LOG_CHAT_ID": "-1003333333333"}, true},
		{
			"source and club chat conflict",
			map[string]string{"CLUB_CHAT_ID": "-1001111111111"},
			true,
		},
		{"invalid owner IDs", map[string]string{"OWNER_TG_IDS": "abc"}, true},
		{"direct without allow flag", map[string]string{"INVITE_MODE": "direct"}, true},
		{
			"direct with allow flag",
			map[string]string{
				"INVITE_MODE":          "direct",
				"ALLOW_DIRECT_INVITES": "true",
				"INVITE_TTL":           "1h",
			},
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
		{
			"non-positive admission fallback",
			map[string]string{"ADMISSION_FALLBACK_MAX_AGE": "0s"},
			true,
		},
		{
			"negative admission join request retries",
			map[string]string{"ADMISSION_JOIN_REQUEST_RETRIES": "-1"},
			true,
		},
		{"invalid timezone", map[string]string{"TIMEZONE": "Mars/Olympus"}, true},
		{"invalid invite mode", map[string]string{"INVITE_MODE": "carrier-pigeon"}, true},
		{"invalid expiry mode", map[string]string{"EXPIRY_MODE": "whenever"}, true},
		{"invalid log format", map[string]string{"LOG_FORMAT": "yaml"}, true},
		{"non-boolean flag", map[string]string{"ALLOW_DIRECT_INVITES": "maybe"}, true},
		{"non-positive owner ID", map[string]string{"OWNER_TG_IDS": "-5"}, true},
		{"non-positive enforcer workers", map[string]string{"ENFORCER_WORKERS": "0"}, true},
		{"invalid subscribe URL", map[string]string{"BOOSTY_SUBSCRIBE_URL": "not-a-url"}, true},
		{"webhook path without leading slash",
			map[string]string{"TRIBUTE_WEBHOOK_PATH": "webhooks/tribute"}, true},
		{"required var with only whitespace",
			map[string]string{"BOT_TOKEN": "   "}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			maps.Copy(env, tt.mutate)

			cfg, err := LoadFromLookup(lookup(env))
			if tt.wantErr {
				require.Error(t, err, "expected error, got cfg=%+v", cfg)

				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, cfg.BotToken, "expected a populated config")
		})
	}
}

func TestLoadParsesValues(t *testing.T) {
	cfg, err := LoadFromLookup(lookup(baseEnv()))
	require.NoError(t, err)

	assert.Equal(t, "123456:ABC-DEF", cfg.BotToken)
	assert.Equal(t, []int64{11111111, 22222222}, cfg.OwnerTGIDs)
	assert.Equal(t, int64(-1003333333333), cfg.ClubChatID)
	assert.Nil(t, cfg.AdminLogChatID, "AdminLogChatID must be nil when unset")
	assert.Equal(t, 72*time.Hour, cfg.GracePeriod)
	assert.Equal(t, time.Hour, cfg.AdmissionFallbackMaxAge)
	assert.Equal(t, 2, cfg.AdmissionJoinRequestRetries)
	assert.False(t, cfg.TributeCancelIsImmediate, "default must be false")

	require.NotNil(t, cfg.Location)
	assert.Equal(t, "UTC", cfg.Location.String())
}

func TestLoadParsesTributeCancelOverride(t *testing.T) {
	env := baseEnv()
	env["TRIBUTE_CANCEL_IS_IMMEDIATE"] = "true"

	cfg, err := LoadFromLookup(lookup(env))
	require.NoError(t, err)
	assert.True(t, cfg.TributeCancelIsImmediate)
}

func TestLoadRejectsInvalidTributeCancelOverride(t *testing.T) {
	env := baseEnv()
	env["TRIBUTE_CANCEL_IS_IMMEDIATE"] = "soon"

	_, err := LoadFromLookup(lookup(env))
	require.Error(t, err, "expected invalid boolean error")
	assert.Contains(t, err.Error(), "TRIBUTE_CANCEL_IS_IMMEDIATE")
}

func TestLoadReportsSourceClubChatConflictKeys(t *testing.T) {
	env := baseEnv()
	env["CLUB_CHAT_ID"] = env["BOOSTY_GROUP_ID"]

	_, err := LoadFromLookup(lookup(env))
	require.Error(t, err, "expected source/club conflict error")

	msg := err.Error()
	assert.Contains(t, msg, "BOOSTY_GROUP_ID")
	assert.Contains(t, msg, "CLUB_CHAT_ID")
	assert.Contains(t, msg, env["BOOSTY_GROUP_ID"], "error must cite the shared value")
}

func TestLoadParsesOptionalAdminLogChatID(t *testing.T) {
	env := baseEnv()
	env["ADMIN_LOG_CHAT_ID"] = "-1005555555555"

	cfg, err := LoadFromLookup(lookup(env))
	require.NoError(t, err)

	require.NotNil(t, cfg.AdminLogChatID)
	assert.Equal(t, int64(-1005555555555), *cfg.AdminLogChatID)
}

// TestLoadTrimsWhitespace verifies that whitespace padding around env
// values is stripped: parsing succeeds and the parsed value carries no
// padding. The companion negative case (whitespace-only required var)
// lives in TestLoad.
func TestLoadTrimsWhitespace(t *testing.T) {
	env := baseEnv()
	env["BOT_TOKEN"] = "  123456:ABC-DEF  "
	env["BOOSTY_SUBSCRIBE_URL"] = "  https://boosty.to/author  "
	env["OWNER_TG_IDS"] = " 11111111 , 22222222 "
	env["GRACE_PERIOD"] = "  72h  "

	cfg, err := LoadFromLookup(lookup(env))
	require.NoError(t, err)

	assert.Equal(t, "123456:ABC-DEF", cfg.BotToken)
	assert.Equal(t, "https://boosty.to/author", cfg.BoostySubscribeURL)
	assert.Equal(t, []int64{11111111, 22222222}, cfg.OwnerTGIDs)
	assert.Equal(t, 72*time.Hour, cfg.GracePeriod)
}

// TestLoadRejectsLongDirectInviteTTL verifies that an INVITE_TTL above
// Telegram's one-hour cap is rejected in direct mode rather than
// silently clamped — the loader must not rewrite the operator's value.
func TestLoadRejectsLongDirectInviteTTL(t *testing.T) {
	env := baseEnv()
	env["INVITE_MODE"] = "direct"
	env["ALLOW_DIRECT_INVITES"] = "true"
	env["INVITE_TTL"] = "24h"

	_, err := LoadFromLookup(lookup(env))
	require.Error(t, err, "INVITE_TTL=24h must be rejected in direct mode")
}
