//nolint:wsl_v5 // Metrics exposition is a compact sequence of collectors.
package webhook

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

// EnforcerStats is the outbox worker pool's state as the metrics endpoint
// needs it.
//
// The shape is declared here and filled by a closure supplied from main, so
// this package never imports internal/enforcer — the same arrangement as
// Readiness.GetMeOK and Readiness.EnforcerAlive.
type EnforcerStats struct {
	WorkersConfigured int
	WorkersAlive      int
	WorkersStale      int
	MaxCycleAge       time.Duration
	Restarts          int64

	// Processed is the throughput counter: how many actions reached each
	// terminal outcome since the process started.
	Processed []ProcessedCount
}

// ProcessedCount is one action-throughput counter slot.
type ProcessedCount struct {
	Type   string
	Result string
	Count  int64
}

// TelegramErrorCount is one normalized Telegram API failure counter.
type TelegramErrorCount struct {
	Method   string
	Category string
	Count    int64
}

// Metrics writes a small Prometheus text exposition from durable state and
// from in-process counters.
type Metrics struct {
	Ops *store.Ops

	// Now supplies the instant every age metric is measured against. Nil means
	// time.Now.
	Now func() time.Time

	// ProcessStart is when this process began. Before any reconcile pass has
	// ever completed it is the source of
	// `gatekeeper_reconcile_last_run_age_seconds`: the time since start is
	// exactly how long this deployment has gone without a pass. The zero value
	// falls back to processLoadedAt.
	ProcessStart time.Time

	// Enforcer, when set, reports the outbox worker pool. A nil func means the
	// pool is not supervised here and its whole family is omitted rather than
	// declared with no samples.
	Enforcer func() EnforcerStats

	// TelegramErrors, when set, reports normalized Telegram API failures.
	TelegramErrors func() []TelegramErrorCount
}

// Write emits metrics with bounded labels only.
//
// The in-process families go out before the database-backed ones on purpose: a
// failing store read aborts the rest of the body, and the worker-pool state is
// exactly what an operator needs when the database is the thing misbehaving.
//
// A store failure is reported in the body rather than by failing the scrape.
// Answering 5xx would discard the in-process families above at the one moment
// they are most valuable, so the response stays a 200 whose
// `gatekeeper_metrics_store_scrape_success` reads 0. The returned error is for
// the caller's log; the gauge is what Prometheus can alert on.
func (m *Metrics) Write(ctx context.Context, w io.Writer) error {
	if m == nil {
		m = &Metrics{}
	}

	m.writeHelp(w)
	m.writeEnforcerMetrics(w)
	m.writeTelegramErrorMetrics(w)

	if m.Ops == nil {
		return nil
	}

	err := m.writeStoreMetrics(ctx, w)
	writeLine(w, "gatekeeper_metrics_store_scrape_success", scrapeSuccess(err))

	return err
}

// writeStoreMetrics emits every family read out of the database. It stops at
// the first failure: the rest of the read would fail the same way, and the
// scrape marker written by Write tells the reader the body is partial.
func (m *Metrics) writeStoreMetrics(ctx context.Context, w io.Writer) error {
	now := m.now()

	stats, err := m.Ops.Stats(ctx, now)
	if err != nil {
		return err
	}

	if err := m.writeInventoryMetrics(ctx, w, stats, now); err != nil {
		return err
	}

	if err := m.writeOutboxMetrics(ctx, w, stats, now); err != nil {
		return err
	}

	return m.writeAlertMetrics(ctx, w)
}

// scrapeSuccess maps a store read outcome onto the gauge's two values.
func scrapeSuccess(err error) int64 {
	if err != nil {
		return 0
	}

	return 1
}

