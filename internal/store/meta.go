package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Meta is the repository for the meta key-value table: poller offset,
// reconcile time, chat health and other service state.
type Meta struct {
	db *sql.DB
}

// NewMeta returns a Meta repository backed by db.
func NewMeta(db *sql.DB) *Meta {
	return &Meta{db: db}
}

// Get returns the value for a key. The second result is false when the
// key is absent.
func (r *Meta) Get(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get meta %q: %w", key, err)
	}
	return value, true, nil
}

// Set stores a value for a key, overwriting any existing value.
func (r *Meta) Set(ctx context.Context, key, value string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO meta (key, value, updated_at)
		VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET
			value      = excluded.value,
			updated_at = excluded.updated_at`,
		key, value, rfc3339(time.Now()))
	if err != nil {
		return fmt.Errorf("set meta %q: %w", key, err)
	}
	return nil
}
