package telegram

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot/models"

	commandbot "github.com/justskiv/gatekeeper/internal/bot"
	"github.com/justskiv/gatekeeper/internal/store"
)

// OutboundKind tells the poller how to deliver a post-commit effect.
type OutboundKind string

const (
	OutboundChatMessage OutboundKind = "chat_message"
	OutboundDM          OutboundKind = "dm"
)

// OutboundMessage is a Telegram call to make after tx2 commits.
type OutboundMessage struct {
	Kind   OutboundKind
	ChatID int64
	TGID   int64
	Text   string
}

// RouteResult describes how an update reached a terminal state.
type RouteResult struct {
	Status  store.TelegramUpdateStatus
	Effects []OutboundMessage
}

// Router dispatches persisted updates to phase-specific handlers.
type Router struct {
	users    *store.Users
	meta     *store.Meta
	audit    *store.Audit
	alerts   *store.Alerts
	chats    []HealthChat
	ownerIDs []int64
	logger   *slog.Logger
}

// RouterDeps are transaction-bound repositories for one update.
type RouterDeps struct {
	Users  *store.Users
	Meta   *store.Meta
	Audit  *store.Audit
	Alerts *store.Alerts
}

// NewRouter returns a router bound to one tx2 repository set.
func NewRouter(
	deps RouterDeps,
	chats []HealthChat,
	ownerIDs []int64,
	logger *slog.Logger,
) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	return &Router{
		users:    deps.Users,
		meta:     deps.Meta,
		audit:    deps.Audit,
		alerts:   deps.Alerts,
		chats:    chats,
		ownerIDs: ownerIDs,
		logger:   logger,
	}
}

// Route applies domain changes for an update without calling Telegram.
func (r *Router) Route(ctx context.Context, update *models.Update) (RouteResult, error) {
	switch {
	case update == nil:
		return ignored(), nil
	case update.Message != nil:
		return r.routeMessage(ctx, update.Message)
	case update.MyChatMember != nil:
		effects, err := handleMyChatMember(ctx, healthRepos{
			users:  r.users,
			meta:   r.meta,
			audit:  r.audit,
			alerts: r.alerts,
		}, r.chats, r.ownerIDs, update.MyChatMember, r.logger)
		if err != nil {
			return RouteResult{}, err
		}
		return processed(effects), nil
	case update.ChatMember != nil || update.ChatJoinRequest != nil:
		return ignored(), nil
	case update.CallbackQuery != nil:
		return ignored(), nil
	default:
		return ignored(), nil
	}
}

func (r *Router) routeMessage(
	ctx context.Context, msg *models.Message,
) (RouteResult, error) {
	commands := commandbot.NewUserCommands(r.users, r.ownerIDs)
	if msg.Chat.Type == models.ChatTypePrivate {
		result, err := commands.HandlePrivate(ctx, msg)
		if err != nil {
			return RouteResult{}, err
		}
		return fromCommandResult(result), nil
	}
	return fromCommandResult(commands.HandleHere(msg)), nil
}

func fromCommandResult(result commandbot.Result) RouteResult {
	if result.Ignored {
		return ignored()
	}
	effects := make([]OutboundMessage, 0, len(result.Replies))
	for _, reply := range result.Replies {
		kind := OutboundChatMessage
		if reply.DM {
			kind = OutboundDM
		}
		effects = append(effects, OutboundMessage{
			Kind:   kind,
			ChatID: reply.ChatID,
			TGID:   reply.TGID,
			Text:   reply.Text,
		})
	}
	return processed(effects)
}

func processed(effects []OutboundMessage) RouteResult {
	return RouteResult{
		Status:  store.TelegramUpdateProcessed,
		Effects: effects,
	}
}

func ignored() RouteResult {
	return RouteResult{Status: store.TelegramUpdateIgnored}
}
