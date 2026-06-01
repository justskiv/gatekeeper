package enforcer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
)

type fakeOutbox struct {
	action     domain.AccessAction
	leased     bool
	done       bool
	dead       bool
	retryRunAt time.Time
	retryError string
	deadError  string
	inLease    *bool
}

func (o *fakeOutbox) LeaseReady(
	context.Context,
	time.Time,
	time.Duration,
) (domain.AccessAction, bool, error) {
	if o.leased {
		return domain.AccessAction{}, false, nil
	}

	o.leased = true
	if o.inLease != nil {
		*o.inLease = true
		defer func() { *o.inLease = false }()
	}

	return o.action, true, nil
}

func (o *fakeOutbox) MarkDone(context.Context, int64) error {
	o.done = true

	return nil
}

func (o *fakeOutbox) Retry(
	_ context.Context,
	_ int64,
	runAfter time.Time,
	lastError string,
) (domain.AccessAction, error) {
	o.retryRunAt = runAfter
	o.retryError = lastError

	return o.action, nil
}

func (o *fakeOutbox) MarkDead(_ context.Context, _ int64, lastError string) error {
	o.dead = true
	o.deadError = lastError

	return nil
}

type fakeTelegram struct {
	sendErr        error
	member         *models.ChatMember
	memberErr      error
	approveErr     error
	calls          []string
	onlyIfBanned   bool
	networkInLease *bool
}

func (t *fakeTelegram) SendMessage(context.Context, int64, string) error {
	t.calls = append(t.calls, "sendMessage")
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
	t.calls = append(t.calls, "sendMessageWithReplyMarkup")

	return t.sendErr
}

func (t *fakeTelegram) GetChatMember(
	context.Context,
	int64,
	int64,
) (*models.ChatMember, error) {
	t.calls = append(t.calls, "getChatMember")

	return t.member, t.memberErr
}

func (t *fakeTelegram) ApproveChatJoinRequest(context.Context, int64, int64) error {
	t.calls = append(t.calls, "approveChatJoinRequest")

	return t.approveErr
}

func (t *fakeTelegram) DeclineChatJoinRequest(context.Context, int64, int64) error {
	t.calls = append(t.calls, "declineChatJoinRequest")

	return nil
}

func (t *fakeTelegram) BanChatMember(context.Context, int64, int64) error {
	t.calls = append(t.calls, "banChatMember")

	return nil
}

func (t *fakeTelegram) UnbanChatMember(
	_ context.Context,
	_ int64,
	_ int64,
	onlyIfBanned bool,
) error {
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
}

func (a *fakeAlerts) Create(
	_ context.Context,
	alert store.AlertInput,
) (int64, error) {
	a.created = append(a.created, alert)

	return int64(len(a.created)), nil
}

func TestEnforcerRetriesRateLimitWithRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tgID := int64(42)
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
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if !ok || !outbox.retryRunAt.Equal(now.Add(17*time.Second)) {
		t.Fatalf("retry = (%v, %v), want retry_after", ok, outbox.retryRunAt)
	}
}

func TestEnforcerSendDMBlockedMarksUserAndCompletes(t *testing.T) {
	tgID := int64(43)
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

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if users.blocked != tgID || !outbox.done || outbox.retryError != "" {
		t.Fatalf("blocked=%d done=%v retry=%q, want blocked done without retry",
			users.blocked, outbox.done, outbox.retryError)
	}
}

func TestEnforcerSendDMRetryButtonUsesReplyMarkup(t *testing.T) {
	tgID := int64(53)
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

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if len(tg.calls) != 1 || tg.calls[0] != "sendMessageWithReplyMarkup" {
		t.Fatalf("calls=%v, want reply markup send", tg.calls)
	}
}

func TestEnforcerSendInviteBlockedMarksUserAndCompletes(t *testing.T) {
	tgID := int64(49)
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

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if users.blocked != tgID || !outbox.done || outbox.retryError != "" {
		t.Fatalf("blocked=%d done=%v retry=%q, want blocked done without retry",
			users.blocked, outbox.done, outbox.retryError)
	}
}

func TestEnforcerSendInviteCompletesWhenMarkSentFails(t *testing.T) {
	tgID := int64(51)
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

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if !outbox.done || outbox.retryError != "" || outbox.dead {
		t.Fatalf("done=%v retry=%q dead=%v, want done without retry",
			outbox.done, outbox.retryError, outbox.dead)
	}

	if invites.markSentCalls != 1 {
		t.Fatalf("markSentCalls=%d, want 1", invites.markSentCalls)
	}

	if len(tg.calls) != 1 || tg.calls[0] != "sendMessage" {
		t.Fatalf("calls=%v, want one sendMessage", tg.calls)
	}
}

