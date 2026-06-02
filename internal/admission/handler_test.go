package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	invitepkg "github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

func TestAccessRequestActiveSharedCreatesPendingGrantsAndDM(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5001)

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+chat")
	seedSharedInvite(t, db, domain.ResourceChannel, "https://t.me/+channel")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	for range 2 {
		if err := handler.HandleAccessRequest(ctx, AccessRequest{
			User:     domain.User{TGID: tgID, FirstName: "Active"},
			Snapshot: activeSnapshot(tgID),
			Trigger:  "start",
		}); err != nil {
			t.Fatalf("HandleAccessRequest: %v", err)
		}
	}

	if got := countRows(t, db, `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = ? AND state = 'pending'`, tgID); got != 2 {
		t.Fatalf("pending grants = %d, want 2", got)
	}

	if got := countActions(t, db, domain.ActionSendDM); got != 1 {
		t.Fatalf("send_dm actions = %d, want 1", got)
	}

	payload := firstDMPayload(t, db)
	if !strings.Contains(payload.Text, "https://t.me/+chat") ||
		!strings.Contains(payload.Text, "https://t.me/+channel") {
		t.Fatalf("dm payload = %+v, want shared links", payload)
	}

	if got := countActions(t, db, domain.ActionSendInvite); got != 0 {
		t.Fatalf("send_invite actions = %d, want 0 in shared mode", got)
	}
}

func TestAccessRequestInactiveDoesNotCreateGrants(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5002)

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: inactiveSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	if got := countRows(t, db,
		`SELECT count(*) FROM access_grants WHERE tg_id = ?`, tgID); got != 0 {
		t.Fatalf("grants = %d, want none", got)
	}

	payload := firstDMPayload(t, db)
	if payload.Text != messages.MsgNoSub {
		t.Fatalf("dm text = %q, want no-sub", payload.Text)
	}
}

func TestAccessRequestUnknownUsesFreshLocalFallback(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5003)
	now := time.Unix(1_700_000_000, 0)

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+chat-fallback")
	seedSharedInvite(t, db, domain.ResourceChannel, "https://t.me/+channel-fallback")

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:          tgID,
		Platform:      domain.PlatformBoosty,
		StartedAt:     now.Add(-time.Hour),
		LastCheckedAt: &now,
		LastSignal:    "on_demand",
	}); err != nil {
		t.Fatalf("upsert active subscription: %v", err)
	}

	handler := newTestHandler(db, domain.InviteSharedJoinRequest,
		WithClock(func() time.Time { return now }))
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: unknownSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	if got := countRows(t, db, `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = ? AND state = 'pending'`, tgID); got != 2 {
		t.Fatalf("pending grants = %d, want fallback to active", got)
	}
}

func TestAccessRequestUnknownFallbackUsesFreshestSubscriptionSignal(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5010)
	now := time.Unix(1_700_000_000, 0)
	lastEventAt := now.Add(-30 * time.Minute)
	lastCheckedAt := now.Add(-2 * time.Hour)

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+chat-freshest")
	seedSharedInvite(t, db, domain.ResourceChannel, "https://t.me/+channel-freshest")

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:          tgID,
		Platform:      domain.PlatformBoosty,
		StartedAt:     now.Add(-24 * time.Hour),
		LastEventAt:   &lastEventAt,
		LastCheckedAt: &lastCheckedAt,
		LastSignal:    "event",
	}); err != nil {
		t.Fatalf("upsert active subscription: %v", err)
	}

	handler := newTestHandler(db, domain.InviteSharedJoinRequest,
		WithClock(func() time.Time { return now }))
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: unknownSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	if got := countRows(t, db, `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = ? AND state = 'pending'`, tgID); got != 2 {
		t.Fatalf("pending grants = %d, want fallback from fresh event", got)
	}
}

func TestAccessRequestBannedSkipsGrantsAndReturnsBanned(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5004)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   tgID,
		Banned: true,
	}); err != nil {
		t.Fatalf("upsert banned user: %v", err)
	}

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	if got := countRows(t, db,
		`SELECT count(*) FROM access_grants WHERE tg_id = ?`, tgID); got != 0 {
		t.Fatalf("grants = %d, want none", got)
	}

	payload := firstDMPayload(t, db)
	if payload.Text != messages.Banned() {
		t.Fatalf("dm text = %q, want banned", payload.Text)
	}
}

