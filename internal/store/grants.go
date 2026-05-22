package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// Grants is the repository for the access_grants table: one row per
// (user, resource) pair.
type Grants struct {
	db *sql.DB
}

// NewGrants returns a Grants repository backed by db.
func NewGrants(db *sql.DB) *Grants {
	return &Grants{db: db}
}

// Upsert inserts or updates the access grant for a (user, resource)
// pair. created_at is preserved on update; updated_at is set to now.
func (r *Grants) Upsert(ctx context.Context, g domain.AccessGrant) error {
	now := rfc3339(time.Now())
	admittedBy := g.AdmittedBy
	if admittedBy == "" {
		admittedBy = "bot"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO access_grants (
			tg_id, resource, state, admitted_by, joined_at, revoked_at,
			revoked_reason, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(tg_id, resource) DO UPDATE SET
			state          = excluded.state,
			admitted_by    = excluded.admitted_by,
			joined_at      = excluded.joined_at,
			revoked_at     = excluded.revoked_at,
			revoked_reason = excluded.revoked_reason,
			updated_at     = excluded.updated_at`,
		g.TGID, string(g.Resource), string(g.State), admittedBy,
		nullTime(g.JoinedAt), nullTime(g.RevokedAt), g.RevokedReason,
		now, now)
	if err != nil {
		return fmt.Errorf("upsert grant %d/%s: %w", g.TGID, g.Resource, err)
	}
	return nil
}
