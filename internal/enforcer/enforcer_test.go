package enforcer

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

type fakeOutbox struct {
	mu         sync.Mutex
	action     domain.AccessAction
	leased     bool
	leaseCount int
	leaseErr   error
	// leaseErrTimes limits leaseErr to the first N calls, so a test can let a
	// worker crash a bounded number of times and then recover. -1 means always.
	leaseErrTimes int
	done          bool
	dead          bool
	cancelled     bool
	released      bool
	retryRunAt    time.Time
	retryError    string
	deadError     string
	cancelReason  string
	inLease       *bool

	// repeat makes LeaseReady hand out the same action forever, so worker-loop
	// tests can observe repeated failures instead of a single pass.
	repeat bool
	// leaseLost makes terminal transitions report a reclaimed lease.
	leaseLost bool
}

func (o *fakeOutbox) LeaseReady(
	context.Context,
	time.Time,
	time.Duration,
) (domain.AccessAction, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.leaseErr != nil && o.leaseErrTimes != 0 {
		if o.leaseErrTimes > 0 {
			o.leaseErrTimes--
		}

		return domain.AccessAction{}, false, o.leaseErr
	}

	if o.leased && !o.repeat {
		return domain.AccessAction{}, false, nil
	}

	o.leased = true
	o.leaseCount++

	if o.inLease != nil {
		*o.inLease = true
		defer func() { *o.inLease = false }()
	}

	return o.action, true, nil
}

func (o *fakeOutbox) MarkDone(context.Context, int64, time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.leaseLost {
		return store.ErrLeaseLost
	}

	o.done = true

	return nil
}

func (o *fakeOutbox) Retry(
	_ context.Context,
	_ int64,
	_ time.Time,
	runAfter time.Time,
	lastError string,
) (domain.AccessAction, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.leaseLost {
		return domain.AccessAction{}, store.ErrLeaseLost
	}

	o.retryRunAt = runAfter
	o.retryError = lastError

	return o.action, nil
}

func (o *fakeOutbox) MarkDead(
	_ context.Context,
	_ int64,
	_ time.Time,
	lastError string,
) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.leaseLost {
		return store.ErrLeaseLost
	}

	o.dead = true
	o.deadError = lastError

	return nil
}

func (o *fakeOutbox) MarkCancelled(
	_ context.Context,
	_ int64,
	_ time.Time,
	reason string,
) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.leaseLost {
		return store.ErrLeaseLost
	}

	o.cancelled = true
	o.cancelReason = reason

	return nil
}

func (o *fakeOutbox) ReleaseLease(context.Context, int64, time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.released = true

	return nil
}

func (o *fakeOutbox) snapshot() fakeOutboxState {
	o.mu.Lock()
	defer o.mu.Unlock()

	return fakeOutboxState{
		leaseCount: o.leaseCount,
		done:       o.done,
		dead:       o.dead,
		released:   o.released,
		retryRunAt: o.retryRunAt,
		retryError: o.retryError,
	}
}

type fakeOutboxState struct {
	leaseCount int
	done       bool
	dead       bool
	released   bool
	retryRunAt time.Time
	retryError string
}

// fakeTelegram is driven by the whole worker pool in the supervision and
// liveness tests, so every field access is guarded: several workers call the
// same fake concurrently and an unguarded `calls` append is a data race.
type fakeTelegram struct {
	mu              sync.Mutex
	sendErr         error
	member          *models.ChatMember
	memberErr       error
	approveErr      error
	calls           []string
	parseMode       string
	onlyIfBanned    bool
	networkInLease  *bool
	editText        string
	editChatID      int64
	editMessageID   int
	editReplyMarkup models.ReplyMarkup
}

func (t *fakeTelegram) SendMessage(context.Context, int64, string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "sendMessage")
	if t.networkInLease != nil && *t.networkInLease {
		return errors.New("network called while lease was active")
	}

	return t.sendErr
}

