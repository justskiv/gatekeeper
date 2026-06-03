package admission

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/operatorlog"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

// eventRecorder records every operator event passed to the writer's renderer,
// so tests can assert typed content (method, sources, actor) directly.
type eventRecorder struct {
	events []domain.OperatorEvent
}

func (r *eventRecorder) render(ev domain.OperatorEvent) (string, error) {
	r.events = append(r.events, ev)

	return string(ev.Kind), nil
}

func (r *eventRecorder) byKind(
	kind domain.OperatorEventKind,
) []domain.OperatorEvent {
	var out []domain.OperatorEvent

	for _, ev := range r.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}

	return out
}

func newOperatorHandler(db *sql.DB, rec *eventRecorder) *Handler {
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
		OperatorLog:   operatorlog.New(-1006666666666, rec.render, nil),
	}, Config{
		InviteMode:    domain.InviteSharedJoinRequest,
		ClubChatID:    -1001,
		ClubChannelID: -1002,
		Resources: []ResourceConfig{
			{Resource: domain.ResourceChat, ChatID: -1001},
			{Resource: domain.ResourceChannel, ChatID: -1002},
		},
	})
}

func TestMembershipExternalJoinEmitsJoinedEvent(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rec := &eventRecorder{}
	handler := newOperatorHandler(db, rec)

	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: random.TGID()},
		Resource: domain.ResourceChat,
		Joined:   true,
	}))

	joined := rec.byKind(domain.OpClubChatJoined)
	require.Len(t, joined, 1, "external chat join emits one club_chat_joined")
	assert.Equal(t, domain.AdmissionExternal, joined[0].Method)
	assert.Empty(t, joined[0].ActorLabel, "no actor was provided")
}

func TestMembershipJoinIncludesActiveAccessSources(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rec := &eventRecorder{}
	handler := newOperatorHandler(db, rec)
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"seed user")

	expires := time.Now().Add(720 * time.Hour)
	_, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:      tgID,
		Platform:  domain.PlatformBoosty,
		Status:    domain.SubActive,
		StartedAt: time.Now(),
		ExpiresAt: &expires,
	})
	require.NoError(t, err, "seed active subscription")

	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: tgID},
		Resource: domain.ResourceChat,
		Joined:   true,
	}))

	joined := rec.byKind(domain.OpClubChatJoined)
	require.Len(t, joined, 1)
	assert.Contains(t, joined[0].ActiveSources, domain.PlatformBoosty,
		"join event must show why the user has access")
}

func TestMembershipBannedJoinEmitsBannedAttempt(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rec := &eventRecorder{}
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID: tgID, Banned: true,
	}))

	handler := newOperatorHandler(db, rec)
	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:      domain.User{TGID: tgID},
		Resource:  domain.ResourceChat,
		Joined:    true,
		EventDate: time.Unix(1_700_000_000, 0),
	}))

	assert.Len(t, rec.byKind(domain.OpBannedJoinAttempt), 1,
		"banned join emits one banned_join_attempt")
	assert.Empty(t, rec.byKind(domain.OpClubChatJoined),
		"a banned join must not also emit a normal join")
}

// Regression guard for the admission engineStore() wiring: a /start that
// recomputes an active user with a pending revocation must cancel it and emit
// access_kept through the engine. If engineStore() drops OperatorLog, the
// engine emit silently no-ops and this fails.
func TestAccessRequestCancelsPendingRevocationEmitsKept(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rec := &eventRecorder{}
	handler := newOperatorHandler(db, rec)
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}))

	expires := time.Now().Add(720 * time.Hour)
	_, err := store.NewSubscriptions(db).UpsertManual(ctx, tgID, &expires, "seed")
	require.NoError(t, err, "seed active manual subscription")

	require.NoError(t, store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID:        tgID,
		Reason:      "expired",
		ScheduledAt: time.Now().Add(time.Hour),
	}), "seed pending revocation")

	require.NoError(t, handler.HandleAccessRequest(ctx, AccessRequest{
		User:    domain.User{TGID: tgID},
		Trigger: "start",
	}))

	assert.Len(t, rec.byKind(domain.OpAccessKept), 1,
		"recompute that cancels a pending revoke must emit access_kept")

	_, ok, err := store.NewRevocations(db).Get(ctx, tgID)
	require.NoError(t, err)
	assert.False(t, ok, "pending revocation must be cancelled")
}

func TestMembershipChannelLeaveEmitsUnsubscribe(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	rec := &eventRecorder{}
	handler := newOperatorHandler(db, rec)

	require.NoError(t, handler.HandleMembershipUpdate(ctx, MembershipUpdate{
		User:     domain.User{TGID: random.TGID()},
		Resource: domain.ResourceChannel,
		Joined:   false,
	}))

	assert.Len(t, rec.byKind(domain.OpClubChannelUnsubscribed), 1,
		"leaving the club channel emits club_channel_unsubscribed")
}
