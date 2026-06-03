package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	signalEvent    = "event"
	signalOnDemand = "on_demand"
	signalWebhook  = "webhook"

	auditSubscriptionActivated = "subscription_activated"
	auditSubscriptionExpired   = "subscription_expired"
	auditSubscriptionCancelled = "cancelled_subscription"
	auditRevocationCancelled   = "revocation_cancelled"
	auditStatusUnknown         = "status_unknown"
	auditRevocationScheduled   = "revocation_scheduled"
	auditAccessRevoked         = "access_revoked"
	auditExpiredNotice         = "expired_notice"

	alertProtectedAdmin = "protected_admin_lost_subscription"
	alertUnsafeRevoke   = "revocation_unsafe"
)

// RevocationConfig controls expiry behavior.
type RevocationConfig struct {
	ExpiryMode  string
	GracePeriod time.Duration
}

// Engine applies subscription events and computes access status.
type Engine struct {
	sources []SubscriptionSource
	locks   *KeyedMutex
	now     func() time.Time
	revoke  RevocationConfig
}

// Option configures Engine.
type Option func(*Engine)

// WithClock replaces time.Now for tests.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) {
		e.now = now
	}
}

// WithRevocationConfig sets expiry-mode behavior for recomputeAccess.
func WithRevocationConfig(cfg RevocationConfig) Option {
	return func(e *Engine) {
		e.revoke = normalizeRevocationConfig(cfg)
	}
}

// New returns a status engine.
func New(sources []SubscriptionSource, opts ...Option) *Engine {
	e := &Engine{
		sources: append([]SubscriptionSource(nil), sources...),
		locks:   NewKeyedMutex(),
		now:     time.Now,
		revoke:  normalizeRevocationConfig(RevocationConfig{}),
	}
	for _, opt := range opts {
		opt(e)
	}

	return e
}

func normalizeRevocationConfig(cfg RevocationConfig) RevocationConfig {
	if cfg.ExpiryMode == "" {
		cfg.ExpiryMode = "grace"
	}

	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 72 * time.Hour
	}

	return cfg
}

// ApplyObservations persists live source observations inside handleTx.
func (e *Engine) ApplyObservations(
	ctx context.Context,
	repos Store,
	tgID int64,
	verdicts []domain.SourceVerdict,
) error {
	unlock := e.locks.Lock(tgID)
	defer unlock()

	return e.applyObservations(ctx, repos, tgID, verdicts)
}

func (e *Engine) applyObservations(
	ctx context.Context,
	repos Store,
	tgID int64,
	verdicts []domain.SourceVerdict,
) error {
	now := e.now()

	for _, verdict := range verdicts {
		if verdict.Source == domain.PlatformManual {
			continue
		}

		switch verdict.Verdict {
		case domain.VerdictActive:
			if _, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
				TGID:          tgID,
				Platform:      verdict.Source,
				Status:        domain.SubActive,
				StartedAt:     now,
				ExpiresAt:     verdict.Until,
				LastSignal:    signalOnDemand,
				LastCheckedAt: &now,
			}); err != nil {
				return err
			}
		case domain.VerdictInactive:
			if _, err := repos.Subscriptions.ExpireActive(
				ctx, tgID, verdict.Source, now, signalOnDemand,
			); err != nil {
				return err
			}
		case domain.VerdictUnknown, domain.VerdictNoSignal:
		}
	}

	return nil
}

