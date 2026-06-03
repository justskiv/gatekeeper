package telegram

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/admission"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

func TestRouterRoutesSourceChatMemberToEngine(t *testing.T) {
	db := testutil.NewDB(t)
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
	require.NoError(t, err, "Route")
	assert.Equal(t, store.TelegramUpdateProcessed, result.Status)

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 42, domain.PlatformBoosty)
	require.NoError(t, err, "GetActive")
	require.True(t, ok, "boosty subscription must be active")
	assert.Equal(t, domain.PlatformBoosty, sub.Platform)
}

func TestRouterIgnoresTributeMembershipInWebhookMode(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	eventAt := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: 42}),
		"upsert user")

	_, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:        42,
		Platform:    domain.PlatformTribute,
		StartedAt:   eventAt,
		LastSignal:  "webhook",
		LastEventAt: &eventAt,
	})
	require.NoError(t, err, "seed tribute subscription")

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
		WithStatusEngine(engine.New(nil)),
		WithSourceChats(SourceChats{
			TributeChannelID:   -1002,
			TributeObservation: false,
		}))

	result, err := router.Route(ctx, sourceLeaveUpdate(-1002, 42))
	require.NoError(t, err, "Route")
	assert.Equal(t, store.TelegramUpdateIgnored, result.Status)

	_, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 42, domain.PlatformTribute)
	require.NoError(t, err, "GetActive")
	assert.True(t, ok, "tribute subscription must be kept")
}

func TestRouterSourceRevocationWiresProtectedMemberAlertDelivery(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := int64(43)
	ownerID := int64(100)
	outbox := store.NewOutbox(db)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:       tgID,
		Platform:   domain.PlatformBoosty,
		StartedAt:  time.Now().Add(-time.Hour),
		LastSignal: "event",
	})
	require.NoError(t, err, "upsert subscription")

	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert grant")

	statusEngine := engine.New(nil, engine.WithRevocationConfig(
		engine.RevocationConfig{ExpiryMode: "immediate"}))
	router := NewRouter(RouterDeps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Meta:          store.NewMeta(db),
		Audit:         store.NewAudit(db),
		Alerts: store.NewAlertsWithDelivery(
			db, outbox, []int64{ownerID}, nil),
		Whitelist:   store.NewWhitelist(db),
		Revocations: store.NewRevocations(db),
		Outbox:      outbox,
	}, nil, []int64{ownerID}, nil,
		WithStatusEngine(statusEngine),
		WithSourceChats(SourceChats{BoostyGroupID: -1001}),
		WithMemberChecker(staticMemberChecker{member: &models.ChatMember{
			Type: models.ChatMemberTypeAdministrator,
			Administrator: &models.ChatMemberAdministrator{
				User: models.User{ID: tgID},
			},
		}}))

	result, err := router.Route(ctx, sourceLeaveUpdate(-1001, tgID))
	require.NoError(t, err, "Route")
	assert.Equal(t, store.TelegramUpdateProcessed, result.Status)

	assert.Zero(t, countRouterActions(t, db, domain.ActionSoftKick),
		"soft_kick actions must be none for protected admin")
	assert.Equal(t, 1, countRouterAlerts(t, db, "protected_admin_lost_subscription"),
		"protected alerts")
	assert.Equal(t, 1, countRouterActions(t, db, domain.ActionSendDM),
		"operator delivery actions")
}

func TestRouterEnqueuesCommandDMInOutbox(t *testing.T) {
	db := testutil.NewDB(t)
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
	require.NoError(t, err, "Route")
	assert.Equal(t, store.TelegramUpdateProcessed, result.Status)
	assert.Empty(t, result.Effects, "command DM must go through the outbox")
	assert.Equal(t, 1, countSendDMActions(t, db), "send_dm actions")

	payload := firstRouterDMPayload(t, db)
	assert.Equal(t, messages.ParseModeHTML, payload.ParseMode)
}

func TestRouterRoutesJoinRequestToAdmission(t *testing.T) {
	db := testutil.NewDB(t)
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
	require.NoError(t, err, "Route")
	assert.Equal(t, store.TelegramUpdateProcessed, result.Status)
	assert.Equal(t, 1, countRouterActions(t, db, domain.ActionApproveJoin),
		"approve_join actions")
}

func TestRouterJoinRequestPreservesKnownDMState(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := int64(9104)
	rawLink := "https://t.me/+router-join-dm-state"

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMBlocked,
	}), "upsert user")

	seedRouterSharedInvite(t, db, domain.ResourceChat, rawLink)

	router := admissionRouter(t, db, activeSnapshotForRouter(tgID))
	_, err := router.Route(ctx, &models.Update{
		ID: 904,
		ChatJoinRequest: &models.ChatJoinRequest{
			Chat:       models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
			From:       models.User{ID: tgID, FirstName: "Join"},
			UserChatID: 99104,
			Date:       int(time.Unix(1_700_000_000, 0).Unix()),
			InviteLink: &models.ChatInviteLink{InviteLink: rawLink},
		},
	})
	require.NoError(t, err, "Route")

	user, err := store.NewUsers(db).Get(ctx, tgID)
	require.NoError(t, err, "get user")
	assert.Equal(t, domain.DMBlocked, user.DMState, "dm_state must be preserved")
}

