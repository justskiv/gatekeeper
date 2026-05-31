package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

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

func TestNormalizeActionErrorExpectedNoop(t *testing.T) {
	err := fmt.Errorf("%w, USER_NOT_PARTICIPANT", botapi.ErrorBadRequest)
	if got := NormalizeActionError("ban_chat_member", err); got != nil {
		t.Fatalf("NormalizeActionError = %v, want nil no-op", got)
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
