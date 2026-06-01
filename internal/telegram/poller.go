package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/admission"
	commandbot "github.com/justskiv/gatekeeper/internal/bot"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/source"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	defaultPollLimit   = 100
	defaultPollTimeout = 50
	pendingBatchLimit  = 100
	maxPollBackoff     = 5 * time.Second
	liveRetryDelay     = 100 * time.Millisecond
)

// Poller runs the durable long-polling loop.
type Poller struct {
	db       *sql.DB
	client   *Client
	notifier *notify.Notifier

	statusEngine *engine.Engine
	sourceChats  SourceChats
	admissionCfg admission.Config
	rateLimiter  *admissionRateLimiter
	chats        []HealthChat
	ownerIDs     []int64
	logger       *slog.Logger

	afterBeginTx func() // test hook for tx-boundary assertions

	limit   int
	timeout int
}

// PollerOption configures optional poller integrations.
type PollerOption func(*Poller)

// WithPollerStatusEngine attaches the status engine.
func WithPollerStatusEngine(statusEngine *engine.Engine) PollerOption {
	return func(p *Poller) {
		p.statusEngine = statusEngine
	}
}

// WithPollerSourceChats attaches source chat routing config.
func WithPollerSourceChats(sourceChats SourceChats) PollerOption {
	return func(p *Poller) {
		p.sourceChats = sourceChats
	}
}

// WithPollerAdmissionConfig attaches managed resource admission config.
func WithPollerAdmissionConfig(cfg admission.Config) PollerOption {
	return func(p *Poller) {
		p.admissionCfg = cfg
	}
}

// NewPoller returns a durable sequential Telegram update poller.
func NewPoller(
	db *sql.DB,
	client *Client,
	notifier *notify.Notifier,
	chats []HealthChat,
	ownerIDs []int64,
	logger *slog.Logger,
	opts ...PollerOption,
) *Poller {
	if logger == nil {
		logger = slog.Default()
	}

	poller := &Poller{
		db:          db,
		client:      client,
		notifier:    notifier,
		chats:       chats,
		ownerIDs:    ownerIDs,
		logger:      logger,
		rateLimiter: newAdmissionRateLimiter(time.Now),
		limit:       defaultPollLimit,
		timeout:     defaultPollTimeout,
	}
	for _, opt := range opts {
		opt(poller)
	}

	return poller
}

// Run recovers pending rows, then polls Telegram until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) error {
	updatesRepo := store.NewTelegramUpdates(p.db)

	offset, err := updatesRepo.ResolveOffset(ctx, store.NewMeta(p.db))
	if err != nil {
		if isContextDone(ctx, err) {
			return nil
		}

		return err
	}

	p.logger.Info("telegram poller resolved offset", slog.Int64("offset", offset))

	if err := p.processPending(ctx); err != nil {
		if isContextDone(ctx, err) {
			return nil
		}

		return err
	}

	backoff := 100 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		updates, err := p.client.GetUpdates(ctx, GetUpdatesParams{
			Offset:         offset,
			Limit:          p.limit,
			Timeout:        p.timeout,
			AllowedUpdates: DefaultAllowedUpdates,
		})
		if err != nil {
			if isContextDone(ctx, err) {
				return nil
			}

			if shouldAbortPolling(err) {
				return err
			}

			nextBackoff, waitErr := p.waitAfterPollError(ctx, err, backoff)
			if waitErr != nil {
				if isContextDone(ctx, waitErr) {
					return nil
				}

				return waitErr
			}

			backoff = nextBackoff

			continue
		}

		backoff = 100 * time.Millisecond

		if len(updates) == 0 {
			continue
		}

		batch, nextOffset, err := buildUpdateBatch(updates, offset)
		if err != nil {
			return err
		}

		if err := updatesRepo.InsertBatch(ctx, batch, nextOffset); err != nil {
			if isContextDone(ctx, err) {
				return nil
			}

			return err
		}

		offset = nextOffset

		if err := p.processPending(ctx); err != nil {
			if isContextDone(ctx, err) {
				return nil
			}

			return err
		}
	}
}