// HandleEvent applies one normalized subscription event inside handleTx.
//
//nolint:gocognit,gocyclo,cyclop,funlen,wsl_v5 // Domain event switch is explicit.
func (e *Engine) HandleEvent(
	ctx context.Context,
	repos Store,
	event domain.SubscriptionEvent,
) ([]Effect, error) {
	unlock := e.locks.Lock(event.TGUserID)
	defer unlock()

	if err := repos.Users.Upsert(ctx, domain.User{
		TGID:         event.TGUserID,
		Username:     event.TGUsername,
		FirstName:    event.TGFirstName,
		LastName:     event.TGLastName,
		LanguageCode: event.TGLanguageCode,
		IsBot:        event.TGIsBot,
	}); err != nil {
		return nil, err
	}

	now := event.OccurredAt
	if now.IsZero() {
		now = e.now()
	}

	eventAt := event.EventAt
	if eventAt.IsZero() {
		eventAt = now
	}

	tgID := event.TGUserID

	// Capture the effective status before applying the event so the engine
	// can emit access_granted only on a real non-active→active transition.
	var priorStatus domain.EffectiveStatus
	if repos.OperatorLog != nil {
		prior, err := e.persistedDecision(ctx, repos, tgID)
		if err != nil {
			return nil, err
		}

		priorStatus = prior.Status
	}

	switch event.Kind {
	case domain.EventActivated:
		signal := signalEvent
		lastEventAt := now
		if event.ProviderEvent != "" {
			signal = signalWebhook
			lastEventAt = eventAt

			stale, err := e.staleTributeWebhookEvent(ctx, repos, event, eventAt)
			if err != nil {
				return nil, err
			}

			if stale {
				return nil, nil
			}
		}

		if _, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
			TGID:        tgID,
			Platform:    event.Platform,
			Status:      domain.SubActive,
			ExternalID:  event.ExternalID,
			PeriodID:    event.PeriodID,
			Tier:        event.Tier,
			StartedAt:   now,
			ExpiresAt:   event.ExpiresAt,
			LastSignal:  signal,
			LastEventAt: &lastEventAt,
		}); err != nil {
			return nil, err
		}

		if err := repos.Audit.Append(ctx, store.AuditEntry{
			TGID:   &tgID,
			Kind:   auditSubscriptionActivated,
			Source: string(event.Platform),
			Actor:  "provider",
			Detail: eventDetail(event),
		}); err != nil {
			return nil, err
		}

		if err := e.emitSourceEvent(ctx, repos,
			domain.OpSourceSubscriptionActivated, subjectFromEvent(event),
			event); err != nil {
			return nil, err
		}
	case domain.EventDeactivated:
		signal := signalEvent
		endedAt := now
		if event.ProviderEvent != "" {
			signal = signalWebhook
			endedAt = eventAt

			stale, err := e.staleTributeWebhookEvent(ctx, repos, event, eventAt)
			if err != nil {
				return nil, err
			}

			if stale {
				return nil, nil
			}
		}

		ok, err := repos.Subscriptions.ExpireActive(
			ctx, tgID, event.Platform, endedAt, signal)
		if err != nil {
			return nil, err
		}

		if ok {
			if err := repos.Audit.Append(ctx, store.AuditEntry{
				TGID:   &tgID,
				Kind:   auditSubscriptionExpired,
				Source: string(event.Platform),
				Actor:  "provider",
				Detail: eventDetail(event),
			}); err != nil {
				return nil, err
			}

			if err := e.emitSourceEvent(ctx, repos,
				domain.OpSourceSubscriptionExpired, subjectFromEvent(event),
				event); err != nil {
				return nil, err
			}
		}
	case domain.EventCancelledSubscription:
		if err := repos.Audit.Append(ctx, store.AuditEntry{
			TGID:   &tgID,
			Kind:   auditSubscriptionCancelled,
			Source: string(event.Platform),
			Actor:  "provider",
			Detail: eventDetail(event),
		}); err != nil {
			return nil, err
		}

		if event.ProviderEvent != "" {
			stale, err := e.staleTributeWebhookEvent(ctx, repos, event, eventAt)
			if err != nil {
				return nil, err
			}

			if stale {
				return nil, nil
			}
		}

		return nil, nil
	default:
		return nil, fmt.Errorf("unknown subscription event kind %q", event.Kind)
	}

	effects, err := e.recomputeAccess(ctx, repos, tgID)
	if err != nil {
		return nil, err
	}

	if err := e.emitAccessGrantedOnTransition(
		ctx, repos, tgID, priorStatus); err != nil {
		return nil, err
	}

	return effects, nil
}

func (e *Engine) staleTributeWebhookEvent(
	ctx context.Context,
	repos Store,
	event domain.SubscriptionEvent,
	eventAt time.Time,
) (bool, error) {
	if event.Platform != domain.PlatformTribute ||
		event.ProviderEvent == "" ||
		repos.Subscriptions == nil {
		return false, nil
	}

	sub, ok, err := repos.Subscriptions.GetActive(
		ctx, event.TGUserID, domain.PlatformTribute)
	if err != nil || !ok || sub.LastEventAt == nil {
		return false, err
	}

	return !eventAt.After(*sub.LastEventAt), nil
}

