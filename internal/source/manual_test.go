package source

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
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

	got, err := source.Verdict(context.Background(), random.TGID())
	require.NoError(t, err, "Verdict")
	assert.Equal(t, domain.VerdictActive, got.Verdict)
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

	got, err := source.Verdict(context.Background(), random.TGID())
	require.NoError(t, err, "Verdict active")
	assert.Equal(t, domain.VerdictActive, got.Verdict)

	source = NewManual(fakeWhitelist{}, fakeManualSubs{})

	got, err = source.Verdict(context.Background(), random.TGID())
	require.NoError(t, err, "Verdict no signal")
	assert.Equal(t, domain.VerdictNoSignal, got.Verdict)
}

func TestManualVerdictErrorsBecomeUnknown(t *testing.T) {
	source := NewManual(
		fakeWhitelist{err: errors.New("whitelist unavailable")},
		fakeManualSubs{},
	)

	got, err := source.Verdict(context.Background(), random.TGID())
	require.NoError(t, err, "Verdict whitelist error")
	assert.Equal(t, domain.VerdictUnknown, got.Verdict)

	source = NewManual(
		fakeWhitelist{},
		fakeManualSubs{err: errors.New("subscriptions unavailable")},
	)

	got, err = source.Verdict(context.Background(), random.TGID())
	require.NoError(t, err, "Verdict subscription error")
	assert.Equal(t, domain.VerdictUnknown, got.Verdict)
}
