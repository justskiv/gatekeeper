package store

import (
	"context"
	"testing"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
)

func TestTelegramUpdatesInsertBatchAdvancesOffsetAtomically(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	chatID := int64(-1001)
	tgID := random.TGID()

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
	require.NoError(t, err, "InsertBatch")

	var rows int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM telegram_updates WHERE status = 'pending'`,
	).Scan(&rows), "count updates")
	assert.Equal(t, 2, rows)

	offset, ok, err := NewMeta(db).GetUpdateOffset(ctx)
	require.NoError(t, err, "GetUpdateOffset")
	require.True(t, ok, "offset must be persisted with the batch")
	assert.Equal(t, int64(43), offset)
}

func TestTelegramUpdatesInsertBatchParticipatesInCallerRollback(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err, "BeginTx")

	err = NewTelegramUpdates(tx).InsertBatch(ctx, []TelegramUpdate{
		{
			UpdateID:    50,
			UpdateType:  "message",
			PayloadJSON: []byte(`{"update_id":50}`),
		},
	}, 51)
	require.NoError(t, err, "InsertBatch in tx")

	require.NoError(t, tx.Rollback(), "Rollback")

	var rows int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM telegram_updates WHERE update_id = 50`,
	).Scan(&rows), "count updates")
	assert.Zero(t, rows, "rolled-back batch must leave no rows")

	_, ok, err := NewMeta(db).GetUpdateOffset(ctx)
	require.NoError(t, err, "GetUpdateOffset")
	assert.False(t, ok, "rolled-back offset must be absent")
}

func TestTelegramUpdateTerminalStatusSharesHandlerTransaction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	err := NewTelegramUpdates(db).InsertBatch(ctx, []TelegramUpdate{
		{
			UpdateID:    100,
			UpdateType:  "message",
			PayloadJSON: []byte(`{"update_id":100}`),
		},
	}, 101)
	require.NoError(t, err, "InsertBatch")

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err, "BeginTx")

	tgID := random.TGID()

	err = NewUsers(tx).Upsert(ctx, domain.User{
		TGID:     tgID,
		Username: gofakeit.Username(),
		DMState:  domain.DMOpen,
	})
	require.NoError(t, err, "Upsert in tx")

	require.NoError(t,
		NewTelegramUpdates(tx).MarkTerminal(ctx, 100, TelegramUpdateProcessed, ""),
		"MarkTerminal in tx")

	require.NoError(t, tx.Rollback(), "Rollback")

	_, err = NewUsers(db).Get(ctx, tgID)
	require.ErrorIs(t, err, ErrNotFound, "rolled-back user must be gone")

	var status string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT status FROM telegram_updates WHERE update_id = 100`,
	).Scan(&status), "read update status")
	assert.Equal(t, string(TelegramUpdatePending), status,
		"rolled-back MarkTerminal must leave the update pending")
}
