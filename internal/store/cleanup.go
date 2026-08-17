package store

import (
	"context"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// Cleanup contains repository operations for retention and maintenance.
type Cleanup struct {
	db DBTX
}

// NewCleanup returns cleanup operations backed by db or tx.
func NewCleanup(db DBTX) *Cleanup {
	return &Cleanup{db: db}
}

// DeleteTerminalTelegramUpdates deletes processed or ignored inbox rows.
func (r *Cleanup) DeleteTerminalTelegramUpdates(
	ctx context.Context,
	cutoff time.Time,
) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM telegram_updates
		WHERE status IN ('processed', 'ignored')
		  AND processed_at IS NOT NULL
		  AND processed_at < ?`, rfc3339(cutoff))
	if err != nil {
		return 0, fmt.Errorf("cleanup telegram updates: %w", err)
	}

	return res.RowsAffected()
}

// DeleteTerminalTributeEvents deletes processed or ignored Tribute rows.
func (r *Cleanup) DeleteTerminalTributeEvents(
	ctx context.Context,
	cutoff time.Time,
) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM tribute_events
		WHERE status IN ('processed', 'ignored')
		  AND processed_at IS NOT NULL
		  AND processed_at < ?`, rfc3339(cutoff))
	if err != nil {
		return 0, fmt.Errorf("cleanup tribute events: %w", err)
	}

	return res.RowsAffected()
}

// DeleteSettledActions deletes outbox rows that ended without leaving anything
// for an operator to look at: `done` (delivered) and `cancelled` (retired
// before delivery, e.g. because the alert it reported on resolved first).
// `dead` rows are preserved — those are the ones worth investigating.
func (r *Cleanup) DeleteSettledActions(
	ctx context.Context,
	cutoff time.Time,
) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM access_actions
		WHERE status IN ('done', 'cancelled')
		  AND updated_at < ?`, rfc3339(cutoff))
	if err != nil {
		return 0, fmt.Errorf("cleanup settled actions: %w", err)
	}

	return res.RowsAffected()
}

// DeleteResolvedAlerts deletes resolved alerts after retention.
func (r *Cleanup) DeleteResolvedAlerts(
	ctx context.Context,
	cutoff time.Time,
) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM admin_alerts
		WHERE status = 'resolved'
		  AND resolved_at IS NOT NULL
		  AND resolved_at < ?`, rfc3339(cutoff))
	if err != nil {
		return 0, fmt.Errorf("cleanup resolved alerts: %w", err)
	}

	return res.RowsAffected()
}

// DeleteAudit deletes old audit rows after retention.
func (r *Cleanup) DeleteAudit(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM audit_log WHERE created_at < ?`, rfc3339(cutoff))
	if err != nil {
		return 0, fmt.Errorf("cleanup audit log: %w", err)
	}

	return res.RowsAffected()
}

// ExpireInvite marks an invite as expired.
func (r *Cleanup) ExpireInvite(ctx context.Context, id int64) error {
	return NewInvites(r.db).MarkStatus(ctx, id, domain.InviteExpired, nil, "")
}

// WALCheckpoint runs a passive SQLite WAL checkpoint.
func (r *Cleanup) WALCheckpoint(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}

	return nil
}