func (t *fakeTelegram) SendFormattedMessage(
	_ context.Context,
	_ int64,
	_ string,
	parseMode string,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "sendFormattedMessage")
	t.parseMode = parseMode

	if t.networkInLease != nil && *t.networkInLease {
		return errors.New("network called while lease was active")
	}

	return t.sendErr
}

func (t *fakeTelegram) EditMessageText(
	_ context.Context,
	chatID int64,
	messageID int,
	text string,
	replyMarkup models.ReplyMarkup,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "editMessageText")
	t.editChatID = chatID
	t.editMessageID = messageID
	t.editText = text
	t.editReplyMarkup = replyMarkup

	if t.networkInLease != nil && *t.networkInLease {
		return errors.New("network called while lease was active")
	}

	return t.sendErr
}

func (t *fakeTelegram) SendMessageWithReplyMarkup(
	context.Context,
	int64,
	string,
	models.ReplyMarkup,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "sendMessageWithReplyMarkup")

	return t.sendErr
}

func (t *fakeTelegram) SendFormattedMessageWithReplyMarkup(
	_ context.Context,
	_ int64,
	_ string,
	parseMode string,
	_ models.ReplyMarkup,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "sendFormattedMessageWithReplyMarkup")
	t.parseMode = parseMode

	return t.sendErr
}

func (t *fakeTelegram) GetChatMember(
	context.Context,
	int64,
	int64,
) (*models.ChatMember, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "getChatMember")

	return t.member, t.memberErr
}

func (t *fakeTelegram) ApproveChatJoinRequest(context.Context, int64, int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "approveChatJoinRequest")

	return t.approveErr
}

func (t *fakeTelegram) DeclineChatJoinRequest(context.Context, int64, int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "declineChatJoinRequest")

	return nil
}

func (t *fakeTelegram) BanChatMember(context.Context, int64, int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "banChatMember")

	return nil
}

func (t *fakeTelegram) UnbanChatMember(
	_ context.Context,
	_ int64,
	_ int64,
	onlyIfBanned bool,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls = append(t.calls, "unbanChatMember")
	t.onlyIfBanned = onlyIfBanned

	return nil
}

type fakeInvites struct {
	markSentErr   error
	markSentCalls int
}

func (*fakeInvites) Ensure(
	context.Context,
	invite.EnsureRequest,
) (domain.InviteLink, error) {
	return domain.InviteLink{InviteLink: "https://t.me/+invite"}, nil
}

func (*fakeInvites) Revoke(context.Context, domain.InviteLink) error {
	return nil
}

func (i *fakeInvites) MarkSent(context.Context, domain.InviteLink) error {
	i.markSentCalls++

	return i.markSentErr
}

type recordingInvites struct {
	ensureCalls int
	revokeCalls int
}

func (i *recordingInvites) Ensure(
	context.Context,
	invite.EnsureRequest,
) (domain.InviteLink, error) {
	i.ensureCalls++

	return domain.InviteLink{InviteLink: "https://t.me/+invite"}, nil
}

func (i *recordingInvites) Revoke(context.Context, domain.InviteLink) error {
	i.revokeCalls++

	return nil
}

func (*recordingInvites) MarkSent(context.Context, domain.InviteLink) error {
	return nil
}

type recordingLimiter struct {
	calls []limiterCall
}

type limiterCall struct {
	kind   requestKind
	chatID int64
}

func (l *recordingLimiter) Wait(
	_ context.Context,
	kind requestKind,
	chatID int64,
) error {
	l.calls = append(l.calls, limiterCall{kind: kind, chatID: chatID})

	return nil
}

type fakeUsers struct {
	blocked int64
}

func (u *fakeUsers) SetDMState(_ context.Context, tgID int64, state domain.DMState) error {
	if state == domain.DMBlocked {
		u.blocked = tgID
	}

	return nil
}

func (u *fakeUsers) Upsert(_ context.Context, user domain.User) error {
	u.blocked = user.TGID

	return nil
}

type fakeAlerts struct {
	created []store.AlertInput

	// resolved makes IsOpen answer "no" for every alert, standing in for an
	// alert that auto-resolved while its notification sat in the queue.
	resolved bool
	// isOpenErr makes the pre-execute check fail, so a test can prove a read
	// failure does not swallow the notification.
	isOpenErr error
	// isOpenCalls counts the pre-execute checks, so a test can prove the
	// check is skipped for an action that carries no alert link.
	isOpenCalls int
}

