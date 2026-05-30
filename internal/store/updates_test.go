package store

import (
	"context"
	"errors"
	"testing"

	"github.com/justskiv/gatekeeper/internal/domain"
)

func TestTelegramUpdatesInsertBatchAdvancesOffsetAtomically(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	chatID := int64(-1001)
	tgID := int64(42)

	err := NewTelegramUpdates(db).InsertBatch(ctx, []TelegramUpdate{
		{
			UpdateID:    41,
			UpdateType:  "message",
			ChatID:      &chatID,
			TGID:        &tgID,
			PayloadJSON: []byte(`{"update_id":41}`),
		},
		{
			UpdateID:    42,
			UpdateType:  "message",
			ChatID:      &chatID,
			TGID:        &tgID,
			PayloadJSON: []byte(`{"update_id":42}`),
		},
	}, 43)
	if err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	var rows int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM telegram_updates WHERE status = 'pending'`,
	).Scan(&rows); err != nil {
		t.Fatalf("count updates: %v", err)
	}
	if rows != 2 {
		t.Fatalf("pending rows = %d, want 2", rows)
	}

	offset, ok, err := NewMeta(db).GetUpdateOffset(ctx)
	if err != nil {
		t.Fatalf("GetUpdateOffset: %v", err)
	}
	if !ok || offset != 43 {
		t.Fatalf("offset = (%d, %v), want (43, true)", offset, ok)
	}
}

func TestTelegramUpdatesInsertBatchParticipatesInCallerRollback(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := NewTelegramUpdates(tx).InsertBatch(ctx, []TelegramUpdate{
		{
			UpdateID:    50,
			UpdateType:  "message",
			PayloadJSON: []byte(`{"update_id":50}`),
		},
	}, 51); err != nil {
		t.Fatalf("InsertBatch in tx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	var rows int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM telegram_updates WHERE update_id = 50`,
	).Scan(&rows); err != nil {
		t.Fatalf("count updates: %v", err)
	}
	if rows != 0 {
		t.Fatalf("updates after rollback = %d, want 0", rows)
	}
	if _, ok, err := NewMeta(db).GetUpdateOffset(ctx); err != nil || ok {
		t.Fatalf("offset after rollback = (_, %v, %v), want absent", ok, err)
	}
}

func TestTelegramUpdateTerminalStatusSharesHandlerTransaction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := NewTelegramUpdates(db).InsertBatch(ctx, []TelegramUpdate{
		{
			UpdateID:    100,
			UpdateType:  "message",
			PayloadJSON: []byte(`{"update_id":100}`),
		},
	}, 101); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := NewUsers(tx).Upsert(ctx, domain.User{
		TGID:     777,
		Username: "rollback",
		DMState:  domain.DMOpen,
	}); err != nil {
		t.Fatalf("Upsert in tx: %v", err)
	}
	if err := NewTelegramUpdates(tx).MarkTerminal(
		ctx, 100, TelegramUpdateProcessed, "",
	); err != nil {
		t.Fatalf("MarkTerminal in tx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if _, err := NewUsers(db).Get(ctx, 777); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user after rollback err = %v, want ErrNotFound", err)
	}
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM telegram_updates WHERE update_id = 100`,
	).Scan(&status); err != nil {
		t.Fatalf("read update status: %v", err)
	}
	if status != string(TelegramUpdatePending) {
		t.Fatalf("status after rollback = %q, want pending", status)
	}
}
