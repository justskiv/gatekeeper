package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
)

// newTestDB opens a fresh on-disk database in a temp directory and
// applies every migration from the top-level migrations/ directory.
//
// store keeps its own copy of this helper instead of using
// internal/testutil: store's tests live in package store, and testutil
// imports store, so importing it here would form an import cycle.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err, "open database")

	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(migrationsDir(t)))
	require.NoError(t, err, "new goose provider")

	_, err = provider.Up(context.Background())
	require.NoError(t, err, "apply migrations")

	return db
}

// migrationsDir resolves the repo's top-level migrations/ directory
// relative to this test file, so the path holds regardless of the
// `go test` invocation cwd.
func migrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed; cannot resolve migrations dir")

	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

func TestMigrateCreatesSchema(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// goose_db_version records at least one applied migration.
	var version sql.NullInt64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT max(version_id) FROM goose_db_version WHERE is_applied = 1`,
	).Scan(&version), "query goose_db_version")
	require.True(t, version.Valid)
	assert.GreaterOrEqual(t, version.Int64, int64(1))

	// All 12 domain/ops tables exist.
	wantTables := []string{
		"users", "subscriptions", "access_grants", "pending_revocations",
		"whitelist", "invite_links", "telegram_updates", "tribute_events",
		"access_actions", "audit_log", "admin_alerts", "meta",
	}
	for _, table := range wantTables {
		var n int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`,
			table).Scan(&n), "check table %s", table)
		assert.Equalf(t, 1, n, "table %s is missing", table)
	}
}

func TestCheckSchemaErrorsOnEmptyDB(t *testing.T) {
	// Open without migrating — CheckSchema must reject the db.
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "empty.db"))
	require.NoError(t, err, "open database")

	t.Cleanup(func() { _ = db.Close() })

	err = CheckSchema(context.Background(), db)
	require.ErrorIs(t, err, ErrUnmigrated)
	assert.Contains(t, err.Error(), "task migrate:up",
		"error must point the operator at the migrate command")
}

func TestCheckSchemaPassesOnMigratedDB(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, CheckSchema(context.Background(), db),
		"CheckSchema on migrated db")
}

func TestForeignKeysEnforced(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	var fk int
	require.NoError(t, db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk),
		"read foreign_keys pragma")
	require.Equal(t, 1, fk, "foreign keys must be enforced")

	// A subscription that references a non-existent user must be rejected.
	_, err := NewSubscriptions(db).Create(ctx, domain.Subscription{
		TGID:      random.TGID(),
		Platform:  domain.PlatformBoosty,
		Status:    domain.SubActive,
		StartedAt: time.Now(),
	})
	require.Error(t, err, "subscription for an unknown user must violate the FK")
}

func TestActiveSubscriptionIsUnique(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	users := NewUsers(db)
	subs := NewSubscriptions(db)
	tgID := random.TGID()

	require.NoError(t,
		users.Upsert(ctx, domain.User{TGID: tgID, Username: gofakeit.Username()}),
		"upsert user")

	active := domain.Subscription{
		TGID:      tgID,
		Platform:  domain.PlatformBoosty,
		Status:    domain.SubActive,
		StartedAt: time.Now(),
	}

	_, err := subs.Create(ctx, active)
	require.NoError(t, err, "first active subscription")

	// idx_subscriptions_active_unique rejects a second active
	// subscription for the same (user, platform) pair.
	_, err = subs.Create(ctx, active)
	require.Error(t, err, "second active subscription must violate the unique index")

	// The index is partial (WHERE status='active'): an expired
	// subscription for the same pair is allowed.
	expired := active
	expired.Status = domain.SubExpired

	_, err = subs.Create(ctx, expired)
	require.NoError(t, err, "expired subscription on the same pair must be allowed")

	// An active subscription on a different platform is also allowed.
	other := active
	other.Platform = domain.PlatformTribute

	_, err = subs.Create(ctx, other)
	require.NoError(t, err, "active subscription on another platform must be allowed")
}

func TestUsersUpsertAndGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)

	tgID := random.TGID()
	firstName := gofakeit.FirstName()
	name := gofakeit.Username()

	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:      tgID,
		Username:  name,
		FirstName: firstName,
		DMState:   domain.DMOpen,
	}), "upsert")

	got, err := users.Get(ctx, tgID)
	require.NoError(t, err, "get")
	assert.Equal(t, name, got.Username)
	assert.Equal(t, domain.DMOpen, got.DMState)

	// A second upsert updates mutable columns but preserves dm_state.
	renamed := gofakeit.Username()
	require.NoError(t, users.Upsert(ctx, domain.User{TGID: tgID, Username: renamed}),
		"re-upsert")

	got, err = users.Get(ctx, tgID)
	require.NoError(t, err, "get after re-upsert")
	assert.Equal(t, renamed, got.Username)
	assert.Equal(t, domain.DMOpen, got.DMState, "dm_state must be preserved")

	// A banned user keeps its admin fields across a profile refresh.
	bannedID := random.TGID()
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:         bannedID,
		Username:     gofakeit.Username(),
		DMState:      domain.DMBlocked,
		Banned:       true,
		BannedReason: "manual",
		Notes:        "keep",
	}), "upsert banned user")

	refreshed := gofakeit.Username()
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:     bannedID,
		Username: refreshed,
		DMState:  domain.DMOpen,
	}), "refresh banned user")

	got, err = users.Get(ctx, bannedID)
	require.NoError(t, err, "get banned user")
	assert.Equal(t, refreshed, got.Username)
	assert.Equal(t, domain.DMOpen, got.DMState)
	assert.True(t, got.Banned, "ban must be preserved")
	assert.Equal(t, "manual", got.BannedReason)
	assert.Equal(t, "keep", got.Notes)

	_, err = users.Get(ctx, random.TGID())
	require.ErrorIs(t, err, ErrNotFound, "unknown user")
}

func TestMetaGetSet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	meta := NewMeta(db)

	_, ok, err := meta.Get(ctx, "offset")
	require.NoError(t, err)
	assert.False(t, ok, "absent key must report ok=false")

	require.NoError(t, meta.Set(ctx, "offset", "100"), "set")

	v, ok, err := meta.Get(ctx, "offset")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "100", v)

	require.NoError(t, meta.Set(ctx, "offset", "200"), "update")

	v, _, _ = meta.Get(ctx, "offset")
	assert.Equal(t, "200", v, "value must be updated")
}

