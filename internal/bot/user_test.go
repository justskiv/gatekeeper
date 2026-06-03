package bot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

func TestStartRegistersUserIdempotently(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	handler := NewUserCommands(store.NewUsers(db), nil)
	msg := privateMessage(1, 42, "/start")

	for range 2 {
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

func TestStatusEnsuresUserAndAppliesPreflight(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	statusEngine := engine.New(nil)

	handler := NewCommands(CommandDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Audit:         store.NewAudit(db),
		Whitelist:     store.NewWhitelist(db),
		StatusEngine:  statusEngine,
		Preflight: &engine.Snapshot{
			TGID: 55,
			Verdicts: []domain.SourceVerdict{{
				Source:  domain.PlatformBoosty,
				Verdict: domain.VerdictActive,
				Detail:  messages.ReasonMembershipInChat(-1001),
			}},
			Decision: domain.AccessDecision{
				TGID:    55,
				Status:  domain.StatusActive,
				Allowed: true,
				Reasons: []domain.AccessReason{{
					Source:  domain.PlatformBoosty,
					Verdict: domain.VerdictActive,
					Detail:  messages.ReasonMembershipInChat(-1001),
				}},
			},
		},
	}, nil)

	result, err := handler.HandlePrivate(ctx, privateMessage(1, 55, "/status"))
	if err != nil {
		t.Fatalf("HandlePrivate: %v", err)
	}

	if result.Ignored || len(result.Replies) != 1 {
		t.Fatalf("result = %+v, want one status reply", result)
	}

	if !strings.Contains(result.Replies[0].Text, "Доступ активен") {
		t.Fatalf("reply = %q, want status text", result.Replies[0].Text)
	}

	if strings.Contains(result.Replies[0].Text, "Проверка источников") ||
		strings.Contains(result.Replies[0].Text, messages.ReasonMembershipInChat(-1001)) {
		t.Fatalf("reply = %q, want user status without source reasons",
			result.Replies[0].Text)
	}

	user, err := store.NewUsers(db).Get(ctx, 55)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	if user.DMState != domain.DMOpen {
		t.Fatalf("dm_state = %s, want open", user.DMState)
	}

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 55, domain.PlatformBoosty)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}

	if !ok || sub.LastSignal != "on_demand" {
		t.Fatalf("subscription = (%+v, %v), want on-demand active", sub, ok)
	}
}

func TestStatusFallsBackToPersistedDecision(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := store.NewUsers(db)

	if err := users.Upsert(ctx, domain.User{TGID: 56}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if err := store.NewWhitelist(db).Add(ctx, 56, 100, "test"); err != nil {
		t.Fatalf("add whitelist: %v", err)
	}

	handler := NewCommands(CommandDeps{
		Users:         users,
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Audit:         store.NewAudit(db),
		Whitelist:     store.NewWhitelist(db),
		StatusEngine:  engine.New(nil),
	}, nil)

	result, err := handler.HandlePrivate(ctx, privateMessage(1, 56, "/status"))
	if err != nil {
		t.Fatalf("HandlePrivate: %v", err)
	}

	if result.Ignored || len(result.Replies) != 1 {
		t.Fatalf("result = %+v, want one status reply", result)
	}

	if !strings.Contains(result.Replies[0].Text, "Доступ активен") {
		t.Fatalf("reply = %q, want persisted active decision", result.Replies[0].Text)
	}

	if strings.Contains(result.Replies[0].Text, "белый список") {
		t.Fatalf("reply = %q, want user status without internal basis",
			result.Replies[0].Text)
	}
}

func TestUserStatusHidesInternalDiagnostics(t *testing.T) {
	text := messages.Status(domain.AccessDecision{
		Status: domain.StatusInactive,
		Reasons: []domain.AccessReason{{
			Source:  domain.Platform("system"),
			Verdict: domain.VerdictUnknown,
			Detail:  "whitelist chat_id=-1001 reason=local database failed",
		}},
	}, nil, nil, nil, false)

	for _, forbidden := range []string{
		"whitelist",
		"system",
		"chat_id",
		"reason=",
		"local",
		"database",
		"Проверка источников",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status text = %q, contains forbidden %q", text, forbidden)
		}
	}
}

func TestFormattedReplySplitKeepsTagsOnLineBoundaries(t *testing.T) {
	text := strings.Join([]string{
		"<b>Заголовок</b>",
		"строка с <code>safe&amp;value</code>",
		"ещё строка",
	}, "\n")

	parts := splitFormattedReply(text, len("<b>Заголовок</b>\n")+1)
	if len(parts) < 2 {
		t.Fatalf("parts = %q, want split", parts)
	}

	for _, part := range parts {
		if strings.Contains(part, "<code>") && !strings.Contains(part, "</code>") {
			t.Fatalf("part = %q, split inside code tag", part)
		}
		if strings.Contains(part, "&amp") && !strings.Contains(part, "&amp;") {
			t.Fatalf("part = %q, split inside entity", part)
		}
	}
}

