package webhook

import (
	"context"
	"crypto/subtle"
	"io"
	"net/http"
)

const telegramSecretHeader = "X-Telegram-Bot-Api-Secret-Token" // #nosec G101

// TelegramProcessor is the durable Telegram webhook processing surface.
type TelegramProcessor interface {
	HandleWebhookUpdate(ctx context.Context, raw []byte) error
}

// TelegramHandler accepts Telegram webhook updates.
type TelegramHandler struct {
	Secret    string
	Processor TelegramProcessor
}

// NewTelegramHandler returns a Telegram webhook handler.
func NewTelegramHandler(secret string, processor TelegramProcessor) *TelegramHandler {
	return &TelegramHandler{Secret: secret, Processor: processor}
}

// ServeHTTP verifies Telegram's secret token before reading the update.
func (h *TelegramHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Secret == "" || subtle.ConstantTimeCompare(
		[]byte(r.Header.Get(telegramSecretHeader)),
		[]byte(h.Secret),
	) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	if h.Processor == nil {
		http.Error(w, "processor unavailable", http.StatusInternalServerError)

		return
	}

	if err := h.Processor.HandleWebhookUpdate(r.Context(), raw); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
