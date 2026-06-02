// Package engine contains the subscription status core.
package engine

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

// SubscriptionSource is the consumer-side source interface.
type SubscriptionSource interface {
	Platform() domain.Platform
	Verdict(ctx context.Context, tgID int64) (domain.SourceVerdict, error)
}

// UserStore is the user repository surface needed by the engine.
type UserStore interface {
	Upsert(ctx context.Context, u domain.User) error
	Get(ctx context.Context, tgID int64) (domain.User, error)
}

// SubscriptionStore is the subscription repository surface needed by the engine.
type SubscriptionStore interface {
	UpsertActive(ctx context.Context, s domain.Subscription) (int64, error)
	ExpireActive(
		ctx context.Context,
		tgID int64,
		platform domain.Platform,
		endedAt time.Time,
		signal string,
	) (bool, error)
	ListActiveByUser(ctx context.Context, tgID int64) ([]domain.Subscription, error)
}

// AuditStore is the audit repository surface needed by the engine.
type AuditStore interface {
	Append(ctx context.Context, e store.AuditEntry) error
}

// RevocationStore is the pending-revocation surface needed by the engine.
type RevocationStore interface {
	CreateIfAbsent(ctx context.Context, p domain.PendingRevocation) (bool, error)
	Get(ctx context.Context, tgID int64) (domain.PendingRevocation, bool, error)
	Delete(ctx context.Context, tgID int64) error
	MarkNotified(ctx context.Context, tgID int64) error
}

// WhitelistStore is the whitelist repository surface needed by the engine.
type WhitelistStore interface {
	Has(ctx context.Context, tgID int64) (bool, error)
}

// GrantStore is the access grant surface needed by revocation.
type GrantStore interface {
	ListEligibleForRevoke(ctx context.Context, tgID int64) ([]domain.AccessGrant, error)
	Revoke(ctx context.Context, tgID int64, resource domain.Resource, reason string) (bool, error)
}

// OutboxStore is the durable Telegram action surface needed by revocation.
type OutboxStore interface {
	Enqueue(
		ctx context.Context,
		input store.AccessActionInput,
	) (domain.AccessAction, bool, error)
}

// AlertStore is the operational alert surface needed by revocation.
type AlertStore interface {
	Create(ctx context.Context, a store.AlertInput) (int64, error)
}

// MemberChecker reads live Telegram membership for protection checks.
type MemberChecker interface {
	GetChatMember(
		ctx context.Context,
		resource domain.Resource,
		tgID int64,
	) (*models.ChatMember, error)
}

// Store is the narrow repository bundle used by engine operations.
type Store struct {
	Users         UserStore
	Subscriptions SubscriptionStore
	Audit         AuditStore
	Revocations   RevocationStore
	Whitelist     WhitelistStore
	Grants        GrantStore
	Outbox        OutboxStore
	Alerts        AlertStore
	Members       MemberChecker
}

// Effect is a post-commit side effect prepared by the engine.
type Effect struct {
	TGID int64
	Text string
}

// Snapshot is a live, pre-transaction status snapshot.
type Snapshot struct {
	TGID     int64
	Verdicts []domain.SourceVerdict
	Decision domain.AccessDecision
}
