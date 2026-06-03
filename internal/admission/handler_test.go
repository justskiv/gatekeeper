package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	invitepkg "github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

func TestAccessRequestActiveSharedCreatesPendingGrantsAndDM(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	chatLink := "https://t.me/+" + gofakeit.LetterN(10)
	channelLink := "https://t.me/+" + gofakeit.LetterN(10)

	seedSharedInvite(t, db, domain.ResourceChat, chatLink)
	seedSharedInvite(t, db, domain.ResourceChannel, channelLink)

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	for range 2 {
		require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
			User:     domain.User{TGID: tgID, FirstName: gofakeit.FirstName()},
			Snapshot: activeSnapshot(tgID),
			Trigger:  "start",
		}), "HandleAccessRequest")
	}

	assert.Equal(t, 2, countRows(t, db, `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = ? AND state = 'pending'`, tgID), "pending grants")

	assert.Equal(t, 2, countActions(t, db, domain.ActionSendDM), "send_dm actions")

	payload := firstDMPayload(t, db)
	assert.Contains(t, payload.Text, chatLink, "dm must carry shared chat link")
	assert.Contains(t, payload.Text, channelLink, "dm must carry shared channel link")
	assert.Equal(t, messages.ParseModeHTML, payload.ParseMode)

	assert.Equal(t, 0, countActions(t, db, domain.ActionSendInvite),
		"shared mode must not enqueue send_invite")
}

func TestAccessRequestInactiveDoesNotCreateGrants(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: inactiveSnapshot(tgID),
	}), "HandleAccessRequest")

	assert.Equal(t, 0, countRows(t, db,
		`SELECT count(*) FROM access_grants WHERE tg_id = ?`, tgID), "grants")

	payload := firstDMPayload(t, db)
	assert.Equal(t, messages.NoSub(), payload.Text, "dm text")
}

func TestAccessRequestUnknownUsesFreshLocalFallback(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	now := time.Unix(1_700_000_000, 0)

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+"+gofakeit.LetterN(10))
	seedSharedInvite(t, db, domain.ResourceChannel, "https://t.me/+"+gofakeit.LetterN(10))

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:          tgID,
		Platform:      domain.PlatformBoosty,
		StartedAt:     now.Add(-time.Hour),
		LastCheckedAt: &now,
		LastSignal:    "on_demand",
	})
	require.NoError(t, err, "upsert active subscription")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest,
		WithClock(func() time.Time { return now }))
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: unknownSnapshot(tgID),
	}), "HandleAccessRequest")

	assert.Equal(t, 2, countRows(t, db, `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = ? AND state = 'pending'`, tgID), "pending grants from fallback")
}

func TestAccessRequestUnknownFallbackUsesFreshestSubscriptionSignal(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	now := time.Unix(1_700_000_000, 0)
	lastEventAt := now.Add(-30 * time.Minute)
	lastCheckedAt := now.Add(-2 * time.Hour)

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+"+gofakeit.LetterN(10))
	seedSharedInvite(t, db, domain.ResourceChannel, "https://t.me/+"+gofakeit.LetterN(10))

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:          tgID,
		Platform:      domain.PlatformBoosty,
		StartedAt:     now.Add(-24 * time.Hour),
		LastEventAt:   &lastEventAt,
		LastCheckedAt: &lastCheckedAt,
		LastSignal:    "event",
	})
	require.NoError(t, err, "upsert active subscription")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest,
		WithClock(func() time.Time { return now }))
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: unknownSnapshot(tgID),
	}), "HandleAccessRequest")

	assert.Equal(t, 2, countRows(t, db, `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = ? AND state = 'pending'`, tgID), "pending grants from fresh event")
}

func TestAccessRequestBannedSkipsGrantsAndReturnsBanned(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   tgID,
		Banned: true,
	}), "upsert banned user")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}), "HandleAccessRequest")

	assert.Equal(t, 0, countRows(t, db,
		`SELECT count(*) FROM access_grants WHERE tg_id = ?`, tgID), "grants")

	payload := firstDMPayload(t, db)
	assert.Equal(t, messages.Banned(), payload.Text, "dm text")
}

