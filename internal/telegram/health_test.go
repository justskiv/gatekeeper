package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

func TestCheckStartupHealthRecordsHealthyChat(t *testing.T) {
	db := testutil.NewDB(t)
	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch methodName(r.URL.Path) {
		case "getChat":
			writeTelegramResult(w, map[string]any{
				"id": -1001, "type": "supergroup", "title": "club",
			})
		case "getChatMember":
			writeTelegramResult(w, map[string]any{
				"status": "administrator",
				"user": map[string]any{
					"id": 123, "is_bot": true, "first_name": "Gatekeeper",
				},
				"can_invite_users":     true,
				"can_restrict_members": true,
			})
		default:
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}
	})

	chat := HealthChat{
		Key:                 "club_chat",
		Name:                "club chat",
		ID:                  -1001,
		Resource:            string(domain.ResourceChat),
		Severity:            "critical",
		RequiresManageRight: true,
	}
	_, err := store.NewAlerts(db).Create(context.Background(), store.AlertInput{
		Severity: "critical",
		Kind:     alertKindBotRightsLost,
		Title:    alertTitle(chat),
		Detail:   "stale",
	})
	require.NoError(t, err, "create stale alert")

	require.NoError(t, CheckStartupHealth(
		context.Background(), db, client, nil,
		[]HealthChat{chat}, nil, 123, slog.Default(),
	), "CheckStartupHealth")

	value, ok, err := store.NewMeta(db).Get(context.Background(), "health.club_chat")
	require.NoError(t, err, "get health")
	require.True(t, ok)
	assert.Equal(t, "ok", value)

	var openAlerts int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM admin_alerts
		WHERE status = 'open' AND kind = 'bot_rights_lost'`,
	).Scan(&openAlerts), "count open alerts")
	assert.Zero(t, openAlerts)
}

func TestCheckStartupHealthDegradesMissingRights(t *testing.T) {
	db := testutil.NewDB(t)
	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch methodName(r.URL.Path) {
		case "getChat":
			writeTelegramResult(w, map[string]any{
				"id": -1002, "type": "supergroup", "title": "club",
			})
		case "getChatMember":
			writeTelegramResult(w, map[string]any{
				"status": "member",
				"user": map[string]any{
					"id": 123, "is_bot": true, "first_name": "Gatekeeper",
				},
			})
		default:
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}
	})

	chat := HealthChat{
		Key:      "club_chat",
		Name:     "club chat",
		ID:       -1002,
		Resource: string(domain.ResourceChat),
		Severity: "critical",
	}
	for range 2 {
		require.NoError(t, CheckStartupHealth(
			context.Background(), db, client, nil,
			[]HealthChat{chat}, nil, 123, slog.Default(),
		), "CheckStartupHealth")
	}

	value, _, err := store.NewMeta(db).Get(context.Background(), "health.club_chat")
	require.NoError(t, err, "get health")
	assert.Equal(t, "fail:not_admin", value)

	var alerts int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM admin_alerts WHERE severity = 'critical'`,
	).Scan(&alerts), "count alerts")
	assert.Equal(t, 1, alerts)
}

func TestCheckStartupHealthRejectsWrongChatType(t *testing.T) {
	db := testutil.NewDB(t)
	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "getChat" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		writeTelegramResult(w, map[string]any{
			"id": -1003, "type": "supergroup", "title": "not a channel",
		})
	})

	chat := HealthChat{
		Key:      "club_channel",
		Name:     "club channel",
		ID:       -1003,
		Resource: string(domain.ResourceChannel),
		Severity: "critical",
	}
	require.NoError(t, CheckStartupHealth(
		context.Background(), db, client, nil,
		[]HealthChat{chat}, nil, 123, slog.Default(),
	), "CheckStartupHealth")

	value, _, err := store.NewMeta(db).Get(context.Background(), "health.club_channel")
	require.NoError(t, err, "get health")
	assert.Equal(t, "fail:wrong_type", value)
}

