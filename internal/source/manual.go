package source

import (
	"context"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
)

type whitelistChecker interface {
	Has(ctx context.Context, tgID int64) (bool, error)
}

type manualSubscriptionReader interface {
	GetActive(
		ctx context.Context,
		tgID int64,
		platform domain.Platform,
	) (domain.Subscription, bool, error)
}

// Manual checks manual operator overrides: whitelist and manual periods.
type Manual struct {
	whitelist     whitelistChecker
	subscriptions manualSubscriptionReader
	now           func() time.Time
}

// ManualOption configures Manual.
type ManualOption func(*Manual)

// WithManualClock replaces time.Now for tests.
func WithManualClock(now func() time.Time) ManualOption {
	return func(m *Manual) {
		m.now = now
	}
}

// NewManual returns the manual override source.
func NewManual(
	whitelist whitelistChecker,
	subscriptions manualSubscriptionReader,
	opts ...ManualOption,
) *Manual {
	m := &Manual{
		whitelist:     whitelist,
		subscriptions: subscriptions,
		now:           time.Now,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Platform returns the source platform.
func (m *Manual) Platform() domain.Platform {
	return domain.PlatformManual
}

// Verdict returns active for explicit manual access, otherwise no_signal.
func (m *Manual) Verdict(
	ctx context.Context, tgID int64,
) (domain.SourceVerdict, error) {
	whitelisted, err := m.whitelist.Has(ctx, tgID)
	if err != nil {
		return domain.SourceVerdict{
			Source:  domain.PlatformManual,
			Verdict: domain.VerdictUnknown,
			Detail:  messages.ReasonWhitelistReadFailed(),
		}, nil
	}
	if whitelisted {
		return domain.SourceVerdict{
			Source:  domain.PlatformManual,
			Verdict: domain.VerdictActive,
			Detail:  messages.ReasonWhitelist(),
		}, nil
	}

	sub, ok, err := m.subscriptions.GetActive(ctx, tgID, domain.PlatformManual)
	if err != nil {
		return domain.SourceVerdict{
			Source:  domain.PlatformManual,
			Verdict: domain.VerdictUnknown,
			Detail:  messages.ReasonManualSubscriptionReadFailed(),
		}, nil
	}
	if ok && (sub.ExpiresAt == nil || m.now().Before(*sub.ExpiresAt)) {
		return domain.SourceVerdict{
			Source:  domain.PlatformManual,
			Verdict: domain.VerdictActive,
			Detail:  messages.ReasonManualSubscriptionActive(),
			Until:   sub.ExpiresAt,
		}, nil
	}
	return domain.SourceVerdict{
		Source:  domain.PlatformManual,
		Verdict: domain.VerdictNoSignal,
		Detail:  messages.ReasonManualNoBasis(),
	}, nil
}