// writeInventoryMetrics emits the "how much of each thing exists right now"
// gauges. Every one of them is a live count of table rows, so none of them is a
// counter and none may be fed to rate(): retention shrinks them.
func (m *Metrics) writeInventoryMetrics(
	ctx context.Context,
	w io.Writer,
	stats store.OpsStats,
	now time.Time,
) error {
	updates, err := m.Ops.TelegramUpdateCounts(ctx)
	if err != nil {
		return err
	}

	samples := make([]sample, 0, len(updates))
	for _, count := range updates {
		samples = append(samples, sample{
			value:  int64(count.Count),
			labels: []string{"type", bounded(count.Name, "unknown")},
		})
	}
	writeAliasedFamily(w, "gatekeeper_updates", samples)

	webhooks, err := m.Ops.TributeEventCounts(ctx)
	if err != nil {
		return err
	}

	samples = make([]sample, 0, len(webhooks))
	for _, count := range webhooks {
		samples = append(samples, sample{
			value:  int64(count.Count),
			labels: []string{"status", bounded(count.Name, "received")},
		})
	}
	writeAliasedFamily(w, "gatekeeper_webhooks", samples)

	for _, count := range stats.ActiveSubscriptions {
		writeLine(w, "gatekeeper_subscriptions_active", int64(count.Count),
			"platform", bounded(count.Name, string(domain.PlatformManual)))
	}

	for _, count := range stats.Grants {
		writeLine(w, "gatekeeper_access_grants", int64(count.Count),
			"resource", bounded(string(count.Resource), "chat"),
			"state", bounded(string(count.State), "pending"))
	}

	invites, err := m.Ops.InviteLinkCounts(ctx)
	if err != nil {
		return err
	}
	for _, count := range invites {
		writeLine(w, "gatekeeper_invite_links", int64(count.Count),
			"resource", bounded(string(count.Resource), "chat"),
			"mode", bounded(string(count.Mode), "shared_join_request"),
			"status", bounded(string(count.Status), "created"))
	}

	revocations, err := m.Ops.RevocationReasonCounts(ctx)
	if err != nil {
		return err
	}

	samples = make([]sample, 0, len(revocations))
	for _, count := range revocations {
		samples = append(samples, sample{
			value:  int64(count.Count),
			labels: []string{"reason", bounded(count.Name, "other")},
		})
	}
	writeAliasedFamily(w, "gatekeeper_revocations", samples)

	// Emitted on every scrape that could read the database, in both states.
	//
	// This is not an exception to the "a declared family must have a source"
	// rule: the family has two sources. With the meta key present the value is
	// the age of the last completed pass; with the key absent — no pass has ever
	// completed — it is the age of the process, which is literally how long the
	// system has gone without one.
	//
	// Do not "fix" this back into an `if != nil`. The consumer is an idle
	// watchdog whose noDataState is Ok, so an absent series reads as healthy:
	// omitting the sample silences the watchdog exactly when it has something to
	// report. That the gap is masked today — main completes one reconcile pass
	// before the HTTP listener opens, so the key exists by the first scrape — is
	// startup ordering, not a contract.
	writeSecondsLine(w, "gatekeeper_reconcile_last_run_age_seconds",
		m.reconcileAge(stats.ReconcileLastRunAt, now))

	return nil
}

// writeOutboxMetrics emits queue state and queue throughput.
//
// The per-status gauges are zero-filled across the whole type × status cross
// product. Without that, a status with no rows produces no series at all, and
// an alert on `status="dead"` sits in NoData instead of reading zero — it would
// only start reporting once the very thing it watches for had happened.
//
// The zero-fill list is the domain registry, and that registry is a
// hand-maintained list rather than a derivation of the enum, so it can drift.
// Anything the database reports outside it is therefore emitted as well: drift
// may cost a zero-filled series, but it must never silently drop a series that
// has rows behind it.
func (m *Metrics) writeOutboxMetrics(
	ctx context.Context,
	w io.Writer,
	stats store.OpsStats,
	now time.Time,
) error {
	present := make(map[outboxPair]int64, len(stats.Outbox))
	for _, count := range stats.Outbox {
		present[outboxPair{
			actionType: count.Type,
			status:     count.Status,
		}] = int64(count.Count)
	}

	// The cross product comes from the domain enums rather than from a list
	// kept in this file. Those enums are themselves hand-maintained, though —
	// see the exhaustiveness tests that hold them against the schema and
	// against their own const block — so the registry is treated as the
	// zero-fill source and not as the truth about what exists.
	types := domain.AllActionTypes()
	statuses := domain.AllActionStatuses()

	registered := make(map[outboxPair]struct{}, len(types)*len(statuses))
	samples := make([]sample, 0, len(types)*len(statuses))

	for _, actionType := range types {
		for _, status := range statuses {
			pair := outboxPair{actionType: actionType, status: status}
			registered[pair] = struct{}{}
			samples = append(samples, outboxSample(pair, present[pair]))
		}
	}

	// Fail-safe against registry drift. A pair the database reports but the
	// registry has never heard of is emitted anyway: a stale registry may cost
	// a zero-filled series, which is a missing alert at worst, but it must
	// never make a series with real rows behind it disappear.
	for _, pair := range unregisteredPairs(present, registered) {
		samples = append(samples, outboxSample(pair, present[pair]))
	}

	writeAliasedFamily(w, "gatekeeper_outbox_actions", samples)

	backlog, err := m.Ops.OutboxBacklog(ctx, now)
	if err != nil {
		return err
	}

	writeLine(w, "gatekeeper_outbox_pending",
		int64(backlog.Queued+backlog.Running))
	writeLine(w, "gatekeeper_outbox_running", int64(backlog.Running))
	writeSecondsLine(w, "gatekeeper_outbox_oldest_queued_age_seconds",
		backlog.OldestQueuedAge)
	writeSecondsLine(w, "gatekeeper_outbox_oldest_due_age_seconds",
		backlog.OldestDueAge)

	return nil
}

