package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/config"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	healthOK = "ok"

	alertKindBotRightsLost = "bot_rights_lost"
)

// HealthChat describes one configured chat whose bot rights are checked.
type HealthChat struct {
	Key                 string
	Name                string
	ID                  int64
	Source              string
	Resource            string
	Severity            string
	RequiresManageRight bool

	// PostingOnly marks a feed chat (EVENT_LOG_CHAT_ID) where the bot only
	// needs to be a member able to send messages — administrator status is
	// not required, so a plain member with posting rights is healthy.
	PostingOnly bool
}

// EventLogHealthChat describes the operator event-log feed chat. The bot only
// needs posting rights there (member or admin), and a problem is severity
// error: the feed is observability, not an access-control resource, and MUST
// NOT be treated as one of the four source/club chats.
func EventLogHealthChat(cfg config.Config) HealthChat {
	return HealthChat{
		Key:         "event_log",
		Name:        "operator event log",
		ID:          cfg.EventLogChatID,
		Resource:    string(domain.ResourceChat),
		Severity:    "error",
		PostingOnly: true,
	}
}

// HealthChatsFromConfig returns the four chats required by the product.
func HealthChatsFromConfig(cfg config.Config) []HealthChat {
	return []HealthChat{
		{
			Key:      "boosty_group",
			Name:     "Boosty group",
			ID:       cfg.BoostyGroupID,
			Source:   string(domain.PlatformBoosty),
			Resource: string(domain.ResourceChat),
			Severity: "error",
		},
		{
			Key:      "tribute_channel",
			Name:     "Tribute channel",
			ID:       cfg.TributeChannelID,
			Source:   string(domain.PlatformTribute),
			Resource: string(domain.ResourceChannel),
			Severity: "error",
		},
		{
			Key:                 "club_chat",
			Name:                "club chat",
			ID:                  cfg.ClubChatID,
			Resource:            string(domain.ResourceChat),
			Severity:            "critical",
			RequiresManageRight: true,
		},
		{
			Key:                 "club_channel",
			Name:                "club channel",
			ID:                  cfg.ClubChannelID,
			Resource:            string(domain.ResourceChannel),
			Severity:            "critical",
			RequiresManageRight: true,
		},
	}
}

// CheckStartupHealth records bot presence and rights in every configured chat.
func CheckStartupHealth(
	ctx context.Context,
	db store.DBTX,
	client *Client,
	notifier *notify.Notifier,
	chats []HealthChat,
	ownerIDs []int64,
	botID int64,
	logger *slog.Logger,
) error {
	meta := store.NewMeta(db)
	alerts := store.NewAlerts(db)

	for _, chat := range chats {
		info, err := client.GetChat(ctx, chat.ID)
		if err != nil {
			reason := healthReason(err)
			if err := recordHealthFailure(
				ctx, meta, alerts, notifier, ownerIDs, chat, reason, logger,
			); err != nil {
				return err
			}

			continue
		}

		if !chatTypeMatchesResource(chat, info.Type) {
			if err := recordHealthFailure(
				ctx, meta, alerts, notifier, ownerIDs, chat, "wrong_type", logger,
			); err != nil {
				return err
			}

			continue
		}

		member, err := client.GetChatMember(ctx, chat.ID, botID)
		if err != nil {
			reason := healthReason(err)
			if err := recordHealthFailure(
				ctx, meta, alerts, notifier, ownerIDs, chat, reason, logger,
			); err != nil {
				return err
			}

			continue
		}

		if ok, reason := memberHasRequiredRights(chat, member); !ok {
			if err := recordHealthFailure(
				ctx, meta, alerts, notifier, ownerIDs, chat, reason, logger,
			); err != nil {
				return err
			}

			continue
		}

		if err := meta.SetHealth(ctx, chat.Key, healthOK); err != nil {
			return err
		}

		if err := alerts.ResolveOpenByTitle(
			ctx, alertKindBotRightsLost, alertTitle(chat),
		); err != nil {
			return err
		}

		logger.Info("chat health ok",
			slog.String("chat_key", chat.Key),
			slog.Int64("chat_id", chat.ID))
	}

	return nil
}

