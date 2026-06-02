package engine

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

func TestHandleEventActivatedIsIdempotentAndCancelsRevocation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)

	if err := repos.Users.Upsert(ctx, domain.User{TGID: 42}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if err := store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID:        42,
		Reason:      "expired",
		ScheduledAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("upsert revocation: %v", err)
	}

	event := domain.SubscriptionEvent{
		Platform:   domain.PlatformBoosty,
		Kind:       domain.EventActivated,
		TGUserID:   42,
		TGUsername: "alice",
		OccurredAt: now,
	}
	for i := range 2 {
		effects, err := e.HandleEvent(ctx, repos, event)
		if err != nil {
			t.Fatalf("HandleEvent #%d: %v", i+1, err)
		}

		if i == 0 && len(effects) != 1 {
			t.Fatalf("effects on first activation = %+v, want access-kept dm", effects)
		}

		if i == 1 && len(effects) != 0 {
			t.Fatalf("effects on repeated activation = %+v, want none", effects)
		}
	}

	subs, err := repos.Subscriptions.ListActiveByUser(ctx, 42)
	if err != nil {
		t.Fatalf("list active subscriptions: %v", err)
	}

	if len(subs) != 1 || subs[0].Platform != domain.PlatformBoosty {
		t.Fatalf("active subscriptions = %+v, want one boosty row", subs)
	}

	if _, ok, err := repos.Revocations.Get(ctx, 42); err != nil || ok {
		t.Fatalf("revocation = (_, %v, %v), want absent", ok, err)
	}
}

func TestHandleEventDeactivatedAndCancelledAreIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)

	activated := domain.SubscriptionEvent{
		Platform:   domain.PlatformTribute,
		Kind:       domain.EventActivated,
		TGUserID:   77,
		OccurredAt: now,
	}
	if _, err := e.HandleEvent(ctx, repos, activated); err != nil {
		t.Fatalf("activate: %v", err)
	}

	deactivated := activated
	deactivated.Kind = domain.EventDeactivated

	deactivated.OccurredAt = now.Add(time.Hour)
	for i := range 2 {
		if _, err := e.HandleEvent(ctx, repos, deactivated); err != nil {
			t.Fatalf("deactivate #%d: %v", i+1, err)
		}
	}

	active, err := repos.Subscriptions.ListActiveByUser(ctx, 77)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}

	if len(active) != 0 {
		t.Fatalf("active subscriptions = %+v, want none", active)
	}

	history, err := store.NewSubscriptions(db).ListByUser(ctx, 77)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}

	if len(history) != 1 || history[0].Status != domain.SubExpired {
		t.Fatalf("history = %+v, want one expired row", history)
	}

	cancelled := activated

	cancelled.Kind = domain.EventCancelledSubscription
	if _, err := e.HandleEvent(ctx, repos, cancelled); err != nil {
		t.Fatalf("cancelled: %v", err)
	}

	if active, err = repos.Subscriptions.ListActiveByUser(ctx, 77); err != nil {
		t.Fatalf("list active after cancelled: %v", err)
	}

	if len(active) != 0 {
		t.Fatalf("cancelled recreated active subscription: %+v", active)
	}
}

func TestApplyObservationsKeepsActiveOnUnknownAndNoSignal(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)

	if err := repos.Users.Upsert(ctx, domain.User{TGID: 88}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
		TGID:       88,
		Platform:   domain.PlatformBoosty,
		StartedAt:  now.Add(-time.Hour),
		LastSignal: "event",
	}); err != nil {
		t.Fatalf("seed active subscription: %v", err)
	}

	err := e.ApplyObservations(ctx, repos, 88, []domain.SourceVerdict{
		{Source: domain.PlatformBoosty, Verdict: domain.VerdictUnknown},
		{Source: domain.PlatformTribute, Verdict: domain.VerdictNoSignal},
	})
	if err != nil {
		t.Fatalf("ApplyObservations: %v", err)
	}

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, 88, domain.PlatformBoosty)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}

	if !ok || sub.Status != domain.SubActive {
		t.Fatalf("subscription = (%+v, %v), want active preserved", sub, ok)
	}
}

func TestPersistedDecisionIgnoresExpiredActiveRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := engineStore(db)

	expires := now.Add(-time.Hour)

	if err := repos.Users.Upsert(ctx, domain.User{TGID: 90}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
		TGID:       90,
		Platform:   domain.PlatformBoosty,
		StartedAt:  expires.Add(-time.Hour),
		ExpiresAt:  &expires,
		LastSignal: "event",
	}); err != nil {
		t.Fatalf("seed expired active subscription: %v", err)
	}

	decision, err := e.PersistedDecision(ctx, repos, 90)
	if err != nil {
		t.Fatalf("PersistedDecision: %v", err)
	}

	if decision.Status != domain.StatusInactive || decision.Allowed {
		t.Fatalf("decision = %+v, want inactive denied", decision)
	}

	if len(decision.Reasons) != 1 ||
		decision.Reasons[0].Verdict != domain.VerdictInactive {
		t.Fatalf("reasons = %+v, want inactive expired reason", decision.Reasons)
	}
}

