// Package notify sends best-effort direct messages for early runtime
// phases before the durable outbox exists.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

type messageSender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// Notifier sends direct messages while respecting users.dm_state.
type Notifier struct {
	users  *store.Users
	sender messageSender
	logger *slog.Logger
}

// New returns a best-effort notifier.
func New(users *store.Users, sender messageSender, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}

	return &Notifier{users: users, sender: sender, logger: logger}
}

// SendDM sends a direct message unless the user is known as blocked.
func (n *Notifier) SendDM(ctx context.Context, tgID int64, text string) error {
	user, err := n.users.Get(ctx, tgID)
	switch {
	case err == nil && user.DMState == domain.DMBlocked:
		n.logger.Debug("skip blocked dm", slog.Int64("tg_id", tgID))

		return nil
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
	default:
		return fmt.Errorf("load dm state for %d: %w", tgID, err)
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
