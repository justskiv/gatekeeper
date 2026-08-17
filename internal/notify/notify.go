// Package notify sends best-effort direct messages for early runtime
// phases before the durable outbox exists.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

type messageSender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

type formattedMessageSender interface {
	SendFormattedMessage(
		ctx context.Context,
		chatID int64,
		text string,
		parseMode string,
	) error
}

type outboxEnqueuer interface {
	Enqueue(
		ctx context.Context,
		input store.AccessActionInput,
	) (domain.AccessAction, bool, error)
}

// Notifier sends direct messages while respecting users.dm_state.
type Notifier struct {
	users  *store.Users
	sender messageSender
	outbox outboxEnqueuer
	logger *slog.Logger
}

// New returns a best-effort notifier.
func New(users *store.Users, sender messageSender, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}

	return &Notifier{users: users, sender: sender, logger: logger}
}

// NewDurable returns a notifier that enqueues send_dm actions.
func NewDurable(users *store.Users, outbox outboxEnqueuer, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}

	return &Notifier{users: users, outbox: outbox, logger: logger}
}

// dmTarget is one outgoing direct message and everything that decides how it
// is stored. It travels as a struct because the durable path needs the marker
// and the alert link alongside the text, and a positional call with that many
// knobs stops being readable at the call site.
type dmTarget struct {
	tgID      int64
	text      string
	parseMode string
	plain     bool

	// marker participates in the idempotency key. An empty marker means the
	// caller has no natural dedupe identity for this message.
	marker string

	// alertID links the enqueued row to the alert it reports on, so resolving
	// that alert cancels the delivery. Nil for messages unrelated to an alert.
	alertID *int64
}

// SendDM sends a direct message unless the user is known as blocked.
func (n *Notifier) SendDM(ctx context.Context, tgID int64, text string) error {
	return n.SendDurableDM(ctx, tgID, text, "")
}

// SendFormattedDM sends a formatted direct message unless the user is known as
// blocked.
func (n *Notifier) SendFormattedDM(ctx context.Context, tgID int64, text string) error {
	return n.SendFormattedDurableDM(ctx, tgID, text, messages.ParseModeHTML, "")
}

// SendDurableDM enqueues a direct message when the notifier was
// constructed with NewDurable; marker participates in idempotency.
func (n *Notifier) SendDurableDM(
	ctx context.Context,
	tgID int64,
	text string,
	marker string,
) error {
	return n.sendDM(ctx, dmTarget{
		tgID:   tgID,
		text:   text,
		plain:  true,
		marker: marker,
	})
}

// SendFormattedDurableDM enqueues or sends a formatted direct message.
func (n *Notifier) SendFormattedDurableDM(
	ctx context.Context,
	tgID int64,
	text string,
	parseMode string,
	marker string,
) error {
	return n.sendDM(ctx, dmTarget{
		tgID:      tgID,
		text:      text,
		parseMode: parseMode,
		marker:    marker,
	})
}

func (n *Notifier) sendDM(ctx context.Context, dm dmTarget) error {
	tgID, text, parseMode := dm.tgID, dm.text, dm.parseMode

	user, err := n.users.Get(ctx, tgID)
	switch {
	case err == nil && user.DMState == domain.DMBlocked:
		n.logger.Debug("skip blocked dm", slog.Int64("tg_id", tgID))

		return nil
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		if n.outbox != nil {
			if err := n.users.Upsert(ctx, domain.User{TGID: tgID}); err != nil {
				return fmt.Errorf("ensure dm user %d: %w", tgID, err)
			}
		}
	default:
		return fmt.Errorf("load dm state for %d: %w", tgID, err)
	}

	if n.outbox != nil {
		return n.enqueueDM(ctx, dm)
	}

	if n.sender == nil {
		return nil
	}

	if parseMode != "" {
		if sender, ok := n.sender.(formattedMessageSender); ok {
			err = sender.SendFormattedMessage(ctx, tgID, text, parseMode)
		} else {
			err = n.sender.SendMessage(ctx, tgID, text)
		}
	} else {
		err = n.sender.SendMessage(ctx, tgID, text)
	}

	if err == nil {
		return nil
	}

	if isDMBlocked(err) {
		if setErr := n.users.SetDMState(ctx, tgID, domain.DMBlocked); setErr != nil {
			if errors.Is(setErr, store.ErrNotFound) {
				setErr = n.users.Upsert(ctx, domain.User{
					TGID:    tgID,
					DMState: domain.DMBlocked,
				})
			}

			if setErr != nil {
				return fmt.Errorf("mark dm blocked for %d: %w", tgID, setErr)
			}
		}

		n.logger.Info("dm blocked by user", slog.Int64("tg_id", tgID))

		return nil
	}

	return fmt.Errorf("send dm to %d: %w", tgID, err)
}

