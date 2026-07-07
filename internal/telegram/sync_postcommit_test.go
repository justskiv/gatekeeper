package telegram

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/engine"
)

// TestSyncDefersReconcileToPostCommit guards the incident route: an owner
// /sync must NOT run reconciliation inside the update transaction (which would
// deadlock the single SQLite connection). adminSync only records a post-commit
// job that surfaces on RouteResult.PostCommitSync; the poller runs it on the
// pool after commit.
func TestSyncDefersReconcileToPostCommit(t *testing.T) {
	r := NewRouter(RouterDeps{}, nil, nil, slog.Default(),
		WithStatusEngine(engine.New(nil)))

	target := int64(4242)
	reply, err := r.adminSync(context.Background(), &target)
	require.NoError(t, err, "adminSync")
	assert.NotEmpty(t, reply, "owner gets an immediate reply")

	res, err := r.processedEffects(context.Background(), nil)
	require.NoError(t, err, "processedEffects")
	require.NotNil(t, res.PostCommitSync,
		"reconcile must be deferred to post-commit, not run inline")
	require.NotNil(t, res.PostCommitSync.Target, "single-user sync carries target")
	assert.Equal(t, target, *res.PostCommitSync.Target)
}

// TestNonSyncUpdateHasNoPostCommitJob verifies the field stays nil for updates
// that did not request a sync.
func TestNonSyncUpdateHasNoPostCommitJob(t *testing.T) {
	r := NewRouter(RouterDeps{}, nil, nil, slog.Default(),
		WithStatusEngine(engine.New(nil)))

	res, err := r.processedEffects(context.Background(), nil)
	require.NoError(t, err, "processedEffects")
	assert.Nil(t, res.PostCommitSync, "no sync requested => no post-commit job")
}
