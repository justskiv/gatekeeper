package reconcile

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

// poolReadingSource reproduces the self-deadlock shape: its Verdict reads the
// connection pool (like the real Membership source with WithLedger), so the
// engine's LiveSnapshot issues a pool query. Before the decide/apply split,
// revokeDue ran LiveSnapshot inside its write transaction and this pool read
// blocked forever on the single SQLite connection. It always reports inactive
// so the revocation proceeds.
type poolReadingSource struct {
	db       *sql.DB
	platform domain.Platform
}

func (s poolReadingSource) Platform() domain.Platform { return s.platform }

func (s poolReadingSource) Verdict(
	ctx context.Context, tgID int64,
) (domain.SourceVerdict, error) {
	// The exact query that self-deadlocked when run inside a transaction.
	if _, _, err := store.NewSubscriptions(s.db).GetActive(
		ctx, tgID, s.platform); err != nil {
		return domain.SourceVerdict{}, err
	}

	return domain.SourceVerdict{
		Source:  s.platform,
		Verdict: domain.VerdictInactive,
	}, nil
}

// adminMemberChecker reports every user as a chat administrator, so revocation
// treats their grants as protected.
type adminMemberChecker struct{}

func (adminMemberChecker) GetChatMember(
	_ context.Context, _ domain.Resource, _ int64,
) (*models.ChatMember, error) {
	return &models.ChatMember{
		Type:          models.ChatMemberTypeAdministrator,
		Administrator: &models.ChatMemberAdministrator{},
	}, nil
}

func seedDueRevocation(t *testing.T, db *sql.DB, tgID int64, now time.Time) {
	t.Helper()

	ctx := context.Background()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")
	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert grant")
	require.NoError(t, store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID:        tgID,
		Reason:      "expired",
		ScheduledAt: now.Add(-time.Minute),
	}), "upsert pending revocation")
}

// TestRevokeDueWithPoolReadingSourceDoesNotDeadlock guards the SQLite
// self-deadlock: revokeDue must run the live decide phase on the pool (outside
// its write transaction), so a pool-reading source no longer blocks on the
// single connection. The context timeout turns a regression from an infinite
// hang into a fast failure.
func TestRevokeDueWithPoolReadingSourceDoesNotDeadlock(t *testing.T) {
	db := testutil.NewDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	tgID := random.TGID()
	seedDueRevocation(t, db, tgID, now)

	eng := engine.New(
		[]engine.SubscriptionSource{
			poolReadingSource{db: db, platform: domain.PlatformTribute},
		},
		engine.WithClock(func() time.Time { return now }),
	)
	r := New(db, eng, nil, Config{}, nil,
		WithClock(func() time.Time { return now }))

	require.NoError(t,
		r.revokeDue(ctx, domain.PendingRevocation{TGID: tgID, Reason: "expired"}),
		"revokeDue must complete without deadlock")

	assert.Equal(t, 1, countActions(t, db, domain.ActionSoftKick),
		"one soft-kick enqueued")

	_, ok, err := store.NewRevocations(db).Get(ctx, tgID)
	require.NoError(t, err, "get pending revocation")
	assert.False(t, ok, "pending revocation cleared")
}

// TestRevokeDueProtectionResolvedInDecidePhase verifies the second live read —
// the admin protection check — is resolved during decide (on the pool) and that
// a protected grant is not soft-kicked in the apply phase.
func TestRevokeDueProtectionResolvedInDecidePhase(t *testing.T) {
	db := testutil.NewDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	tgID := random.TGID()
	seedDueRevocation(t, db, tgID, now)

	eng := engine.New(
		[]engine.SubscriptionSource{
			poolReadingSource{db: db, platform: domain.PlatformTribute},
		},
		engine.WithClock(func() time.Time { return now }),
	)
	r := New(db, eng, nil, Config{}, nil,
		WithClock(func() time.Time { return now }),
		WithMemberChecker(adminMemberChecker{}))

	require.NoError(t,
		r.revokeDue(ctx, domain.PendingRevocation{TGID: tgID, Reason: "expired"}),
		"revokeDue must complete")

	assert.Equal(t, 0, countActions(t, db, domain.ActionSoftKick),
		"protected admin must not be soft-kicked")
}
