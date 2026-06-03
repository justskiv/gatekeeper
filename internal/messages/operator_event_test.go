package messages

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
)

func TestRenderOperatorEventUnknownKindIsBuildError(t *testing.T) {
	_, err := RenderOperatorEvent(domain.OperatorEvent{Kind: "nope"})
	require.Error(t, err, "unknown kind must be a feed-build error")
}

func TestRenderOperatorEventEscapesDynamicFields(t *testing.T) {
	out, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind:         domain.OpManualRevoke,
		TGID:         12345,
		Username:     `ev<il>`,
		ManualReason: `"><b>owned</b>&`,
	})
	require.NoError(t, err)

	assert.NotContains(t, out, "<b>owned</b>",
		"injected markup must not survive as live HTML")
	assert.Contains(t, out, "owned", "the reason text must still be shown")
	assert.Contains(t, out, "&lt;b&gt;owned&lt;/b&gt;",
		"markup must be escaped as text")
	assert.NotContains(t, out, "ev<il>", "username markup must be escaped")
}

func TestRenderOperatorEventResourceLabelsDiffer(t *testing.T) {
	chat := domain.ResourceChat
	channel := domain.ResourceChannel

	chatOut, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind: domain.OpClubChatJoined, TGID: 1, Resource: &chat,
		Method: domain.AdmissionBotLink,
	})
	require.NoError(t, err)

	chanOut, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind: domain.OpClubChannelSubscribed, TGID: 1, Resource: &channel,
		Method: domain.AdmissionBotLink,
	})
	require.NoError(t, err)

	assert.Contains(t, chatOut, "чат")
	assert.Contains(t, chanOut, "канал")
	assert.NotEqual(t, chatOut, chanOut,
		"chat join and channel subscribe must read differently")
}

func TestRenderOperatorEventGrantedListsAllActiveSources(t *testing.T) {
	out, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind:          domain.OpAccessGranted,
		TGID:          1,
		ActiveSources: []domain.Platform{domain.PlatformBoosty, domain.PlatformTribute},
		Method:        domain.AdmissionBotLink,
		Resources:     []domain.Resource{domain.ResourceChat, domain.ResourceChannel},
	})
	require.NoError(t, err)

	assert.Contains(t, out, "Boosty")
	assert.Contains(t, out, "Tribute")
}

func TestRenderOperatorEventSourceShowsProviderEvent(t *testing.T) {
	out, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind:          domain.OpSourceSubscriptionActivated,
		TGID:          1,
		Platform:      domain.PlatformTribute,
		ProviderEvent: "new_subscription",
		Tier:          "vip",
	})
	require.NoError(t, err)

	assert.Contains(t, out, "new_subscription",
		"provider event name must be shown when available")
	assert.Contains(t, out, "vip")
}

func TestRenderOperatorEventLostShowsSpecificReason(t *testing.T) {
	out, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind:      domain.OpAccessLost,
		TGID:      1,
		Resources: []domain.Resource{domain.ResourceChat},
		Reason:    "hard_ban_cleanup",
	})
	require.NoError(t, err)
	assert.Contains(t, out, "hard_ban_cleanup",
		"a specific revocation reason must reach the feed")

	generic, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind:      domain.OpAccessLost,
		TGID:      1,
		Resources: []domain.Resource{domain.ResourceChat},
		Reason:    "inactive",
	})
	require.NoError(t, err)
	assert.NotContains(t, generic, "inactive",
		"the generic 'inactive' token must not leak as a code reason")
}

func TestRenderOperatorEventGrantedShowsManualReason(t *testing.T) {
	out, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind:          domain.OpAccessGranted,
		TGID:          1,
		Method:        domain.AdmissionAdmin,
		ActiveSources: []domain.Platform{domain.PlatformManual},
		ManualReason:  "comp friend",
	})
	require.NoError(t, err)

	assert.Contains(t, out, "comp friend",
		"manual access reason must be shown when available")
}

func TestRenderOperatorEventExternalActorIsOptionalAndSafe(t *testing.T) {
	chat := domain.ResourceChat

	noActor, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind: domain.OpClubChatJoined, TGID: 1, Resource: &chat,
		Method: domain.AdmissionExternal,
	})
	require.NoError(t, err)
	assert.Contains(t, noActor, "внешний",
		"external join without actor must not name an admin")

	withActor, err := RenderOperatorEvent(domain.OperatorEvent{
		Kind: domain.OpClubChatJoined, TGID: 1, Resource: &chat,
		Method: domain.AdmissionExternal, ActorLabel: `<b>adm</b>`,
	})
	require.NoError(t, err)
	assert.NotContains(t, withActor, "<b>adm</b>",
		"actor label markup must be escaped")
	assert.Contains(t, withActor, "adm")
}

// The feed must never leak a managed-resource chat id: the typed model carries
// no such field, so the renderer cannot emit one. This guards the invariant.
func TestRenderOperatorEventDoesNotLeakChatID(t *testing.T) {
	chat := domain.ResourceChat
	expiry := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	events := []domain.OperatorEvent{
		{
			Kind: domain.OpClubChatJoined, TGID: 777,
			Resource: &chat, Method: domain.AdmissionBotLink,
		},
		{
			Kind: domain.OpSourceSubscriptionActivated, TGID: 777,
			Platform: domain.PlatformBoosty, ExpiresAt: &expiry,
		},
		{Kind: domain.OpBannedJoinAttempt, TGID: 777, Resource: &chat},
	}

	for _, ev := range events {
		out, err := RenderOperatorEvent(ev)
		require.NoError(t, err)
		assert.NotContains(t, out, "-100",
			"rendered event must not contain a raw negative chat id")
	}
}