func recordHealthFailure(
	ctx context.Context,
	meta *store.Meta,
	alerts *store.Alerts,
	notifier *notify.Notifier,
	ownerIDs []int64,
	chat HealthChat,
	reason string,
	logger *slog.Logger,
) error {
	if err := meta.SetHealth(ctx, chat.Key, "fail:"+reason); err != nil {
		return err
	}

	_, created, err := alerts.CreateOpenIfMissing(ctx, store.AlertInput{
		Severity: chat.Severity,
		Kind:     alertKindBotRightsLost,
		Title:    alertTitle(chat),
		Detail: fmt.Sprintf(
			"chat_key=%s chat_id=%d reason=%s", chat.Key, chat.ID, reason),
	})
	if err != nil {
		return err
	}

	logger.Warn("chat health degraded",
		slog.String("chat_key", chat.Key),
		slog.Int64("chat_id", chat.ID),
		slog.String("reason", reason))

	if notifier != nil && created {
		_ = notifier.SendFormattedOwners(ctx, ownerIDs,
			messages.HealthFailure(chat.Name, chat.ID, reason))
	}

	return nil
}

type healthRepos struct {
	users  *store.Users
	meta   *store.Meta
	audit  *store.Audit
	alerts *store.Alerts
}

func handleMyChatMember(
	ctx context.Context,
	repos healthRepos,
	chats []HealthChat,
	ownerIDs []int64,
	update *models.ChatMemberUpdated,
	logger *slog.Logger,
) ([]OutboundMessage, error) {
	if update.Chat.Type == models.ChatTypePrivate {
		state := dmStateFromMember(update.NewChatMember)
		if err := repos.users.SetDMState(ctx, update.From.ID, state); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}

			if err := repos.users.Upsert(ctx, userFromTelegram(update.From, state)); err != nil {
				return nil, err
			}
		}

		return nil, nil
	}

	justJoined := memberIsPresent(update.NewChatMember) &&
		!memberIsPresent(update.OldChatMember)
	if justJoined {
		logBotAddedToChat(logger, update)
	}

	chat, ok := findHealthChat(chats, update.Chat.ID)
	if !ok {
		// Alert only on the actual join transition. Telegram emits a
		// separate my_chat_member for every later status change (e.g.
		// member→administrator, admin rights edits), and each one would
		// otherwise re-fire the "unknown chat" alert for a chat we have
		// already reported.
		if !justJoined {
			return nil, nil
		}

		return ownerEffects(ownerIDs,
			messages.UnknownChat(update.Chat.ID, string(update.Chat.Type), update.Chat.Title)), nil
	}

	oldOK, _ := memberHasRequiredRights(chat, &update.OldChatMember)

	newOK, reason := memberHasRequiredRights(chat, &update.NewChatMember)
	if newOK {
		if err := repos.meta.SetHealth(ctx, chat.Key, healthOK); err != nil {
			return nil, err
		}

		if !oldOK {
			if err := repos.audit.Append(ctx, store.AuditEntry{
				Kind:     "bot_rights_restored",
				Source:   chat.Source,
				Resource: chat.Resource,
				Detail:   fmt.Sprintf("chat_key=%s chat_id=%d", chat.Key, chat.ID),
			}); err != nil {
				return nil, err
			}

			if err := repos.alerts.ResolveOpenByTitle(
				ctx, alertKindBotRightsLost, alertTitle(chat),
			); err != nil {
				return nil, err
			}

			return ownerEffects(ownerIDs,
				messages.HealthRestored(chat.Name, chat.ID)), nil
		}

		return nil, nil
	}

	if err := repos.meta.SetHealth(ctx, chat.Key, "fail:"+reason); err != nil {
		return nil, err
	}

	if oldOK {
		if err := repos.audit.Append(ctx, store.AuditEntry{
			Kind:     "bot_rights_lost",
			Source:   chat.Source,
			Resource: chat.Resource,
			Detail: fmt.Sprintf(
				"chat_key=%s chat_id=%d reason=%s", chat.Key, chat.ID, reason),
		}); err != nil {
			return nil, err
		}

		if _, _, err := repos.alerts.CreateOpenIfMissing(ctx, store.AlertInput{
			Severity: chat.Severity,
			Kind:     alertKindBotRightsLost,
			Title:    alertTitle(chat),
			Detail: fmt.Sprintf(
				"chat_key=%s chat_id=%d reason=%s", chat.Key, chat.ID, reason),
		}); err != nil {
			return nil, err
		}

		return ownerEffects(ownerIDs,
			messages.HealthFailure(chat.Name, chat.ID, reason)), nil
	}

	return nil, nil
}