func (e *Engine) recomputeAccess(
	ctx context.Context,
	repos Store,
	tgID int64,
) ([]Effect, error) {
	decision, err := e.persistedDecision(ctx, repos, tgID)
	if err != nil {
		return nil, err
	}

	switch decision.Status {
	case domain.StatusActive:
		if _, ok, err := repos.Revocations.Get(ctx, tgID); err != nil {
			return nil, err
		} else if ok {
			if err := repos.Revocations.Delete(ctx, tgID); err != nil {
				return nil, err
			}

			if err := repos.Audit.Append(ctx, store.AuditEntry{
				TGID:   &tgID,
				Kind:   auditRevocationCancelled,
				Actor:  "system",
				Detail: "subscription is active again",
			}); err != nil {
				return nil, err
			}

			if err := e.emitAccessKept(ctx, repos, tgID, decision); err != nil {
				return nil, err
			}

			return e.notifyUser(ctx, repos, tgID, messages.AccessKept(),
				"revocation_cancelled")
		}
	case domain.StatusUnknown:
		// Persisted state cannot currently produce unknown without a future
		// source of local uncertain facts, but the branch is part of the
		// recomputeAccess contract and keeps the path fail-open.
		if err := repos.Audit.Append(ctx, store.AuditEntry{
			TGID:   &tgID,
			Kind:   auditStatusUnknown,
			Actor:  "system",
			Detail: "access status is unknown",
		}); err != nil {
			return nil, err
		}
	case domain.StatusInactive:
		return e.handleInactive(ctx, repos, tgID, "inactive")
	}

	return nil, nil
}

func (e *Engine) handleInactive(
	ctx context.Context,
	repos Store,
	tgID int64,
	reason string,
) ([]Effect, error) {
	if repos.Grants == nil {
		return nil, nil
	}

	grants, err := repos.Grants.ListEligibleForRevoke(ctx, tgID)
	if err != nil {
		return nil, err
	}

	if len(grants) == 0 {
		return nil, nil
	}

	switch e.revoke.ExpiryMode {
	case "notify_only":
		if err := repos.Audit.Append(ctx, store.AuditEntry{
			TGID:   &tgID,
			Kind:   auditExpiredNotice,
			Actor:  "system",
			Detail: "subscription inactive; notify_only mode",
		}); err != nil {
			return nil, err
		}

		return e.notifyUser(ctx, repos, tgID, messages.ExpiredNotice(),
			"expired_notice")
	case "immediate":
		return e.revokeNow(ctx, repos, tgID, reason, false)
	default:
		scheduledAt := e.now().Add(e.revoke.GracePeriod)

		created, err := repos.Revocations.CreateIfAbsent(
			ctx,
			domain.PendingRevocation{
				TGID:        tgID,
				Reason:      reason,
				ScheduledAt: scheduledAt,
			},
		)
		if err != nil {
			return nil, err
		}

		if !created {
			return nil, nil
		}

		if err := repos.Audit.Append(ctx, store.AuditEntry{
			TGID:  &tgID,
			Kind:  auditRevocationScheduled,
			Actor: "system",
			Detail: fmt.Sprintf("scheduled_at=%s reason=%s",
				scheduledAt.UTC().Format(time.RFC3339), reason),
		}); err != nil {
			return nil, err
		}

		if err := e.emitAccessLossScheduled(
			ctx, repos, tgID, scheduledAt); err != nil {
			return nil, err
		}

		effects, err := e.notifyUser(ctx, repos, tgID, messages.ExpiryWarning(scheduledAt),
			"expiry_warning:"+scheduledAt.UTC().Format(time.RFC3339))
		if err != nil {
			return nil, err
		}

		if err := repos.Revocations.MarkNotified(ctx, tgID); err != nil {
			return nil, err
		}

		return effects, nil
	}
}

// RevokeNow safely revokes current bot-admitted grants for a user.
func (e *Engine) RevokeNow(
	ctx context.Context,
	repos Store,
	tgID int64,
	reason string,
) ([]Effect, error) {
	unlock := e.locks.Lock(tgID)
	defer unlock()

	return e.revokeNow(ctx, repos, tgID, reason, true)
}

