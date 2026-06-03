package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTelegramProcessor struct {
	calls int
	raw   string
}

func (p *fakeTelegramProcessor) HandleWebhookUpdate(
	_ context.Context,
	raw []byte,
) error {
	p.calls++
	p.raw = string(raw)

	return nil
}

func TestTelegramWebhookSecretGatesProcessing(t *testing.T) {
	processor := &fakeTelegramProcessor{}
	handler := NewTelegramHandler("secret", processor)

	bad := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost, "/webhooks/telegram",
		strings.NewReader(`{"update_id":1}`))
	badResp := httptest.NewRecorder()
	handler.ServeHTTP(badResp, bad)

	assert.Equal(t, http.StatusUnauthorized, badResp.Code, "bad status")
	assert.Equal(t, 0, processor.calls, "processor must not run without the secret")

	good := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost, "/webhooks/telegram",
		strings.NewReader(`{"update_id":1}`))
	good.Header.Set(telegramSecretHeader, "secret")

	goodResp := httptest.NewRecorder()
	handler.ServeHTTP(goodResp, good)

	require.Equalf(t, http.StatusOK, goodResp.Code, "good body=%s",
		goodResp.Body.String())
	assert.Equal(t, 1, processor.calls, "processor must run once")
	assert.Equal(t, `{"update_id":1}`, processor.raw, "raw update must be forwarded")
}
