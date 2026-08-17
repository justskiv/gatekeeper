package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/testutil"
)

// TestPollerSurvivesGetUpdatesTimeout guards the transport-chain change. Before
// it, rawRequest wrapped transport failures with %s, which broke errors.Is and
// accidentally shielded the poller from the shutdown-vs-timeout confusion that
// killed the outbox workers. Now the chain survives, so the poller's own
// shutdown check must rest on the context and nothing else.
func TestPollerSurvivesGetUpdatesTimeout(t *testing.T) {
	db := testutil.NewDB(t)

	rawUpdate := json.RawMessage(`{
		"update_id": 77,
		"message": {
			"message_id": 77,
			"from": {"id": 7007, "is_bot": false, "first_name": "Test"},
			"chat": {"id": 7007, "type": "private"},
			"date": 1,
			"text": "/start"
		}
	}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu    sync.Mutex
		calls int
	)

	// The first two polls time out the way a flapping Telegram API makes them:
	// an http.Client.Timeout failure, which reports itself as a deadline.
	httpClient := &http.Client{Transport: roundTripFunc(
		func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()

			if methodName(req.URL.Path) == "getUpdates" && n <= 2 {
				return nil, context.DeadlineExceeded
			}

			recorder := httptest.NewRecorder()

			if n > 2 {
				time.AfterFunc(50*time.Millisecond, cancel)
				writeTelegramResult(recorder, []json.RawMessage{rawUpdate})
			} else {
				writeTelegramResult(recorder, []json.RawMessage{})
			}

			return recorder.Result(), nil
		})}

	client, err := NewClient("123:ABC",
		WithServerURL("http://telegram.test"),
		WithHTTPClient(httpClient))
	require.NoError(t, err, "NewClient")

	poller := NewPoller(db, client, nil, nil, nil, slog.Default())
	poller.timeout = 1

	errCh := make(chan error, 1)
	go func() { errCh <- poller.Run(ctx) }()

	select {
	case runErr := <-errCh:
		require.NoError(t, runErr, "Run")
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("poller did not finish; it likely exited early on a timeout")
	}

	mu.Lock()
	got := calls
	mu.Unlock()

	require.Greater(t, got, 2,
		"the poller must keep polling after transport timeouts")
}
