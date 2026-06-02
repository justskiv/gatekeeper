package telegram

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
)

type resourceMemberClient interface {
	GetChatMember(ctx context.Context, chatID, userID int64) (*models.ChatMember, error)
}

// ResourceMemberChecker maps managed resources to Telegram chat membership.
type ResourceMemberChecker struct {
	client  resourceMemberClient
	chatIDs map[domain.Resource]int64
}

// NewClubMemberChecker maps managed club resources to Telegram chat IDs for
// engine protection checks.
func NewClubMemberChecker(
	client resourceMemberClient,
	clubChatID int64,
	clubChannelID int64,
) *ResourceMemberChecker {
	if client == nil {
		return nil
	}

	return &ResourceMemberChecker{
		client: client,
		chatIDs: map[domain.Resource]int64{
			domain.ResourceChat:    clubChatID,
			domain.ResourceChannel: clubChannelID,
		},
	}
}

func (c *ResourceMemberChecker) GetChatMember(
	ctx context.Context,
	resource domain.Resource,
	tgID int64,
) (*models.ChatMember, error) {
	if c == nil || c.client == nil {
		return nil, nil
	}

	chatID, ok := c.chatIDs[resource]
	if !ok || chatID == 0 {
		return nil, fmt.Errorf("no chat id configured for resource %q", resource)
	}

	return c.client.GetChatMember(ctx, chatID, tgID)
}
