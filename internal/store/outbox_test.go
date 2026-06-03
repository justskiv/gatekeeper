package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
)

func TestOutboxEnqueueIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	key := domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "update:1")
	outbox := NewOutbox(db)

	first, inserted, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: key,
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "first enqueue")
	require.True(t, inserted, "first enqueue must insert")

	second, inserted, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: key,
		PayloadJSON:    []byte(`{"text":"changed"}`),
	})
	require.NoError(t, err, "second enqueue")
	assert.False(t, inserted, "second enqueue must reuse the row")
	assert.Equal(t, first.ID, second.ID, "idempotent enqueue keeps one row")

	var rows int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM access_actions WHERE idempotency_key = ?`,
		key).Scan(&rows), "count actions")
	assert.Equal(t, 1, rows)
}

func TestOutboxLeaseReclaimsExpiredRunningAction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	now := time.Now()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	outbox := NewOutbox(db)

	queued, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "lease"),
		RunAfter:       now.Add(-time.Minute),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue")

	leased, ok, err := outbox.LeaseReady(ctx, now, time.Minute)
	require.NoError(t, err, "lease first")
	require.True(t, ok, "a ready action must be leased")
	assert.Equal(t, queued.ID, leased.ID)
	assert.Equal(t, domain.ActionRunning, leased.Status)

	_, ok, err = outbox.LeaseReady(ctx, now, time.Minute)
	require.NoError(t, err, "second lease")
	assert.False(t, ok, "a leased action must not be handed out again")

	reclaimed, ok, err := outbox.LeaseReady(ctx, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err, "lease expired running")
	require.True(t, ok, "an expired lease must be reclaimable")
	assert.Equal(t, queued.ID, reclaimed.ID)
}

func TestOutboxRetryRecordsMetadata(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	outbox := NewOutbox(db)

	action, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "retry"),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue")

	nextRun := time.Now().Add(17 * time.Second)

	retried, err := outbox.Retry(ctx, action.ID, nextRun, "telegram 429")
	require.NoError(t, err, "retry")
	assert.Equal(t, domain.ActionQueued, retried.Status)
	assert.Equal(t, 1, retried.Attempts)
	assert.Equal(t, "telegram 429", retried.LastError)
	assert.False(t, retried.RunAfter.Before(nextRun.Add(-time.Second)),
		"run_after must track the requested next run")
}
