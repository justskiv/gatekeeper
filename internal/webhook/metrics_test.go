package webhook

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

// TestMetricsZeroFillsEveryOutboxPair is the regression for the alerting gap
// that made this rework necessary: a status with no rows used to produce no
// series at all, so an alert on dead actions sat in NoData until the first dead
// action ever appeared — that is, until after the thing it watches for had
// already happened.
func TestMetricsZeroFillsEveryOutboxPair(t *testing.T) {
	db := testutil.NewDB(t)
	body := renderMetrics(t, &Metrics{Ops: store.NewOps(db)})

	for _, actionType := range domain.AllActionTypes() {
		for _, status := range domain.AllActionStatuses() {
			sample := `gatekeeper_outbox_actions{status="` + string(status) +
				`",type="` + string(actionType) + `"}`
			assert.Contains(t, body, sample+" 0",
				"every type x status pair must be present on an empty database")
		}
	}

	// Spelled out separately because this exact series is the one an alert
	// rule watches; the loop above would still pass if `dead` were dropped
	// from the status enum.
	assert.Contains(t, body,
		`gatekeeper_outbox_actions{status="dead",type="send_dm"} 0`,
		"the dead-action series must read zero, not be missing")
}

// TestMetricsEmitsDeprecatedAliases pins the deprecation window open. The
// dashboards and alert rules reading the old names live in another repository;
// removing the alias must be a deliberate act that fails this test first.
func TestMetricsEmitsDeprecatedAliases(t *testing.T) {
	db := testutil.NewDB(t)
	body := renderMetrics(t, &Metrics{Ops: store.NewOps(db)})

	assert.Contains(t, body,
		`gatekeeper_outbox_actions_total{status="queued",type="send_dm"} 0`,
		"the deprecated alias must still carry the same samples")
	assert.Contains(t, body,
		"# HELP gatekeeper_outbox_actions_total DEPRECATED: "+
			"use gatekeeper_outbox_actions.",
		"the alias must announce its replacement")

	// The alias keeps the old name but not the old type: it was declared a
	// counter while being a live row count, which is what made rate() over it
	// invent throughput whenever retention reaped a row.
	assert.Contains(t, body, "# TYPE gatekeeper_outbox_actions_total gauge")
	assert.NotContains(t, body, "# TYPE gatekeeper_outbox_actions_total counter")
}

// TestMetricsDropsNeverEmittedFamilies guards the other half of the same
// problem: a `# TYPE` line with no collector behind it. Both families named
// here were declared by the old exposition and produced by nothing.
func TestMetricsDropsNeverEmittedFamilies(t *testing.T) {
	db := testutil.NewDB(t)
	body := renderMetrics(t, &Metrics{Ops: store.NewOps(db)})

	assert.NotContains(t, body, "gatekeeper_reconcile_duration_seconds",
		"the replaced reconcile metric must be gone entirely")
	assert.NotContains(t, body, "gatekeeper_telegram_api_errors_total",
		"the telegram error family must not be declared without a source")
}

func TestMetricsReportsBacklogAges(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	now := time.Now().Add(time.Hour).Truncate(time.Second)
	resource := domain.ResourceChat

	outbox := store.NewOutbox(db)
	_, _, err := outbox.Enqueue(ctx, store.AccessActionInput{
		Type:           domain.ActionEnsureInvite,
		Resource:       &resource,
		IdempotencyKey: "backlog-due",
		RunAfter:       now.Add(-30 * time.Minute),
	})
	require.NoError(t, err, "enqueue due action")

	_, _, err = outbox.Enqueue(ctx, store.AccessActionInput{
		Type:           domain.ActionEnsureInvite,
		Resource:       &resource,
		IdempotencyKey: "backlog-scheduled",
		RunAfter:       now.Add(time.Hour),
	})
	require.NoError(t, err, "enqueue scheduled action")

	body := renderMetrics(t, &Metrics{
		Ops: store.NewOps(db),
		Now: func() time.Time { return now },
	})

	assert.Contains(t, body, "gatekeeper_outbox_pending 2")
	assert.Contains(t, body, "gatekeeper_outbox_running 0")

	// Only the due row counts towards the overdue age: work scheduled for the
	// future is not a backlog, and counting it would make every retry with a
	// backoff look like a stall.
	assert.InDelta(t, 1800.0,
		metricValue(t, body, "gatekeeper_outbox_oldest_due_age_seconds"), 1,
		"overdue age is measured from run_after of the oldest due row")

	// Both rows are queued, and both were created just now, so the queued age
	// is the distance from the seeded clock to real time.
	assert.InDelta(t, 3600.0,
		metricValue(t, body, "gatekeeper_outbox_oldest_queued_age_seconds"), 5,
		"queued age is measured from created_at of the oldest queued row")

	assert.Contains(t, body,
		`gatekeeper_outbox_actions{status="queued",type="ensure_invite"} 2`)
}

