//nolint:wsl_v5 // Readiness checks are a compact list of independent inputs.
package webhook

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/store"
)

// Readiness contains dependencies used by /readyz.
type Readiness struct {
	DB                                 *sql.DB
	HealthKeys                         []string
	ReconcileInterval                  time.Duration
	GetMeOK                            func() bool
	RequireTelegramWebhookRegistration bool
	TelegramWebhookRegistered          func() bool
	Now                                func() time.Time
}

// ReadinessResult is the machine-readable readiness decision.
type ReadinessResult struct {
	Ready  bool
	Failed []string
}

// Check returns readiness failures without performing external network calls.
//
//nolint:gocyclo,cyclop // Each branch reports a separate readiness input.
func (r Readiness) Check(ctx context.Context) ReadinessResult {
	now := r.Now
	if now == nil {
		now = time.Now
	}

	interval := r.ReconcileInterval
	if interval <= 0 {
		interval = time.Hour
	}

	var failed []string

	if r.DB == nil {
		failed = append(failed, "db")
	} else {
		if err := r.DB.PingContext(ctx); err != nil {
			failed = append(failed, "db")
		}

		if err := store.CheckSchema(ctx, r.DB); err != nil {
			failed = append(failed, "schema")
		}
	}

	if r.GetMeOK == nil || !r.GetMeOK() {
		failed = append(failed, "get_me")
	}

	if r.DB == nil {
		for _, key := range r.HealthKeys {
			failed = append(failed, "health."+strings.TrimPrefix(key, "health."))
		}
		failed = append(failed, "reconcile.last_run_at")
	} else {
		meta := store.NewMeta(r.DB)
		for _, key := range r.HealthKeys {
			name := strings.TrimPrefix(key, "health.")
			value, ok, err := meta.Get(ctx, "health."+name)
			if err != nil || !ok || value != "ok" {
				failed = append(failed, "health."+name)
			}
		}

		value, ok, err := meta.Get(ctx, "reconcile.last_run_at")
		if err != nil || !ok {
			failed = append(failed, "reconcile.last_run_at")
		} else {
			lastRun, parseErr := time.Parse(time.RFC3339, value)
			if parseErr != nil {
				failed = append(failed, "reconcile.last_run_at")
			} else if now().Sub(lastRun) > 2*interval {
				failed = append(failed, "reconcile.stale")
			}
		}
	}

	if r.RequireTelegramWebhookRegistration &&
		(r.TelegramWebhookRegistered == nil || !r.TelegramWebhookRegistered()) {
		failed = append(failed, "telegram_webhook_registration")
	}

	return ReadinessResult{
		Ready:  len(failed) == 0,
		Failed: failed,
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}
