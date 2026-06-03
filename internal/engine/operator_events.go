package engine

import (
	"context"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

const platformWhitelist domain.Platform = "whitelist"

// opSubject is the safe subject identity carried into operator events.
type opSubject struct {
	TGID     int64
	Username string
	Label    string
}

func subjectFromUser(u domain.User) opSubject {
	return opSubject{
		TGID:     u.TGID,
		Username: u.Username,
		Label:    strings.TrimSpace(u.FirstName + " " + u.LastName),
	}
}

func subjectFromEvent(event domain.SubscriptionEvent) opSubject {
	return opSubject{
		TGID:     event.TGUserID,
		Username: event.TGUsername,
		Label:    strings.TrimSpace(event.TGFirstName + " " + event.TGLastName),
	}
}

// lossMode reports the access-loss mode only when it is unambiguous from
// local context: an immediate-mode revocation outside the final due check.
// Grace-due and manual revocations both reach revokeNow with finalCheck set
// and are indistinguishable here, so the mode is left empty rather than
// mislabeled (the renderer omits it).
func (e *Engine) lossMode(finalCheck bool) domain.LossMode {
	if !finalCheck && e.revoke.ExpiryMode == "immediate" {
		return domain.LossImmediate
	}

	return ""
}

// subject resolves the subject identity from local user state, best-effort.
func (e *Engine) subject(ctx context.Context, repos Store, tgID int64) opSubject {
	if repos.Users == nil {
		return opSubject{TGID: tgID}
	}

	user, err := repos.Users.Get(ctx, tgID)
	if err != nil {
		return opSubject{TGID: tgID}
	}

	return subjectFromUser(user)
}

func (sub opSubject) apply(ev *domain.OperatorEvent) {
	ev.TGID = sub.TGID
	ev.Username = sub.Username
	ev.UserLabel = sub.Label
}

// emit sends one operator event through the transaction-bound writer, if the
// feed is wired. A render failure inside Emit is swallowed there; only a
// persistence failure propagates.
func (e *Engine) emit(ctx context.Context, repos Store, ev domain.OperatorEvent) error {
	if repos.OperatorLog == nil || repos.Outbox == nil {
		return nil
	}

	if ev.EventTime.IsZero() {
		ev.EventTime = e.now()
	}

	return repos.OperatorLog.Emit(ctx, repos.Outbox, ev)
}

func decisionWhitelisted(decision domain.AccessDecision) bool {
	for _, reason := range decision.Reasons {
		if reason.Source == platformWhitelist && reason.Verdict == domain.VerdictActive {
			return true
		}
	}

	return false
}

// accessEpisodeMarker derives the per-episode access_granted idempotency
// anchor from current local state, shared with admission so the two emit
// points never double-log the same episode.
func (e *Engine) accessEpisodeMarker(
	ctx context.Context,
	repos Store,
	tgID int64,
	whitelisted bool,
) (string, error) {
	if repos.Subscriptions == nil {
		return domain.AccessEpisodeMarker(nil, whitelisted, e.now()), nil
	}

	subs, err := repos.Subscriptions.ListActiveByUser(ctx, tgID)
	if err != nil {
		return "", err
	}

	return domain.AccessEpisodeMarker(subs, whitelisted, e.now()), nil
}

// emitSourceEvent emits a raw per-platform subscription source event.
func (e *Engine) emitSourceEvent(
	ctx context.Context,
	repos Store,
	kind domain.OperatorEventKind,
	sub opSubject,
	event domain.SubscriptionEvent,
) error {
	if repos.OperatorLog == nil {
		return nil
	}

	ev := domain.OperatorEvent{
		Kind:          kind,
		Actor:         domain.OperatorActorProvider,
		Platform:      event.Platform,
		Tier:          event.Tier,
		ProviderEvent: event.ProviderEvent,
		ExpiresAt:     event.ExpiresAt,
		EventTime:     opEventTime(event, e.now()),
		Marker:        sourceMarker(event),
	}
	sub.apply(&ev)

	return e.emit(ctx, repos, ev)
}

// emitAccessGrantedOnTransition emits access_granted only when effective
// access moved from non-active to active. The marker is the shared episode
// anchor, so an engine transition and a later admission never double-emit.
func (e *Engine) emitAccessGrantedOnTransition(
	ctx context.Context,
	repos Store,
	tgID int64,
	priorStatus domain.EffectiveStatus,
) error {
	if repos.OperatorLog == nil || priorStatus == domain.StatusActive {
		return nil
	}

	decision, err := e.persistedDecision(ctx, repos, tgID)
	if err != nil {
		return err
	}

	if decision.Status != domain.StatusActive {
		return nil
	}

	marker, err := e.accessEpisodeMarker(ctx, repos, tgID, decisionWhitelisted(decision))
	if err != nil {
		return err
	}

	ev := domain.OperatorEvent{
		Kind:          domain.OpAccessGranted,
		Actor:         domain.OperatorActorProvider,
		Method:        domain.AdmissionProvider,
		ActiveSources: domain.ActiveSourceReasons(decision),
		Marker:        marker,
		EventTime:     e.now(),
	}
	e.subject(ctx, repos, tgID).apply(&ev)

	return e.emit(ctx, repos, ev)
}

func (e *Engine) emitAccessKept(
	ctx context.Context,
	repos Store,
	tgID int64,
	decision domain.AccessDecision,
) error {
	ev := domain.OperatorEvent{
		Kind:          domain.OpAccessKept,
		Actor:         domain.OperatorActorSystem,
		ActiveSources: domain.ActiveSourceReasons(decision),
		Marker:        "revocation_cancelled",
		EventTime:     e.now(),
	}
	e.subject(ctx, repos, tgID).apply(&ev)

	return e.emit(ctx, repos, ev)
}

func (e *Engine) emitAccessLossScheduled(
	ctx context.Context,
	repos Store,
	tgID int64,
	scheduledAt time.Time,
) error {
	scheduled := scheduledAt
	ev := domain.OperatorEvent{
		Kind:        domain.OpAccessLossScheduled,
		Actor:       domain.OperatorActorSystem,
		LossMode:    domain.LossGrace,
		ScheduledAt: &scheduled,
		Marker:      "loss_scheduled:" + scheduledAt.UTC().Format(time.RFC3339Nano),
		EventTime:   e.now(),
	}
	e.subject(ctx, repos, tgID).apply(&ev)

	return e.emit(ctx, repos, ev)
}

func (e *Engine) emitAccessLost(
	ctx context.Context,
	repos Store,
	tgID int64,
	resources []domain.Resource,
	reason string,
	mode domain.LossMode,
) error {
	if repos.OperatorLog == nil || len(resources) == 0 {
		return nil
	}

	hardBan := false

	if repos.Users != nil {
		if user, err := repos.Users.Get(ctx, tgID); err == nil {
			hardBan = user.Banned
		}
	}

	ev := domain.OperatorEvent{
		Kind:      domain.OpAccessLost,
		Actor:     domain.OperatorActorSystem,
		Resources: resources,
		LossMode:  mode,
		HardBan:   hardBan,
		Reason:    reason,
		Marker:    "access_lost:" + reason + ":" + opResourcesMarker(resources),
		EventTime: e.now(),
	}
	e.subject(ctx, repos, tgID).apply(&ev)

	return e.emit(ctx, repos, ev)
}

func opEventTime(event domain.SubscriptionEvent, fallback time.Time) time.Time {
	if !event.EventAt.IsZero() {
		return event.EventAt
	}

	if !event.OccurredAt.IsZero() {
		return event.OccurredAt
	}

	return fallback
}

// sourceMarker is the source-stable idempotency token for a subscription
// source event: the provider event time when present, else the platform and
// kind, never process wall-clock.
func sourceMarker(event domain.SubscriptionEvent) string {
	parts := []string{string(event.Platform), string(event.Kind)}

	if event.ProviderEvent != "" {
		parts = append(parts, "pe:"+event.ProviderEvent)
	}

	if !event.EventAt.IsZero() {
		parts = append(parts, "at:"+event.EventAt.UTC().Format(time.RFC3339Nano))
	} else if !event.OccurredAt.IsZero() {
		parts = append(parts, "at:"+event.OccurredAt.UTC().Format(time.RFC3339Nano))
	}

	if event.ExternalID != "" {
		parts = append(parts, "eid:"+event.ExternalID)
	}

	return strings.Join(parts, ":")
}

func opResourcesMarker(resources []domain.Resource) string {
	labels := make([]string, 0, len(resources))
	for _, resource := range resources {
		labels = append(labels, string(resource))
	}

	return strings.Join(labels, ",")
}
