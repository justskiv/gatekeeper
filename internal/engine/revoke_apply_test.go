package engine

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

func seedJoinedGrant(
	t *testing.T, db *sql.DB, tgID int64, resource domain.Resource,
) domain.AccessGrant {
	t.Helper()

	ctx := context.Background()
	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   resource,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert grant")

	g, err := store.NewGrants(db).Get(ctx, tgID, resource)
	require.NoError(t, err, "get grant")

	return g
}

func countSoftKicks(t *testing.T, db *sql.DB) int {
	t.Helper()

	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM access_actions WHERE action_type = ?`,
		string(domain.ActionSoftKick)).Scan(&n), "count soft kicks")

	return n
}

func grantState(
	t *testing.T, db *sql.DB, tgID int64, resource domain.Resource,
) domain.GrantState {
	t.Helper()

	g, err := store.NewGrants(db).Get(context.Background(), tgID, resource)
	require.NoError(t, err, "get grant")

	return g.State
}

// TestApplyRevocationSkipsGrantChangedSinceDecide guards Codex finding #2: a
// grant whose identity token (updated_at) differs from the decide snapshot has
// a stale protection verdict, so apply must NOT revoke it and MUST keep the
// pending revocation for the next pass.
func TestApplyRevocationSkipsGrantChangedSinceDecide(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")
	grant := seedJoinedGrant(t, db, tgID, domain.ResourceChat)
	require.NoError(t, store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID: tgID, Reason: "expired", ScheduledAt: time.Unix(0, 0),
	}), "upsert pending")

	// Plan carries a STALE identity token (grant changed since decide).
	plan := RevocationPlan{
		TGID:     tgID,
		Reason:   "expired",
		Decision: domain.AccessDecision{Status: domain.StatusInactive},
		Revokes: []PlannedRevoke{{
			Resource:  domain.ResourceChat,
			UpdatedAt: grant.UpdatedAt.Add(-time.Hour),
			Protected: false,
		}},
	}

	_, err := New(nil).ApplyRevocation(ctx, revocationEngineStore(db), tgID, plan)
	require.NoError(t, err, "ApplyRevocation")

	assert.Equal(t, domain.GrantJoined, grantState(t, db, tgID, domain.ResourceChat),
		"stale grant must not be revoked")
	assert.Equal(t, 0, countSoftKicks(t, db), "no soft-kick for stale grant")

	_, ok, err := store.NewRevocations(db).Get(ctx, tgID)
	require.NoError(t, err, "get pending")
	assert.True(t, ok, "pending revocation kept for retry")
}

// TestApplyRevocationKeepsPendingWhenSomeGrantsSkipped guards Codex finding #1:
// when one grant is revoked but another appeared since decide (absent from the
// plan), the pending revocation MUST remain so the new grant is retried — even
// though a revoke happened.
func TestApplyRevocationKeepsPendingWhenSomeGrantsSkipped(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")
	chat := seedJoinedGrant(t, db, tgID, domain.ResourceChat)
	// channel grant exists in the DB but is NOT in the decide plan (it appeared
	// after the snapshot).
	seedJoinedGrant(t, db, tgID, domain.ResourceChannel)
	require.NoError(t, store.NewRevocations(db).Upsert(ctx, domain.PendingRevocation{
		TGID: tgID, Reason: "expired", ScheduledAt: time.Unix(0, 0),
	}), "upsert pending")

	plan := RevocationPlan{
		TGID:     tgID,
		Reason:   "expired",
		Decision: domain.AccessDecision{Status: domain.StatusInactive},
		Revokes: []PlannedRevoke{{
			Resource:  domain.ResourceChat,
			UpdatedAt: chat.UpdatedAt,
			Protected: false,
		}},
	}

	_, err := New(nil).ApplyRevocation(ctx, revocationEngineStore(db), tgID, plan)
	require.NoError(t, err, "ApplyRevocation")

	assert.Equal(t, domain.GrantRevoked, grantState(t, db, tgID, domain.ResourceChat),
		"planned grant revoked")
	assert.Equal(t, domain.GrantJoined, grantState(t, db, tgID, domain.ResourceChannel),
		"grant that appeared after decide is not revoked")

	_, ok, err := store.NewRevocations(db).Get(ctx, tgID)
	require.NoError(t, err, "get pending")
	assert.True(t, ok, "pending kept because a grant was skipped")
}
