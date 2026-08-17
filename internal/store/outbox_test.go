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

	_, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "retry"),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue")

	// Retry is a terminal transition, so it needs the lease it fences on.
	leased, ok, err := outbox.LeaseReady(ctx, time.Now(), time.Minute)
	require.NoError(t, err, "lease")
	require.True(t, ok, "action must be leasable")

	nextRun := time.Now().Add(17 * time.Second)

	retried, err := outbox.Retry(
		ctx, leased.ID, *leased.LockedUntil, nextRun, "telegram 429")
	require.NoError(t, err, "retry")
	assert.Equal(t, domain.ActionQueued, retried.Status)
	assert.Equal(t, 1, retried.Attempts)
	assert.Equal(t, "telegram 429", retried.LastError)
	assert.False(t, retried.RunAfter.Before(nextRun.Add(-time.Second)),
		"run_after must track the requested next run")
}

// TestOutboxTerminalTransitionsRequireLeaseOwnership covers the fencing
// contract: an action that outlives its lease is reclaimed by another worker,
// and the previous owner must not be able to write the outcome afterwards.
func TestOutboxTerminalTransitionsRequireLeaseOwnership(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	outbox := NewOutbox(db)

	_, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "fence"),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue")

	// First worker takes a lease that is already expired, mimicking an action
	// whose execution ran longer than the lease allowed.
	// Timestamps are stored at second precision, so the reclaim is driven by
	// advancing "now" past the lease rather than by shortening the lease.
	first, ok, err := outbox.LeaseReady(ctx, time.Now(), time.Second)
	require.NoError(t, err, "first lease")
	require.True(t, ok, "action must be leasable")

	// Second worker reclaims the expired lease.
	second, ok, err := outbox.LeaseReady(
		ctx, time.Now().Add(5*time.Second), time.Minute)
	require.NoError(t, err, "reclaim")
	require.True(t, ok, "expired lease must be reclaimable")
	require.Equal(t, first.ID, second.ID, "the same action is reclaimed")

	err = outbox.MarkDone(ctx, first.ID, *first.LockedUntil)
	require.ErrorIs(t, err, ErrLeaseLost,
		"the previous owner must not complete a reclaimed action")

	_, err = outbox.Retry(
		ctx, first.ID, *first.LockedUntil, time.Now(), "stale")
	require.ErrorIs(t, err, ErrLeaseLost, "retry must be fenced too")

	err = outbox.MarkDead(ctx, first.ID, *first.LockedUntil, "stale")
	require.ErrorIs(t, err, ErrLeaseLost, "mark dead must be fenced too")

	err = outbox.MarkCancelled(ctx, first.ID, *first.LockedUntil, "stale")
	require.ErrorIs(t, err, ErrLeaseLost, "mark cancelled must be fenced too")

	require.NoError(t, outbox.MarkDone(ctx, second.ID, *second.LockedUntil),
		"the current owner writes the outcome")

	got, err := outbox.GetByID(ctx, second.ID)
	require.NoError(t, err, "reload")
	assert.Equal(t, domain.ActionDone, got.Status)
}

// TestOutboxCancelQueuedForAlertSparesRunningRow pins the deliberate
// `status='queued'` guard: a running row is owned by the worker that leased it,
// and cancelling it here would make this statement a second writer racing that
// worker for the outcome. The worker's own pre-execute check covers it instead.
func TestOutboxCancelQueuedForAlertSparesRunningRow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	alertID, err := NewAlerts(db).Create(ctx, AlertInput{
		Severity: "critical",
		Kind:     "bot_rights_lost",
		Title:    "bot rights lost: club_chat",
	})
	require.NoError(t, err, "create alert")

	outbox := NewOutbox(db)

	inflight, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		AlertID:        &alertID,
		IdempotencyKey: "alert-delivery-inflight",
		RunAfter:       time.Now().Add(-time.Minute),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue the in-flight delivery")

	waiting, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		AlertID:        &alertID,
		IdempotencyKey: "alert-delivery-waiting",
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue the waiting delivery")

	leased, ok, err := outbox.LeaseReady(ctx, time.Now(), time.Minute)
	require.NoError(t, err, "lease")
	require.True(t, ok, "the older delivery must be leasable")
	require.Equal(t, inflight.ID, leased.ID, "the older row is leased first")

	cancelled, err := outbox.CancelQueuedForAlert(ctx, alertID, "alert resolved")
	require.NoError(t, err, "CancelQueuedForAlert")
	assert.Equal(t, int64(1), cancelled, "only the waiting row is cancelled")

	got, err := outbox.GetByID(ctx, inflight.ID)
	require.NoError(t, err, "reload the in-flight row")
	assert.Equal(t, domain.ActionRunning, got.Status,
		"a mid-flight row stays with its worker")

	got, err = outbox.GetByID(ctx, waiting.ID)
	require.NoError(t, err, "reload the waiting row")
	assert.Equal(t, domain.ActionCancelled, got.Status)
	assert.Equal(t, "alert resolved", got.LastError,
		"the reason is readable on the cancelled row")
}

// TestOutboxReleaseLeaseKeepsAttempts checks that handing an action back on
// shutdown does not charge it a failed attempt and makes it instantly leasable.
func TestOutboxReleaseLeaseKeepsAttempts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	outbox := NewOutbox(db)

	_, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "release"),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue")

	leased, ok, err := outbox.LeaseReady(ctx, time.Now(), time.Minute)
	require.NoError(t, err, "lease")
	require.True(t, ok, "action must be leasable")

	require.NoError(t, outbox.ReleaseLease(ctx, leased.ID, *leased.LockedUntil),
		"release")

	got, err := outbox.GetByID(ctx, leased.ID)
	require.NoError(t, err, "reload")
	assert.Equal(t, domain.ActionQueued, got.Status, "action returns to the queue")
	assert.Equal(t, 0, got.Attempts, "shutdown is not a failed attempt")
	assert.Nil(t, got.LockedUntil, "the lease is cleared")

	_, ok, err = outbox.LeaseReady(ctx, time.Now(), time.Minute)
	require.NoError(t, err, "re-lease")
	assert.True(t, ok, "the next process picks it up without waiting out the lease")
}

// TestOutboxReleaseLeaseIsFenced makes sure a shutdown race cannot yank an
// action away from the worker that reclaimed it.
func TestOutboxReleaseLeaseIsFenced(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	outbox := NewOutbox(db)

	_, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "fenced-release"),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue")

	// Timestamps are stored at second precision, so the reclaim is driven by
	// advancing "now" past the lease rather than by shortening the lease.
	first, ok, err := outbox.LeaseReady(ctx, time.Now(), time.Second)
	require.NoError(t, err, "first lease")
	require.True(t, ok)

	_, ok, err = outbox.LeaseReady(ctx, time.Now().Add(5*time.Second), time.Minute)
	require.NoError(t, err, "reclaim")
	require.True(t, ok)

	err = outbox.ReleaseLease(ctx, first.ID, *first.LockedUntil)
	require.ErrorIs(t, err, ErrLeaseLost,
		"a stale owner must not requeue an action someone else is running")
}
