package store

import (
	"context"
	"testing"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

func TestOpsExportRowsOrdersSubscriptionsAndExpiriesTogether(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := NewUsers(db).Upsert(ctx, domain.User{TGID: 900}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	boostyExpires := now.Add(30 * 24 * time.Hour)
	tributeExpires := now.Add(60 * 24 * time.Hour)

	subs := NewSubscriptions(db)
	if _, err := subs.UpsertActive(ctx, domain.Subscription{
		TGID:      900,
		Platform:  domain.PlatformTribute,
		StartedAt: now,
		ExpiresAt: &tributeExpires,
	}); err != nil {
		t.Fatalf("upsert tribute: %v", err)
	}

	if _, err := subs.UpsertActive(ctx, domain.Subscription{
		TGID:      900,
		Platform:  domain.PlatformBoosty,
		StartedAt: now,
		ExpiresAt: &boostyExpires,
	}); err != nil {
		t.Fatalf("upsert boosty: %v", err)
	}

	rows, err := NewOps(db).ExportRows(ctx, 10)
	if err != nil {
		t.Fatalf("ExportRows: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one row", rows)
	}

	wantSubscriptions := "boosty|tribute"
	if rows[0].Subscriptions != wantSubscriptions {
		t.Fatalf("subscriptions = %q, want %q",
			rows[0].Subscriptions, wantSubscriptions)
	}

	wantExpires := boostyExpires.Format(time.RFC3339) + "|" +
		tributeExpires.Format(time.RFC3339)
	if rows[0].ExpiresAt != wantExpires {
		t.Fatalf("expires_at = %q, want %q", rows[0].ExpiresAt, wantExpires)
	}
}