func (a *fakeAlerts) Create(
	_ context.Context,
	alert store.AlertInput,
) (int64, error) {
	a.created = append(a.created, alert)

	return int64(len(a.created)), nil
}

func (a *fakeAlerts) IsOpen(_ context.Context, _ int64) (bool, error) {
	a.isOpenCalls++

	if a.isOpenErr != nil {
		return false, a.isOpenErr
	}

	return !a.resolved, nil
}

func TestEnforcerRetriesRateLimitWithRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tgID := random.TGID()
	outbox := &fakeOutbox{action: sendDMAction(tgID, 0)}
	tg := &fakeTelegram{
		sendErr: &telegram.APIError{
			Method:     "sendMessage",
			Category:   telegram.ErrorCategoryRateLimited,
			RetryAfter: 17,
			Err:        errors.New("too many requests"),
		},
	}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{},
		WithClock(func() time.Time { return now }))

	ok, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, ok, "rate-limited action must be retried")
	assert.True(t, outbox.retryRunAt.Equal(now.Add(17*time.Second)),
		"retry must honor retry_after")
}

func TestEnforcerSendDMBlockedMarksUserAndCompletes(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: sendDMAction(tgID, 0)}
	users := &fakeUsers{}
	tg := &fakeTelegram{
		sendErr: &telegram.APIError{
			Method:   "sendMessage",
			Category: telegram.ErrorCategoryDMBlocked,
			Err:      errors.New("bot was blocked by the user"),
		},
	}
	e := newTestEnforcer(outbox, tg, users, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.Equal(t, tgID, users.blocked, "blocked user must be recorded")
	assert.True(t, outbox.done, "action must complete")
	assert.Empty(t, outbox.retryError, "blocked DM must not retry")
}

func TestEnforcerSendDMRetryButtonUsesReplyMarkup(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "send-dm-retry",
		PayloadJSON:    []byte(`{"text":"try later","retry_button":true}`),
		MaxAttempts:    8,
	}}
	tg := &fakeTelegram{}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	require.Len(t, tg.calls, 1, "want one telegram call")
	assert.Equal(t, "sendMessageWithReplyMarkup", tg.calls[0])
}

func TestEnforcerFormattedDMPreservesParseMode(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "send-dm-formatted",
		PayloadJSON: []byte(
			`{"text":"<b>hello</b>","parse_mode":"HTML"}`),
		MaxAttempts: 8,
	}}
	tg := &fakeTelegram{}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	require.Len(t, tg.calls, 1, "want one telegram call")
	assert.Equal(t, "sendFormattedMessage", tg.calls[0])
	assert.Equal(t, messages.ParseModeHTML, tg.parseMode)
}

func TestEnforcerFormattedDMRetryButtonPreservesParseMode(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "send-dm-formatted-retry",
		PayloadJSON: []byte(
			`{"text":"<b>try later</b>","parse_mode":"HTML","retry_button":true}`),
		MaxAttempts: 8,
	}}
	tg := &fakeTelegram{}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	require.Len(t, tg.calls, 1, "want one telegram call")
	assert.Equal(t, "sendFormattedMessageWithReplyMarkup", tg.calls[0])
	assert.Equal(t, messages.ParseModeHTML, tg.parseMode)
}

func TestEnforcerLegacyDMPayloadWithoutParseModeStaysPlain(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "send-dm-legacy",
		PayloadJSON:    []byte(`{"text":"Use /whois <tg_id|@username>"}`),
		MaxAttempts:    8,
	}}
	tg := &fakeTelegram{}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	require.Len(t, tg.calls, 1, "want one telegram call")
	assert.Equal(t, "sendMessage", tg.calls[0])
	assert.Empty(t, tg.parseMode, "legacy payload must stay plain")
}

