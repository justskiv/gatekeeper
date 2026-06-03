package enforcer

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/source"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
)

const (
	defaultWorkers       = 2
	defaultLeaseDuration = 2 * time.Minute
	defaultIdleDelay     = 250 * time.Millisecond
	defaultRetryBackoff  = 5 * time.Second
)

// Config controls worker execution and resource mapping.
type Config struct {
	Workers       int
	LeaseDuration time.Duration
	IdleDelay     time.Duration
	ClubChatID    int64
	ClubChannelID int64
}

// Enforcer leases and executes durable Telegram actions.
type Enforcer struct {
	stores  Stores
	tg      TelegramClient
	invites InviteService
	cfg     Config
	logger  *slog.Logger
	limiter RateLimiter
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
}

// Option configures Enforcer.
type Option func(*Enforcer)

// WithLogger sets the worker logger.
func WithLogger(logger *slog.Logger) Option {
	return func(e *Enforcer) {
		if logger != nil {
			e.logger = logger
		}
	}
}

// WithRateLimiter replaces the default Telegram limiter.
func WithRateLimiter(limiter RateLimiter) Option {
	return func(e *Enforcer) {
		if limiter != nil {
			e.limiter = limiter
		}
	}
}

// WithClock replaces time.Now for tests.
func WithClock(now func() time.Time) Option {
	return func(e *Enforcer) {
		e.now = now
	}
}

// WithSleep replaces worker sleep for tests.
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(e *Enforcer) {
		e.sleep = sleep
	}
}

// New returns an Enforcer.
func New(
	stores Stores,
	tg TelegramClient,
	invites InviteService,
	cfg Config,
	opts ...Option,
) *Enforcer {
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}

	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = defaultLeaseDuration
	}

	if cfg.IdleDelay <= 0 {
		cfg.IdleDelay = defaultIdleDelay
	}

	e := &Enforcer{
		stores:  stores,
		tg:      tg,
		invites: invites,
		cfg:     cfg,
		logger:  slog.Default(),
		limiter: NewRateLimiter(),
		now:     time.Now,
		sleep:   sleepContext,
	}
	for _, opt := range opts {
		opt(e)
	}

	return e
}

// Run starts worker goroutines and blocks until ctx is cancelled or a
// non-recoverable worker error occurs.
func (e *Enforcer) Run(ctx context.Context) error {
	if e.stores.Outbox == nil {
		return errors.New("outbox store is nil")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, e.cfg.Workers)
	done := make(chan struct{})

	var wg sync.WaitGroup

	for i := range e.cfg.Workers {
		workerID := i + 1

		wg.Go(func() {
			if err := e.runWorker(runCtx, workerID); err != nil {
				select {
				case errCh <- err:
					cancel()
				default:
				}
			}
		})
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		cancel()
		<-done

		return nil
	case err := <-errCh:
		cancel()
		<-done

		return err
	case <-done:
		return nil
	}
}

func (e *Enforcer) runWorker(ctx context.Context, workerID int) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		ok, err := e.runOnce(ctx)
		if err != nil {
			if isContextDone(ctx, err) {
				return nil
			}

			return fmt.Errorf("enforcer worker %d: %w", workerID, err)
		}

		if ok {
			continue
		}

		if err := e.sleep(ctx, e.cfg.IdleDelay); err != nil {
			return nil
		}
	}
}

func (e *Enforcer) runOnce(ctx context.Context) (bool, error) {
	action, ok, err := e.stores.Outbox.LeaseReady(
		ctx, e.now(), e.cfg.LeaseDuration)
	if err != nil || !ok {
		return ok, err
	}

	err = e.execute(ctx, action)
	if err == nil {
		return true, e.stores.Outbox.MarkDone(ctx, action.ID)
	}

	if isContextDone(ctx, err) {
		return true, err
	}

	return true, e.handleFailure(ctx, action, err)
}