func (n *Notifier) enqueueDM(ctx context.Context, dm dmTarget) error {
	tgID := dm.tgID

	payload, err := json.Marshal(struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
		Plain     bool   `json:"plain,omitempty"`
	}{Text: dm.text, ParseMode: dm.parseMode, Plain: dm.plain})
	if err != nil {
		return fmt.Errorf("encode dm payload for %d: %w", tgID, err)
	}

	_, _, err = n.outbox.Enqueue(ctx, store.AccessActionInput{
		Type:    domain.ActionSendDM,
		TGID:    &tgID,
		AlertID: dm.alertID,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionSendDM, &tgID, nil, dmMarker(dm)),
		PayloadJSON: payload,
	})
	if err != nil {
		return fmt.Errorf("enqueue dm to %d: %w", tgID, err)
	}

	return nil
}

// dmMarker picks the idempotency marker for an enqueued message.
//
// An alert-linked message keys on the alert, which also dedupes it: a health
// check that keeps failing raises the same alert and must not queue a second
// copy of the same DM. The timestamped `manual:` fallback is the opposite — a
// deliberately unique marker for callers with no dedupe identity of their own,
// where suppressing a repeat would silently drop a distinct message.
func dmMarker(dm dmTarget) string {
	switch {
	case dm.marker != "":
		return dm.marker
	case dm.alertID != nil:
		return fmt.Sprintf("admin_alert:%d:%d", *dm.alertID, dm.tgID)
	default:
		return fmt.Sprintf("manual:%d:%d", dm.tgID, time.Now().UnixNano())
	}
}

// SendOwners sends the same direct message to every owner.
func (n *Notifier) SendOwners(
	ctx context.Context, ownerIDs []int64, text string,
) error {
	return n.sendOwners(ctx, ownerIDs, dmTarget{text: text, plain: true})
}

// SendFormattedOwners sends the same formatted direct message to every owner.
func (n *Notifier) SendFormattedOwners(
	ctx context.Context, ownerIDs []int64, text string,
) error {
	return n.sendOwners(ctx, ownerIDs, dmTarget{
		text:      text,
		parseMode: messages.ParseModeHTML,
	})
}

// SendFormattedAlertOwners notifies every owner about alertID and links each
// enqueued row to it, so resolving the alert cancels whatever has not gone out
// yet. On the non-durable notifier (startup, no outbox) the message is sent
// immediately and there is no row to link or cancel — that path keeps its
// current behaviour.
func (n *Notifier) SendFormattedAlertOwners(
	ctx context.Context,
	ownerIDs []int64,
	alertID int64,
	text string,
) error {
	return n.sendOwners(ctx, ownerIDs, dmTarget{
		text:      text,
		parseMode: messages.ParseModeHTML,
		alertID:   &alertID,
	})
}

// SendAlertDM enqueues one direct message linked to alertID, for a caller that
// already knows the single recipient. Same contract as
// SendFormattedAlertOwners: the link retires the delivery when the alert
// resolves, and it is also the message's dedupe identity. An empty parseMode
// sends plain text.
func (n *Notifier) SendAlertDM(
	ctx context.Context,
	tgID int64,
	alertID int64,
	text string,
	parseMode string,
) error {
	return n.sendDM(ctx, dmTarget{
		tgID:      tgID,
		text:      text,
		parseMode: parseMode,
		plain:     parseMode == "",
		alertID:   &alertID,
	})
}

func (n *Notifier) sendOwners(
	ctx context.Context,
	ownerIDs []int64,
	dm dmTarget,
) error {
	var firstErr error

	for _, ownerID := range ownerIDs {
		dm.tgID = ownerID

		err := n.sendDM(ctx, dm)
		if err != nil {
			n.logger.Warn("failed to send owner dm",
				slog.Int64("owner_tg_id", ownerID),
				slog.Any("error", err))

			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

type categorizedTelegramError interface {
	TelegramCategory() string
}

func isDMBlocked(err error) bool {
	var categorized categorizedTelegramError

	return errors.As(err, &categorized) &&
		categorized.TelegramCategory() == "dm_blocked"
}