func TestEnforcerFormattedInvitePreservesParseMode(t *testing.T) {
	tgID := random.TGID()
	action := resourceAction(domain.ActionSendInvite, tgID)
	action.PayloadJSON = []byte(`{"text":"<b>invite</b>","parse_mode":"HTML"}`)
	outbox := &fakeOutbox{action: action}
	tg := &fakeTelegram{}
	invites := &fakeInvites{}
	e := New(Stores{
		Outbox: outbox,
		Users:  &fakeUsers{},
		Alerts: &fakeAlerts{},
	}, tg, invites, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(noopLimiter{}))

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	require.Len(t, tg.calls, 1, "want one telegram call")
	assert.Equal(t, "sendFormattedMessage", tg.calls[0])
	assert.Equal(t, messages.ParseModeHTML, tg.parseMode)
}

func TestEnforcerSendInviteBlockedMarksUserAndCompletes(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: resourceAction(domain.ActionSendInvite, tgID)}
	users := &fakeUsers{}
	tg := &fakeTelegram{
		sendErr: &telegram.APIError{
			Method:   "sendMessage",
			Category: telegram.ErrorCategoryDMBlocked,
			Err:      errors.New("bot was blocked by the user"),
		},
	}
	e := newTestEnforcer(outbox, tg, users, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.Equal(t, tgID, users.blocked, "blocked user must be recorded")
	assert.True(t, outbox.done, "action must complete")
	assert.Empty(t, outbox.retryError, "blocked DM must not retry")
}

func TestEnforcerSendInviteCompletesWhenMarkSentFails(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: resourceAction(domain.ActionSendInvite, tgID)}
	tg := &fakeTelegram{}
	invites := &fakeInvites{markSentErr: errors.New("mark sent failed")}
	e := New(Stores{
		Outbox: outbox,
		Users:  &fakeUsers{},
		Alerts: &fakeAlerts{},
	}, tg, invites, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(noopLimiter{}))

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, outbox.done, "action must complete")
	assert.Empty(t, outbox.retryError, "mark-sent failure must not retry")
	assert.False(t, outbox.dead, "mark-sent failure must not go dead")
	assert.Equal(t, 1, invites.markSentCalls, "mark sent must be attempted once")
	require.Len(t, tg.calls, 1, "want one telegram call")
	assert.Equal(t, "sendMessage", tg.calls[0])
}

func TestEnforcerApproveJoinExpectedNoopCompletes(t *testing.T) {
	tgID := random.TGID()
	resource := domain.ResourceChat
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionApproveJoin,
		TGID:           &tgID,
		Resource:       &resource,
		IdempotencyKey: "approve",
		PayloadJSON:    []byte(`{}`),
		MaxAttempts:    8,
	}}
	tg := &fakeTelegram{
		approveErr: errors.New("bad request: request not found"),
	}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, outbox.done, "expected no-op must complete")
	assert.Empty(t, outbox.retryError, "no-op must not retry")
}

func TestEnforcerChatNotFoundRetriesAction(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: resourceAction(domain.ActionApproveJoin, tgID)}
	tg := &fakeTelegram{
		approveErr: errors.New("bad request: chat not found"),
	}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{},
		WithClock(func() time.Time {
			return time.Unix(1_700_000_000, 0)
		}))

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.False(t, outbox.done, "chat-not-found must not complete as no-op")
	assert.NotEmpty(t, outbox.retryError, "chat-not-found must retry")
}

func TestEnforcerForbiddenActionGoesDeadWithoutRetry(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: resourceAction(domain.ActionApproveJoin, tgID)}
	alerts := &fakeAlerts{}
	tg := &fakeTelegram{
		approveErr: &telegram.APIError{
			Method:   "approveChatJoinRequest",
			Category: telegram.ErrorCategoryForbidden,
			Err:      errors.New("forbidden"),
		},
	}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, alerts)

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, outbox.dead, "forbidden action must go dead")
	assert.False(t, outbox.done, "forbidden action must not complete")
	assert.Empty(t, outbox.retryError, "forbidden action must not retry")
	require.Len(t, alerts.created, 1, "want one dead-action alert")
	assert.Equal(t, "outbox_action_dead", alerts.created[0].Kind)
}