func (e *Enforcer) execute(ctx context.Context, action domain.AccessAction) error {
	switch action.Type {
	case domain.ActionEnsureInvite:
		return e.ensureInvite(ctx, action)
	case domain.ActionSendInvite:
		return e.sendInvite(ctx, action)
	case domain.ActionApproveJoin:
		return e.approveJoin(ctx, action)
	case domain.ActionDeclineJoin:
		return e.declineJoin(ctx, action)
	case domain.ActionSoftKick:
		return e.softKick(ctx, action)
	case domain.ActionHardBan:
		return e.hardBan(ctx, action)
	case domain.ActionUnban:
		return e.unban(ctx, action)
	case domain.ActionSendDM:
		return e.sendDM(ctx, action)
	case domain.ActionEditMessage:
		return e.editMessage(ctx, action)
	case domain.ActionVerifyMember:
		return e.verifyMember(ctx, action)
	case domain.ActionRevokeInvite:
		return e.revokeInvite(ctx, action)
	default:
		return fmt.Errorf("unsupported action type %q", action.Type)
	}
}

func (e *Enforcer) ensureInvite(
	ctx context.Context,
	action domain.AccessAction,
) error {
	resource, err := requiredResource(action)
	if err != nil {
		return err
	}

	chatID, err := e.chatID(resource)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	_, err = e.invites.Ensure(ctx, invite.EnsureRequest{
		TGID:     action.TGID,
		Resource: resource,
	})

	return err
}

func (e *Enforcer) sendInvite(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, err := requiredTGID(action)
	if err != nil {
		return err
	}

	resource, err := requiredResource(action)
	if err != nil {
		return err
	}

	chatID, err := e.chatID(resource)
	if err != nil {
		return err
	}

	var payload sendInvitePayload
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	link, err := e.invites.Ensure(ctx, invite.EnsureRequest{
		TGID:     &tgID,
		Resource: resource,
	})
	if err != nil {
		return err
	}

	text := payload.Text
	if text == "" {
		text = link.InviteLink
		payload.ParseMode = ""
		payload.Plain = true
	}

	if err := e.wait(ctx, requestKindMessage, tgID); err != nil {
		return err
	}

	if err := e.sendPayloadMessage(ctx, tgID, text, payload.ParseMode, payload.Plain, nil); err != nil {
		return err
	}

	if err := e.invites.MarkSent(ctx, link); err != nil {
		e.logger.Warn("failed to mark invite link sent",
			slog.Int64("action_id", action.ID),
			slog.Int64("invite_link_id", link.ID),
			slog.String("invite_link_hash", link.InviteLinkHash),
			slog.Any("error", err))
	}

	return nil
}

func (e *Enforcer) approveJoin(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.ApproveChatJoinRequest(ctx, chatID, tgID); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) declineJoin(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.DeclineChatJoinRequest(ctx, chatID, tgID); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) softKick(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindGetMember, chatID); err != nil {
		return err
	}

	member, err := e.tg.GetChatMember(ctx, chatID, tgID)
	if err != nil {
		return classifyNoop(string(action.Type), err)
	}

	if memberIsPrivileged(member) {
		return expectedNoopError{
			err: fmt.Errorf("soft_kick target %d is %s", tgID, member.Type),
		}
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.BanChatMember(ctx, chatID, tgID); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.UnbanChatMember(ctx, chatID, tgID, true); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) hardBan(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindGetMember, chatID); err != nil {
		return err
	}

	member, err := e.tg.GetChatMember(ctx, chatID, tgID)
	if err != nil {
		return classifyNoop(string(action.Type), err)
	}

	if memberIsPrivileged(member) {
		return expectedNoopError{
			err: fmt.Errorf("hard_ban target %d is %s", tgID, member.Type),
		}
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.BanChatMember(ctx, chatID, tgID); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) unban(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.UnbanChatMember(ctx, chatID, tgID, true); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) sendDM(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, err := requiredTGID(action)
	if err != nil {
		return err
	}

	var payload sendDMPayload
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	if payload.Text == "" {
		return errors.New("send_dm payload text is required")
	}

	chatID := tgID
	if payload.ChatID != 0 {
		chatID = payload.ChatID
	}

	if err := e.wait(ctx, requestKindMessage, chatID); err != nil {
		return err
	}

	if payload.RetryButton {
		if sender, ok := e.tg.(replyMarkupSender); ok {
			return e.sendPayloadMessage(
				ctx, chatID, payload.Text, payload.ParseMode, payload.Plain,
				retryKeyboardWithSender(sender))
		}
	}

	if len(payload.Buttons) > 0 {
		if sender, ok := e.tg.(replyMarkupSender); ok {
			return e.sendPayloadMessage(
				ctx, chatID, payload.Text, payload.ParseMode, payload.Plain,
				keyboardWithSender(sender, inlineKeyboard(payload.Buttons)))
		}
	}

	return e.sendPayloadMessage(ctx, chatID, payload.Text, payload.ParseMode, payload.Plain, nil)
}

