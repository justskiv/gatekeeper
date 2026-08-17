package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// AccessActionInput describes an action to enqueue in access_actions.
type AccessActionInput struct {
	Type     domain.ActionType
	TGID     *int64
	Resource *domain.Resource

	// AlertID links the row to the operational alert it reports on. Set it on
	// every operator notification about an alert: it is the only handle
	// resolve-time cancellation has on the delivery.
	AlertID        *int64
	IdempotencyKey string
	PayloadJSON    []byte
	RunAfter       time.Time
	MaxAttempts    int
}

// ErrLeaseLost reports that a terminal transition was attempted by a worker
// that no longer owns the action: the lease expired and another worker
// reclaimed the row. The caller MUST NOT retry the transition — the current
// owner is responsible for the outcome.
var ErrLeaseLost = errors.New("outbox lease lost")

// Outbox is the repository for durable Telegram actions.
type Outbox struct {
	db DBTX
}

// NewOutbox returns an Outbox repository backed by db or tx.
func NewOutbox(db DBTX) *Outbox {
	return &Outbox{db: db}
}

// Enqueue idempotently stores one queued action.
func (r *Outbox) Enqueue(
	ctx context.Context,
	input AccessActionInput,
) (domain.AccessAction, bool, error) {
	if input.Type == "" {
		return domain.AccessAction{}, false, errors.New("action type is required")
	}

	if input.IdempotencyKey == "" {
		return domain.AccessAction{}, false, errors.New("idempotency key is required")
	}

	payload := input.PayloadJSON
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}

	runAfter := input.RunAfter
	if runAfter.IsZero() {
		runAfter = time.Now()
	}

	maxAttempts := input.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 8
	}

	now := rfc3339(time.Now())

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO access_actions (
			action_type, tg_id, resource, alert_id, idempotency_key,
			payload_json, status, run_after, attempts, max_attempts,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, 0, ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		string(input.Type), sqlNullInt64(input.TGID), nullableResource(input.Resource),
		sqlNullInt64(input.AlertID), input.IdempotencyKey, string(payload),
		rfc3339(runAfter), maxAttempts, now, now)
	if err != nil {
		return domain.AccessAction{}, false,
			fmt.Errorf("enqueue action %s: %w", input.IdempotencyKey, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return domain.AccessAction{}, false,
			fmt.Errorf("read enqueue rows affected: %w", err)
	}

	action, err := r.GetByIdempotencyKey(ctx, input.IdempotencyKey)
	if err != nil {
		return domain.AccessAction{}, false, err
	}

	return action, affected == 1, nil
}

