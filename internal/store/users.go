package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// Users is the repository for the users table.
type Users struct {
	db DBTX
}

// NewUsers returns a Users repository backed by db or tx.
func NewUsers(db DBTX) *Users {
	return &Users{db: db}
}

// Upsert inserts a user or refreshes Telegram-owned profile fields.
// Admin-owned fields such as bans and notes are preserved on update.
// created_at is preserved; updated_at and last_seen_at are set to now.
func (r *Users) Upsert(ctx context.Context, u domain.User) error {
	now := rfc3339(time.Now())
	dmState := u.DMState
	if dmState == "" {
		dmState = domain.DMUnknown
	}
	dmStateUpdate := string(u.DMState)
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
			dm_state      = CASE WHEN ? != '' THEN excluded.dm_state ELSE dm_state END,
			updated_at    = excluded.updated_at,
			last_seen_at  = excluded.last_seen_at`,
		u.TGID, u.Username, u.FirstName, u.LastName, u.LanguageCode,
		u.IsBot, string(dmState), u.Banned, u.BannedReason, u.Notes,
		now, now, now, dmStateUpdate)
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

	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get user %d: %w", tgID, err)
	}
	return u, nil
}

// FindByUsername returns a locally known user by Telegram username.
func (r *Users) FindByUsername(
	ctx context.Context, username string,
) (domain.User, bool, error) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return domain.User{}, false, nil
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT tg_id, username, first_name, last_name, language_code,
			is_bot, dm_state, banned, banned_reason, notes,
			created_at, updated_at, last_seen_at
		FROM users WHERE lower(username) = lower(?)`, username)

	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, false, nil
	}
	if err != nil {
		return domain.User{}, false,
			fmt.Errorf("find user by username %q: %w", username, err)
	}
	return user, true, nil
}

// SetDMState narrowly updates the direct-message state without touching
// profile cache fields. Opening a DM also advances last_seen_at.
func (r *Users) SetDMState(
	ctx context.Context, tgID int64, state domain.DMState,
) error {
	now := rfc3339(time.Now())
	res, err := r.db.ExecContext(ctx, `
		UPDATE users
		SET dm_state = ?,
		    updated_at = ?,
		    last_seen_at = CASE WHEN ? = 'open' THEN ? ELSE last_seen_at END
		WHERE tg_id = ?`,
		string(state), now, string(state), now, tgID)
	if err != nil {
		return fmt.Errorf("set user %d dm_state: %w", tgID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("read user %d dm_state rows affected: %w", tgID, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

type userScanner interface {
	Scan(dest ...any) error
}

func scanUser(scanner userScanner) (domain.User, error) {
	var (
		u                              domain.User
		dmState                        string
		createdAt, updatedAt, lastSeen string
	)
	err := scanner.Scan(&u.TGID, &u.Username, &u.FirstName, &u.LastName,
		&u.LanguageCode, &u.IsBot, &dmState, &u.Banned, &u.BannedReason,
		&u.Notes, &createdAt, &updatedAt, &lastSeen)
	if err != nil {
		return domain.User{}, err
	}

	u.DMState = domain.DMState(dmState)
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.User{}, fmt.Errorf("parse user %d created_at: %w", u.TGID, err)
	}
	if u.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.User{}, fmt.Errorf("parse user %d updated_at: %w", u.TGID, err)
	}
	if u.LastSeenAt, err = parseTime(lastSeen); err != nil {
		return domain.User{}, fmt.Errorf("parse user %d last_seen_at: %w", u.TGID, err)
	}
	return u, nil
}
