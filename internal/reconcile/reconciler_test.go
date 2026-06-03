package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

func TestRunOnceExecutesDueRevocationAndEnqueuesVerify(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	revokedID := random.TGID()
	activeID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: revokedID}),
		"upsert user")
	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: activeID}),
		"upsert active user")

	_, err := store.NewSubscriptions(db).UpsertManual(ctx, activeID, nil, "test")
	require.NoError(t, err, "upsert active subscription")

	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       revokedID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert chat grant")

	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       revokedID,
		Resource:   domain.ResourceChannel,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert channel grant")

	require.NoError(t, store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID:        revokedID,
		Reason:      "expired",
		ScheduledAt: now.Add(-time.Minute),
	}), "upsert pending")

	r := New(db, engine.New(nil), nil, Config{
		Sources: []SourceChat{{
			Platform: domain.PlatformBoosty,
			ChatID:   -1001,
			Enabled:  true,
		}},
		Resources: []ResourceChat{{
			Resource: domain.ResourceChat,
			ChatID:   -1003,
		}},
	}, nil, WithClock(func() time.Time { return now }))

	summary, err := r.RunOnce(ctx)
	require.NoError(t, err, "RunOnce")

	assert.Equal(t, 1, summary.DueRevocations, "due revocations")
	assert.NotZero(t, summary.VerifyActions, "verify actions")

	assert.Equal(t, 2, countActions(t, db, domain.ActionSoftKick),
		"soft_kick actions for chat and channel")
	assert.NotZero(t, countActions(t, db, domain.ActionVerifyMember),
		"verify_member actions")

	_, ok, err := store.NewMeta(db).Get(ctx, "reconcile.last_run_at")
	require.NoError(t, err, "get last_run_at")
	assert.True(t, ok, "last_run_at must be present")
}

func TestRunOnceReportsHealthFailureWithoutReturningError(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	ownerID := random.TGID()

	r := New(db, nil, nil, Config{
		OwnerIDs: []int64{ownerID},
	}, nil,
		WithClock(func() time.Time { return now }),
		WithHealthCheck(func(context.Context) error {
			return errors.New("health degraded")
		}))

	summary, err := r.RunOnce(ctx)
	require.NoError(t, err, "RunOnce")

	assert.Equal(t, 1, summary.Failed, "one recoverable failure")

	assert.Equal(t, 1, countRows(t, db, "admin_alerts",
		"kind = 'reconcile_health_failed'"), "health alerts")
	assert.Equal(t, 1, countActions(t, db, domain.ActionSendDM),
		"operator deliveries")
}

func TestRunOnceRepeatsVerifyInNewCycleOnly(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := store.NewSubscriptions(db).UpsertManual(ctx, tgID, nil, "test")
	require.NoError(t, err, "upsert subscription")

	r := New(db, nil, nil, Config{
		Interval: time.Hour,
		Sources: []SourceChat{{
			Platform: domain.PlatformBoosty,
			ChatID:   -1001,
			Enabled:  true,
		}},
		Resources: []ResourceChat{{
			Resource: domain.ResourceChat,
			ChatID:   -1003,
		}},
	}, nil, WithClock(func() time.Time { return now }))

	_, err = r.RunOnce(ctx)
	require.NoError(t, err, "RunOnce first")

	_, err = r.RunOnce(ctx)
	require.NoError(t, err, "RunOnce same bucket")

	assert.Equal(t, 2, countActions(t, db, domain.ActionVerifyMember),
		"verify actions same bucket for source and club")

	now = now.Add(2 * time.Hour)

	_, err = r.RunOnce(ctx)
	require.NoError(t, err, "RunOnce next bucket")

	assert.Equal(t, 4, countActions(t, db, domain.ActionVerifyMember),
		"verify actions next bucket repeated source and club")
}

func TestRunOnceEnqueuesMissingSharedInvite(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	r := New(db, nil, fakeInviteMaintainer{}, Config{
		InviteMode: domain.InviteSharedJoinRequest,
		Resources: []ResourceChat{{
			Resource: domain.ResourceChat,
			ChatID:   -1003,
		}},
	}, nil, WithClock(func() time.Time { return now }))

	summary, err := r.RunOnce(ctx)
	require.NoError(t, err, "RunOnce")

	assert.Equal(t, 1, summary.InviteActions, "one invite action")
	assert.Equal(t, 1, countActions(t, db, domain.ActionEnsureInvite),
		"ensure_invite actions")
}

func TestCleanupPreservesFailedAndDeadRows(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour).Format(time.RFC3339)
	tgID := random.TGID()

	_, err := db.ExecContext(ctx, `
		INSERT INTO telegram_updates (
			update_id, update_type, payload_json, status, error,
			received_at, processed_at)
		VALUES
			(1, 'message', '{}', 'processed', '', ?, ?),
			(2, 'message', '{}', 'failed', 'boom', ?, ?)`,
		old, old, old, old)
	require.NoError(t, err, "seed updates")

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err = db.ExecContext(ctx, `
		INSERT INTO access_actions (
			action_type, tg_id, idempotency_key, payload_json, status,
			run_after, attempts, max_attempts, last_error, created_at, updated_at)
		VALUES
			('send_dm', ?, 'done-key', '{}', 'done', ?, 0, 8, '', ?, ?),
			('send_dm', ?, 'dead-key', '{}', 'dead', ?, 1, 8, 'boom', ?, ?)`,
		tgID, old, old, old, tgID, old, old, old)
	require.NoError(t, err, "seed actions")

	cleanup := NewCleanupService(db, Config{
		RawRetention:   24 * time.Hour,
		AuditRetention: 24 * time.Hour,
	}, nil)
	cleanup.now = func() time.Time { return now }

	require.NoError(t, cleanup.RunOnce(ctx), "cleanup RunOnce")

	assert.Equal(t, 1, countRows(t, db, "telegram_updates", "status = 'failed'"),
		"failed updates preserved")
	assert.Equal(t, 1, countRows(t, db, "access_actions", "status = 'dead'"),
		"dead actions preserved")
	assert.Equal(t, 0, countRows(t, db, "access_actions", "status = 'done'"),
		"done actions deleted")
}

type fakeInviteMaintainer struct{}

func (fakeInviteMaintainer) ActiveShared(
	context.Context,
	domain.Resource,
) (domain.InviteLink, bool, error) {
	return domain.InviteLink{}, false, nil
}

func countActions(t *testing.T, db *sql.DB, actionType domain.ActionType) int {
	t.Helper()

	var got int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM access_actions WHERE action_type = ?`,
		string(actionType)).Scan(&got), "count actions")

	return got
}

func countRows(t *testing.T, db *sql.DB, table, where string) int {
	t.Helper()

	var got int
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+table+" WHERE "+where).Scan(&got),
		"count %s", table)

	return got
}
