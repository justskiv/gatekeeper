package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AuditEntry is one append-only business-journal record. A nil TGID,
// or an empty Source or Resource, is stored as a SQL NULL.
type AuditEntry struct {
	ID        int64
	TGID      *int64
	Kind      string
	Source    string
	Resource  string
	Actor     string // defaults to "system" when empty
	Detail    string
	CreatedAt time.Time
}

// Audit is the repository for the append-only audit_log table.
type Audit struct {
	db DBTX
}

// NewAudit returns an Audit repository backed by db or tx.
func NewAudit(db DBTX) *Audit {
	return &Audit{db: db}
}

// Append writes one audit record.
func (r *Audit) Append(ctx context.Context, e AuditEntry) error {
	actor := e.Actor
	if actor == "" {
		actor = "system"
	}
	var tgID any
	if e.TGID != nil {
		tgID = *e.TGID
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO audit_log (tg_id, kind, source, resource, actor, detail, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		tgID, e.Kind, nullString(e.Source), nullString(e.Resource),
		actor, e.Detail, rfc3339(time.Now()))
	if err != nil {
		return fmt.Errorf("append audit %q: %w", e.Kind, err)
	}
	return nil
}

// ListRecentByUser returns recent audit rows for a user from newest to oldest.
func (r *Audit) ListRecentByUser(
	ctx context.Context, tgID int64, limit int,
) ([]AuditEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tg_id, kind, source, resource, actor, detail, created_at
		FROM audit_log
		WHERE tg_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, tgID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent audit for %d: %w", tgID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []AuditEntry
	for rows.Next() {
		var (
			e                AuditEntry
			tgIDValue        sql.NullInt64
			source, resource sql.NullString
			createdAt        string
		)
		if err := rows.Scan(&e.ID, &tgIDValue, &e.Kind, &source, &resource,
			&e.Actor, &e.Detail, &createdAt); err != nil {
			return nil, fmt.Errorf("scan audit row: %w", err)
		}
		if tgIDValue.Valid {
			tgID := tgIDValue.Int64
			e.TGID = &tgID
		}
		if source.Valid {
			e.Source = source.String
		}
		if resource.Valid {
			e.Resource = resource.String
		}
		if e.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse audit created_at: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit for %d: %w", tgID, err)
	}
	return out, nil
}
