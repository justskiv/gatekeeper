package admission

import (
	"context"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// emitOperatorEvent sends one operator event through the transaction-bound
// writer, if the feed is wired. Render failures are swallowed inside Emit;
// only a persistence failure propagates.
func (h *Handler) emitOperatorEvent(
	ctx context.Context,
	ev domain.OperatorEvent,
) error {
	if h.deps.OperatorLog == nil || h.deps.Outbox == nil {
		return nil
	}

	if ev.EventTime.IsZero() {
		ev.EventTime = h.now()
	}

	return h.deps.OperatorLog.Emit(ctx, h.deps.Outbox, ev)
}

func subjectLabel(u domain.User) string {
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

func applySubject(ev *domain.OperatorEvent, u domain.User) {
	ev.TGID = u.TGID
	ev.Username = u.Username
	ev.UserLabel = subjectLabel(u)
}

// activeAccessSources lists the platforms currently granting the user access,
// from already-stored data only (no network probe). It builds the same active
// reasons the engine would and runs them through the shared normalizer, so
// whitelist is reported as manual and duplicates collapse — consistent with
// every other emit point.
func (h *Handler) activeAccessSources(
	ctx context.Context,
	tgID int64,
) ([]domain.Platform, error) {
	var reasons []domain.AccessReason

	if h.deps.Whitelist != nil {
		ok, err := h.deps.Whitelist.Has(ctx, tgID)
		if err != nil {
			return nil, err
		}

		if ok {
			reasons = append(reasons, domain.AccessReason{
				Source:  domain.PlatformWhitelist,
				Verdict: domain.VerdictActive,
			})
		}
	}

	if h.deps.Subscriptions != nil {
		subs, err := h.deps.Subscriptions.ListActiveByUser(ctx, tgID)
		if err != nil {
			return nil, err
		}

		now := h.now()
		for _, sub := range subs {
			if sub.ExpiresAt != nil && !now.Before(*sub.ExpiresAt) {
				continue
			}

			reasons = append(reasons, domain.AccessReason{
				Source:  sub.Platform,
				Verdict: domain.VerdictActive,
			})
		}
	}

	return domain.ActiveSourceReasons(domain.AccessDecision{Reasons: reasons}), nil
}

// accessEpisodeMarker derives the per-episode access_granted anchor, shared
// with the engine transition so the two emit points never double-log the
// same active episode.
func (h *Handler) accessEpisodeMarker(
	ctx context.Context,
	tgID int64,
) (string, error) {
	whitelisted := false

	if h.deps.Whitelist != nil {
		ok, err := h.deps.Whitelist.Has(ctx, tgID)
		if err != nil {
			return "", err
		}

		whitelisted = ok
	}

	var subs []domain.Subscription

	if h.deps.Subscriptions != nil {
		got, err := h.deps.Subscriptions.ListActiveByUser(ctx, tgID)
		if err != nil {
			return "", err
		}

		subs = got
	}

	return domain.AccessEpisodeMarker(subs, whitelisted, h.now()), nil
}

// emitAccessGranted emits access_granted for an admission that establishes
// eligibility. The shared episode marker makes it idempotent against the
// engine transition and repeat /start.
func (h *Handler) emitAccessGranted(
	ctx context.Context,
	user domain.User,
	fallback bool,
) error {
	if h.deps.OperatorLog == nil {
		return nil
	}

	sources, err := h.activeAccessSources(ctx, user.TGID)
	if err != nil {
		return err
	}

	marker, err := h.accessEpisodeMarker(ctx, user.TGID)
	if err != nil {
		return err
	}

	ev := domain.OperatorEvent{
		Kind:          domain.OpAccessGranted,
		Actor:         domain.OperatorActorUser,
		Method:        domain.AdmissionBotLink,
		ActiveSources: sources,
		Fallback:      fallback,
		InviteMode:    h.cfg.InviteMode,
		Resources:     h.configuredResources(),
		Marker:        marker,
	}
	applySubject(&ev, user)

	return h.emitOperatorEvent(ctx, ev)
}

// emitMembership emits a club membership event (join/leave, subscribe/
// unsubscribe). On a join it includes the admission method, a safe external
// actor label when present, and the joining user's active access sources.
func (h *Handler) emitMembership(
	ctx context.Context,
	update MembershipUpdate,
	user domain.User,
	method domain.AdmissionMethod,
) error {
	if h.deps.OperatorLog == nil {
		return nil
	}

	kind := membershipKind(update.Resource, update.Joined)
	if kind == "" {
		return nil
	}

	resource := update.Resource
	ev := domain.OperatorEvent{
		Kind:      kind,
		Actor:     domain.OperatorActorUser,
		Resource:  &resource,
		EventTime: update.EventDate,
		Marker:    membershipMarker(update),
	}
	applySubject(&ev, user)

	if update.Joined {
		ev.Method = method

		if method == domain.AdmissionExternal && update.Actor != nil {
			ev.ActorLabel = actorLabel(*update.Actor)
		}

		sources, err := h.activeAccessSources(ctx, user.TGID)
		if err != nil {
			return err
		}

		ev.ActiveSources = sources
	}

	return h.emitOperatorEvent(ctx, ev)
}

// emitBannedJoinAttempt reports a hard-banned user entering a managed resource
// with a hard-ban removal enqueued.
func (h *Handler) emitBannedJoinAttempt(
	ctx context.Context,
	update MembershipUpdate,
	user domain.User,
) error {
	if h.deps.OperatorLog == nil {
		return nil
	}

	resource := update.Resource
	ev := domain.OperatorEvent{
		Kind:      domain.OpBannedJoinAttempt,
		Actor:     domain.OperatorActorSystem,
		Resource:  &resource,
		EventTime: update.EventDate,
		Marker:    "banned_join:" + string(resource) + ":" + eventDateMarker(update.EventDate),
	}
	applySubject(&ev, user)

	return h.emitOperatorEvent(ctx, ev)
}

func membershipKind(
	resource domain.Resource,
	joined bool,
) domain.OperatorEventKind {
	switch resource {
	case domain.ResourceChat:
		if joined {
			return domain.OpClubChatJoined
		}

		return domain.OpClubChatLeft
	case domain.ResourceChannel:
		if joined {
			return domain.OpClubChannelSubscribed
		}

		return domain.OpClubChannelUnsubscribed
	default:
		return ""
	}
}

func membershipMarker(update MembershipUpdate) string {
	state := "left"
	if update.Joined {
		state = "joined"
	}

	return strings.Join([]string{
		string(update.Resource), state, eventDateMarker(update.EventDate),
	}, ":")
}

func eventDateMarker(t time.Time) string {
	if t.IsZero() {
		return "nodate"
	}

	return t.UTC().Format(time.RFC3339Nano)
}

// actorLabel renders a safe label for an external actor: display name, else
// @username, else the actor's id. Escaping happens in the renderer.
func actorLabel(actor domain.User) string {
	if name := subjectLabel(actor); name != "" {
		return name
	}

	if actor.Username != "" {
		return "@" + actor.Username
	}

	return ""
}