func TestOwnerReplySplitsLongFormattedDiagnostics(t *testing.T) {
	handler := NewCommands(CommandDeps{}, []int64{100})
	line := "<code>" + strings.Repeat("x", 80) + "</code>\n"
	text := "<b>Диагностика</b>\n" + strings.Repeat(line, 80)

	result := handler.ownerReply(privateMessage(1, 100, "/whois 42"), text)
	if len(result.Replies) < 2 {
		t.Fatalf("replies = %d, want split long diagnostics", len(result.Replies))
	}

	for _, reply := range result.Replies {
		if len(reply.Text) > replyChunkSize {
			t.Fatalf("reply length = %d, want <= %d", len(reply.Text), replyChunkSize)
		}
		if reply.ParseMode != messages.ParseModeHTML {
			t.Fatalf("parse_mode = %q, want HTML", reply.ParseMode)
		}
		if strings.Count(reply.Text, "<code>") != strings.Count(reply.Text, "</code>") {
			t.Fatalf("reply = %q, unbalanced code tags", reply.Text)
		}
	}
}

func TestWhoisOwnerByIDAndUsername(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := store.NewUsers(db)

	if err := users.Upsert(ctx, domain.User{
		TGID:      77,
		Username:  "known",
		FirstName: "Known",
		DMState:   domain.DMOpen,
	}); err != nil {
		t.Fatalf("upsert target user: %v", err)
	}

	if err := users.Upsert(ctx, domain.User{TGID: 100, Username: "owner"}); err != nil {
		t.Fatalf("upsert owner: %v", err)
	}

	started := time.Now().UTC().Truncate(time.Second)
	if _, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:       77,
		Platform:   domain.PlatformTribute,
		StartedAt:  started,
		LastSignal: "event",
	}); err != nil {
		t.Fatalf("upsert subscription: %v", err)
	}

	handler := NewCommands(CommandDeps{
		Users:         users,
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Audit:         store.NewAudit(db),
		Whitelist:     store.NewWhitelist(db),
		StatusEngine:  engine.New(nil),
		Preflight: &engine.Snapshot{
			TGID: 77,
			Verdicts: []domain.SourceVerdict{{
				Source:  domain.PlatformBoosty,
				Verdict: domain.VerdictActive,
				Detail:  messages.ReasonMembershipInChat(-1001),
			}},
			Decision: domain.AccessDecision{
				TGID:   77,
				Status: domain.StatusActive,
				Reasons: []domain.AccessReason{{
					Source:  domain.PlatformTribute,
					Verdict: domain.VerdictActive,
					Detail:  messages.ReasonLedgerActive(),
				}},
			},
		},
	}, []int64{100})

	for _, text := range []string{"/whois 77", "/whois @known"} {
		result, err := handler.HandlePrivate(ctx, privateMessage(1, 100, text))
		if err != nil {
			t.Fatalf("HandlePrivate %q: %v", text, err)
		}

		if result.Ignored || len(result.Replies) != 1 {
			t.Fatalf("result for %q = %+v, want one reply", text, result)
		}

		if !strings.Contains(result.Replies[0].Text, "<code>77</code>") {
			t.Fatalf("reply for %q = %q, want user card", text, result.Replies[0].Text)
		}

		if !strings.Contains(result.Replies[0].Text, "<b>Диагностика</b>") ||
			!strings.Contains(result.Replies[0].Text, "<code>Tribute</code>") {
			t.Fatalf("reply for %q = %q, want localized reasons", text,
				result.Replies[0].Text)
		}
	}

	if _, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 77, domain.PlatformBoosty,
	); err != nil || !ok {
		t.Fatalf("preflight subscription = (_, %v, %v), want active boosty", ok, err)
	}
}

func TestWhoisWithIncompleteDepsReturnsUnavailable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	handler := NewUserCommands(store.NewUsers(db), []int64{100})

	result, err := handler.HandlePrivate(ctx, privateMessage(1, 100, "/whois 77"))
	if err != nil {
		t.Fatalf("HandlePrivate: %v", err)
	}

	if result.Ignored || len(result.Replies) != 1 ||
		!strings.Contains(result.Replies[0].Text, "недоступна") {
		t.Fatalf("result = %+v, want unavailable reply", result)
	}
}

func TestWhoisUnknownUsernameAndNonOwner(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	handler := NewCommands(CommandDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Audit:         store.NewAudit(db),
		Whitelist:     store.NewWhitelist(db),
	}, []int64{100})

	result, err := handler.HandlePrivate(ctx, privateMessage(1, 100, "/whois @missing"))
	if err != nil {
		t.Fatalf("HandlePrivate missing: %v", err)
	}

	if result.Ignored || len(result.Replies) != 1 ||
		!strings.Contains(result.Replies[0].Text, "не найден") {
		t.Fatalf("missing result = %+v, want not-found reply", result)
	}

	result, err = handler.HandlePrivate(ctx, privateMessage(2, 101, "/whois 77"))
	if err != nil {
		t.Fatalf("HandlePrivate non-owner: %v", err)
	}

	if !result.Ignored || len(result.Replies) != 0 {
		t.Fatalf("non-owner result = %+v, want ignored", result)
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
