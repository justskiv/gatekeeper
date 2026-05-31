// Package source contains concrete subscription sources used by the
// status engine.
package source

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
)

type memberChecker interface {
	GetChatMember(ctx context.Context, chatID, userID int64) (*models.ChatMember, error)
}

type ledgerReader interface {
	GetActive(
		ctx context.Context,
		tgID int64,
		platform domain.Platform,
	) (domain.Subscription, bool, error)
}

// Membership checks live Telegram membership in one source chat.
type Membership struct {
	platform domain.Platform
	chatID   int64
	checker  memberChecker
	ledger   ledgerReader
	now      func() time.Time
}

// MembershipOption configures a membership source.
type MembershipOption func(*Membership)

// WithLedger adds the local ledger signal used by Tribute.
func WithLedger(ledger ledgerReader) MembershipOption {
	return func(m *Membership) {
		m.ledger = ledger
	}
}

// WithClock replaces time.Now for tests.
func WithClock(now func() time.Time) MembershipOption {
	return func(m *Membership) {
		m.now = now
	}
}

// NewMembership returns a Telegram membership source.
func NewMembership(
	platform domain.Platform,
	chatID int64,
	checker memberChecker,
	opts ...MembershipOption,
) *Membership {
	m := &Membership{
		platform: platform,
		chatID:   chatID,
		checker:  checker,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Platform returns the source platform.
func (m *Membership) Platform() domain.Platform {
	return m.platform
}

// Verdict probes Telegram and returns one four-valued source verdict.
func (m *Membership) Verdict(
	ctx context.Context, tgID int64,
) (domain.SourceVerdict, error) {
	memberVerdict := m.memberVerdict(ctx, tgID)
	if m.platform != domain.PlatformTribute || m.ledger == nil {
		return memberVerdict, nil
	}
	ledgerVerdict := m.ledgerVerdict(ctx, tgID)
	return combineTribute(memberVerdict, ledgerVerdict), nil
}

func (m *Membership) memberVerdict(
	ctx context.Context, tgID int64,
) domain.SourceVerdict {
	member, err := m.checker.GetChatMember(ctx, m.chatID, tgID)
	if err != nil {
		return domain.SourceVerdict{
			Source:  m.platform,
			Verdict: domain.VerdictUnknown,
			Detail:  messages.ReasonMembershipCheckFailed(),
		}
	}
	if MemberInChat(member) {
		return domain.SourceVerdict{
			Source:  m.platform,
			Verdict: domain.VerdictActive,
			Detail:  messages.ReasonMembershipInChat(m.chatID),
		}
	}
	return domain.SourceVerdict{
		Source:  m.platform,
		Verdict: domain.VerdictInactive,
		Detail:  messages.ReasonMembershipNotInChat(m.chatID),
	}
}

func (m *Membership) ledgerVerdict(
	ctx context.Context, tgID int64,
) domain.SourceVerdict {
	sub, ok, err := m.ledger.GetActive(ctx, tgID, m.platform)
	if err != nil {
		return domain.SourceVerdict{
			Source:  m.platform,
			Verdict: domain.VerdictUnknown,
			Detail:  messages.ReasonLedgerReadFailed(),
		}
	}
	if !ok {
		return domain.SourceVerdict{
			Source:  m.platform,
			Verdict: domain.VerdictNoSignal,
			Detail:  messages.ReasonLedgerNoActive(),
		}
	}
	if sub.ExpiresAt != nil && !m.now().Before(*sub.ExpiresAt) {
		return domain.SourceVerdict{
			Source:  m.platform,
			Verdict: domain.VerdictInactive,
			Detail:  messages.ReasonLedgerExpired(),
			Until:   sub.ExpiresAt,
		}
	}
	return domain.SourceVerdict{
		Source:  m.platform,
		Verdict: domain.VerdictActive,
		Detail:  messages.ReasonLedgerActive(),
		Until:   sub.ExpiresAt,
	}
}

func combineTribute(a, b domain.SourceVerdict) domain.SourceVerdict {
	switch {
	case a.Verdict == domain.VerdictActive || b.Verdict == domain.VerdictActive:
		return domain.SourceVerdict{
			Source:  domain.PlatformTribute,
			Verdict: domain.VerdictActive,
			Detail:  a.Detail + "; " + b.Detail,
			Until:   firstUntil(a.Until, b.Until),
		}
	case a.Verdict == domain.VerdictInactive &&
		(b.Verdict == domain.VerdictInactive ||
			b.Verdict == domain.VerdictNoSignal):
		return domain.SourceVerdict{
			Source:  domain.PlatformTribute,
			Verdict: domain.VerdictInactive,
			Detail:  a.Detail + "; " + b.Detail,
			Until:   firstUntil(a.Until, b.Until),
		}
	default:
		return domain.SourceVerdict{
			Source:  domain.PlatformTribute,
			Verdict: domain.VerdictUnknown,
			Detail:  a.Detail + "; " + b.Detail,
			Until:   firstUntil(a.Until, b.Until),
		}
	}
}

func firstUntil(a, b *time.Time) *time.Time {
	// Membership verdicts do not set Until today; keep the left operand
	// first so a future source-specific expiry can still win explicitly.
	if a != nil {
		return a
	}
	return b
}

// MemberInChat maps Telegram chat-member statuses to subscription presence.
func MemberInChat(member *models.ChatMember) bool {
	if member == nil {
		return false
	}
	switch member.Type {
	case models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator,
		models.ChatMemberTypeMember:
		return true
	case models.ChatMemberTypeRestricted:
		return member.Restricted != nil && member.Restricted.IsMember
	default:
		return false
	}
}
