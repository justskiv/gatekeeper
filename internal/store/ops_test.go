package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
)

func TestOpsExportRowsOrdersSubscriptionsAndExpiriesTogether(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	boostyExpires := now.Add(30 * 24 * time.Hour)
	tributeExpires := now.Add(60 * 24 * time.Hour)

	subs := NewSubscriptions(db)

	_, err := subs.UpsertActive(ctx, domain.Subscription{
		TGID:      tgID,
		Platform:  domain.PlatformTribute,
		StartedAt: now,
		ExpiresAt: &tributeExpires,
	})
	require.NoError(t, err, "upsert tribute")

	_, err = subs.UpsertActive(ctx, domain.Subscription{
		TGID:      tgID,
		Platform:  domain.PlatformBoosty,
		StartedAt: now,
		ExpiresAt: &boostyExpires,
	})
	require.NoError(t, err, "upsert boosty")

	rows, err := NewOps(db).ExportRows(ctx, 10)
	require.NoError(t, err, "ExportRows")
	require.Len(t, rows, 1)

	// The export joins both platforms into a single ordered row.
	assert.Equal(t, "boosty|tribute", rows[0].Subscriptions)

	wantExpires := boostyExpires.Format(time.RFC3339) + "|" +
		tributeExpires.Format(time.RFC3339)
	assert.Equal(t, wantExpires, rows[0].ExpiresAt)
}