// writeAlertMetrics emits operational alerts two ways, and the asymmetry is
// deliberate.
//
// The labelled family stays sparse on `kind`: alert kinds are string literals
// scattered across several packages, so zero-filling them would need a registry
// that does not exist and would rot the first time somebody raised a new kind
// without updating it. The severity rollup is zero-filled instead, because that
// is the series an alert rule watches — "any open critical alert" must read `0`
// when there are none, not NoData.
func (m *Metrics) writeAlertMetrics(ctx context.Context, w io.Writer) error {
	alerts, err := m.Ops.AlertCounts(ctx)
	if err != nil {
		return err
	}

	open := map[string]int64{}
	for _, count := range alerts {
		writeLine(w, "gatekeeper_admin_alerts", int64(count.Count),
			"kind", bounded(count.Kind, "unknown"),
			"severity", bounded(count.Severity, "info"),
			"state", bounded(count.Status, "open"))

		if count.Status == "open" {
			open[bounded(count.Severity, "info")] += int64(count.Count)
		}
	}

	for _, severity := range store.AlertSeverities() {
		writeLine(w, "gatekeeper_admin_alerts_open", open[severity],
			"severity", severity)
	}

	return nil
}

// writeEnforcerMetrics emits the worker-pool state that made the 2026-08-15
// outage invisible: a pool with no live workers left no trace in the exposition
// at all, so nothing could alert on it.
func (m *Metrics) writeEnforcerMetrics(w io.Writer) {
	if m.Enforcer == nil {
		return
	}

	stats := m.Enforcer()

	writeLine(w, "gatekeeper_enforcer_workers_configured",
		int64(stats.WorkersConfigured))
	writeLine(w, "gatekeeper_enforcer_workers_alive",
		int64(stats.WorkersAlive))
	writeLine(w, "gatekeeper_enforcer_workers_stale",
		int64(stats.WorkersStale))
	writeSecondsLine(w, "gatekeeper_enforcer_max_cycle_age_seconds",
		stats.MaxCycleAge)
	writeLine(w, "gatekeeper_enforcer_worker_restarts_total", stats.Restarts)

	for _, count := range stats.Processed {
		writeLine(w, "gatekeeper_outbox_actions_processed_total", count.Count,
			"type", bounded(count.Type, "unknown"),
			"result", bounded(count.Result, "unknown"))
	}
}

// writeTelegramErrorMetrics emits normalized Telegram failures. With the
// `timeout` category this is the earliest warning available for the class of
// outage where the API answers slowly and the queue quietly stops draining.
func (m *Metrics) writeTelegramErrorMetrics(w io.Writer) {
	if m.TelegramErrors == nil {
		return
	}

	for _, count := range m.TelegramErrors() {
		writeLine(w, "gatekeeper_telegram_api_errors_total", count.Count,
			"method", bounded(count.Method, "unknown"),
			"category", bounded(count.Category, "other"))
	}
}

func (m *Metrics) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}

	return m.Now()
}

// reconcileAge is the value of `gatekeeper_reconcile_last_run_age_seconds`: the
// age of the last completed reconcile pass, or the age of this process while no
// pass has completed.
//
// A meta value that is present but unparsable never reaches here. Ops.Stats
// fails on it, which aborts the store section and drops
// `gatekeeper_metrics_store_scrape_success` to 0 — the louder of the two
// signals — so the fallback covers absence only.
func (m *Metrics) reconcileAge(lastRun *time.Time, now time.Time) time.Duration {
	if lastRun != nil {
		return now.Sub(*lastRun)
	}

	return now.Sub(m.startedAt())
}

// startedAt is the instant the pre-first-pass fallback measures from.
func (m *Metrics) startedAt() time.Time {
	if m.ProcessStart.IsZero() {
		return processLoadedAt
	}

	return m.ProcessStart
}