func TestAccessRequestAlreadyInReturnsAlreadyIn(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	grants := store.NewGrants(db)
	require.NoError(t, grants.MarkJoined(ctx, tgID, domain.ResourceChat, "bot"),
		"mark chat joined")
	require.NoError(t, grants.MarkJoined(ctx, tgID, domain.ResourceChannel, "bot"),
		"mark channel joined")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}), "HandleAccessRequest")

	payload := firstDMPayload(t, db)
	assert.Equal(t, messages.AlreadyIn(), payload.Text, "dm text")

	assert.Equal(t, 0, countActions(t, db, domain.ActionSendInvite),
		"send_invite actions")
}

func TestAccessRequestDirectEnqueuesSendInvites(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	handler := newTestHandler(db, domain.InviteDirect)
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}), "HandleAccessRequest")

	assert.Equal(t, 2, countActions(t, db, domain.ActionSendInvite),
		"send_invite actions")

	assert.Equal(t, 2, countRows(t, db, `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = ? AND state = 'pending'`, tgID), "pending grants")

	payload := firstDMPayload(t, db)
	assert.Equal(t, messages.ActiveDirect(), payload.Text, "dm text")
}

func TestAccessRequestPersonalLeftGrantCreatesNewSendInviteCycle(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	handler := newTestHandler(db, domain.InvitePersonalJoinRequest)
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}), "HandleAccessRequest first")

	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}), "HandleAccessRequest repeated pending")

	assert.Equal(t, 2, countActions(t, db, domain.ActionSendInvite),
		"send_invite actions after repeat")

	grants := store.NewGrants(db)
	require.NoError(t, grants.MarkJoined(ctx, tgID, domain.ResourceChat, "bot"),
		"mark joined")

	_, err := grants.MarkLeftUnlessRevoked(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "mark left")

	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}), "HandleAccessRequest after left")

	assert.Equal(t, 3, countActions(t, db, domain.ActionSendInvite),
		"send_invite actions after left")
}

func TestAccessRequestMissingSharedInviteCommitsAlertAndDM(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}), "HandleAccessRequest")

	assert.Equal(t, 1, countRows(t, db,
		`SELECT count(*) FROM admin_alerts WHERE kind = 'invite_link_missing'`),
		"invite_link_missing alerts")

	assert.Equal(t, 0, countRows(t, db,
		`SELECT count(*) FROM access_grants WHERE tg_id = ?`, tgID), "grants")

	payload := firstDMPayload(t, db)
	assert.Equal(t, messages.TryLater(), payload.Text, "dm text")
	assert.True(t, payload.RetryButton, "retry-later dm must carry a button")
}

func TestAccessRequestBannedReturnsBanned(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   tgID,
		Banned: true,
	}), "upsert banned user")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}), "HandleAccessRequest")

	payload := firstDMPayload(t, db)
	assert.Equal(t, messages.Banned(), payload.Text, "dm text")
}

func TestJoinRequestActiveApproveKeyUsesRequestDate(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	userChatID := random.TGID()
	rawLink := "https://t.me/+" + gofakeit.LetterN(10)

	seedSharedInvite(t, db, domain.ResourceChat, rawLink)

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	for _, date := range []time.Time{
		time.Unix(1_700_000_000, 0),
		time.Unix(1_700_000_100, 0),
	} {
		require.NoError(t, handler.HandleJoinRequest(ctx, JoinRequest{
			User:        domain.User{TGID: tgID, FirstName: gofakeit.FirstName()},
			UserChatID:  userChatID,
			Resource:    domain.ResourceChat,
			InviteLink:  rawLink,
			RequestDate: date,
			Snapshot:    activeSnapshot(tgID),
		}), "HandleJoinRequest %v", date)
	}

	assert.Equal(t, 2, countActions(t, db, domain.ActionApproveJoin),
		"approve_join actions, one per request date")

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "get grant")
	assert.Equal(t, domain.GrantJoined, grant.State)
	assert.Equal(t, "bot", grant.AdmittedBy)

	payload := firstDMPayload(t, db)
	assert.Equal(t, userChatID, payload.ChatID, "dm must target user_chat_id")
	assert.Equal(t, messages.Granted(), payload.Text, "dm text")
}

