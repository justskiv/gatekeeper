package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// newTestDB opens a fresh on-disk database in a temp directory and
// applies every migration from the top-level migrations/ directory.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
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

// migrationsDir resolves the repo's top-level migrations/ directory
// relative to this test file, so the path holds regardless of the
// `go test` invocation cwd.
func migrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot resolve migrations dir")
	}

	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

func TestMigrateCreatesSchema(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// goose_db_version records at least one applied migration.
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT max(version_id) FROM goose_db_version WHERE is_applied = 1`,
	).Scan(&version); err != nil {
		t.Fatalf("query goose_db_version: %v", err)
	}

	if !version.Valid || version.Int64 < 1 {
		t.Fatalf("goose_db_version max = %v, want >= 1", version)
	}

	// All 12 domain/ops tables exist.
	wantTables := []string{
		"users", "subscriptions", "access_grants", "pending_revocations",
		"whitelist", "invite_links", "telegram_updates", "tribute_events",
		"access_actions", "audit_log", "admin_alerts", "meta",
	}
	for _, table := range wantTables {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`,
			table).Scan(&n); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}

		if n != 1 {
			t.Errorf("table %s is missing", table)
		}
	}
}

func TestCheckSchemaErrorsOnEmptyDB(t *testing.T) {
	// Open without migrating — CheckSchema must reject the db.
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	err = CheckSchema(context.Background(), db)
	if !errors.Is(err, ErrUnmigrated) {
		t.Fatalf("CheckSchema on empty db = %v, want ErrUnmigrated", err)
	}

	if !strings.Contains(err.Error(), "task migrate:up") {
		t.Errorf("error message %q lacks the migrate:up instruction",
			err.Error())
	}
}

