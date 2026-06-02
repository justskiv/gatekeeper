package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/store"
)

func TestRunOnceExecutesDueRevocationAndEnqueuesVerify(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: 701}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: 702}); err != nil {
		t.Fatalf("upsert active user: %v", err)
	}

	if _, err := store.NewSubscriptions(db).UpsertManual(
		ctx, 702, nil, "test",
	); err != nil {
		t.Fatalf("upsert active subscription: %v", err)
	}

	if err := store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       701,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}); err != nil {
		t.Fatalf("upsert chat grant: %v", err)
	}

	if err := store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       701,
		Resource:   domain.ResourceChannel,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}); err != nil {
		t.Fatalf("upsert channel grant: %v", err)
	}

	if err := store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID:        701,
		Reason:      "expired",
		ScheduledAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("upsert pending: %v", err)
	}

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
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if summary.DueRevocations != 1 || summary.VerifyActions == 0 {
		t.Fatalf("summary = %+v, want due revocation and verify actions", summary)
	}

	if got := countActions(t, db, domain.ActionSoftKick); got != 2 {
		t.Fatalf("soft_kick actions = %d, want chat and channel", got)
	}

	if got := countActions(t, db, domain.ActionVerifyMember); got == 0 {
		t.Fatal("verify_member actions = 0, want candidates")
	}

	if _, ok, err := store.NewMeta(db).Get(ctx, "reconcile.last_run_at"); err != nil || !ok {
		t.Fatalf("last_run_at = (_, %v, %v), want present", ok, err)
	}
}

func TestRunOnceReportsHealthFailureWithoutReturningError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	r := New(db, nil, nil, Config{
		OwnerIDs: []int64{9001},
	}, nil,
		WithClock(func() time.Time { return now }),
		WithHealthCheck(func(context.Context) error {
			return errors.New("health degraded")
		}))

	summary, err := r.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if summary.Failed != 1 {
		t.Fatalf("summary = %+v, want one recoverable failure", summary)
	}

	if got := countRows(t, db, "admin_alerts",
		"kind = 'reconcile_health_failed'"); got != 1 {
		t.Fatalf("health alerts = %d, want one", got)
	}

	if got := countActions(t, db, domain.ActionSendDM); got != 1 {
		t.Fatalf("operator deliveries = %d, want one", got)
	}
}

func TestRunOnceRepeatsVerifyInNewCycleOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: 703}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := store.NewSubscriptions(db).UpsertManual(ctx, 703, nil, "test"); err != nil {
		t.Fatalf("upsert subscription: %v", err)
	}

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

	if _, err := r.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce first: %v", err)
	}

	if _, err := r.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce same bucket: %v", err)
	}

	if got := countActions(t, db, domain.ActionVerifyMember); got != 2 {
		t.Fatalf("verify actions same bucket = %d, want source and club", got)
	}

	now = now.Add(2 * time.Hour)

	if _, err := r.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce next bucket: %v", err)
	}

	if got := countActions(t, db, domain.ActionVerifyMember); got != 4 {
		t.Fatalf("verify actions next bucket = %d, want repeated source and club", got)
	}
}

func TestRunOnceEnqueuesMissingSharedInvite(t *testing.T) {
	db := newTestDB(t)
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
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if summary.InviteActions != 1 {
		t.Fatalf("summary = %+v, want one invite action", summary)
	}

	if got := countActions(t, db, domain.ActionEnsureInvite); got != 1 {
		t.Fatalf("ensure_invite actions = %d, want one", got)
	}
}

func TestCleanupPreservesFailedAndDeadRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour).Format(time.RFC3339)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO telegram_updates (
			update_id, update_type, payload_json, status, error,
			received_at, processed_at)
		VALUES
			(1, 'message', '{}', 'processed', '', ?, ?),
			(2, 'message', '{}', 'failed', 'boom', ?, ?)`,
		old, old, old, old); err != nil {
		t.Fatalf("seed updates: %v", err)
	}

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: 702}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO access_actions (
			action_type, tg_id, idempotency_key, payload_json, status,
			run_after, attempts, max_attempts, last_error, created_at, updated_at)
		VALUES
			('send_dm', 702, 'done-key', '{}', 'done', ?, 0, 8, '', ?, ?),
			('send_dm', 702, 'dead-key', '{}', 'dead', ?, 1, 8, 'boom', ?, ?)`,
		old, old, old, old, old, old); err != nil {
		t.Fatalf("seed actions: %v", err)
	}

	cleanup := NewCleanupService(db, Config{
		RawRetention:   24 * time.Hour,
		AuditRetention: 24 * time.Hour,
	}, nil)
	cleanup.now = func() time.Time { return now }

	if err := cleanup.RunOnce(ctx); err != nil {
		t.Fatalf("cleanup RunOnce: %v", err)
	}

	if got := countRows(t, db, "telegram_updates", "status = 'failed'"); got != 1 {
		t.Fatalf("failed updates = %d, want preserved", got)
	}

	if got := countRows(t, db, "access_actions", "status = 'dead'"); got != 1 {
		t.Fatalf("dead actions = %d, want preserved", got)
	}

	if got := countRows(t, db, "access_actions", "status = 'done'"); got != 0 {
		t.Fatalf("done actions = %d, want deleted", got)
	}
}

type fakeInviteMaintainer struct{}

func (fakeInviteMaintainer) ActiveShared(
	context.Context,
	domain.Resource,
) (domain.InviteLink, bool, error) {
	return domain.InviteLink{}, false, nil
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(migrationsDir(t)))
	if err != nil {
		t.Fatalf("new goose provider: %v", err)
	}

	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return db
}

func migrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

func countActions(t *testing.T, db *sql.DB, actionType domain.ActionType) int {
	t.Helper()

	var got int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM access_actions WHERE action_type = ?`,
		string(actionType)).Scan(&got); err != nil {
		t.Fatalf("count actions: %v", err)
	}

	return got
}

func countRows(t *testing.T, db *sql.DB, table, where string) int {
	t.Helper()

	var got int
	if err := db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+table+" WHERE "+where).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}

	return got
}