func TestJoinRequestMissingInviteLinkSharedFallbackApproves(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+"+gofakeit.LetterN(10))

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleJoinRequest(ctx, JoinRequest{
		User:        domain.User{TGID: tgID},
		UserChatID:  random.TGID(),
		Resource:    domain.ResourceChat,
		RequestDate: time.Unix(1_700_000_000, 0),
		Snapshot:    activeSnapshot(tgID),
	}), "HandleJoinRequest")

	assert.Equal(t, 1, countActions(t, db, domain.ActionApproveJoin),
		"approve_join actions")

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "get grant")
	assert.Equal(t, domain.GrantJoined, grant.State)
}

func TestJoinRequestPersonalMisuseDeclinesAndMarksInvite(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	requesterID := random.TGID()
	rawLink := "https://t.me/+" + gofakeit.LetterN(10)

	for _, tgID := range []int64{ownerID, requesterID} {
		require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
			"upsert user %d", tgID)
	}

	link := seedPersonalInvite(t, db, ownerID, domain.ResourceChat,
		domain.InvitePersonalJoinRequest, rawLink)

	handler := newTestHandler(db, domain.InvitePersonalJoinRequest)
	require.NoError(t, handler.HandleJoinRequest(ctx, JoinRequest{
		User:        domain.User{TGID: requesterID},
		UserChatID:  random.TGID(),
		Resource:    domain.ResourceChat,
		InviteLink:  rawLink,
		RequestDate: time.Unix(1_700_000_000, 0),
		Snapshot:    activeSnapshot(requesterID),
	}), "HandleJoinRequest")

	assert.Equal(t, 1, countActions(t, db, domain.ActionDeclineJoin),
		"decline_join actions")

	got, err := store.NewInvites(db).GetByID(ctx, link.ID)
	require.NoError(t, err, "get invite")
	assert.Equal(t, domain.InviteUsedByOther, got.Status)
	require.NotNil(t, got.AttemptedBy, "misuse must record the attempting user")
	assert.Equal(t, requesterID, *got.AttemptedBy)
}

func TestJoinRequestBannedDoesNotBurnPersonalInvite(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	requesterID := random.TGID()
	rawLink := "https://t.me/+" + gofakeit.LetterN(10)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: ownerID}),
		"upsert owner")

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   requesterID,
		Banned: true,
	}), "upsert banned requester")

	link := seedPersonalInvite(t, db, ownerID, domain.ResourceChat,
		domain.InvitePersonalJoinRequest, rawLink)

	handler := newTestHandler(db, domain.InvitePersonalJoinRequest)
	require.NoError(t, handler.HandleJoinRequest(ctx, JoinRequest{
		User:        domain.User{TGID: requesterID},
		UserChatID:  random.TGID(),
		Resource:    domain.ResourceChat,
		InviteLink:  rawLink,
		RequestDate: time.Unix(1_700_000_000, 0),
		Snapshot:    activeSnapshot(requesterID),
	}), "HandleJoinRequest")

	got, err := store.NewInvites(db).GetByID(ctx, link.ID)
	require.NoError(t, err, "get invite")
	assert.NotEqual(t, domain.InviteUsedByOther, got.Status,
		"banned requester must not burn the invite")
	assert.Nil(t, got.AttemptedBy, "banned requester must not be recorded")

	payload := firstDMPayload(t, db)
	assert.Equal(t, messages.Banned(), payload.Text, "dm text")
}

func TestJoinRequestInactiveAndUnknownDecline(t *testing.T) {
	tests := []struct {
		name     string
		snapshot func(tgID int64) *engine.Snapshot
		wantText string
	}{
		{
			name:     "inactive",
			snapshot: inactiveSnapshot,
			wantText: messages.NoSub(),
		},
		{
			name:     "unknown",
			snapshot: unknownSnapshot,
			wantText: messages.TryLater(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewDB(t)
			ctx := context.Background()
			tgID := random.TGID()
			rawLink := "https://t.me/+" + gofakeit.LetterN(10)

			seedSharedInvite(t, db, domain.ResourceChat, rawLink)

			handler := newTestHandler(db, domain.InviteSharedJoinRequest)
			require.NoError(t, handler.HandleJoinRequest(ctx, JoinRequest{
				User:        domain.User{TGID: tgID},
				UserChatID:  random.TGID(),
				Resource:    domain.ResourceChat,
				InviteLink:  rawLink,
				RequestDate: time.Unix(1_700_000_000, 0),
				Snapshot:    tt.snapshot(tgID),
			}), "HandleJoinRequest")

			assert.Equal(t, 1, countActions(t, db, domain.ActionDeclineJoin),
				"decline_join actions")

			payload := firstDMPayload(t, db)
			assert.Equal(t, tt.wantText, payload.Text, "dm text")
		})
	}
}