func TestCheckSchemaPassesOnMigratedDB(t *testing.T) {
	db := newTestDB(t)
	if err := CheckSchema(context.Background(), db); err != nil {
		t.Fatalf("CheckSchema on migrated db: %v", err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	var fk int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}

	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}

	// A subscription that references a non-existent user must be rejected.
	_, err := NewSubscriptions(db).Create(ctx, domain.Subscription{
		TGID:      999,
		Platform:  domain.PlatformBoosty,
		Status:    domain.SubActive,
		StartedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected a foreign-key violation for an unknown user, got nil")
	}
}

func TestActiveSubscriptionIsUnique(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	users := NewUsers(db)
	subs := NewSubscriptions(db)

	if err := users.Upsert(ctx, domain.User{TGID: 1, Username: "alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	active := domain.Subscription{
		TGID:      1,
		Platform:  domain.PlatformBoosty,
		Status:    domain.SubActive,
		StartedAt: time.Now(),
	}
	if _, err := subs.Create(ctx, active); err != nil {
		t.Fatalf("first active subscription: %v", err)
	}

	// idx_subscriptions_active_unique rejects a second active
	// subscription for the same (user, platform) pair.
	if _, err := subs.Create(ctx, active); err == nil {
		t.Fatal("expected a unique-index violation for the second active subscription")
	}

	// The index is partial (WHERE status='active'): an expired
	// subscription for the same pair is allowed.
	expired := active

	expired.Status = domain.SubExpired
	if _, err := subs.Create(ctx, expired); err != nil {
		t.Fatalf("expired subscription should be allowed: %v", err)
	}

	// An active subscription on a different platform is also allowed.
	other := active

	other.Platform = domain.PlatformTribute
	if _, err := subs.Create(ctx, other); err != nil {
		t.Fatalf("active subscription on another platform should be allowed: %v", err)
	}
}

func TestUsersUpsertAndGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)

	if err := users.Upsert(ctx, domain.User{
		TGID:      42,
		Username:  "bob",
		FirstName: "Bob",
		DMState:   domain.DMOpen,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := users.Get(ctx, 42)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Username != "bob" || got.DMState != domain.DMOpen {
		t.Errorf("got %+v, want username=bob dm_state=open", got)
	}

	// A second upsert updates the mutable columns.
	if err := users.Upsert(ctx, domain.User{TGID: 42, Username: "robert"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	if got, _ = users.Get(ctx, 42); got.Username != "robert" ||
		got.DMState != domain.DMOpen {
		t.Errorf("refreshed user = %+v, want username=robert dm_state preserved", got)
	}

	if err := users.Upsert(ctx, domain.User{
		TGID:         99,
		Username:     "blocked",
		DMState:      domain.DMBlocked,
		Banned:       true,
		BannedReason: "manual",
		Notes:        "keep",
	}); err != nil {
		t.Fatalf("upsert banned user: %v", err)
	}

	if err := users.Upsert(ctx, domain.User{
		TGID:     99,
		Username: "fresh",
		DMState:  domain.DMOpen,
	}); err != nil {
		t.Fatalf("refresh banned user: %v", err)
	}

	got, err = users.Get(ctx, 99)
	if err != nil {
		t.Fatalf("get banned user: %v", err)
	}

	if got.Username != "fresh" ||
		got.DMState != domain.DMOpen ||
		!got.Banned ||
		got.BannedReason != "manual" ||
		got.Notes != "keep" {
		t.Errorf("refreshed user = %+v, want admin fields preserved", got)
	}

	if _, err := users.Get(ctx, 7777); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown user, got %v", err)
	}
}

func TestMetaGetSet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	meta := NewMeta(db)

	if _, ok, err := meta.Get(ctx, "offset"); err != nil || ok {
		t.Fatalf("absent key: ok=%v err=%v", ok, err)
	}

	if err := meta.Set(ctx, "offset", "100"); err != nil {
		t.Fatalf("set: %v", err)
	}

	v, ok, err := meta.Get(ctx, "offset")
	if err != nil || !ok || v != "100" {
		t.Fatalf("get = (%q, %v, %v), want (\"100\", true, nil)", v, ok, err)
	}

	if err := meta.Set(ctx, "offset", "200"); err != nil {
		t.Fatalf("update: %v", err)
	}

	if v, _, _ = meta.Get(ctx, "offset"); v != "200" {
		t.Errorf("value not updated: %q", v)
	}
}

func TestSubscriptionsGetActive(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	users := NewUsers(db)
	subs := NewSubscriptions(db)

	if err := users.Upsert(ctx, domain.User{TGID: 1, Username: "alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	// No active subscription yet.
	if _, ok, err := subs.GetActive(ctx, 1, domain.PlatformBoosty); err != nil || ok {
		t.Fatalf("GetActive on empty store: ok=%v err=%v", ok, err)
	}

	// RFC3339 storage rounds to seconds; truncate inputs to compare.
	started := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	expires := started.Add(30 * 24 * time.Hour)

	id, err := subs.Create(ctx, domain.Subscription{
		TGID:       1,
		Platform:   domain.PlatformBoosty,
		Status:     domain.SubActive,
		ExternalID: "ext-1",
		PeriodID:   "p-1",
		Tier:       "gold",
		StartedAt:  started,
		ExpiresAt:  &expires,
		LastSignal: "webhook",
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	got, ok, err := subs.GetActive(ctx, 1, domain.PlatformBoosty)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}

	if !ok {
		t.Fatal("GetActive ok = false after Create, want true")
	}

	if got.ID != id {
		t.Errorf("ID = %d, want %d", got.ID, id)
	}

	if got.Status != domain.SubActive {
		t.Errorf("Status = %s, want active", got.Status)
	}

	if got.ExternalID != "ext-1" || got.Tier != "gold" {
		t.Errorf("fields mismatch: %+v", got)
	}

	if !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
	}

	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
	}

	// Expire the row via raw SQL — GetActive must no longer find it.
	if _, err := db.ExecContext(ctx,
		`UPDATE subscriptions SET status = 'expired', ended_at = ?
		 WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id); err != nil {
		t.Fatalf("expire subscription: %v", err)
	}

	if _, ok, err := subs.GetActive(ctx, 1, domain.PlatformBoosty); err != nil || ok {
		t.Fatalf("GetActive after expire: ok=%v err=%v", ok, err)
	}
}

func TestSubscriptionsUpsertExpireAndList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)
	subs := NewSubscriptions(db)

	if err := users.Upsert(ctx, domain.User{TGID: 11, Username: "alice"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	started := time.Now().UTC().Truncate(time.Second)
	eventAt := started.Add(time.Minute)

	id, err := subs.UpsertActive(ctx, domain.Subscription{
		TGID:        11,
		Platform:    domain.PlatformBoosty,
		StartedAt:   started,
		LastSignal:  "event",
		LastEventAt: &eventAt,
	})
	if err != nil {
		t.Fatalf("first UpsertActive: %v", err)
	}

	if id == 0 {
		t.Fatal("first UpsertActive returned id 0")
	}

	if id2, err := subs.UpsertActive(ctx, domain.Subscription{
		TGID:          11,
		Platform:      domain.PlatformBoosty,
		StartedAt:     started.Add(time.Hour),
		LastSignal:    "on_demand",
		LastCheckedAt: &started,
	}); err != nil {
		t.Fatalf("second UpsertActive: %v", err)
	} else if id2 != id {
		t.Fatalf("second UpsertActive id = %d, want %d", id2, id)
	}

	active, err := subs.ListActiveByUser(ctx, 11)
	if err != nil {
		t.Fatalf("ListActiveByUser: %v", err)
	}

	if len(active) != 1 {
		t.Fatalf("active subscriptions = %+v, want one row", active)
	}

	if active[0].LastEventAt == nil || !active[0].LastEventAt.Equal(eventAt) {
		t.Fatalf("last_event_at = %v, want preserved %v",
			active[0].LastEventAt, eventAt)
	}

	if active[0].LastCheckedAt == nil ||
		!active[0].LastCheckedAt.Equal(started) {
		t.Fatalf("last_checked_at = %v, want %v",
			active[0].LastCheckedAt, started)
	}

	ended := started.Add(2 * time.Hour)

	ok, err := subs.ExpireActive(ctx, 11, domain.PlatformBoosty, ended, "event")
	if err != nil {
		t.Fatalf("ExpireActive: %v", err)
	}

	if !ok {
		t.Fatal("ExpireActive ok = false, want true")
	}

	ok, err = subs.ExpireActive(ctx, 11, domain.PlatformBoosty, ended, "event")
	if err != nil {
		t.Fatalf("second ExpireActive: %v", err)
	}

	if ok {
		t.Fatal("second ExpireActive ok = true, want false")
	}

	history, err := subs.ListByUser(ctx, 11)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}

	if len(history) != 1 || history[0].Status != domain.SubExpired {
		t.Fatalf("history = %+v, want one expired row", history)
	}

	if history[0].LastEventAt == nil || !history[0].LastEventAt.Equal(ended) {
		t.Fatalf("expired last_event_at = %v, want %v",
			history[0].LastEventAt, ended)
	}

	if history[0].LastCheckedAt == nil ||
		!history[0].LastCheckedAt.Equal(started) {
		t.Fatalf("expired last_checked_at = %v, want preserved %v",
			history[0].LastCheckedAt, started)
	}
}

func TestGrantsUpsertAndGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	users := NewUsers(db)
	grants := NewGrants(db)

	if err := users.Upsert(ctx, domain.User{TGID: 7, Username: "carol"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	joined := time.Now().UTC().Truncate(time.Second)
	if err := grants.Upsert(ctx, domain.AccessGrant{
		TGID:     7,
		Resource: domain.ResourceChat,
		State:    domain.GrantJoined,
		JoinedAt: &joined,
	}); err != nil {
		t.Fatalf("upsert grant: %v", err)
	}

	got, err := grants.Get(ctx, 7, domain.ResourceChat)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}

	if got.State != domain.GrantJoined {
		t.Errorf("State = %s, want joined", got.State)
	}

	if got.AdmittedBy != "bot" {
		t.Errorf("AdmittedBy = %q, want bot (default)", got.AdmittedBy)
	}

	if got.JoinedAt == nil || !got.JoinedAt.Equal(joined) {
		t.Errorf("JoinedAt = %v, want %v", got.JoinedAt, joined)
	}

	if got.RevokedAt != nil {
		t.Errorf("RevokedAt = %v, want nil", got.RevokedAt)
	}

	if _, err := grants.Get(ctx, 999, domain.ResourceChat); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown grant, got %v", err)
	}
}

func TestRepositoryLookupAndRecentLists(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)
	grants := NewGrants(db)
	audit := NewAudit(db)
	revocations := NewRevocations(db)

	if err := users.Upsert(ctx, domain.User{TGID: 12, Username: "Known"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if got, ok, err := users.FindByUsername(ctx, "@known"); err != nil || !ok ||
		got.TGID != 12 {
		t.Fatalf("FindByUsername = (%+v, %v, %v), want tg_id 12", got, ok, err)
	}

	if _, ok, err := users.FindByUsername(ctx, "@missing"); err != nil || ok {
		t.Fatalf("FindByUsername missing = (_, %v, %v), want false nil", ok, err)
	}

	if err := grants.Upsert(ctx, domain.AccessGrant{
		TGID:     12,
		Resource: domain.ResourceChat,
		State:    domain.GrantJoined,
	}); err != nil {
		t.Fatalf("upsert chat grant: %v", err)
	}

	if err := grants.Upsert(ctx, domain.AccessGrant{
		TGID:     12,
		Resource: domain.ResourceChannel,
		State:    domain.GrantPending,
	}); err != nil {
		t.Fatalf("upsert channel grant: %v", err)
	}

	userGrants, err := grants.ListByUser(ctx, 12)
	if err != nil {
		t.Fatalf("ListByUser grants: %v", err)
	}

	if len(userGrants) != 2 {
		t.Fatalf("grants = %+v, want two rows", userGrants)
	}

	tgID := int64(12)
	for _, kind := range []string{"old", "new"} {
		if err := audit.Append(ctx, AuditEntry{
			TGID: &tgID,
			Kind: kind,
		}); err != nil {
			t.Fatalf("append audit %s: %v", kind, err)
		}
	}

	recent, err := audit.ListRecentByUser(ctx, 12, 1)
	if err != nil {
		t.Fatalf("ListRecentByUser: %v", err)
	}

	if len(recent) != 1 || recent[0].Kind != "new" {
		t.Fatalf("recent audit = %+v, want newest only", recent)
	}

	if _, ok, err := revocations.Get(ctx, 12); err != nil || ok {
		t.Fatalf("missing revocation = (_, %v, %v), want false nil", ok, err)
	}

	scheduled := time.Now().UTC().Add(time.Hour)
	if err := revocations.Upsert(ctx, domain.PendingRevocation{
		TGID:        12,
		Reason:      "expired",
		ScheduledAt: scheduled,
	}); err != nil {
		t.Fatalf("upsert revocation: %v", err)
	}

	if got, ok, err := revocations.Get(ctx, 12); err != nil || !ok ||
		got.Reason != "expired" {
		t.Fatalf("revocation = (%+v, %v, %v), want reason expired", got, ok, err)
	}
}

// TestDatabaseFileMode is the regression test for SPEC §22.1: the
// database file must not be world-readable. Open pre-creates it 0600,
// which sql.Open would otherwise leave at the default 0644.
func TestDatabaseFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mode.db")

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database file: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("database file mode = %#o, want 0600", perm)
	}
}

// TestInviteLinksPartialUniqueIndexes checks the partial unique indexes
// on invite_links: at most one active (created/sent) link may occupy a
// slot, while inactive links (e.g. expired) leave the slot free. Phase
// 01 has no invite repository, so rows are inserted with raw SQL.
func TestInviteLinksPartialUniqueIndexes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := NewUsers(db)

	// personal/direct links carry a NOT NULL tg_id (FK + CHECK).
	if err := users.Upsert(ctx, domain.User{TGID: 1001, Username: "alice"}); err != nil {
		t.Fatalf("upsert user 1001: %v", err)
	}

	if err := users.Upsert(ctx, domain.User{TGID: 1002, Username: "bob"}); err != nil {
		t.Fatalf("upsert user 1002: %v", err)
	}

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
	if err := insertInvite(nil, "chat", "shared_join_request", "created", "shared-1"); err != nil {
		t.Fatalf("first shared invite: %v", err)
	}

	if err := insertInvite(nil, "chat", "shared_join_request", "sent", "shared-2"); err == nil {
		t.Fatal("expected a unique-index violation for the second active shared invite")
	}

	if err := insertInvite(nil, "chat", "shared_join_request", "expired", "shared-3"); err != nil {
		t.Fatalf("expired shared invite should be allowed: %v", err)
	}

	// personal_join_request: one active link per (tg_id, resource, mode).
	alice := int64(1001)
	if err := insertInvite(
		&alice, "chat", "personal_join_request", "created", "personal-1",
	); err != nil {
		t.Fatalf("first personal invite: %v", err)
	}

	if err := insertInvite(&alice, "chat", "personal_join_request", "sent", "personal-2"); err == nil {
		t.Fatal("expected a unique-index violation for the second active personal invite")
	}
	// A different user does not share the slot.
	bob := int64(1002)
	if err := insertInvite(
		&bob, "chat", "personal_join_request", "created", "personal-3",
	); err != nil {
		t.Fatalf("personal invite for another user should be allowed: %v", err)
	}

	// direct shares the partial index with personal_join_request.
	if err := insertInvite(&alice, "channel", "direct", "created", "direct-1"); err != nil {
		t.Fatalf("first direct invite: %v", err)
	}

	if err := insertInvite(&alice, "channel", "direct", "sent", "direct-2"); err == nil {
		t.Fatal("expected a unique-index violation for the second active direct invite")
	}
}

// nullableInt64 turns an optional ID into a SQL NULL or its value.
func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}

	return *v
}
