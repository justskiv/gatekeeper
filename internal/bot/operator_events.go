package bot

import (
	"context"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// emitManual emits a confirmed owner-command operator event. The marker is the
// confirmed action id, so a replayed confirm callback resolves to the same
// durable row instead of enqueuing a duplicate.
func (h *UserCommands) emitManual(
	ctx context.Context,
	action *adminAction,
	kind domain.OperatorEventKind,
) error {
	if h.deps.OperatorLog == nil || h.deps.Outbox == nil || action.TargetID == nil {
		return nil
	}

	ev := domain.OperatorEvent{
		Kind:         kind,
		Actor:        domain.OperatorActorAdmin,
		Method:       domain.AdmissionAdmin,
		ManualReason: action.Reason,
		ExpiresAt:    action.ExpiresAt,
		Marker:       "manual:" + action.ID,
	}
	h.applyOpSubject(ctx, &ev, *action.TargetID)

	return h.deps.OperatorLog.Emit(ctx, h.deps.Outbox, ev)
}

// emitManualAccessGranted emits access_granted with source manual when a
// /grant moves effective access from non-active to active. The shared episode
// marker dedupes against any engine transition for the same episode.
func (h *UserCommands) emitManualAccessGranted(
	ctx context.Context,
	tgID int64,
	priorStatus domain.EffectiveStatus,
	action *adminAction,
) error {
	if h.deps.OperatorLog == nil || h.deps.Outbox == nil ||
		h.deps.StatusEngine == nil || priorStatus == domain.StatusActive {
		return nil
	}

	decision, err := h.deps.StatusEngine.PersistedDecision(
		ctx, engineStore(h.deps), tgID)
	if err != nil {
		return err
	}

	if decision.Status != domain.StatusActive {
		return nil
	}

	marker, err := h.accessEpisodeMarker(ctx, tgID)
	if err != nil {
		return err
	}

	ev := domain.OperatorEvent{
		Kind:          domain.OpAccessGranted,
		Actor:         domain.OperatorActorAdmin,
		Method:        domain.AdmissionAdmin,
		ActiveSources: domain.ActiveSourceReasons(decision),
		ManualReason:  action.Reason,
		Marker:        marker,
	}
	h.applyOpSubject(ctx, &ev, tgID)

	return h.deps.OperatorLog.Emit(ctx, h.deps.Outbox, ev)
}

// emitManualAccessLost emits access_lost for a hard-ban that revoked grants,
// identifying hard-ban as the overriding reason.
func (h *UserCommands) emitManualAccessLost(
	ctx context.Context,
	tgID int64,
	resources []domain.Resource,
	action *adminAction,
) error {
	if h.deps.OperatorLog == nil || h.deps.Outbox == nil || len(resources) == 0 {
		return nil
	}

	ev := domain.OperatorEvent{
		Kind:      domain.OpAccessLost,
		Actor:     domain.OperatorActorAdmin,
		Resources: resources,
		LossMode:  domain.LossManual,
		HardBan:   true,
		Marker:    "manual_ban_loss:" + action.ID,
	}
	h.applyOpSubject(ctx, &ev, tgID)

	return h.deps.OperatorLog.Emit(ctx, h.deps.Outbox, ev)
}

// priorStatus reads the effective access status before a manual change so a
// /grant can detect a real non-active→active transition. With no engine wired
// it reports unknown, which suppresses the transition emit.
func (h *UserCommands) priorStatus(
	ctx context.Context,
	tgID int64,
) (domain.EffectiveStatus, error) {
	if h.deps.OperatorLog == nil || h.deps.StatusEngine == nil {
		return domain.StatusUnknown, nil
	}

	decision, err := h.deps.StatusEngine.PersistedDecision(
		ctx, engineStore(h.deps), tgID)
	if err != nil {
		return "", err
	}

	return decision.Status, nil
}

// applyOpSubject fills the subject identity, reading cached user state
// best-effort so a missing row still yields a valid (id-only) event.
func (h *UserCommands) applyOpSubject(
	ctx context.Context,
	ev *domain.OperatorEvent,
	tgID int64,
) {
	ev.TGID = tgID

	if h.deps.Users == nil {
		return
	}

	user, err := h.deps.Users.Get(ctx, tgID)
	if err != nil {
		return
	}

	ev.Username = user.Username
	ev.UserLabel = strings.TrimSpace(user.FirstName + " " + user.LastName)
}

// accessEpisodeMarker derives the per-episode access_granted anchor from local
// state, shared with the engine and admission so the emit points never
// double-log the same active episode.
func (h *UserCommands) accessEpisodeMarker(
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

	// time.Now only bounds subscription expiry; the marker value itself is the
	// stable StartedAt, so this stays source-stable across retries.
	return domain.AccessEpisodeMarker(subs, whitelisted, time.Now()), nil
}