func (e *Engine) revokeNow(
	ctx context.Context,
	repos Store,
	tgID int64,
	reason string,
	finalCheck bool,
) ([]Effect, error) {
	if reason == "" {
		reason = "inactive"
	}

	if finalCheck {
		decision, err := e.revocationDecision(ctx, repos, tgID)
		if err != nil {
			return nil, err
		}

		switch decision.Status {
		case domain.StatusActive:
			return e.cancelRevocation(ctx, repos, tgID, decision)
		case domain.StatusUnknown:
			return nil, e.alertUnsafeRevocation(ctx, repos, tgID, reason)
		case domain.StatusInactive:
		}
	}

	grants, err := repos.Grants.ListEligibleForRevoke(ctx, tgID)
	if err != nil {
		return nil, err
	}

	revokedResources := make([]domain.Resource, 0, len(grants))

	for _, grant := range grants {
		ok, err := e.revokeGrant(ctx, repos, grant, reason)
		if err != nil {
			return nil, err
		}

		if ok {
			revokedResources = append(revokedResources, grant.Resource)
		}
	}

	if len(revokedResources) == 0 {
		if err := repos.Revocations.Delete(ctx, tgID); err != nil {
			return nil, err
		}

		if err := repos.Audit.Append(ctx, store.AuditEntry{
			TGID:   &tgID,
			Kind:   "access_revoke_noop",
			Actor:  "system",
			Detail: reason,
		}); err != nil {
			return nil, err
		}

		return nil, nil
	}

	if err := repos.Revocations.Delete(ctx, tgID); err != nil {
		return nil, err
	}

	if err := e.emitAccessLost(ctx, repos, tgID, revokedResources, reason,
		e.lossMode(finalCheck)); err != nil {
		return nil, err
	}

	return e.notifyUser(ctx, repos, tgID, messages.Revoked(), "revoked:"+reason)
}

func (e *Engine) revokeGrant(
	ctx context.Context,
	repos Store,
	grant domain.AccessGrant,
	reason string,
) (bool, error) {
	protected, err := e.isProtected(ctx, repos, grant)
	if err != nil {
		return false, err
	}

	if protected {
		return false, e.alertProtected(ctx, repos, grant)
	}

	ok, err := repos.Grants.Revoke(ctx, grant.TGID, grant.Resource, reason)
	if err != nil || !ok {
		return false, err
	}

	if repos.Outbox != nil {
		if err := enqueueAction(ctx, repos.Outbox, domain.ActionSoftKick,
			grant.TGID, grant.Resource, "revoke:"+reason); err != nil {
			return false, err
		}
	}

	if err := repos.Audit.Append(ctx, store.AuditEntry{
		TGID:     &grant.TGID,
		Kind:     auditAccessRevoked,
		Resource: string(grant.Resource),
		Actor:    "system",
		Detail:   reason,
	}); err != nil {
		return false, err
	}

	return true, nil
}

func (e *Engine) cancelRevocation(
	ctx context.Context,
	repos Store,
	tgID int64,
	decision domain.AccessDecision,
) ([]Effect, error) {
	if err := repos.Revocations.Delete(ctx, tgID); err != nil {
		return nil, err
	}

	if err := repos.Audit.Append(ctx, store.AuditEntry{
		TGID:   &tgID,
		Kind:   auditRevocationCancelled,
		Actor:  "system",
		Detail: "subscription is active again",
	}); err != nil {
		return nil, err
	}

	if err := e.emitAccessKept(ctx, repos, tgID, decision); err != nil {
		return nil, err
	}

	return e.notifyUser(ctx, repos, tgID, messages.AccessKept(),
		"revocation_cancelled")
}

func (e *Engine) alertUnsafeRevocation(
	ctx context.Context,
	repos Store,
	tgID int64,
	reason string,
) error {
	if repos.Alerts == nil {
		return nil
	}

	_, err := repos.Alerts.Create(ctx, store.AlertInput{
		Severity:  "warning",
		Kind:      alertUnsafeRevoke,
		Title:     "revocation blocked by unknown status",
		Detail:    fmt.Sprintf("tg_id=%d reason=%s", tgID, reason),
		TGID:      &tgID,
		DedupeKey: fmt.Sprintf("revocation_unknown:%d", tgID),
	})

	return err
}

