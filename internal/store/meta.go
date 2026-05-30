package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Meta is the repository for the meta key-value table: poller offset,
// reconcile time, chat health and other service state.
type Meta struct {
	db DBTX
}

// NewMeta returns a Meta repository backed by db or tx.
func NewMeta(db DBTX) *Meta {
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

// GetUpdateOffset returns the durable Telegram polling offset.
func (r *Meta) GetUpdateOffset(ctx context.Context) (int64, bool, error) {
	value, ok, err := r.Get(ctx, "update_offset")
	if err != nil || !ok {
		return 0, ok, err
	}
	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse update_offset %q: %w", value, err)
	}
	return offset, true, nil
}

// SetUpdateOffset stores the durable Telegram polling offset.
func (r *Meta) SetUpdateOffset(ctx context.Context, offset int64) error {
	return r.Set(ctx, "update_offset", strconv.FormatInt(offset, 10))
}

// SetHealth stores a stable health.<key> value. The caller may pass
// either "club_chat" or "health.club_chat".
func (r *Meta) SetHealth(ctx context.Context, key, value string) error {
	if !strings.HasPrefix(key, "health.") {
		key = "health." + key
	}
	return r.Set(ctx, key, value)
}
