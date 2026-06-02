//nolint:wsl_v5 // HTTP tests group arrange/assert blocks tightly.
package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justskiv/gatekeeper/internal/store"
)

func TestServerRoutesHealthReadinessMetricsAndGating(t *testing.T) {
	db := newWebhookTestDB(t)
	ctx := context.Background()
	meta := store.NewMeta(db)
	for _, key := range []string{
		"boosty_group",
		"tribute_channel",
		"club_chat",
		"club_channel",
	} {
		if err := meta.SetHealth(ctx, key, "ok"); err != nil {
			t.Fatalf("set health: %v", err)
		}
	}

	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	if err := meta.Set(ctx, "reconcile.last_run_at",
		now.Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("set reconcile: %v", err)
	}

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
	if !strings.Contains(resp.Body.String(), "gatekeeper_updates_total") {
		t.Fatalf("metrics body missing update metric: %s", resp.Body.String())
	}

	if strings.Contains(resp.Body.String(),
		`gatekeeper_telegram_api_errors_total{code="none",method="unknown"} 0`) {
		t.Fatalf("metrics body contains fake telegram error sample: %s",
			resp.Body.String())
	}
}

func TestReadinessFailsForHealthStaleReconcileAndTelegramRegistration(t *testing.T) {
	db := newWebhookTestDB(t)
	ctx := context.Background()
	meta := store.NewMeta(db)
	if err := meta.SetHealth(ctx, "club_chat", "fail:not_admin"); err != nil {
		t.Fatalf("set health: %v", err)
	}

	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	if err := meta.Set(ctx, "reconcile.last_run_at",
		now.Add(-3*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("set reconcile: %v", err)
	}

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
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", resp.Code)
	}

	body := resp.Body.String()
	for _, want := range []string{
		"health.club_chat",
		"reconcile.stale",
		"telegram_webhook_registration",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("readyz body missing %q: %s", want, body)
		}
	}
}

func TestMetricsDisabledReturnsNotFound(t *testing.T) {
	server := NewServer(Config{})

	assertStatus(t, server.Handler(), http.MethodGet, "/metrics", http.StatusNotFound)
}

func TestWriteJSONEncodeErrorUsesInternalServerError(t *testing.T) {
	resp := httptest.NewRecorder()

	writeJSON(resp, http.StatusOK, make(chan int))

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500",
			resp.Code, resp.Body.String())
	}
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
	if resp.Code != want {
		t.Fatalf("%s %s status = %d body=%s, want %d",
			method, path, resp.Code, resp.Body.String(), want)
	}
}

func request(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	return resp
}
