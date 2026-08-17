package webhook

import (
	"context"
	"encoding/json"
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

// TestReadinessFailsWhenEnforcerIsDead pins the enforcer as a readiness input:
// unlike `get_me` or reconcile freshness, a stopped worker pool never recovers
// on its own, so it must not be reported ready.
func TestReadinessFailsWhenEnforcerIsDead(t *testing.T) {
	ctx := context.Background()
	base := Readiness{
		DB:      testutil.NewDB(t),
		GetMeOK: func() bool { return true },
	}

	dead := base
	dead.EnforcerAlive = func() bool { return false }
	assert.Contains(t, dead.Check(ctx).Failed, "enforcer",
		"a stopped worker pool must fail readiness")

	live := base
	live.EnforcerAlive = func() bool { return true }
	assert.NotContains(t, live.Check(ctx).Failed, "enforcer",
		"a turning worker pool is not a readiness failure")

	assert.NotContains(t, base.Check(ctx).Failed, "enforcer",
		"a nil probe means the enforcer is not supervised here and is skipped")
}

// TestLivezReportsEnforcerState covers the property that separates `/livez`
// from `/readyz`: liveness is process-local. Every case is built with a nil
// `DB`, which `/readyz` reports as `db` and `schema` failures — `/livez` must
// report neither, because it never looks.
func TestLivezReportsEnforcerState(t *testing.T) {
	cases := []struct {
		name       string
		alive      func() bool
		wantStatus int
		wantBody   string
		wantFailed []string
	}{
		{
			name:       "unsupervised enforcer is live",
			alive:      nil,
			wantStatus: http.StatusOK,
			wantBody:   "ok",
			wantFailed: nil,
		},
		{
			name:       "turning worker pool is live",
			alive:      func() bool { return true },
			wantStatus: http.StatusOK,
			wantBody:   "ok",
			wantFailed: nil,
		},
		{
			name:       "stopped worker pool is not live",
			alive:      func() bool { return false },
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "not_live",
			wantFailed: []string{"enforcer"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(Config{
				Readiness: Readiness{DB: nil, EnforcerAlive: tc.alive},
			})

			resp := request(server.Handler(), http.MethodGet, "/livez")
			require.Equal(t, tc.wantStatus, resp.Code, "livez status body=%s",
				resp.Body.String())

			var body struct {
				Status string   `json:"status"`
				Failed []string `json:"failed"`
			}

			require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body),
				"decode livez body")

			assert.Equal(t, tc.wantBody, body.Status, "livez status field")
			assert.Equal(t, tc.wantFailed, body.Failed,
				"the enforcer is the only livez input")
			assert.NotContains(t, body.Failed, "db",
				"livez must not reach for the database")
			assert.NotContains(t, body.Failed, "schema",
				"livez must not check the schema")
		})
	}

	// Same nil DB, same live enforcer: `/readyz` fails on the dependencies
	// `/livez` deliberately ignores. This is the contrast the two endpoints
	// exist for.
	server := NewServer(Config{
		Readiness: Readiness{DB: nil, EnforcerAlive: func() bool { return true }},
	})

	resp := request(server.Handler(), http.MethodGet, "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, resp.Code, "readyz status")
	assert.Contains(t, resp.Body.String(), "db",
		"readyz still checks the database that livez skipped")
}

// TestHealthzUnchangedByLiveness pins the existing contract: `/healthz` means
// the HTTP server is up and nothing more, so no enforcer state can change it.
func TestHealthzUnchangedByLiveness(t *testing.T) {
	cases := []struct {
		name  string
		alive func() bool
	}{
		{name: "unsupervised enforcer", alive: nil},
		{name: "turning worker pool", alive: func() bool { return true }},
		{name: "stopped worker pool", alive: func() bool { return false }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(Config{
				Readiness: Readiness{EnforcerAlive: tc.alive},
			})

			assertStatus(t, server.Handler(), http.MethodGet, "/healthz",
				http.StatusOK)
		})
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
