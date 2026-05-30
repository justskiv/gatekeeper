// Package bot contains Telegram command handlers.
package bot

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

// Reply is a post-commit Telegram message effect.
type Reply struct {
	ChatID int64
	TGID   int64
	Text   string
	DM     bool
}

// Result is the durable command handling result.
type Result struct {
	Ignored bool
	Replies []Reply
}

// UserCommands handles /start, /help and /here.
type UserCommands struct {
	users  *store.Users
	owners map[int64]struct{}
}

// NewUserCommands returns user command handlers bound to users.
func NewUserCommands(users *store.Users, ownerIDs []int64) *UserCommands {
	owners := make(map[int64]struct{}, len(ownerIDs))
	for _, id := range ownerIDs {
		owners[id] = struct{}{}
	}
	return &UserCommands{users: users, owners: owners}
}

// HandlePrivate routes a private message to /start or /help.
func (h *UserCommands) HandlePrivate(
	ctx context.Context, msg *models.Message,
) (Result, error) {
	if msg == nil || msg.From == nil {
		return Result{Ignored: true}, nil
	}

	cmd := commandName(msg.Text)
	switch cmd {
	case "help":
		if err := h.rememberPrivateUser(ctx, *msg.From); err != nil {
			return Result{}, err
		}
		return Result{Replies: []Reply{{
			ChatID: msg.Chat.ID,
			TGID:   msg.From.ID,
			Text:   messages.Help(),
			DM:     true,
		}}}, nil
	case "start", "":
		if err := h.rememberPrivateUser(ctx, *msg.From); err != nil {
			return Result{}, err
		}
		return Result{Replies: []Reply{{
			ChatID: msg.Chat.ID,
			TGID:   msg.From.ID,
			Text:   messages.Welcome(),
			DM:     true,
		}}}, nil
	default:
		return Result{Ignored: true}, nil
	}
}

func (h *UserCommands) rememberPrivateUser(ctx context.Context, user models.User) error {
	return h.users.Upsert(ctx, userFromTelegram(user, domain.DMOpen))
}

// HandleHere returns chat id/type for owners in non-private chats.
func (h *UserCommands) HandleHere(msg *models.Message) Result {
	if msg == nil || msg.From == nil || commandName(msg.Text) != "here" {
		return Result{Ignored: true}
	}
	if _, ok := h.owners[msg.From.ID]; !ok {
		return Result{Ignored: true}
	}
	return Result{Replies: []Reply{{
		ChatID: msg.Chat.ID,
		TGID:   msg.From.ID,
		Text:   messages.Here(msg.Chat.ID, string(msg.Chat.Type)),
	}}}
}

func userFromTelegram(user models.User, dmState domain.DMState) domain.User {
	return domain.User{
		TGID:         user.ID,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		LanguageCode: user.LanguageCode,
		IsBot:        user.IsBot,
		DMState:      dmState,
	}
}

func commandName(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || !strings.HasPrefix(text, "/") {
		return ""
	}
	cmd := strings.Fields(text)[0]
	cmd = strings.TrimPrefix(cmd, "/")
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at]
	}
	return strings.ToLower(cmd)
}
