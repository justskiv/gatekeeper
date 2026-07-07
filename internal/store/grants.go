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

// MarkPending idempotently stores a pending grant without changing
// already joined access. It returns the grant cycle timestamp:
// stable while the row remains pending, refreshed when a new pending cycle
// starts after left/external states.
func (r *Grants) MarkPending(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
) (time.Time, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var updatedAt string

	err := r.db.QueryRowContext(ctx, `
		INSERT INTO access_grants (
			tg_id, resource, state, admitted_by, created_at, updated_at)
		VALUES (?, ?, 'pending', 'bot', ?, ?)
		ON CONFLICT(tg_id, resource) DO UPDATE SET
			state = CASE
				WHEN access_grants.state = 'joined'
				THEN access_grants.state
				ELSE 'pending'
			END,
			admitted_by = CASE
				WHEN access_grants.state = 'joined'
				THEN access_grants.admitted_by
				ELSE 'bot'
			END,
			revoked_at = CASE
				WHEN access_grants.state = 'joined'
				THEN access_grants.revoked_at
				ELSE NULL
			END,
			revoked_reason = CASE
				WHEN access_grants.state = 'joined'
				THEN access_grants.revoked_reason
				ELSE ''
			END,
			updated_at = CASE
				WHEN access_grants.state IN ('pending', 'joined')
				THEN access_grants.updated_at
				ELSE excluded.updated_at
			END
		RETURNING updated_at`,
		tgID, string(resource), now, now).Scan(&updatedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf(
			"mark grant pending %d/%s: %w", tgID, resource, err)
	}

	pendingAt, err := parseTime(updatedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse pending grant updated_at: %w", err)
	}

	return pendingAt, nil
}

// MarkJoinedByAdmission records a fresh bot-approved admission cycle. Unlike
// passive verification, this may restore a previously revoked grant.
func (r *Grants) MarkJoinedByAdmission(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
	admittedBy string,
) error {
	nowTime := time.Now()
	now := rfc3339(nowTime)

	if admittedBy == "" {
		admittedBy = "bot"
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO access_grants (
			tg_id, resource, state, admitted_by, joined_at, created_at, updated_at)
		VALUES (?, ?, 'joined', ?, ?, ?, ?)
		ON CONFLICT(tg_id, resource) DO UPDATE SET
			state = 'joined',
			admitted_by = excluded.admitted_by,
			joined_at = COALESCE(access_grants.joined_at, excluded.joined_at),
			revoked_at = NULL,
			revoked_reason = '',
			updated_at = excluded.updated_at`,
		tgID, string(resource), admittedBy, rfc3339(nowTime), now, now)
	if err != nil {
		return fmt.Errorf("mark admitted grant joined %d/%s: %w", tgID, resource, err)
	}

	return nil
}

// MarkJoined records observed membership and admission metadata.
func (r *Grants) MarkJoined(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
	admittedBy string,
) error {
	nowTime := time.Now()
	now := rfc3339(nowTime)

	if admittedBy == "" {
		admittedBy = "bot"
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO access_grants (
			tg_id, resource, state, admitted_by, joined_at, created_at, updated_at)
		VALUES (?, ?, 'joined', ?, ?, ?, ?)
		ON CONFLICT(tg_id, resource) DO UPDATE SET
			state = CASE
				WHEN access_grants.state = 'revoked'
				THEN access_grants.state
				ELSE 'joined'
			END,
			admitted_by = CASE
				WHEN access_grants.state = 'revoked'
				THEN access_grants.admitted_by
				ELSE excluded.admitted_by
			END,
			joined_at = CASE
				WHEN access_grants.state = 'revoked'
				THEN access_grants.joined_at
				ELSE COALESCE(access_grants.joined_at, excluded.joined_at)
			END,
			revoked_at = CASE
				WHEN access_grants.state = 'revoked'
				THEN access_grants.revoked_at
				ELSE NULL
			END,
			revoked_reason = CASE
				WHEN access_grants.state = 'revoked'
				THEN access_grants.revoked_reason
				ELSE ''
			END,
			updated_at = CASE
				WHEN access_grants.state = 'revoked'
				THEN access_grants.updated_at
				ELSE excluded.updated_at
			END`,
		tgID, string(resource), admittedBy, rfc3339(nowTime), now, now)
	if err != nil {
		return fmt.Errorf("mark grant joined %d/%s: %w", tgID, resource, err)
	}

	return nil
}