func TestAccessRequestAlreadyInReturnsAlreadyIn(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5005)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	grants := store.NewGrants(db)
	if err := grants.MarkJoined(ctx, tgID, domain.ResourceChat, "bot"); err != nil {
		t.Fatalf("mark chat joined: %v", err)
	}

	if err := grants.MarkJoined(ctx, tgID, domain.ResourceChannel, "bot"); err != nil {
		t.Fatalf("mark channel joined: %v", err)
	}

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	payload := firstDMPayload(t, db)
	if payload.Text != messages.AlreadyIn() {
		t.Fatalf("dm text = %q, want already-in", payload.Text)
	}

	if got := countActions(t, db, domain.ActionSendInvite); got != 0 {
		t.Fatalf("send_invite actions = %d, want 0", got)
	}
}

func TestAccessRequestDirectEnqueuesSendInvites(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5006)

	handler := newTestHandler(db, domain.InviteDirect)
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	if got := countActions(t, db, domain.ActionSendInvite); got != 2 {
		t.Fatalf("send_invite actions = %d, want 2", got)
	}

	if got := countRows(t, db, `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = ? AND state = 'pending'`, tgID); got != 2 {
		t.Fatalf("pending grants = %d, want 2", got)
	}

	payload := firstDMPayload(t, db)
	if payload.Text != messages.ActiveDirect() {
		t.Fatalf("dm text = %q, want direct active", payload.Text)
	}
}

func TestAccessRequestPersonalLeftGrantCreatesNewSendInviteCycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5008)

	handler := newTestHandler(db, domain.InvitePersonalJoinRequest)
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest first: %v", err)
	}

	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest repeated pending: %v", err)
	}

	if got := countActions(t, db, domain.ActionSendInvite); got != 2 {
		t.Fatalf("send_invite actions after repeat = %d, want 2", got)
	}

	grants := store.NewGrants(db)
	if err := grants.MarkJoined(ctx, tgID, domain.ResourceChat, "bot"); err != nil {
		t.Fatalf("mark joined: %v", err)
	}

	if _, err := grants.MarkLeftUnlessRevoked(ctx, tgID, domain.ResourceChat); err != nil {
		t.Fatalf("mark left: %v", err)
	}

	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest after left: %v", err)
	}

	if got := countActions(t, db, domain.ActionSendInvite); got != 3 {
		t.Fatalf("send_invite actions after left = %d, want 3", got)
	}
}

func TestAccessRequestMissingSharedInviteCommitsAlertAndDM(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5007)

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:     domain.User{TGID: tgID},
		Snapshot: activeSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	if got := countRows(t, db,
		`SELECT count(*) FROM admin_alerts WHERE kind = 'invite_link_missing'`); got != 1 {
		t.Fatalf("invite_link_missing alerts = %d, want 1", got)
	}

	if got := countRows(t, db,
		`SELECT count(*) FROM access_grants WHERE tg_id = ?`, tgID); got != 0 {
		t.Fatalf("grants = %d, want none", got)
	}

	payload := firstDMPayload(t, db)
	if payload.Text != messages.TryLater() || !payload.RetryButton {
		t.Fatalf("dm payload = %+v, want retry-later with button", payload)
	}
}

func TestAccessRequestRateLimitedBannedReturnsBanned(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(5009)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   tgID,
		Banned: true,
	}); err != nil {
		t.Fatalf("upsert banned user: %v", err)
	}

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleAccessRequest(ctx, AccessRequest{
		User:        domain.User{TGID: tgID},
		Snapshot:    activeSnapshot(tgID),
		RateLimited: true,
	}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	payload := firstDMPayload(t, db)
	if payload.Text != messages.Banned() {
		t.Fatalf("dm text = %q, want banned", payload.Text)
	}
}

