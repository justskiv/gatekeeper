package enforcer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/telegram"
)

// TestEnforcerCountsEveryTerminalResult is the throughput counter's contract:
// each way an action can leave the queue lands in its own bucket. Without it,
// "how much did the bot actually do this hour" has no honest answer — the row
// counts in access_actions shrink when retention reaps them, so a rate over
// those reports work that never happened.
func TestEnforcerCountsEveryTerminalResult(t *testing.T) {
	tgID := random.TGID()
	alertID := int64(42)

	linked := sendDMAction(tgID, 0)
	linked.AlertID = &alertID

	exhausted := sendDMAction(tgID, 7)
	exhausted.MaxAttempts = 8

	cases := []struct {
		name   string
		action domain.AccessAction
		tg     *fakeTelegram
		alerts *fakeAlerts
		want   ProcessedResult
	}{
		{
			name:   "delivered",
			action: sendDMAction(tgID, 0),
			tg:     &fakeTelegram{},
			alerts: &fakeAlerts{},
			want:   ResultDone,
		},
		{
			name:   "transient failure returns to the queue",
			action: sendDMAction(tgID, 0),
			tg: &fakeTelegram{sendErr: &telegram.APIError{
				Method:   "sendMessage",
				Category: telegram.ErrorCategoryTimeout,
				Err:      errors.New("request timeout"),
			}},
			alerts: &fakeAlerts{},
			want:   ResultRetried,
		},
		{
			name:   "attempt budget exhausted",
			action: exhausted,
			tg: &fakeTelegram{sendErr: &telegram.APIError{
				Method:   "sendMessage",
				Category: telegram.ErrorCategoryTimeout,
				Err:      errors.New("request timeout"),
			}},
			alerts: &fakeAlerts{},
			want:   ResultDead,
		},
		{
			name:   "resolved alert retires its notification",
			action: linked,
			tg:     &fakeTelegram{},
			alerts: &fakeAlerts{resolved: true},
			want:   ResultCancelled,
		},
		{
			// A blocked DM retires the row as `done` because nothing is owed a
			// retry, but nothing was delivered either — counting it as `done`
			// would inflate throughput with messages nobody received.
			name:   "subject blocked the bot",
			action: sendDMAction(tgID, 0),
			tg: &fakeTelegram{sendErr: &telegram.APIError{
				Method:   "sendMessage",
				Category: telegram.ErrorCategoryDMBlocked,
				Err:      errors.New("bot was blocked by the user"),
			}},
			alerts: &fakeAlerts{},
			want:   ResultNoop,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox := &fakeOutbox{action: tc.action}
			e := newTestEnforcer(outbox, tc.tg, &fakeUsers{}, tc.alerts)

			_, err := e.runOnce(context.Background())
			require.NoError(t, err, "runOnce")

			assert.Equal(t, []ProcessedCount{{
				Type:   domain.ActionSendDM,
				Result: tc.want,
				Count:  1,
			}}, e.Processed())
		})
	}
}

// TestEnforcerCountsExpectedNoopSeparately keeps "Telegram says this already
// holds" out of the delivered bucket. Approving a join request that no longer
// exists is a normal outcome, not work performed.
func TestEnforcerCountsExpectedNoopSeparately(t *testing.T) {
	tgID := random.TGID()
	resource := domain.ResourceChat
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionApproveJoin,
		TGID:           &tgID,
		Resource:       &resource,
		IdempotencyKey: "approve-join-noop",
		MaxAttempts:    8,
	}}
	tg := &fakeTelegram{
		approveErr: errors.New("Bad Request: user is already a member"),
	}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")

	assert.Equal(t, []ProcessedCount{{
		Type:   domain.ActionApproveJoin,
		Result: ResultNoop,
		Count:  1,
	}}, e.Processed())
}

// TestEnforcerDoesNotCountALostLease pins the one case where a terminal
// transition must not be counted here: the lease expired and another worker
// owns the outcome. Counting it in both places would double the throughput of
// every action that outlived its lease.
func TestEnforcerDoesNotCountALostLease(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: sendDMAction(tgID, 0), leaseLost: true}
	e := newTestEnforcer(outbox, &fakeTelegram{}, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")

	assert.Empty(t, e.Processed(),
		"the worker that still owns the lease records the outcome")
}

// TestEnforcerAccumulatesAcrossRuns proves the counter is monotonic within a
// process rather than a per-scrape snapshot.
func TestEnforcerAccumulatesAcrossRuns(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: sendDMAction(tgID, 0), repeat: true}
	e := newTestEnforcer(outbox, &fakeTelegram{}, &fakeUsers{}, &fakeAlerts{})

	for range 3 {
		_, err := e.runOnce(context.Background())
		require.NoError(t, err, "runOnce")
	}

	assert.Equal(t, []ProcessedCount{{
		Type:   domain.ActionSendDM,
		Result: ResultDone,
		Count:  3,
	}}, e.Processed())
}
