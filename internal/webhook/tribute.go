//nolint:wsl_v5 // Webhook handler branches mirror provider outcomes.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/operatorlog"
	"github.com/justskiv/gatekeeper/internal/redact"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	tributeSignatureHeader = "Trbt-Signature"

	tributeEventNew       = "new_subscription"
	tributeEventRenewed   = "renewed_subscription"
	tributeEventCancelled = "cancelled_subscription"
)

// TributeHandler accepts Tribute webhooks.
type TributeHandler struct {
	DB              *sql.DB
	APIKey          string
	Engine          *engine.Engine
	CancelImmediate bool
	OwnerIDs        []int64
	AdminLogChatID  *int64
	OperatorLog     *operatorlog.Writer
	Logger          *slog.Logger
}

// ServeHTTP validates and processes one Tribute webhook.
func (h *TributeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	if !validTributeSignature(h.APIKey, raw, r.Header.Get(tributeSignatureHeader)) {
		if err := h.recordInvalidSignature(r.Context(), raw); err != nil {
			h.logger().Warn("failed to record invalid tribute webhook",
				slog.Any("error", err))
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return
	}

	envelope, err := parseTributeEnvelope(raw)
	if err != nil {
		h.recordFailedRequest(r.Context(), raw, "unknown", "", nil, err)
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	dedupKey := tributeDedupKey(envelope, raw)
	subscriptionID := envelope.payloadString("subscription_id")
	tgID, _ := envelope.payloadInt64("telegram_user_id")
	var tgIDPtr *int64
	if tgID > 0 {
		tgIDPtr = &tgID
	}

	event, inserted, err := h.insertReceived(
		r.Context(), dedupKey, envelope.Name, subscriptionID, tgIDPtr, raw)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if !inserted &&
		(event.Status == store.TributeEventProcessed ||
			event.Status == store.TributeEventIgnored) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

		return
	}

	switch envelope.Name {
	case tributeEventNew, tributeEventRenewed, tributeEventCancelled:
	default:
		if err := store.NewTributeEvents(h.DB).MarkTerminal(
			r.Context(), event.ID, store.TributeEventIgnored, "",
		); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

		return
	}

	subEvent, err := h.subscriptionEvent(envelope)
	if err != nil {
		_ = store.NewTributeEvents(h.DB).MarkTerminal(
			r.Context(), event.ID, store.TributeEventFailed,
			redact.String(err.Error()),
		)
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	if err := h.processSubscriptionEvent(r.Context(), event.ID, subEvent); err != nil {
		_ = store.NewTributeEvents(h.DB).MarkTerminal(
			r.Context(), event.ID, store.TributeEventFailed,
			redact.String(err.Error()),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *TributeHandler) recordInvalidSignature(ctx context.Context, raw []byte) error {
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	events := store.NewTributeEvents(tx)
	event, inserted, err := events.InsertReceived(ctx, store.TributeEventInput{
		DedupKey:       rawDedupKey(raw),
		EventName:      "unknown",
		SignatureValid: false,
		PayloadJSON:    redact.JSONPayload(raw),
	})
	if err != nil {
		return err
	}

	if !inserted {
		return tx.Commit()
	}

	if err := events.MarkTerminal(
		ctx, event.ID, store.TributeEventFailed, "invalid signature",
	); err != nil {
		return err
	}

	if err := store.NewAudit(tx).Append(ctx, store.AuditEntry{
		Kind:   "webhook_rejected",
		Source: string(domain.PlatformTribute),
		Actor:  "provider",
		Detail: "invalid signature",
	}); err != nil {
		return err
	}

	return tx.Commit()
}

func (h *TributeHandler) recordFailedRequest(
	ctx context.Context,
	raw []byte,
	eventName string,
	subscriptionID string,
	tgID *int64,
	cause error,
) {
	if h.DB == nil {
		return
	}

	event, inserted, err := h.insertReceived(
		ctx, rawDedupKey(raw), eventName, subscriptionID, tgID, raw)
	if err != nil {
		h.logger().Warn("failed to insert malformed tribute event",
			slog.Any("error", err))

		return
	}

	if !inserted {
		return
	}

	if err := store.NewTributeEvents(h.DB).MarkTerminal(
		ctx, event.ID, store.TributeEventFailed,
		redact.String(cause.Error()),
	); err != nil {
		h.logger().Warn("failed to mark malformed tribute event",
			slog.Any("error", err))
	}
}

func (h *TributeHandler) insertReceived(
	ctx context.Context,
	dedupKey string,
	eventName string,
	subscriptionID string,
	tgID *int64,
	raw []byte,
) (store.TributeEvent, bool, error) {
	return store.NewTributeEvents(h.DB).InsertReceived(ctx, store.TributeEventInput{
		DedupKey:       dedupKey,
		EventName:      eventName,
		TGID:           tgID,
		SubscriptionID: subscriptionID,
		SignatureValid: true,
		PayloadJSON:    redact.JSONPayload(raw),
	})
}

func (h *TributeHandler) processSubscriptionEvent(
	ctx context.Context,
	eventID int64,
	subEvent domain.SubscriptionEvent,
) error {
	if h.Engine == nil {
		return errors.New("tribute status engine is unavailable")
	}

	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tribute event tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	outbox := store.NewOutbox(tx)
	alerts := store.NewAlertsWithDelivery(
		tx, outbox, h.OwnerIDs, h.AdminLogChatID)
	effects, err := h.Engine.HandleEvent(ctx, engine.Store{
		Users:         store.NewUsers(tx),
		Subscriptions: store.NewSubscriptions(tx),
		Audit:         store.NewAudit(tx),
		Revocations:   store.NewRevocations(tx),
		Whitelist:     store.NewWhitelist(tx),
		Grants:        store.NewGrants(tx),
		Outbox:        outbox,
		Alerts:        alerts,
		OperatorLog:   h.OperatorLog,
	}, subEvent)
	if err != nil {
		return err
	}

	notifier := notify.NewDurable(store.NewUsers(tx), outbox, h.logger())
	for i, effect := range effects {
		marker := fmt.Sprintf("tribute_event:%d:%d", eventID, i)
		if err := notifier.SendDurableDM(
			ctx, effect.TGID, effect.Text, marker,
		); err != nil {
			return err
		}
	}

	if err := store.NewTributeEvents(tx).MarkTerminal(
		ctx, eventID, store.TributeEventProcessed, "",
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tribute event tx: %w", err)
	}

	return nil
}

func (h *TributeHandler) subscriptionEvent(
	envelope tributeEnvelope,
) (domain.SubscriptionEvent, error) {
	tgID, ok := envelope.payloadInt64("telegram_user_id")
	if !ok || tgID <= 0 {
		return domain.SubscriptionEvent{}, errors.New("telegram_user_id is required")
	}

	eventAt, err := parseTributeTime(envelope.CreatedAt)
	if err != nil {
		return domain.SubscriptionEvent{}, fmt.Errorf("parse created_at: %w", err)
	}

	expiresAt, err := envelope.payloadTime("expires_at")
	if err != nil {
		return domain.SubscriptionEvent{}, fmt.Errorf("parse expires_at: %w", err)
	}

	kind := domain.EventActivated
	if envelope.Name == tributeEventCancelled {
		kind = domain.EventCancelledSubscription
		if h.CancelImmediate {
			kind = domain.EventDeactivated
		}
	}

	return domain.SubscriptionEvent{
		Platform:      domain.PlatformTribute,
		Kind:          kind,
		TGUserID:      tgID,
		TGUsername:    envelope.payloadString("telegram_username"),
		Tier:          envelope.payloadString("subscription_name"),
		ExpiresAt:     expiresAt,
		ExternalID:    envelope.payloadString("subscription_id"),
		PeriodID:      envelope.payloadString("period_id"),
		EventAt:       eventAt,
		ProviderEvent: envelope.Name,
		OccurredAt:    eventAt,
	}, nil
}

func (h *TributeHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}

	return h.Logger
}

type tributeEnvelope struct {
	Name      string         `json:"name"`
	CreatedAt string         `json:"created_at"`
	SentAt    string         `json:"sent_at"`
	Payload   map[string]any `json:"payload"`
}

func parseTributeEnvelope(raw []byte) (tributeEnvelope, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()

	var envelope tributeEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return tributeEnvelope{}, fmt.Errorf("decode tribute webhook: %w", err)
	}

	if envelope.Name == "" {
		return tributeEnvelope{}, errors.New("name is required")
	}

	if envelope.CreatedAt == "" {
		return tributeEnvelope{}, errors.New("created_at is required")
	}

	if envelope.Payload == nil {
		return tributeEnvelope{}, errors.New("payload is required")
	}

	return envelope, nil
}

func (e tributeEnvelope) payloadString(key string) string {
	value, ok := e.Payload[key]
	if !ok || value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func (e tributeEnvelope) payloadInt64(key string) (int64, bool) {
	value := e.payloadString(key)
	if value == "" {
		return 0, false
	}

	out, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}

	return out, true
}

func (e tributeEnvelope) payloadTime(key string) (*time.Time, error) {
	value := e.payloadString(key)
	if value == "" {
		return nil, nil
	}

	t, err := parseTributeTime(value)
	if err != nil {
		return nil, err
	}

	return &t, nil
}

func parseTributeTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func validTributeSignature(apiKey string, raw []byte, got string) bool {
	if apiKey == "" || got == "" {
		return false
	}

	gotBytes, err := hex.DecodeString(strings.TrimSpace(got))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write(raw)
	expected := mac.Sum(nil)

	return subtle.ConstantTimeCompare(gotBytes, expected) == 1
}

func tributeDedupKey(envelope tributeEnvelope, raw []byte) string {
	subscriptionID := envelope.payloadString("subscription_id")
	periodID := envelope.payloadString("period_id")
	if envelope.Name == "" ||
		subscriptionID == "" ||
		periodID == "" ||
		envelope.CreatedAt == "" {
		return rawDedupKey(raw)
	}

	return sha256Hex([]byte(strings.Join([]string{
		envelope.Name,
		subscriptionID,
		periodID,
		envelope.CreatedAt,
	}, "|")))
}

func rawDedupKey(raw []byte) string {
	return sha256Hex(raw)
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])
}