// MarkLeftUnlessRevoked records that a user left a resource, preserving
// a prior revoked state.
func (r *Grants) MarkLeftUnlessRevoked(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
) (bool, error) {
	now := rfc3339(time.Now())

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO access_grants (
			tg_id, resource, state, admitted_by, created_at, updated_at)
		VALUES (?, ?, 'left', 'bot', ?, ?)
		ON CONFLICT(tg_id, resource) DO UPDATE SET
			state = CASE
				WHEN access_grants.state = 'revoked'
				THEN access_grants.state
				ELSE 'left'
			END,
			updated_at = CASE
				WHEN access_grants.state = 'revoked'
				THEN access_grants.updated_at
				ELSE excluded.updated_at
			END`,
		tgID, string(resource), now, now)
	if err != nil {
		return false, fmt.Errorf("mark grant left %d/%s: %w", tgID, resource, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read left grant rows affected: %w", err)
	}

	return affected > 0, nil
}

// Get returns the access grant for (tgID, resource), or ErrNotFound.
func (r *Grants) Get(
	ctx context.Context, tgID int64, resource domain.Resource,
) (domain.AccessGrant, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tg_id, resource, state, admitted_by,
		       joined_at, revoked_at, revoked_reason, updated_at
		FROM access_grants
		WHERE tg_id = ? AND resource = ?`,
		tgID, string(resource))

	g, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccessGrant{}, ErrNotFound
	}

	if err != nil {
		return domain.AccessGrant{}, fmt.Errorf(
			"get grant %d/%s: %w", tgID, resource, err)
	}

	return g, nil
}

// ListByUser returns all access grants for a user.
func (r *Grants) ListByUser(
	ctx context.Context, tgID int64,
) ([]domain.AccessGrant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tg_id, resource, state, admitted_by,
		       joined_at, revoked_at, revoked_reason, updated_at
		FROM access_grants
		WHERE tg_id = ?
		ORDER BY resource`, tgID)
	if err != nil {
		return nil, fmt.Errorf("list grants for %d: %w", tgID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.AccessGrant

	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, g)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate grants for %d: %w", tgID, err)
	}

	return out, nil
}

// ListEligibleForRevoke returns grants that automatic revocation may touch.
func (r *Grants) ListEligibleForRevoke(
	ctx context.Context,
	tgID int64,
) ([]domain.AccessGrant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tg_id, resource, state, admitted_by,
		       joined_at, revoked_at, revoked_reason, updated_at
		FROM access_grants
		WHERE tg_id = ?
		  AND state IN ('joined', 'pending')
		  AND admitted_by = 'bot'
		ORDER BY resource`, tgID)
	if err != nil {
		return nil, fmt.Errorf("list revoke-eligible grants for %d: %w", tgID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.AccessGrant

	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, g)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate revoke-eligible grants for %d: %w", tgID, err)
	}

	return out, nil
}

// ListBotAdmittedJoinedTGIDs returns users with current bot-managed access.
func (r *Grants) ListBotAdmittedJoinedTGIDs(
	ctx context.Context,
	limit int,
) ([]int64, error) {
	if limit <= 0 {
		limit = 1000
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT tg_id
		FROM access_grants
		WHERE state = 'joined'
		  AND admitted_by = 'bot'
		ORDER BY tg_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list bot-admitted joined tg_ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []int64

	for rows.Next() {
		var tgID int64
		if err := rows.Scan(&tgID); err != nil {
			return nil, fmt.Errorf("scan joined grant tg_id: %w", err)
		}

		out = append(out, tgID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate joined grant tg_ids: %w", err)
	}

	return out, nil
}

// Revoke marks one grant as revoked. Missing or already-revoked rows are no-op.
func (r *Grants) Revoke(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
	reason string,
) (bool, error) {
	now := rfc3339(time.Now())

	res, err := r.db.ExecContext(ctx, `
		UPDATE access_grants
		SET state = 'revoked',
		    revoked_at = ?,
		    revoked_reason = ?,
		    updated_at = ?
		WHERE tg_id = ?
		  AND resource = ?
		  AND state IN ('joined', 'pending')
		  AND admitted_by = 'bot'`,
		now, reason, now, tgID, string(resource))
	if err != nil {
		return false, fmt.Errorf("revoke grant %d/%s: %w", tgID, resource, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read revoked grant rows affected: %w", err)
	}

	return affected > 0, nil
}

type grantScanner interface {
	Scan(dest ...any) error
}

func scanGrant(scanner grantScanner) (domain.AccessGrant, error) {
	var (
		g                     domain.AccessGrant
		resourceStr, stateStr string
		joinedAt, revokedAt   sql.NullString
		updatedAt             string
	)

	err := scanner.Scan(&g.ID, &g.TGID, &resourceStr, &stateStr, &g.AdmittedBy,
		&joinedAt, &revokedAt, &g.RevokedReason, &updatedAt)
	if err != nil {
		return domain.AccessGrant{}, err
	}

	g.Resource = domain.Resource(resourceStr)

	g.State = domain.GrantState(stateStr)
	if g.JoinedAt, err = parseNullTime(joinedAt); err != nil {
		return domain.AccessGrant{}, fmt.Errorf("parse grant joined_at: %w", err)
	}

	if g.RevokedAt, err = parseNullTime(revokedAt); err != nil {
		return domain.AccessGrant{}, fmt.Errorf("parse grant revoked_at: %w", err)
	}

	if g.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.AccessGrant{}, fmt.Errorf("parse grant updated_at: %w", err)
	}

	return g, nil
}
