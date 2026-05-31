package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Whitelist is the repository for the whitelist table: users with
// permanent free access (owner, moderators, etc.).
type Whitelist struct {
	db DBTX
}

// NewWhitelist returns a Whitelist repository backed by db or tx.
func NewWhitelist(db DBTX) *Whitelist {
	return &Whitelist{db: db}
}

// Add whitelists a user, recording who added them and why. Adding an
// already-whitelisted user updates the reason and the admin.
func (r *Whitelist) Add(ctx context.Context, tgID, addedBy int64, reason string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO whitelist (tg_id, reason, added_by, created_at)
		VALUES (?,?,?,?)
		ON CONFLICT(tg_id) DO UPDATE SET
			reason   = excluded.reason,
			added_by = excluded.added_by`,
		tgID, reason, addedBy, rfc3339(time.Now()))
	if err != nil {
		return fmt.Errorf("add whitelist %d: %w", tgID, err)
	}

	return nil
}

// Has reports whether the user is whitelisted.
func (r *Whitelist) Has(ctx context.Context, tgID int64) (bool, error) {
	var one int

	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM whitelist WHERE tg_id = ?`, tgID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("check whitelist %d: %w", tgID, err)
	}

	return true, nil
}

// Remove drops a user from the whitelist. Removing a missing row is a no-op.
func (r *Whitelist) Remove(ctx context.Context, tgID int64) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM whitelist WHERE tg_id = ?`, tgID); err != nil {
		return fmt.Errorf("remove whitelist %d: %w", tgID, err)
	}

	return nil
}