func TestMembershipExternalJoinAlertsWithoutKick(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   true,
	}), "HandleMembershipUpdate")

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "get grant")
	assert.Equal(t, domain.GrantJoined, grant.State)
	assert.Equal(t, "external", grant.AdmittedBy)

	assert.Equal(t, 0, countActions(t, db, domain.ActionSoftKick), "soft_kick actions")

	assert.Equal(t, 1, countRows(t, db,
		`SELECT count(*) FROM admin_alerts WHERE kind = 'external_join'`),
		"external_join alerts")
}

func TestMembershipBannedExternalJoinHardBansWithoutGrant(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   tgID,
		Banned: true,
	}), "upsert banned user")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:      domain.User{TGID: tgID},
		Resource:  domain.ResourceChat,
		Joined:    true,
		EventDate: time.Unix(1_700_000_000, 0),
	}), "HandleMembershipUpdate")

	_, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	require.ErrorIs(t, err, store.ErrNotFound,
		"banned external join must not create a grant")

	assert.Equal(t, 1, countActions(t, db, domain.ActionHardBan), "hard_ban actions")
}

func TestMembershipDirectInactiveSoftKicks(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	rawLink := "https://t.me/+" + gofakeit.LetterN(10)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	seedPersonalInvite(t, db, tgID, domain.ResourceChat, domain.InviteDirect, rawLink)

	handler := newTestHandler(db, domain.InviteDirect)
	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:       domain.User{TGID: tgID},
		Resource:   domain.ResourceChat,
		Joined:     true,
		InviteLink: rawLink,
		Snapshot:   inactiveSnapshot(tgID),
	}), "HandleMembershipUpdate")

	assert.Equal(t, 1, countActions(t, db, domain.ActionSoftKick), "soft_kick actions")
}

func TestMembershipDirectPendingGrantInactiveSoftKicks(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := store.NewGrants(db).MarkPending(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "mark pending grant")

	handler := newTestHandler(db, domain.InviteDirect)
	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   true,
		Snapshot: inactiveSnapshot(tgID),
	}), "HandleMembershipUpdate")

	assert.Equal(t, 1, countActions(t, db, domain.ActionSoftKick), "soft_kick actions")
}

func TestMembershipDirectInactiveSoftKickUsesEventCycle(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	handler := newTestHandler(db, domain.InviteDirect)
	for _, eventDate := range []time.Time{
		time.Unix(1_700_000_000, 0),
		time.Unix(1_700_000_000, 0),
		time.Unix(1_700_000_060, 0),
	} {
		_, err := store.NewGrants(db).MarkPending(ctx, tgID, domain.ResourceChat)
		require.NoError(t, err, "mark pending grant")

		require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
			User:      domain.User{TGID: tgID},
			Resource:  domain.ResourceChat,
			Joined:    true,
			EventDate: eventDate,
			Snapshot:  inactiveSnapshot(tgID),
		}), "HandleMembershipUpdate %v", eventDate)
	}

	assert.Equal(t, 2, countActions(t, db, domain.ActionSoftKick), "soft_kick actions")
}

func TestMembershipLeftPreservesRevokedGrant(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	now := time.Unix(1_700_000_000, 0)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:      tgID,
		Resource:  domain.ResourceChat,
		State:     domain.GrantRevoked,
		RevokedAt: &now,
	}), "upsert revoked grant")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   false,
	}), "HandleMembershipUpdate")

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "get grant")
	assert.Equal(t, domain.GrantRevoked, grant.State)
}

func TestMembershipJoinPreservesRevokedGrant(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	now := time.Unix(1_700_000_000, 0)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:          tgID,
		Resource:      domain.ResourceChat,
		State:         domain.GrantRevoked,
		AdmittedBy:    "bot",
		RevokedAt:     &now,
		RevokedReason: "manual",
	}), "upsert revoked grant")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   true,
	}), "HandleMembershipUpdate")

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	require.NoError(t, err, "get grant")
	assert.Equal(t, domain.GrantRevoked, grant.State)
	require.NotNil(t, grant.RevokedAt, "revoked_at must be preserved")
	assert.Equal(t, "manual", grant.RevokedReason)
}

