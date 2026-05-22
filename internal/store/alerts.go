package store

import (
	"context"
	"database/sql"
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
	db *sql.DB
}

// NewAlerts returns an Alerts repository backed by db.
func NewAlerts(db *sql.DB) *Alerts {
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