// processLoadedAt is captured when this package is loaded, which is within
// microseconds of process start.
//
// It stands in for an unset Metrics.ProcessStart because the reconcile-age
// fallback must have a real instant behind it in every configuration: a zero
// time.Time would report an age of two thousand years, and omitting the sample
// again is the blind spot the fallback exists to close. A guarantee that
// depends on every caller remembering to fill a field is not a guarantee.
var processLoadedAt = time.Now()

// outboxPair is one cell of the type × status cross product.
type outboxPair struct {
	actionType domain.ActionType
	status     domain.ActionStatus
}

func outboxSample(pair outboxPair, value int64) sample {
	return sample{
		value: value,
		labels: []string{
			"type", string(pair.actionType),
			"status", string(pair.status),
		},
	}
}

// unregisteredPairs returns the pairs present in the database that the domain
// registry does not list, ordered so the exposition stays stable between
// scrapes.
func unregisteredPairs(
	present map[outboxPair]int64,
	registered map[outboxPair]struct{},
) []outboxPair {
	var extra []outboxPair

	for pair := range present {
		if _, ok := registered[pair]; !ok {
			extra = append(extra, pair)
		}
	}

	sort.Slice(extra, func(i, j int) bool {
		if extra[i].actionType != extra[j].actionType {
			return extra[i].actionType < extra[j].actionType
		}

		return extra[i].status < extra[j].status
	})

	return extra
}

// deprecatedAliases maps an honest metric name to the misleading name it
// replaces. Both are emitted for one release: the dashboards and alert rules
// that read the old names live in another repository and have to be switched in
// the same deployment, not before it.
//
// The aliases keep the old name but not the old type declaration: they were
// declared `counter` while being built from `SELECT count(*)`, which is what
// made rate() over them fabricate spikes whenever retention reaped a row.
var deprecatedAliases = map[string]string{
	"gatekeeper_updates":        "gatekeeper_updates_total",
	"gatekeeper_webhooks":       "gatekeeper_webhooks_total",
	"gatekeeper_outbox_actions": "gatekeeper_outbox_actions_total",
	"gatekeeper_revocations":    "gatekeeper_revocations_total",
}

// writeHelp declares every family this endpoint can emit, once, at the top of
// the response. A family is declared only when its source is wired, so a
// `# TYPE` line here always has a collector behind it.
func (m *Metrics) writeHelp(w io.Writer) {
	if m.Ops != nil {
		writeHelpLines(w, storeMetricHelp)
	}

	if m.Enforcer != nil {
		writeHelpLines(w, enforcerMetricHelp)
	}

	if m.TelegramErrors != nil {
		writeHelpLines(w, telegramMetricHelp)
	}
}