func (e *Enforcer) sendPayloadMessage(
	ctx context.Context,
	chatID int64,
	text string,
	parseMode string,
	plain bool,
	replyMarkup replyMarkupSend,
) error {
	if parseMode == "" || plain {
		if replyMarkup != nil {
			return replyMarkup.SendPlain(ctx, chatID, text)
		}

		return e.tg.SendMessage(ctx, chatID, text)
	}

	if replyMarkup != nil {
		return replyMarkup.SendFormatted(ctx, chatID, text, parseMode)
	}

	return e.tg.SendFormattedMessage(ctx, chatID, text, parseMode)
}

func (e *Enforcer) editMessage(
	ctx context.Context,
	action domain.AccessAction,
) error {
	var payload editMessagePayload
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	if payload.ChatID == 0 || payload.MessageID == 0 {
		return errors.New("edit_message payload requires chat_id and message_id")
	}

	if payload.Text == "" {
		return errors.New("edit_message payload text is required")
	}

	if err := e.wait(ctx, requestKindMessage, payload.ChatID); err != nil {
		return err
	}

	// A non-retry result (e.g. access granted) clears the keyboard so the
	// "checking" button does not linger. The empty inline keyboard must be a
	// non-nil slice: a nil slice marshals to JSON null, which Telegram rejects
	// with `field "inline_keyboard" must be of type Array`.
	markup := models.ReplyMarkup(models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{},
	})
	if payload.RetryButton {
		markup = retryKeyboard()
	}

	return e.tg.EditMessageText(
		ctx, payload.ChatID, payload.MessageID, payload.Text, markup)
}

func (e *Enforcer) verifyMember(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, err := requiredTGID(action)
	if err != nil {
		return err
	}

	payload := verifyMemberPayload{}
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	chatID := payload.ChatID
	if chatID == 0 {
		resource, err := requiredResource(action)
		if err != nil {
			return err
		}

		chatID, err = e.chatID(resource)
		if err != nil {
			return err
		}

		payload.Resource = string(resource)
		payload.Kind = "club"
	}

	if err := e.wait(ctx, requestKindGetMember, chatID); err != nil {
		return err
	}

	member, err := e.tg.GetChatMember(ctx, chatID, tgID)
	if err != nil {
		return err
	}

	return e.applyVerifiedMember(ctx, tgID, member, payload)
}

func (e *Enforcer) applyVerifiedMember(
	ctx context.Context,
	tgID int64,
	member *models.ChatMember,
	payload verifyMemberPayload,
) error {
	joined := source.MemberInChat(member)
	observedAt := e.now()

	switch payload.Kind {
	case "source":
		platform := domain.Platform(payload.Platform)
		if platform == "" {
			return errors.New("verify_member source payload platform is required")
		}

		if e.stores.Users != nil {
			if err := e.stores.Users.Upsert(ctx, userFromMember(tgID, member)); err != nil {
				return err
			}
		}

		if e.stores.Subscriptions == nil {
			return nil
		}

		if joined {
			if _, err := e.stores.Subscriptions.UpsertActive(ctx, domain.Subscription{
				TGID:          tgID,
				Platform:      platform,
				Status:        domain.SubActive,
				StartedAt:     observedAt,
				LastSignal:    "reconcile",
				LastCheckedAt: &observedAt,
			}); err != nil {
				return err
			}
		} else if _, err := e.stores.Subscriptions.ExpireActive(
			ctx, tgID, platform, observedAt, "reconcile",
		); err != nil {
			return err
		}

		return e.recompute(ctx, tgID)
	case "club", "":
		resource := domain.Resource(payload.Resource)
		if resource == "" {
			switch payload.ChatID {
			case e.cfg.ClubChatID:
				resource = domain.ResourceChat
			case e.cfg.ClubChannelID:
				resource = domain.ResourceChannel
			}
		}

		if resource == "" {
			return errors.New("verify_member club payload resource is required")
		}

		if e.stores.Users != nil {
			if err := e.stores.Users.Upsert(ctx, userFromMember(tgID, member)); err != nil {
				return err
			}
		}

		if e.stores.Grants == nil {
			return nil
		}

		if joined {
			return e.stores.Grants.MarkJoined(ctx, tgID, resource, "bot")
		}

		_, err := e.stores.Grants.MarkLeftUnlessRevoked(ctx, tgID, resource)

		return err
	default:
		return fmt.Errorf("unknown verify_member kind %q", payload.Kind)
	}
}

