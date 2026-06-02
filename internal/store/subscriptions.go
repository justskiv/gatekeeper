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
	db DBTX
}

// NewSubscriptions returns a Subscriptions repository backed by db or tx.
func NewSubscriptions(db DBTX) *Subscriptions {
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
		nullTime(s.EndedAt), s.LastSignal, nullEventTime(s.LastEventAt),
		nullTime(s.LastCheckedAt), now, now)
	if err != nil {
		return 0, fmt.Errorf("create subscription for %d: %w", s.TGID, err)
	}

	return res.LastInsertId()
}

// UpsertActive creates or updates the active row for (tgID, platform).
// Existing periods keep their original started_at. Empty identity fields
// do not clear previous values; last_signal tracks the newest signal.
func (r *Subscriptions) UpsertActive(
	ctx context.Context, s domain.Subscription,
) (int64, error) {
	nowTime := time.Now()
	now := rfc3339(nowTime)

	startedAt := s.StartedAt
	if startedAt.IsZero() {
		startedAt = nowTime
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE subscriptions
		SET external_id = CASE WHEN ? != '' THEN ? ELSE external_id END,
		    external_period_id = CASE WHEN ? != '' THEN ? ELSE external_period_id END,
		    tier = CASE WHEN ? != '' THEN ? ELSE tier END,
		    expires_at = COALESCE(?, expires_at),
		    ended_at = NULL,
		    last_signal = CASE WHEN ? != '' THEN ? ELSE last_signal END,
		    last_event_at = COALESCE(?, last_event_at),
		    last_checked_at = COALESCE(?, last_checked_at),
		    updated_at = ?
		WHERE tg_id = ? AND platform = ? AND status = 'active'`,
		s.ExternalID, s.ExternalID,
		s.PeriodID, s.PeriodID,
		s.Tier, s.Tier,
		nullTime(s.ExpiresAt),
		s.LastSignal, s.LastSignal,
		nullEventTime(s.LastEventAt), nullTime(s.LastCheckedAt),
		now, s.TGID, string(s.Platform))
	if err != nil {
		return 0, fmt.Errorf("update active subscription for %d/%s: %w",
			s.TGID, s.Platform, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read active subscription rows affected: %w", err)
	}

	if affected > 0 {
		var id int64
		if err := r.db.QueryRowContext(ctx, `
			SELECT id FROM subscriptions
			WHERE tg_id = ? AND platform = ? AND status = 'active'`,
			s.TGID, string(s.Platform)).Scan(&id); err != nil {
			return 0, fmt.Errorf("read active subscription id: %w", err)
		}

		return id, nil
	}

	s.Status = domain.SubActive
	s.StartedAt = startedAt

	id, err := r.Create(ctx, s)
	if err != nil {
		return 0, err
	}

	return id, nil
}

// ExpireActive closes the active row for (tgID, platform), if present.
func (r *Subscriptions) ExpireActive(
	ctx context.Context,
	tgID int64,
	platform domain.Platform,
	endedAt time.Time,
	signal string,
) (bool, error) {
	now := rfc3339(time.Now())

	res, err := r.db.ExecContext(ctx, `
		UPDATE subscriptions
		SET status = 'expired',
		    ended_at = ?,
		    last_signal = ?,
		    last_event_at = CASE
		        WHEN ? IN ('event', 'webhook') THEN ?
		        ELSE last_event_at
		    END,
		    last_checked_at = CASE
		        WHEN ? IN ('event', 'webhook') THEN last_checked_at
		        ELSE ?
		    END,
		    updated_at = ?
		WHERE tg_id = ? AND platform = ? AND status = 'active'`,
		rfc3339(endedAt), signal,
		signal, rfc3339Nano(endedAt),
		signal, rfc3339(endedAt),
		now,
		tgID, string(platform))
	if err != nil {
		return false, fmt.Errorf("expire active subscription for %d/%s: %w",
			tgID, platform, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read expired subscription rows affected: %w", err)
	}

	return affected > 0, nil
}

// UpsertManual creates or refreshes a manual active subscription.
func (r *Subscriptions) UpsertManual(
	ctx context.Context,
	tgID int64,
	expiresAt *time.Time,
	signal string,
) (int64, error) {
	now := time.Now()

	return r.UpsertActive(ctx, domain.Subscription{
		TGID:       tgID,
		Platform:   domain.PlatformManual,
		Status:     domain.SubActive,
		StartedAt:  now,
		ExpiresAt:  expiresAt,
		LastSignal: signal,
	})
}

// ExpireManual expires the active manual subscription, if one exists.
func (r *Subscriptions) ExpireManual(
	ctx context.Context,
	tgID int64,
	endedAt time.Time,
	signal string,
) (bool, error) {
	return r.ExpireActive(ctx, tgID, domain.PlatformManual, endedAt, signal)
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

	s, err := scanSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Subscription{}, false, nil
	}

	if err != nil {
		return domain.Subscription{}, false,
			fmt.Errorf("get active subscription for %d/%s: %w", tgID, platform, err)
	}

	return s, true, nil
}

// ListActiveByUser returns all active subscriptions for a user.
func (r *Subscriptions) ListActiveByUser(
	ctx context.Context, tgID int64,
) ([]domain.Subscription, error) {
	return r.listByUser(ctx, tgID, true)
}

// ListActiveTGIDs returns users that have at least one active subscription.
func (r *Subscriptions) ListActiveTGIDs(
	ctx context.Context,
	limit int,
) ([]int64, error) {
	if limit <= 0 {
		limit = 1000
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT tg_id
		FROM subscriptions
		WHERE status = 'active'
		ORDER BY tg_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list active subscription tg_ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []int64

	for rows.Next() {
		var tgID int64
		if err := rows.Scan(&tgID); err != nil {
			return nil, fmt.Errorf("scan active subscription tg_id: %w", err)
		}

		out = append(out, tgID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active subscription tg_ids: %w", err)
	}

	return out, nil
}

// ListByUser returns a user's subscription history from newest to oldest.
func (r *Subscriptions) ListByUser(
	ctx context.Context, tgID int64,
) ([]domain.Subscription, error) {
	return r.listByUser(ctx, tgID, false)
}

func (r *Subscriptions) listByUser(
	ctx context.Context, tgID int64, activeOnly bool,
) ([]domain.Subscription, error) {
	whereStatus := ""
	if activeOnly {
		whereStatus = " AND status = 'active'"
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tg_id, platform, status, external_id, external_period_id,
		       tier, started_at, expires_at, ended_at, last_signal,
		       last_event_at, last_checked_at
		FROM subscriptions
		WHERE tg_id = ?`+whereStatus+`
		ORDER BY started_at DESC, id DESC`,
		tgID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions for %d: %w", tgID, err)
	}

	defer func() { _ = rows.Close() }()

	var out []domain.Subscription

	for rows.Next() {
		s, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, s)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscriptions for %d: %w", tgID, err)
	}

	return out, nil
}

