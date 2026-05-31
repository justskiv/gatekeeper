package store

import (
	"context"
	"testing"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

func TestInvitesActiveSharedLookupReturnsOneLink(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	invites := NewInvites(db)

	saved, err := invites.SaveCreated(ctx, InviteLinkInput{
		Resource:           domain.ResourceChat,
		Mode:               domain.InviteSharedJoinRequest,
		InviteLink:         "https://t.me/+shared",
		InviteLinkHash:     "hash-shared",
		TelegramName:       "gk-shared-chat",
		CreatesJoinRequest: true,
	})
	if err != nil {
		t.Fatalf("save shared invite: %v", err)
	}

	got, ok, err := invites.FindActiveShared(
		ctx, domain.ResourceChat, domain.InviteSharedJoinRequest)
	if err != nil {
		t.Fatalf("FindActiveShared: %v", err)
	}

	if !ok || got.ID != saved.ID || !got.CreatesJoinRequest {
		t.Fatalf("shared link = (%+v, %v), want saved active link", got, ok)
	}
}

func TestInvitesExpiredPersonalLinkFreesActiveSlot(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(2001)
	expiresAt := time.Now().Add(time.Hour)

	if err := NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	invites := NewInvites(db)

	first, err := invites.SaveCreated(ctx, InviteLinkInput{
		TGID:               &tgID,
		Resource:           domain.ResourceChat,
		Mode:               domain.InvitePersonalJoinRequest,
		InviteLink:         "https://t.me/+personal-1",
		InviteLinkHash:     "hash-personal-1",
		TelegramName:       "gk-personal-1",
		Nonce:              "one",
		CreatesJoinRequest: true,
		ExpiresAt:          &expiresAt,
	})
	if err != nil {
		t.Fatalf("save first invite: %v", err)
	}

	if _, err := invites.SaveCreated(ctx, InviteLinkInput{
		TGID:               &tgID,
		Resource:           domain.ResourceChat,
		Mode:               domain.InvitePersonalJoinRequest,
		InviteLink:         "https://t.me/+personal-2",
		InviteLinkHash:     "hash-personal-2",
		TelegramName:       "gk-personal-2",
		Nonce:              "two",
		CreatesJoinRequest: true,
		ExpiresAt:          &expiresAt,
	}); err == nil {
		t.Fatal("second active personal invite succeeded, want unique-index error")
	}

	if err := invites.MarkStatus(
		ctx, first.ID, domain.InviteExpired, nil, "",
	); err != nil {
		t.Fatalf("expire first invite: %v", err)
	}

	second, err := invites.SaveCreated(ctx, InviteLinkInput{
		TGID:               &tgID,
		Resource:           domain.ResourceChat,
		Mode:               domain.InvitePersonalJoinRequest,
		InviteLink:         "https://t.me/+personal-2",
		InviteLinkHash:     "hash-personal-2",
		TelegramName:       "gk-personal-2",
		Nonce:              "two",
		CreatesJoinRequest: true,
		ExpiresAt:          &expiresAt,
	})
	if err != nil {
		t.Fatalf("save second invite after expiration: %v", err)
	}

	active, ok, err := invites.FindActivePersonal(
		ctx, tgID, domain.ResourceChat, domain.InvitePersonalJoinRequest)
	if err != nil {
		t.Fatalf("FindActivePersonal: %v", err)
	}

	if !ok || active.ID != second.ID {
		t.Fatalf("active personal = (%+v, %v), want second link", active, ok)
	}
}

func TestInvitesMarkFailedStoresLastError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	invites := NewInvites(db)

	link, err := invites.SaveCreated(ctx, InviteLinkInput{
		Resource:           domain.ResourceChannel,
		Mode:               domain.InviteSharedJoinRequest,
		InviteLink:         "https://t.me/+failed",
		InviteLinkHash:     "hash-failed",
		TelegramName:       "gk-shared-channel",
		CreatesJoinRequest: true,
	})
	if err != nil {
		t.Fatalf("save invite: %v", err)
	}

	if err := invites.MarkStatus(
		ctx, link.ID, domain.InviteFailed, nil, "not enough rights",
	); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	got, err := invites.GetByID(ctx, link.ID)
	if err != nil {
		t.Fatalf("get invite: %v", err)
	}

	if got.Status != domain.InviteFailed || got.LastError != "not enough rights" {
		t.Fatalf("failed invite = %+v, want status failed with last_error", got)
	}
}
