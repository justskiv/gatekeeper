package store

import (
	"context"
	"testing"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/lib/random"
)

func TestTributeEventsInsertDedupAndTerminalStatus(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewTributeEvents(db)

	tgID := random.TGID()
	dedupKey := gofakeit.UUID()
	payload := []byte(`{"name":"new_subscription"}`)

	event, inserted, err := repo.InsertReceived(ctx, TributeEventInput{
		DedupKey:       dedupKey,
		EventName:      "new_subscription",
		TGID:           &tgID,
		SubscriptionID: gofakeit.UUID(),
		SignatureValid: true,
		PayloadJSON:    payload,
	})
	require.NoError(t, err, "InsertReceived")
	require.True(t, inserted, "first insert must report inserted")

	duplicate, inserted, err := repo.InsertReceived(ctx, TributeEventInput{
		DedupKey:       dedupKey,
		EventName:      "new_subscription",
		SignatureValid: true,
		PayloadJSON:    payload,
	})
	require.NoError(t, err, "InsertReceived duplicate")
	assert.False(t, inserted, "duplicate must not report inserted")
	assert.Equal(t, event.ID, duplicate.ID, "duplicate reuses the row")

	require.NoError(t,
		repo.MarkTerminal(ctx, event.ID, TributeEventProcessed, ""),
		"MarkTerminal")

	got, err := repo.GetByDedupKey(ctx, dedupKey)
	require.NoError(t, err, "GetByDedupKey")
	assert.Equal(t, TributeEventProcessed, got.Status)
	assert.NotNil(t, got.ProcessedAt, "processed event must carry a timestamp")
}
