package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/store"
)

type recordingSender struct {
	calls int
}

func (s *recordingSender) SendMessage(
	_ context.Context, _ int64, _ string,
) error {
	s.calls++
	return nil
}

type cancelingSender struct {
	cancel context.CancelFunc
	calls  int
}

func (s *cancelingSender) SendMessage(
	_ context.Context, _ int64, _ string,
) error {
	s.calls++
	s.cancel()
	return nil
}

func TestPollerRunFetchesAllowedUpdatesAndPersistsRawPayload(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rawUpdate := json.RawMessage(`{
		"update_id": 30,
		"message": {
			"message_id": 30,
			"from": {"id": 3003, "is_bot": false, "first_name": "Test"},
			"chat": {"id": 3003, "type": "private"},
			"date": 1,
			"text": "/start"
		},
		"unknown_future_field": {"keep": true}
	}`)

	var captured GetUpdatesParams
	var calls int
	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "getUpdates" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}
		calls++
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode getUpdates request: %v", err)
		}
		writeTelegramResult(w, []json.RawMessage{rawUpdate, rawUpdate})
	})

	sender := &cancelingSender{cancel: cancel}
	poller := NewPoller(db, client,
		notify.New(store.NewUsers(db), sender, slog.Default()),
		nil, nil, slog.Default())
	poller.timeout = 1

	errCh := make(chan error, 1)
	go func() {
		errCh <- poller.Run(ctx)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("poller did not stop after delivering update")
	}

	if calls != 1 {
		t.Fatalf("getUpdates calls = %d, want 1", calls)
	}
	if !slices.Equal(captured.AllowedUpdates, DefaultAllowedUpdates) {
		t.Fatalf("allowed_updates = %#v, want %#v",
			captured.AllowedUpdates, DefaultAllowedUpdates)
	}
	if sender.calls != 1 {
		t.Fatalf("send calls = %d, want 1", sender.calls)
	}

	var rows, offset int
	var payload string
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*), max(payload_json)
		FROM telegram_updates
		WHERE status = 'processed'`,
	).Scan(&rows, &payload); err != nil {
		t.Fatalf("read processed updates: %v", err)
	}
	if rows != 1 {
		t.Fatalf("processed rows = %d, want 1", rows)
	}
	if !strings.Contains(payload, "unknown_future_field") {
		t.Fatalf("payload_json lost raw future field: %s", payload)
	}
	if err := db.QueryRowContext(context.Background(), `
		SELECT value FROM meta WHERE key = 'update_offset'`,
	).Scan(&offset); err != nil {
		t.Fatalf("read update_offset: %v", err)
	}
	if offset != 31 {
		t.Fatalf("update_offset = %d, want 31", offset)
	}
}

func TestPollerProcessesDuplicateUpdateIDOnce(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sender := &recordingSender{}

	update := privateTextUpdate(10, 1001, "/start")
	batch, nextOffset, err := buildUpdateBatch(
		[]FetchedUpdate{fetchedUpdate(t, update), fetchedUpdate(t, update)}, 0)
	if err != nil {
		t.Fatalf("buildUpdateBatch: %v", err)
	}
	if err := store.NewTelegramUpdates(db).InsertBatch(ctx, batch, nextOffset); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	poller := NewPoller(db, nil,
		notify.New(store.NewUsers(db), sender, slog.Default()),
		nil, nil, slog.Default())
	if err := poller.processPending(ctx); err != nil {
		t.Fatalf("processPending: %v", err)
	}

	if sender.calls != 1 {
		t.Fatalf("send calls = %d, want 1", sender.calls)
	}
	user, err := store.NewUsers(db).Get(ctx, 1001)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.DMState != domain.DMOpen {
		t.Fatalf("dm_state = %s, want open", user.DMState)
	}
	var processed int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM telegram_updates WHERE status = 'processed'`,
	).Scan(&processed); err != nil {
		t.Fatalf("count processed: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed rows = %d, want 1", processed)
	}
}

func TestPollerRecoversPendingOnStartup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sender := &recordingSender{}

	update := privateTextUpdate(20, 2002, "hello")
	batch, nextOffset, err := buildUpdateBatch(
		[]FetchedUpdate{fetchedUpdate(t, update)}, 0)
	if err != nil {
		t.Fatalf("buildUpdateBatch: %v", err)
	}
	if err := store.NewTelegramUpdates(db).InsertBatch(ctx, batch, nextOffset); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	poller := NewPoller(db, nil,
		notify.New(store.NewUsers(db), sender, slog.Default()),
		nil, nil, slog.Default())
	if err := poller.processPending(ctx); err != nil {
		t.Fatalf("processPending: %v", err)
	}

	if sender.calls != 1 {
		t.Fatalf("send calls = %d, want 1", sender.calls)
	}
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM telegram_updates WHERE update_id = 20`,
	).Scan(&status); err != nil {
		t.Fatalf("read update status: %v", err)
	}
	if status != string(store.TelegramUpdateProcessed) {
		t.Fatalf("status = %q, want processed", status)
	}
}

func TestShouldAbortPollingKeepsConflictRetryable(t *testing.T) {
	if shouldAbortPolling(&APIError{Category: ErrorCategoryConflict}) {
		t.Fatal("conflict should be retryable")
	}
	if !shouldAbortPolling(&APIError{Category: ErrorCategoryUnauthorized}) {
		t.Fatal("unauthorized should abort polling")
	}
}

func fetchedUpdate(t *testing.T, update *models.Update) FetchedUpdate {
	t.Helper()
	raw, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	return FetchedUpdate{Raw: raw, Update: update}
}

func privateTextUpdate(updateID, tgID int64, text string) *models.Update {
	return &models.Update{
		ID: updateID,
		Message: &models.Message{
			ID:   int(updateID),
			From: &models.User{ID: tgID, FirstName: "Test"},
			Chat: models.Chat{
				ID:   tgID,
				Type: models.ChatTypePrivate,
			},
			Text: text,
		},
	}
}
