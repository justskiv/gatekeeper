package store

import (
	"context"
	"testing"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
)

func TestInvitesActiveSharedLookupReturnsOneLink(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	invites := NewInvites(db)

	hash := "hash-" + gofakeit.UUID()

	saved, err := invites.SaveCreated(ctx, InviteLinkInput{
		Resource:           domain.ResourceChat,
		Mode:               domain.InviteSharedJoinRequest,
		InviteLink:         "https://t.me/+" + gofakeit.LetterN(10),
		InviteLinkHash:     hash,
		TelegramName:       gofakeit.Username(),
		CreatesJoinRequest: true,
	})
	require.NoError(t, err, "save shared invite")

	got, ok, err := invites.FindActiveShared(
		ctx, domain.ResourceChat, domain.InviteSharedJoinRequest)
	require.NoError(t, err, "FindActiveShared")
	require.True(t, ok, "an active shared link must be found")
	assert.Equal(t, saved.ID, got.ID)
	assert.True(t, got.CreatesJoinRequest)

	byHash, ok, err := invites.FindActiveByHash(ctx, domain.ResourceChat, hash)
	require.NoError(t, err, "FindActiveByHash")
	require.True(t, ok, "an active link must be found by hash")
	assert.Equal(t, saved.ID, byHash.ID)
}

func TestInvitesExpiredPersonalLinkFreesActiveSlot(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	expiresAt := time.Now().Add(time.Hour)

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	invites := NewInvites(db)

	personal := func(suffix string) InviteLinkInput {
		return InviteLinkInput{
			TGID:               &tgID,
			Resource:           domain.ResourceChat,
			Mode:               domain.InvitePersonalJoinRequest,
			InviteLink:         "https://t.me/+" + suffix,
			InviteLinkHash:     "hash-" + suffix,
			TelegramName:       "gk-" + suffix,
			Nonce:              suffix,
			CreatesJoinRequest: true,
			ExpiresAt:          &expiresAt,
		}
	}

	first, err := invites.SaveCreated(ctx, personal(gofakeit.LetterN(8)))
	require.NoError(t, err, "save first invite")

	// A second active personal link for the same slot is rejected by
	// the partial unique index.
	_, err = invites.SaveCreated(ctx, personal(gofakeit.LetterN(8)))
	require.Error(t, err, "second active personal invite must violate the unique index")

	require.NoError(t,
		invites.MarkStatus(ctx, first.ID, domain.InviteExpired, nil, ""),
		"expire first invite")

	second, err := invites.SaveCreated(ctx, personal(gofakeit.LetterN(8)))
	require.NoError(t, err, "save second invite after expiration")

	active, ok, err := invites.FindActivePersonal(
		ctx, tgID, domain.ResourceChat, domain.InvitePersonalJoinRequest)
	require.NoError(t, err, "FindActivePersonal")
	require.True(t, ok, "the freed slot must hold the second link")
	assert.Equal(t, second.ID, active.ID)
}

func TestInvitesMarkFailedStoresLastError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	invites := NewInvites(db)

	link, err := invites.SaveCreated(ctx, InviteLinkInput{
		Resource:           domain.ResourceChannel,
		Mode:               domain.InviteSharedJoinRequest,
		InviteLink:         "https://t.me/+" + gofakeit.LetterN(10),
		InviteLinkHash:     "hash-" + gofakeit.UUID(),
		TelegramName:       gofakeit.Username(),
		CreatesJoinRequest: true,
	})
	require.NoError(t, err, "save invite")

	require.NoError(t,
		invites.MarkStatus(ctx, link.ID, domain.InviteFailed, nil, "not enough rights"),
		"mark failed")

	got, err := invites.GetByID(ctx, link.ID)
	require.NoError(t, err, "get invite")
	assert.Equal(t, domain.InviteFailed, got.Status)
	assert.Equal(t, "not enough rights", got.LastError)
}

func TestInvitesMarkUsedByOtherStoresAttemptedBy(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	attemptedBy := random.TGID()

	users := NewUsers(db)
	for _, tgID := range []int64{ownerID, attemptedBy} {
		require.NoError(t, users.Upsert(ctx, domain.User{TGID: tgID}),
			"upsert user %d", tgID)
	}

	invites := NewInvites(db)

	link, err := invites.SaveCreated(ctx, InviteLinkInput{
		TGID:               &ownerID,
		Resource:           domain.ResourceChat,
		Mode:               domain.InvitePersonalJoinRequest,
		InviteLink:         "https://t.me/+" + gofakeit.LetterN(10),
		InviteLinkHash:     "hash-" + gofakeit.UUID(),
		TelegramName:       gofakeit.Username(),
		CreatesJoinRequest: true,
	})
	require.NoError(t, err, "save invite")

	require.NoError(t,
		invites.MarkStatus(ctx, link.ID, domain.InviteUsedByOther, &attemptedBy, ""),
		"mark used_by_other")

	got, err := invites.GetByID(ctx, link.ID)
	require.NoError(t, err, "get invite")
	assert.Equal(t, domain.InviteUsedByOther, got.Status)
	require.NotNil(t, got.AttemptedBy, "used_by_other must record the attempting user")
	assert.Equal(t, attemptedBy, *got.AttemptedBy)
}