func writeHelpLines(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

var storeMetricHelp = []string{
	"# HELP gatekeeper_updates Stored Telegram updates by update type.",
	"# TYPE gatekeeper_updates gauge",
	"# HELP gatekeeper_updates_total DEPRECATED: use gatekeeper_updates.",
	"# TYPE gatekeeper_updates_total gauge",
	"# HELP gatekeeper_webhooks Stored Tribute events by terminal status.",
	"# TYPE gatekeeper_webhooks gauge",
	"# HELP gatekeeper_webhooks_total DEPRECATED: use gatekeeper_webhooks.",
	"# TYPE gatekeeper_webhooks_total gauge",
	"# HELP gatekeeper_subscriptions_active Active subscriptions by platform.",
	"# TYPE gatekeeper_subscriptions_active gauge",
	"# HELP gatekeeper_access_grants Club access grants by resource and state.",
	"# TYPE gatekeeper_access_grants gauge",
	"# HELP gatekeeper_invite_links Invite links by resource, mode and status.",
	"# TYPE gatekeeper_invite_links gauge",
	"# HELP gatekeeper_outbox_actions Outbox rows by action type and status.",
	"# TYPE gatekeeper_outbox_actions gauge",
	"# HELP gatekeeper_outbox_actions_total DEPRECATED: use " +
		"gatekeeper_outbox_actions.",
	"# TYPE gatekeeper_outbox_actions_total gauge",
	"# HELP gatekeeper_outbox_pending Outbox rows queued or running.",
	"# TYPE gatekeeper_outbox_pending gauge",
	"# HELP gatekeeper_outbox_running Outbox rows leased by a worker.",
	"# TYPE gatekeeper_outbox_running gauge",
	"# HELP gatekeeper_outbox_oldest_queued_age_seconds Age of the oldest " +
		"queued outbox row.",
	"# TYPE gatekeeper_outbox_oldest_queued_age_seconds gauge",
	"# HELP gatekeeper_outbox_oldest_due_age_seconds How long the oldest due " +
		"outbox row has been waiting past its run_after.",
	"# TYPE gatekeeper_outbox_oldest_due_age_seconds gauge",
	"# HELP gatekeeper_admin_alerts Operational alerts by kind, severity and " +
		"state.",
	"# TYPE gatekeeper_admin_alerts gauge",
	"# HELP gatekeeper_admin_alerts_open Open operational alerts by severity.",
	"# TYPE gatekeeper_admin_alerts_open gauge",
	"# HELP gatekeeper_revocations Recorded access revocations by reason.",
	"# TYPE gatekeeper_revocations gauge",
	"# HELP gatekeeper_revocations_total DEPRECATED: use " +
		"gatekeeper_revocations.",
	"# TYPE gatekeeper_revocations_total gauge",
	"# HELP gatekeeper_reconcile_last_run_age_seconds Time since the last " +
		"completed reconcile pass, or since process start while no pass has " +
		"completed.",
	"# TYPE gatekeeper_reconcile_last_run_age_seconds gauge",
	"# HELP gatekeeper_metrics_store_scrape_success Whether this scrape read " +
		"the database completely (1) or gave up part-way (0).",
	"# TYPE gatekeeper_metrics_store_scrape_success gauge",
}

var enforcerMetricHelp = []string{
	"# HELP gatekeeper_enforcer_workers_configured Outbox worker pool size.",
	"# TYPE gatekeeper_enforcer_workers_configured gauge",
	"# HELP gatekeeper_enforcer_workers_alive Workers currently in their loop.",
	"# TYPE gatekeeper_enforcer_workers_alive gauge",
	"# HELP gatekeeper_enforcer_workers_stale Workers whose last completed " +
		"loop is older than the staleness threshold.",
	"# TYPE gatekeeper_enforcer_workers_stale gauge",
	"# HELP gatekeeper_enforcer_max_cycle_age_seconds Age of the oldest " +
		"worker heartbeat.",
	"# TYPE gatekeeper_enforcer_max_cycle_age_seconds gauge",
	"# HELP gatekeeper_enforcer_worker_restarts_total Worker restarts since " +
		"process start.",
	"# TYPE gatekeeper_enforcer_worker_restarts_total counter",
	"# HELP gatekeeper_outbox_actions_processed_total Actions settled since " +
		"process start, by type and terminal outcome.",
	"# TYPE gatekeeper_outbox_actions_processed_total counter",
}

var telegramMetricHelp = []string{
	"# HELP gatekeeper_telegram_api_errors_total Normalized Telegram API " +
		"failures by method and error category.",
	"# TYPE gatekeeper_telegram_api_errors_total counter",
}

// sample is one exposition line: a value plus its alternating label pairs.
type sample struct {
	labels []string
	value  int64
}

// writeAliasedFamily emits a family under its honest name and then, for the
// length of the deprecation window, the same samples again under the retired
// name.
//
// Two contiguous groups rather than interleaved lines: an exposition must keep
// every sample of one metric family adjacent, and a scraper is entitled to
// reject a body that alternates between two names.
func writeAliasedFamily(w io.Writer, name string, samples []sample) {
	for _, s := range samples {
		writeLine(w, name, s.value, s.labels...)
	}

	alias, ok := deprecatedAliases[name]
	if !ok {
		return
	}

	for _, s := range samples {
		writeLine(w, alias, s.value, s.labels...)
	}
}

func writeLine(w io.Writer, name string, value int64, labels ...string) {
	if len(labels) == 0 {
		fmt.Fprintf(w, "%s %d\n", name, value)

		return
	}

	fmt.Fprintf(w, "%s{%s} %d\n", name, formatLabels(labels), value)
}

// writeSecondsLine emits a duration gauge. The value is formatted without an
// exponent so that a scrape stays readable by eye as well as by parser.
func writeSecondsLine(w io.Writer, name string, value time.Duration) {
	fmt.Fprintf(w, "%s %s\n", name,
		strconv.FormatFloat(value.Seconds(), 'f', -1, 64))
}

func formatLabels(labels []string) string {
	pairs := make([]string, 0, len(labels)/2)
	for i := 0; i+1 < len(labels); i += 2 {
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`,
			labels[i], escapeLabel(labels[i+1])))
	}
	sort.Strings(pairs)

	return strings.Join(pairs, ",")
}

func bounded(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return fallback
	}

	return value
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, `"`, `\"`)

	return value
}
