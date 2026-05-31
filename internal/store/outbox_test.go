package store

import (
	"context"
	"testing"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

func TestOutboxEnqueueIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(1001)

	if err := NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	key := domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "update:1")
	outbox := NewOutbox(db)

	first, inserted, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: key,
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}

	if !inserted {
		t.Fatal("first enqueue inserted=false, want true")
	}

	second, inserted, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: key,
		PayloadJSON:    []byte(`{"text":"changed"}`),
	})
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}

	if inserted {
		t.Fatal("second enqueue inserted=true, want idempotent reuse")
	}

	if first.ID != second.ID {
		t.Fatalf("ids = %d/%d, want same row", first.ID, second.ID)
	}

	var rows int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM access_actions WHERE idempotency_key = ?`,
		key).Scan(&rows); err != nil {
		t.Fatalf("count actions: %v", err)
	}

	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
}

func TestOutboxLeaseReclaimsExpiredRunningAction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(1002)
	now := time.Now()

	if err := NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	outbox := NewOutbox(db)

	queued, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "lease"),
		RunAfter:       now.Add(-time.Minute),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	leased, ok, err := outbox.LeaseReady(ctx, now, time.Minute)
	if err != nil {
		t.Fatalf("lease first: %v", err)
	}

	if !ok || leased.ID != queued.ID || leased.Status != domain.ActionRunning {
		t.Fatalf("first lease = (%+v, %v), want queued row running", leased, ok)
	}

	if _, ok, err = outbox.LeaseReady(ctx, now, time.Minute); err != nil || ok {
		t.Fatalf("second lease = (_, %v, %v), want no row", ok, err)
	}

	reclaimed, ok, err := outbox.LeaseReady(ctx, now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("lease expired running: %v", err)
	}

	if !ok || reclaimed.ID != queued.ID {
		t.Fatalf("reclaimed = (%+v, %v), want same row", reclaimed, ok)
	}
}

func TestOutboxRetryRecordsMetadata(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(1003)

	if err := NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	outbox := NewOutbox(db)

	action, _, err := outbox.Enqueue(ctx, AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, "retry"),
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	nextRun := time.Now().Add(17 * time.Second)

	retried, err := outbox.Retry(ctx, action.ID, nextRun, "telegram 429")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	if retried.Status != domain.ActionQueued ||
		retried.Attempts != 1 ||
		retried.LastError != "telegram 429" {
		t.Fatalf("retried action = %+v, want queued attempts=1 last_error", retried)
	}

	if retried.RunAfter.Before(nextRun.Add(-time.Second)) {
		t.Fatalf("run_after = %v, want near %v", retried.RunAfter, nextRun)
	}
}
