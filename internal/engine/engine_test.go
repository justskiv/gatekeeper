package engine

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

func TestHandleEventActivatedIsIdempotentAndCancelsRevocation(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)
	tgID := random.TGID()

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	require.NoError(t, store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID:        tgID,
		Reason:      "expired",
		ScheduledAt: now.Add(time.Hour),
	}), "upsert revocation")

	event := domain.SubscriptionEvent{
		Platform:   domain.PlatformBoosty,
		Kind:       domain.EventActivated,
		TGUserID:   tgID,
		TGUsername: gofakeit.Username(),
		OccurredAt: now,
	}
	for i := range 2 {
		effects, err := e.HandleEvent(ctx, repos, event)
		require.NoErrorf(t, err, "HandleEvent #%d", i+1)

		if i == 0 {
			assert.Len(t, effects, 1, "first activation must keep access via dm")
		}

		if i == 1 {
			assert.Empty(t, effects, "repeated activation must produce no effects")
		}
	}

	subs, err := repos.Subscriptions.ListActiveByUser(ctx, tgID)
	require.NoError(t, err, "list active subscriptions")
	require.Len(t, subs, 1, "want one active row")
	assert.Equal(t, domain.PlatformBoosty, subs[0].Platform)

	_, ok, err := repos.Revocations.Get(ctx, tgID)
	require.NoError(t, err)
	assert.False(t, ok, "revocation must be cancelled")
}

func TestHandleEventDeactivatedAndCancelledAreIdempotent(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)
	tgID := random.TGID()

	activated := domain.SubscriptionEvent{
		Platform:   domain.PlatformTribute,
		Kind:       domain.EventActivated,
		TGUserID:   tgID,
		OccurredAt: now,
	}
	_, err := e.HandleEvent(ctx, repos, activated)
	require.NoError(t, err, "activate")

	deactivated := activated
	deactivated.Kind = domain.EventDeactivated

	deactivated.OccurredAt = now.Add(time.Hour)
	for i := range 2 {
		_, err := e.HandleEvent(ctx, repos, deactivated)
		require.NoErrorf(t, err, "deactivate #%d", i+1)
	}

	active, err := repos.Subscriptions.ListActiveByUser(ctx, tgID)
	require.NoError(t, err, "list active")
	assert.Empty(t, active, "deactivated user must have no active subscriptions")

	history, err := store.NewSubscriptions(db).ListByUser(ctx, tgID)
	require.NoError(t, err, "list history")
	require.Len(t, history, 1, "want one history row")
	assert.Equal(t, domain.SubExpired, history[0].Status)

	cancelled := activated

	cancelled.Kind = domain.EventCancelledSubscription
	_, err = e.HandleEvent(ctx, repos, cancelled)
	require.NoError(t, err, "cancelled")

	active, err = repos.Subscriptions.ListActiveByUser(ctx, tgID)
	require.NoError(t, err, "list active after cancelled")
	assert.Empty(t, active, "cancelled must not recreate an active subscription")
}

func TestTributeCancelledSubscriptionDoesNotRevokeByDefault(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)
	tgID := random.TGID()

	activated := domain.SubscriptionEvent{
		Platform:      domain.PlatformTribute,
		Kind:          domain.EventActivated,
		TGUserID:      tgID,
		EventAt:       now,
		ProviderEvent: "new_subscription",
		OccurredAt:    now,
	}
	_, err := e.HandleEvent(ctx, repos, activated)
	require.NoError(t, err, "activate")

	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert grant")

	cancelled := activated
	cancelled.Kind = domain.EventCancelledSubscription
	cancelled.EventAt = now.Add(time.Hour)
	cancelled.ProviderEvent = "cancelled_subscription"
	cancelled.OccurredAt = cancelled.EventAt

	effects, err := e.HandleEvent(ctx, repos, cancelled)
	require.NoError(t, err, "cancelled")
	assert.Empty(t, effects, "cancel must produce no effects by default")

	_, ok, err := repos.Subscriptions.GetActive(ctx, tgID, domain.PlatformTribute)
	require.NoError(t, err)
	assert.True(t, ok, "active subscription must be kept")

	_, ok, err = repos.Revocations.Get(ctx, tgID)
	require.NoError(t, err)
	assert.False(t, ok, "no revocation must be scheduled")
}

