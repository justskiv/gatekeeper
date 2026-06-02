package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TributeEventStatus is the durable Tribute webhook state machine.
type TributeEventStatus string

const (
	TributeEventReceived  TributeEventStatus = "received"
	TributeEventProcessed TributeEventStatus = "processed"
	TributeEventIgnored   TributeEventStatus = "ignored"
	TributeEventFailed    TributeEventStatus = "failed"
)

// TributeEvent is one row from the Tribute webhook inbox.
type TributeEvent struct {
	ID             int64
	DedupKey       string
	EventName      string
	TGID           *int64
	SubscriptionID string
	SignatureValid bool
	PayloadJSON    []byte
	Status         TributeEventStatus
	Error          string
	ReceivedAt     time.Time
	ProcessedAt    *time.Time
}

// TributeEventInput describes a received Tribute webhook.
type TributeEventInput struct {
	DedupKey       string
	EventName      string
	TGID           *int64
	SubscriptionID string
	SignatureValid bool
	PayloadJSON    []byte
	ReceivedAt     time.Time
}

// TributeEvents is the repository for the tribute_events inbox.
type TributeEvents struct {
	db DBTX
}

// NewTributeEvents returns a repository backed by db or tx.
func NewTributeEvents(db DBTX) *TributeEvents {
	return &TributeEvents{db: db}
}

// InsertReceived inserts a received event. If the dedup key already
// exists, the existing row is returned with inserted=false.
func (r *TributeEvents) InsertReceived(
	ctx context.Context,
	input TributeEventInput,
) (TributeEvent, bool, error) {
	if input.DedupKey == "" {
		return TributeEvent{}, false, errors.New("dedup key is required")
	}

	if input.EventName == "" {
		input.EventName = "unknown"
	}

	payload := input.PayloadJSON
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}

	receivedAt := input.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO tribute_events (
			dedup_key, event_name, tg_id, subscription_id,
			signature_valid, payload_json, status, received_at)
		VALUES (?, ?, ?, ?, ?, ?, 'received', ?)
		ON CONFLICT(dedup_key) DO NOTHING`,
		input.DedupKey, input.EventName, nullableEventInt64(input.TGID),
		input.SubscriptionID, input.SignatureValid, string(payload),
		rfc3339(receivedAt))
	if err != nil {
		return TributeEvent{}, false,
			fmt.Errorf("insert tribute event %q: %w", input.DedupKey, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return TributeEvent{}, false,
			fmt.Errorf("read tribute event rows affected: %w", err)
	}

	event, err := r.GetByDedupKey(ctx, input.DedupKey)
	if err != nil {
		return TributeEvent{}, false, err
	}

	return event, affected == 1, nil
}

// GetByDedupKey returns one event by dedup key.
func (r *TributeEvents) GetByDedupKey(
	ctx context.Context,
	dedupKey string,
) (TributeEvent, error) {
	event, err := scanTributeEvent(r.db.QueryRowContext(ctx, `
		SELECT id, dedup_key, event_name, tg_id, subscription_id,
		       signature_valid, payload_json, status, error, received_at,
		       processed_at
		FROM tribute_events
		WHERE dedup_key = ?`, dedupKey))
	if errors.Is(err, sql.ErrNoRows) {
		return TributeEvent{}, ErrNotFound
	}

	if err != nil {
		return TributeEvent{}, fmt.Errorf("get tribute event %q: %w", dedupKey, err)
	}

	return event, nil
}

// MarkTerminal moves an event to a terminal status.
func (r *TributeEvents) MarkTerminal(
	ctx context.Context,
	id int64,
	status TributeEventStatus,
	errorText string,
) error {
	switch status {
	case TributeEventProcessed, TributeEventIgnored, TributeEventFailed:
	default:
		return fmt.Errorf("invalid terminal tribute event status %q", status)
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE tribute_events
		SET status = ?, error = ?, processed_at = ?
		WHERE id = ?`,
		string(status), errorText, rfc3339(time.Now()), id)
	if err != nil {
		return fmt.Errorf("mark tribute event %d %s: %w", id, status, err)
	}

	return requireAffected(res, "tribute_event", id)
}

type tributeEventScanner interface {
	Scan(dest ...any) error
}

func scanTributeEvent(scanner tributeEventScanner) (TributeEvent, error) {
	var (
		e              TributeEvent
		tgID           sql.NullInt64
		signature      bool
		payload        string
		status         string
		receivedAt     string
		processedAt    sql.NullString
		subscriptionID string
	)

	err := scanner.Scan(&e.ID, &e.DedupKey, &e.EventName, &tgID,
		&subscriptionID, &signature, &payload, &status, &e.Error,
		&receivedAt, &processedAt)
	if err != nil {
		return TributeEvent{}, err
	}

	e.TGID = nullableInt64Ptr(tgID)
	e.SubscriptionID = subscriptionID
	e.SignatureValid = signature
	e.PayloadJSON = []byte(payload)
	e.Status = TributeEventStatus(status)

	if e.ReceivedAt, err = parseTime(receivedAt); err != nil {
		return TributeEvent{}, fmt.Errorf("parse tribute event received_at: %w", err)
	}

	if e.ProcessedAt, err = parseNullTime(processedAt); err != nil {
		return TributeEvent{}, fmt.Errorf("parse tribute event processed_at: %w", err)
	}

	return e, nil
}

func nullableEventInt64(v *int64) any {
	if v == nil {
		return nil
	}

	return *v
}

func nullableInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}

	return &v.Int64
}
