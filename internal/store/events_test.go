//nolint:wsl_v5 // Test setup and assertions stay grouped by scenario.
package store

import (
	"context"
	"testing"
)

func TestTributeEventsInsertDedupAndTerminalStatus(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewTributeEvents(db)

	tgID := int64(123)
	event, inserted, err := repo.InsertReceived(ctx, TributeEventInput{
		DedupKey:       "dedup",
		EventName:      "new_subscription",
		TGID:           &tgID,
		SubscriptionID: "sub-1",
		SignatureValid: true,
		PayloadJSON:    []byte(`{"name":"new_subscription"}`),
	})
	if err != nil {
		t.Fatalf("InsertReceived: %v", err)
	}

	if !inserted {
		t.Fatal("inserted = false, want true")
	}

	duplicate, inserted, err := repo.InsertReceived(ctx, TributeEventInput{
		DedupKey:       "dedup",
		EventName:      "new_subscription",
		SignatureValid: true,
		PayloadJSON:    []byte(`{"name":"new_subscription"}`),
	})
	if err != nil {
		t.Fatalf("InsertReceived duplicate: %v", err)
	}

	if inserted {
		t.Fatal("duplicate inserted = true, want false")
	}

	if duplicate.ID != event.ID {
		t.Fatalf("duplicate ID = %d, want %d", duplicate.ID, event.ID)
	}

	if err := repo.MarkTerminal(ctx, event.ID, TributeEventProcessed, ""); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}

	got, err := repo.GetByDedupKey(ctx, "dedup")
	if err != nil {
		t.Fatalf("GetByDedupKey: %v", err)
	}

	if got.Status != TributeEventProcessed || got.ProcessedAt == nil {
		t.Fatalf("event status = %s processed_at=%v, want processed timestamp",
			got.Status, got.ProcessedAt)
	}
}
