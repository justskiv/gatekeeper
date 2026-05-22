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
	TGID     *int64
	Kind     string
	Source   string
	Resource string
	Actor    string // defaults to "system" when empty
	Detail   string
}

// Audit is the repository for the append-only audit_log table.
type Audit struct {
	db *sql.DB
}

// NewAudit returns an Audit repository backed by db.
func NewAudit(db *sql.DB) *Audit {
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