// TestBotRightsLostOwnerDMIsCancelledWhenTheAlertResolves is the regression for
// the 2026-08-16 incident. Five `bot_rights_lost` alerts auto-resolved at 01:22
// while the outbox worker pool was dead; their owner DMs were delivered at
// 09:16 and described a problem that had been over for eight hours, and the
// owner acted on that. The DM must be linked to the alert that produced it, and
// resolving that alert must retire whatever has not gone out yet.
func TestBotRightsLostOwnerDMIsCancelledWhenTheAlertResolves(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	rightsRestored := false

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch methodName(r.URL.Path) {
		case "getChat":
			writeTelegramResult(w, map[string]any{
				"id": -1001, "type": "supergroup", "title": "club",
			})
		case "getChatMember":
			member := map[string]any{
				"status": "member",
				"user": map[string]any{
					"id": 123, "is_bot": true, "first_name": "Gatekeeper",
				},
			}
			if rightsRestored {
				member = map[string]any{
					"status": "administrator",
					"user": map[string]any{
						"id": 123, "is_bot": true, "first_name": "Gatekeeper",
					},
					"can_invite_users":     true,
					"can_restrict_members": true,
				}
			}

			writeTelegramResult(w, member)
		default:
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}
	})

	chat := HealthChat{
		Key:                 "club_chat",
		Name:                "club chat",
		ID:                  -1001,
		Resource:            string(domain.ResourceChat),
		Severity:            "critical",
		RequiresManageRight: true,
	}
	// The reconcile path builds exactly this notifier: durable, so the owner DM
	// becomes an outbox row rather than an immediate send.
	notifier := notify.NewDurable(store.NewUsers(db), store.NewOutbox(db), nil)

	require.NoError(t, CheckStartupHealth(
		ctx, db, client, notifier,
		[]HealthChat{chat}, []int64{ownerID}, 123, slog.Default(),
	), "CheckStartupHealth with the rights lost")

	var (
		actionID int64
		alertID  sql.NullInt64
		status   string
	)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id, alert_id, status FROM access_actions
		WHERE action_type = 'send_dm' AND tg_id = ?`, ownerID,
	).Scan(&actionID, &alertID, &status), "read the queued owner dm")
	require.True(t, alertID.Valid, "the owner dm must be linked to its alert")
	require.Equal(t, "queued", status, "the dm waits in the outbox")

	rightsRestored = true

	require.NoError(t, CheckStartupHealth(
		ctx, db, client, notifier,
		[]HealthChat{chat}, []int64{ownerID}, 123, slog.Default(),
	), "CheckStartupHealth with the rights back")

	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT status FROM access_actions WHERE id = ?`, actionID,
	).Scan(&status), "re-read the owner dm")
	assert.Equal(t, "cancelled", status,
		"a dm about rights that came back must never be delivered")
}

// TestRealtimeRightsLostDMIsCancelledWhenRightsReturn is the live-poller twin
// of the test above. The reconcile path was fixed by threading the alert id
// through the notifier; the my_chat_member path kept raising the alert, throwing
// the id away and queueing an unlinked owner DM. Nothing could retire that row,
// so rights that came back within the minute still cost the owner a stale
// warning once the queue drained.
func TestRealtimeRightsLostDMIsCancelledWhenRightsReturn(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	outbox := store.NewOutbox(db)
	chat := HealthChat{
		Key:                 "club_chat",
		Name:                "club chat",
		ID:                  -1001,
		Resource:            string(domain.ResourceChat),
		Severity:            "critical",
		RequiresManageRight: true,
	}
	failureText := messages.HealthFailure(chat.Name, chat.ID, "not_admin")

	admin := models.ChatMember{
		Type: models.ChatMemberTypeAdministrator,
		Administrator: &models.ChatMemberAdministrator{
			CanInviteUsers:     true,
			CanRestrictMembers: true,
		},
	}
	plainMember := models.ChatMember{Type: models.ChatMemberTypeMember}

	result, err := newHealthRouter(db, outbox, chat, ownerID, 1).Route(
		ctx, rightsUpdate(chat.ID, ownerID, admin, plainMember))
	require.NoError(t, err, "route the rights-lost update")
	assert.Equal(t, store.TelegramUpdateProcessed, result.Status)

	queued := healthFailureDMs(t, db, failureText)
	require.Len(t, queued, 1, "the owner is warned once")
	assert.Equal(t, "queued", queued[0].status, "the warning waits in the outbox")
	assert.True(t, queued[0].linked,
		"the warning must carry the alert it reports on")

	// The rights come back before a worker gets to the queue.
	result, err = newHealthRouter(db, outbox, chat, ownerID, 2).Route(
		ctx, rightsUpdate(chat.ID, ownerID, plainMember, admin))
	require.NoError(t, err, "route the rights-restored update")
	assert.Equal(t, store.TelegramUpdateProcessed, result.Status)

	settled := healthFailureDMs(t, db, failureText)
	require.Len(t, settled, 1, "no second copy appears")
	assert.Equal(t, "cancelled", settled[0].status,
		"a warning about rights that came back must never be delivered")
}

