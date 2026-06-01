package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	signalEvent    = "event"
	signalOnDemand = "on_demand"

	auditSubscriptionActivated = "subscription_activated"
	auditSubscriptionExpired   = "subscription_expired"
	auditSubscriptionCancelled = "cancelled_subscription"
	auditRevocationCancelled   = "revocation_cancelled"
	auditStatusUnknown         = "status_unknown"
)

// Engine applies subscription events and computes access status.
type Engine struct {
	sources []SubscriptionSource
	locks   *KeyedMutex
	now     func() time.Time
}

// Option configures Engine.
type Option func(*Engine)

// WithClock replaces time.Now for tests.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) {
		e.now = now
	}
}

// New returns a status engine.
func New(sources []SubscriptionSource, opts ...Option) *Engine {
	e := &Engine{
		sources: append([]SubscriptionSource(nil), sources...),
		locks:   NewKeyedMutex(),
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(e)
	}

	return e
}

// ApplyObservations persists live source observations inside tx2.
func (e *Engine) ApplyObservations(
	ctx context.Context,
	repos Store,
	tgID int64,
	verdicts []domain.SourceVerdict,
) error {
	unlock := e.locks.Lock(tgID)
	defer unlock()

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

// HandleEvent applies one normalized subscription event inside tx2.
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

	tgID := event.TGUserID
	switch event.Kind {
	case domain.EventActivated:
		if _, err := repos.Subscriptions.UpsertActive(ctx, domain.Subscription{
			TGID:        tgID,
			Platform:    event.Platform,
			Status:      domain.SubActive,
			ExternalID:  event.ExternalID,
			PeriodID:    event.PeriodID,
			Tier:        event.Tier,
			StartedAt:   now,
			ExpiresAt:   event.ExpiresAt,
			LastSignal:  signalEvent,
			LastEventAt: &now,
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
	case domain.EventDeactivated:
		ok, err := repos.Subscriptions.ExpireActive(
			ctx, tgID, event.Platform, now, signalEvent)
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
	default:
		return nil, fmt.Errorf("unknown subscription event kind %q", event.Kind)
	}

	effects, err := e.recomputeAccess(ctx, repos, tgID)
	if err != nil {
		return nil, err
	}

	return effects, nil
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

			return []Effect{{TGID: tgID, Text: messages.AccessKept()}}, nil
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
		// TODO: Phase 06 will enqueue durable revoke actions.
	}

	return nil, nil
}

// RecomputeAccess re-evaluates durable local access state after callers
// have persisted fresh observations.
func (e *Engine) RecomputeAccess(
	ctx context.Context,
	repos Store,
	tgID int64,
) ([]Effect, error) {
	return e.recomputeAccess(ctx, repos, tgID)
}

func eventDetail(event domain.SubscriptionEvent) string {
	return fmt.Sprintf("platform=%s kind=%s external_id=%s period_id=%s",
		event.Platform, event.Kind, event.ExternalID, event.PeriodID)
}