func TestMetricsReportsEnforcerStateWhenWired(t *testing.T) {
	db := testutil.NewDB(t)

	body := renderMetrics(t, &Metrics{
		Ops: store.NewOps(db),
		Enforcer: func() EnforcerStats {
			return EnforcerStats{
				WorkersConfigured: 2,
				WorkersAlive:      1,
				WorkersStale:      1,
				MaxCycleAge:       90 * time.Second,
				Restarts:          3,
				Processed: []ProcessedCount{
					{Type: "send_dm", Result: "done", Count: 7},
					{Type: "send_dm", Result: "dead", Count: 1},
				},
			}
		},
	})

	assert.Contains(t, body, "gatekeeper_enforcer_workers_configured 2")
	assert.Contains(t, body, "gatekeeper_enforcer_workers_alive 1")
	assert.Contains(t, body, "gatekeeper_enforcer_workers_stale 1")
	assert.Contains(t, body, "gatekeeper_enforcer_max_cycle_age_seconds 90")
	assert.Contains(t, body, "gatekeeper_enforcer_worker_restarts_total 3")
	assert.Contains(t, body,
		`gatekeeper_outbox_actions_processed_total{result="done",type="send_dm"} 7`)
	assert.Contains(t, body,
		`gatekeeper_outbox_actions_processed_total{result="dead",type="send_dm"} 1`)
}

// TestMetricsOmitsEnforcerFamilyWhenUnwired covers the configuration where the
// pool is not supervised by this process. The family must vanish completely —
// declaring it with no samples would put back exactly the bare `# TYPE` lines
// this change removed.
func TestMetricsOmitsEnforcerFamilyWhenUnwired(t *testing.T) {
	db := testutil.NewDB(t)
	body := renderMetrics(t, &Metrics{Ops: store.NewOps(db)})

	assert.NotContains(t, body, "gatekeeper_enforcer_",
		"no enforcer series or type lines without a source")
	assert.NotContains(t, body, "gatekeeper_outbox_actions_processed_total",
		"throughput counters come from the same source")
}

func TestMetricsReportsTelegramErrorsWhenWired(t *testing.T) {
	db := testutil.NewDB(t)

	body := renderMetrics(t, &Metrics{
		Ops: store.NewOps(db),
		TelegramErrors: func() []TelegramErrorCount {
			return []TelegramErrorCount{
				{Method: "sendMessage", Category: "timeout", Count: 4},
			}
		},
	})

	assert.Contains(t, body,
		`gatekeeper_telegram_api_errors_total{category="timeout",method="sendMessage"} 4`)
	assert.Contains(t, body,
		"# TYPE gatekeeper_telegram_api_errors_total counter")
}

func TestMetricsReportsAlertsAndZeroFilledSeverityRollup(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	_, err := store.NewAlerts(db).Create(ctx, store.AlertInput{
		Severity: "critical",
		Kind:     "outbox_action_dead",
		Title:    "outbox action dead",
		Detail:   "action_id=1",
	})
	require.NoError(t, err, "create alert")

	body := renderMetrics(t, &Metrics{Ops: store.NewOps(db)})

	assert.Contains(t, body,
		`gatekeeper_admin_alerts{kind="outbox_action_dead",`+
			`severity="critical",state="open"} 1`)
	assert.Contains(t, body,
		`gatekeeper_admin_alerts_open{severity="critical"} 1`)

	// The severity rollup is what an alert rule reads, so every severity must
	// be present even with nothing open at that level.
	for _, severity := range []string{"info", "warning", "error"} {
		assert.Contains(t, body,
			`gatekeeper_admin_alerts_open{severity="`+severity+`"} 0`,
			"the rollup must read zero rather than go missing")
	}

	// The labelled family stays sparse on kind on purpose: kinds are literals
	// spread across packages, so there is nothing to zero-fill from.
	assert.NotContains(t, body, `kind="invite_mode_degraded"`)
}

