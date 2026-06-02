//nolint:wsl_v5 // HTTP tests group arrange/assert blocks tightly.
package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	if badResp.Code != http.StatusUnauthorized {
		t.Fatalf("bad status = %d, want 401", badResp.Code)
	}

	if processor.calls != 0 {
		t.Fatalf("processor calls = %d, want 0", processor.calls)
	}

	good := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost, "/webhooks/telegram",
		strings.NewReader(`{"update_id":1}`))
	good.Header.Set(telegramSecretHeader, "secret")
	goodResp := httptest.NewRecorder()
	handler.ServeHTTP(goodResp, good)

	if goodResp.Code != http.StatusOK {
		t.Fatalf("good status = %d body=%s, want 200",
			goodResp.Code, goodResp.Body.String())
	}

	if processor.calls != 1 || processor.raw != `{"update_id":1}` {
		t.Fatalf("processor = (%d, %q), want one raw call",
			processor.calls, processor.raw)
	}
}
