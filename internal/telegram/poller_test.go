package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/admission"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
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
	db := testutil.NewDB(t)

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

		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&captured),
			"decode getUpdates request") {
			return
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
		require.NoError(t, err, "Run")
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("poller did not stop after delivering update")
	}

	require.NotZero(t, calls, "getUpdates was not called")

	assert.True(t, slices.Equal(captured.AllowedUpdates, DefaultAllowedUpdates),
		"allowed_updates must match the default set")

	assert.Zero(t, sender.calls, "send calls must be 0 with durable outbox")
	assert.Equal(t, 1, countSendDMActions(t, db), "send_dm actions")

	var (
		rows, offset int
		payload      string
	)
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*), max(payload_json)
		FROM telegram_updates
		WHERE status = 'processed'`,
	).Scan(&rows, &payload), "read processed updates")

	assert.Equal(t, 1, rows, "processed rows")
	assert.Contains(t, payload, "unknown_future_field",
		"payload_json must keep the raw future field")

	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT value FROM meta WHERE key = 'update_offset'`,
	).Scan(&offset), "read update_offset")
	assert.Equal(t, 31, offset)
}

func TestHandleWebhookUpdateUsesDurablePipelineAndDeduplicates(t *testing.T) {
	db := testutil.NewDB(t)
	poller := NewPoller(db, nil, nil, nil, nil, slog.Default())

	raw := []byte(`{
		"update_id": 700,
		"web_app_link":"https://t.me/app?startapp=secret"
	}`)

	for range 2 {
		require.NoError(t, poller.HandleWebhookUpdate(context.Background(), raw),
			"HandleWebhookUpdate")
	}

	var rows int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM telegram_updates`).Scan(&rows),
		"count telegram updates")
	assert.Equal(t, 1, rows, "duplicate webhook delivery must dedupe")

	var status, payload string
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT status, payload_json
		FROM telegram_updates
		WHERE update_id = 700`,
	).Scan(&status, &payload), "read telegram update")

	assert.Equal(t, string(store.TelegramUpdateIgnored), status)
	assert.Contains(t, payload, "startapp=secret", "payload is stored raw")
}

