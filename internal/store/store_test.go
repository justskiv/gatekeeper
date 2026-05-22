package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// newTestDB opens a fresh on-disk database in a temp directory and
// applies the migrations.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestMigrateCreatesSchema(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// schema_migrations records exactly migration 0001.
	var (
		version int
		name    string
	)
	if err := db.QueryRowContext(ctx,
		`SELECT version, name FROM schema_migrations`).Scan(&version, &name); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if version != 1 || name != "init" {
		t.Fatalf("schema_migrations = (%d, %q), want (1, \"init\")", version, name)
	}

	// All 13 tables exist: 12 domain/ops tables plus schema_migrations.
	wantTables := []string{
		"users", "subscriptions", "access_grants", "pending_revocations",
		"whitelist", "invite_links", "telegram_updates", "tribute_events",
		"access_actions", "audit_log", "admin_alerts", "meta",
		"schema_migrations",
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

func TestMigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t) // already migrated once
	ctx := context.Background()

	if err := Migrate(db); err != nil {
		t.Fatalf("second migrate run: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("schema_migrations has %d rows after re-run, want 1", count)
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
	if got, _ = users.Get(ctx, 42); got.Username != "robert" {
		t.Errorf("username not updated: %q", got.Username)
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
