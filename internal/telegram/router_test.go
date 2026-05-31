package telegram

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/store"
)

func TestRouterRoutesSourceChatMemberToEngine(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	statusEngine := engine.New(nil)
	router := NewRouter(RouterDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Meta:          store.NewMeta(db),
		Audit:         store.NewAudit(db),
		Alerts:        store.NewAlerts(db),
		Whitelist:     store.NewWhitelist(db),
		Revocations:   store.NewRevocations(db),
	}, nil, nil, nil,
		WithStatusEngine(statusEngine),
		WithSourceChats(SourceChats{
			BoostyGroupID: -1001,
		}))

	result, err := router.Route(ctx, sourceJoinUpdate(-1001, 42))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if result.Status != store.TelegramUpdateProcessed {
		t.Fatalf("status = %s, want processed", result.Status)
	}

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 42, domain.PlatformBoosty)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if !ok || sub.Platform != domain.PlatformBoosty {
		t.Fatalf("subscription = (%+v, %v), want active boosty", sub, ok)
	}
}

func TestRouterSourceEventSharesTerminalTransaction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	updateID := int64(700)
	if err := store.NewTelegramUpdates(db).InsertBatch(ctx, []store.TelegramUpdate{{
		UpdateID:    updateID,
		UpdateType:  "chat_member",
		PayloadJSON: []byte(`{"update_id":700}`),
	}}, updateID+1); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	statusEngine := engine.New(nil)
	router := NewRouter(RouterDeps{
		Users:         store.NewUsers(tx),
		Subscriptions: store.NewSubscriptions(tx),
		Grants:        store.NewGrants(tx),
		Meta:          store.NewMeta(tx),
		Audit:         store.NewAudit(tx),
		Alerts:        store.NewAlerts(tx),
		Whitelist:     store.NewWhitelist(tx),
		Revocations:   store.NewRevocations(tx),
	}, nil, nil, nil,
		WithStatusEngine(statusEngine),
		WithSourceChats(SourceChats{
			BoostyGroupID: -1001,
		}))

	result, err := router.Route(ctx, sourceJoinUpdate(-1001, 42))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if err := store.NewTelegramUpdates(tx).MarkTerminal(
		ctx, updateID, result.Status, "",
	); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if _, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 42, domain.PlatformBoosty,
	); err != nil || ok {
		t.Fatalf("subscription after rollback = (_, %v, %v), want absent", ok, err)
	}

	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM telegram_updates WHERE update_id = ?`, updateID,
	).Scan(&status); err != nil {
		t.Fatalf("read update status: %v", err)
	}
	if status != string(store.TelegramUpdatePending) {
		t.Fatalf("status after rollback = %q, want pending", status)
	}
}

func TestRouterIgnoresBotAndRightsOnlyChatMemberEvents(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	statusEngine := engine.New(nil)
	router := NewRouter(RouterDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Meta:          store.NewMeta(db),
		Audit:         store.NewAudit(db),
		Alerts:        store.NewAlerts(db),
		Whitelist:     store.NewWhitelist(db),
		Revocations:   store.NewRevocations(db),
	}, nil, nil, nil,
		WithStatusEngine(statusEngine),
		WithSourceChats(SourceChats{
			BoostyGroupID: -1001,
		}))

	updates := []*models.Update{
		{
			ChatMember: &models.ChatMemberUpdated{
				Chat: models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
				OldChatMember: models.ChatMember{
					Type: models.ChatMemberTypeLeft,
					Left: &models.ChatMemberLeft{
						User: &models.User{ID: 99, IsBot: true},
					},
				},
				NewChatMember: models.ChatMember{
					Type: models.ChatMemberTypeMember,
					Member: &models.ChatMemberMember{
						User: &models.User{ID: 99, IsBot: true},
					},
				},
			},
		},
		{
			ChatMember: &models.ChatMemberUpdated{
				Chat: models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
				OldChatMember: models.ChatMember{
					Type: models.ChatMemberTypeMember,
					Member: &models.ChatMemberMember{
						User: &models.User{ID: 42},
					},
				},
				NewChatMember: models.ChatMember{
					Type: models.ChatMemberTypeAdministrator,
					Administrator: &models.ChatMemberAdministrator{
						User: models.User{ID: 42},
					},
				},
			},
		},
	}
	for _, update := range updates {
		result, err := router.Route(ctx, update)
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if result.Status != store.TelegramUpdateProcessed {
			t.Fatalf("status = %s, want processed", result.Status)
		}
	}

	active, err := store.NewSubscriptions(db).ListActiveByUser(ctx, 42)
	if err != nil {
		t.Fatalf("ListActiveByUser: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active subscriptions = %+v, want none", active)
	}
}

func sourceJoinUpdate(chatID, tgID int64) *models.Update {
	return &models.Update{
		ChatMember: &models.ChatMemberUpdated{
			Chat: models.Chat{ID: chatID, Type: models.ChatTypeSupergroup},
			From: models.User{ID: 1, FirstName: "Owner"},
			OldChatMember: models.ChatMember{
				Type: models.ChatMemberTypeLeft,
				Left: &models.ChatMemberLeft{
					User: &models.User{ID: tgID, FirstName: "User"},
				},
			},
			NewChatMember: models.ChatMember{
				Type: models.ChatMemberTypeMember,
				Member: &models.ChatMemberMember{
					User: &models.User{ID: tgID, FirstName: "User"},
				},
			},
		},
	}
}