func TestPollerProcessesDuplicateUpdateIDOnce(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	sender := &recordingSender{}

	update := privateTextUpdate(10, 1001, "/start")

	batch, nextOffset, err := buildUpdateBatch(
		[]FetchedUpdate{fetchedUpdate(t, update), fetchedUpdate(t, update)}, 0)
	require.NoError(t, err, "buildUpdateBatch")

	require.NoError(t, store.NewTelegramUpdates(db).InsertBatch(ctx, batch, nextOffset),
		"InsertBatch")

	poller := NewPoller(db, nil,
		notify.New(store.NewUsers(db), sender, slog.Default()),
		nil, nil, slog.Default())
	require.NoError(t, poller.processPending(ctx), "processPending")

	assert.Zero(t, sender.calls, "send calls must be 0 with durable outbox")
	assert.Equal(t, 1, countSendDMActions(t, db), "send_dm actions")

	user, err := store.NewUsers(db).Get(ctx, 1001)
	require.NoError(t, err, "get user")
	assert.Equal(t, domain.DMOpen, user.DMState)

	var processed int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM telegram_updates WHERE status = 'processed'`,
	).Scan(&processed), "count processed")
	assert.Equal(t, 1, processed, "processed rows")
}

func TestPollerRecoversPendingOnStartup(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	sender := &recordingSender{}

	update := privateTextUpdate(20, 2002, "hello")

	batch, nextOffset, err := buildUpdateBatch(
		[]FetchedUpdate{fetchedUpdate(t, update)}, 0)
	require.NoError(t, err, "buildUpdateBatch")

	require.NoError(t, store.NewTelegramUpdates(db).InsertBatch(ctx, batch, nextOffset),
		"InsertBatch")

	poller := NewPoller(db, nil,
		notify.New(store.NewUsers(db), sender, slog.Default()),
		nil, nil, slog.Default())
	require.NoError(t, poller.processPending(ctx), "processPending")

	assert.Zero(t, sender.calls, "send calls must be 0 with durable outbox")
	assert.Equal(t, 1, countSendDMActions(t, db), "send_dm actions")

	var status string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT status FROM telegram_updates WHERE update_id = 20`,
	).Scan(&status), "read update status")
	assert.Equal(t, string(store.TelegramUpdateProcessed), status)
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
				require.NoError(t, store.NewUsers(db).Upsert(
					ctx, domain.User{TGID: 7070},
				), "upsert target user")
			},
			owners: []int64{100},
			tgID:   7070,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewDB(t)
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
			require.NoError(t, err, "buildUpdateBatch")

			require.NoError(t, store.NewTelegramUpdates(db).InsertBatch(
				ctx, batch, nextOffset,
			), "InsertBatch")

			require.NoError(t, poller.processPending(ctx), "processPending")

			assert.Equal(t, 1, source.calls, "source must run exactly one preflight call")
			assert.False(t, calledInTx.Load(),
				"source must not be called after handleTx began")
			assert.Zero(t, sender.calls, "send calls must be 0 with durable outbox")
			assert.Equal(t, 1, countSendDMActions(t, db), "send_dm command reply")

			sub, ok, err := store.NewSubscriptions(db).GetActive(
				ctx, tt.tgID, domain.PlatformBoosty)
			require.NoError(t, err, "GetActive")
			require.True(t, ok, "on-demand subscription must be active")
			assert.Equal(t, "on_demand", sub.LastSignal)

			var status string
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT status FROM telegram_updates WHERE update_id = ?`,
				tt.update.ID,
			).Scan(&status), "read update status")
			assert.Equal(t, string(store.TelegramUpdateProcessed), status)
		})
	}
}

func TestPollerAdmissionBannedUserSkipsSourceProbe(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := int64(8081)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{
		TGID:   tgID,
		Banned: true,
	}), "upsert banned user")

	source := &countingSource{platform: domain.PlatformBoosty}
	poller := NewPoller(
		db,
		nil,
		notify.New(store.NewUsers(db), &recordingSender{}, slog.Default()),
		nil,
		nil,
		slog.Default(),
		WithPollerStatusEngine(engine.New([]engine.SubscriptionSource{source})),
		WithPollerAdmissionConfig(admission.Config{
			InviteMode:    domain.InviteSharedJoinRequest,
			ClubChatID:    -1001,
			ClubChannelID: -1002,
		}),
	)

	update := privateTextUpdate(82, tgID, "/start")

	batch, nextOffset, err := buildUpdateBatch(
		[]FetchedUpdate{fetchedUpdate(t, update)}, 0)
	require.NoError(t, err, "buildUpdateBatch")

	require.NoError(t, store.NewTelegramUpdates(db).InsertBatch(ctx, batch, nextOffset),
		"InsertBatch")

	require.NoError(t, poller.processPending(ctx), "processPending")

	assert.Zero(t, source.calls, "source must not run for banned user")
	assert.Equal(t, 1, countSendDMActions(t, db), "send_dm banned response")
}

func TestShouldAbortPollingKeepsConflictRetryable(t *testing.T) {
	assert.False(t, shouldAbortPolling(&APIError{Category: ErrorCategoryConflict}),
		"conflict must stay retryable")
	assert.True(t, shouldAbortPolling(&APIError{Category: ErrorCategoryUnauthorized}),
		"unauthorized must abort polling")
}

func fetchedUpdate(t *testing.T, update *models.Update) FetchedUpdate {
	t.Helper()

	raw, err := json.Marshal(update)
	require.NoError(t, err, "marshal update")

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
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM access_actions WHERE action_type = ?`,
		string(domain.ActionSendDM)).Scan(&n), "count actions")

	return n
}