func newTestHandler(
	db *sql.DB,
	mode domain.InviteMode,
	opts ...Option,
) *Handler {
	return New(Deps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Invites:       store.NewInvites(db),
		Outbox:        store.NewOutbox(db),
		Audit:         store.NewAudit(db),
		Alerts:        store.NewAlerts(db),
		Whitelist:     store.NewWhitelist(db),
		Revocations:   store.NewRevocations(db),
		StatusEngine:  engine.New(nil),
	}, Config{
		InviteMode:    mode,
		ClubChatID:    -1001,
		ClubChannelID: -1002,
		Resources: []ResourceConfig{
			{Resource: domain.ResourceChat, ChatID: -1001},
			{Resource: domain.ResourceChannel, ChatID: -1002},
		},
	}, opts...)
}

func seedSharedInvite(
	t *testing.T,
	db *sql.DB,
	resource domain.Resource,
	rawLink string,
) domain.InviteLink {
	t.Helper()

	link, err := store.NewInvites(db).SaveCreated(context.Background(),
		store.InviteLinkInput{
			Resource:           resource,
			Mode:               domain.InviteSharedJoinRequest,
			InviteLink:         rawLink,
			InviteLinkHash:     invitepkg.HashInviteLink(rawLink),
			TelegramName:       gofakeit.Username(),
			CreatesJoinRequest: true,
		})
	require.NoError(t, err, "save shared invite")

	return link
}

func seedPersonalInvite(
	t *testing.T,
	db *sql.DB,
	tgID int64,
	resource domain.Resource,
	mode domain.InviteMode,
	rawLink string,
) domain.InviteLink {
	t.Helper()

	link, err := store.NewInvites(db).SaveCreated(context.Background(),
		store.InviteLinkInput{
			TGID:               &tgID,
			Resource:           resource,
			Mode:               mode,
			InviteLink:         rawLink,
			InviteLinkHash:     invitepkg.HashInviteLink(rawLink),
			TelegramName:       gofakeit.Username(),
			CreatesJoinRequest: mode != domain.InviteDirect,
		})
	require.NoError(t, err, "save personal invite")

	return link
}

func activeSnapshot(tgID int64) *engine.Snapshot {
	return snapshot(tgID, domain.StatusActive, domain.VerdictActive)
}

func inactiveSnapshot(tgID int64) *engine.Snapshot {
	return snapshot(tgID, domain.StatusInactive, domain.VerdictInactive)
}

func unknownSnapshot(tgID int64) *engine.Snapshot {
	return snapshot(tgID, domain.StatusUnknown, domain.VerdictUnknown)
}

func snapshot(
	tgID int64,
	status domain.EffectiveStatus,
	verdict domain.Verdict,
) *engine.Snapshot {
	sourceVerdict := domain.SourceVerdict{
		Source:  domain.PlatformBoosty,
		Verdict: verdict,
		Detail:  "test",
	}

	return &engine.Snapshot{
		TGID:     tgID,
		Verdicts: []domain.SourceVerdict{sourceVerdict},
		Decision: domain.AccessDecision{
			TGID:    tgID,
			Status:  status,
			Allowed: status == domain.StatusActive,
			Reasons: []domain.AccessReason{
				domain.AccessReason(sourceVerdict),
			},
		},
	}
}

func countActions(t *testing.T, db *sql.DB, action domain.ActionType) int {
	t.Helper()

	return countRows(t, db,
		`SELECT count(*) FROM access_actions WHERE action_type = ?`, string(action))
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t,
		db.QueryRowContext(context.Background(), query, args...).Scan(&n),
		"count rows")

	return n
}

func firstDMPayload(t *testing.T, db *sql.DB) sendDMPayload {
	t.Helper()

	var raw string
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT payload_json
		FROM access_actions
		WHERE action_type = ?
		ORDER BY id
		LIMIT 1`, string(domain.ActionSendDM)).Scan(&raw), "read action payload")

	var payload sendDMPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload), "decode action payload")

	return payload
}