func TestSubscriptionsGetActive(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	users := NewUsers(db)
	subs := NewSubscriptions(db)
	tgID := random.TGID()

	require.NoError(t,
		users.Upsert(ctx, domain.User{TGID: tgID, Username: gofakeit.Username()}),
		"upsert user")

	// No active subscription yet.
	_, ok, err := subs.GetActive(ctx, tgID, domain.PlatformBoosty)
	require.NoError(t, err)
	require.False(t, ok, "empty store must report no active subscription")

	started := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	expires := started.Add(30 * 24 * time.Hour)
	externalID := gofakeit.UUID()
	periodID := gofakeit.UUID()
	tier := "gold"

	id, err := subs.Create(ctx, domain.Subscription{
		TGID:       tgID,
		Platform:   domain.PlatformBoosty,
		Status:     domain.SubActive,
		ExternalID: externalID,
		PeriodID:   periodID,
		Tier:       tier,
		StartedAt:  started,
		ExpiresAt:  &expires,
		LastSignal: "webhook",
	})
	require.NoError(t, err, "create subscription")

	got, ok, err := subs.GetActive(ctx, tgID, domain.PlatformBoosty)
	require.NoError(t, err, "GetActive")
	require.True(t, ok, "active subscription must be found after Create")
	assert.Equal(t, id, got.ID)
	assert.Equal(t, domain.SubActive, got.Status)
	assert.Equal(t, externalID, got.ExternalID)
	assert.Equal(t, tier, got.Tier)
	assert.True(t, got.StartedAt.Equal(started), "StartedAt must round-trip")
	require.NotNil(t, got.ExpiresAt)
	assert.True(t, got.ExpiresAt.Equal(expires), "ExpiresAt must round-trip")

	// Expire the row via raw SQL — GetActive must no longer find it.
	_, err = db.ExecContext(ctx,
		`UPDATE subscriptions SET status = 'expired', ended_at = ?
		 WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	require.NoError(t, err, "expire subscription")

	_, ok, err = subs.GetActive(ctx, tgID, domain.PlatformBoosty)
	require.NoError(t, err)
	assert.False(t, ok, "expired subscription must not be active")
}

func TestSubscriptionsUpsertExpireAndList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)
	subs := NewSubscriptions(db)
	tgID := random.TGID()

	require.NoError(t,
		users.Upsert(ctx, domain.User{TGID: tgID, Username: gofakeit.Username()}),
		"upsert user")

	started := time.Now().UTC().Truncate(time.Second)
	eventAt := started.Add(time.Minute + 123*time.Millisecond)

	id, err := subs.UpsertActive(ctx, domain.Subscription{
		TGID:        tgID,
		Platform:    domain.PlatformBoosty,
		StartedAt:   started,
		LastSignal:  "event",
		LastEventAt: &eventAt,
	})
	require.NoError(t, err, "first UpsertActive")
	require.NotZero(t, id, "first UpsertActive must return an id")

	id2, err := subs.UpsertActive(ctx, domain.Subscription{
		TGID:          tgID,
		Platform:      domain.PlatformBoosty,
		StartedAt:     started.Add(time.Hour),
		LastSignal:    "on_demand",
		LastCheckedAt: &started,
	})
	require.NoError(t, err, "second UpsertActive")
	require.Equal(t, id, id2, "UpsertActive must update the same row")

	active, err := subs.ListActiveByUser(ctx, tgID)
	require.NoError(t, err, "ListActiveByUser")
	require.Len(t, active, 1)
	require.NotNil(t, active[0].LastEventAt)
	assert.True(t, active[0].LastEventAt.Equal(eventAt), "last_event_at preserved")
	require.NotNil(t, active[0].LastCheckedAt)
	assert.True(t, active[0].LastCheckedAt.Equal(started), "last_checked_at preserved")

	ended := started.Add(2*time.Hour + 456*time.Millisecond)

	ok, err := subs.ExpireActive(ctx, tgID, domain.PlatformBoosty, ended, "event")
	require.NoError(t, err, "ExpireActive")
	require.True(t, ok, "the active subscription must be expired")

	ok, err = subs.ExpireActive(ctx, tgID, domain.PlatformBoosty, ended, "event")
	require.NoError(t, err, "second ExpireActive")
	assert.False(t, ok, "expiring an already-expired pair must be a no-op")

	history, err := subs.ListByUser(ctx, tgID)
	require.NoError(t, err, "ListByUser")
	require.Len(t, history, 1)
	assert.Equal(t, domain.SubExpired, history[0].Status)
	require.NotNil(t, history[0].LastEventAt)
	assert.True(t, history[0].LastEventAt.Equal(ended), "expired last_event_at")
	require.NotNil(t, history[0].LastCheckedAt)
	assert.True(t, history[0].LastCheckedAt.Equal(started),
		"expired last_checked_at preserved")
}

func TestGrantsUpsertAndGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	users := NewUsers(db)
	grants := NewGrants(db)
	tgID := random.TGID()

	require.NoError(t,
		users.Upsert(ctx, domain.User{TGID: tgID, Username: gofakeit.Username()}),
		"upsert user")

	joined := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:     tgID,
		Resource: domain.ResourceChat,
		State:    domain.GrantJoined,
		JoinedAt: &joined,
	}), "upsert grant")

	got, err := grants.Get(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "get grant")
	assert.Equal(t, domain.GrantJoined, got.State)
	assert.Equal(t, "bot", got.AdmittedBy, "admitted_by defaults to bot")
	require.NotNil(t, got.JoinedAt)
	assert.True(t, got.JoinedAt.Equal(joined))
	assert.Nil(t, got.RevokedAt)

	_, err = grants.Get(ctx, random.TGID(), domain.ResourceChat)
	require.ErrorIs(t, err, ErrNotFound, "unknown grant")
}

func TestRepositoryLookupAndRecentLists(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)
	grants := NewGrants(db)
	audit := NewAudit(db)
	revocations := NewRevocations(db)
	tgID := random.TGID()

	// FindByUsername is case-insensitive: store a mixed-case handle and
	// look it up in lower case.
	require.NoError(t, users.Upsert(ctx, domain.User{TGID: tgID, Username: "Known"}),
		"upsert user")

	got, ok, err := users.FindByUsername(ctx, "@known")
	require.NoError(t, err)
	require.True(t, ok, "case-insensitive lookup must find the user")
	assert.Equal(t, tgID, got.TGID)

	_, ok, err = users.FindByUsername(ctx, "@missing")
	require.NoError(t, err)
	assert.False(t, ok, "unknown handle must report ok=false")

	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:     tgID,
		Resource: domain.ResourceChat,
		State:    domain.GrantJoined,
	}), "upsert chat grant")

	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:     tgID,
		Resource: domain.ResourceChannel,
		State:    domain.GrantPending,
	}), "upsert channel grant")

	userGrants, err := grants.ListByUser(ctx, tgID)
	require.NoError(t, err, "ListByUser grants")
	assert.Len(t, userGrants, 2)

	for _, kind := range []string{"old", "new"} {
		require.NoError(t, audit.Append(ctx, AuditEntry{TGID: &tgID, Kind: kind}),
			"append audit %s", kind)
	}

	recent, err := audit.ListRecentByUser(ctx, tgID, 1)
	require.NoError(t, err, "ListRecentByUser")
	require.Len(t, recent, 1)
	assert.Equal(t, "new", recent[0].Kind, "the newest entry must come first")

	_, ok, err = revocations.Get(ctx, tgID)
	require.NoError(t, err)
	assert.False(t, ok, "no revocation scheduled yet")

	scheduled := time.Now().UTC().Add(time.Hour)
	require.NoError(t, revocations.Upsert(ctx, domain.PendingRevocation{
		TGID:        tgID,
		Reason:      "expired",
		ScheduledAt: scheduled,
	}), "upsert revocation")

	rev, ok, err := revocations.Get(ctx, tgID)
	require.NoError(t, err)
	require.True(t, ok, "scheduled revocation must be found")
	assert.Equal(t, "expired", rev.Reason)
}

func TestRevocationsCreateIfAbsentListDueAndMarkNotified(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)
	revocations := NewRevocations(db)
	dueID := random.TGID()
	futureID := random.TGID()

	require.NoError(t, users.Upsert(ctx, domain.User{TGID: dueID}), "upsert due user")
	require.NoError(t, users.Upsert(ctx, domain.User{TGID: futureID}),
		"upsert future user")

	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	created, err := revocations.CreateIfAbsent(ctx, domain.PendingRevocation{
		TGID:        dueID,
		Reason:      "expired",
		ScheduledAt: now.Add(-time.Minute),
	})
	require.NoError(t, err, "first CreateIfAbsent")
	require.True(t, created, "first CreateIfAbsent must insert")

	created, err = revocations.CreateIfAbsent(ctx, domain.PendingRevocation{
		TGID:        dueID,
		Reason:      "changed",
		ScheduledAt: now.Add(time.Hour),
	})
	require.NoError(t, err, "duplicate CreateIfAbsent")
	assert.False(t, created, "duplicate CreateIfAbsent must keep the existing row")

	_, err = revocations.CreateIfAbsent(ctx, domain.PendingRevocation{
		TGID:        futureID,
		Reason:      "future",
		ScheduledAt: now.Add(time.Hour),
	})
	require.NoError(t, err, "future CreateIfAbsent")

	require.NoError(t, revocations.MarkNotified(ctx, dueID), "MarkNotified")

	due, err := revocations.ListDue(ctx, now, 10)
	require.NoError(t, err, "ListDue")
	require.Len(t, due, 1, "only the past-due revocation is due")
	assert.Equal(t, dueID, due[0].TGID)
	assert.True(t, due[0].Notified)
}

func TestManualAccessBanEligibleGrantsAndCleanup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)
	subs := NewSubscriptions(db)
	grants := NewGrants(db)
	cleanup := NewCleanup(db)
	tgID := random.TGID()

	require.NoError(t, users.EnsureStub(ctx, tgID), "EnsureStub")

	expires := time.Now().UTC().Add(time.Hour)
	_, err := subs.UpsertManual(ctx, tgID, &expires, "manual")
	require.NoError(t, err, "UpsertManual")

	require.NoError(t, users.SetBanned(ctx, tgID, true, "abuse"), "SetBanned")

	user, err := users.Get(ctx, tgID)
	require.NoError(t, err, "Get")
	assert.True(t, user.Banned)
	assert.Equal(t, "abuse", user.BannedReason)

	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert bot grant")

	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChannel,
		State:      domain.GrantJoined,
		AdmittedBy: "external",
	}), "upsert external grant")

	// Only bot-admitted grants are eligible for revocation.
	eligible, err := grants.ListEligibleForRevoke(ctx, tgID)
	require.NoError(t, err, "ListEligibleForRevoke")
	require.Len(t, eligible, 1)
	assert.Equal(t, domain.ResourceChat, eligible[0].Resource)

	ok, err := grants.Revoke(ctx, tgID, domain.ResourceChat, "expired")
	require.NoError(t, err, "Revoke")
	require.True(t, ok, "the bot grant must be revoked")

	_, err = grants.MarkPending(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "MarkPending after revoke")

	restored, err := grants.Get(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "Get restored grant")
	assert.Equal(t, domain.GrantPending, restored.State)
	assert.Equal(t, "bot", restored.AdmittedBy)
	assert.Nil(t, restored.RevokedAt)
	assert.Empty(t, restored.RevokedReason)

	cutoff := time.Now().UTC().Add(-time.Hour)
	_, err = db.ExecContext(ctx, `
		INSERT INTO telegram_updates (
			update_id, update_type, payload_json, status, error,
			received_at, processed_at)
		VALUES
			(1, 'message', '{}', 'processed', '', ?, ?),
			(2, 'message', '{}', 'failed', 'boom', ?, ?)`,
		cutoff.Add(-time.Hour).Format(time.RFC3339),
		cutoff.Add(-time.Hour).Format(time.RFC3339),
		cutoff.Add(-time.Hour).Format(time.RFC3339),
		cutoff.Add(-time.Hour).Format(time.RFC3339))
	require.NoError(t, err, "seed updates")

	// Only terminal (processed) updates older than the cutoff are purged.
	deleted, err := cleanup.DeleteTerminalTelegramUpdates(ctx, cutoff)
	require.NoError(t, err, "DeleteTerminalTelegramUpdates")
	assert.Equal(t, int64(1), deleted, "only the processed row is purged")
}

func TestAlertsDedupeAndDurableDelivery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	outbox := NewOutbox(db)
	ownerID := random.TGID()
	alerts := NewAlertsWithDelivery(db, outbox, []int64{ownerID}, nil)

	input := AlertInput{
		Severity:  "critical",
		Kind:      "bot_rights_lost",
		Title:     "rights lost",
		Detail:    "chat_id=-1001",
		DedupeKey: "rights:-1001",
	}

	id, created, err := alerts.CreateOpenIfMissing(ctx, input)
	require.NoError(t, err, "CreateOpenIfMissing")
	require.True(t, created, "first alert must be created")
	require.NotZero(t, id)

	_, created, err = alerts.CreateOpenIfMissing(ctx, input)
	require.NoError(t, err, "duplicate CreateOpenIfMissing")
	assert.False(t, created, "duplicate alert must dedupe")

	var actions int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM access_actions
		WHERE action_type = 'send_dm' AND tg_id = ?`, ownerID).Scan(&actions),
		"count alert deliveries")
	assert.Equal(t, 1, actions, "the alert is delivered once to the owner")
}

