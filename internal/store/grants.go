package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// Grants is the repository for the access_grants table: one row per
// (user, resource) pair.
type Grants struct {
	db DBTX
}

// NewGrants returns a Grants repository backed by db or tx.
func NewGrants(db DBTX) *Grants {
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

// Get returns the access grant for (tgID, resource), or ErrNotFound.
func (r *Grants) Get(
	ctx context.Context, tgID int64, resource domain.Resource,
) (domain.AccessGrant, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tg_id, resource, state, admitted_by,
		       joined_at, revoked_at, revoked_reason
		FROM access_grants
		WHERE tg_id = ? AND resource = ?`,
		tgID, string(resource))

	var (
		g                     domain.AccessGrant
		resourceStr, stateStr string
		joinedAt, revokedAt   sql.NullString
	)
	err := row.Scan(&g.ID, &g.TGID, &resourceStr, &stateStr, &g.AdmittedBy,
		&joinedAt, &revokedAt, &g.RevokedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccessGrant{}, ErrNotFound
	}
	if err != nil {
		return domain.AccessGrant{}, fmt.Errorf(
			"get grant %d/%s: %w", tgID, resource, err)
	}

	g.Resource = domain.Resource(resourceStr)
	g.State = domain.GrantState(stateStr)
	if g.JoinedAt, err = parseNullTime(joinedAt); err != nil {
		return domain.AccessGrant{}, fmt.Errorf(
			"parse grant joined_at: %w", err)
	}
	if g.RevokedAt, err = parseNullTime(revokedAt); err != nil {
		return domain.AccessGrant{}, fmt.Errorf(
			"parse grant revoked_at: %w", err)
	}
	return g, nil
}
