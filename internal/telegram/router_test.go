package telegram

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/admission"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/messages"
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

func TestRouterEnqueuesCommandDMInOutbox(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	updateID := int64(800)

	router := NewRouter(RouterDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Meta:          store.NewMeta(db),
		Audit:         store.NewAudit(db),
		Alerts:        store.NewAlerts(db),
		Whitelist:     store.NewWhitelist(db),
		Revocations:   store.NewRevocations(db),
		Outbox:        store.NewOutbox(db),
		UpdateID:      updateID,
	}, nil, nil, nil)

	result, err := router.Route(ctx, privateTextUpdate(updateID, 9001, "/start"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Status != store.TelegramUpdateProcessed {
		t.Fatalf("status = %s, want processed", result.Status)
	}

	if len(result.Effects) != 0 {
		t.Fatalf("effects = %+v, want no direct DM effects", result.Effects)
	}

	if got := countSendDMActions(t, db); got != 1 {
		t.Fatalf("send_dm actions = %d, want 1", got)
	}
}

func TestRouterRoutesJoinRequestToAdmission(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(9101)
	rawLink := "https://t.me/+router-join"

	seedRouterSharedInvite(t, db, domain.ResourceChat, rawLink)

	router := admissionRouter(t, db, activeSnapshotForRouter(tgID))

	result, err := router.Route(ctx, &models.Update{
		ID: 901,
		ChatJoinRequest: &models.ChatJoinRequest{
			Chat:       models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
			From:       models.User{ID: tgID, FirstName: "Join"},
			UserChatID: 99101,
			Date:       int(time.Unix(1_700_000_000, 0).Unix()),
			InviteLink: &models.ChatInviteLink{InviteLink: rawLink},
		},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Status != store.TelegramUpdateProcessed {
		t.Fatalf("status = %s, want processed", result.Status)
	}

	if got := countRouterActions(t, db, domain.ActionApproveJoin); got != 1 {
		t.Fatalf("approve_join actions = %d, want 1", got)
	}
}

func TestRouterJoinRequestPreservesKnownDMState(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(9104)
	rawLink := "https://t.me/+router-join-dm-state"

	if err := store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMBlocked,
	}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	seedRouterSharedInvite(t, db, domain.ResourceChat, rawLink)

	router := admissionRouter(t, db, activeSnapshotForRouter(tgID))
	if _, err := router.Route(ctx, &models.Update{
		ID: 904,
		ChatJoinRequest: &models.ChatJoinRequest{
			Chat:       models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
			From:       models.User{ID: tgID, FirstName: "Join"},
			UserChatID: 99104,
			Date:       int(time.Unix(1_700_000_000, 0).Unix()),
			InviteLink: &models.ChatInviteLink{InviteLink: rawLink},
		},
	}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	user, err := store.NewUsers(db).Get(ctx, tgID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	if user.DMState != domain.DMBlocked {
		t.Fatalf("dm_state = %s, want blocked preserved", user.DMState)
	}
}

func TestRouterRoutesClubChatMemberToAdmission(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(9102)

	router := admissionRouter(t, db, activeSnapshotForRouter(tgID))

	result, err := router.Route(ctx, &models.Update{
		ID: 902,
		ChatMember: &models.ChatMemberUpdated{
			Chat: models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
			OldChatMember: models.ChatMember{
				Type: models.ChatMemberTypeLeft,
				Left: &models.ChatMemberLeft{
					User: &models.User{ID: tgID, FirstName: "External"},
				},
			},
			NewChatMember: models.ChatMember{
				Type: models.ChatMemberTypeMember,
				Member: &models.ChatMemberMember{
					User: &models.User{ID: tgID, FirstName: "External"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Status != store.TelegramUpdateProcessed {
		t.Fatalf("status = %s, want processed", result.Status)
	}

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}

	if grant.AdmittedBy != "external" {
		t.Fatalf("grant = %+v, want external admission", grant)
	}
}

func TestRouterClubMembershipPreservesKnownDMState(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(9105)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMOpen,
	}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	router := admissionRouter(t, db, activeSnapshotForRouter(tgID))
	if _, err := router.Route(ctx, &models.Update{
		ID: 905,
		ChatMember: &models.ChatMemberUpdated{
			Chat: models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
			OldChatMember: models.ChatMember{
				Type: models.ChatMemberTypeLeft,
				Left: &models.ChatMemberLeft{
					User: &models.User{ID: tgID, FirstName: "Member"},
				},
			},
			NewChatMember: models.ChatMember{
				Type: models.ChatMemberTypeMember,
				Member: &models.ChatMemberMember{
					User: &models.User{ID: tgID, FirstName: "Member"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	user, err := store.NewUsers(db).Get(ctx, tgID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	if user.DMState != domain.DMOpen {
		t.Fatalf("dm_state = %s, want open preserved", user.DMState)
	}
}

func TestRouterRoutesRetryCallbackToAdmission(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(9103)

	router := admissionRouter(t, db, inactiveSnapshotForRouter(tgID))

	result, err := router.Route(ctx, &models.Update{
		ID: 903,
		CallbackQuery: &models.CallbackQuery{
			From: models.User{ID: tgID, FirstName: "Retry"},
			Data: messages.RetryAccessCallbackData,
		},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	if result.Status != store.TelegramUpdateProcessed {
		t.Fatalf("status = %s, want processed", result.Status)
	}

	if got := countSendDMActions(t, db); got != 1 {
		t.Fatalf("send_dm actions = %d, want 1", got)
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

func admissionRouter(
	t *testing.T,
	db *sql.DB,
	snapshot *engine.Snapshot,
) *Router {
	t.Helper()

	return NewRouter(RouterDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Meta:          store.NewMeta(db),
		Audit:         store.NewAudit(db),
		Alerts:        store.NewAlerts(db),
		Whitelist:     store.NewWhitelist(db),
		Revocations:   store.NewRevocations(db),
		Outbox:        store.NewOutbox(db),
		Invites:       store.NewInvites(db),
	}, nil, nil, nil,
		WithStatusEngine(engine.New(nil)),
		WithAdmissionConfig(admission.Config{
			InviteMode:    domain.InviteSharedJoinRequest,
			ClubChatID:    -1001,
			ClubChannelID: -1002,
			Resources: []admission.ResourceConfig{
				{Resource: domain.ResourceChat, ChatID: -1001},
				{Resource: domain.ResourceChannel, ChatID: -1002},
			},
		}),
		WithPreflight(RoutePreflight{Snapshot: snapshot}))
}

func seedRouterSharedInvite(
	t *testing.T,
	db *sql.DB,
	resource domain.Resource,
	rawLink string,
) {
	t.Helper()

	if _, err := store.NewInvites(db).SaveCreated(context.Background(),
		store.InviteLinkInput{
			Resource:           resource,
			Mode:               domain.InviteSharedJoinRequest,
			InviteLink:         rawLink,
			InviteLinkHash:     routerInviteHash(rawLink),
			TelegramName:       "gk-router",
			CreatesJoinRequest: true,
		}); err != nil {
		t.Fatalf("save shared invite: %v", err)
	}
}

func activeSnapshotForRouter(tgID int64) *engine.Snapshot {
	return statusSnapshotForRouter(tgID, domain.StatusActive, domain.VerdictActive)
}

func inactiveSnapshotForRouter(tgID int64) *engine.Snapshot {
	return statusSnapshotForRouter(tgID, domain.StatusInactive, domain.VerdictInactive)
}

func statusSnapshotForRouter(
	tgID int64,
	status domain.EffectiveStatus,
	verdict domain.Verdict,
) *engine.Snapshot {
	sourceVerdict := domain.SourceVerdict{
		Source:  domain.PlatformBoosty,
		Verdict: verdict,
		Detail:  "router test",
	}

	return &engine.Snapshot{
		TGID:     tgID,
		Verdicts: []domain.SourceVerdict{sourceVerdict},
		Decision: domain.AccessDecision{
			TGID:    tgID,
			Status:  status,
			Allowed: status == domain.StatusActive,
			Reasons: []domain.AccessReason{
				domain.AccessReason(sourceVerdict),
			},
		},
	}
}

func countRouterActions(t *testing.T, db *sql.DB, action domain.ActionType) int {
	t.Helper()

	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM access_actions
		WHERE action_type = ?`, string(action)).Scan(&n); err != nil {
		t.Fatalf("count actions: %v", err)
	}

	return n
}

func routerInviteHash(link string) string {
	sum := sha256.Sum256([]byte(link))

	return hex.EncodeToString(sum[:])
}
