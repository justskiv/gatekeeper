package bot

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/store"
)

const adminTestOwnerID int64 = 100

func TestAdminGrantRequiresOwnerAndConfirmation(t *testing.T) {
	resetAdminConfirmations(t)

	db := newTestDB(t)
	ctx := context.Background()
	handler := NewCommands(adminDeps(db, nil), []int64{adminTestOwnerID})

	nonOwner, err := handler.HandlePrivate(
		ctx, privateMessage(1, 101, "/grant 800"))
	if err != nil {
		t.Fatalf("HandlePrivate non-owner: %v", err)
	}

	if !nonOwner.Ignored {
		t.Fatalf("non-owner result = %+v, want ignored", nonOwner)
	}

	confirm := adminConfirmData(t, handler, ctx, "/grant 800 1h temp")

	first, err := handler.HandleCallback(ctx, adminCallback(confirm))
	if err != nil {
		t.Fatalf("HandleCallback confirm: %v", err)
	}

	duplicate, err := handler.HandleCallback(ctx, adminCallback(confirm))
	if err != nil {
		t.Fatalf("HandleCallback duplicate: %v", err)
	}

	if len(first.Replies) != 1 || first.Replies[0].Text != duplicate.Replies[0].Text {
		t.Fatalf("confirm replies = %+v / %+v, want idempotent result",
			first.Replies, duplicate.Replies)
	}

	if _, err := store.NewUsers(db).Get(ctx, 800); err != nil {
		t.Fatalf("get stub user: %v", err)
	}

	if _, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 800, domain.PlatformManual,
	); err != nil || !ok {
		t.Fatalf("manual subscription = (_, %v, %v), want active", ok, err)
	}
}

func TestAdminRevokeBanAndUnbanEffects(t *testing.T) {
	resetAdminConfirmations(t)

	db := newTestDB(t)
	ctx := context.Background()
	handler := NewCommands(adminDeps(db, nil), []int64{adminTestOwnerID})

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: 801}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if err := store.NewWhitelist(db).Add(ctx, 801, adminTestOwnerID, "seed"); err != nil {
		t.Fatalf("add whitelist: %v", err)
	}

	if _, err := store.NewSubscriptions(db).UpsertManual(
		ctx, 801, nil, "seed",
	); err != nil {
		t.Fatalf("upsert manual: %v", err)
	}

	if err := store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       801,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}); err != nil {
		t.Fatalf("upsert grant: %v", err)
	}

	confirmRevoke := adminConfirmData(t, handler, ctx, "/revoke 801 test")
	if _, err := handler.HandleCallback(ctx, adminCallback(confirmRevoke)); err != nil {
		t.Fatalf("confirm revoke: %v", err)
	}

	if ok, err := store.NewWhitelist(db).Has(ctx, 801); err != nil || ok {
		t.Fatalf("whitelist = (%v, %v), want absent", ok, err)
	}

	if _, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 801, domain.PlatformManual,
	); err != nil || ok {
		t.Fatalf("manual subscription = (_, %v, %v), want expired", ok, err)
	}

	if _, ok, err := store.NewRevocations(db).Get(ctx, 801); err != nil || !ok {
		t.Fatalf("pending revocation = (_, %v, %v), want present", ok, err)
	}

	confirmBan := adminConfirmData(t, handler, ctx, "/ban 801 abuse")
	if _, err := handler.HandleCallback(ctx, adminCallback(confirmBan)); err != nil {
		t.Fatalf("confirm ban: %v", err)
	}

	if _, err := handler.HandleCallback(ctx, adminCallback(confirmBan)); err != nil {
		t.Fatalf("duplicate ban: %v", err)
	}

	user, err := store.NewUsers(db).Get(ctx, 801)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	if !user.Banned {
		t.Fatal("user banned = false, want true")
	}

	if got := countAdminActions(t, db, domain.ActionHardBan); got != 2 {
		t.Fatalf("hard_ban actions = %d, want chat and channel", got)
	}

	if _, ok, err := store.NewRevocations(db).Get(ctx, 801); err != nil || ok {
		t.Fatalf("pending revocation = (_, %v, %v), want deleted", ok, err)
	}

	confirmUnban := adminConfirmData(t, handler, ctx, "/unban 801 pardon")
	if _, err := handler.HandleCallback(ctx, adminCallback(confirmUnban)); err != nil {
		t.Fatalf("confirm unban: %v", err)
	}

	user, err = store.NewUsers(db).Get(ctx, 801)
	if err != nil {
		t.Fatalf("get user after unban: %v", err)
	}

	if user.Banned {
		t.Fatal("user banned = true, want false")
	}

	if got := countAdminActions(t, db, domain.ActionUnban); got != 2 {
		t.Fatalf("unban actions = %d, want chat and channel", got)
	}
}