func TestJoinRequestActiveApproveKeyUsesRequestDate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(6001)
	rawLink := "https://t.me/+join-active"

	seedSharedInvite(t, db, domain.ResourceChat, rawLink)

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	for _, date := range []time.Time{
		time.Unix(1_700_000_000, 0),
		time.Unix(1_700_000_100, 0),
	} {
		if err := handler.HandleJoinRequest(ctx, JoinRequest{
			User:        domain.User{TGID: tgID, FirstName: "Join"},
			UserChatID:  9001,
			Resource:    domain.ResourceChat,
			InviteLink:  rawLink,
			RequestDate: date,
			Snapshot:    activeSnapshot(tgID),
		}); err != nil {
			t.Fatalf("HandleJoinRequest %v: %v", date, err)
		}
	}

	if got := countActions(t, db, domain.ActionApproveJoin); got != 2 {
		t.Fatalf("approve_join actions = %d, want one per request date", got)
	}

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}

	if grant.State != domain.GrantJoined || grant.AdmittedBy != "bot" {
		t.Fatalf("grant = %+v, want joined by bot", grant)
	}

	payload := firstDMPayload(t, db)
	if payload.ChatID != 9001 || payload.Text != messages.Granted() {
		t.Fatalf("dm payload = %+v, want user_chat_id and granted", payload)
	}
}

func TestJoinRequestMissingInviteLinkSharedFallbackApproves(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(6004)

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+missing-link")

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleJoinRequest(ctx, JoinRequest{
		User:        domain.User{TGID: tgID},
		UserChatID:  9004,
		Resource:    domain.ResourceChat,
		RequestDate: time.Unix(1_700_000_000, 0),
		Snapshot:    activeSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleJoinRequest: %v", err)
	}

	if got := countActions(t, db, domain.ActionApproveJoin); got != 1 {
		t.Fatalf("approve_join actions = %d, want 1", got)
	}

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}

	if grant.State != domain.GrantJoined {
		t.Fatalf("grant state = %s, want joined", grant.State)
	}
}

func TestJoinRequestPersonalMisuseDeclinesAndMarksInvite(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ownerID := int64(6002)
	requesterID := int64(6003)
	rawLink := "https://t.me/+personal-misuse"

	for _, tgID := range []int64{ownerID, requesterID} {
		if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
			t.Fatalf("upsert user %d: %v", tgID, err)
		}
	}

	link := seedPersonalInvite(t, db, ownerID, domain.ResourceChat,
		domain.InvitePersonalJoinRequest, rawLink)

	handler := newTestHandler(db, domain.InvitePersonalJoinRequest)
	if err := handler.HandleJoinRequest(ctx, JoinRequest{
		User:        domain.User{TGID: requesterID},
		UserChatID:  9003,
		Resource:    domain.ResourceChat,
		InviteLink:  rawLink,
		RequestDate: time.Unix(1_700_000_000, 0),
		Snapshot:    activeSnapshot(requesterID),
	}); err != nil {
		t.Fatalf("HandleJoinRequest: %v", err)
	}

	if got := countActions(t, db, domain.ActionDeclineJoin); got != 1 {
		t.Fatalf("decline_join actions = %d, want 1", got)
	}

	got, err := store.NewInvites(db).GetByID(ctx, link.ID)
	if err != nil {
		t.Fatalf("get invite: %v", err)
	}

	if got.Status != domain.InviteUsedByOther ||
		got.AttemptedBy == nil ||
		*got.AttemptedBy != requesterID {
		t.Fatalf("invite = %+v, want used_by_other attempted_by requester", got)
	}
}

func TestJoinRequestBannedDoesNotBurnPersonalInvite(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ownerID := int64(6005)
	requesterID := int64(6006)
	rawLink := "https://t.me/+banned-personal-misuse"

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: ownerID}); err != nil {
		t.Fatalf("upsert owner: %v", err)
	}

	if err := store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   requesterID,
		Banned: true,
	}); err != nil {
		t.Fatalf("upsert banned requester: %v", err)
	}

	link := seedPersonalInvite(t, db, ownerID, domain.ResourceChat,
		domain.InvitePersonalJoinRequest, rawLink)

	handler := newTestHandler(db, domain.InvitePersonalJoinRequest)
	if err := handler.HandleJoinRequest(ctx, JoinRequest{
		User:        domain.User{TGID: requesterID},
		UserChatID:  9006,
		Resource:    domain.ResourceChat,
		InviteLink:  rawLink,
		RequestDate: time.Unix(1_700_000_000, 0),
		Snapshot:    activeSnapshot(requesterID),
	}); err != nil {
		t.Fatalf("HandleJoinRequest: %v", err)
	}

	got, err := store.NewInvites(db).GetByID(ctx, link.ID)
	if err != nil {
		t.Fatalf("get invite: %v", err)
	}

	if got.Status == domain.InviteUsedByOther || got.AttemptedBy != nil {
		t.Fatalf("invite = %+v, want not burned by banned requester", got)
	}

	payload := firstDMPayload(t, db)
	if payload.Text != messages.Banned() {
		t.Fatalf("dm text = %q, want banned", payload.Text)
	}
}

