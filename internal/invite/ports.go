// Package invite owns Telegram invite-link lifecycle rules.
package invite

import (
	"context"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
)

// LinkManager is the consumer-side Telegram surface needed by Service.
type LinkManager interface {
	CreateChatInviteLink(
		ctx context.Context,
		params telegram.CreateChatInviteLinkParams,
	) (*models.ChatInviteLink, error)
	RevokeChatInviteLink(
		ctx context.Context,
		chatID int64,
		inviteLink string,
	) (*models.ChatInviteLink, error)
}

// Store is the invite_links repository surface needed by Service.
type Store interface {
	FindActiveShared(
		ctx context.Context,
		resource domain.Resource,
		mode domain.InviteMode,
	) (domain.InviteLink, bool, error)
	FindActivePersonal(
		ctx context.Context,
		tgID int64,
		resource domain.Resource,
		mode domain.InviteMode,
	) (domain.InviteLink, bool, error)
	SaveCreated(ctx context.Context, input store.InviteLinkInput) (domain.InviteLink, error)
	MarkStatus(
		ctx context.Context,
		id int64,
		status domain.InviteStatus,
		attemptedBy *int64,
		lastError string,
	) error
}
