package enforcer

import (
	"context"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/operatorlog"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

type opEventRecorder struct {
	kinds []domain.OperatorEventKind
}

func (r *opEventRecorder) render(ev domain.OperatorEvent) (string, error) {
	r.kinds = append(r.kinds, ev.Kind)

	return string(ev.Kind), nil
}

// A verify_member that finds the last source gone must let the engine recompute
// emit an access lifecycle event. Regression guard: if enforcer.Stores drops
// OperatorLog, the engine.Store inside recompute has a nil writer and nothing
// reaches the feed.
func TestEnforcerVerifyMemberEmitsAccessLossScheduled(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	outbox := store.NewOutbox(db)
	rec := &opEventRecorder{}

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:       tgID,
		Platform:   domain.PlatformBoosty,
		StartedAt:  time.Now().Add(-time.Hour),
		LastSignal: "event",
	})
	require.NoError(t, err, "upsert subscription")

	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       tgID,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}), "upsert grant")

	enqueueVerifyMember(t, ctx, outbox, tgID, nil,
		[]byte(`{"kind":"source","platform":"boosty","chat_id":-1001}`))

	tg := &fakeTelegram{member: &models.ChatMember{
		Type: models.ChatMemberTypeLeft,
	}}
	e := New(Stores{
		Outbox:        outbox,
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Audit:         store.NewAudit(db),
		Revocations:   store.NewRevocations(db),
		Whitelist:     store.NewWhitelist(db),
		Alerts:        store.NewAlerts(db),
		StatusEngine:  engine.New(nil),
		OperatorLog:   operatorlog.New(-1006666666666, rec.render, nil),
	}, tg, &fakeInvites{}, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(noopLimiter{}))

	_, err = e.runOnce(ctx)
	require.NoError(t, err, "runOnce")

	assert.Contains(t, rec.kinds, domain.OpAccessLossScheduled,
		"verify_member expiring the last source must emit access_loss_scheduled")
}