func (e *Engine) revocationDecision(
	ctx context.Context,
	repos Store,
	tgID int64,
) (domain.AccessDecision, error) {
	if len(e.sources) == 0 {
		return e.persistedDecision(ctx, repos, tgID)
	}

	snapshot, err := e.LiveSnapshot(ctx, repos, tgID)
	if err != nil {
		return domain.AccessDecision{}, err
	}

	if err := e.applyObservations(ctx, repos, tgID, snapshot.Verdicts); err != nil {
		return domain.AccessDecision{}, err
	}

	return snapshot.Decision, nil
}

func (e *Engine) isProtected(
	ctx context.Context,
	repos Store,
	grant domain.AccessGrant,
) (bool, error) {
	if repos.Members == nil {
		return false, nil
	}

	member, err := repos.Members.GetChatMember(ctx, grant.Resource, grant.TGID)
	if err != nil {
		return false, err
	}

	if member == nil {
		return false, nil
	}

	return member.Type == models.ChatMemberTypeOwner ||
		member.Type == models.ChatMemberTypeAdministrator, nil
}

func (e *Engine) alertProtected(
	ctx context.Context,
	repos Store,
	grant domain.AccessGrant,
) error {
	if repos.Alerts == nil {
		return nil
	}

	_, err := repos.Alerts.Create(ctx, store.AlertInput{
		Severity: "warning",
		Kind:     alertProtectedAdmin,
		Title:    "protected admin lost subscription",
		Detail: fmt.Sprintf(
			"tg_id=%d resource=%s", grant.TGID, grant.Resource),
		TGID:      &grant.TGID,
		DedupeKey: fmt.Sprintf("protected_admin:%d:%s", grant.TGID, grant.Resource),
	})

	return err
}

func (e *Engine) notifyUser(
	ctx context.Context,
	repos Store,
	tgID int64,
	text string,
	marker string,
) ([]Effect, error) {
	if repos.Outbox == nil {
		return []Effect{{
			TGID:      tgID,
			Text:      text,
			ParseMode: messages.ParseModeHTML,
		}}, nil
	}

	payload, err := json.Marshal(struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
	}{Text: text, ParseMode: messages.ParseModeHTML})
	if err != nil {
		return nil, fmt.Errorf("encode user notification: %w", err)
	}

	_, _, err = repos.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type: domain.ActionSendDM,
		TGID: &tgID,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionSendDM, &tgID, nil, marker),
		PayloadJSON: payload,
	})
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func enqueueAction(
	ctx context.Context,
	outbox OutboxStore,
	actionType domain.ActionType,
	tgID int64,
	resource domain.Resource,
	marker string,
) error {
	payload, err := json.Marshal(struct {
		Reason string `json:"reason,omitempty"`
	}{Reason: marker})
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", actionType, err)
	}

	_, _, err = outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     actionType,
		TGID:     &tgID,
		Resource: &resource,
		IdempotencyKey: domain.AccessActionKey(
			actionType, &tgID, &resource, marker),
		PayloadJSON: payload,
	})

	return err
}

// RecomputeAccess re-evaluates durable local access state after callers
// have persisted fresh observations.
func (e *Engine) RecomputeAccess(
	ctx context.Context,
	repos Store,
	tgID int64,
) ([]Effect, error) {
	unlock := e.locks.Lock(tgID)
	defer unlock()

	return e.recomputeAccess(ctx, repos, tgID)
}

func eventDetail(event domain.SubscriptionEvent) string {
	detail := fmt.Sprintf("platform=%s kind=%s external_id=%s period_id=%s",
		event.Platform, event.Kind, event.ExternalID, event.PeriodID)

	if event.ProviderEvent != "" {
		detail += " provider_event=" + event.ProviderEvent
	}

	if !event.EventAt.IsZero() {
		detail += " event_at=" + event.EventAt.UTC().Format(time.RFC3339)
	}

	if event.ExpiresAt != nil {
		detail += " expires_at=" + event.ExpiresAt.UTC().Format(time.RFC3339)
	}

	return detail
}