func (p *Poller) waitAfterPollError(
	ctx context.Context, err error, backoff time.Duration,
) (time.Duration, error) {
	wait := backoff

	var apiErr *APIError
	if errors.As(err, &apiErr) &&
		apiErr.Category == ErrorCategoryRateLimited &&
		apiErr.RetryAfter > 0 {
		wait = time.Duration(apiErr.RetryAfter) * time.Second
	}

	p.logger.Warn("telegram polling failed",
		slog.Duration("retry_after", wait),
		slog.Any("error", err))

	select {
	case <-ctx.Done():
		return backoff, ctx.Err()
	case <-time.After(wait):
	}

	if wait >= maxPollBackoff {
		return maxPollBackoff, nil
	}

	next := min(wait*2, maxPollBackoff)

	return next, nil
}

func shouldAbortPolling(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	return apiErr.Category == ErrorCategoryUnauthorized
}

func (p *Poller) processPending(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		rows, err := store.NewTelegramUpdates(p.db).ListPending(ctx, pendingBatchLimit)
		if err != nil {
			return err
		}

		if len(rows) == 0 {
			return nil
		}

		for _, row := range rows {
			if err := p.processOne(ctx, row); err != nil {
				return err
			}
		}
	}
}

func (p *Poller) processOne(ctx context.Context, row store.TelegramUpdate) error {
	var update models.Update
	if err := json.Unmarshal(row.PayloadJSON, &update); err != nil {
		return p.markFailed(ctx, row.UpdateID, fmt.Errorf("decode update payload: %w", err))
	}

	preflight, err := p.buildPreflight(ctx, &update)
	if err != nil {
		if isContextDone(ctx, err) {
			return err
		}

		return p.markFailed(ctx, row.UpdateID, err)
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		if isContextDone(ctx, err) {
			return err
		}

		return fmt.Errorf("begin telegram update tx2: %w", err)
	}

	if p.afterBeginTx != nil {
		p.afterBeginTx()
	}

	router := NewRouter(RouterDeps{
		Users:         store.NewUsers(tx),
		Subscriptions: store.NewSubscriptions(tx),
		Grants:        store.NewGrants(tx),
		Meta:          store.NewMeta(tx),
		Audit:         store.NewAudit(tx),
		Alerts:        store.NewAlerts(tx),
		Whitelist:     store.NewWhitelist(tx),
		Revocations:   store.NewRevocations(tx),
		Outbox:        store.NewOutbox(tx),
		Invites:       store.NewInvites(tx),
		UpdateID:      row.UpdateID,
	}, p.chats, p.ownerIDs, p.logger,
		WithStatusEngine(p.statusEngine),
		WithSourceChats(p.sourceChats),
		WithAdmissionConfig(p.admissionCfg),
		WithPreflight(preflight))

	result, err := router.Route(ctx, &update)
	if err != nil {
		_ = tx.Rollback()

		if isContextDone(ctx, err) {
			return err
		}

		return p.markFailed(ctx, row.UpdateID, err)
	}

	if err := store.NewTelegramUpdates(tx).MarkTerminal(
		ctx, row.UpdateID, result.Status, "",
	); err != nil {
		_ = tx.Rollback()

		if isContextDone(ctx, err) {
			return err
		}

		return err
	}

	if err := tx.Commit(); err != nil {
		if isContextDone(ctx, err) {
			return err
		}

		return fmt.Errorf("commit telegram update tx2: %w", err)
	}

	p.deliverEffects(ctx, result.Effects)

	return nil
}

func (p *Poller) buildPreflight(
	ctx context.Context,
	update *models.Update,
) (RoutePreflight, error) {
	if p.statusEngine == nil || update == nil {
		return RoutePreflight{}, nil
	}

	preflight, ok, err := p.buildAdmissionPreflight(ctx, update)
	if ok || err != nil {
		return preflight, err
	}

	return p.buildCommandPreflight(ctx, update)
}