type subscriptionScanner interface {
	Scan(dest ...any) error
}

func scanSubscription(scanner subscriptionScanner) (domain.Subscription, error) {
	var (
		s                                              domain.Subscription
		platformStr, statusStr, startedAt              string
		expiresAt, endedAt, lastEventAt, lastCheckedAt sql.NullString
	)

	err := scanner.Scan(&s.ID, &s.TGID, &platformStr, &statusStr, &s.ExternalID,
		&s.PeriodID, &s.Tier, &startedAt, &expiresAt, &endedAt,
		&s.LastSignal, &lastEventAt, &lastCheckedAt)
	if err != nil {
		return domain.Subscription{}, err
	}

	s.Platform = domain.Platform(platformStr)

	s.Status = domain.SubscriptionStatus(statusStr)
	if s.StartedAt, err = parseTime(startedAt); err != nil {
		return domain.Subscription{}, fmt.Errorf("parse subscription started_at: %w", err)
	}

	if s.ExpiresAt, err = parseNullTime(expiresAt); err != nil {
		return domain.Subscription{}, fmt.Errorf("parse subscription expires_at: %w", err)
	}

	if s.EndedAt, err = parseNullTime(endedAt); err != nil {
		return domain.Subscription{}, fmt.Errorf("parse subscription ended_at: %w", err)
	}

	if s.LastEventAt, err = parseNullTime(lastEventAt); err != nil {
		return domain.Subscription{}, fmt.Errorf("parse subscription last_event_at: %w", err)
	}

	if s.LastCheckedAt, err = parseNullTime(lastCheckedAt); err != nil {
		return domain.Subscription{}, fmt.Errorf("parse subscription last_checked_at: %w", err)
	}

	return s, nil
}
