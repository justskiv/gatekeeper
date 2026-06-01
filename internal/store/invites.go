package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// InviteLinkInput describes a Telegram invite link to persist.
type InviteLinkInput struct {
	TGID               *int64
	Resource           domain.Resource
	Mode               domain.InviteMode
	InviteLink         string
	InviteLinkHash     string
	TelegramName       string
	Nonce              string
	CreatesJoinRequest bool
	ExpiresAt          *time.Time
}

// Invites is the repository for invite_links.
type Invites struct {
	db DBTX
}

// NewInvites returns an Invites repository backed by db or tx.
func NewInvites(db DBTX) *Invites {
	return &Invites{db: db}
}

// FindActiveShared returns the active shared link for a resource and mode.
func (r *Invites) FindActiveShared(
	ctx context.Context,
	resource domain.Resource,
	mode domain.InviteMode,
) (domain.InviteLink, bool, error) {
	link, err := scanInviteLink(r.db.QueryRowContext(ctx, `
		SELECT id, tg_id, resource, mode, invite_link, invite_link_hash,
		       telegram_name, nonce, status, creates_join_request, expires_at,
		       sent_at, used_at, revoked_at, attempted_by, last_error,
		       created_at, updated_at
		FROM invite_links
		WHERE tg_id IS NULL
		  AND resource = ?
		  AND mode = ?
		  AND status IN ('created', 'sent')
		ORDER BY id
		LIMIT 1`, string(resource), string(mode)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InviteLink{}, false, nil
	}

	if err != nil {
		return domain.InviteLink{}, false,
			fmt.Errorf("find active shared invite %s/%s: %w", resource, mode, err)
	}

	return link, true, nil
}

// FindActivePersonal returns the active personal or direct link.
func (r *Invites) FindActivePersonal(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
	mode domain.InviteMode,
) (domain.InviteLink, bool, error) {
	link, err := scanInviteLink(r.db.QueryRowContext(ctx, `
		SELECT id, tg_id, resource, mode, invite_link, invite_link_hash,
		       telegram_name, nonce, status, creates_join_request, expires_at,
		       sent_at, used_at, revoked_at, attempted_by, last_error,
		       created_at, updated_at
		FROM invite_links
		WHERE tg_id = ?
		  AND resource = ?
		  AND mode = ?
		  AND status IN ('created', 'sent')
		ORDER BY id
		LIMIT 1`, tgID, string(resource), string(mode)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InviteLink{}, false, nil
	}

	if err != nil {
		return domain.InviteLink{}, false,
			fmt.Errorf("find active personal invite %d/%s/%s: %w",
				tgID, resource, mode, err)
	}

	return link, true, nil
}

// FindActiveByHash returns an active invite link for a managed resource by hash.
func (r *Invites) FindActiveByHash(
	ctx context.Context,
	resource domain.Resource,
	inviteLinkHash string,
) (domain.InviteLink, bool, error) {
	if inviteLinkHash == "" {
		return domain.InviteLink{}, false, nil
	}

	link, err := scanInviteLink(r.db.QueryRowContext(ctx, `
		SELECT id, tg_id, resource, mode, invite_link, invite_link_hash,
		       telegram_name, nonce, status, creates_join_request, expires_at,
		       sent_at, used_at, revoked_at, attempted_by, last_error,
		       created_at, updated_at
		FROM invite_links
		WHERE invite_link_hash = ?
		  AND resource = ?
		  AND status IN ('created', 'sent')
		LIMIT 1`, inviteLinkHash, string(resource)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InviteLink{}, false, nil
	}

	if err != nil {
		return domain.InviteLink{}, false,
			fmt.Errorf("find active invite by hash %s/%s: %w",
				resource, inviteLinkHash, err)
	}

	return link, true, nil
}

// SaveCreated inserts a newly created Telegram invite link.
func (r *Invites) SaveCreated(
	ctx context.Context,
	input InviteLinkInput,
) (domain.InviteLink, error) {
	if input.InviteLink == "" {
		return domain.InviteLink{}, errors.New("invite link is required")
	}

	if input.InviteLinkHash == "" {
		return domain.InviteLink{}, errors.New("invite link hash is required")
	}

	now := rfc3339(time.Now())

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO invite_links (
			tg_id, resource, mode, invite_link, invite_link_hash,
			telegram_name, nonce, status, creates_join_request, expires_at,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'created', ?, ?, ?, ?)`,
		sqlNullInt64(input.TGID), string(input.Resource), string(input.Mode),
		input.InviteLink, input.InviteLinkHash, input.TelegramName, input.Nonce,
		input.CreatesJoinRequest, nullTime(input.ExpiresAt), now, now)
	if err != nil {
		return domain.InviteLink{}, fmt.Errorf(
			"save invite %s/%s: %w", input.Resource, input.Mode, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return domain.InviteLink{}, fmt.Errorf("read invite insert id: %w", err)
	}

	return r.GetByID(ctx, id)
}

// GetByID returns an invite link by primary key.
func (r *Invites) GetByID(ctx context.Context, id int64) (domain.InviteLink, error) {
	link, err := scanInviteLink(r.db.QueryRowContext(ctx, `
		SELECT id, tg_id, resource, mode, invite_link, invite_link_hash,
		       telegram_name, nonce, status, creates_join_request, expires_at,
		       sent_at, used_at, revoked_at, attempted_by, last_error,
		       created_at, updated_at
		FROM invite_links
		WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InviteLink{}, ErrNotFound
	}

	if err != nil {
		return domain.InviteLink{}, fmt.Errorf("get invite %d: %w", id, err)
	}

	return link, nil
}

// MarkStatus moves an invite link to a new lifecycle status.
func (r *Invites) MarkStatus(
	ctx context.Context,
	id int64,
	status domain.InviteStatus,
	attemptedBy *int64,
	lastError string,
) error {
	now := rfc3339(time.Now())

	var sentAt, usedAt, revokedAt any

	switch status {
	case domain.InviteSent:
		sentAt = now
	case domain.InviteUsed, domain.InviteUsedByOther:
		usedAt = now
	case domain.InviteRevoked, domain.InviteExpired:
		revokedAt = now
	case domain.InviteFailed:
	default:
		return fmt.Errorf("unsupported invite status transition %q", status)
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE invite_links
		SET status = ?,
		    sent_at = COALESCE(?, sent_at),
		    used_at = COALESCE(?, used_at),
		    revoked_at = COALESCE(?, revoked_at),
		    attempted_by = COALESCE(?, attempted_by),
		    last_error = ?,
		    updated_at = ?
		WHERE id = ?`,
		string(status), sentAt, usedAt, revokedAt, sqlNullInt64(attemptedBy),
		lastError, now, id)
	if err != nil {
		return fmt.Errorf("mark invite %d %s: %w", id, status, err)
	}

	return requireAffected(res, "invite", id)
}

// ListExpiredActive returns active invite links whose TTL has elapsed.
func (r *Invites) ListExpiredActive(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]domain.InviteLink, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tg_id, resource, mode, invite_link, invite_link_hash,
		       telegram_name, nonce, status, creates_join_request, expires_at,
		       sent_at, used_at, revoked_at, attempted_by, last_error,
		       created_at, updated_at
		FROM invite_links
		WHERE status IN ('created', 'sent')
		  AND expires_at IS NOT NULL
		  AND expires_at <= ?
		ORDER BY expires_at, id
		LIMIT ?`, rfc3339(now), limit)
	if err != nil {
		return nil, fmt.Errorf("list expired active invites: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.InviteLink

	for rows.Next() {
		link, err := scanInviteLink(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, link)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired active invites: %w", err)
	}

	return out, nil
}

type inviteLinkScanner interface {
	Scan(dest ...any) error
}

func scanInviteLink(scanner inviteLinkScanner) (domain.InviteLink, error) {
	var (
		link                                 domain.InviteLink
		tgID, attemptedBy                    sql.NullInt64
		resource, mode, status               string
		createsJoinRequest                   int
		expiresAt, sentAt, usedAt, revokedAt sql.NullString
		createdAt, updatedAt                 string
	)

	err := scanner.Scan(&link.ID, &tgID, &resource, &mode, &link.InviteLink,
		&link.InviteLinkHash, &link.TelegramName, &link.Nonce, &status,
		&createsJoinRequest, &expiresAt, &sentAt, &usedAt, &revokedAt,
		&attemptedBy, &link.LastError, &createdAt, &updatedAt)
	if err != nil {
		return domain.InviteLink{}, err
	}

	link.TGID = int64Ptr(tgID)
	link.Resource = domain.Resource(resource)
	link.Mode = domain.InviteMode(mode)
	link.Status = domain.InviteStatus(status)
	link.CreatesJoinRequest = createsJoinRequest == 1
	link.AttemptedBy = int64Ptr(attemptedBy)

	if link.ExpiresAt, err = parseNullTime(expiresAt); err != nil {
		return domain.InviteLink{}, fmt.Errorf("parse invite expires_at: %w", err)
	}

	if link.SentAt, err = parseNullTime(sentAt); err != nil {
		return domain.InviteLink{}, fmt.Errorf("parse invite sent_at: %w", err)
	}

	if link.UsedAt, err = parseNullTime(usedAt); err != nil {
		return domain.InviteLink{}, fmt.Errorf("parse invite used_at: %w", err)
	}

	if link.RevokedAt, err = parseNullTime(revokedAt); err != nil {
		return domain.InviteLink{}, fmt.Errorf("parse invite revoked_at: %w", err)
	}

	if link.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.InviteLink{}, fmt.Errorf("parse invite created_at: %w", err)
	}

	if link.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.InviteLink{}, fmt.Errorf("parse invite updated_at: %w", err)
	}

	return link, nil
}
