package enforcer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/telegram"
)

// A 403 sending to a negative (group/feed) chat_id is a lost-posting-rights
// failure, not the subject blocking DMs: it must go dead + alert and must not
// mutate the subject's dm_state. This also covers the latent ADMIN_LOG_CHAT_ID
// alert-delivery bug.
func TestEnforcerGroupFeedForbiddenGoesDeadWithoutBlockingSubject(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "opevent-feed",
		PayloadJSON: []byte(
			`{"text":"event","parse_mode":"HTML","chat_id":-1006666666666}`),
		MaxAttempts: 8,
	}}
	users := &fakeUsers{}
	alerts := &fakeAlerts{}
	tg := &fakeTelegram{sendErr: &telegram.APIError{
		Method:   "sendMessage",
		Category: telegram.ErrorCategoryDMBlocked,
		Err:      errors.New("forbidden: bot is not a member of the chat"),
	}}
	e := newTestEnforcer(outbox, tg, users, alerts)

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")

	assert.True(t, outbox.dead, "group-target 403 must go dead")
	assert.False(t, outbox.done, "group-target 403 must not count as delivered")
	assert.Zero(t, users.blocked, "subject must not be marked dm_blocked")
	require.Len(t, alerts.created, 1, "dead action must raise an alert")
	assert.Equal(t, "outbox_action_dead", alerts.created[0].Kind)
}

// A 403 sending to a positive user chat_id (e.g. the user_chat_id grant DM
// from a join-request approval) is the subject blocking the bot: it must mark
// dm_state blocked and complete without retry, never going dead.
func TestEnforcerPositiveChatIDForbiddenBlocksSubject(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "grant-dm",
		PayloadJSON: []byte(
			fmt.Sprintf(`{"text":"granted","chat_id":%d}`, tgID)),
		MaxAttempts: 8,
	}}
	users := &fakeUsers{}
	alerts := &fakeAlerts{}
	tg := &fakeTelegram{sendErr: &telegram.APIError{
		Method:   "sendMessage",
		Category: telegram.ErrorCategoryDMBlocked,
		Err:      errors.New("bot was blocked by the user"),
	}}
	e := newTestEnforcer(outbox, tg, users, alerts)

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")

	assert.Equal(t, tgID, users.blocked, "subject must be marked dm_blocked")
	assert.True(t, outbox.done, "blocked user DM must complete")
	assert.False(t, outbox.dead, "blocked user DM must not go dead")
	assert.Empty(t, alerts.created, "blocked user DM must not raise an alert")
}
