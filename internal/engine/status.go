package engine

import (
	"context"
	"errors"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	sourceBan       domain.Platform = "ban"
	sourceWhitelist domain.Platform = "whitelist"
)

// Aggregate merges source verdicts into an effective access decision.
func Aggregate(
	verdicts []domain.SourceVerdict,
	banned bool,
) (domain.EffectiveStatus, domain.AccessDecision) {
	reasons := make([]domain.AccessReason, 0, len(verdicts)+1)
	seenActive := false
	seenUnknown := false
	seenInactive := false

	for _, verdict := range verdicts {
		reasons = append(reasons, domain.AccessReason(verdict))

		switch verdict.Verdict {
		case domain.VerdictActive:
			seenActive = true
		case domain.VerdictUnknown:
			seenUnknown = true
		case domain.VerdictInactive:
			seenInactive = true
		}
	}

	var status domain.EffectiveStatus
	switch {
	case banned:
		status = domain.StatusInactive
	case seenActive:
		status = domain.StatusActive
	case seenUnknown:
		status = domain.StatusUnknown
	case seenInactive:
		status = domain.StatusInactive
	default:
		status = domain.StatusInactive
	}
	if banned {
		reasons = append(reasons, domain.AccessReason{
			Source:  sourceBan,
			Verdict: domain.VerdictInactive,
			Detail:  messages.ReasonHardBan(),
		})
	}

	return status, domain.AccessDecision{
		Status:  status,
		Allowed: status == domain.StatusActive,
		Reasons: reasons,
	}
}

// LiveSnapshot collects source verdicts outside tx2 and aggregates them.
func (e *Engine) LiveSnapshot(
	ctx context.Context,
	repos Store,
	tgID int64,
) (Snapshot, error) {
	verdicts := make([]domain.SourceVerdict, 0, len(e.sources))
	for _, source := range e.sources {
		verdict, err := source.Verdict(ctx, tgID)
		if err != nil {
			verdict = domain.SourceVerdict{
				Source:  source.Platform(),
				Verdict: domain.VerdictUnknown,
				Detail:  messages.ReasonSourceError(),
			}
		}
		verdicts = append(verdicts, verdict)
	}

	banned := false
	user, err := repos.Users.Get(ctx, tgID)
	switch {
	case err == nil:
		banned = user.Banned
	case errors.Is(err, store.ErrNotFound):
	default:
		return Snapshot{}, err
	}

	status, decision := Aggregate(verdicts, banned)
	decision.TGID = tgID
	decision.Status = status
	return Snapshot{TGID: tgID, Verdicts: verdicts, Decision: decision}, nil
}

func (e *Engine) persistedDecision(
	ctx context.Context,
	repos Store,
	tgID int64,
) (domain.AccessDecision, error) {
	banned := false
	user, err := repos.Users.Get(ctx, tgID)
	switch {
	case err == nil:
		banned = user.Banned
	case errors.Is(err, store.ErrNotFound):
	default:
		return domain.AccessDecision{}, err
	}

	verdicts := make([]domain.SourceVerdict, 0, 3)
	whitelisted, err := repos.Whitelist.Has(ctx, tgID)
	if err != nil {
		return domain.AccessDecision{}, err
	}
	if whitelisted {
		verdicts = append(verdicts, domain.SourceVerdict{
			Source:  sourceWhitelist,
			Verdict: domain.VerdictActive,
			Detail:  messages.ReasonWhitelist(),
		})
	}

	subs, err := repos.Subscriptions.ListActiveByUser(ctx, tgID)
	if err != nil {
		return domain.AccessDecision{}, err
	}
	for _, sub := range subs {
		verdict := domain.VerdictActive
		detail := messages.ReasonLocalActiveSubscription()
		if sub.ExpiresAt != nil && !e.now().Before(*sub.ExpiresAt) {
			verdict = domain.VerdictInactive
			detail = messages.ReasonLocalExpiredSubscription()
		}
		verdicts = append(verdicts, domain.SourceVerdict{
			Source:  sub.Platform,
			Verdict: verdict,
			Detail:  detail,
			Until:   sub.ExpiresAt,
		})
	}
	if len(verdicts) == 0 {
		verdicts = append(verdicts, domain.SourceVerdict{
			Source:  domain.PlatformManual,
			Verdict: domain.VerdictNoSignal,
			Detail:  messages.ReasonLocalNoBasis(),
		})
	}

	_, decision := Aggregate(verdicts, banned)
	decision.TGID = tgID
	return decision, nil
}

// PersistedDecision computes access from local state without network probes.
func (e *Engine) PersistedDecision(
	ctx context.Context,
	repos Store,
	tgID int64,
) (domain.AccessDecision, error) {
	return e.persistedDecision(ctx, repos, tgID)
}