func TestEnforcerSoftKickSkipsCreatorOrAdmin(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: resourceAction(domain.ActionSoftKick, tgID)}
	tg := &fakeTelegram{member: &models.ChatMember{
		Type: models.ChatMemberTypeAdministrator,
	}}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, outbox.done, "soft_kick admin must complete as no-op")
	require.Len(t, tg.calls, 1, "want only getChatMember")
	assert.Equal(t, "getChatMember", tg.calls[0])
}

func TestEnforcerSoftKickBanThenUnban(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: resourceAction(domain.ActionSoftKick, tgID)}
	tg := &fakeTelegram{member: &models.ChatMember{
		Type: models.ChatMemberTypeMember,
	}}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.Equal(t,
		[]string{"getChatMember", "banChatMember", "unbanChatMember"}, tg.calls)
	assert.True(t, tg.onlyIfBanned, "unban must use only_if_banned")
	assert.True(t, outbox.done, "soft_kick must complete")
}

func TestEnforcerHardBanSkipsProtectedAdmin(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: resourceAction(domain.ActionHardBan, tgID)}
	tg := &fakeTelegram{member: &models.ChatMember{
		Type: models.ChatMemberTypeAdministrator,
	}}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, outbox.done, "hard_ban admin must complete as no-op")
	require.Len(t, tg.calls, 1, "want only getChatMember")
	assert.Equal(t, "getChatMember", tg.calls[0])
}

func TestEnforcerUnbanUsesOnlyIfBanned(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: resourceAction(domain.ActionUnban, tgID)}
	tg := &fakeTelegram{}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, outbox.done, "unban must complete")
	assert.True(t, tg.onlyIfBanned, "unban must use only_if_banned")
}

func TestEnforcerVerifyMemberSourceInactiveSchedulesRevocation(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	outbox := store.NewOutbox(db)

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
	}, tg, &fakeInvites{}, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(noopLimiter{}))

	_, err = e.runOnce(ctx)
	require.NoError(t, err, "runOnce")

	_, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, tgID, domain.PlatformBoosty)
	require.NoError(t, err)
	assert.False(t, ok, "inactive source must expire the subscription")

	_, ok, err = store.NewRevocations(db).Get(ctx, tgID)
	require.NoError(t, err)
	assert.True(t, ok, "a pending revocation must be scheduled")

	assert.Zero(t, countStoreActions(t, db, domain.ActionSoftKick),
		"grace must not soft kick immediately")
}

func TestEnforcerVerifyMemberUnknownLeavesAccessUntouched(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	outbox := store.NewOutbox(db)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, err := store.NewSubscriptions(db).UpsertActive(ctx, domain.Subscription{
		TGID:       tgID,
		Platform:   domain.PlatformBoosty,
		StartedAt:  time.Now().Add(-time.Hour),
		LastSignal: "event",
	})
	require.NoError(t, err, "upsert subscription")

	enqueueVerifyMember(t, ctx, outbox, tgID, nil,
		[]byte(`{"kind":"source","platform":"boosty","chat_id":-1001}`))

	tg := &fakeTelegram{memberErr: errors.New("telegram timeout")}
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
	}, tg, &fakeInvites{}, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(noopLimiter{}))

	_, err = e.runOnce(ctx)
	require.NoError(t, err, "runOnce")

	_, ok, err := store.NewSubscriptions(db).GetActive(
		ctx, tgID, domain.PlatformBoosty)
	require.NoError(t, err)
	assert.True(t, ok, "unknown verdict must preserve the subscription")

	_, ok, err = store.NewRevocations(db).Get(ctx, tgID)
	require.NoError(t, err)
	assert.False(t, ok, "unknown verdict must not schedule a revocation")
}

