package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

func TestServerRoutesHealthReadinessMetricsAndGating(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	meta := store.NewMeta(db)

	for _, key := range []string{
		"boosty_group",
		"tribute_channel",
		"club_chat",
		"club_channel",
	} {
		require.NoError(t, meta.SetHealth(ctx, key, "ok"), "set health")
	}

	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	require.NoError(t, meta.Set(ctx, "reconcile.last_run_at",
		now.Add(-time.Hour).Format(time.RFC3339)), "set reconcile")

	server := NewServer(Config{
		MetricsEnabled: true,
		TributeEnabled: true,
		TributePath:    "/webhooks/tribute",
		Tribute: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		Readiness: Readiness{
			DB: db,
			HealthKeys: []string{
				"boosty_group",
				"tribute_channel",
				"club_chat",
				"club_channel",
			},
			ReconcileInterval: time.Hour,
			GetMeOK:           func() bool { return true },
			Now:               func() time.Time { return now },
		},
		Metrics: &Metrics{Ops: store.NewOps(db)},
	})

	assertStatus(t, server.Handler(), http.MethodGet, "/healthz", http.StatusOK)
	assertStatus(t, server.Handler(), http.MethodGet, "/readyz", http.StatusOK)
	assertStatus(t, server.Handler(), http.MethodGet, "/metrics", http.StatusOK)
	assertStatus(t, server.Handler(), http.MethodGet,
		"/webhooks/tribute", http.StatusMethodNotAllowed)
	assertStatus(t, server.Handler(), http.MethodPost,
		"/webhooks/telegram", http.StatusNotFound)

	resp := request(server.Handler(), http.MethodGet, "/metrics")
	assert.Contains(t, resp.Body.String(), "gatekeeper_updates_total",
		"metrics body must expose the update metric")
	assert.NotContains(t, resp.Body.String(),
		`gatekeeper_telegram_api_errors_total{code="none",method="unknown"} 0`,
		"metrics body must not contain a fake telegram error sample")
}

func TestReadinessFailsForHealthStaleReconcileAndTelegramRegistration(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	meta := store.NewMeta(db)
	require.NoError(t, meta.SetHealth(ctx, "club_chat", "fail:not_admin"),
		"set health")

	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	require.NoError(t, meta.Set(ctx, "reconcile.last_run_at",
		now.Add(-3*time.Hour).Format(time.RFC3339)), "set reconcile")

	server := NewServer(Config{
		Readiness: Readiness{
			DB:                                 db,
			HealthKeys:                         []string{"club_chat"},
			ReconcileInterval:                  time.Hour,
			GetMeOK:                            func() bool { return true },
			RequireTelegramWebhookRegistration: true,
			TelegramWebhookRegistered:          func() bool { return false },
			Now:                                func() time.Time { return now },
		},
	})

	resp := request(server.Handler(), http.MethodGet, "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, resp.Code, "readyz status")

	body := resp.Body.String()
	for _, want := range []string{
		"health.club_chat",
		"reconcile.stale",
		"telegram_webhook_registration",
	} {
		assert.Containsf(t, body, want, "readyz body missing %q", want)
	}
}

func TestMetricsDisabledReturnsNotFound(t *testing.T) {
	server := NewServer(Config{})

	assertStatus(t, server.Handler(), http.MethodGet, "/metrics", http.StatusNotFound)
}

func TestWriteJSONEncodeErrorUsesInternalServerError(t *testing.T) {
	resp := httptest.NewRecorder()

	writeJSON(resp, http.StatusOK, make(chan int))

	assert.Equal(t, http.StatusInternalServerError, resp.Code)
}

func assertStatus(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	want int,
) {
	t.Helper()

	resp := request(handler, method, path)
	assert.Equalf(t, want, resp.Code, "%s %s body=%s",
		method, path, resp.Body.String())
}

func request(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	return resp
}
