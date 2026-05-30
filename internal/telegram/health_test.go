package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

func TestCheckStartupHealthRecordsHealthyChat(t *testing.T) {
	db := newTestDB(t)
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
	if _, err := store.NewAlerts(db).Create(context.Background(), store.AlertInput{
		Severity: "critical",
		Kind:     alertKindBotRightsLost,
		Title:    alertTitle(chat),
		Detail:   "stale",
	}); err != nil {
		t.Fatalf("create stale alert: %v", err)
	}
	if err := CheckStartupHealth(
		context.Background(), db, client, nil,
		[]HealthChat{chat}, nil, 123, slog.Default(),
	); err != nil {
		t.Fatalf("CheckStartupHealth: %v", err)
	}

	value, ok, err := store.NewMeta(db).Get(context.Background(), "health.club_chat")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	if !ok || value != "ok" {
		t.Fatalf("health = (%q, %v), want ok", value, ok)
	}
	var openAlerts int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM admin_alerts
		WHERE status = 'open' AND kind = 'bot_rights_lost'`,
	).Scan(&openAlerts); err != nil {
		t.Fatalf("count open alerts: %v", err)
	}
	if openAlerts != 0 {
		t.Fatalf("open alerts = %d, want 0", openAlerts)
	}
}

func TestCheckStartupHealthDegradesMissingRights(t *testing.T) {
	db := newTestDB(t)
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
	for i := 0; i < 2; i++ {
		if err := CheckStartupHealth(
			context.Background(), db, client, nil,
			[]HealthChat{chat}, nil, 123, slog.Default(),
		); err != nil {
			t.Fatalf("CheckStartupHealth: %v", err)
		}
	}

	value, _, err := store.NewMeta(db).Get(context.Background(), "health.club_chat")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	if value != "fail:not_admin" {
		t.Fatalf("health = %q, want fail:not_admin", value)
	}
	var alerts int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM admin_alerts WHERE severity = 'critical'`,
	).Scan(&alerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if alerts != 1 {
		t.Fatalf("alerts = %d, want 1", alerts)
	}
}

func TestCheckStartupHealthRejectsWrongChatType(t *testing.T) {
	db := newTestDB(t)
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
	if err := CheckStartupHealth(
		context.Background(), db, client, nil,
		[]HealthChat{chat}, nil, 123, slog.Default(),
	); err != nil {
		t.Fatalf("CheckStartupHealth: %v", err)
	}

	value, _, err := store.NewMeta(db).Get(context.Background(), "health.club_channel")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	if value != "fail:wrong_type" {
		t.Fatalf("health = %q, want fail:wrong_type", value)
	}
}

func TestGetMeUnauthorizedIsFatalSignal(t *testing.T) {
	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "getMe" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}
		writeTelegramError(w, http.StatusUnauthorized, "Unauthorized")
	})

	if _, err := client.GetMe(context.Background()); err == nil {
		t.Fatal("GetMe returned nil error, want unauthorized")
	}
}

func TestMyChatMemberPrivateUpdatesDMStateAndEnsuresUser(t *testing.T) {
	db := newTestDB(t)
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
	if err != nil {
		t.Fatalf("handleMyChatMember: %v", err)
	}

	user, err := store.NewUsers(db).Get(ctx, 77)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.DMState != domain.DMBlocked {
		t.Fatalf("dm_state = %s, want blocked", user.DMState)
	}
}

func TestMyChatMemberPrivatePreservesAdminOwnedUserFields(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	users := store.NewUsers(db)
	if err := users.Upsert(ctx, domain.User{
		TGID:         77,
		Username:     "user",
		DMState:      domain.DMOpen,
		Banned:       true,
		BannedReason: "manual",
		Notes:        "watch",
	}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

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
	if err != nil {
		t.Fatalf("handleMyChatMember: %v", err)
	}

	user, err := users.Get(ctx, 77)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.DMState != domain.DMBlocked ||
		!user.Banned ||
		user.BannedReason != "manual" ||
		user.Notes != "watch" {
		t.Fatalf("user = %+v, want blocked with admin fields preserved", user)
	}
}

func TestMyChatMemberKnownChatUpdatesHealthAndAlerts(t *testing.T) {
	db := newTestDB(t)
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
	if err != nil {
		t.Fatalf("handleMyChatMember: %v", err)
	}
	if len(effects) != 1 || effects[0].Kind != OutboundDM {
		t.Fatalf("effects = %+v, want one owner dm", effects)
	}

	value, _, err := store.NewMeta(db).Get(ctx, "health.club_chat")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	if value != "fail:not_admin" {
		t.Fatalf("health = %q, want fail:not_admin", value)
	}
	var alerts int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM admin_alerts WHERE kind = 'bot_rights_lost'`,
	).Scan(&alerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if alerts != 1 {
		t.Fatalf("alerts = %d, want 1", alerts)
	}
}

func TestMyChatMemberUnknownChatReturnsDiscoveryDM(t *testing.T) {
	db := newTestDB(t)
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
	if err != nil {
		t.Fatalf("handleMyChatMember: %v", err)
	}
	if len(effects) != 1 || effects[0].TGID != 1 || effects[0].Text == "" {
		t.Fatalf("effects = %+v, want owner discovery dm", effects)
	}
}

func TestMyChatMemberUnknownChatIgnoresRemoval(t *testing.T) {
	db := newTestDB(t)
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
	if err != nil {
		t.Fatalf("handleMyChatMember: %v", err)
	}
	if len(effects) != 0 {
		t.Fatalf("effects = %+v, want none", effects)
	}
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
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
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
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"result": result,
	})
}

func writeTelegramError(w http.ResponseWriter, code int, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":          false,
		"error_code":  code,
		"description": description,
	})
}
