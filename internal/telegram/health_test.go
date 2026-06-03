package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
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
