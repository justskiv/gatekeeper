package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

// TestWithUserLockPropagatesResult verifies the scoped lock helper runs fn and
// returns its result.
func TestWithUserLockPropagatesResult(t *testing.T) {
	e := New(nil)
	sentinel := errors.New("boom")

	require.NoError(t, e.WithUserLock(1, func() error { return nil }))
	assert.ErrorIs(t,
		e.WithUserLock(1, func() error { return sentinel }), sentinel)
}

// TestDecideApplyInsideUserLockDoesNotSelfDeadlock proves the non-reentrant
// contract: RevocationDecision and ApplyRevocation take no per-user lock, so
// they run safely inside WithUserLock. A regression (either method taking the
// lock) would hang forever; the timeout turns that into a fast failure.
func TestDecideApplyInsideUserLockDoesNotSelfDeadlock(t *testing.T) {
	db := testutil.NewDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	e := New(nil) // no sources: RevocationDecision uses persistedDecision only
	repos := revocationEngineStore(db)
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	err := e.WithUserLock(tgID, func() error {
		plan, err := e.RevocationDecision(ctx, repos, tgID, "expired")
		if err != nil {
			return err
		}

		_, err = e.ApplyRevocation(ctx, repos, tgID, plan)

		return err
	})
	require.NoError(t, err,
		"decide+apply inside WithUserLock must not deadlock")
}
