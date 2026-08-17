package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
)

// TestOpsOutboxBacklogOnEmptyTable pins the shape of the aggregate: an
// aggregate without GROUP BY returns one row of NULLs on an empty table, and a
// naive scan of that row fails rather than reporting an idle queue.
func TestOpsOutboxBacklogOnEmptyTable(t *testing.T) {
	db := newTestDB(t)

	backlog, err := NewOps(db).OutboxBacklog(context.Background(), time.Now())
	require.NoError(t, err, "OutboxBacklog")

	assert.Equal(t, OutboxBacklog{}, backlog,
		"an empty queue reports zeros, not an error")
}

func TestOpsOutboxBacklogSeparatesDueFromScheduled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().Add(time.Hour).Truncate(time.Second)
	resource := domain.ResourceChat
	outbox := NewOutbox(db)

	for key, runAfter := range map[string]time.Time{
		"backlog-oldest":    now.Add(-30 * time.Minute),
		"backlog-due":       now.Add(-10 * time.Minute),
		"backlog-scheduled": now.Add(time.Hour),
	} {
		_, _, err := outbox.Enqueue(ctx, AccessActionInput{
			Type:           domain.ActionEnsureInvite,
			Resource:       &resource,
			IdempotencyKey: key,
			RunAfter:       runAfter,
		})
		require.NoError(t, err, "enqueue %s", key)
	}

	// Leasing takes the oldest due row, which is what makes running and due
	// distinguishable in the same scan.
	_, ok, err := outbox.LeaseReady(ctx, now, time.Minute)
	require.NoError(t, err, "LeaseReady")
	require.True(t, ok, "a due action must be leasable")

	backlog, err := NewOps(db).OutboxBacklog(ctx, now)
	require.NoError(t, err, "OutboxBacklog")

	assert.Equal(t, 2, backlog.Queued, "the leased row is no longer queued")
	assert.Equal(t, 1, backlog.Running)
	assert.Equal(t, 1, backlog.Due, "the future row is not backlog")
	assert.Equal(t, 10*time.Minute, backlog.OldestDueAge,
		"overdue age comes from run_after of the oldest due row")
	assert.InDelta(t, time.Hour.Seconds(),
		backlog.OldestQueuedAge.Seconds(), 5,
		"queued age comes from created_at of the oldest queued row")
}

func TestOpsAlertCountsGroupsByBoundedDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ops := NewOps(db)

	empty, err := ops.AlertCounts(ctx)
	require.NoError(t, err, "AlertCounts on an empty table")
	assert.Empty(t, empty)

	alerts := NewAlerts(db)
	for i := range 2 {
		_, err := alerts.Create(ctx, AlertInput{
			Severity: "error",
			Kind:     "outbox_action_dead",
			Title:    "outbox action dead " + strconv.Itoa(i),
		})
		require.NoError(t, err, "create dead-action alert")
	}

	_, err = alerts.Create(ctx, AlertInput{
		Severity: "warning",
		Kind:     "invite_mode_degraded",
		Title:    "invite mode degraded",
	})
	require.NoError(t, err, "create degraded-mode alert")

	require.NoError(t, alerts.ResolveOpenByTitle(
		ctx, "invite_mode_degraded", "invite mode degraded"), "resolve")

	counts, err := ops.AlertCounts(ctx)
	require.NoError(t, err, "AlertCounts")

	assert.Equal(t, []AlertCount{
		{
			Kind:     "invite_mode_degraded",
			Severity: "warning",
			Status:   "resolved",
			Count:    1,
		},
		{
			Kind:     "outbox_action_dead",
			Severity: "error",
			Status:   "open",
			Count:    2,
		},
	}, counts, "counts are grouped and ordered by kind, severity, status")
}

func TestOpsExportRowsOrdersSubscriptionsAndExpiriesTogether(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	boostyExpires := now.Add(30 * 24 * time.Hour)
	tributeExpires := now.Add(60 * 24 * time.Hour)

	subs := NewSubscriptions(db)

	_, err := subs.UpsertActive(ctx, domain.Subscription{
		TGID:      tgID,
		Platform:  domain.PlatformTribute,
		StartedAt: now,
		ExpiresAt: &tributeExpires,
	})
	require.NoError(t, err, "upsert tribute")

	_, err = subs.UpsertActive(ctx, domain.Subscription{
		TGID:      tgID,
		Platform:  domain.PlatformBoosty,
		StartedAt: now,
		ExpiresAt: &boostyExpires,
	})
	require.NoError(t, err, "upsert boosty")

	rows, err := NewOps(db).ExportRows(ctx, 10)
	require.NoError(t, err, "ExportRows")
	require.Len(t, rows, 1)

	// The export joins both platforms into a single ordered row.
	assert.Equal(t, "boosty|tribute", rows[0].Subscriptions)

	wantExpires := boostyExpires.Format(time.RFC3339) + "|" +
		tributeExpires.Format(time.RFC3339)
	assert.Equal(t, wantExpires, rows[0].ExpiresAt)
}