func TestEnforcerApproveJoinExpectedNoopCompletes(t *testing.T) {
	tgID := int64(44)
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

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if !outbox.done || outbox.retryError != "" {
		t.Fatalf("done=%v retry=%q, want no-op done", outbox.done, outbox.retryError)
	}
}

func TestEnforcerChatNotFoundRetriesAction(t *testing.T) {
	tgID := int64(50)
	outbox := &fakeOutbox{action: resourceAction(domain.ActionApproveJoin, tgID)}
	tg := &fakeTelegram{
		approveErr: errors.New("bad request: chat not found"),
	}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{},
		WithClock(func() time.Time {
			return time.Unix(1_700_000_000, 0)
		}))

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if outbox.done || outbox.retryError == "" {
		t.Fatalf("done=%v retry=%q, want retry instead of no-op done",
			outbox.done, outbox.retryError)
	}
}

func TestEnforcerForbiddenActionGoesDeadWithoutRetry(t *testing.T) {
	tgID := int64(52)
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

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if !outbox.dead || outbox.done || outbox.retryError != "" {
		t.Fatalf("dead=%v done=%v retry=%q, want dead without retry",
			outbox.dead, outbox.done, outbox.retryError)
	}

	if len(alerts.created) != 1 ||
		alerts.created[0].Kind != "outbox_action_dead" {
		t.Fatalf("alerts=%+v, want one dead-action alert", alerts.created)
	}
}

func TestEnforcerSoftKickSkipsCreatorOrAdmin(t *testing.T) {
	tgID := int64(45)
	outbox := &fakeOutbox{action: resourceAction(domain.ActionSoftKick, tgID)}
	tg := &fakeTelegram{member: &models.ChatMember{
		Type: models.ChatMemberTypeAdministrator,
	}}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if !outbox.done {
		t.Fatal("soft_kick admin was not completed as no-op")
	}

	if len(tg.calls) != 1 || tg.calls[0] != "getChatMember" {
		t.Fatalf("calls = %v, want only getChatMember", tg.calls)
	}
}

func TestEnforcerSoftKickBanThenUnban(t *testing.T) {
	tgID := int64(46)
	outbox := &fakeOutbox{action: resourceAction(domain.ActionSoftKick, tgID)}
	tg := &fakeTelegram{member: &models.ChatMember{
		Type: models.ChatMemberTypeMember,
	}}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	wantCalls := []string{"getChatMember", "banChatMember", "unbanChatMember"}
	if !equalStrings(tg.calls, wantCalls) || !tg.onlyIfBanned || !outbox.done {
		t.Fatalf("calls=%v only_if_banned=%v done=%v",
			tg.calls, tg.onlyIfBanned, outbox.done)
	}
}

func TestEnforcerDeadActionCreatesAlert(t *testing.T) {
	tgID := int64(47)
	outbox := &fakeOutbox{action: sendDMAction(tgID, 7)}
	alerts := &fakeAlerts{}
	tg := &fakeTelegram{sendErr: errors.New("network down")}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, alerts)

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if !outbox.dead || len(alerts.created) != 1 ||
		alerts.created[0].Kind != "outbox_action_dead" {
		t.Fatalf("dead=%v alerts=%+v, want one dead alert",
			outbox.dead, alerts.created)
	}
}

func TestEnforcerNetworkRunsAfterLeaseReturns(t *testing.T) {
	tgID := int64(48)
	inLease := false
	outbox := &fakeOutbox{
		action:  sendDMAction(tgID, 0),
		inLease: &inLease,
	}
	tg := &fakeTelegram{networkInLease: &inLease}
	e := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if !outbox.done {
		t.Fatal("action was not completed")
	}
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

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce ensure_invite: %v", err)
	}

	if invites.ensureCalls != 1 ||
		len(limiter.calls) != 1 ||
		limiter.calls[0] != (limiterCall{kind: requestKindDefault, chatID: -1001}) {
		t.Fatalf("ensure calls=%d limiter=%+v", invites.ensureCalls, limiter.calls)
	}

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

	if _, err := e.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce revoke_invite: %v", err)
	}

	if invites.revokeCalls != 1 ||
		len(limiter.calls) != 1 ||
		limiter.calls[0] != (limiterCall{kind: requestKindDefault, chatID: -1001}) {
		t.Fatalf("revoke calls=%d limiter=%+v", invites.revokeCalls, limiter.calls)
	}
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

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
