// Package config loads and validates the application configuration from
// the environment (12-factor). In development a .env file in the working
// directory is loaded automatically.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config is the fully parsed and validated application configuration.
type Config struct {
	// Telegram
	BotToken   string
	OwnerTGIDs []int64

	// Storage
	DBPath string

	// Observed sources
	BoostyGroupID    int64
	TributeChannelID int64

	// Managed club resources
	ClubChatID     int64
	ClubChannelID  int64
	AdminLogChatID int64 // 0 when unset

	// Subscribe links
	BoostySubscribeURL  string
	TributeSubscribeURL string

	// Access granting
	InviteMode         string
	InviteTTL          time.Duration
	AllowDirectInvites bool

	// Tribute
	TributeMode              string
	TributeAPIKey            string
	TributeCancelIsImmediate bool
	WebhookListenAddr        string
	TributeWebhookPath       string

	// Telegram transport
	TelegramMode             string
	TelegramWebhookPublicURL string
	TelegramWebhookPath      string
	TelegramWebhookSecret    string

	// Expiry policy
	ExpiryMode        string
	GracePeriod       time.Duration
	ReconcileInterval time.Duration
	CleanupInterval   time.Duration
	RawRetention      time.Duration
	AuditRetention    time.Duration
	EnforcerWorkers   int

	// Misc
	Timezone       string
	Location       *time.Location
	LogLevel       string
	LogFormat      string
	MetricsEnabled bool
}

// Load reads the configuration from the environment, applying defaults
// and validating every value. A .env file in the working directory is
// loaded first if present (development convenience). On any problem it
// returns a single error listing every issue found.
func Load() (*Config, error) {
	// Best-effort .env load; a missing file is not an error.
	_ = godotenv.Load()

	l := &loader{}
	cfg := &Config{}

	cfg.BotToken = l.required("BOT_TOKEN")
	cfg.OwnerTGIDs = l.idList("OWNER_TG_IDS")
	cfg.DBPath = l.str("DB_PATH", "./data/gatekeeper.db")

	cfg.BoostyGroupID = l.chatID("BOOSTY_GROUP_ID", true)
	cfg.TributeChannelID = l.chatID("TRIBUTE_CHANNEL_ID", true)
	cfg.ClubChatID = l.chatID("CLUB_CHAT_ID", true)
	cfg.ClubChannelID = l.chatID("CLUB_CHANNEL_ID", true)
	cfg.AdminLogChatID = l.chatID("ADMIN_LOG_CHAT_ID", false)

	cfg.BoostySubscribeURL = l.required("BOOSTY_SUBSCRIBE_URL")
	cfg.TributeSubscribeURL = l.required("TRIBUTE_SUBSCRIBE_URL")

	cfg.InviteMode = l.enum("INVITE_MODE", "shared_join_request",
		"shared_join_request", "personal_join_request", "direct")
	cfg.InviteTTL = l.duration("INVITE_TTL", "24h")
	cfg.AllowDirectInvites = l.boolean("ALLOW_DIRECT_INVITES", false)

	cfg.TributeMode = l.enum("TRIBUTE_MODE", "observation", "observation", "webhook")
	cfg.TributeAPIKey = l.str("TRIBUTE_API_KEY", "")
	cfg.TributeCancelIsImmediate = l.boolean("TRIBUTE_CANCEL_IS_IMMEDIATE", false)
	cfg.WebhookListenAddr = l.str("WEBHOOK_LISTEN_ADDR", ":8080")
	cfg.TributeWebhookPath = l.str("TRIBUTE_WEBHOOK_PATH", "/webhooks/tribute")

	cfg.TelegramMode = l.enum("TELEGRAM_MODE", "polling", "polling", "webhook")
	cfg.TelegramWebhookPublicURL = l.str("TELEGRAM_WEBHOOK_PUBLIC_URL", "")
	cfg.TelegramWebhookPath = l.str("TELEGRAM_WEBHOOK_PATH", "/webhooks/telegram")
	cfg.TelegramWebhookSecret = l.str("TELEGRAM_WEBHOOK_SECRET", "")

	cfg.ExpiryMode = l.enum("EXPIRY_MODE", "grace", "grace", "immediate", "notify_only")
	cfg.GracePeriod = l.duration("GRACE_PERIOD", "72h")
	cfg.ReconcileInterval = l.duration("RECONCILE_INTERVAL", "1h")
	cfg.CleanupInterval = l.duration("CLEANUP_INTERVAL", "24h")
	cfg.RawRetention = l.duration("RAW_RETENTION", "720h")
	cfg.AuditRetention = l.duration("AUDIT_RETENTION", "8760h")
	cfg.EnforcerWorkers = l.intVal("ENFORCER_WORKERS", 2)

	cfg.Timezone = l.str("TIMEZONE", "UTC")
	cfg.Location = l.location("TIMEZONE", cfg.Timezone)
	cfg.LogLevel = l.enum("LOG_LEVEL", "info", "debug", "info", "warn", "error")
	cfg.LogFormat = l.enum("LOG_FORMAT", "json", "json", "text")
	cfg.MetricsEnabled = l.boolean("METRICS_ENABLED", false)

	l.validate(cfg)

	if len(l.errs) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s",
			strings.Join(l.errs, "\n  - "))
	}
	return cfg, nil
}

