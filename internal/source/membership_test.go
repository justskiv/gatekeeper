package source

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
)

type fakeMemberChecker struct {
	member *models.ChatMember
	err    error
}

func (c fakeMemberChecker) GetChatMember(
	context.Context, int64, int64,
) (*models.ChatMember, error) {
	return c.member, c.err
}

type fakeLedger struct {
	sub domain.Subscription
	ok  bool
	err error
}

func (l fakeLedger) GetActive(
	context.Context,
	int64,
	domain.Platform,
) (domain.Subscription, bool, error) {
	return l.sub, l.ok, l.err
}

func TestMembershipVerdictMapsTelegramStatuses(t *testing.T) {
	tests := []struct {
		name   string
		member *models.ChatMember
		want   domain.Verdict
	}{
		{
			name: "member active",
			member: &models.ChatMember{
				Type:   models.ChatMemberTypeMember,
				Member: &models.ChatMemberMember{},
			},
			want: domain.VerdictActive,
		},
		{
			name: "owner active",
			member: &models.ChatMember{
				Type: models.ChatMemberTypeOwner,
				Owner: &models.ChatMemberOwner{
					User: &models.User{ID: 42},
				},
			},
			want: domain.VerdictActive,
		},
		{
			name: "administrator active",
			member: &models.ChatMember{
				Type: models.ChatMemberTypeAdministrator,
				Administrator: &models.ChatMemberAdministrator{
					User: models.User{ID: 42},
				},
			},
			want: domain.VerdictActive,
		},
		{
			name: "restricted member active",
			member: &models.ChatMember{
				Type: models.ChatMemberTypeRestricted,
				Restricted: &models.ChatMemberRestricted{
					IsMember: true,
				},
			},
			want: domain.VerdictActive,
		},
		{
			name: "left inactive",
			member: &models.ChatMember{
				Type: models.ChatMemberTypeLeft,
				Left: &models.ChatMemberLeft{},
			},
			want: domain.VerdictInactive,
		},
		{
			name: "restricted non-member inactive",
			member: &models.ChatMember{
				Type: models.ChatMemberTypeRestricted,
				Restricted: &models.ChatMemberRestricted{
					IsMember: false,
				},
			},
			want: domain.VerdictInactive,
		},
		{
			name: "banned inactive",
			member: &models.ChatMember{
				Type: models.ChatMemberTypeBanned,
				Banned: &models.ChatMemberBanned{
					User: &models.User{ID: 42},
				},
			},
			want: domain.VerdictInactive,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := NewMembership(domain.PlatformBoosty, -1001,
				fakeMemberChecker{member: tt.member})

			got, err := source.Verdict(context.Background(), 42)
			if err != nil {
				t.Fatalf("Verdict: %v", err)
			}

			if got.Verdict != tt.want {
				t.Fatalf("verdict = %s, want %s", got.Verdict, tt.want)
			}
		})
	}
}

func TestMembershipVerdictReturnsUnknownOnCheckerError(t *testing.T) {
	source := NewMembership(domain.PlatformBoosty, -1001,
		fakeMemberChecker{err: errors.New("telegram unavailable")})

	got, err := source.Verdict(context.Background(), 42)
	if err != nil {
		t.Fatalf("Verdict: %v", err)
	}

	if got.Verdict != domain.VerdictUnknown {
		t.Fatalf("verdict = %s, want unknown", got.Verdict)
	}
}

func TestTributeVerdictCombinesMembershipAndLedger(t *testing.T) {
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)

	tests := []struct {
		name      string
		member    *models.ChatMember
		ledger    fakeLedger
		want      domain.Verdict
		wantUntil *time.Time
	}{
		{
			name:   "membership active wins without ledger",
			member: memberChatMember(),
			ledger: fakeLedger{},
			want:   domain.VerdictActive,
		},
		{
			name:   "ledger active wins over inactive membership",
			member: leftChatMember(),
			ledger: fakeLedger{
				sub: domain.Subscription{ExpiresAt: &expires},
				ok:  true,
			},
			want:      domain.VerdictActive,
			wantUntil: &expires,
		},
		{
			name:   "inactive membership and no ledger signal is inactive",
			member: leftChatMember(),
			ledger: fakeLedger{},
			want:   domain.VerdictInactive,
		},
		{
			name:   "inactive membership and expired ledger is inactive",
			member: leftChatMember(),
			ledger: fakeLedger{
				sub: domain.Subscription{ExpiresAt: new(now.Add(-time.Hour))},
				ok:  true,
			},
			want: domain.VerdictInactive,
		},
		{
			name:   "inactive membership and ledger error is unknown",
			member: leftChatMember(),
			ledger: fakeLedger{err: errors.New("database unavailable")},
			want:   domain.VerdictUnknown,
		},
		{
			name:   "membership error and no ledger signal is unknown",
			member: nil,
			ledger: fakeLedger{},
			want:   domain.VerdictUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := fakeMemberChecker{member: tt.member}
			if tt.member == nil {
				checker.err = errors.New("telegram unavailable")
			}

			source := NewMembership(
				domain.PlatformTribute,
				-1002,
				checker,
				WithLedger(tt.ledger),
				WithClock(func() time.Time { return now }),
			)

			got, err := source.Verdict(context.Background(), 42)
			if err != nil {
				t.Fatalf("Verdict: %v", err)
			}

			if got.Verdict != tt.want {
				t.Fatalf("verdict = %s, want %s", got.Verdict, tt.want)
			}

			if tt.wantUntil != nil &&
				(got.Until == nil || !got.Until.Equal(*tt.wantUntil)) {
				t.Fatalf("until = %v, want %v", got.Until, *tt.wantUntil)
			}
		})
	}
}

func memberChatMember() *models.ChatMember {
	return &models.ChatMember{
		Type: models.ChatMemberTypeMember,
		Member: &models.ChatMemberMember{
			User: &models.User{ID: 42},
		},
	}
}

func leftChatMember() *models.ChatMember {
	return &models.ChatMember{
		Type: models.ChatMemberTypeLeft,
		Left: &models.ChatMemberLeft{
			User: &models.User{ID: 42},
		},
	}
}
