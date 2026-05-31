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
