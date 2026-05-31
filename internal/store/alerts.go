package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AlertInput describes a new operational alert for the administrator.
// A nil TGID is stored as a SQL NULL.
type AlertInput struct {
	Severity string // info|warning|error|critical
	Kind     string
	Title    string
	Detail   string
	TGID     *int64
}

// Alerts is the repository for the admin_alerts table.
type Alerts struct {
	db DBTX
}

// NewAlerts returns an Alerts repository backed by db or tx.
func NewAlerts(db DBTX) *Alerts {
	return &Alerts{db: db}
}

// Create inserts a new open alert and returns its generated id.
func (r *Alerts) Create(ctx context.Context, a AlertInput) (int64, error) {
	var tgID any
	if a.TGID != nil {
		tgID = *a.TGID
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO admin_alerts (severity, status, kind, title, detail, tg_id, created_at)
		VALUES (?, 'open', ?, ?, ?, ?, ?)`,
		a.Severity, a.Kind, a.Title, a.Detail, tgID, rfc3339(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("create alert %q: %w", a.Kind, err)
	}

	return res.LastInsertId()
}

// CreateOpenIfMissing inserts an open alert unless one with the same kind
// and title is already open. The bool reports whether a new row was inserted.
func (r *Alerts) CreateOpenIfMissing(
	ctx context.Context, a AlertInput,
) (int64, bool, error) {
	var existingID int64

	err := r.db.QueryRowContext(ctx, `
		SELECT id
		FROM admin_alerts
		WHERE status = 'open' AND kind = ? AND title = ?
		ORDER BY id
		LIMIT 1`,
		a.Kind, a.Title).Scan(&existingID)
	if err == nil {
		return existingID, false, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false,
			fmt.Errorf("find open alert %q/%q: %w", a.Kind, a.Title, err)
	}

	id, err := r.Create(ctx, a)
	if err != nil {
		return 0, false, err
	}

	return id, true, nil
}

// ResolveOpenByTitle resolves matching open alerts. Missing alerts are a no-op.
func (r *Alerts) ResolveOpenByTitle(ctx context.Context, kind, title string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE admin_alerts
		SET status = 'resolved', resolved_at = ?
		WHERE status = 'open' AND kind = ? AND title = ?`,
		rfc3339(time.Now()), kind, title)
	if err != nil {
		return fmt.Errorf("resolve alert %q/%q: %w", kind, title, err)
	}

	return nil
}