func TestKeyedMutexSerializesSameUser(t *testing.T) {
	locks := NewKeyedMutex()
	unlock := locks.Lock(1)

	entered := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		unlockSecond := locks.Lock(1)
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

	if len(locks.locks) != 0 {
		t.Fatalf("locks map len = %d, want cleanup after unlock", len(locks.locks))
	}
}

func TestRecomputeAccessGraceSchedulesSingleRevocation(t *testing.T) {
	db := newTestDB(t)
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

	if err := repos.Users.Upsert(ctx, domain.User{TGID: 501}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	grants := concreteGrants(t, repos)
	if err := grants.Upsert(ctx, domain.AccessGrant{
		TGID:       501,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}); err != nil {
		t.Fatalf("upsert grant: %v", err)
	}

	for i := range 2 {
		if _, err := e.RecomputeAccess(ctx, repos, 501); err != nil {
			t.Fatalf("RecomputeAccess #%d: %v", i+1, err)
		}
	}

	pending, ok, err := repos.Revocations.Get(ctx, 501)
	if err != nil || !ok {
		t.Fatalf("pending = (%+v, %v, %v), want present", pending, ok, err)
	}

	if !pending.ScheduledAt.Equal(now.Add(time.Hour)) || !pending.Notified {
		t.Fatalf("pending = %+v, want scheduled notified", pending)
	}

	if got := countActions(t, ctx, db, domain.ActionSendDM); got != 1 {
		t.Fatalf("warning actions = %d, want one", got)
	}
}

func TestRecomputeAccessImmediateAndNotifyOnlyModes(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	t.Run("immediate revokes bot grants", func(t *testing.T) {
		ctx := context.Background()
		db := newTestDB(t)
		e := New(nil,
			WithClock(func() time.Time { return now }),
			WithRevocationConfig(RevocationConfig{ExpiryMode: "immediate"}),
		)
		repos := revocationEngineStore(db)

		seedRevocableGrant(t, ctx, repos, 502)

		if _, err := e.RecomputeAccess(ctx, repos, 502); err != nil {
			t.Fatalf("RecomputeAccess: %v", err)
		}

		if got := countActions(t, ctx, db, domain.ActionSoftKick); got != 1 {
			t.Fatalf("soft_kick actions = %d, want one", got)
		}

		grants := concreteGrants(t, repos)

		grant, err := grants.Get(ctx, 502, domain.ResourceChat)
		if err != nil {
			t.Fatalf("get grant: %v", err)
		}

		if grant.State != domain.GrantRevoked {
			t.Fatalf("grant state = %s, want revoked", grant.State)
		}
	})

	t.Run("notify_only does not revoke", func(t *testing.T) {
		ctx := context.Background()
		db := newTestDB(t)
		e := New(nil,
			WithClock(func() time.Time { return now }),
			WithRevocationConfig(RevocationConfig{ExpiryMode: "notify_only"}),
		)
		repos := revocationEngineStore(db)

		seedRevocableGrant(t, ctx, repos, 503)

		if _, err := e.RecomputeAccess(ctx, repos, 503); err != nil {
			t.Fatalf("RecomputeAccess: %v", err)
		}

		if got := countActions(t, ctx, db, domain.ActionSoftKick); got != 0 {
			t.Fatalf("soft_kick actions = %d, want zero", got)
		}

		if _, ok, err := repos.Revocations.Get(ctx, 503); err != nil || ok {
			t.Fatalf("pending = (_, %v, %v), want absent", ok, err)
		}
	})
}

func TestRevokeNowUsesLiveFinalDecision(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	t.Run("active source cancels pending revoke", func(t *testing.T) {
		ctx := context.Background()
		db := newTestDB(t)
		e := New([]SubscriptionSource{fakeSource{
			platform: domain.PlatformBoosty,
			verdict: domain.SourceVerdict{
				Source:  domain.PlatformBoosty,
				Verdict: domain.VerdictActive,
			},
		}}, WithClock(func() time.Time { return now }))
		repos := revocationEngineStore(db)

		seedRevocableGrant(t, ctx, repos, 604)

		if err := store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
			TGID:        604,
			Reason:      "expired",
			ScheduledAt: now,
		}); err != nil {
			t.Fatalf("upsert pending: %v", err)
		}

		if _, err := e.RevokeNow(ctx, repos, 604, "expired"); err != nil {
			t.Fatalf("RevokeNow: %v", err)
		}

		if got := countActions(t, ctx, db, domain.ActionSoftKick); got != 0 {
			t.Fatalf("soft_kick actions = %d, want zero", got)
		}

		if _, ok, err := repos.Revocations.Get(ctx, 604); err != nil || ok {
			t.Fatalf("pending = (_, %v, %v), want deleted", ok, err)
		}
	})

	t.Run("unknown source blocks revoke", func(t *testing.T) {
		ctx := context.Background()
		db := newTestDB(t)
		e := New([]SubscriptionSource{fakeSource{
			platform: domain.PlatformBoosty,
			err:      errors.New("source unavailable"),
		}}, WithClock(func() time.Time { return now }))
		repos := revocationEngineStore(db)

		seedRevocableGrant(t, ctx, repos, 605)

		if _, err := e.RevokeNow(ctx, repos, 605, "expired"); err != nil {
			t.Fatalf("RevokeNow: %v", err)
		}

		if got := countActions(t, ctx, db, domain.ActionSoftKick); got != 0 {
			t.Fatalf("soft_kick actions = %d, want zero", got)
		}

		var alerts int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM admin_alerts
			WHERE kind = 'revocation_unsafe'`).Scan(&alerts); err != nil {
			t.Fatalf("count alerts: %v", err)
		}

		if alerts != 1 {
			t.Fatalf("unsafe alerts = %d, want one", alerts)
		}
	})
}

func TestRevokeNowSafetyAndGrantFilters(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := revocationEngineStore(db)

	for _, tgID := range []int64{601, 602, 603} {
		if err := repos.Users.Upsert(ctx, domain.User{TGID: tgID}); err != nil {
			t.Fatalf("upsert user %d: %v", tgID, err)
		}
	}

	if _, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
		TGID:      601,
		Platform:  domain.PlatformBoosty,
		StartedAt: now,
	}); err != nil {
		t.Fatalf("active sub: %v", err)
	}

	if err := store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID:        601,
		Reason:      "expired",
		ScheduledAt: now,
	}); err != nil {
		t.Fatalf("pending active user: %v", err)
	}

	if _, err := e.RevokeNow(ctx, repos, 601, "expired"); err != nil {
		t.Fatalf("RevokeNow active: %v", err)
	}

	if _, ok, err := repos.Revocations.Get(ctx, 601); err != nil || ok {
		t.Fatalf("active pending = (_, %v, %v), want deleted", ok, err)
	}

	if got := countActions(t, ctx, db, domain.ActionSoftKick); got != 0 {
		t.Fatalf("soft_kick actions for active = %d, want zero", got)
	}

	grants := concreteGrants(t, repos)
	if err := grants.Upsert(ctx, domain.AccessGrant{
		TGID:       602,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "external",
	}); err != nil {
		t.Fatalf("external grant: %v", err)
	}

	if _, err := e.RevokeNow(ctx, repos, 602, "expired"); err != nil {
		t.Fatalf("RevokeNow external: %v", err)
	}

	if got := countActions(t, ctx, db, domain.ActionSoftKick); got != 0 {
		t.Fatalf("soft_kick actions for external = %d, want zero", got)
	}

	repos.Members = fakeMemberChecker{member: &models.ChatMember{
		Type: models.ChatMemberTypeAdministrator,
	}}

	if err := grants.Upsert(ctx, domain.AccessGrant{
		TGID:       603,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}); err != nil {
		t.Fatalf("protected grant: %v", err)
	}

	if _, err := e.RevokeNow(ctx, repos, 603, "expired"); err != nil {
		t.Fatalf("RevokeNow protected: %v", err)
	}

	if got := countActions(t, ctx, db, domain.ActionSoftKick); got != 0 {
		t.Fatalf("soft_kick actions for protected = %d, want zero", got)
	}

	var alerts int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM admin_alerts
		WHERE kind = 'protected_admin_lost_subscription'`).Scan(&alerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}

	if alerts != 1 {
		t.Fatalf("protected alerts = %d, want one", alerts)
	}
}

func seedRevocableGrant(
	t *testing.T,
	ctx context.Context,
	repos Store,
	tgID int64,
) {
	t.Helper()

	if err := repos.Users.Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	grants := concreteGrants(t, repos)
	if err := grants.Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}); err != nil {
		t.Fatalf("upsert grant: %v", err)
	}
}

func concreteGrants(t *testing.T, repos Store) *store.Grants {
	t.Helper()

	grants, ok := repos.Grants.(*store.Grants)
	if !ok {
		t.Fatalf("grants repo type = %T, want *store.Grants", repos.Grants)
	}

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
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM access_actions
		WHERE action_type = ?`, string(actionType)).Scan(&got); err != nil {
		t.Fatalf("count actions: %v", err)
	}

	return got
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