func TestTributeStaleCancelledSubscriptionWritesAudit(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)
	tgID := random.TGID()

	activatedAt := now.Add(time.Hour)
	activated := domain.SubscriptionEvent{
		Platform:      domain.PlatformTribute,
		Kind:          domain.EventActivated,
		TGUserID:      tgID,
		EventAt:       activatedAt,
		ProviderEvent: "new_subscription",
		OccurredAt:    activatedAt,
	}
	_, err := e.HandleEvent(ctx, repos, activated)
	require.NoError(t, err, "activate")

	cancelled := activated
	cancelled.Kind = domain.EventCancelledSubscription
	cancelled.EventAt = now
	cancelled.ProviderEvent = "cancelled_subscription"
	cancelled.OccurredAt = cancelled.EventAt

	effects, err := e.HandleEvent(ctx, repos, cancelled)
	require.NoError(t, err, "cancelled")
	assert.Empty(t, effects, "stale cancel must produce no effects")

	sub, ok, err := repos.Subscriptions.GetActive(ctx, tgID, domain.PlatformTribute)
	require.NoError(t, err)
	require.True(t, ok, "active subscription must be kept")
	require.NotNil(t, sub.LastEventAt)
	assert.True(t, sub.LastEventAt.Equal(activatedAt),
		"stale cancel must not overwrite last_event_at")

	var cancelledAudit int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM audit_log
		WHERE kind = ?`,
		auditSubscriptionCancelled).Scan(&cancelledAudit), "count cancel audit")
	assert.Equal(t, 1, cancelledAudit, "stale cancel must write one audit row")
}

func TestApplyObservationsKeepsActiveOnUnknownAndNoSignal(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)
	tgID := random.TGID()

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
		TGID:       tgID,
		Platform:   domain.PlatformBoosty,
		StartedAt:  now.Add(-time.Hour),
		LastSignal: "event",
	})
	require.NoError(t, err, "seed active subscription")

	err = e.ApplyObservations(ctx, repos, tgID, []domain.SourceVerdict{
		{Source: domain.PlatformBoosty, Verdict: domain.VerdictUnknown},
		{Source: domain.PlatformTribute, Verdict: domain.VerdictNoSignal},
	})
	require.NoError(t, err, "ApplyObservations")

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, tgID, domain.PlatformBoosty)
	require.NoError(t, err, "GetActive")
	require.True(t, ok, "subscription must be preserved")
	assert.Equal(t, domain.SubActive, sub.Status, "active status must be preserved")
}

func TestPersistedDecisionIgnoresExpiredActiveRows(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)
	tgID := random.TGID()

	expires := now.Add(-time.Hour)

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
		TGID:       tgID,
		Platform:   domain.PlatformBoosty,
		StartedAt:  expires.Add(-time.Hour),
		ExpiresAt:  &expires,
		LastSignal: "event",
	})
	require.NoError(t, err, "seed expired active subscription")

	decision, err := e.PersistedDecision(ctx, repos, tgID)
	require.NoError(t, err, "PersistedDecision")
	assert.Equal(t, domain.StatusInactive, decision.Status)
	assert.False(t, decision.Allowed, "expired subscription must be denied")
	require.Len(t, decision.Reasons, 1, "want one inactive reason")
	assert.Equal(t, domain.VerdictInactive, decision.Reasons[0].Verdict)
}

func TestKeyedMutexSerializesSameUser(t *testing.T) {
	locks := NewKeyedMutex()
	key := random.TGID()
	unlock := locks.Lock(key)

	entered := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		unlockSecond := locks.Lock(key)
		defer unlockSecond()

		close(entered)
	}()

	select {
	case <-entered:
		t.Fatal("second lock entered while first lock is held")
	case <-time.After(20 * time.Millisecond):
	}

	unlock()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("second lock did not enter after unlock")
	}

	<-done

	locks.mu.Lock()
	defer locks.mu.Unlock()

	assert.Empty(t, locks.locks, "locks map must be cleaned up after unlock")
}

func TestRecomputeAccessGraceSchedulesSingleRevocation(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	e := New(nil,
		WithClock(func() time.Time { return now }),
		WithRevocationConfig(RevocationConfig{
			ExpiryMode:  "grace",
			GracePeriod: time.Hour,
		}),
	)
	repos := revocationEngineStore(db)
	tgID := random.TGID()

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	grants := concreteGrants(t, repos)
	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert grant")

	for i := range 2 {
		_, err := e.RecomputeAccess(ctx, repos, tgID)
		require.NoErrorf(t, err, "RecomputeAccess #%d", i+1)
	}

	pending, ok, err := repos.Revocations.Get(ctx, tgID)
	require.NoError(t, err)
	require.True(t, ok, "pending revocation must be present")
	assert.True(t, pending.ScheduledAt.Equal(now.Add(time.Hour)),
		"revocation must be scheduled at grace end")
	assert.True(t, pending.Notified, "user must have been notified")

	assert.Equal(t, 1, countActions(t, ctx, db, domain.ActionSendDM),
		"grace must enqueue a single warning")
}

func TestRecomputeAccessImmediateAndNotifyOnlyModes(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	t.Run("immediate revokes bot grants", func(t *testing.T) {
		ctx := context.Background()
		db := testutil.NewDB(t)
		e := New(nil,
			WithClock(func() time.Time { return now }),
			WithRevocationConfig(RevocationConfig{ExpiryMode: "immediate"}),
		)
		repos := revocationEngineStore(db)
		tgID := random.TGID()

		seedRevocableGrant(t, ctx, repos, tgID)

		_, err := e.RecomputeAccess(ctx, repos, tgID)
		require.NoError(t, err, "RecomputeAccess")
		assert.Equal(t, 1, countActions(t, ctx, db, domain.ActionSoftKick),
			"immediate mode must enqueue a soft kick")

		grants := concreteGrants(t, repos)

		grant, err := grants.Get(ctx, tgID, domain.ResourceChat)
		require.NoError(t, err, "get grant")
		assert.Equal(t, domain.GrantRevoked, grant.State)
	})

	t.Run("notify_only does not revoke", func(t *testing.T) {
		ctx := context.Background()
		db := testutil.NewDB(t)
		e := New(nil,
			WithClock(func() time.Time { return now }),
			WithRevocationConfig(RevocationConfig{ExpiryMode: "notify_only"}),
		)
		repos := revocationEngineStore(db)
		tgID := random.TGID()

		seedRevocableGrant(t, ctx, repos, tgID)

		_, err := e.RecomputeAccess(ctx, repos, tgID)
		require.NoError(t, err, "RecomputeAccess")
		assert.Zero(t, countActions(t, ctx, db, domain.ActionSoftKick),
			"notify_only must not enqueue a soft kick")

		_, ok, err := repos.Revocations.Get(ctx, tgID)
		require.NoError(t, err)
		assert.False(t, ok, "notify_only must not schedule a revocation")
	})
}

func TestRevokeNowUsesLiveFinalDecision(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	t.Run("active source cancels pending revoke", func(t *testing.T) {
		ctx := context.Background()
		db := testutil.NewDB(t)
		e := New([]SubscriptionSource{fakeSource{
			platform: domain.PlatformBoosty,
			verdict: domain.SourceVerdict{
				Source:  domain.PlatformBoosty,
				Verdict: domain.VerdictActive,
			},
		}}, WithClock(func() time.Time { return now }))
		repos := revocationEngineStore(db)
		tgID := random.TGID()

		seedRevocableGrant(t, ctx, repos, tgID)

		require.NoError(t, store.NewRevocations(db).Upsert(ctx,
			domain.PendingRevocation{
				TGID:        tgID,
				Reason:      "expired",
				ScheduledAt: now,
			}), "upsert pending")

		_, err := e.RevokeNow(ctx, repos, tgID, "expired")
		require.NoError(t, err, "RevokeNow")
		assert.Zero(t, countActions(t, ctx, db, domain.ActionSoftKick),
			"active source must not enqueue a soft kick")

		_, ok, err := repos.Revocations.Get(ctx, tgID)
		require.NoError(t, err)
		assert.False(t, ok, "active source must clear the pending revocation")
	})

	t.Run("unknown source blocks revoke", func(t *testing.T) {
		ctx := context.Background()
		db := testutil.NewDB(t)
		e := New([]SubscriptionSource{fakeSource{
			platform: domain.PlatformBoosty,
			err:      errors.New("source unavailable"),
		}}, WithClock(func() time.Time { return now }))
		repos := revocationEngineStore(db)
		tgID := random.TGID()

		seedRevocableGrant(t, ctx, repos, tgID)

		_, err := e.RevokeNow(ctx, repos, tgID, "expired")
		require.NoError(t, err, "RevokeNow")
		assert.Zero(t, countActions(t, ctx, db, domain.ActionSoftKick),
			"unknown source must not enqueue a soft kick")

		var alerts int
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT count(*) FROM admin_alerts
			WHERE kind = 'revocation_unsafe'`).Scan(&alerts), "count alerts")
		assert.Equal(t, 1, alerts, "unknown source must raise one unsafe alert")
	})
}

