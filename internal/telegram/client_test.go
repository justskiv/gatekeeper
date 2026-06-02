package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	botapi "github.com/go-telegram/bot"
)

func TestNormalizeErrorCategories(t *testing.T) {
	rateErr := NormalizeError("sendMessage", &botapi.TooManyRequestsError{
		Message:    "too many requests",
		RetryAfter: 12,
	})

	var apiErr *APIError
	if !errors.As(rateErr, &apiErr) ||
		apiErr.Category != ErrorCategoryRateLimited ||
		apiErr.RetryAfter != 12 {
		t.Fatalf("rate error = %#v, want retry_after category", rateErr)
	}

	blockErr := NormalizeError("sendMessage",
		fmt.Errorf("%w, bot was blocked by the user", botapi.ErrorForbidden))
	if !IsDMBlocked(blockErr) {
		t.Fatalf("sendMessage 403 = %v, want dm_blocked", blockErr)
	}

	rightsErr := NormalizeError("getChatMember",
		fmt.Errorf("%w, not enough rights", botapi.ErrorBadRequest))
	if !IsPermanentRights(rightsErr) {
		t.Fatalf("rights error = %v, want permanent_rights", rightsErr)
	}
}

func TestIsExpectedNoopClassification(t *testing.T) {
	err := fmt.Errorf("%w, USER_NOT_PARTICIPANT", botapi.ErrorBadRequest)
	if !IsExpectedNoop("ban_chat_member", err) {
		t.Fatalf("IsExpectedNoop(%v) = false, want true", err)
	}

	alreadyMember := fmt.Errorf("%w, user is already a member", botapi.ErrorBadRequest)
	if !IsExpectedNoop("approve_join", alreadyMember) {
		t.Fatalf("IsExpectedNoop(%v) = false, want true", alreadyMember)
	}

	broadAlready := fmt.Errorf("%w, already failed internally", botapi.ErrorBadRequest)
	if IsExpectedNoop("approve_join", broadAlready) {
		t.Fatalf("IsExpectedNoop(%v) = true, want false", broadAlready)
	}

	chatErr := fmt.Errorf("%w, chat not found", botapi.ErrorBadRequest)
	if IsExpectedNoop("soft_kick", chatErr) {
		t.Fatal("IsExpectedNoop(chat not found) = true, want false")
	}

	got := NormalizeError("getChatMember", err)
	if got == nil {
		t.Fatal("NormalizeError for health check returned nil, want real signal")
	}
}

func TestSendMessageClassifiesForbiddenByTarget(t *testing.T) {
	client := newBotAPITestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeTelegramError(w, http.StatusForbidden, "Forbidden")
	})

	privateErr := client.SendMessage(context.Background(), 42, "hello")
	if !IsDMBlocked(privateErr) {
		t.Fatalf("private send error = %v, want dm_blocked", privateErr)
	}

	groupErr := client.SendMessage(context.Background(), -1001, "hello")

	var apiErr *APIError
	if !errors.As(groupErr, &apiErr) ||
		apiErr.Category != ErrorCategoryForbidden {
		t.Fatalf("group send error = %#v, want forbidden", groupErr)
	}
}

func TestCreateChatInviteLinkSerializesParams(t *testing.T) {
	expiresAt := time.Unix(1_700_000_000, 0)

	var captured map[string]any

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "createChatInviteLink" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
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
	if err != nil {
		t.Fatalf("CreateChatInviteLink: %v", err)
	}

	if link.InviteLink != "https://t.me/+abc" {
		t.Fatalf("invite link = %q", link.InviteLink)
	}

	if captured["chat_id"] != float64(-1001) ||
		captured["name"] != "gk-shared-chat" ||
		captured["expire_date"] != float64(1_700_000_000) ||
		captured["member_limit"] != float64(1) ||
		captured["creates_join_request"] != true {
		t.Fatalf("captured request = %#v", captured)
	}
}

func TestUnbanChatMemberSerializesOnlyIfBanned(t *testing.T) {
	var captured map[string]any

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "unbanChatMember" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		writeTelegramResult(w, true)
	})

	if err := client.UnbanChatMember(context.Background(), -1001, 42, true); err != nil {
		t.Fatalf("UnbanChatMember: %v", err)
	}

	if captured["chat_id"] != float64(-1001) ||
		captured["user_id"] != float64(42) ||
		captured["only_if_banned"] != true {
		t.Fatalf("captured request = %#v", captured)
	}
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

	if err := client.SetMyCommands(context.Background(), []int64{100}); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}

	if len(captured) != 2 {
		t.Fatalf("captured calls = %d, want default and owner scope", len(captured))
	}

	defaultCommands := commandSet(captured[0].Commands)
	if defaultCommands["grant"] || defaultCommands["ban"] {
		t.Fatalf("default commands = %v, want no owner-only commands",
			defaultCommands)
	}

	ownerCommands := commandSet(captured[1].Commands)
	for _, command := range []string{
		"start", "help", "status", "here", "whois",
		"grant", "revoke", "ban", "unban", "sync",
	} {
		if !ownerCommands[command] {
			t.Fatalf("owner commands = %v, missing %q", ownerCommands, command)
		}
	}
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
	if !ok || wait != 17*time.Second {
		t.Fatalf("RetryAfter(%v) = (%v, %v), want 17s true", err, wait, ok)
	}
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

func decodeSetMyCommandsRequest(t *testing.T, r *http.Request) commandsRequest {
	t.Helper()

	var req commandsRequest

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		return req
	}

	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart request: %v", err)
		}
	} else if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form request: %v", err)
	}

	if err := json.Unmarshal([]byte(r.FormValue("commands")), &req.Commands); err != nil {
		t.Fatalf("decode commands form field: %v", err)
	}

	if rawScope := r.FormValue("scope"); rawScope != "" {
		if err := json.Unmarshal([]byte(rawScope), &req.Scope); err != nil {
			t.Fatalf("decode scope form field: %v", err)
		}
	}

	return req
}