// TestDatabaseFileMode is the regression test for SPEC §22.1: the
// database file must not be world-readable. Open pre-creates it 0600,
// which sql.Open would otherwise leave at the default 0644.
func TestDatabaseFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mode.db")

	db, err := Open(context.Background(), path)
	require.NoError(t, err, "open database")

	t.Cleanup(func() { _ = db.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err, "stat database file")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestInviteLinksPartialUniqueIndexes checks the partial unique indexes
// on invite_links: at most one active (created/sent) link may occupy a
// slot, while inactive links (e.g. expired) leave the slot free. Phase
// 01 has no invite repository, so rows are inserted with raw SQL.
func TestInviteLinksPartialUniqueIndexes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)
	alice := random.TGID()
	bob := random.TGID()

	// personal/direct links carry a NOT NULL tg_id (FK + CHECK).
	require.NoError(t, users.Upsert(ctx, domain.User{TGID: alice}), "upsert alice")
	require.NoError(t, users.Upsert(ctx, domain.User{TGID: bob}), "upsert bob")

	insertInvite := func(tgID *int64, resource, mode, status, suffix string) error {
		_, err := db.ExecContext(ctx, `
			INSERT INTO invite_links (
				tg_id, resource, mode, invite_link, invite_link_hash,
				status, creates_join_request, created_at, updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, 1,
			        '2026-05-22T00:00:00Z', '2026-05-22T00:00:00Z')`,
			nullableInt64(tgID), resource, mode,
			"https://t.me/+"+suffix, "hash-"+suffix, status)

		return err
	}

	// shared_join_request: one active link per (resource, mode); an
	// expired link does not occupy the slot.
	require.NoError(t,
		insertInvite(nil, "chat", "shared_join_request", "created", "shared-1"),
		"first shared invite")
	require.Error(t,
		insertInvite(nil, "chat", "shared_join_request", "sent", "shared-2"),
		"second active shared invite must violate the unique index")
	require.NoError(t,
		insertInvite(nil, "chat", "shared_join_request", "expired", "shared-3"),
		"expired shared invite must be allowed")

	// personal_join_request: one active link per (tg_id, resource, mode).
	require.NoError(t,
		insertInvite(&alice, "chat", "personal_join_request", "created", "personal-1"),
		"first personal invite")
	require.Error(t,
		insertInvite(&alice, "chat", "personal_join_request", "sent", "personal-2"),
		"second active personal invite must violate the unique index")
	// A different user does not share the slot.
	require.NoError(t,
		insertInvite(&bob, "chat", "personal_join_request", "created", "personal-3"),
		"personal invite for another user must be allowed")

	// direct shares the partial index with personal_join_request.
	require.NoError(t,
		insertInvite(&alice, "channel", "direct", "created", "direct-1"),
		"first direct invite")
	require.Error(t,
		insertInvite(&alice, "channel", "direct", "sent", "direct-2"),
		"second active direct invite must violate the unique index")
}

// nullableInt64 turns an optional ID into a SQL NULL or its value.
func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}

	return *v
}
