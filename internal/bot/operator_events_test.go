package bot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/operatorlog"
	"github.com/justskiv/gatekeeper/internal/store"
)

type opEventRecorder struct {
	events []domain.OperatorEvent
}

func (r *opEventRecorder) render(ev domain.OperatorEvent) (string, error) {
	r.events = append(r.events, ev)

	return string(ev.Kind), nil
}

func (r *opEventRecorder) count(kind domain.OperatorEventKind) int {
	n := 0

	for _, ev := range r.events {
		if ev.Kind == kind {
			n++
		}
	}

	return n
}

func (r *opEventRecorder) first(
	kind domain.OperatorEventKind,
) (domain.OperatorEvent, bool) {
	for _, ev := range r.events {
		if ev.Kind == kind {
			return ev, true
		}
	}

	return domain.OperatorEvent{}, false
}

func operatorAdminHandler(db store.DBTX, rec *opEventRecorder) *UserCommands {
	deps := adminDeps(db, nil)
	deps.OperatorLog = operatorlog.New(-1006666666666, rec.render, nil)

	return NewCommands(deps, []int64{adminTestOwnerID})
}

func TestAdminGrantEmitsManualGrantAndAccessGranted(t *testing.T) {
	resetAdminConfirmations(t)

	db := newTestDB(t)
	ctx := context.Background()
	rec := &opEventRecorder{}
	handler := operatorAdminHandler(db, rec)

	confirm := adminConfirmData(t, handler, ctx, "/grant 800 1h temp")
	_, err := handler.HandleCallback(ctx, adminCallback(confirm))
	require.NoError(t, err, "confirm grant")

	// A repeated confirm must not re-execute or duplicate operator events.
	_, err = handler.HandleCallback(ctx, adminCallback(confirm))
	require.NoError(t, err, "duplicate confirm")

	assert.Equal(t, 1, rec.count(domain.OpManualGrant),
		"one manual_grant per confirmed command")
	assert.Equal(t, 1, rec.count(domain.OpAccessGranted),
		"a grant that activates access emits one access_granted")
}

func TestAdminBanEmitsManualBanAndAccessLost(t *testing.T) {
	resetAdminConfirmations(t)

	db := newTestDB(t)
	ctx := context.Background()
	rec := &opEventRecorder{}
	handler := operatorAdminHandler(db, rec)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: 801}))
	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:       801,
		Resource:   domain.ResourceChat,
		State:      domain.GrantJoined,
		AdmittedBy: "bot",
	}))

	confirm := adminConfirmData(t, handler, ctx, "/ban 801 abuse")
	_, err := handler.HandleCallback(ctx, adminCallback(confirm))
	require.NoError(t, err, "confirm ban")

	assert.Equal(t, 1, rec.count(domain.OpManualBan), "one manual_ban")

	lost, ok := rec.first(domain.OpAccessLost)
	require.True(t, ok, "ban that revokes a grant emits access_lost")
	assert.True(t, lost.HardBan, "ban-driven loss must mark hard-ban override")
}

func TestAdminNoExpiryGrantReportsManualNotWhitelist(t *testing.T) {
	resetAdminConfirmations(t)

	db := newTestDB(t)
	ctx := context.Background()
	rec := &opEventRecorder{}
	handler := operatorAdminHandler(db, rec)

	// "comp" is not a duration, so this is a no-expiry grant stored as
	// whitelist. The feed must still report it as manual access.
	confirm := adminConfirmData(t, handler, ctx, "/grant 805 comp")
	_, err := handler.HandleCallback(ctx, adminCallback(confirm))
	require.NoError(t, err, "confirm grant")

	granted, ok := rec.first(domain.OpAccessGranted)
	require.True(t, ok, "no-expiry grant activates access and emits access_granted")
	assert.Contains(t, granted.ActiveSources, domain.PlatformManual,
		"whitelist-backed grant must read as manual in the feed")
	assert.NotContains(t, granted.ActiveSources, domain.Platform("whitelist"),
		"whitelist must never surface as a feed source")
}

func TestAdminUnbanEmitsManualUnbanWithoutGrant(t *testing.T) {
	resetAdminConfirmations(t)

	db := newTestDB(t)
	ctx := context.Background()
	rec := &opEventRecorder{}
	handler := operatorAdminHandler(db, rec)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID: 802, Banned: true,
	}))

	confirm := adminConfirmData(t, handler, ctx, "/unban 802 pardon")
	_, err := handler.HandleCallback(ctx, adminCallback(confirm))
	require.NoError(t, err, "confirm unban")

	assert.Equal(t, 1, rec.count(domain.OpManualUnban), "one manual_unban")
	assert.Equal(t, 0, rec.count(domain.OpAccessGranted),
		"unban alone must not grant access")
}
