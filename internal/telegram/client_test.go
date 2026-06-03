package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	botapi "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/messages"
)

func TestNormalizeErrorCategories(t *testing.T) {
	rateErr := NormalizeError("sendMessage", &botapi.TooManyRequestsError{
		Message:    "too many requests",
		RetryAfter: 12,
	})

	var apiErr *APIError
	require.ErrorAs(t, rateErr, &apiErr)
	assert.Equal(t, ErrorCategoryRateLimited, apiErr.Category)
	assert.Equal(t, 12, apiErr.RetryAfter)

	blockErr := NormalizeError("sendMessage",
		fmt.Errorf("%w, bot was blocked by the user", botapi.ErrorForbidden))
	assert.True(t, IsDMBlocked(blockErr), "403 send must classify as dm_blocked")

	rightsErr := NormalizeError("getChatMember",
		fmt.Errorf("%w, not enough rights", botapi.ErrorBadRequest))
	assert.True(t, IsPermanentRights(rightsErr),
		"rights error must classify as permanent_rights")
}

func TestIsExpectedNoopClassification(t *testing.T) {
	err := fmt.Errorf("%w, USER_NOT_PARTICIPANT", botapi.ErrorBadRequest)
	assert.True(t, IsExpectedNoop("ban_chat_member", err))

	alreadyMember := fmt.Errorf("%w, user is already a member", botapi.ErrorBadRequest)
	assert.True(t, IsExpectedNoop("approve_join", alreadyMember))

	broadAlready := fmt.Errorf("%w, already failed internally", botapi.ErrorBadRequest)
	assert.False(t, IsExpectedNoop("approve_join", broadAlready),
		"broad 'already' must not be treated as a no-op")

	chatErr := fmt.Errorf("%w, chat not found", botapi.ErrorBadRequest)
	assert.False(t, IsExpectedNoop("soft_kick", chatErr))

	got := NormalizeError("getChatMember", err)
	assert.Error(t, got, "NormalizeError for health check must return a real signal")
}

func TestSendMessageClassifiesForbiddenByTarget(t *testing.T) {
	client := newBotAPITestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeTelegramError(w, http.StatusForbidden, "Forbidden")
	})

	privateErr := client.SendMessage(context.Background(), 42, "hello")
	assert.True(t, IsDMBlocked(privateErr), "private 403 must classify as dm_blocked")

	groupErr := client.SendMessage(context.Background(), -1001, "hello")

	var apiErr *APIError
	require.ErrorAs(t, groupErr, &apiErr)
	assert.Equal(t, ErrorCategoryForbidden, apiErr.Category)
}

func TestSendMessageSerializesParseModeOnlyForFormattedMessages(t *testing.T) {
	var captured []map[string]string

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "sendMessage" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		captured = append(captured, decodeSendMessageRequest(t, r))

		writeTelegramResult(w, map[string]any{"message_id": 1})
	})

	require.NoError(t, client.SendFormattedMessage(
		context.Background(), 42, "<b>hello</b>", messages.ParseModeHTML,
	), "SendFormattedMessage")

	require.NoError(t, client.SendMessage(context.Background(), 42, "<b>plain</b>"),
		"SendMessage")

	require.NoError(t, client.SendFormattedMessageWithReplyMarkup(
		context.Background(),
		42,
		"<b>retry</b>",
		messages.ParseModeHTML,
		models.InlineKeyboardMarkup{},
	), "SendFormattedMessageWithReplyMarkup")

	require.Len(t, captured, 3)
	assert.Equal(t, messages.ParseModeHTML, captured[0]["parse_mode"],
		"formatted body must carry HTML parse mode")
	assert.Empty(t, captured[1]["parse_mode"], "plain body must carry no parse_mode")
	assert.Equal(t, messages.ParseModeHTML, captured[2]["parse_mode"],
		"reply markup body must carry HTML parse mode")
}

func TestCreateChatInviteLinkSerializesParams(t *testing.T) {
	expiresAt := time.Unix(1_700_000_000, 0)

	var captured map[string]any

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "createChatInviteLink" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&captured),
			"decode request") {
			return
		}

		writeTelegramResult(w, map[string]any{
			"invite_link":          "https://t.me/+abc",
			"creates_join_request": true,
			"creator": map[string]any{
				"id": 123, "is_bot": true, "first_name": "Gatekeeper",
			},
		})
	})

	link, err := client.CreateChatInviteLink(context.Background(), CreateChatInviteLinkParams{
		ChatID:             -1001,
		Name:               "gk-shared-chat",
		ExpireAt:           &expiresAt,
		MemberLimit:        1,
		CreatesJoinRequest: true,
	})
	require.NoError(t, err, "CreateChatInviteLink")

	assert.Equal(t, "https://t.me/+abc", link.InviteLink)

	assert.InDelta(t, float64(-1001), captured["chat_id"], 0)
	assert.Equal(t, "gk-shared-chat", captured["name"])
	assert.InDelta(t, float64(1_700_000_000), captured["expire_date"], 0)
	assert.InDelta(t, float64(1), captured["member_limit"], 0)
	assert.Equal(t, true, captured["creates_join_request"])
}

