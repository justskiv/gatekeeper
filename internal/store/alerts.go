package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
)

const (
	alertStatusOpen = "open"

	// alertCancelReason lands in access_actions.last_error on a delivery that
	// was retired at resolve time. It is the only free-text column on the row,
	// and an operator reading a cancelled action wants to know why.
	alertCancelReason = "alert resolved before delivery"
)

// AlertSeverities returns every severity `admin_alerts.severity` accepts, from
// least to most urgent.
//
// The list is owned here because the constraint is: `0001_init.sql` pins the
// column to exactly these four values. Consumers that must enumerate the whole
// set — the metrics endpoint zero-fills one open-alert series per severity, so
// that "no critical alerts" reads as `0` instead of as a missing series — take
// it from here rather than repeating the literals.
func AlertSeverities() []string {
	return []string{"info", "warning", "error", "critical"}
}

// AlertInput describes a new operational alert for the administrator.
// A nil TGID is stored as a SQL NULL.
type AlertInput struct {
	Severity  string // info|warning|error|critical
	Kind      string
	Title     string
	Detail    string
	TGID      *int64
	DedupeKey string

	// OwnerNotifiedByCaller marks an alert whose owner notification the caller
	// sends itself, with copy written for that specific failure. A
	// delivery-enabled repository then skips its own generic owner DM, so one
	// failure costs the owner one message instead of two saying the same thing
	// in different words.
	//
	// It suppresses the owner DM only. When an admin log chat is configured the
	// generic delivery is not addressed to the owner at all — it is a line in
	// an operator feed — and it still goes out: dropping it would trade a
	// duplicate for a hole in the log.
	//
	// Every other alert kind is unaffected: with the flag unset the repository
	// remains the sole producer, which is what the paths with no message of
	// their own rely on.
	OwnerNotifiedByCaller bool
}

// Alerts is the repository for the admin_alerts table.
type Alerts struct {
	db       DBTX
	delivery alertDelivery
}

type alertDelivery struct {
	outbox         *Outbox
	ownerIDs       []int64
	adminLogChatID *int64
}

// NewAlerts returns an Alerts repository backed by db or tx.
func NewAlerts(db DBTX) *Alerts {
	return &Alerts{db: db}
}

// NewAlertsWithDelivery returns an Alerts repository that also enqueues
// durable operator notification actions in the same transaction.
func NewAlertsWithDelivery(
	db DBTX,
	outbox *Outbox,
	ownerIDs []int64,
	adminLogChatID *int64,
) *Alerts {
	return &Alerts{
		db: db,
		delivery: alertDelivery{
			outbox:         outbox,
			ownerIDs:       append([]int64(nil), ownerIDs...),
			adminLogChatID: adminLogChatID,
		},
	}
}

// Create inserts a new open alert and returns its generated id.
func (r *Alerts) Create(ctx context.Context, a AlertInput) (int64, error) {
	if a.DedupeKey != "" {
		id, _, err := r.CreateOpenIfMissing(ctx, a)

		return id, err
	}

	var tgID any
	if a.TGID != nil {
		tgID = *a.TGID
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO admin_alerts (severity, status, kind, title, detail, tg_id, created_at)
		VALUES (?, 'open', ?, ?, ?, ?, ?)`,
		a.Severity, a.Kind, a.Title, alertDetail(a), tgID, rfc3339(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("create alert %q: %w", a.Kind, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read alert insert id: %w", err)
	}

	if err := r.enqueueDelivery(ctx, id, a); err != nil {
		return 0, err
	}

	return id, nil
}

// CreateOpenIfMissing inserts an open alert unless one with the same kind
// and title is already open. The bool reports whether a new row was inserted.
func (r *Alerts) CreateOpenIfMissing(
	ctx context.Context, a AlertInput,
) (int64, bool, error) {
	var (
		existingID int64
		query      = `
		SELECT id
		FROM admin_alerts
		WHERE status = 'open' AND kind = ? AND title = ?
		ORDER BY id
		LIMIT 1`
		args = []any{a.Kind, a.Title}
	)

	if a.DedupeKey != "" {
		query = `
			SELECT id
			FROM admin_alerts
			WHERE status = 'open' AND kind = ? AND detail LIKE ?
			ORDER BY id
			LIMIT 1`
		args = []any{a.Kind, alertDedupePrefix(a.DedupeKey) + "%"}
	}

	err := r.db.QueryRowContext(ctx, query, args...).Scan(&existingID)
	if err == nil {
		return existingID, false, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false,
			fmt.Errorf("find open alert %q/%q: %w", a.Kind, a.Title, err)
	}

	insert := a
	insert.DedupeKey = ""
	insert.Detail = alertDetail(a)

	id, err := r.Create(ctx, insert)
	if err != nil {
		return 0, false, err
	}

	return id, true, nil
}

// ResolveOpen resolves an open alert by kind and stable key or title.
func (r *Alerts) ResolveOpen(ctx context.Context, kind, key string) error {
	return r.ResolveOpenByTitle(ctx, kind, key)
}

// IsOpen reports whether the alert is still open. A missing alert reads as not
// open: a delivery whose alert row is gone has nothing left to describe.
func (r *Alerts) IsOpen(ctx context.Context, id int64) (bool, error) {
	var status string

	err := r.db.QueryRowContext(ctx,
		`SELECT status FROM admin_alerts WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("read alert %d status: %w", id, err)
	}

	return status == alertStatusOpen, nil
}

