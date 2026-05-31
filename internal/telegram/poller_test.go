package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
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

type countingSource struct {
	calls      int
	platform   domain.Platform
	inTx       *atomic.Bool
	calledInTx *atomic.Bool
}

func (s *countingSource) Platform() domain.Platform {
	return s.platform
}

func (s *countingSource) Verdict(
	ctx context.Context,
	tgID int64,
) (domain.SourceVerdict, error) {
	s.calls++
	if s.inTx != nil && s.inTx.Load() && s.calledInTx != nil {
		s.calledInTx.Store(true)
	}

	return domain.SourceVerdict{
		Source:  s.platform,
		Verdict: domain.VerdictActive,
		Detail:  "preflight probe",
	}, nil
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

	var (
		captured GetUpdatesParams
		calls    int
	)

	client := newBotAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if methodName(r.URL.Path) != "getUpdates" {
			t.Fatalf("unexpected method %s", methodName(r.URL.Path))
		}

		calls++
		if calls > 1 {
			<-r.Context().Done()
			writeTelegramResult(w, []json.RawMessage{})

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode getUpdates request: %v", err)
		}

		writeTelegramResult(w, []json.RawMessage{rawUpdate, rawUpdate})
		time.AfterFunc(50*time.Millisecond, cancel)
	})

	sender := &recordingSender{}
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

	if calls == 0 {
		t.Fatal("getUpdates was not called")
	}

	if !slices.Equal(captured.AllowedUpdates, DefaultAllowedUpdates) {
		t.Fatalf("allowed_updates = %#v, want %#v",
			captured.AllowedUpdates, DefaultAllowedUpdates)
	}

	if sender.calls != 0 {
		t.Fatalf("send calls = %d, want 0 with durable outbox", sender.calls)
	}

	if got := countSendDMActions(t, db); got != 1 {
		t.Fatalf("send_dm actions = %d, want 1", got)
	}

	var (
		rows, offset int
		payload      string
	)
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

	if sender.calls != 0 {
		t.Fatalf("send calls = %d, want 0 with durable outbox", sender.calls)
	}

	if got := countSendDMActions(t, db); got != 1 {
		t.Fatalf("send_dm actions = %d, want 1", got)
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

	if sender.calls != 0 {
		t.Fatalf("send calls = %d, want 0 with durable outbox", sender.calls)
	}

	if got := countSendDMActions(t, db); got != 1 {
		t.Fatalf("send_dm actions = %d, want 1", got)
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

func TestPollerStatusPreflightRunsSourceOutsideHandlerTransaction(t *testing.T) {
	tests := []struct {
		name   string
		update *models.Update
		seed   func(context.Context, *sql.DB)
		owners []int64
		tgID   int64
	}{
		{
			name:   "status",
			update: privateTextUpdate(60, 6060, "/status"),
			tgID:   6060,
		},
		{
			name:   "whois",
			update: privateTextUpdate(61, 100, "/whois 7070"),
			seed: func(ctx context.Context, db *sql.DB) {
				if err := store.NewUsers(db).Upsert(
					ctx, domain.User{TGID: 7070},
				); err != nil {
					t.Fatalf("upsert target user: %v", err)
				}
			},
			owners: []int64{100},
			tgID:   7070,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			sender := &recordingSender{}

			if tt.seed != nil {
				tt.seed(ctx, db)
			}

			inTx := &atomic.Bool{}
			calledInTx := &atomic.Bool{}
			source := &countingSource{
				platform:   domain.PlatformBoosty,
				inTx:       inTx,
				calledInTx: calledInTx,
			}
			statusEngine := engine.New([]engine.SubscriptionSource{source})
			poller := NewPoller(
				db,
				nil,
				notify.New(store.NewUsers(db), sender, slog.Default()),
				nil,
				tt.owners,
				slog.Default(),
				WithPollerStatusEngine(statusEngine),
			)
			poller.afterBeginTx = func() {
				inTx.Store(true)
			}

			batch, nextOffset, err := buildUpdateBatch(
				[]FetchedUpdate{fetchedUpdate(t, tt.update)}, 0)
			if err != nil {
				t.Fatalf("buildUpdateBatch: %v", err)
			}

			if err := store.NewTelegramUpdates(db).InsertBatch(
				ctx, batch, nextOffset,
			); err != nil {
				t.Fatalf("InsertBatch: %v", err)
			}

			if err := poller.processPending(ctx); err != nil {
				t.Fatalf("processPending: %v", err)
			}

			if source.calls != 1 {
				t.Fatalf("source calls = %d, want exactly one preflight call",
					source.calls)
			}

			if calledInTx.Load() {
				t.Fatal("source was called after tx2 began")
			}

			if sender.calls != 0 {
				t.Fatalf("send calls = %d, want durable outbox", sender.calls)
			}

			if got := countSendDMActions(t, db); got != 1 {
				t.Fatalf("send_dm actions = %d, want one command reply", got)
			}

			sub, ok, err := store.NewSubscriptions(db).GetActive(
				ctx, tt.tgID, domain.PlatformBoosty)
			if err != nil {
				t.Fatalf("GetActive: %v", err)
			}

			if !ok || sub.LastSignal != "on_demand" {
				t.Fatalf("subscription = (%+v, %v), want on-demand active", sub, ok)
			}

			var status string
			if err := db.QueryRowContext(ctx,
				`SELECT status FROM telegram_updates WHERE update_id = ?`,
				tt.update.ID,
			).Scan(&status); err != nil {
				t.Fatalf("read update status: %v", err)
			}

			if status != string(store.TelegramUpdateProcessed) {
				t.Fatalf("status = %q, want processed", status)
			}
		})
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

func countSendDMActions(t *testing.T, db *sql.DB) int {
	t.Helper()

	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM access_actions WHERE action_type = ?`,
		string(domain.ActionSendDM)).Scan(&n); err != nil {
		t.Fatalf("count actions: %v", err)
	}

	return n
}
