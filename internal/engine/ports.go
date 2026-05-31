// Package engine contains the subscription status core.
package engine

import (
	"context"
	"time"

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
	Get(ctx context.Context, tgID int64) (domain.PendingRevocation, bool, error)
	Delete(ctx context.Context, tgID int64) error
}

// WhitelistStore is the whitelist repository surface needed by the engine.
type WhitelistStore interface {
	Has(ctx context.Context, tgID int64) (bool, error)
}

// Store is the narrow repository bundle used by engine operations.
type Store struct {
	Users         UserStore
	Subscriptions SubscriptionStore
	Audit         AuditStore
	Revocations   RevocationStore
	Whitelist     WhitelistStore
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
