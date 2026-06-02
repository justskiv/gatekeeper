//nolint:wsl_v5 // Read-model scans are kept compact and repetitive.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// CountByName is a bounded-label counter value.
type CountByName struct {
	Name  string
	Count int
}

// GrantCount is an access grant count grouped by resource and state.
type GrantCount struct {
	Resource domain.Resource
	State    domain.GrantState
	Count    int
}

// OutboxCount is an outbox count grouped by action type and status.
type OutboxCount struct {
	Type   domain.ActionType
	Status domain.ActionStatus
	Count  int
}

// InviteLinkCount is an invite-link count grouped by resource, mode and status.
type InviteLinkCount struct {
	Resource domain.Resource
	Mode     domain.InviteMode
	Status   domain.InviteStatus
	Count    int
}

// OpsStats is the owner-facing runtime summary.
type OpsStats struct {
	ActiveSubscriptions []CountByName
	Grants              []GrantCount
	PendingRevocations  int
	DueRevocations      int
	Health              map[string]string
	ReconcileLastRunAt  *time.Time
	Outbox              []OutboxCount
	OpenAlerts          int
}

// OpsAlert is one open alert for owner summaries.
type OpsAlert struct {
	ID        int64
	Severity  string
	Kind      string
	Title     string
	Detail    string
	CreatedAt time.Time
}

// ExportRow is one CSV row for owner export.
type ExportRow struct {
	TGID          int64
	Username      string
	FirstName     string
	LastName      string
	LanguageCode  string
	DMState       domain.DMState
	Banned        bool
	BannedReason  string
	Subscriptions string
	ExpiresAt     string
	GrantChat     string
	GrantChannel  string
	Whitelisted   bool
	LastSeenAt    time.Time
}

// Ops contains read-only operational queries for owner commands and metrics.
type Ops struct {
	db DBTX
}

// NewOps returns read-only operational queries backed by db or tx.
func NewOps(db DBTX) *Ops {
	return &Ops{db: db}
}

// Stats returns a bounded operational summary.
func (r *Ops) Stats(ctx context.Context, now time.Time) (OpsStats, error) {
	stats := OpsStats{Health: map[string]string{}}

	var err error
	if stats.ActiveSubscriptions, err = r.counts(ctx, `
		SELECT platform, count(*)
		FROM subscriptions
		WHERE status = 'active'
		GROUP BY platform
		ORDER BY platform`); err != nil {
		return OpsStats{}, err
	}

	if stats.Grants, err = r.grantCounts(ctx); err != nil {
		return OpsStats{}, err
	}

	if stats.PendingRevocations, err = r.scalarCount(ctx,
		`SELECT count(*) FROM pending_revocations`); err != nil {
		return OpsStats{}, err
	}

	if stats.DueRevocations, err = r.scalarCount(ctx,
		`SELECT count(*) FROM pending_revocations WHERE scheduled_at <= ?`,
		rfc3339(now)); err != nil {
		return OpsStats{}, err
	}

	if stats.Health, err = r.health(ctx); err != nil {
		return OpsStats{}, err
	}

	if stats.ReconcileLastRunAt, err = r.optionalMetaTime(
		ctx, "reconcile.last_run_at"); err != nil {
		return OpsStats{}, err
	}

	if stats.Outbox, err = r.outboxCounts(ctx); err != nil {
		return OpsStats{}, err
	}

	if stats.OpenAlerts, err = r.scalarCount(ctx,
		`SELECT count(*) FROM admin_alerts WHERE status = 'open'`); err != nil {
		return OpsStats{}, err
	}

	return stats, nil
}