// loader reads typed values from the environment and accumulates every
// problem so that Load can report them all at once.
type loader struct {
	errs []string
}

func (l *loader) errf(format string, args ...any) {
	l.errs = append(l.errs, fmt.Sprintf(format, args...))
}

func (l *loader) str(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (l *loader) required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		l.errf("%s is required", key)
	}
	return v
}

// chatID parses a Telegram chat ID, which must be a negative integer.
// When required is false an empty value yields 0 without an error.
func (l *loader) chatID(key string, required bool) int64 {
	v := os.Getenv(key)
	if v == "" {
		if required {
			l.errf("%s is required", key)
		}
		return 0
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.errf("%s must be an integer, got %q", key, v)
		return 0
	}
	if id >= 0 {
		l.errf("%s must be a negative chat ID, got %d", key, id)
	}
	return id
}

func (l *loader) idList(key string) []int64 {
	v := os.Getenv(key)
	if v == "" {
		l.errf("%s is required", key)
		return nil
	}
	var ids []int64
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			l.errf("%s contains an invalid ID %q", key, part)
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		l.errf("%s is required", key)
	}
	return ids
}

func (l *loader) enum(key, def string, allowed ...string) string {
	v := l.str(key, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.errf("%s must be one of [%s], got %q", key, strings.Join(allowed, ", "), v)
	return v
}

func (l *loader) duration(key, def string) time.Duration {
	raw := l.str(key, def)
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.errf("%s must be a valid duration, got %q", key, raw)
		return 0
	}
	if d <= 0 {
		l.errf("%s must be a positive duration, got %q", key, raw)
	}
	return d
}

func (l *loader) boolean(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errf("%s must be a boolean, got %q", key, v)
		return def
	}
	return b
}

func (l *loader) intVal(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errf("%s must be an integer, got %q", key, v)
		return def
	}
	return n
}

func (l *loader) location(key, tz string) *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		l.errf("%s is not a loadable IANA timezone, got %q", key, tz)
		return time.UTC
	}
	return loc
}

// validate runs the cross-field rules from SPEC §18.3.
func (l *loader) validate(cfg *Config) {
	// The four source and club chat IDs must be pairwise distinct.
	type namedID struct {
		name string
		id   int64
	}
	chats := []namedID{
		{"BOOSTY_GROUP_ID", cfg.BoostyGroupID},
		{"TRIBUTE_CHANNEL_ID", cfg.TributeChannelID},
		{"CLUB_CHAT_ID", cfg.ClubChatID},
		{"CLUB_CHANNEL_ID", cfg.ClubChannelID},
	}
	for i := 0; i < len(chats); i++ {
		for j := i + 1; j < len(chats); j++ {
			// id == 0 means the value failed to parse; skip to avoid
			// reporting the same problem twice.
			if chats[i].id != 0 && chats[i].id == chats[j].id {
				l.errf("%s and %s must be different chats (both %d)",
					chats[i].name, chats[j].name, chats[i].id)
			}
		}
	}

	if cfg.TributeMode == "webhook" && cfg.TributeAPIKey == "" {
		l.errf("TRIBUTE_API_KEY is required when TRIBUTE_MODE=webhook")
	}

	if cfg.TelegramMode == "webhook" {
		if cfg.TelegramWebhookPublicURL == "" {
			l.errf("TELEGRAM_WEBHOOK_PUBLIC_URL is required when TELEGRAM_MODE=webhook")
		}
		if cfg.TelegramWebhookSecret == "" {
			l.errf("TELEGRAM_WEBHOOK_SECRET is required when TELEGRAM_MODE=webhook")
		}
	}

	if cfg.InviteMode == "direct" {
		if !cfg.AllowDirectInvites {
			l.errf("ALLOW_DIRECT_INVITES must be true when INVITE_MODE=direct")
		}
		// Telegram caps direct invite links at one hour.
		if cfg.InviteTTL > time.Hour {
			cfg.InviteTTL = time.Hour
		}
	}
}
