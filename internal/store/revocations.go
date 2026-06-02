package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// Revocations is the repository for the pending_revocations table — the
// grace-period queue, keyed by user.
type Revocations struct {
	db DBTX
}

// NewRevocations returns a Revocations repository backed by db or tx.
func NewRevocations(db DBTX) *Revocations {
	return &Revocations{db: db}
}

// Upsert schedules (or reschedules) a pending revocation for a user.
func (r *Revocations) Upsert(ctx context.Context, p domain.PendingRevocation) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO pending_revocations (tg_id, reason, scheduled_at, notified, created_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(tg_id) DO UPDATE SET
			reason       = excluded.reason,
			scheduled_at = excluded.scheduled_at,
			notified     = excluded.notified`,
		p.TGID, p.Reason, rfc3339(p.ScheduledAt), p.Notified, rfc3339(time.Now()))
	if err != nil {
		return fmt.Errorf("upsert revocation %d: %w", p.TGID, err)
	}

	return nil
}

// CreateIfAbsent schedules a revocation without changing an existing one.
func (r *Revocations) CreateIfAbsent(
	ctx context.Context,
	p domain.PendingRevocation,
) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO pending_revocations (tg_id, reason, scheduled_at, notified, created_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(tg_id) DO NOTHING`,
		p.TGID, p.Reason, rfc3339(p.ScheduledAt), p.Notified, rfc3339(time.Now()))
	if err != nil {
		return false, fmt.Errorf("create revocation %d: %w", p.TGID, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read revocation rows affected: %w", err)
	}

	return affected == 1, nil
}

// Get returns a pending revocation, or ok=false when none exists.
func (r *Revocations) Get(
	ctx context.Context, tgID int64,
) (domain.PendingRevocation, bool, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT tg_id, reason, scheduled_at, notified, created_at
		FROM pending_revocations
		WHERE tg_id = ?`, tgID)

	var (
		p                      domain.PendingRevocation
		scheduledAt, createdAt string
	)

	err := row.Scan(&p.TGID, &p.Reason, &scheduledAt, &p.Notified, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PendingRevocation{}, false, nil
	}

	if err != nil {
		return domain.PendingRevocation{}, false,
			fmt.Errorf("get revocation %d: %w", tgID, err)
	}

	if p.ScheduledAt, err = parseTime(scheduledAt); err != nil {
		return domain.PendingRevocation{}, false,
			fmt.Errorf("parse revocation scheduled_at: %w", err)
	}

	if p.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.PendingRevocation{}, false,
			fmt.Errorf("parse revocation created_at: %w", err)
	}

	return p, true, nil
}

// Delete cancels a pending revocation. Removing a missing row is a no-op.
func (r *Revocations) Delete(ctx context.Context, tgID int64) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM pending_revocations WHERE tg_id = ?`, tgID); err != nil {
		return fmt.Errorf("delete revocation %d: %w", tgID, err)
	}

	return nil
}

// ListDue returns due revocations ordered by scheduled_at.
func (r *Revocations) ListDue(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]domain.PendingRevocation, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT tg_id, reason, scheduled_at, notified, created_at
		FROM pending_revocations
		WHERE scheduled_at <= ?
		ORDER BY scheduled_at, tg_id
		LIMIT ?`, rfc3339(now), limit)
	if err != nil {
		return nil, fmt.Errorf("list due revocations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.PendingRevocation

	for rows.Next() {
		p, err := scanPendingRevocation(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due revocations: %w", err)
	}

	return out, nil
}

// MarkNotified records that the warning message was scheduled.
func (r *Revocations) MarkNotified(ctx context.Context, tgID int64) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE pending_revocations
		SET notified = 1
		WHERE tg_id = ?`, tgID)
	if err != nil {
		return fmt.Errorf("mark revocation %d notified: %w", tgID, err)
	}

	return requireAffected(res, "revocation", tgID)
}

type revocationScanner interface {
	Scan(dest ...any) error
}

func scanPendingRevocation(
	scanner revocationScanner,
) (domain.PendingRevocation, error) {
	var (
		p                      domain.PendingRevocation
		scheduledAt, createdAt string
	)

	if err := scanner.Scan(
		&p.TGID, &p.Reason, &scheduledAt, &p.Notified, &createdAt,
	); err != nil {
		return domain.PendingRevocation{}, err
	}

	var err error
	if p.ScheduledAt, err = parseTime(scheduledAt); err != nil {
		return domain.PendingRevocation{},
			fmt.Errorf("parse revocation scheduled_at: %w", err)
	}

	if p.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.PendingRevocation{},
			fmt.Errorf("parse revocation created_at: %w", err)
	}

	return p, nil
}