// TestMetricsReportsReconcileAgeBeforeFirstPass is the regression for a
// watchdog that fell silent exactly when it had something to report.
//
// The exposition used to omit this family entirely while `reconcile.last_run_at`
// was absent. The rule reading it (`gk-pipeline-idle`, in the dashboards
// repository) is configured `noDataState: Ok`, so the missing series read as
// healthy: a process whose reconciler had never run once looked identical to one
// reconciling on schedule. Today's startup order masks the gap — main completes
// a pass before the listener opens — which is exactly why it needs a test rather
// than an assumption.
func TestMetricsReportsReconcileAgeBeforeFirstPass(t *testing.T) {
	db := testutil.NewDB(t)

	// A migrated database with no reconcile row: the state a fresh deployment is
	// in until its first pass completes.
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	body := renderMetrics(t, &Metrics{
		Ops:          store.NewOps(db),
		Now:          func() time.Time { return now },
		ProcessStart: now.Add(-90 * time.Second),
	})

	assert.InDelta(t, 90.0,
		metricValue(t, body, "gatekeeper_reconcile_last_run_age_seconds"), 0.001,
		"with no completed pass the age is measured from process start")
}

// TestMetricsReportsReconcileAgeFromLastRun pins the behaviour with the key
// present: the fallback must not leak into the normal case.
func TestMetricsReportsReconcileAgeFromLastRun(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.NewMeta(db).Set(ctx, "reconcile.last_run_at",
		now.Add(-15*time.Minute).Format(time.RFC3339)), "set reconcile")

	body := renderMetrics(t, &Metrics{
		Ops: store.NewOps(db),
		Now: func() time.Time { return now },
		// Deliberately far off: a completed pass is the only thing this value
		// may be measured from once one exists.
		ProcessStart: now.Add(-72 * time.Hour),
	})

	assert.InDelta(t, 900.0,
		metricValue(t, body, "gatekeeper_reconcile_last_run_age_seconds"), 0.001,
		"a completed pass wins over the process-start fallback")
}

// TestMetricsReconcileAgeSurvivesUnwiredProcessStart covers the fallback's own
// failure mode. `ProcessStart` is a plain field, so any caller can leave it
// zero; measuring from the zero time.Time would report an age of two thousand
// years, and re-omitting the sample would restore the blind spot. Neither is
// acceptable, so an unset field measures from package load instead.
func TestMetricsReconcileAgeSurvivesUnwiredProcessStart(t *testing.T) {
	db := testutil.NewDB(t)
	body := renderMetrics(t, &Metrics{Ops: store.NewOps(db)})

	age := metricValue(t, body, "gatekeeper_reconcile_last_run_age_seconds")
	assert.GreaterOrEqual(t, age, 0.0, "the age must not run backwards")
	assert.Less(t, age, float64(time.Hour/time.Second),
		"an unset ProcessStart must measure from this process, not year one")
}