func TestRevokeNowSafetyAndGrantFilters(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := revocationEngineStore(db)
	activeID := random.TGID()
	externalID := random.TGID()
	protectedID := random.TGID()

	for _, tgID := range []int64{activeID, externalID, protectedID} {
		require.NoErrorf(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}),
			"upsert user %d", tgID)
	}

	_, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
		TGID:      activeID,
		Platform:  domain.PlatformBoosty,
		StartedAt: now,
	})
	require.NoError(t, err, "active sub")

	require.NoError(t, store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID:        activeID,
		Reason:      "expired",
		ScheduledAt: now,
	}), "pending active user")

	_, err = e.RevokeNow(ctx, repos, activeID, "expired")
	require.NoError(t, err, "RevokeNow active")

	_, ok, err := repos.Revocations.Get(ctx, activeID)
	require.NoError(t, err)
	assert.False(t, ok, "active user pending must be deleted")
	assert.Zero(t, countActions(t, ctx, db, domain.ActionSoftKick),
		"active user must not be soft kicked")

	grants := concreteGrants(t, repos)
	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:       externalID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "external",
	}), "external grant")

	_, err = e.RevokeNow(ctx, repos, externalID, "expired")
	require.NoError(t, err, "RevokeNow external")
	assert.Zero(t, countActions(t, ctx, db, domain.ActionSoftKick),
		"external grant must not be soft kicked")

	repos.Members = fakeMemberChecker{member: &models.ChatMember{
		Type: models.ChatMemberTypeAdministrator,
	}}

	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:       protectedID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "protected grant")

	_, err = e.RevokeNow(ctx, repos, protectedID, "expired")
	require.NoError(t, err, "RevokeNow protected")
	assert.Zero(t, countActions(t, ctx, db, domain.ActionSoftKick),
		"protected admin must not be soft kicked")

	var alerts int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM admin_alerts
		WHERE kind = 'protected_admin_lost_subscription'`).Scan(&alerts),
		"count alerts")
	assert.Equal(t, 1, alerts, "protected admin must raise one alert")
}

func seedRevocableGrant(
	t *testing.T,
	ctx context.Context,
	repos Store,
	tgID int64,
) {
	t.Helper()

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	grants := concreteGrants(t, repos)
	require.NoError(t, grants.Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert grant")
}

func concreteGrants(t *testing.T, repos Store) *store.Grants {
	t.Helper()

	grants, ok := repos.Grants.(*store.Grants)
	require.Truef(t, ok, "grants repo type = %T, want *store.Grants", repos.Grants)

	return grants
}

func engineStore(db *sql.DB) Store {
	return Store{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Audit:         store.NewAudit(db),
		Revocations:   store.NewRevocations(db),
		Whitelist:     store.NewWhitelist(db),
	}
}

func revocationEngineStore(db *sql.DB) Store {
	repos := engineStore(db)
	repos.Grants = store.NewGrants(db)
	repos.Outbox = store.NewOutbox(db)
	repos.Alerts = store.NewAlerts(db)

	return repos
}

type fakeMemberChecker struct {
	member *models.ChatMember
}

func (f fakeMemberChecker) GetChatMember(
	context.Context,
	domain.Resource,
	int64,
) (*models.ChatMember, error) {
	return f.member, nil
}

type fakeSource struct {
	platform domain.Platform
	verdict  domain.SourceVerdict
	err      error
}

func (s fakeSource) Platform() domain.Platform {
	if s.platform == "" {
		return domain.PlatformBoosty
	}

	return s.platform
}

func (s fakeSource) Verdict(
	context.Context,
	int64,
) (domain.SourceVerdict, error) {
	if s.err != nil {
		return domain.SourceVerdict{}, s.err
	}

	verdict := s.verdict
	if verdict.Source == "" {
		verdict.Source = s.Platform()
	}

	return verdict, nil
}

func countActions(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	actionType domain.ActionType,
) int {
	t.Helper()

	var got int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM access_actions
		WHERE action_type = ?`, string(actionType)).Scan(&got), "count actions")

	return got
}