// GetByID returns an action by primary key.
func (r *Outbox) GetByID(
	ctx context.Context,
	id int64,
) (domain.AccessAction, error) {
	action, err := scanAction(r.db.QueryRowContext(ctx, `
		SELECT id, action_type, tg_id, resource, alert_id, idempotency_key,
		       payload_json, status, run_after, attempts, max_attempts,
		       locked_until, last_error, created_at, updated_at
		FROM access_actions
		WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccessAction{}, ErrNotFound
	}

	if err != nil {
		return domain.AccessAction{}, fmt.Errorf("get action %d: %w", id, err)
	}

	return action, nil
}

// GetByIdempotencyKey returns an action by its unique idempotency key.
func (r *Outbox) GetByIdempotencyKey(
	ctx context.Context,
	key string,
) (domain.AccessAction, error) {
	action, err := scanAction(r.db.QueryRowContext(ctx, `
		SELECT id, action_type, tg_id, resource, alert_id, idempotency_key,
		       payload_json, status, run_after, attempts, max_attempts,
		       locked_until, last_error, created_at, updated_at
		FROM access_actions
		WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccessAction{}, ErrNotFound
	}

	if err != nil {
		return domain.AccessAction{}, fmt.Errorf("get action %q: %w", key, err)
	}

	return action, nil
}

// LeaseReady atomically leases one ready or expired running action.
func (r *Outbox) LeaseReady(
	ctx context.Context,
	now time.Time,
	leaseFor time.Duration,
) (domain.AccessAction, bool, error) {
	if leaseFor <= 0 {
		return domain.AccessAction{}, false, errors.New("lease duration is required")
	}

	action, err := scanAction(r.db.QueryRowContext(ctx, `
		UPDATE access_actions
		SET status = 'running',
		    locked_until = ?,
		    updated_at = ?
		WHERE id = (
			SELECT id
			FROM access_actions
			WHERE (status = 'queued' AND run_after <= ?)
			   OR (status = 'running' AND locked_until IS NOT NULL
			       AND locked_until < ?)
			ORDER BY run_after, id
			LIMIT 1
		)
		RETURNING id, action_type, tg_id, resource, alert_id, idempotency_key,
		          payload_json, status, run_after, attempts, max_attempts,
		          locked_until, last_error, created_at, updated_at`,
		rfc3339(now.Add(leaseFor)), rfc3339(time.Now()), rfc3339(now),
		rfc3339(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccessAction{}, false, nil
	}

	if err != nil {
		return domain.AccessAction{}, false, fmt.Errorf("lease ready action: %w", err)
	}

	return action, true, nil
}

// MarkDone marks a running action as successfully completed. leaseUntil is the
// fencing token returned by LeaseReady; a stale one yields ErrLeaseLost.
func (r *Outbox) MarkDone(
	ctx context.Context,
	id int64,
	leaseUntil time.Time,
) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE access_actions
		SET status = 'done',
		    locked_until = NULL,
		    last_error = '',
		    updated_at = ?
		WHERE id = ? AND status = 'running' AND locked_until = ?`,
		rfc3339(time.Now()), id, rfc3339(leaseUntil))
	if err != nil {
		return fmt.Errorf("mark action %d done: %w", id, err)
	}

	return requireLease(res, id)
}

// ReleaseLease returns an in-flight action to the queue without recording a
// failed attempt. Used when the process stops mid-execution: the next process
// picks the action up immediately instead of waiting out locked_until.
func (r *Outbox) ReleaseLease(
	ctx context.Context,
	id int64,
	leaseUntil time.Time,
) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE access_actions
		SET status = 'queued',
		    locked_until = NULL,
		    updated_at = ?
		WHERE id = ? AND status = 'running' AND locked_until = ?`,
		rfc3339(time.Now()), id, rfc3339(leaseUntil))
	if err != nil {
		return fmt.Errorf("release action %d lease: %w", id, err)
	}

	return requireLease(res, id)
}

// Retry returns an action to the queue and records retry metadata. leaseUntil
// is the fencing token returned by LeaseReady; a stale one yields ErrLeaseLost.
func (r *Outbox) Retry(
	ctx context.Context,
	id int64,
	leaseUntil time.Time,
	runAfter time.Time,
	lastError string,
) (domain.AccessAction, error) {
	action, err := scanAction(r.db.QueryRowContext(ctx, `
		UPDATE access_actions
		SET status = 'queued',
		    attempts = attempts + 1,
		    run_after = ?,
		    locked_until = NULL,
		    last_error = ?,
		    updated_at = ?
		WHERE id = ? AND status = 'running' AND locked_until = ?
		RETURNING id, action_type, tg_id, resource, alert_id, idempotency_key,
		          payload_json, status, run_after, attempts, max_attempts,
		          locked_until, last_error, created_at, updated_at`,
		rfc3339(runAfter), lastError, rfc3339(time.Now()), id,
		rfc3339(leaseUntil)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccessAction{}, ErrLeaseLost
	}

	if err != nil {
		return domain.AccessAction{}, fmt.Errorf("retry action %d: %w", id, err)
	}

	return action, nil
}

// MarkDead moves an action to dead and records the final error. leaseUntil is
// the fencing token returned by LeaseReady; a stale one yields ErrLeaseLost.
func (r *Outbox) MarkDead(
	ctx context.Context,
	id int64,
	leaseUntil time.Time,
	lastError string,
) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE access_actions
		SET status = 'dead',
		    attempts = attempts + 1,
		    locked_until = NULL,
		    last_error = ?,
		    updated_at = ?
		WHERE id = ? AND status = 'running' AND locked_until = ?`,
		lastError, rfc3339(time.Now()), id, rfc3339(leaseUntil))
	if err != nil {
		return fmt.Errorf("mark action %d dead: %w", id, err)
	}

	return requireLease(res, id)
}

// MarkCancelled retires a leased action that must not be executed after all,
// recording why in last_error. It is not a failure: nothing was attempted and
// nothing is owed a retry. leaseUntil is the fencing token returned by
// LeaseReady; a stale one yields ErrLeaseLost.
func (r *Outbox) MarkCancelled(
	ctx context.Context,
	id int64,
	leaseUntil time.Time,
	reason string,
) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE access_actions
		SET status = 'cancelled',
		    locked_until = NULL,
		    last_error = ?,
		    updated_at = ?
		WHERE id = ? AND status = 'running' AND locked_until = ?`,
		reason, rfc3339(time.Now()), id, rfc3339(leaseUntil))
	if err != nil {
		return fmt.Errorf("mark action %d cancelled: %w", id, err)
	}

	return requireLease(res, id)
}