// TestMetricsFailedStoreReadIsObservable is the regression for a scrape that
// lied. A SQLite failure used to produce a 200 with a partial body: the
// database-backed series simply vanished, every rule built on them went NoData,
// and the only trace was a log line. A monitoring endpoint that reports a
// healthy target while its own read failed is the blind-failure class this work
// exists to remove.
func TestMetricsFailedStoreReadIsObservable(t *testing.T) {
	db := testutil.NewDB(t)
	// A closed handle fails every statement, which is the shape of a database
	// that has gone away underneath a running process.
	require.NoError(t, db.Close(), "close the database")

	metrics := &Metrics{
		Ops: store.NewOps(db),
		Enforcer: func() EnforcerStats {
			return EnforcerStats{WorkersConfigured: 2, WorkersAlive: 0}
		},
		TelegramErrors: func() []TelegramErrorCount {
			return []TelegramErrorCount{
				{Method: "sendMessage", Category: "timeout", Count: 9},
			}
		},
	}

	var body bytes.Buffer

	err := metrics.Write(context.Background(), &body)
	require.Error(t, err, "the caller must still learn the read failed")

	assert.Contains(t, body.String(), "gatekeeper_metrics_store_scrape_success 0",
		"a partial scrape must say so in the body, not only in the log")

	// The in-process families are exactly what an operator wants when the
	// database is the thing that broke, so they must survive its failure.
	assert.Contains(t, body.String(), "gatekeeper_enforcer_workers_alive 0",
		"worker-pool state must survive a store failure")
	assert.Contains(t, body.String(),
		`gatekeeper_telegram_api_errors_total{category="timeout",method="sendMessage"} 9`,
		"telegram error counters must survive a store failure")
}

// TestMetricsHealthyScrapeReportsSuccess is the other half: the marker is only
// worth alerting on if it reads 1 whenever the read did complete.
func TestMetricsHealthyScrapeReportsSuccess(t *testing.T) {
	db := testutil.NewDB(t)
	body := renderMetrics(t, &Metrics{Ops: store.NewOps(db)})

	assert.Contains(t, body, "gatekeeper_metrics_store_scrape_success 1")
	assert.Contains(t, body,
		"# TYPE gatekeeper_metrics_store_scrape_success gauge")
}

// TestMetricsEmitsPairsMissingFromTheRegistry covers registry drift, which the
// zero-fill design cannot prevent: domain.AllActionTypes is a hand-kept list,
// so a type can exist in the database and be absent from it. A zero-filled
// series that goes missing costs an alert; a series with rows behind it going
// missing hides work the system is actually doing.
func TestMetricsEmitsPairsMissingFromTheRegistry(t *testing.T) {
	db := testutil.NewDB(t)
	metrics := &Metrics{Ops: store.NewOps(db)}

	var body bytes.Buffer

	require.NoError(t, metrics.writeOutboxMetrics(
		context.Background(), &body, store.OpsStats{
			Outbox: []store.OutboxCount{
				{Type: "unregistered_type", Status: domain.ActionQueued, Count: 3},
				{Type: domain.ActionSendDM, Status: "unregistered_status", Count: 5},
			},
		}, time.Now()), "write outbox metrics")

	assert.Contains(t, body.String(),
		`gatekeeper_outbox_actions{status="queued",type="unregistered_type"} 3`,
		"a real series must survive a stale type registry")
	assert.Contains(t, body.String(),
		`gatekeeper_outbox_actions{status="unregistered_status",type="send_dm"} 5`,
		"a real series must survive a stale status registry")

	// The registry-driven zero-fill is untouched by the fail-safe.
	assert.Contains(t, body.String(),
		`gatekeeper_outbox_actions{status="dead",type="send_dm"} 0`)
}

// TestMetricsNilReceiverWritesNothing pins the degenerate path taken when the
// endpoint is mounted without any source wired: no panic, and no declarations
// for collectors that do not exist.
func TestMetricsNilReceiverWritesNothing(t *testing.T) {
	var metrics *Metrics

	var body bytes.Buffer
	require.NoError(t, metrics.Write(context.Background(), &body), "write")
	assert.Empty(t, body.String())
}

func renderMetrics(t *testing.T, metrics *Metrics) string {
	t.Helper()

	var body bytes.Buffer
	require.NoError(t, metrics.Write(context.Background(), &body),
		"write metrics")

	return body.String()
}

// metricValue returns the sample value of an unlabelled series.
func metricValue(t *testing.T, body, name string) float64 {
	t.Helper()

	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, name+" ") {
			continue
		}

		value, err := strconv.ParseFloat(
			strings.TrimPrefix(line, name+" "), 64)
		require.NoError(t, err, "parse %s", name)

		return value
	}

	t.Fatalf("metric %s is not present in the exposition", name)

	return 0
}