func TestEnforcerVerifyMemberClubPreservesRevokedGrant(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	outbox := store.NewOutbox(db)
	resource := domain.ResourceChat

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	require.NoError(t, store.NewGrants(db).Upsert(ctx, domain.AccessGrant{
		TGID:          tgID,
		Resource:      resource,
		State:         domain.GrantRevoked,
		AdmittedBy:    "bot",
		RevokedAt:     ptrTime(time.Now().Add(-time.Hour)),
		RevokedReason: "expired",
	}), "upsert revoked grant")

	enqueueVerifyMember(t, ctx, outbox, tgID, &resource,
		[]byte(`{"kind":"club","resource":"chat","chat_id":-1001}`))

	tg := &fakeTelegram{member: &models.ChatMember{
		Type: models.ChatMemberTypeMember,
	}}
	e := New(Stores{
		Outbox: outbox,
		Users:  store.NewUsers(db),
		Grants: store.NewGrants(db),
		Alerts: store.NewAlerts(db),
	}, tg, &fakeInvites{}, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(noopLimiter{}))

	_, err := e.runOnce(ctx)
	require.NoError(t, err, "runOnce")

	grant, err := store.NewGrants(db).Get(ctx, tgID, resource)
	require.NoError(t, err, "get grant")
	assert.Equal(t, domain.GrantRevoked, grant.State,
		"club membership must not resurrect a revoked grant")
}

// TestEnforcerCancelsNotificationWhoseAlertResolved covers the narrow window
// resolve-time cancellation cannot reach: the row was already leased when the
// alert resolved. Delivering it would tell the owner about a problem that is
// over — the 2026-08-16 incident in miniature.
func TestEnforcerCancelsNotificationWhoseAlertResolved(t *testing.T) {
	tgID := random.TGID()
	alertID := int64(42)
	action := sendDMAction(tgID, 0)
	action.AlertID = &alertID
	outbox := &fakeOutbox{action: action}
	tg := &fakeTelegram{}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{resolved: true})

	ok, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, ok, "the leased action still counts as work")
	assert.Empty(t, tg.calls, "a resolved alert's notification must not be sent")
	assert.True(t, outbox.cancelled, "the action must be cancelled")
	assert.False(t, outbox.done, "cancelling is not delivering")
	assert.False(t, outbox.dead, "cancelling is not failing")
}

// TestEnforcerDeliversWhenAlertStateIsUnreadable pins the fail-open side of the
// guard: a database read failure must never be what swallows a notification.
func TestEnforcerDeliversWhenAlertStateIsUnreadable(t *testing.T) {
	tgID := random.TGID()
	alertID := int64(42)
	action := sendDMAction(tgID, 0)
	action.AlertID = &alertID
	outbox := &fakeOutbox{action: action}
	tg := &fakeTelegram{}
	e := newTestEnforcer(outbox, tg, &fakeUsers{},
		&fakeAlerts{isOpenErr: errors.New("database is locked")})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.Equal(t, []string{"sendMessage"}, tg.calls,
		"an unreadable alert state must not suppress the message")
	assert.True(t, outbox.done, "the action completes normally")
	assert.False(t, outbox.cancelled, "nothing proved the alert resolved")
}

// TestEnforcerSkipsAlertCheckForUnlinkedAction keeps the guard off the hot
// path: almost every action carries no alert, and none of them should pay for
// an extra read.
func TestEnforcerSkipsAlertCheckForUnlinkedAction(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: sendDMAction(tgID, 0)}
	alerts := &fakeAlerts{resolved: true}
	tg := &fakeTelegram{}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, alerts)

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.Zero(t, alerts.isOpenCalls, "an unlinked action must not be checked")
	assert.True(t, outbox.done, "the action is delivered as usual")
}

func TestEnforcerDeadActionCreatesAlert(t *testing.T) {
	tgID := random.TGID()
	outbox := &fakeOutbox{action: sendDMAction(tgID, 7)}
	alerts := &fakeAlerts{}
	tg := &fakeTelegram{sendErr: errors.New("network down")}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, alerts)

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, outbox.dead, "exhausted action must go dead")
	require.Len(t, alerts.created, 1, "want one dead-action alert")
	assert.Equal(t, "outbox_action_dead", alerts.created[0].Kind)
}

