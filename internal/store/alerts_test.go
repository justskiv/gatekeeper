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

const (
	testAlertKind  = "bot_rights_lost"
	testAlertTitle = "bot rights lost: club_chat"
)

// TestResolveCancelsQueuedAlertDeliveries is the storage half of the
// 2026-08-16 incident regression: an alert that resolves must take its
// undelivered notifications with it, without disturbing rows it does not own.
func TestResolveCancelsQueuedAlertDeliveries(t *testing.T) {
	db := newTestDB(t)

	assertResolveCancelsDeliveries(t, context.Background(), db)
}

// TestResolveCancelsQueuedAlertDeliveriesInsideTx runs the same contract in the
// poller's shape, where every repository is bound to the update's *sql.Tx.
// WithTx must then run in place instead of opening a nested transaction —
// SQLite has none, and a silent no-op here would leave the deliveries queued.
func TestResolveCancelsQueuedAlertDeliveriesInsideTx(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err, "begin tx")

	t.Cleanup(func() { _ = tx.Rollback() })

	assertResolveCancelsDeliveries(t, ctx, tx)
}

func assertResolveCancelsDeliveries(t *testing.T, ctx context.Context, q DBTX) {
	t.Helper()

	ownerID := random.TGID()
	outbox := NewOutbox(q)
	alerts := NewAlertsWithDelivery(q, outbox, []int64{ownerID}, nil)

	alertID, err := alerts.Create(ctx, AlertInput{
		Severity: "critical",
		Kind:     testAlertKind,
		Title:    testAlertTitle,
		Detail:   "chat_key=club_chat chat_id=-1001 reason=not_admin",
	})
	require.NoError(t, err, "create alert")

	var pending int64
	require.NoError(t, q.QueryRowContext(ctx,
		`SELECT id FROM access_actions WHERE alert_id = ?`, alertID).Scan(&pending),
		"the alert must have queued an owner delivery")

	// A second delivery for the same alert that already went out. Cancellation
	// must not rewrite history for a message the owner has already read.
	delivered, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &ownerID,
		AlertID:        &alertID,
		IdempotencyKey: "alert-delivery-already-sent",
		PayloadJSON:    []byte(`{"text":"already sent"}`),
	})
	require.NoError(t, err, "enqueue the delivered notification")

	_, err = q.ExecContext(ctx,
		`UPDATE access_actions SET status = 'done' WHERE id = ?`, delivered.ID)
	require.NoError(t, err, "mark the second delivery done")

	// An ordinary queued message with no alert link at all.
	unlinked, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &ownerID,
		IdempotencyKey: "unrelated-dm",
		PayloadJSON:    []byte(`{"text":"unrelated"}`),
	})
	require.NoError(t, err, "enqueue the unlinked message")

	require.NoError(t,
		alerts.ResolveOpenByTitle(ctx, testAlertKind, testAlertTitle), "resolve")

	assert.Equal(t, domain.ActionCancelled, actionStatus(t, ctx, q, pending),
		"a queued delivery for a resolved alert must be cancelled")
	assert.Equal(t, domain.ActionDone, actionStatus(t, ctx, q, delivered.ID),
		"an already delivered row must keep its outcome")
	assert.Equal(t, domain.ActionQueued, actionStatus(t, ctx, q, unlinked.ID),
		"a message unrelated to the alert must not be touched")
}

// TestOwnerNotifiedByCallerSkipsTheGenericOwnerDM is the storage half of the
// collapse: a caller that writes its own message about the alert must not also
// get the repository's generic one. Only that caller is affected — an alert
// created without the flag still gets its delivery, which is the only thing
// callers with no copy of their own have.
func TestOwnerNotifiedByCallerSkipsTheGenericOwnerDM(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	alerts := NewAlertsWithDelivery(db, NewOutbox(db), []int64{ownerID}, nil)

	_, err := alerts.Create(ctx, AlertInput{
		Severity:              "critical",
		Kind:                  testAlertKind,
		Title:                 testAlertTitle,
		Detail:                "chat_key=club_chat chat_id=-1001 reason=not_admin",
		OwnerNotifiedByCaller: true,
	})
	require.NoError(t, err, "create the caller-notified alert")

	assert.Zero(t, countActions(t, ctx, db),
		"the caller sends its own message; a generic copy is noise")

	_, err = alerts.Create(ctx, AlertInput{
		Severity: "error",
		Kind:     "outbox_action_dead",
		Title:    "outbox action dead",
		Detail:   "action_id=1",
	})
	require.NoError(t, err, "create an ordinary alert")

	assert.Equal(t, 1, countActions(t, ctx, db),
		"an alert nobody else reports must still reach the owner")
}