func (p *Poller) buildCommandPreflight(
	ctx context.Context,
	update *models.Update,
) (RoutePreflight, error) {
	if update.Message == nil ||
		update.Message.From == nil ||
		update.Message.Chat.Type != models.ChatTypePrivate {
		return RoutePreflight{}, nil
	}

	msg := update.Message

	var targetID int64

	switch commandbot.CommandName(msg.Text) {
	case "status":
		targetID = msg.From.ID
	case "whois":
		if !containsID(p.ownerIDs, msg.From.ID) {
			return RoutePreflight{}, nil
		}

		users := store.NewUsers(p.db)

		target, err := commandbot.ResolveWhoisTarget(
			ctx, users, msg.Text)
		if err != nil || target.BadSyntax || target.NotFound {
			return RoutePreflight{}, err
		}

		if _, err := users.Get(ctx, target.TGID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return RoutePreflight{}, nil
			}

			return RoutePreflight{}, err
		}

		targetID = target.TGID
	default:
		return RoutePreflight{}, nil
	}

	snapshot, err := p.statusEngine.LiveSnapshot(ctx, engine.Store{
		Users:         store.NewUsers(p.db),
		Subscriptions: store.NewSubscriptions(p.db),
		Whitelist:     store.NewWhitelist(p.db),
	}, targetID)
	if err != nil {
		return RoutePreflight{}, err
	}

	return RoutePreflight{Snapshot: &snapshot}, nil
}

func (p *Poller) buildAdmissionPreflight(
	ctx context.Context,
	update *models.Update,
) (RoutePreflight, bool, error) {
	target, ok := p.admissionPreflightTarget(update)
	if !ok {
		return RoutePreflight{}, false, nil
	}

	if target.rateLimited {
		return RoutePreflight{AdmissionRateLimited: true}, true, nil
	}

	banned, err := p.isKnownBanned(ctx, target.tgID)
	if err != nil {
		return RoutePreflight{}, true, err
	}

	if banned {
		return RoutePreflight{}, true, nil
	}

	snapshot, err := p.liveSnapshotWithRetries(ctx, target.tgID, target.retries)
	if err != nil {
		return RoutePreflight{}, true, err
	}

	return RoutePreflight{Snapshot: snapshot}, true, nil
}

type admissionPreflightTarget struct {
	tgID        int64
	retries     int
	rateLimited bool
}

func (p *Poller) admissionPreflightTarget(
	update *models.Update,
) (admissionPreflightTarget, bool) {
	switch {
	case update.Message != nil:
		msg := update.Message
		if msg.From == nil ||
			msg.Chat.Type != models.ChatTypePrivate ||
			!isAccessRequestMessage(msg) {
			return admissionPreflightTarget{}, false
		}

		if !p.rateLimiter.Allow(msg.From.ID, 30*time.Second) {
			return admissionPreflightTarget{
				tgID:        msg.From.ID,
				rateLimited: true,
			}, true
		}

		return admissionPreflightTarget{tgID: msg.From.ID}, true
	case update.CallbackQuery != nil:
		query := update.CallbackQuery
		if query.Data != messages.RetryAccessCallbackData {
			return admissionPreflightTarget{}, false
		}

		if !p.rateLimiter.Allow(query.From.ID, 30*time.Second) {
			return admissionPreflightTarget{
				tgID:        query.From.ID,
				rateLimited: true,
			}, true
		}

		return admissionPreflightTarget{tgID: query.From.ID}, true
	case update.ChatJoinRequest != nil:
		req := update.ChatJoinRequest
		if !p.isClubResource(req.Chat.ID) {
			return admissionPreflightTarget{}, false
		}

		return admissionPreflightTarget{
			tgID:    req.From.ID,
			retries: p.joinRequestRetries(),
		}, true
	case update.ChatMember != nil:
		memberUpdate := update.ChatMember
		if !p.isClubResource(memberUpdate.Chat.ID) {
			return admissionPreflightTarget{}, false
		}

		if source.MemberInChat(&memberUpdate.OldChatMember) ||
			!source.MemberInChat(&memberUpdate.NewChatMember) {
			return admissionPreflightTarget{}, false
		}

		if memberUpdate.ViaJoinRequest ||
			p.admissionCfg.InviteMode != domain.InviteDirect {
			return admissionPreflightTarget{}, false
		}

		user := chatMemberUser(&memberUpdate.NewChatMember)
		if user == nil || user.IsBot {
			return admissionPreflightTarget{}, false
		}

		return admissionPreflightTarget{tgID: user.ID}, true
	default:
		return admissionPreflightTarget{}, false
	}
}