func chatTypeMatchesResource(chat HealthChat, got models.ChatType) bool {
	switch domain.Resource(chat.Resource) {
	case domain.ResourceChat:
		return got == models.ChatTypeGroup || got == models.ChatTypeSupergroup
	case domain.ResourceChannel:
		return got == models.ChatTypeChannel
	default:
		return true
	}
}

func memberHasRequiredRights(chat HealthChat, member *models.ChatMember) (bool, string) {
	if member == nil {
		return false, "not_member"
	}

	if chat.PostingOnly {
		return memberCanPost(member)
	}

	switch member.Type {
	case models.ChatMemberTypeOwner:
		return true, ""
	case models.ChatMemberTypeAdministrator:
		if member.Administrator == nil {
			return false, "not_admin"
		}

		if chat.RequiresManageRight &&
			(!member.Administrator.CanInviteUsers ||
				!member.Administrator.CanRestrictMembers) {
			return false, "missing_required_rights"
		}

		return true, ""
	case models.ChatMemberTypeLeft, models.ChatMemberTypeBanned:
		return false, "not_member"
	default:
		return false, "not_admin"
	}
}

// memberCanPost reports whether the bot can post to a posting-only feed chat.
// Plain membership is enough unless the member is restricted from sending;
// administrator status is not required.
func memberCanPost(member *models.ChatMember) (bool, string) {
	switch member.Type {
	case models.ChatMemberTypeOwner,
		models.ChatMemberTypeAdministrator,
		models.ChatMemberTypeMember:
		return true, ""
	case models.ChatMemberTypeRestricted:
		if member.Restricted != nil && member.Restricted.CanSendMessages {
			return true, ""
		}

		return false, "cannot_post"
	default:
		return false, "not_member"
	}
}

func logBotAddedToChat(logger *slog.Logger, update *models.ChatMemberUpdated) {
	logger.Info("bot added to chat",
		slog.Int64("chat_id", update.Chat.ID),
		slog.String("chat_type", string(update.Chat.Type)),
		slog.String("chat_title", update.Chat.Title),
		slog.String("chat_username", update.Chat.Username),
		slog.Bool("chat_is_forum", update.Chat.IsForum),
		slog.Bool("chat_is_direct_messages", update.Chat.IsDirectMessages),
		slog.Int64("actor_tg_id", update.From.ID),
		slog.String("actor_username", update.From.Username),
		slog.String("actor_first_name", update.From.FirstName),
		slog.String("actor_last_name", update.From.LastName),
		slog.String("old_member_status", string(update.OldChatMember.Type)),
		slog.String("new_member_status", string(update.NewChatMember.Type)))
}

func memberIsPresent(member models.ChatMember) bool {
	switch member.Type {
	case models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator,
		models.ChatMemberTypeMember:
		return true
	default:
		return false
	}
}

func dmStateFromMember(member models.ChatMember) domain.DMState {
	switch member.Type {
	case models.ChatMemberTypeLeft, models.ChatMemberTypeBanned:
		return domain.DMBlocked
	default:
		return domain.DMOpen
	}
}

func userFromTelegram(user models.User, state domain.DMState) domain.User {
	return domain.User{
		TGID:         user.ID,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		LanguageCode: user.LanguageCode,
		IsBot:        user.IsBot,
		DMState:      state,
	}
}

func findHealthChat(chats []HealthChat, id int64) (HealthChat, bool) {
	for _, chat := range chats {
		if chat.ID == id {
			return chat, true
		}
	}

	return HealthChat{}, false
}

func ownerEffects(ownerIDs []int64, text string) []OutboundMessage {
	effects := make([]OutboundMessage, 0, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		effects = append(effects, OutboundMessage{
			Kind:      OutboundDM,
			TGID:      ownerID,
			Text:      text,
			ParseMode: messages.ParseModeHTML,
		})
	}

	return effects
}

func healthReason(err error) string {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return "telegram_api"
	}

	switch apiErr.Category {
	case ErrorCategoryPermanentRights, ErrorCategoryForbidden:
		return "rights"
	case ErrorCategoryUnauthorized:
		return "unauthorized"
	case ErrorCategoryNotFound:
		return "not_found"
	case ErrorCategoryRateLimited:
		return "rate_limited"
	default:
		return "telegram_api"
	}
}

func alertTitle(chat HealthChat) string {
	return "bot rights lost: " + chat.Key
}
