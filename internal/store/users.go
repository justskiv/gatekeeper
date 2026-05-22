package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// Users is the repository for the users table.
type Users struct {
	db *sql.DB
}

// NewUsers returns a Users repository backed by db.
func NewUsers(db *sql.DB) *Users {
	return &Users{db: db}
}

// Upsert inserts a user or updates the mutable columns of an existing
// row. created_at is preserved on update; updated_at and last_seen_at
// are set to the current time.
func (r *Users) Upsert(ctx context.Context, u domain.User) error {
	now := rfc3339(time.Now())
	dmState := u.DMState
	if dmState == "" {
		dmState = domain.DMUnknown
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO users (
			tg_id, username, first_name, last_name, language_code,
			is_bot, dm_state, banned, banned_reason, notes,
			created_at, updated_at, last_seen_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(tg_id) DO UPDATE SET
			username      = excluded.username,
			first_name    = excluded.first_name,
			last_name     = excluded.last_name,
			language_code = excluded.language_code,
			is_bot        = excluded.is_bot,
			dm_state      = excluded.dm_state,
			banned        = excluded.banned,
			banned_reason = excluded.banned_reason,
			notes         = excluded.notes,
			updated_at    = excluded.updated_at,
			last_seen_at  = excluded.last_seen_at`,
		u.TGID, u.Username, u.FirstName, u.LastName, u.LanguageCode,
		u.IsBot, string(dmState), u.Banned, u.BannedReason, u.Notes,
		now, now, now)
	if err != nil {
		return fmt.Errorf("upsert user %d: %w", u.TGID, err)
	}
	return nil
}

// Get returns the user with the given Telegram ID, or ErrNotFound.
func (r *Users) Get(ctx context.Context, tgID int64) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT tg_id, username, first_name, last_name, language_code,
			is_bot, dm_state, banned, banned_reason, notes,
			created_at, updated_at, last_seen_at
		FROM users WHERE tg_id = ?`, tgID)

	var (
		u                              domain.User
		dmState                        string
		createdAt, updatedAt, lastSeen string
	)
	err := row.Scan(&u.TGID, &u.Username, &u.FirstName, &u.LastName,
		&u.LanguageCode, &u.IsBot, &dmState, &u.Banned, &u.BannedReason,
		&u.Notes, &createdAt, &updatedAt, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get user %d: %w", tgID, err)
	}

	u.DMState = domain.DMState(dmState)
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.User{}, fmt.Errorf("parse user %d created_at: %w", tgID, err)
	}
	if u.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.User{}, fmt.Errorf("parse user %d updated_at: %w", tgID, err)
	}
	if u.LastSeenAt, err = parseTime(lastSeen); err != nil {
		return domain.User{}, fmt.Errorf("parse user %d last_seen_at: %w", tgID, err)
	}
	return u, nil
}