func (p *Poller) liveSnapshotWithRetries(
	ctx context.Context,
	tgID int64,
	retries int,
) (*engine.Snapshot, error) {
	attempts := max(retries, 0) + 1

	var snapshot engine.Snapshot

	for i := range attempts {
		got, err := p.statusEngine.LiveSnapshot(ctx, engine.Store{
			Users:         store.NewUsers(p.db),
			Subscriptions: store.NewSubscriptions(p.db),
			Whitelist:     store.NewWhitelist(p.db),
		}, tgID)
		if err != nil {
			return nil, err
		}

		snapshot = got
		if got.Decision.Status != domain.StatusUnknown || i == attempts-1 {
			return &snapshot, nil
		}

		timer := time.NewTimer(liveRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}

			return nil, ctx.Err()
		}
	}

	return &snapshot, nil
}

func (p *Poller) isClubResource(chatID int64) bool {
	if chatID == 0 {
		return false
	}

	for _, resource := range p.admissionCfg.Resources {
		if resource.ChatID == chatID {
			return true
		}
	}

	return chatID == p.admissionCfg.ClubChatID && p.admissionCfg.ClubChatID != 0 ||
		chatID == p.admissionCfg.ClubChannelID && p.admissionCfg.ClubChannelID != 0
}

func (p *Poller) joinRequestRetries() int {
	if p.admissionCfg.JoinRequestRetries > 0 {
		return p.admissionCfg.JoinRequestRetries
	}

	return 2
}

func (p *Poller) isKnownBanned(ctx context.Context, tgID int64) (bool, error) {
	user, err := store.NewUsers(p.db).Get(ctx, tgID)
	switch {
	case err == nil:
		return user.Banned, nil
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

func containsID(ids []int64, want int64) bool {
	return slices.Contains(ids, want)
}

func (p *Poller) markFailed(ctx context.Context, updateID int64, cause error) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		if isContextDone(ctx, err) {
			return err
		}

		return fmt.Errorf("begin telegram update tx3: %w", err)
	}

	if err := store.NewTelegramUpdates(tx).MarkTerminal(
		ctx, updateID, store.TelegramUpdateFailed, cause.Error(),
	); err != nil {
		_ = tx.Rollback()

		if isContextDone(ctx, err) {
			return err
		}

		return err
	}

	if _, err := store.NewAlerts(tx).Create(ctx, store.AlertInput{
		Severity: "error",
		Kind:     "telegram_update_failed",
		Title:    "telegram update failed",
		Detail:   fmt.Sprintf("update_id=%d error=%s", updateID, cause.Error()),
	}); err != nil {
		_ = tx.Rollback()

		if isContextDone(ctx, err) {
			return err
		}

		return err
	}

	if err := tx.Commit(); err != nil {
		if isContextDone(ctx, err) {
			return err
		}

		return fmt.Errorf("commit telegram update tx3: %w", err)
	}

	p.logger.Error("telegram update failed",
		slog.Int64("update_id", updateID),
		slog.Any("error", cause))

	return nil
}