func (e *Enforcer) recompute(ctx context.Context, tgID int64) error {
	if e.stores.StatusEngine == nil ||
		e.stores.Users == nil ||
		e.stores.Subscriptions == nil ||
		e.stores.Audit == nil ||
		e.stores.Revocations == nil ||
		e.stores.Whitelist == nil {
		return nil
	}

	users, ok := e.stores.Users.(engine.UserStore)
	if !ok {
		return nil
	}

	subscriptions, ok := e.stores.Subscriptions.(engine.SubscriptionStore)
	if !ok {
		return nil
	}

	grants, ok := e.stores.Grants.(engine.GrantStore)
	if !ok {
		return nil
	}

	outbox, ok := e.stores.Outbox.(engine.OutboxStore)
	if !ok {
		return nil
	}

	_, err := e.stores.StatusEngine.RecomputeAccess(ctx, engine.Store{
		Users:         users,
		Subscriptions: subscriptions,
		Audit:         e.stores.Audit,
		Revocations:   e.stores.Revocations,
		Whitelist:     e.stores.Whitelist,
		Grants:        grants,
		Outbox:        outbox,
		Alerts:        e.stores.Alerts,
		Members: telegram.NewClubMemberChecker(
			e.tg, e.cfg.ClubChatID, e.cfg.ClubChannelID),
	}, tgID)

	return err
}

func userFromMember(tgID int64, member *models.ChatMember) domain.User {
	user := memberUser(member)
	if user == nil {
		return domain.User{TGID: tgID}
	}

	return domain.User{
		TGID:         user.ID,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		LanguageCode: user.LanguageCode,
		IsBot:        user.IsBot,
	}
}

func memberUser(member *models.ChatMember) *models.User {
	if member == nil {
		return nil
	}

	switch member.Type {
	case models.ChatMemberTypeOwner:
		if member.Owner == nil {
			return nil
		}

		return member.Owner.User
	case models.ChatMemberTypeAdministrator:
		if member.Administrator == nil {
			return nil
		}

		return &member.Administrator.User
	case models.ChatMemberTypeMember:
		if member.Member == nil {
			return nil
		}

		return member.Member.User
	case models.ChatMemberTypeRestricted:
		if member.Restricted == nil {
			return nil
		}

		return member.Restricted.User
	case models.ChatMemberTypeLeft:
		if member.Left == nil {
			return nil
		}

		return member.Left.User
	case models.ChatMemberTypeBanned:
		if member.Banned == nil {
			return nil
		}

		return member.Banned.User
	default:
		return nil
	}
}

func (e *Enforcer) revokeInvite(
	ctx context.Context,
	action domain.AccessAction,
) error {
	resource, err := requiredResource(action)
	if err != nil {
		return err
	}

	var payload revokeInvitePayload
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	if payload.InviteLink == "" {
		return errors.New("revoke_invite payload invite_link is required")
	}

	chatID, err := e.chatID(resource)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	err = e.invites.Revoke(ctx, domain.InviteLink{
		ID:         payload.InviteLinkID,
		Resource:   resource,
		InviteLink: payload.InviteLink,
	})
	if err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) handleFailure(
	ctx context.Context,
	action domain.AccessAction,
	cause error,
) error {
	var noop expectedNoopError
	if errors.As(cause, &noop) {
		e.logger.Warn("outbox action expected no-op",
			slog.Int64("action_id", action.ID),
			slog.String("action_type", string(action.Type)),
			slog.Any("error", cause))

		return e.stores.Outbox.MarkDone(ctx, action.ID)
	}

	if actionCanBlockDM(action.Type) && telegram.IsDMBlocked(cause) {
		if err := e.markDMBlocked(ctx, action); err != nil {
			return err
		}

		return e.stores.Outbox.MarkDone(ctx, action.ID)
	}

	// Attempts stores previous failed executions; +1 accounts for this failure.
	if telegram.IsPermanentRights(cause) ||
		telegram.IsForbidden(cause) ||
		action.Attempts+1 >= action.MaxAttempts {
		return e.markDead(ctx, action, cause)
	}

	if retryAfter, ok := telegram.RetryAfter(cause); ok {
		_, err := e.stores.Outbox.Retry(
			ctx, action.ID, e.now().Add(retryAfter), cause.Error())

		return err
	}

	wait := e.retryBackoff(action)
	_, err := e.stores.Outbox.Retry(ctx, action.ID, e.now().Add(wait), cause.Error())

	return err
}