// OpenAlerts returns open alerts ordered for operator triage.
func (r *Ops) OpenAlerts(ctx context.Context, limit int) ([]OpsAlert, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, severity, kind, title, detail, created_at
		FROM admin_alerts
		WHERE status = 'open'
		ORDER BY
			CASE severity
				WHEN 'critical' THEN 0
				WHEN 'error' THEN 1
				WHEN 'warning' THEN 2
				ELSE 3
			END,
			created_at DESC,
			id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list open alerts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []OpsAlert
	for rows.Next() {
		var alert OpsAlert
		var createdAt string
		if err := rows.Scan(&alert.ID, &alert.Severity, &alert.Kind,
			&alert.Title, &alert.Detail, &createdAt); err != nil {
			return nil, fmt.Errorf("scan open alert: %w", err)
		}

		var err error
		if alert.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse alert created_at: %w", err)
		}

		out = append(out, alert)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open alerts: %w", err)
	}

	return out, nil
}

// ExportRows returns current users and their active access state.
func (r *Ops) ExportRows(ctx context.Context, limit int) ([]ExportRow, error) {
	if limit <= 0 {
		limit = 1000
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT
			u.tg_id,
			u.username,
			u.first_name,
			u.last_name,
			u.language_code,
			u.dm_state,
			u.banned,
			u.banned_reason,
			COALESCE((
				SELECT group_concat(platform, '|') FROM (
					SELECT platform
					FROM subscriptions s
					WHERE s.tg_id = u.tg_id AND s.status = 'active'
					ORDER BY platform
				)
			), ''),
			COALESCE((
				SELECT group_concat(expires_at, '|') FROM (
					SELECT COALESCE(expires_at, '') AS expires_at
					FROM subscriptions s
					WHERE s.tg_id = u.tg_id AND s.status = 'active'
					ORDER BY platform
				)
			), ''),
			COALESCE((
				SELECT state
				FROM access_grants g
				WHERE g.tg_id = u.tg_id AND g.resource = 'chat'
			), ''),
			COALESCE((
				SELECT state
				FROM access_grants g
				WHERE g.tg_id = u.tg_id AND g.resource = 'channel'
			), ''),
			EXISTS(SELECT 1 FROM whitelist w WHERE w.tg_id = u.tg_id),
			u.last_seen_at
		FROM users u
		ORDER BY u.tg_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("export users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ExportRow
	for rows.Next() {
		var (
			row        ExportRow
			dmState    string
			lastSeenAt string
		)
		if err := rows.Scan(&row.TGID, &row.Username, &row.FirstName,
			&row.LastName, &row.LanguageCode, &dmState, &row.Banned,
			&row.BannedReason, &row.Subscriptions, &row.ExpiresAt,
			&row.GrantChat, &row.GrantChannel, &row.Whitelisted,
			&lastSeenAt); err != nil {
			return nil, fmt.Errorf("scan export row: %w", err)
		}

		row.DMState = domain.DMState(dmState)
		var err error
		if row.LastSeenAt, err = parseTime(lastSeenAt); err != nil {
			return nil, fmt.Errorf("parse export last_seen_at: %w", err)
		}

		out = append(out, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export rows: %w", err)
	}

	return out, nil
}

// TelegramUpdateCounts returns update counts grouped by type.
func (r *Ops) TelegramUpdateCounts(ctx context.Context) ([]CountByName, error) {
	return r.counts(ctx, `
		SELECT update_type, count(*)
		FROM telegram_updates
		GROUP BY update_type
		ORDER BY update_type`)
}

// TributeEventCounts returns webhook counts grouped by terminal status.
func (r *Ops) TributeEventCounts(ctx context.Context) ([]CountByName, error) {
	return r.counts(ctx, `
		SELECT status, count(*)
		FROM tribute_events
		GROUP BY status
		ORDER BY status`)
}

// RevocationReasonCounts returns bounded revocation counters.
func (r *Ops) RevocationReasonCounts(ctx context.Context) ([]CountByName, error) {
	return r.counts(ctx, `
		SELECT
			CASE
				WHEN detail LIKE '%reason=inactive%' THEN 'inactive'
				WHEN kind LIKE '%manual%' THEN 'manual'
				ELSE 'other'
			END AS reason,
			count(*)
		FROM audit_log
		WHERE kind = 'access_revoked'
		GROUP BY reason
		ORDER BY reason`)
}

// InviteLinkCounts returns invite-link counts grouped by bounded dimensions.
func (r *Ops) InviteLinkCounts(ctx context.Context) ([]InviteLinkCount, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT resource, mode, status, count(*)
		FROM invite_links
		GROUP BY resource, mode, status
		ORDER BY resource, mode, status`)
	if err != nil {
		return nil, fmt.Errorf("read invite link counts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []InviteLinkCount
	for rows.Next() {
		var (
			count    InviteLinkCount
			resource string
			mode     string
			status   string
		)
		if err := rows.Scan(&resource, &mode, &status, &count.Count); err != nil {
			return nil, fmt.Errorf("scan invite link count: %w", err)
		}

		count.Resource = domain.Resource(resource)
		count.Mode = domain.InviteMode(mode)
		count.Status = domain.InviteStatus(status)
		out = append(out, count)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invite link counts: %w", err)
	}

	return out, nil
}

func (r *Ops) counts(ctx context.Context, query string) ([]CountByName, error) {
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read counts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []CountByName
	for rows.Next() {
		var count CountByName
		if err := rows.Scan(&count.Name, &count.Count); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}

		out = append(out, count)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate counts: %w", err)
	}

	return out, nil
}

//nolint:dupl // Similar scan shape maps different bounded dimensions.
func (r *Ops) grantCounts(ctx context.Context) ([]GrantCount, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT resource, state, count(*)
		FROM access_grants
		GROUP BY resource, state
		ORDER BY resource, state`)
	if err != nil {
		return nil, fmt.Errorf("read grant counts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []GrantCount
	for rows.Next() {
		var (
			count    GrantCount
			resource string
			state    string
		)
		if err := rows.Scan(&resource, &state, &count.Count); err != nil {
			return nil, fmt.Errorf("scan grant count: %w", err)
		}

		count.Resource = domain.Resource(resource)
		count.State = domain.GrantState(state)
		out = append(out, count)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate grant counts: %w", err)
	}

	return out, nil
}

//nolint:dupl // Similar scan shape maps different bounded dimensions.
func (r *Ops) outboxCounts(ctx context.Context) ([]OutboxCount, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT action_type, status, count(*)
		FROM access_actions
		GROUP BY action_type, status
		ORDER BY action_type, status`)
	if err != nil {
		return nil, fmt.Errorf("read outbox counts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []OutboxCount
	for rows.Next() {
		var (
			count  OutboxCount
			typ    string
			status string
		)
		if err := rows.Scan(&typ, &status, &count.Count); err != nil {
			return nil, fmt.Errorf("scan outbox count: %w", err)
		}

		count.Type = domain.ActionType(typ)
		count.Status = domain.ActionStatus(status)
		out = append(out, count)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox counts: %w", err)
	}

	return out, nil
}

func (r *Ops) health(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT key, value
		FROM meta
		WHERE key LIKE 'health.%'
		ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("read health meta: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan health meta: %w", err)
		}

		out[key] = value
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate health meta: %w", err)
	}

	return out, nil
}

func (r *Ops) optionalMetaTime(ctx context.Context, key string) (*time.Time, error) {
	value, ok, err := NewMeta(r.db).Get(ctx, key)
	if err != nil || !ok {
		return nil, err
	}

	t, err := parseTime(value)
	if err != nil {
		return nil, fmt.Errorf("parse meta %q time: %w", key, err)
	}

	return &t, nil
}

func (r *Ops) scalarCount(ctx context.Context, query string, args ...any) (int, error) {
	var out int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&out); err != nil {
		if errorsIsNoRows(err) {
			return 0, nil
		}

		return 0, fmt.Errorf("read scalar count: %w", err)
	}

	return out, nil
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