func alertDetail(a AlertInput) string {
	if a.DedupeKey == "" {
		return a.Detail
	}

	prefix := alertDedupePrefix(a.DedupeKey)
	if strings.HasPrefix(a.Detail, prefix) {
		return a.Detail
	}

	if a.Detail == "" {
		return strings.TrimSpace(prefix)
	}

	return prefix + a.Detail
}

func alertDedupePrefix(key string) string {
	return "dedupe_key=" + key + "\n"
}

func (r *Alerts) enqueueDelivery(
	ctx context.Context,
	alertID int64,
	a AlertInput,
) error {
	if r.delivery.outbox == nil || len(r.delivery.ownerIDs) == 0 {
		return nil
	}

	text := messages.OperatorAlert(a.Severity, a.Kind, a.Title, a.Detail)

	if r.delivery.adminLogChatID != nil {
		return r.enqueueAlertDM(ctx, alertID, r.delivery.ownerIDs[0], text,
			*r.delivery.adminLogChatID)
	}

	// Past this point the delivery is a direct message to the owner, which is
	// exactly what a caller with its own copy has already arranged.
	if a.OwnerNotifiedByCaller {
		return nil
	}

	for _, ownerID := range r.delivery.ownerIDs {
		if err := r.enqueueAlertDM(ctx, alertID, ownerID, text, 0); err != nil {
			return err
		}
	}

	return nil
}

func (r *Alerts) enqueueAlertDM(
	ctx context.Context,
	alertID int64,
	tgID int64,
	text string,
	chatID int64,
) error {
	if err := NewUsers(r.db).EnsureStub(ctx, tgID); err != nil {
		return err
	}

	payload, err := json.Marshal(struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
		ChatID    int64  `json:"chat_id,omitempty"`
	}{Text: text, ParseMode: messages.ParseModeHTML, ChatID: chatID})
	if err != nil {
		return fmt.Errorf("encode alert delivery: %w", err)
	}

	_, _, err = r.delivery.outbox.Enqueue(ctx, AccessActionInput{
		Type:    domain.ActionSendDM,
		TGID:    &tgID,
		AlertID: &alertID,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionSendDM, &tgID, nil,
			fmt.Sprintf("admin_alert:%d:%d", alertID, chatID)),
		PayloadJSON: payload,
	})
	if err != nil {
		return fmt.Errorf("enqueue alert delivery: %w", err)
	}

	return nil
}

// ResolveOpenByTitle resolves matching open alerts and cancels the operator
// deliveries they still have queued. Missing alerts are a no-op.
//
// Resolution and cancellation share one transaction so an alert can never end
// up resolved with its notifications left in the queue. Cancellation is
// best-effort even so: a worker that leased a delivery just before this commits
// still sends it, and closing that window would mean holding the alert row
// across a Telegram call. The enforcer's pre-execute check narrows the window;
// nothing removes it.
func (r *Alerts) ResolveOpenByTitle(ctx context.Context, kind, title string) error {
	return WithTx(ctx, r.db, func(q DBTX) error {
		// The ids must be captured before the status flips: afterwards the
		// same predicate matches nothing and the deliveries lose their handle.
		ids, err := openAlertIDs(ctx, q, kind, title)
		if err != nil {
			return err
		}

		_, err = q.ExecContext(ctx, `
			UPDATE admin_alerts
			SET status = 'resolved', resolved_at = ?
			WHERE status = 'open' AND kind = ? AND title = ?`,
			rfc3339(time.Now()), kind, title)
		if err != nil {
			return fmt.Errorf("resolve alert %q/%q: %w", kind, title, err)
		}

		outbox := NewOutbox(q)
		for _, id := range ids {
			if _, err := outbox.CancelQueuedForAlert(
				ctx, id, alertCancelReason,
			); err != nil {
				return err
			}
		}

		return nil
	})
}

// openAlertIDs lists the open alerts matching kind and title.
func openAlertIDs(
	ctx context.Context,
	q DBTX,
	kind, title string,
) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id
		FROM admin_alerts
		WHERE status = 'open' AND kind = ? AND title = ?
		ORDER BY id`, kind, title)
	if err != nil {
		return nil, fmt.Errorf("find open alerts %q/%q: %w", kind, title, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan open alert id: %w", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open alerts %q/%q: %w", kind, title, err)
	}

	return ids, nil
}
