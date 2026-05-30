package bot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

func TestStartRegistersUserIdempotently(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	handler := NewUserCommands(store.NewUsers(db), nil)
	msg := privateMessage(1, 42, "/start")

	for i := 0; i < 2; i++ {
		result, err := handler.HandlePrivate(ctx, msg)
		if err != nil {
			t.Fatalf("HandlePrivate: %v", err)
		}
		if result.Ignored || len(result.Replies) != 1 {
			t.Fatalf("result = %+v, want one reply", result)
		}
	}

	user, err := store.NewUsers(db).Get(ctx, 42)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.DMState != domain.DMOpen {
		t.Fatalf("dm_state = %s, want open", user.DMState)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&rows); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if rows != 1 {
		t.Fatalf("users rows = %d, want 1", rows)
	}
}

func TestNonCommandPrivateTextBehavesLikeStart(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	handler := NewUserCommands(store.NewUsers(db), nil)

	result, err := handler.HandlePrivate(ctx, privateMessage(1, 43, "hello"))
	if err != nil {
		t.Fatalf("HandlePrivate: %v", err)
	}
	if result.Ignored || len(result.Replies) != 1 {
		t.Fatalf("result = %+v, want one reply", result)
	}
	if _, err := store.NewUsers(db).Get(ctx, 43); err != nil {
		t.Fatalf("get user: %v", err)
	}
}

func TestHelpReplies(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := store.NewUsers(db)
	handler := NewUserCommands(users, nil)

	result, err := handler.HandlePrivate(
		ctx, privateMessage(1, 44, "/help"))
	if err != nil {
		t.Fatalf("HandlePrivate: %v", err)
	}
	if result.Ignored || len(result.Replies) != 1 || result.Replies[0].Text == "" {
		t.Fatalf("result = %+v, want help reply", result)
	}
	user, err := users.Get(ctx, 44)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.DMState != domain.DMOpen {
		t.Fatalf("dm_state = %s, want open", user.DMState)
	}
}

func TestHereIsOwnerOnly(t *testing.T) {
	db := newTestDB(t)
	handler := NewUserCommands(store.NewUsers(db), []int64{100})

	ownerResult := handler.HandleHere(groupMessage(1, -1001, 100, "/here"))
	if ownerResult.Ignored || len(ownerResult.Replies) != 1 {
		t.Fatalf("owner result = %+v, want one reply", ownerResult)
	}

	otherResult := handler.HandleHere(groupMessage(2, -1001, 101, "/here"))
	if !otherResult.Ignored || len(otherResult.Replies) != 0 {
		t.Fatalf("other result = %+v, want ignored", otherResult)
	}
}

func privateMessage(updateID, tgID int64, text string) *models.Message {
	return &models.Message{
		ID:   int(updateID),
		From: &models.User{ID: tgID, FirstName: "Test"},
		Chat: models.Chat{ID: tgID, Type: models.ChatTypePrivate},
		Text: text,
	}
}

func groupMessage(updateID, chatID, tgID int64, text string) *models.Message {
	return &models.Message{
		ID:   int(updateID),
		From: &models.User{ID: tgID, FirstName: "Owner"},
		Chat: models.Chat{ID: chatID, Type: models.ChatTypeSupergroup},
		Text: text,
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