// TestPollerRightsLossNotifiesTheOwnerOnce is the duplication regression at the
// level where the duplication actually happened. The poller builds the alert
// repository WITH delivery, so raising `bot_rights_lost` used to queue two
// messages to the same owner about one event: the repository's generic operator
// alert and the health path's specific warning. Both were linked and both were
// cancellable — the owner still read the same failure twice.
func TestPollerRightsLossNotifiesTheOwnerOnce(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	chat := HealthChat{
		Key:                 "club_chat",
		Name:                "club chat",
		ID:                  -1001,
		Resource:            string(domain.ResourceChat),
		Severity:            "critical",
		RequiresManageRight: true,
	}

	poller := NewPoller(db, nil, nil,
		[]HealthChat{chat}, []int64{ownerID}, slog.Default())

	require.NoError(t, poller.HandleWebhookUpdate(ctx, rightsUpdateJSON(
		1, chat.ID, ownerID, adminMemberJSON, plainMemberJSON,
	)), "handle the rights-lost update")

	warned := ownerDMs(t, db, ownerID)
	require.Len(t, warned, 1, "one lost right is worth one message to the owner")
	assert.Equal(t, messages.HealthFailure(chat.Name, chat.ID, "not_admin"),
		warned[0].text, "the message that survives is the one that says what broke")
	assert.Equal(t, "queued", warned[0].status, "the warning waits in the outbox")
	assert.True(t, warned[0].linked,
		"the warning must carry the alert it reports on")

	// The rights come back before a worker gets to the queue.
	require.NoError(t, poller.HandleWebhookUpdate(ctx, rightsUpdateJSON(
		2, chat.ID, ownerID, plainMemberJSON, adminMemberJSON,
	)), "handle the rights-restored update")

	assert.Zero(t, alertDMsAwaitingDelivery(t, db),
		"nothing reporting a resolved alert may still be on its way")

	settled := ownerDMs(t, db, ownerID)
	require.Len(t, settled, 2,
		"the warning and the all-clear, with no second copy of either")
	assert.Equal(t, "cancelled", settled[0].status,
		"a warning about rights that came back must never be delivered")
	assert.Equal(t, messages.HealthRestored(chat.Name, chat.ID), settled[1].text,
		"the owner is left with the all-clear alone")
}

// TestStartupRightsLossNotifiesOwnerWithoutRepositoryDelivery guards the other
// half of the collapse. The startup/reconcile path builds the alert repository
// WITHOUT delivery, so the health path's own message is the only thing that
// ever reaches the owner there. Suppressing the generic delivery must not be
// able to reach this path and mute it.
func TestStartupRightsLossNotifiesOwnerWithoutRepositoryDelivery(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch methodName(r.URL.Path) {
		case "getChat":
			writeTelegramResult(w, map[string]any{
				"id": -1004, "type": "supergroup", "title": "club",
			})
		case "getChatMember":
			writeTelegramResult(w, map[string]any{
				"status": "member",
				"user": map[string]any{
					"id": 123, "is_bot": true, "first_name": "Gatekeeper",
				},
			})
		default:
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}
	})

	chat := HealthChat{
		Key:                 "club_chat",
		Name:                "club chat",
		ID:                  -1004,
		Resource:            string(domain.ResourceChat),
		Severity:            "critical",
		RequiresManageRight: true,
	}
	notifier := notify.NewDurable(store.NewUsers(db), store.NewOutbox(db), nil)

	require.NoError(t, CheckStartupHealth(
		ctx, db, client, notifier,
		[]HealthChat{chat}, []int64{ownerID}, 123, slog.Default(),
	), "CheckStartupHealth with the rights lost")

	warned := ownerDMs(t, db, ownerID)
	require.Len(t, warned, 1,
		"the health check is the only producer here and must still fire")
	assert.Equal(t, messages.HealthFailure(chat.Name, chat.ID, "not_admin"),
		warned[0].text)
	assert.Equal(t, "queued", warned[0].status)
	assert.True(t, warned[0].linked,
		"the warning must carry the alert it reports on")
}

