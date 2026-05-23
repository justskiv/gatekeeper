package store

import (
	"context"
	"database/sql"
	"errors"
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

// GetActive returns the active subscription for (tgID, platform), or
// false when none exists. idx_subscriptions_active_unique guarantees at
// most one such row.
func (r *Subscriptions) GetActive(
	ctx context.Context, tgID int64, platform domain.Platform,
) (domain.Subscription, bool, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tg_id, platform, status, external_id, external_period_id,
		       tier, started_at, expires_at, ended_at, last_signal,
		       last_event_at, last_checked_at
		FROM subscriptions
		WHERE tg_id = ? AND platform = ? AND status = 'active'`,
		tgID, string(platform))

	var (
		s                                              domain.Subscription
		platformStr, statusStr, startedAt              string
		expiresAt, endedAt, lastEventAt, lastCheckedAt sql.NullString
	)
	err := row.Scan(&s.ID, &s.TGID, &platformStr, &statusStr, &s.ExternalID,
		&s.PeriodID, &s.Tier, &startedAt, &expiresAt, &endedAt,
		&s.LastSignal, &lastEventAt, &lastCheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Subscription{}, false, nil
	}
	if err != nil {
		return domain.Subscription{}, false,
			fmt.Errorf("get active subscription for %d/%s: %w", tgID, platform, err)
	}

	s.Platform = domain.Platform(platformStr)
	s.Status = domain.SubscriptionStatus(statusStr)
	if s.StartedAt, err = parseTime(startedAt); err != nil {
		return domain.Subscription{}, false,
			fmt.Errorf("parse subscription started_at: %w", err)
	}
	if s.ExpiresAt, err = parseNullTime(expiresAt); err != nil {
		return domain.Subscription{}, false,
			fmt.Errorf("parse subscription expires_at: %w", err)
	}
	if s.EndedAt, err = parseNullTime(endedAt); err != nil {
		return domain.Subscription{}, false,
			fmt.Errorf("parse subscription ended_at: %w", err)
	}
	if s.LastEventAt, err = parseNullTime(lastEventAt); err != nil {
		return domain.Subscription{}, false,
			fmt.Errorf("parse subscription last_event_at: %w", err)
	}
	if s.LastCheckedAt, err = parseNullTime(lastCheckedAt); err != nil {
		return domain.Subscription{}, false,
			fmt.Errorf("parse subscription last_checked_at: %w", err)
	}
	return s, true, nil
}
