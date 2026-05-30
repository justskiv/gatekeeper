package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TelegramUpdateStatus is the durable inbox state machine.
type TelegramUpdateStatus string

const (
	TelegramUpdatePending   TelegramUpdateStatus = "pending"
	TelegramUpdateProcessed TelegramUpdateStatus = "processed"
	TelegramUpdateIgnored   TelegramUpdateStatus = "ignored"
	TelegramUpdateFailed    TelegramUpdateStatus = "failed"
)

// TelegramUpdate is one row from the durable Telegram inbox.
type TelegramUpdate struct {
	UpdateID    int64
	UpdateType  string
	ChatID      *int64
	TGID        *int64
	PayloadJSON []byte
	Status      TelegramUpdateStatus
	Error       string
	ReceivedAt  time.Time
	ProcessedAt *time.Time
}

// TelegramUpdates is the repository for the telegram_updates inbox.
type TelegramUpdates struct {
	db DBTX
}

// NewTelegramUpdates returns a repository backed by db or tx.
func NewTelegramUpdates(db DBTX) *TelegramUpdates {
	return &TelegramUpdates{db: db}
}

// InsertBatch stores new updates as pending and advances the polling
// offset in the same transaction. Duplicate update_id rows are ignored.
func (r *TelegramUpdates) InsertBatch(
	ctx context.Context, updates []TelegramUpdate, nextOffset int64,
) error {
	if len(updates) == 0 {
		return nil
	}

	if starter, ok := r.db.(txStarter); ok {
		tx, err := starter.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin telegram update batch: %w", err)
		}
		if err := insertBatch(ctx, tx, updates, nextOffset); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit telegram update batch: %w", err)
		}
		return nil
	}

	return insertBatch(ctx, r.db, updates, nextOffset)
}

func insertBatch(
	ctx context.Context, q DBTX, updates []TelegramUpdate, nextOffset int64,
) error {
	now := rfc3339(time.Now())
	for _, upd := range updates {
		receivedAt := now
		if !upd.ReceivedAt.IsZero() {
			receivedAt = rfc3339(upd.ReceivedAt)
		}
		_, err := q.ExecContext(ctx, `
			INSERT OR IGNORE INTO telegram_updates (
				update_id, update_type, chat_id, tg_id, payload_json,
				received_at, status)
			VALUES (?,?,?,?,?,?,'pending')`,
			upd.UpdateID, upd.UpdateType, nullableUpdateInt64(upd.ChatID),
			nullableUpdateInt64(upd.TGID), string(upd.PayloadJSON), receivedAt)
		if err != nil {
			return fmt.Errorf("insert telegram update %d: %w", upd.UpdateID, err)
		}
	}
	if err := NewMeta(q).SetUpdateOffset(ctx, nextOffset); err != nil {
		return err
	}
	return nil
}

// ListPending returns pending updates ordered by update_id.
func (r *TelegramUpdates) ListPending(
	ctx context.Context, limit int,
) ([]TelegramUpdate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT update_id, update_type, chat_id, tg_id, payload_json,
		       status, error, received_at, processed_at
		FROM telegram_updates
		WHERE status = 'pending'
		ORDER BY update_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending telegram updates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []TelegramUpdate
	for rows.Next() {
		var (
			upd             TelegramUpdate
			chatID, tgID    sql.NullInt64
			payload, status string
			receivedAt      string
			processedAt     sql.NullString
		)
		if err := rows.Scan(&upd.UpdateID, &upd.UpdateType, &chatID, &tgID,
			&payload, &status, &upd.Error, &receivedAt, &processedAt); err != nil {
			return nil, fmt.Errorf("scan pending telegram update: %w", err)
		}
		upd.ChatID = int64Ptr(chatID)
		upd.TGID = int64Ptr(tgID)
		upd.PayloadJSON = []byte(payload)
		upd.Status = TelegramUpdateStatus(status)
		var err error
		if upd.ReceivedAt, err = parseTime(receivedAt); err != nil {
			return nil, fmt.Errorf("parse telegram update received_at: %w", err)
		}
		if upd.ProcessedAt, err = parseNullTime(processedAt); err != nil {
			return nil, fmt.Errorf("parse telegram update processed_at: %w", err)
		}
		out = append(out, upd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending telegram updates: %w", err)
	}
	return out, nil
}

// MarkTerminal moves a pending update to a terminal state.
func (r *TelegramUpdates) MarkTerminal(
	ctx context.Context, updateID int64, status TelegramUpdateStatus, errorText string,
) error {
	switch status {
	case TelegramUpdateProcessed, TelegramUpdateIgnored, TelegramUpdateFailed:
	default:
		return fmt.Errorf("invalid terminal telegram update status %q", status)
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE telegram_updates
		SET status = ?, error = ?, processed_at = ?
		WHERE update_id = ? AND status = 'pending'`,
		string(status), errorText, rfc3339(time.Now()), updateID)
	if err != nil {
		return fmt.Errorf("mark telegram update %d %s: %w", updateID, status, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("read telegram update %d rows affected: %w", updateID, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// MaxUpdateID returns the largest update_id recorded in the inbox.
func (r *TelegramUpdates) MaxUpdateID(ctx context.Context) (int64, bool, error) {
	var max sql.NullInt64
	if err := r.db.QueryRowContext(ctx,
		`SELECT max(update_id) FROM telegram_updates`).Scan(&max); err != nil {
		return 0, false, fmt.Errorf("read max telegram update id: %w", err)
	}
	if !max.Valid {
		return 0, false, nil
	}
	return max.Int64, true, nil
}

// ResolveOffset returns max(meta.update_offset, max(update_id)+1).
func (r *TelegramUpdates) ResolveOffset(ctx context.Context, meta *Meta) (int64, error) {
	var resolved int64
	if offset, ok, err := meta.GetUpdateOffset(ctx); err != nil {
		return 0, err
	} else if ok {
		resolved = offset
	}
	if maxID, ok, err := r.MaxUpdateID(ctx); err != nil {
		return 0, err
	} else if ok && maxID+1 > resolved {
		resolved = maxID + 1
	}
	return resolved, nil
}

func nullableUpdateInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func int64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// IsUpdateAlreadyTerminal reports whether a terminal mark failed because
// the row was no longer pending.
func IsUpdateAlreadyTerminal(err error) bool {
	return errors.Is(err, ErrNotFound)
}
