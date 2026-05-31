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
	"github.com/justskiv/gatekeeper/internal/store"
)

type messageSender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
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

// SendDM sends a direct message unless the user is known as blocked.
func (n *Notifier) SendDM(ctx context.Context, tgID int64, text string) error {
	return n.SendDurableDM(ctx, tgID, text, "")
}

// SendDurableDM enqueues a direct message when the notifier was
// constructed with NewDurable; marker participates in idempotency.
func (n *Notifier) SendDurableDM(
	ctx context.Context,
	tgID int64,
	text string,
	marker string,
) error {
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
		return n.enqueueDM(ctx, tgID, text, marker)
	}

	if n.sender == nil {
		return nil
	}

	err = n.sender.SendMessage(ctx, tgID, text)
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

func (n *Notifier) enqueueDM(
	ctx context.Context,
	tgID int64,
	text string,
	marker string,
) error {
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return fmt.Errorf("encode dm payload for %d: %w", tgID, err)
	}

	if marker == "" {
		marker = fmt.Sprintf("manual:%d:%d", tgID, time.Now().UnixNano())
	}

	_, _, err = n.outbox.Enqueue(ctx, store.AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: domain.AccessActionKey(domain.ActionSendDM, &tgID, nil, marker),
		PayloadJSON:    payload,
	})
	if err != nil {
		return fmt.Errorf("enqueue dm to %d: %w", tgID, err)
	}

	return nil
}

// SendOwners sends the same direct message to every owner.
func (n *Notifier) SendOwners(
	ctx context.Context, ownerIDs []int64, text string,
) error {
	var firstErr error

	for _, ownerID := range ownerIDs {
		if err := n.SendDM(ctx, ownerID, text); err != nil {
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