// healthDM is one queued owner notification as the assertions need it: the
// status decides whether it will still be delivered, the link decides whether
// resolving the alert can stop it.
type healthDM struct {
	status string
	linked bool
}

func healthFailureDMs(t *testing.T, db *sql.DB, text string) []healthDM {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `
		SELECT status, alert_id, payload_json
		FROM access_actions
		WHERE action_type = 'send_dm'
		ORDER BY id`)
	require.NoError(t, err, "query send_dm actions")

	defer func() { _ = rows.Close() }()

	var found []healthDM

	for rows.Next() {
		var (
			status  string
			alertID sql.NullInt64
			payload string
		)

		require.NoError(t, rows.Scan(&status, &alertID, &payload), "scan action")

		var decoded struct {
			Text string `json:"text"`
		}

		require.NoError(t, json.Unmarshal([]byte(payload), &decoded),
			"decode dm payload")

		if decoded.Text == text {
			found = append(found, healthDM{
				status: status,
				linked: alertID.Valid,
			})
		}
	}

	require.NoError(t, rows.Err(), "iterate send_dm actions")

	return found
}

// ownerDM is one durable message addressed to the owner, as the duplication
// assertions need it: how many rows exist at all, which copy each one carries,
// and whether it can still be delivered.
type ownerDM struct {
	status string
	text   string
	linked bool
}

func ownerDMs(t *testing.T, db *sql.DB, ownerID int64) []ownerDM {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `
		SELECT status, alert_id, payload_json
		FROM access_actions
		WHERE action_type = 'send_dm' AND tg_id = ?
		ORDER BY id`, ownerID)
	require.NoError(t, err, "query owner send_dm actions")

	defer func() { _ = rows.Close() }()

	var found []ownerDM

	for rows.Next() {
		var (
			status  string
			alertID sql.NullInt64
			payload string
		)

		require.NoError(t, rows.Scan(&status, &alertID, &payload), "scan action")

		var decoded struct {
			Text string `json:"text"`
		}

		require.NoError(t, json.Unmarshal([]byte(payload), &decoded),
			"decode dm payload")

		found = append(found, ownerDM{
			status: status,
			text:   decoded.Text,
			linked: alertID.Valid,
		})
	}

	require.NoError(t, rows.Err(), "iterate owner send_dm actions")

	return found
}

