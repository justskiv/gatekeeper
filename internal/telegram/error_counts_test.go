package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientCountsAPIErrorsByMethodAndCategory covers the metric that would
// have named the 2026-08-15 outage while it was happening: the Telegram API
// answered slowly, every send timed out, and nothing in the exposition said so.
func TestClientCountsAPIErrorsByMethodAndCategory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
	defer srv.Close()

	client, err := NewClient("123456:TOKEN",
		WithServerURL(srv.URL),
		WithHTTPClient(&http.Client{Timeout: 30 * time.Millisecond}))
	require.NoError(t, err, "new client")

	assert.Empty(t, client.APIErrorCounts(),
		"a client that has not failed reports nothing")

	ctx := context.Background()
	for range 2 {
		require.Error(t, client.BanChatMember(ctx, -1, 1), "ban must time out")
	}

	require.Error(t, client.UnbanChatMember(ctx, -1, 1, true),
		"unban must time out")

	assert.Equal(t, []APIErrorCount{
		{
			Method:   "banChatMember",
			Category: ErrorCategoryTimeout,
			Count:    2,
		},
		{
			Method:   "unbanChatMember",
			Category: ErrorCategoryTimeout,
			Count:    1,
		},
	}, client.APIErrorCounts(),
		"failures accumulate per method and category, ordered stably")
}

// TestClientDoesNotCountSuccesses keeps the counter a failure counter: a client
// whose calls succeed must leave it empty, or the series would grow with
// traffic and stop meaning anything.
func TestClientDoesNotCountSuccesses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}))
	defer srv.Close()

	client, err := NewClient("123456:TOKEN", WithServerURL(srv.URL))
	require.NoError(t, err, "new client")

	require.NoError(t, client.BanChatMember(context.Background(), -1, 1))
	assert.Empty(t, client.APIErrorCounts())
}
