package source

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

type fakeWhitelist struct {
	has bool
	err error
}

func (w fakeWhitelist) Has(context.Context, int64) (bool, error) {
	return w.has, w.err
}

type fakeManualSubs struct {
	sub domain.Subscription
	ok  bool
	err error
}

func (s fakeManualSubs) GetActive(
	context.Context,
	int64,
	domain.Platform,
) (domain.Subscription, bool, error) {
	return s.sub, s.ok, s.err
}

func TestManualVerdictWhitelistActive(t *testing.T) {
	source := NewManual(fakeWhitelist{has: true}, fakeManualSubs{})

	got, err := source.Verdict(context.Background(), 42)
	if err != nil {
		t.Fatalf("Verdict: %v", err)
	}

	if got.Verdict != domain.VerdictActive {
		t.Fatalf("verdict = %s, want active", got.Verdict)
	}
}

func TestManualVerdictActiveSubscriptionOrNoSignal(t *testing.T) {
	now := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	source := NewManual(
		fakeWhitelist{},
		fakeManualSubs{
			sub: domain.Subscription{ExpiresAt: &expires},
			ok:  true,
		},
		WithManualClock(func() time.Time { return now }),
	)

	got, err := source.Verdict(context.Background(), 42)
	if err != nil {
		t.Fatalf("Verdict active: %v", err)
	}

	if got.Verdict != domain.VerdictActive {
		t.Fatalf("active verdict = %s, want active", got.Verdict)
	}

	source = NewManual(fakeWhitelist{}, fakeManualSubs{})

	got, err = source.Verdict(context.Background(), 42)
	if err != nil {
		t.Fatalf("Verdict no signal: %v", err)
	}

	if got.Verdict != domain.VerdictNoSignal {
		t.Fatalf("empty verdict = %s, want no_signal", got.Verdict)
	}
}

func TestManualVerdictErrorsBecomeUnknown(t *testing.T) {
	source := NewManual(
		fakeWhitelist{err: errors.New("whitelist unavailable")},
		fakeManualSubs{},
	)

	got, err := source.Verdict(context.Background(), 42)
	if err != nil {
		t.Fatalf("Verdict whitelist error: %v", err)
	}

	if got.Verdict != domain.VerdictUnknown {
		t.Fatalf("whitelist error verdict = %s, want unknown", got.Verdict)
	}

	source = NewManual(
		fakeWhitelist{},
		fakeManualSubs{err: errors.New("subscriptions unavailable")},
	)

	got, err = source.Verdict(context.Background(), 42)
	if err != nil {
		t.Fatalf("Verdict subscription error: %v", err)
	}

	if got.Verdict != domain.VerdictUnknown {
		t.Fatalf("subscription error verdict = %s, want unknown", got.Verdict)
	}
}