// alertDMsAwaitingDelivery counts messages that report on some alert and can
// still reach their recipient. Once every alert they describe is resolved, the
// only acceptable number is zero.
func alertDMsAwaitingDelivery(t *testing.T, db *sql.DB) int {
	t.Helper()

	var live int

	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM access_actions
		WHERE action_type = 'send_dm'
		  AND alert_id IS NOT NULL
		  AND status IN ('queued', 'running')`,
	).Scan(&live), "count undelivered alert messages")

	return live
}

// newHealthRouter wires the router the way the poller does for one update:
// alerts that deliver through the outbox, and a fresh update id per update.
func newHealthRouter(
	db *sql.DB,
	outbox *store.Outbox,
	chat HealthChat,
	ownerID int64,
	updateID int64,
) *Router {
	return NewRouter(RouterDeps{
		DB:            db,
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
		UpdateID:    updateID,
	}, []HealthChat{chat}, []int64{ownerID}, nil)
}

// Bot memberships as Telegram puts them on the wire. models.ChatMember decodes
// from a `status` discriminator and has no marshaller, so a test that must feed
// the poller raw bytes cannot round-trip a Go value through encoding/json.
const (
	adminMemberJSON = `{
		"status": "administrator",
		"user": {"id": 123, "is_bot": true, "first_name": "Gatekeeper"},
		"can_invite_users": true,
		"can_restrict_members": true
	}`
	plainMemberJSON = `{
		"status": "member",
		"user": {"id": 123, "is_bot": true, "first_name": "Gatekeeper"}
	}`
)

// rightsUpdateJSON is one my_chat_member update as it arrives from Telegram,
// for tests that enter through the poller rather than the router.
func rightsUpdateJSON(
	updateID, chatID, actorID int64,
	oldMember, newMember string,
) []byte {
	return fmt.Appendf(nil, `{
		"update_id": %d,
		"my_chat_member": {
			"chat": {"id": %d, "type": "supergroup", "title": "club chat"},
			"from": {"id": %d, "is_bot": false, "first_name": "Owner"},
			"date": 1,
			"old_chat_member": %s,
			"new_chat_member": %s
		}
	}`, updateID, chatID, actorID, oldMember, newMember)
}

func rightsUpdate(
	chatID, actorID int64,
	oldMember, newMember models.ChatMember,
) *models.Update {
	return &models.Update{
		MyChatMember: &models.ChatMemberUpdated{
			Chat:          models.Chat{ID: chatID, Type: models.ChatTypeSupergroup},
			From:          models.User{ID: actorID, FirstName: "Owner"},
			OldChatMember: oldMember,
			NewChatMember: newMember,
		},
	}
}

func TestGetMeUnauthorizedIsFatalSignal(t *testing.T) {
	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "getMe" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		writeTelegramError(w, http.StatusUnauthorized, "Unauthorized")
	})

	_, err := client.GetMe(context.Background())
	require.Error(t, err, "GetMe must fail on unauthorized")
}

func TestMyChatMemberPrivateUpdatesDMStateAndEnsuresUser(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	_, err := handleMyChatMember(ctx, healthRepos{
		users:  store.NewUsers(db),
		meta:   store.NewMeta(db),
		audit:  store.NewAudit(db),
		alerts: store.NewAlerts(db),
	}, nil, nil, &models.ChatMemberUpdated{
		Chat: models.Chat{ID: 77, Type: models.ChatTypePrivate},
		From: models.User{ID: 77, FirstName: "User"},
		NewChatMember: models.ChatMember{
			Type: models.ChatMemberTypeBanned,
		},
	}, slog.Default())
	require.NoError(t, err, "handleMyChatMember")

	user, err := store.NewUsers(db).Get(ctx, 77)
	require.NoError(t, err, "get user")
	assert.Equal(t, domain.DMBlocked, user.DMState)
}

func TestMyChatMemberPrivatePreservesAdminOwnedUserFields(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	users := store.NewUsers(db)
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:         77,
		Username:     "user",
		DMState:      domain.DMOpen,
		Banned:       true,
		BannedReason: "manual",
		Notes:        "watch",
	}), "upsert user")

	_, err := handleMyChatMember(ctx, healthRepos{
		users:  users,
		meta:   store.NewMeta(db),
		audit:  store.NewAudit(db),
		alerts: store.NewAlerts(db),
	}, nil, nil, &models.ChatMemberUpdated{
		Chat: models.Chat{ID: 77, Type: models.ChatTypePrivate},
		From: models.User{ID: 77, FirstName: "User"},
		NewChatMember: models.ChatMember{
			Type: models.ChatMemberTypeBanned,
		},
	}, slog.Default())
	require.NoError(t, err, "handleMyChatMember")

	user, err := users.Get(ctx, 77)
	require.NoError(t, err, "get user")
	assert.Equal(t, domain.DMBlocked, user.DMState)
	assert.True(t, user.Banned, "ban must be preserved")
	assert.Equal(t, "manual", user.BannedReason)
	assert.Equal(t, "watch", user.Notes)
}

func TestMyChatMemberKnownChatUpdatesHealthAndAlerts(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	chat := HealthChat{
		Key:      "club_chat",
		Name:     "club chat",
		ID:       -1001,
		Severity: "critical",
	}

	effects, err := handleMyChatMember(ctx, healthRepos{
		users:  store.NewUsers(db),
		meta:   store.NewMeta(db),
		audit:  store.NewAudit(db),
		alerts: store.NewAlerts(db),
	}, []HealthChat{chat}, []int64{1}, &models.ChatMemberUpdated{
		Chat: models.Chat{ID: -1001, Type: models.ChatTypeSupergroup},
		From: models.User{ID: 1, FirstName: "Owner"},
		OldChatMember: models.ChatMember{
			Type: models.ChatMemberTypeAdministrator,
			Administrator: &models.ChatMemberAdministrator{
				CanInviteUsers:     true,
				CanRestrictMembers: true,
			},
		},
		NewChatMember: models.ChatMember{
			Type: models.ChatMemberTypeMember,
		},
	}, slog.Default())
	require.NoError(t, err, "handleMyChatMember")

	require.Len(t, effects, 1)
	assert.Equal(t, OutboundDM, effects[0].Kind)

	value, _, err := store.NewMeta(db).Get(ctx, "health.club_chat")
	require.NoError(t, err, "get health")
	assert.Equal(t, "fail:not_admin", value)

	var alerts int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM admin_alerts WHERE kind = 'bot_rights_lost'`,
	).Scan(&alerts), "count alerts")
	assert.Equal(t, 1, alerts)
}