func actionCanBlockDM(actionType domain.ActionType) bool {
	return actionType == domain.ActionSendDM || actionType == domain.ActionSendInvite
}

func (e *Enforcer) markDMBlocked(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, err := requiredTGID(action)
	if err != nil {
		return err
	}

	if e.stores.Users == nil {
		return errors.New("users store is nil")
	}

	err = e.stores.Users.SetDMState(ctx, tgID, domain.DMBlocked)
	if err == nil {
		return nil
	}

	if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	return e.stores.Users.Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMBlocked,
	})
}

func (e *Enforcer) markDead(
	ctx context.Context,
	action domain.AccessAction,
	cause error,
) error {
	if err := e.stores.Outbox.MarkDead(ctx, action.ID, cause.Error()); err != nil {
		return err
	}

	if e.stores.Alerts == nil {
		return nil
	}

	_, err := e.stores.Alerts.Create(ctx, store.AlertInput{
		Severity: "error",
		Kind:     "outbox_action_dead",
		Title:    "outbox action dead",
		Detail: fmt.Sprintf(
			"action_id=%d action_type=%s error=%s",
			action.ID, action.Type, cause.Error()),
		TGID: action.TGID,
	})

	return err
}

func (e *Enforcer) retryBackoff(action domain.AccessAction) time.Duration {
	step := max(action.Attempts+1, 1)

	backoff := defaultRetryBackoff * time.Duration(1<<min(step-1, 5))
	jitterLimit := big.NewInt(int64(defaultRetryBackoff))

	jitterN, err := cryptorand.Int(cryptorand.Reader, jitterLimit)
	if err != nil {
		return backoff
	}

	jitter := time.Duration(jitterN.Int64())

	return backoff + jitter
}

func (e *Enforcer) tgTarget(action domain.AccessAction) (int64, int64, error) {
	tgID, err := requiredTGID(action)
	if err != nil {
		return 0, 0, err
	}

	resource, err := requiredResource(action)
	if err != nil {
		return 0, 0, err
	}

	chatID, err := e.chatID(resource)
	if err != nil {
		return 0, 0, err
	}

	return tgID, chatID, nil
}

func (e *Enforcer) chatID(resource domain.Resource) (int64, error) {
	switch resource {
	case domain.ResourceChat:
		return e.cfg.ClubChatID, nil
	case domain.ResourceChannel:
		return e.cfg.ClubChannelID, nil
	default:
		return 0, fmt.Errorf("unknown action resource %q", resource)
	}
}

func (e *Enforcer) wait(ctx context.Context, kind requestKind, chatID int64) error {
	if e.limiter == nil {
		return nil
	}

	return e.limiter.Wait(ctx, kind, chatID)
}

func requiredTGID(action domain.AccessAction) (int64, error) {
	if action.TGID == nil {
		return 0, fmt.Errorf("%s action requires tg_id", action.Type)
	}

	return *action.TGID, nil
}

func requiredResource(action domain.AccessAction) (domain.Resource, error) {
	if action.Resource == nil {
		return "", fmt.Errorf("%s action requires resource", action.Type)
	}

	return *action.Resource, nil
}

func decodePayload(action domain.AccessAction, dest any) error {
	payload := action.PayloadJSON
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}

	if err := json.Unmarshal(payload, dest); err != nil {
		return fmt.Errorf("decode %s payload: %w", action.Type, err)
	}

	return nil
}

