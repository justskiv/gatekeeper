package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// Subscriptions is the repository for the subscriptions table, which is
// append-only: a new period adds a new row and an expired one is marked
// rather than deleted.
type Subscriptions struct {
	db *sql.DB
}

// NewSubscriptions returns a Subscriptions repository backed by db.
func NewSubscriptions(db *sql.DB) *Subscriptions {
	return &Subscriptions{db: db}
}

// Create inserts a new subscription row and returns its generated id.
// The partial unique index idx_subscriptions_active_unique rejects a
// second active subscription for the same (user, platform) pair.
func (r *Subscriptions) Create(ctx context.Context, s domain.Subscription) (int64, error) {
	now := rfc3339(time.Now())
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO subscriptions (
			tg_id, platform, status, external_id, external_period_id,
			tier, started_at, expires_at, ended_at, last_signal,
			last_event_at, last_checked_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.TGID, string(s.Platform), string(s.Status), s.ExternalID,
		s.PeriodID, s.Tier, rfc3339(s.StartedAt), nullTime(s.ExpiresAt),
		nullTime(s.EndedAt), s.LastSignal, nullTime(s.LastEventAt),
		nullTime(s.LastCheckedAt), now, now)
	if err != nil {
		return 0, fmt.Errorf("create subscription for %d: %w", s.TGID, err)
	}
	return res.LastInsertId()
}