// CancelQueuedForAlert retires the deliveries still waiting in the queue for
// alertID and reports how many rows it retired.
//
// Only `queued` rows are touched. A `running` row is mid-flight and belongs to
// the worker that leased it; cancelling it here would be a second writer racing
// that worker for the outcome, which is exactly what lease fencing exists to
// prevent. The worker's own pre-execute check covers that narrow window
// instead, so cancellation is best-effort by construction: an action leased
// microseconds before this statement commits can still be delivered.
func (r *Outbox) CancelQueuedForAlert(
	ctx context.Context,
	alertID int64,
	reason string,
) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE access_actions
		SET status = 'cancelled',
		    locked_until = NULL,
		    last_error = ?,
		    updated_at = ?
		WHERE alert_id = ? AND status = 'queued'`,
		reason, rfc3339(time.Now()), alertID)
	if err != nil {
		return 0, fmt.Errorf(
			"cancel queued deliveries for alert %d: %w", alertID, err)
	}

	return res.RowsAffected()
}

type actionScanner interface {
	Scan(dest ...any) error
}

func scanAction(scanner actionScanner) (domain.AccessAction, error) {
	var (
		a                    domain.AccessAction
		actionType, status   string
		tgID                 sql.NullInt64
		resource             sql.NullString
		alertID              sql.NullInt64
		payload              string
		runAfter             string
		lockedUntil          sql.NullString
		createdAt, updatedAt string
	)

	err := scanner.Scan(&a.ID, &actionType, &tgID, &resource, &alertID,
		&a.IdempotencyKey, &payload, &status, &runAfter, &a.Attempts,
		&a.MaxAttempts, &lockedUntil, &a.LastError, &createdAt, &updatedAt)
	if err != nil {
		return domain.AccessAction{}, err
	}

	a.Type = domain.ActionType(actionType)
	a.TGID = int64Ptr(tgID)
	a.AlertID = int64Ptr(alertID)

	if resource.Valid {
		v := domain.Resource(resource.String)
		a.Resource = &v
	}

	a.PayloadJSON = []byte(payload)
	a.Status = domain.ActionStatus(status)

	if a.RunAfter, err = parseTime(runAfter); err != nil {
		return domain.AccessAction{}, fmt.Errorf("parse action run_after: %w", err)
	}

	if a.LockedUntil, err = parseNullTime(lockedUntil); err != nil {
		return domain.AccessAction{}, fmt.Errorf("parse action locked_until: %w", err)
	}

	if a.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.AccessAction{}, fmt.Errorf("parse action created_at: %w", err)
	}

	if a.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.AccessAction{}, fmt.Errorf("parse action updated_at: %w", err)
	}

	return a, nil
}

func sqlNullInt64(v *int64) any {
	if v == nil {
		return nil
	}

	return *v
}

func nullableResource(v *domain.Resource) any {
	if v == nil {
		return nil
	}

	return string(*v)
}

// requireLease turns a no-op terminal transition into ErrLeaseLost: the row is
// either gone or already reclaimed by another worker.
func requireLease(res sql.Result, id int64) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("read action %d rows affected: %w", id, err)
	}

	if affected == 0 {
		return ErrLeaseLost
	}

	return nil
}

func requireAffected(res sql.Result, label string, id int64) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("read %s %d rows affected: %w", label, id, err)
	}

	if affected == 0 {
		return ErrNotFound
	}

	return nil
}