func TestUnbanChatMemberSerializesOnlyIfBanned(t *testing.T) {
	var captured map[string]any

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "unbanChatMember" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&captured),
			"decode request") {
			return
		}

		writeTelegramResult(w, true)
	})

	require.NoError(t, client.UnbanChatMember(context.Background(), -1001, 42, true),
		"UnbanChatMember")

	assert.InDelta(t, float64(-1001), captured["chat_id"], 0)
	assert.InDelta(t, float64(42), captured["user_id"], 0)
	assert.Equal(t, true, captured["only_if_banned"])
}

type commandsRequest struct {
	Commands []struct {
		Command string `json:"command"`
	} `json:"commands"`
	Scope map[string]any `json:"scope"`
}

func TestSetMyCommandsIncludesOwnerAdminCommands(t *testing.T) {
	var captured []commandsRequest

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "setMyCommands" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		req := decodeSetMyCommandsRequest(t, r)
		captured = append(captured, req)

		writeTelegramResult(w, true)
	})

	require.NoError(t, client.SetMyCommands(context.Background(), []int64{100}),
		"SetMyCommands")

	require.Len(t, captured, 2, "want default and owner scope")

	defaultCommands := commandSet(captured[0].Commands)
	assert.False(t, defaultCommands["grant"], "default scope must omit owner commands")
	assert.False(t, defaultCommands["ban"], "default scope must omit owner commands")

	ownerCommands := commandSet(captured[1].Commands)
	for _, command := range []string{
		"start", "help", "status", "here", "whois",
		"grant", "revoke", "ban", "unban", "sync",
		"stats", "alerts", "export", "chats", "help_admin",
	} {
		assert.Truef(t, ownerCommands[command], "owner scope missing %q", command)
	}
}

func TestSetAndDeleteWebhookSerializeBotAPIRequests(t *testing.T) {
	var (
		methods       []string
		setWebhook    map[string]any
		deleteWebhook map[string]any
	)

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method := methodName(r.URL.Path)
		methods = append(methods, method)

		switch method {
		case "setWebhook":
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&setWebhook),
				"decode setWebhook") {
				return
			}
		case "deleteWebhook":
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&deleteWebhook),
				"decode deleteWebhook") {
				return
			}
		default:
			t.Fatalf("unexpected method %s", method)
		}

		writeTelegramResult(w, true)
	})

	require.NoError(t, client.SetWebhook(context.Background(),
		"https://bot.example.com/webhooks/telegram",
		"secret",
		[]string{"message", "chat_member"},
	), "SetWebhook")

	require.NoError(t, client.DeleteWebhook(context.Background()), "DeleteWebhook")

	assert.Equal(t, "setWebhook,deleteWebhook", strings.Join(methods, ","))

	assert.Equal(t, "https://bot.example.com/webhooks/telegram", setWebhook["url"])
	assert.Equal(t, "secret", setWebhook["secret_token"])

	allowed, ok := setWebhook["allowed_updates"].([]any)
	require.True(t, ok, "allowed_updates must be an explicit list")
	require.Len(t, allowed, 2)
	assert.Equal(t, "message", allowed[0])

	assert.Equal(t, false, deleteWebhook["drop_pending_updates"],
		"deleteWebhook must keep pending updates")
}

func TestRawRequestNormalizesRetryAfter(t *testing.T) {
	client := newBotAPITestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)

		if err := json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  http.StatusTooManyRequests,
			"description": "Too Many Requests",
			"parameters": map[string]any{
				"retry_after": 17,
			},
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	err := client.ApproveChatJoinRequest(context.Background(), -1001, 42)

	wait, ok := RetryAfter(err)
	require.True(t, ok, "rate-limited error must expose retry_after")
	assert.Equal(t, 17*time.Second, wait)
}

func commandSet(commands []struct {
	Command string `json:"command"`
}) map[string]bool {
	out := make(map[string]bool, len(commands))
	for _, command := range commands {
		out[command.Command] = true
	}

	return out
}

func decodeSendMessageRequest(t *testing.T, r *http.Request) map[string]string {
	t.Helper()

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		var req map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req),
			"decode sendMessage")

		return req
	}

	if strings.HasPrefix(contentType, "multipart/form-data") {
		require.NoError(t, r.ParseMultipartForm(1<<20),
			"parse sendMessage multipart request")
	} else {
		require.NoError(t, r.ParseForm(), "parse sendMessage form request")
	}

	return map[string]string{
		"text":       r.FormValue("text"),
		"parse_mode": r.FormValue("parse_mode"),
	}
}

func decodeSetMyCommandsRequest(t *testing.T, r *http.Request) commandsRequest {
	t.Helper()

	var req commandsRequest

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "decode request")

		return req
	}

	if strings.HasPrefix(contentType, "multipart/form-data") {
		require.NoError(t, r.ParseMultipartForm(1<<20), "parse multipart request")
	} else {
		require.NoError(t, r.ParseForm(), "parse form request")
	}

	require.NoError(t,
		json.Unmarshal([]byte(r.FormValue("commands")), &req.Commands),
		"decode commands form field")

	if rawScope := r.FormValue("scope"); rawScope != "" {
		require.NoError(t, json.Unmarshal([]byte(rawScope), &req.Scope),
			"decode scope form field")
	}

	return req
}