func TestJoinRequestInactiveAndUnknownDecline(t *testing.T) {
	tests := []struct {
		name     string
		snapshot *engine.Snapshot
		wantText string
	}{
		{
			name:     "inactive",
			snapshot: inactiveSnapshot(6101),
			wantText: messages.MsgNoSub,
		},
		{
			name:     "unknown",
			snapshot: unknownSnapshot(6101),
			wantText: messages.TryLater(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			tgID := int64(6101)
			rawLink := "https://t.me/+join-decline-" + tt.name

			seedSharedInvite(t, db, domain.ResourceChat, rawLink)

			handler := newTestHandler(db, domain.InviteSharedJoinRequest)
			if err := handler.HandleJoinRequest(ctx, JoinRequest{
				User:        domain.User{TGID: tgID},
				UserChatID:  9101,
				Resource:    domain.ResourceChat,
				InviteLink:  rawLink,
				RequestDate: time.Unix(1_700_000_000, 0),
				Snapshot:    tt.snapshot,
			}); err != nil {
				t.Fatalf("HandleJoinRequest: %v", err)
			}

			if got := countActions(t, db, domain.ActionDeclineJoin); got != 1 {
				t.Fatalf("decline_join actions = %d, want 1", got)
			}

			payload := firstDMPayload(t, db)
			if payload.Text != tt.wantText {
				t.Fatalf("dm text = %q, want %q", payload.Text, tt.wantText)
			}
		})
	}
}

func TestMembershipExternalJoinAlertsWithoutKick(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(7001)

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   true,
	}); err != nil {
		t.Fatalf("HandleMembershipUpdate: %v", err)
	}

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}

	if grant.State != domain.GrantJoined || grant.AdmittedBy != "external" {
		t.Fatalf("grant = %+v, want external joined", grant)
	}

	if got := countActions(t, db, domain.ActionSoftKick); got != 0 {
		t.Fatalf("soft_kick actions = %d, want none", got)
	}

	if got := countRows(t, db,
		`SELECT count(*) FROM admin_alerts WHERE kind = 'external_join'`); got != 1 {
		t.Fatalf("external_join alerts = %d, want 1", got)
	}
}

func TestMembershipBannedExternalJoinHardBansWithoutGrant(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(7007)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   tgID,
		Banned: true,
	}); err != nil {
		t.Fatalf("upsert banned user: %v", err)
	}

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:      domain.User{TGID: tgID},
		Resource:  domain.ResourceChat,
		Joined:    true,
		EventDate: time.Unix(1_700_000_000, 0),
	}); err != nil {
		t.Fatalf("HandleMembershipUpdate: %v", err)
	}

	if _, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat); err == nil {
		t.Fatal("grant exists for banned external join, want none")
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get grant: %v", err)
	}

	if got := countActions(t, db, domain.ActionHardBan); got != 1 {
		t.Fatalf("hard_ban actions = %d, want one", got)
	}
}

func TestMembershipDirectInactiveSoftKicks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(7002)
	rawLink := "https://t.me/+direct-inactive"

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	seedPersonalInvite(t, db, tgID, domain.ResourceChat, domain.InviteDirect, rawLink)

	handler := newTestHandler(db, domain.InviteDirect)
	if err := handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:       domain.User{TGID: tgID},
		Resource:   domain.ResourceChat,
		Joined:     true,
		InviteLink: rawLink,
		Snapshot:   inactiveSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleMembershipUpdate: %v", err)
	}

	if got := countActions(t, db, domain.ActionSoftKick); got != 1 {
		t.Fatalf("soft_kick actions = %d, want 1", got)
	}
}