func TestEnforcerNetworkRunsAfterLeaseReturns(t *testing.T) {
	tgID := random.TGID()
	inLease := false
	outbox := &fakeOutbox{
		action:  sendDMAction(tgID, 0),
		inLease: &inLease,
	}
	tg := &fakeTelegram{networkInLease: &inLease}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce")
	assert.True(t, outbox.done, "action must complete after the lease returns")
}

func TestEnforcerLimitsInviteOperations(t *testing.T) {
	limiter := &recordingLimiter{}
	invites := &recordingInvites{}
	resource := domain.ResourceChat
	outbox := &fakeOutbox{action: domain.AccessAction{
		ID:             1,
		Type:           domain.ActionEnsureInvite,
		Resource:       &resource,
		IdempotencyKey: "ensure",
		PayloadJSON:    []byte(`{}`),
		MaxAttempts:    8,
	}}
	e := New(Stores{
		Outbox: outbox,
		Users:  &fakeUsers{},
		Alerts: &fakeAlerts{},
	}, &fakeTelegram{}, invites, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(limiter))

	_, err := e.runOnce(context.Background())
	require.NoError(t, err, "runOnce ensure_invite")
	assert.Equal(t, 1, invites.ensureCalls, "ensure must be called once")
	assert.Equal(t,
		[]limiterCall{{kind: requestKindDefault, chatID: -1001}}, limiter.calls,
		"ensure must be rate limited on the club chat")

	limiter.calls = nil
	invites.revokeCalls = 0
	outbox.action = domain.AccessAction{
		ID:             2,
		Type:           domain.ActionRevokeInvite,
		Resource:       &resource,
		IdempotencyKey: "revoke",
		PayloadJSON: []byte(
			`{"invite_link_id":1,"invite_link":"https://t.me/+invite"}`),
		MaxAttempts: 8,
	}
	outbox.leased = false
	outbox.done = false

	_, err = e.runOnce(context.Background())
	require.NoError(t, err, "runOnce revoke_invite")
	assert.Equal(t, 1, invites.revokeCalls, "revoke must be called once")
	assert.Equal(t,
		[]limiterCall{{kind: requestKindDefault, chatID: -1001}}, limiter.calls,
		"revoke must be rate limited on the club chat")
}

func enqueueVerifyMember(
	t *testing.T,
	ctx context.Context,
	outbox *store.Outbox,
	tgID int64,
	resource *domain.Resource,
	payload []byte,
) {
	t.Helper()

	_, _, err := outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     domain.ActionVerifyMember,
		TGID:     &tgID,
		Resource: resource,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionVerifyMember, &tgID, resource, string(payload)),
		PayloadJSON: payload,
	})
	require.NoError(t, err, "enqueue verify_member")
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func countStoreActions(
	t *testing.T,
	db *sql.DB,
	actionType domain.ActionType,
) int {
	t.Helper()

	var got int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM access_actions WHERE action_type = ?`,
		string(actionType)).Scan(&got), "count actions")

	return got
}

func newTestEnforcer(
	outbox *fakeOutbox,
	tg *fakeTelegram,
	users *fakeUsers,
	alerts *fakeAlerts,
	opts ...Option,
) *Enforcer {
	allOpts := append([]Option{WithRateLimiter(noopLimiter{})}, opts...)

	return New(Stores{
		Outbox: outbox,
		Users:  users,
		Alerts: alerts,
	}, tg, &fakeInvites{}, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, allOpts...)
}

func sendDMAction(tgID int64, attempts int) domain.AccessAction {
	return domain.AccessAction{
		ID:             1,
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "send-dm",
		PayloadJSON:    []byte(`{"text":"hello"}`),
		Attempts:       attempts,
		MaxAttempts:    8,
	}
}

func resourceAction(actionType domain.ActionType, tgID int64) domain.AccessAction {
	resource := domain.ResourceChat

	return domain.AccessAction{
		ID:             1,
		Type:           actionType,
		TGID:           &tgID,
		Resource:       &resource,
		IdempotencyKey: string(actionType),
		PayloadJSON:    []byte(`{}`),
		MaxAttempts:    8,
	}
}
