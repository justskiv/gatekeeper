// Package bot contains Telegram command handlers.
package bot

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
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

// CommandDeps are transaction-bound dependencies for bot commands.
type CommandDeps struct {
	Users         *store.Users
	Subscriptions *store.Subscriptions
	Grants        *store.Grants
	Audit         *store.Audit
	Whitelist     *store.Whitelist
	StatusEngine  *engine.Engine
	Preflight     *engine.Snapshot
}

// UserCommands handles user and owner bot commands.
type UserCommands struct {
	deps   CommandDeps
	owners map[int64]struct{}
}

// NewUserCommands returns user command handlers bound to users.
func NewUserCommands(users *store.Users, ownerIDs []int64) *UserCommands {
	return NewCommands(CommandDeps{Users: users}, ownerIDs)
}

// NewCommands returns command handlers bound to one tx2 dependency set.
func NewCommands(deps CommandDeps, ownerIDs []int64) *UserCommands {
	owners := make(map[int64]struct{}, len(ownerIDs))
	for _, id := range ownerIDs {
		owners[id] = struct{}{}
	}

	return &UserCommands{deps: deps, owners: owners}
}

// HandlePrivate routes a private message to supported commands.
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
	case "status":
		return h.handleStatus(ctx, msg)
	case "whois":
		return h.handleWhois(ctx, msg)
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
	if h.deps.Users == nil {
		return nil
	}
	// A private incoming message is an explicit signal that DM is open;
	// refresh the profile cache and intentionally reopen dm_state.
	return h.deps.Users.Upsert(ctx, userFromTelegram(user, domain.DMOpen))
}

func (h *UserCommands) handleStatus(
	ctx context.Context, msg *models.Message,
) (Result, error) {
	if err := h.rememberPrivateUser(ctx, *msg.From); err != nil {
		return Result{}, err
	}

	tgID := msg.From.ID
	if h.deps.StatusEngine != nil &&
		h.deps.Preflight != nil &&
		h.deps.Preflight.TGID == tgID {
		if err := h.deps.StatusEngine.ApplyObservations(
			ctx, engineStore(h.deps), tgID, h.deps.Preflight.Verdicts,
		); err != nil {
			return Result{}, err
		}
	}

	var subs []domain.Subscription

	if h.deps.Subscriptions != nil {
		var err error

		subs, err = h.deps.Subscriptions.ListActiveByUser(ctx, tgID)
		if err != nil {
			return Result{}, err
		}

		subs = currentSubscriptions(subs, time.Now())
	}

	var grants []domain.AccessGrant

	if h.deps.Grants != nil {
		var err error

		grants, err = h.deps.Grants.ListByUser(ctx, tgID)
		if err != nil {
			return Result{}, err
		}
	}

	decision, err := h.statusDecision(ctx, tgID)
	if err != nil {
		return Result{}, err
	}

	return Result{Replies: []Reply{{
		ChatID: msg.Chat.ID,
		TGID:   tgID,
		Text:   messages.Status(decision, subs, grants),
		DM:     true,
	}}}, nil
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

func (h *UserCommands) isOwner(tgID int64) bool {
	_, ok := h.owners[tgID]

	return ok
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
	return CommandName(text)
}

// CommandName returns the lower-cased bot command without a bot suffix.
func CommandName(text string) string {
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

func (h *UserCommands) statusDecision(
	ctx context.Context,
	tgID int64,
) (domain.AccessDecision, error) {
	if h.deps.Preflight != nil && h.deps.Preflight.TGID == tgID {
		return h.deps.Preflight.Decision, nil
	}

	if canReadPersistedDecision(h.deps) {
		return h.deps.StatusEngine.PersistedDecision(ctx, engineStore(h.deps), tgID)
	}

	return domain.AccessDecision{
		TGID:   tgID,
		Status: domain.StatusUnknown,
		Reasons: []domain.AccessReason{{
			Source:  domain.Platform("system"),
			Verdict: domain.VerdictUnknown,
			Detail:  messages.ReasonStatusNotComputed(),
		}},
	}, nil
}

func canReadPersistedDecision(deps CommandDeps) bool {
	return deps.StatusEngine != nil &&
		deps.Users != nil &&
		deps.Subscriptions != nil &&
		deps.Whitelist != nil
}

func currentSubscriptions(
	subs []domain.Subscription,
	now time.Time,
) []domain.Subscription {
	out := subs[:0]
	for _, sub := range subs {
		if sub.ExpiresAt == nil || now.Before(*sub.ExpiresAt) {
			out = append(out, sub)
		}
	}

	return out
}

func engineStore(deps CommandDeps) engine.Store {
	return engine.Store{
		Users:         deps.Users,
		Subscriptions: deps.Subscriptions,
		Audit:         deps.Audit,
		Revocations:   nil,
		Whitelist:     deps.Whitelist,
	}
}

func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}