func classifyNoop(action string, err error) error {
	if err == nil {
		return nil
	}

	if telegram.IsExpectedNoop(action, err) {
		return expectedNoopError{err: err}
	}

	return err
}

func memberIsPrivileged(member *models.ChatMember) bool {
	if member == nil {
		return false
	}

	return member.Type == models.ChatMemberTypeOwner ||
		member.Type == models.ChatMemberTypeAdministrator
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isContextDone(ctx context.Context, err error) bool {
	return ctx.Err() != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

type expectedNoopError struct {
	err error
}

func (e expectedNoopError) Error() string {
	return e.err.Error()
}

func (e expectedNoopError) Unwrap() error {
	return e.err
}

type sendDMPayload struct {
	Text        string `json:"text"`
	ParseMode   string `json:"parse_mode,omitempty"`
	Plain       bool   `json:"plain,omitempty"`
	ChatID      int64  `json:"chat_id,omitempty"`
	RetryButton bool   `json:"retry_button,omitempty"`
	Buttons     [][]struct {
		Text         string `json:"text"`
		CallbackData string `json:"callback_data"`
	} `json:"buttons,omitempty"`
}

type editMessagePayload struct {
	ChatID      int64  `json:"chat_id"`
	MessageID   int    `json:"message_id"`
	Text        string `json:"text"`
	RetryButton bool   `json:"retry_button,omitempty"`
}

type sendInvitePayload struct {
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
	Plain     bool   `json:"plain,omitempty"`
}

type revokeInvitePayload struct {
	InviteLinkID int64  `json:"invite_link_id"`
	InviteLink   string `json:"invite_link"`
}

type verifyMemberPayload struct {
	Kind     string `json:"kind,omitempty"` // source|club
	Platform string `json:"platform,omitempty"`
	Resource string `json:"resource,omitempty"`
	ChatID   int64  `json:"chat_id,omitempty"`
}

type replyMarkupSender interface {
	SendMessageWithReplyMarkup(
		ctx context.Context,
		chatID int64,
		text string,
		replyMarkup models.ReplyMarkup,
	) error
	SendFormattedMessageWithReplyMarkup(
		ctx context.Context,
		chatID int64,
		text string,
		parseMode string,
		replyMarkup models.ReplyMarkup,
	) error
}

type replyMarkupSend interface {
	SendPlain(ctx context.Context, chatID int64, text string) error
	SendFormatted(ctx context.Context, chatID int64, text string, parseMode string) error
}

type replyMarkupDelivery struct {
	sender      replyMarkupSender
	replyMarkup models.ReplyMarkup
}

func retryKeyboardWithSender(sender replyMarkupSender) replyMarkupSend {
	return keyboardWithSender(sender, retryKeyboard())
}

func keyboardWithSender(
	sender replyMarkupSender,
	replyMarkup models.ReplyMarkup,
) replyMarkupSend {
	return replyMarkupDelivery{sender: sender, replyMarkup: replyMarkup}
}

func (d replyMarkupDelivery) SendPlain(
	ctx context.Context,
	chatID int64,
	text string,
) error {
	return d.sender.SendMessageWithReplyMarkup(ctx, chatID, text, d.replyMarkup)
}

func (d replyMarkupDelivery) SendFormatted(
	ctx context.Context,
	chatID int64,
	text string,
	parseMode string,
) error {
	return d.sender.SendFormattedMessageWithReplyMarkup(
		ctx, chatID, text, parseMode, d.replyMarkup)
}

func retryKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{
				Text:         messages.RetryAccessButtonText,
				CallbackData: messages.RetryAccessCallbackData,
			},
		}},
	}
}

func inlineKeyboard(buttons [][]struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}) models.InlineKeyboardMarkup {
	keyboard := make([][]models.InlineKeyboardButton, 0, len(buttons))
	for _, row := range buttons {
		outRow := make([]models.InlineKeyboardButton, 0, len(row))
		for _, button := range row {
			outRow = append(outRow, models.InlineKeyboardButton{
				Text:         button.Text,
				CallbackData: button.CallbackData,
			})
		}

		if len(outRow) > 0 {
			keyboard = append(keyboard, outRow)
		}
	}

	return models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}