func TestAdminSyncConfirmCancelAndExpiredCallbacks(t *testing.T) {
	resetAdminConfirmations(t)

	db := newTestDB(t)
	ctx := context.Background()

	var (
		calls      int
		lastTarget *int64
	)

	sync := func(_ context.Context, tgID *int64) (string, error) {
		calls++
		lastTarget = tgID

		return "sync complete", nil
	}

	handler := NewCommands(adminDeps(db, sync), []int64{adminTestOwnerID})

	confirmAll := adminConfirmData(t, handler, ctx, "/sync")
	if _, err := handler.HandleCallback(ctx, adminCallback(confirmAll)); err != nil {
		t.Fatalf("confirm sync all: %v", err)
	}

	if calls != 1 || lastTarget != nil {
		t.Fatalf("sync calls=%d target=%v, want full pass", calls, lastTarget)
	}

	cancel := adminCancelData(t, handler, ctx, "/sync 901")
	if _, err := handler.HandleCallback(ctx, adminCallback(cancel)); err != nil {
		t.Fatalf("cancel sync: %v", err)
	}

	if calls != 1 {
		t.Fatalf("sync calls after cancel = %d, want unchanged", calls)
	}

	expired := adminConfirmData(t, handler, ctx, "/sync 902")
	expireAdminAction(expired)

	result, err := handler.HandleCallback(ctx, adminCallback(expired))
	if err != nil {
		t.Fatalf("expired sync: %v", err)
	}

	if calls != 1 {
		t.Fatalf("sync calls after expired = %d, want unchanged", calls)
	}

	if len(result.Replies) != 1 ||
		!strings.Contains(result.Replies[0].Text, "устарело") {
		t.Fatalf("expired reply = %+v, want expiry message", result.Replies)
	}
}

func TestOpsCommandsAreOwnerOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	handler := NewCommands(adminDeps(db, nil), []int64{adminTestOwnerID})

	result, err := handler.HandlePrivate(ctx, privateMessage(1, 101, "/stats"))
	if err != nil {
		t.Fatalf("HandlePrivate non-owner stats: %v", err)
	}

	if !result.Ignored {
		t.Fatalf("non-owner stats result = %+v, want ignored", result)
	}
}

func TestOpsCommandsRenderStatsAlertsChatsAndExport(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	handler := NewCommands(CommandDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Audit:         store.NewAudit(db),
		Whitelist:     store.NewWhitelist(db),
		Revocations:   store.NewRevocations(db),
		Outbox:        store.NewOutbox(db),
		Alerts:        store.NewAlerts(db),
		Ops:           store.NewOps(db),
		StatusEngine:  engine.New(nil),
		ChatRoles: []ChatRole{
			{Role: "club chat", ChatID: -1003333333333},
		},
	}, []int64{adminTestOwnerID})

	if err := store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:     900,
		Username: "alice",
	}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := store.NewSubscriptions(db).UpsertManual(
		ctx, 900, nil, "manual",
	); err != nil {
		t.Fatalf("upsert manual subscription: %v", err)
	}

	if _, err := store.NewAlerts(db).Create(ctx, store.AlertInput{
		Severity: "warning",
		Kind:     "test_alert",
		Title:    "test alert",
	}); err != nil {
		t.Fatalf("create alert: %v", err)
	}

	for _, command := range []string{"/stats", "/alerts", "/chats", "/help_admin"} {
		result, err := handler.HandlePrivate(ctx,
			privateMessage(1, adminTestOwnerID, command))
		if err != nil {
			t.Fatalf("HandlePrivate %s: %v", command, err)
		}

		if result.Ignored || len(result.Replies) != 1 || result.Replies[0].Text == "" {
			t.Fatalf("%s result = %+v, want one reply", command, result)
		}
	}

	export, err := handler.HandlePrivate(ctx,
		privateMessage(1, adminTestOwnerID, "/export"))
	if err != nil {
		t.Fatalf("HandlePrivate export: %v", err)
	}

	if export.Ignored || len(export.Replies) == 0 {
		t.Fatalf("export result = %+v, want replies", export)
	}

	if !strings.Contains(export.Replies[0].Text, "tg_id,username") ||
		!strings.Contains(export.Replies[0].Text, "900,alice") {
		t.Fatalf("export text = %q, want CSV user row", export.Replies[0].Text)
	}

	groupExport, err := handler.HandlePrivate(ctx, &models.Message{
		From: &models.User{ID: adminTestOwnerID},
		Chat: models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
		Text: "/export",
	})
	if err != nil {
		t.Fatalf("HandlePrivate group export: %v", err)
	}

	if !groupExport.Ignored {
		t.Fatalf("group export result = %+v, want ignored", groupExport)
	}
}

