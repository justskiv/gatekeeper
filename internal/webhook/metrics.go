//nolint:wsl_v5 // Metrics exposition is a compact sequence of collectors.
package webhook

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

// Metrics writes a small Prometheus text exposition from durable state.
type Metrics struct {
	Ops *store.Ops
}

// Write emits metrics with bounded labels only.
func (m *Metrics) Write(ctx context.Context, w io.Writer) error {
	if m == nil || m.Ops == nil {
		writeMetricHelp(w)

		return nil
	}

	writeMetricHelp(w)

	updates, err := m.Ops.TelegramUpdateCounts(ctx)
	if err != nil {
		return err
	}
	for _, count := range updates {
		writeLine(w, "gatekeeper_updates_total", count.Count,
			"type", bounded(count.Name, "unknown"))
	}

	webhooks, err := m.Ops.TributeEventCounts(ctx)
	if err != nil {
		return err
	}
	for _, count := range webhooks {
		writeLine(w, "gatekeeper_webhooks_total", count.Count,
			"status", bounded(count.Name, "received"))
	}

	stats, err := m.Ops.Stats(ctx, time.Now())
	if err != nil {
		return err
	}

	for _, count := range stats.ActiveSubscriptions {
		writeLine(w, "gatekeeper_subscriptions_active", count.Count,
			"platform", bounded(count.Name, string(domain.PlatformManual)))
	}

	for _, count := range stats.Grants {
		writeLine(w, "gatekeeper_access_grants", count.Count,
			"resource", bounded(string(count.Resource), "chat"),
			"state", bounded(string(count.State), "pending"))
	}

	invites, err := m.Ops.InviteLinkCounts(ctx)
	if err != nil {
		return err
	}
	for _, count := range invites {
		writeLine(w, "gatekeeper_invite_links", count.Count,
			"resource", bounded(string(count.Resource), "chat"),
			"mode", bounded(string(count.Mode), "shared_join_request"),
			"status", bounded(string(count.Status), "created"))
	}

	for _, count := range stats.Outbox {
		writeLine(w, "gatekeeper_outbox_actions_total", count.Count,
			"type", bounded(string(count.Type), "send_dm"),
			"status", bounded(string(count.Status), "queued"))
	}

	pendingOutbox := 0
	for _, count := range stats.Outbox {
		if count.Status == "queued" || count.Status == "running" {
			pendingOutbox += count.Count
		}
	}
	writeLine(w, "gatekeeper_outbox_pending", pendingOutbox)

	revocations, err := m.Ops.RevocationReasonCounts(ctx)
	if err != nil {
		return err
	}
	for _, count := range revocations {
		writeLine(w, "gatekeeper_revocations_total", count.Count,
			"reason", bounded(count.Name, "other"))
	}

	return nil
}

func writeMetricHelp(w io.Writer) {
	lines := []string{
		"# TYPE gatekeeper_updates_total counter",
		"# TYPE gatekeeper_webhooks_total counter",
		"# TYPE gatekeeper_subscriptions_active gauge",
		"# TYPE gatekeeper_access_grants gauge",
		"# TYPE gatekeeper_invite_links gauge",
		"# TYPE gatekeeper_outbox_actions_total counter",
		"# TYPE gatekeeper_outbox_pending gauge",
		"# TYPE gatekeeper_telegram_api_errors_total counter",
		"# TYPE gatekeeper_reconcile_duration_seconds gauge",
		"# TYPE gatekeeper_revocations_total counter",
	}

	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

func writeLine(w io.Writer, name string, value int, labels ...string) {
	if len(labels) == 0 {
		fmt.Fprintf(w, "%s %d\n", name, value)

		return
	}

	pairs := make([]string, 0, len(labels)/2)
	for i := 0; i+1 < len(labels); i += 2 {
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`,
			labels[i], escapeLabel(labels[i+1])))
	}
	sort.Strings(pairs)

	fmt.Fprintf(w, "%s{%s} %d\n", name, strings.Join(pairs, ","), value)
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