func TestMyChatMemberUnknownChatReturnsDiscoveryDM(t *testing.T) {
	db := testutil.NewDB(t)

	effects, err := handleMyChatMember(context.Background(), healthRepos{
		users:  store.NewUsers(db),
		meta:   store.NewMeta(db),
		audit:  store.NewAudit(db),
		alerts: store.NewAlerts(db),
	}, nil, []int64{1}, &models.ChatMemberUpdated{
		Chat: models.Chat{
			ID:    -2001,
			Type:  models.ChatTypeSupergroup,
			Title: "new chat",
		},
		From: models.User{ID: 1, FirstName: "Owner"},
		NewChatMember: models.ChatMember{
			Type: models.ChatMemberTypeAdministrator,
		},
	}, slog.Default())
	require.NoError(t, err, "handleMyChatMember")

	require.Len(t, effects, 1)
	assert.Equal(t, int64(1), effects[0].TGID)
	assert.NotEmpty(t, effects[0].Text, "discovery dm must carry text")
}

func TestMyChatMemberUnknownChatIgnoresLaterStatusChange(t *testing.T) {
	db := testutil.NewDB(t)

	// Telegram emits a separate my_chat_member for every status change.
	// Once the bot is already present (e.g. member -> administrator, or an
	// admin-rights edit), the discovery DM must not fire again: the owner
	// has already been told about the chat on the join transition.
	effects, err := handleMyChatMember(context.Background(), healthRepos{
		users:  store.NewUsers(db),
		meta:   store.NewMeta(db),
		audit:  store.NewAudit(db),
		alerts: store.NewAlerts(db),
	}, nil, []int64{1}, &models.ChatMemberUpdated{
		Chat: models.Chat{
			ID:    -2001,
			Type:  models.ChatTypeSupergroup,
			Title: "new chat",
		},
		From: models.User{ID: 1, FirstName: "Owner"},
		OldChatMember: models.ChatMember{
			Type: models.ChatMemberTypeMember,
		},
		NewChatMember: models.ChatMember{
			Type: models.ChatMemberTypeAdministrator,
		},
	}, slog.Default())
	require.NoError(t, err, "handleMyChatMember")
	assert.Empty(t, effects,
		"later status change in unknown chat must not re-send discovery dm")
}

func TestMyChatMemberUnknownChatIgnoresRemoval(t *testing.T) {
	db := testutil.NewDB(t)

	effects, err := handleMyChatMember(context.Background(), healthRepos{
		users:  store.NewUsers(db),
		meta:   store.NewMeta(db),
		audit:  store.NewAudit(db),
		alerts: store.NewAlerts(db),
	}, nil, []int64{1}, &models.ChatMemberUpdated{
		Chat: models.Chat{
			ID:    -2001,
			Type:  models.ChatTypeSupergroup,
			Title: "old chat",
		},
		From: models.User{ID: 1, FirstName: "Owner"},
		NewChatMember: models.ChatMember{
			Type: models.ChatMemberTypeLeft,
		},
	}, slog.Default())
	require.NoError(t, err, "handleMyChatMember")
	assert.Empty(t, effects, "removal of unknown chat must produce no effects")
}

func newBotAPITestClient(
	t *testing.T, handler http.HandlerFunc,
) *Client {
	t.Helper()

	httpClient := &http.Client{Transport: roundTripFunc(
		func(req *http.Request) (*http.Response, error) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			return recorder.Result(), nil
		})}

	client, err := NewClient("123:ABC",
		WithServerURL("http://telegram.test"),
		WithHTTPClient(httpClient))
	require.NoError(t, err, "NewClient")

	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func methodName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}

	return path
}

func writeTelegramResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"result": result,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeTelegramError(w http.ResponseWriter, code int, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	if err := json.NewEncoder(w).Encode(map[string]any{
		"ok":          false,
		"error_code":  code,
		"description": description,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
