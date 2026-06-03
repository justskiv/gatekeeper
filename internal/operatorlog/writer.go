// Package operatorlog emits the owner-facing operator event feed. It turns
// typed domain.OperatorEvent values into durable send_dm actions targeting
// the dedicated EVENT_LOG_CHAT_ID, sharing the caller's transaction so a
// built event is atomic with the domain change that caused it.
package operatorlog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

// Renderer turns a typed operator event into owner-facing HTML. A render
// failure is treated as a feed-build error: the event is skipped, never
// rolling back the domain change.
type Renderer func(domain.OperatorEvent) (string, error)

// Outbox is the durable action surface the writer enqueues into. It is
// satisfied by *store.Outbox and by the engine/admission outbox interfaces,
// so the writer always rides the caller's transaction-bound outbox.
type Outbox interface {
	Enqueue(
		ctx context.Context,
		input store.AccessActionInput,
	) (domain.AccessAction, bool, error)
}

// Writer emits operator events to the dedicated event-log chat.
type Writer struct {
	chatID int64
	render Renderer
	logger *slog.Logger
}

// New returns a writer that targets eventLogChatID. render must be a leaf
// renderer (typically messages.RenderOperatorEvent) so this package never
// imports message templates transitively into a cycle.
func New(eventLogChatID int64, render Renderer, logger *slog.Logger) *Writer {
	if logger == nil {
		logger = slog.Default()
	}

	return &Writer{chatID: eventLogChatID, render: render, logger: logger}
}

// ChatID is the configured event-log chat the writer targets.
func (w *Writer) ChatID() int64 {
	if w == nil {
		return 0
	}

	return w.chatID
}

// Emit renders ev and enqueues a durable send_dm to the event-log chat using
// the caller's transaction-bound outbox.
//
// Two failure classes are handled differently (see the event-log spec):
//   - a render/build failure is logged and the event is skipped; the domain
//     change is NOT rolled back (observability never gates access-control);
//   - a persistence (enqueue) failure propagates so the transaction fails
//     like any other durable write.
//
// A nil writer or outbox is a no-op, so callers can wire the feed optionally.
func (w *Writer) Emit(
	ctx context.Context,
	outbox Outbox,
	ev domain.OperatorEvent,
) error {
	if w == nil || outbox == nil {
		return nil
	}

	text, err := w.render(ev)
	if err != nil {
		w.logger.Error("operator event render failed; skipping event",
			slog.String("kind", string(ev.Kind)),
			slog.Int64("tg_id", ev.TGID),
			slog.Any("error", err))

		return nil
	}

	payload, err := json.Marshal(struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
		ChatID    int64  `json:"chat_id"`
	}{Text: text, ParseMode: messages.ParseModeHTML, ChatID: w.chatID})
	if err != nil {
		w.logger.Error("operator event encode failed; skipping event",
			slog.String("kind", string(ev.Kind)),
			slog.Int64("tg_id", ev.TGID),
			slog.Any("error", err))

		return nil
	}

	tgID := ev.TGID

	_, _, err = outbox.Enqueue(ctx, store.AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: idempotencyKey(ev),
		PayloadJSON:    payload,
	})
	if err != nil {
		return fmt.Errorf("enqueue operator event %s: %w", ev.Kind, err)
	}

	return nil
}

// idempotencyKey binds the event kind, subject and source-stable marker into
// the canonical outbox key so a replayed update resolves to the existing row
// instead of enqueuing a duplicate feed message.
func idempotencyKey(ev domain.OperatorEvent) string {
	tgID := ev.TGID
	marker := "opevent:" + string(ev.Kind) + ":" + ev.Marker

	return domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, marker)
}
