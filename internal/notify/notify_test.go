package notify

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

type fakeSender struct {
	calls int
	err   error
}

func (s *fakeSender) SendMessage(context.Context, int64, string) error {
	s.calls++

	return s.err
}

type blockedDMError struct{}

func (blockedDMError) Error() string {
	return "blocked"
}

func (blockedDMError) TelegramCategory() string {
	return "dm_blocked"
}

func TestSendDMSkipsKnownBlockedUser(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	users := store.NewUsers(db)
	if err := users.Upsert(ctx, domain.User{
		TGID:    1,
		DMState: domain.DMBlocked,
	}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	sender := &fakeSender{}

	if err := New(users, sender, nil).SendDM(ctx, 1, "hello"); err != nil {
		t.Fatalf("SendDM: %v", err)
	}

	if sender.calls != 0 {
		t.Fatalf("send calls = %d, want 0", sender.calls)
	}
}

func TestSendDMMarksBlockedWithoutRetry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	users := store.NewUsers(db)
	if err := users.Upsert(ctx, domain.User{
		TGID:    2,
		DMState: domain.DMOpen,
	}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	sender := &fakeSender{err: blockedDMError{}}

	if err := New(users, sender, nil).SendDM(ctx, 2, "hello"); err != nil {
		t.Fatalf("SendDM: %v", err)
	}

	if sender.calls != 1 {
		t.Fatalf("send calls = %d, want 1", sender.calls)
	}

	user, err := users.Get(ctx, 2)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	if user.DMState != domain.DMBlocked {
		t.Fatalf("dm_state = %s, want blocked", user.DMState)
	}
}

func TestSendDMReturnsNonBlockedErrors(t *testing.T) {
	db := newTestDB(t)

	err := New(store.NewUsers(db), &fakeSender{err: errors.New("network")}, nil).
		SendDM(context.Background(), 3, "hello")
	if err == nil {
		t.Fatal("SendDM returned nil, want error")
	}
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