func TestRenderExportCSVAndSplitReplyEdgeCases(t *testing.T) {
	text, err := renderExportCSV([]store.ExportRow{{TGID: 901}})
	if err != nil {
		t.Fatalf("renderExportCSV: %v", err)
	}

	records, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("records = %+v, want header and one row", records)
	}

	lastSeenColumn := len(records[1]) - 1
	if records[1][lastSeenColumn] != "" {
		t.Fatalf("last_seen_at = %q, want empty", records[1][lastSeenColumn])
	}

	multibyte := "a🙂b"

	parts := splitReply(multibyte, 3)
	for _, part := range parts {
		if !utf8.ValidString(part) {
			t.Fatalf("part %q is not valid UTF-8", part)
		}
	}

	if strings.Join(parts, "") != multibyte {
		t.Fatalf("parts = %+v, want to preserve input", parts)
	}
}

func adminDeps(db store.DBTX, sync AdminSyncFunc) CommandDeps {
	return CommandDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Audit:         store.NewAudit(db),
		Whitelist:     store.NewWhitelist(db),
		Revocations:   store.NewRevocations(db),
		Outbox:        store.NewOutbox(db),
		Alerts:        store.NewAlerts(db),
		Ops:           store.NewOps(db),
		StatusEngine:  engine.New(nil),
		AdminSync:     sync,
	}
}

func adminConfirmData(
	t *testing.T,
	handler *UserCommands,
	ctx context.Context,
	text string,
) string {
	t.Helper()

	result, err := handler.HandlePrivate(ctx, privateMessage(1, adminTestOwnerID, text))
	if err != nil {
		t.Fatalf("HandlePrivate %q: %v", text, err)
	}

	if result.Ignored || len(result.Replies) != 1 ||
		len(result.Replies[0].Buttons) != 1 ||
		len(result.Replies[0].Buttons[0]) != 2 {
		t.Fatalf("result for %q = %+v, want confirmation buttons", text, result)
	}

	return result.Replies[0].Buttons[0][0].CallbackData
}

func adminCancelData(
	t *testing.T,
	handler *UserCommands,
	ctx context.Context,
	text string,
) string {
	t.Helper()

	result, err := handler.HandlePrivate(ctx, privateMessage(1, adminTestOwnerID, text))
	if err != nil {
		t.Fatalf("HandlePrivate %q: %v", text, err)
	}

	if result.Ignored || len(result.Replies) != 1 ||
		len(result.Replies[0].Buttons) != 1 ||
		len(result.Replies[0].Buttons[0]) != 2 {
		t.Fatalf("result for %q = %+v, want confirmation buttons", text, result)
	}

	return result.Replies[0].Buttons[0][1].CallbackData
}

func adminCallback(data string) *models.CallbackQuery {
	return &models.CallbackQuery{
		From: models.User{ID: adminTestOwnerID},
		Data: data,
	}
}

func expireAdminAction(confirmData string) {
	id := strings.TrimPrefix(confirmData, adminConfirmPrefix)

	adminConfirmations.Lock()
	defer adminConfirmations.Unlock()

	if action := adminConfirmations.items[id]; action != nil {
		action.CreatedAt = time.Now().Add(-confirmationTTL - time.Second)
	}
}

func resetAdminConfirmations(t *testing.T) {
	t.Helper()

	adminConfirmations.Lock()
	adminConfirmations.items = map[string]*adminAction{}
	adminConfirmations.Unlock()

	t.Cleanup(func() {
		adminConfirmations.Lock()
		adminConfirmations.items = map[string]*adminAction{}
		adminConfirmations.Unlock()
	})
}

func countAdminActions(
	t *testing.T,
	db store.DBTX,
	actionType domain.ActionType,
) int {
	t.Helper()

	var got int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM access_actions WHERE action_type = ?`,
		string(actionType)).Scan(&got); err != nil {
		t.Fatalf("count actions: %v", err)
	}

	return got
}