func (p *Poller) deliverEffects(ctx context.Context, effects []OutboundMessage) {
	for _, effect := range effects {
		var err error

		switch effect.Kind {
		case OutboundDM:
			if p.notifier == nil {
				continue
			}

			err = p.notifier.SendDM(ctx, effect.TGID, effect.Text)
		case OutboundChatMessage:
			if p.client == nil {
				continue
			}

			err = p.client.SendMessage(ctx, effect.ChatID, effect.Text)
		}

		if err != nil {
			p.logger.Warn("failed to deliver telegram effect",
				slog.String("kind", string(effect.Kind)),
				slog.Int64("chat_id", effect.ChatID),
				slog.Int64("tg_id", effect.TGID),
				slog.Any("error", err))
		}
	}
}

func buildUpdateBatch(
	updates []FetchedUpdate, currentOffset int64,
) ([]store.TelegramUpdate, int64, error) {
	batch := make([]store.TelegramUpdate, 0, len(updates))
	nextOffset := currentOffset

	for _, fetched := range updates {
		update := fetched.Update
		if update == nil {
			continue
		}

		payload := fetched.Raw
		if len(payload) == 0 {
			var err error

			payload, err = json.Marshal(update)
			if err != nil {
				return nil, 0, fmt.Errorf("encode update %d payload: %w", update.ID, err)
			}
		}

		if update.ID+1 > nextOffset {
			nextOffset = update.ID + 1
		}

		chatID, tgID := updateChatAndUser(update)
		batch = append(batch, store.TelegramUpdate{
			UpdateID:    update.ID,
			UpdateType:  updateType(update),
			ChatID:      chatID,
			TGID:        tgID,
			PayloadJSON: payload,
			ReceivedAt:  time.Now(),
		})
	}

	return batch, nextOffset, nil
}

func isContextDone(ctx context.Context, err error) bool {
	return ctx.Err() != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func updateType(update *models.Update) string {
	switch {
	case update.Message != nil:
		return models.AllowedUpdateMessage
	case update.CallbackQuery != nil:
		return models.AllowedUpdateCallbackQuery
	case update.MyChatMember != nil:
		return models.AllowedUpdateMyChatMember
	case update.ChatMember != nil:
		return models.AllowedUpdateChatMember
	case update.ChatJoinRequest != nil:
		return models.AllowedUpdateChatJoinRequest
	default:
		return "unknown"
	}
}

func updateChatAndUser(update *models.Update) (*int64, *int64) {
	switch {
	case update.Message != nil:
		return int64Ptr(update.Message.Chat.ID), messageUserID(update.Message)
	case update.CallbackQuery != nil:
		return nil, int64Ptr(update.CallbackQuery.From.ID)
	case update.MyChatMember != nil:
		return int64Ptr(update.MyChatMember.Chat.ID),
			int64Ptr(update.MyChatMember.From.ID)
	case update.ChatMember != nil:
		return int64Ptr(update.ChatMember.Chat.ID),
			int64Ptr(update.ChatMember.From.ID)
	case update.ChatJoinRequest != nil:
		return int64Ptr(update.ChatJoinRequest.Chat.ID),
			int64Ptr(update.ChatJoinRequest.From.ID)
	default:
		return nil, nil
	}
}

func messageUserID(msg *models.Message) *int64 {
	if msg.From == nil {
		return nil
	}

	return int64Ptr(msg.From.ID)
}

func int64Ptr(v int64) *int64 {
	return &v
}

type admissionRateLimiter struct {
	mu   sync.Mutex
	last map[int64]time.Time
	now  func() time.Time
}

func newAdmissionRateLimiter(now func() time.Time) *admissionRateLimiter {
	return &admissionRateLimiter{
		last: make(map[int64]time.Time),
		now:  now,
	}
}

func (l *admissionRateLimiter) Allow(tgID int64, interval time.Duration) bool {
	if l == nil || interval <= 0 {
		return true
	}

	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	last, ok := l.last[tgID]
	if ok && now.Sub(last) < interval {
		return false
	}

	l.last[tgID] = now

	return true
}