func TestMembershipDirectPendingGrantInactiveSoftKicks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(7004)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := store.NewGrants(db).MarkPending(
		ctx, tgID, domain.ResourceChat,
	); err != nil {
		t.Fatalf("mark pending grant: %v", err)
	}

	handler := newTestHandler(db, domain.InviteDirect)
	if err := handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   true,
		Snapshot: inactiveSnapshot(tgID),
	}); err != nil {
		t.Fatalf("HandleMembershipUpdate: %v", err)
	}

	if got := countActions(t, db, domain.ActionSoftKick); got != 1 {
		t.Fatalf("soft_kick actions = %d, want 1", got)
	}
}

func TestMembershipDirectInactiveSoftKickUsesEventCycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(7006)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	handler := newTestHandler(db, domain.InviteDirect)
	for _, eventDate := range []time.Time{
		time.Unix(1_700_000_000, 0),
		time.Unix(1_700_000_000, 0),
		time.Unix(1_700_000_060, 0),
	} {
		if _, err := store.NewGrants(db).MarkPending(
			ctx, tgID, domain.ResourceChat,
		); err != nil {
			t.Fatalf("mark pending grant: %v", err)
		}

		if err := handler.HandleMembershipUpdate(ctx, MembershipUpdate{
			User:      domain.User{TGID: tgID},
			Resource:  domain.ResourceChat,
			Joined:    true,
			EventDate: eventDate,
			Snapshot:  inactiveSnapshot(tgID),
		}); err != nil {
			t.Fatalf("HandleMembershipUpdate %v: %v", eventDate, err)
		}
	}

	if got := countActions(t, db, domain.ActionSoftKick); got != 2 {
		t.Fatalf("soft_kick actions = %d, want 2", got)
	}
}

func TestMembershipLeftPreservesRevokedGrant(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(7003)
	now := time.Unix(1_700_000_000, 0)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if err := store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:      tgID,
		Resource:  domain.ResourceChat,
		State:     domain.GrantRevoked,
		RevokedAt: &now,
	}); err != nil {
		t.Fatalf("upsert revoked grant: %v", err)
	}

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   false,
	}); err != nil {
		t.Fatalf("HandleMembershipUpdate: %v", err)
	}

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}

	if grant.State != domain.GrantRevoked {
		t.Fatalf("grant state = %s, want revoked", grant.State)
	}
}

func TestMembershipJoinPreservesRevokedGrant(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(7005)
	now := time.Unix(1_700_000_000, 0)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if err := store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:          tgID,
		Resource:      domain.ResourceChat,
		State:         domain.GrantRevoked,
		AdmittedBy:    "bot",
		RevokedAt:     &now,
		RevokedReason: "manual",
	}); err != nil {
		t.Fatalf("upsert revoked grant: %v", err)
	}

	handler := newTestHandler(db, domain.InviteSharedJoinRequest)
	if err := handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   true,
	}); err != nil {
		t.Fatalf("HandleMembershipUpdate: %v", err)
	}

	grant, err := store.NewGrants(db).Get(ctx, tgID, domain.ResourceChat)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}

	if grant.State != domain.GrantRevoked ||
		grant.RevokedAt == nil ||
		grant.RevokedReason != "manual" {
		t.Fatalf("grant = %+v, want revoked preserved", grant)
	}
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
			TelegramName:       "gk-shared",
			CreatesJoinRequest: true,
		})
	if err != nil {
		t.Fatalf("save shared invite: %v", err)
	}

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
			TelegramName:       "gk-personal",
			CreatesJoinRequest: mode != domain.InviteDirect,
		})
	if err != nil {
		t.Fatalf("save personal invite: %v", err)
	}

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
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}

	return n
}

func firstDMPayload(t *testing.T, db *sql.DB) sendDMPayload {
	t.Helper()

	var raw string
	if err := db.QueryRowContext(context.Background(), `
		SELECT payload_json
		FROM access_actions
		WHERE action_type = ?
		ORDER BY id
		LIMIT 1`, string(domain.ActionSendDM)).Scan(&raw); err != nil {
		t.Fatalf("read action payload: %v", err)
	}

	var payload sendDMPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode action payload: %v", err)
	}

	return payload
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(migrationsDir(t)))
	if err != nil {
		t.Fatalf("new goose provider: %v", err)
	}

	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return db
}

func migrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}