// TestOwnerNotifiedByCallerKeepsTheAdminLogDelivery pins the limit of the
// opt-out. With ADMIN_LOG_CHAT_ID configured the generic delivery is a line in
// an operator feed, not a second message to the owner, so there is no
// duplication to collapse and dropping it would only cost the log an entry.
func TestOwnerNotifiedByCallerKeepsTheAdminLogDelivery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	logChatID := int64(-100500)
	alerts := NewAlertsWithDelivery(
		db, NewOutbox(db), []int64{ownerID}, &logChatID)

	_, err := alerts.Create(ctx, AlertInput{
		Severity:              "critical",
		Kind:                  testAlertKind,
		Title:                 testAlertTitle,
		Detail:                "chat_key=club_chat chat_id=-1001 reason=not_admin",
		OwnerNotifiedByCaller: true,
	})
	require.NoError(t, err, "create the caller-notified alert")

	var payload string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT payload_json FROM access_actions`).Scan(&payload),
		"the operator feed keeps its entry")
	assert.Contains(t, payload, `"chat_id":-100500`,
		"and it is addressed to the log chat, not to the owner")
}

func countActions(t *testing.T, ctx context.Context, q DBTX) int {
	t.Helper()

	var n int

	require.NoError(t, q.QueryRowContext(ctx,
		`SELECT count(*) FROM access_actions`).Scan(&n), "count actions")

	return n
}

func TestAlertsIsOpenTracksAlertLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	alerts := NewAlerts(db)

	id, err := alerts.Create(ctx, AlertInput{
		Severity: "error",
		Kind:     testAlertKind,
		Title:    testAlertTitle,
		Detail:   "chat_key=club_chat chat_id=-1001 reason=not_admin",
	})
	require.NoError(t, err, "create alert")

	open, err := alerts.IsOpen(ctx, id)
	require.NoError(t, err, "IsOpen for an open alert")
	assert.True(t, open, "a freshly created alert is open")

	require.NoError(t,
		alerts.ResolveOpenByTitle(ctx, testAlertKind, testAlertTitle), "resolve")

	open, err = alerts.IsOpen(ctx, id)
	require.NoError(t, err, "IsOpen for a resolved alert")
	assert.False(t, open, "a resolved alert is not open")

	open, err = alerts.IsOpen(ctx, id+1_000)
	require.NoError(t, err, "a missing alert must not be an error")
	assert.False(t, open, "a missing alert is not open")
}

// TestDeleteResolvedAlertsKeepsLinkedAction is the foreign-key regression:
// store.Open runs with foreign_keys ON, and retention deletes resolved alerts.
// Without ON DELETE SET NULL on access_actions.alert_id the first delivered
// notification that outlives its alert would wedge cleanup permanently.
func TestDeleteResolvedAlertsKeepsLinkedAction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	outbox := NewOutbox(db)
	alerts := NewAlertsWithDelivery(db, outbox, []int64{ownerID}, nil)

	_, err := alerts.Create(ctx, AlertInput{
		Severity: "critical",
		Kind:     testAlertKind,
		Title:    testAlertTitle,
		Detail:   "chat_key=club_chat chat_id=-1001 reason=not_admin",
	})
	require.NoError(t, err, "create alert")

	leased, ok, err := outbox.LeaseReady(ctx, time.Now(), time.Minute)
	require.NoError(t, err, "lease the delivery")
	require.True(t, ok, "the alert must have queued a delivery")
	require.NoError(t, outbox.MarkDone(ctx, leased.ID, *leased.LockedUntil),
		"deliver it")

	require.NoError(t,
		alerts.ResolveOpenByTitle(ctx, testAlertKind, testAlertTitle), "resolve")

	deleted, err := NewCleanup(db).DeleteResolvedAlerts(
		ctx, time.Now().Add(time.Hour))
	require.NoError(t, err, "retention must not trip the foreign key")
	assert.Equal(t, int64(1), deleted, "the resolved alert is reaped")

	got, err := outbox.GetByID(ctx, leased.ID)
	require.NoError(t, err, "the delivered action must survive its alert")
	assert.Nil(t, got.AlertID, "the dangling link is cleared, not the row")
}

// TestCleanupReapsSettledActionsAndKeepsDead pins the retention split: `done`
// and `cancelled` rows are noise once old, `dead` rows are the ones an operator
// still needs.
func TestCleanupReapsSettledActionsAndKeepsDead(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	old := rfc3339(time.Now().Add(-2 * time.Hour))
	_, err := db.ExecContext(ctx, `
		INSERT INTO access_actions (
			action_type, tg_id, idempotency_key, payload_json, status,
			run_after, created_at, updated_at)
		VALUES
			('send_dm', ?, 'settled-done', '{}', 'done', ?, ?, ?),
			('send_dm', ?, 'settled-cancelled', '{}', 'cancelled', ?, ?, ?),
			('send_dm', ?, 'settled-dead', '{}', 'dead', ?, ?, ?)`,
		tgID, old, old, old, tgID, old, old, old, tgID, old, old, old)
	require.NoError(t, err, "seed actions")

	deleted, err := NewCleanup(db).DeleteSettledActions(
		ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err, "DeleteSettledActions")
	assert.Equal(t, int64(2), deleted, "done and cancelled rows are reaped")

	var remaining string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT status FROM access_actions`).Scan(&remaining), "read survivor")
	assert.Equal(t, "dead", remaining, "dead rows stay for investigation")
}

func actionStatus(
	t *testing.T,
	ctx context.Context,
	q DBTX,
	id int64,
) domain.ActionStatus {
	t.Helper()

	var status string

	err := q.QueryRowContext(ctx,
		`SELECT status FROM access_actions WHERE id = ?`, id).Scan(&status)
	require.NoError(t, err, "read action status")

	return domain.ActionStatus(status)
}