func TestRouterRoutesClubChatMemberToAdmission(t *testing.T) {
	db := testutil.NewDB(t)
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
	require.NoError(t, err, "Route")
	assert.Equal(t, store.TelegramUpdateProcessed, result.Status)

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "get grant")
	assert.Equal(t, "external", grant.AdmittedBy)
}

func TestRouterClubMembershipPreservesKnownDMState(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := int64(9105)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMOpen,
	}), "upsert user")

	router := admissionRouter(t, db, activeSnapshotForRouter(tgID))
	_, err := router.Route(ctx, &models.Update{
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
	})
	require.NoError(t, err, "Route")

	user, err := store.NewUsers(db).Get(ctx, tgID)
	require.NoError(t, err, "get user")
	assert.Equal(t, domain.DMOpen, user.DMState, "dm_state must be preserved")
}

func TestRouterRoutesRetryCallbackToAdmission(t *testing.T) {
	db := testutil.NewDB(t)
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
	require.NoError(t, err, "Route")
	assert.Equal(t, store.TelegramUpdateProcessed, result.Status)
	assert.Equal(t, 1, countSendDMActions(t, db), "send_dm actions")
}

func TestRouterSourceEventSharesTerminalTransaction(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	updateID := int64(700)
	require.NoError(t, store.NewTelegramUpdates(db).InsertBatch(ctx, []store.TelegramUpdate{{
		UpdateID:    updateID,
		UpdateType:  "chat_member",
		PayloadJSON: []byte(`{"update_id":700}`),
	}}, updateID+1), "InsertBatch")

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err, "BeginTx")

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
	require.NoError(t, err, "Route")

	require.NoError(t, store.NewTelegramUpdates(tx).MarkTerminal(
		ctx, updateID, result.Status, "",
	), "MarkTerminal")

	require.NoError(t, tx.Rollback(), "Rollback")

	_, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 42, domain.PlatformBoosty)
	require.NoError(t, err, "GetActive")
	assert.False(t, ok, "subscription must be absent after rollback")

	var status string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT status FROM telegram_updates WHERE update_id = ?`, updateID,
	).Scan(&status), "read update status")
	assert.Equal(t, string(store.TelegramUpdatePending), status,
		"rolled-back MarkTerminal must leave the update pending")
}

func TestRouterIgnoresBotAndRightsOnlyChatMemberEvents(t *testing.T) {
	db := testutil.NewDB(t)
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
		require.NoError(t, err, "Route")
		assert.Equal(t, store.TelegramUpdateProcessed, result.Status)
	}

	active, err := store.NewSubscriptions(db).ListActiveByUser(ctx, 42)
	require.NoError(t, err, "ListActiveByUser")
	assert.Empty(t, active, "bot and rights-only events must not grant access")
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

func sourceLeaveUpdate(chatID, tgID int64) *models.Update {
	return &models.Update{
		ChatMember: &models.ChatMemberUpdated{
			Chat: models.Chat{ID: chatID, Type: models.ChatTypeSupergroup},
			From: models.User{ID: 1, FirstName: "Owner"},
			OldChatMember: models.ChatMember{
				Type: models.ChatMemberTypeMember,
				Member: &models.ChatMemberMember{
					User: &models.User{ID: tgID, FirstName: "User"},
				},
			},
			NewChatMember: models.ChatMember{
				Type: models.ChatMemberTypeLeft,
				Left: &models.ChatMemberLeft{
					User: &models.User{ID: tgID, FirstName: "User"},
				},
			},
		},
	}
}

type staticMemberChecker struct {
	member *models.ChatMember
}

func (c staticMemberChecker) GetChatMember(
	context.Context,
	domain.Resource,
	int64,
) (*models.ChatMember, error) {
	return c.member, nil
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

	_, err := store.NewInvites(db).SaveCreated(context.Background(),
		store.InviteLinkInput{
			Resource:           resource,
			Mode:               domain.InviteSharedJoinRequest,
			InviteLink:         rawLink,
			InviteLinkHash:     routerInviteHash(rawLink),
			TelegramName:       "gk-router",
			CreatesJoinRequest: true,
		})
	require.NoError(t, err, "save shared invite")
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
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM access_actions
		WHERE action_type = ?`, string(action)).Scan(&n), "count actions")

	return n
}

func countRouterAlerts(t *testing.T, db *sql.DB, kind string) int {
	t.Helper()

	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM admin_alerts
		WHERE kind = ?`, kind).Scan(&n), "count alerts")

	return n
}

type routerDMPayload struct {
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

func firstRouterDMPayload(t *testing.T, db *sql.DB) routerDMPayload {
	t.Helper()

	var raw string
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT payload_json
		FROM access_actions
		WHERE action_type = ?
		ORDER BY id
		LIMIT 1`, string(domain.ActionSendDM)).Scan(&raw), "read action payload")

	var payload routerDMPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload), "decode action payload")

	return payload
}

func routerInviteHash(link string) string {
	sum := sha256.Sum256([]byte(link))

	return hex.EncodeToString(sum[:])
}
