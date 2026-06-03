// Package enforcer executes durable Telegram actions from access_actions.
package enforcer

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/store"
)

// OutboxStore is the durable action queue surface used by workers.
type OutboxStore interface {
	LeaseReady(
		ctx context.Context,
		now time.Time,
		leaseFor time.Duration,
	) (domain.AccessAction, bool, error)
	MarkDone(ctx context.Context, id int64) error
	Retry(
		ctx context.Context,
		id int64,
		runAfter time.Time,
		lastError string,
	) (domain.AccessAction, error)
	MarkDead(ctx context.Context, id int64, lastError string) error
}

// TelegramClient is the consumer-side Telegram API surface.
type TelegramClient interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
	SendFormattedMessage(
		ctx context.Context,
		chatID int64,
		text string,
		parseMode string,
	) error
	EditMessageText(
		ctx context.Context,
		chatID int64,
		messageID int,
		text string,
		replyMarkup models.ReplyMarkup,
	) error
	GetChatMember(ctx context.Context, chatID, userID int64) (*models.ChatMember, error)
	ApproveChatJoinRequest(ctx context.Context, chatID, userID int64) error
	DeclineChatJoinRequest(ctx context.Context, chatID, userID int64) error
	BanChatMember(ctx context.Context, chatID, userID int64) error
	UnbanChatMember(ctx context.Context, chatID, userID int64, onlyIfBanned bool) error
}

// InviteService is the invite-link behavior used by action executors.
type InviteService interface {
	Ensure(ctx context.Context, req invite.EnsureRequest) (domain.InviteLink, error)
	Revoke(ctx context.Context, link domain.InviteLink) error
	MarkSent(ctx context.Context, link domain.InviteLink) error
}

// UserStore is the user surface needed for DM state updates.
type UserStore interface {
	SetDMState(ctx context.Context, tgID int64, state domain.DMState) error
	Upsert(ctx context.Context, u domain.User) error
}

// SubscriptionStore is the source-observation surface used by verify_member.
type SubscriptionStore interface {
	UpsertActive(ctx context.Context, s domain.Subscription) (int64, error)
	ExpireActive(
		ctx context.Context,
		tgID int64,
		platform domain.Platform,
		endedAt time.Time,
		signal string,
	) (bool, error)
}

// GrantStore is the club-observation surface used by verify_member.
type GrantStore interface {
	MarkJoined(
		ctx context.Context,
		tgID int64,
		resource domain.Resource,
		admittedBy string,
	) error
	MarkLeftUnlessRevoked(
		ctx context.Context,
		tgID int64,
		resource domain.Resource,
	) (bool, error)
}

// AuditStore is the append-only journal surface used by verify_member.
type AuditStore interface {
	Append(ctx context.Context, e store.AuditEntry) error
}

// AlertStore is the operational alert surface used for dead actions.
type AlertStore interface {
	Create(ctx context.Context, a store.AlertInput) (int64, error)
}

// Stores bundles repository dependencies.
type Stores struct {
	Outbox        OutboxStore
	Users         UserStore
	Subscriptions SubscriptionStore
	Grants        GrantStore
	Audit         AuditStore
	Revocations   engine.RevocationStore
	Whitelist     engine.WhitelistStore
	Alerts        AlertStore
	StatusEngine  *engine.Engine
}
