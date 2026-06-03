package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/operatorlog"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

const testEventLogChatID int64 = -1006666666666

// operatorEngineStore wires a full revocation store plus an operator-event
// writer whose rendered text is the event kind, so tests can read back which
// kinds were durably enqueued to the event-log chat.
func operatorEngineStore(db *sql.DB) Store {
	repos := revocationEngineStore(db)
	repos.OperatorLog = operatorlog.New(testEventLogChatID,
		func(ev domain.OperatorEvent) (string, error) {
			return string(ev.Kind), nil
		}, nil)

	return repos
}

// emittedKinds returns the kind of every operator event durably enqueued to
// the event-log chat (rendered text == kind), in id order.
func emittedKinds(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(),
		`SELECT payload_json FROM access_actions WHERE action_type='send_dm' ORDER BY id`)
	require.NoError(t, err, "query access_actions")

	defer rows.Close()

	var kinds []string

	for rows.Next() {
		var raw string
		require.NoError(t, rows.Scan(&raw))

		var payload struct {
			Text   string `json:"text"`
			ChatID int64  `json:"chat_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))

		if payload.ChatID == testEventLogChatID {
			kinds = append(kinds, payload.Text)
		}
	}

	require.NoError(t, rows.Err())

	return kinds
}

func countKind(kinds []string, want domain.OperatorEventKind) int {
	n := 0

	for _, k := range kinds {
		if k == string(want) {
			n++
		}
	}

	return n
}

func TestHandleEventEmitsSourceAndGrantedOnFirstActiveSource(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := operatorEngineStore(db)
	tgID := random.TGID()

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}))

	_, err := e.HandleEvent(ctx, repos, domain.SubscriptionEvent{
		Platform:   domain.PlatformBoosty,
		Kind:       domain.EventActivated,
		TGUserID:   tgID,
		OccurredAt: now,
	})
	require.NoError(t, err, "HandleEvent boosty activate")

	kinds := emittedKinds(t, db)
	assert.Equal(t, 1, countKind(kinds, domain.OpSourceSubscriptionActivated),
		"first activation emits one source event")
	assert.Equal(t, 1, countKind(kinds, domain.OpAccessGranted),
		"first activation emits one access_granted on the transition")
}

func TestHandleEventDoesNotReGrantWhileAlreadyActive(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := operatorEngineStore(db)
	tgID := random.TGID()

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}))

	// First active source: a transition → access_granted.
	_, err := e.HandleEvent(ctx, repos, domain.SubscriptionEvent{
		Platform: domain.PlatformBoosty, Kind: domain.EventActivated,
		TGUserID: tgID, OccurredAt: now,
	})
	require.NoError(t, err)

	// A second source activates while already active: a source event, but NO
	// second access_granted (no non-active→active transition).
	_, err = e.HandleEvent(ctx, repos, domain.SubscriptionEvent{
		Platform: domain.PlatformTribute, Kind: domain.EventActivated,
		TGUserID: tgID, OccurredAt: now.Add(time.Minute),
	})
	require.NoError(t, err)

	kinds := emittedKinds(t, db)
	assert.Equal(t, 1, countKind(kinds, domain.OpAccessGranted),
		"access_granted must fire once per active episode, not per source")
	assert.Equal(t, 2, countKind(kinds, domain.OpSourceSubscriptionActivated),
		"each source activation emits its own source event")
}

func TestHandleEventReplayDoesNotDuplicateEvents(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := operatorEngineStore(db)
	tgID := random.TGID()

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}))

	event := domain.SubscriptionEvent{
		Platform: domain.PlatformBoosty, Kind: domain.EventActivated,
		TGUserID: tgID, OccurredAt: now,
	}

	for range 2 {
		_, err := e.HandleEvent(ctx, repos, event)
		require.NoError(t, err)
	}

	kinds := emittedKinds(t, db)
	assert.Equal(t, 1, countKind(kinds, domain.OpSourceSubscriptionActivated),
		"a replayed activation must not duplicate the source event")
	assert.Equal(t, 1, countKind(kinds, domain.OpAccessGranted),
		"a replayed activation must not duplicate access_granted")
}

func TestRevokeNowEmitsAccessLost(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	e := New(nil, WithClock(func() time.Time { return now }))
	repos := operatorEngineStore(db)
	tgID := random.TGID()

	require.NoError(t, repos.Users.Upsert(ctx, domain.User{TGID: tgID}))
	require.NoError(t, store.NewGrants(db).MarkJoinedByAdmission(
		ctx, tgID, domain.ResourceChat, "bot"))

	_, err := e.RevokeNow(ctx, repos, tgID, "manual cleanup")
	require.NoError(t, err, "RevokeNow")

	kinds := emittedKinds(t, db)
	assert.Equal(t, 1, countKind(kinds, domain.OpAccessLost),
		"an actual revocation emits one access_lost")
}
