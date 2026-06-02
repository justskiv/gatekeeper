package redact

import (
	"strings"
	"testing"
)

func TestJSONPayloadRedactsSensitiveFields(t *testing.T) {
	raw := []byte(`{
		"email":"alice@example.com",
		"web_app_link":"https://t.me/example/app?startapp=secret",
		"telegram_user_id":123,
		"subscription_id":456,
		"invite_link":"https://t.me/+abcdef",
		"nested":{"bot_token":"123456:abcdefghijklmnopqrstuvwxyz"}
	}`)

	redacted := string(JSONPayload(raw))

	for _, forbidden := range []string{
		"alice@example.com",
		"startapp=secret",
		"https://t.me/+abcdef",
		"123456:abcdefghijklmnopqrstuvwxyz",
	} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted payload contains %q: %s", forbidden, redacted)
		}
	}

	for _, preserved := range []string{
		"telegram_user_id",
		"subscription_id",
		"123",
		"456",
	} {
		if !strings.Contains(redacted, preserved) {
			t.Fatalf("redacted payload missing %q: %s", preserved, redacted)
		}
	}
}

func TestRedactStringMasksEmailsTokensAndInviteURLs(t *testing.T) {
	got := String(
		"email bob@example.com token 123456:abcdefghijklmnopqrstuvwxyz " +
			"https://t.me/joinchat/abcdef",
	)

	for _, forbidden := range []string{
		"bob@example.com",
		"123456:abcdefghijklmnopqrstuvwxyz",
		"https://t.me/joinchat/abcdef",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("redacted string contains %q: %s", forbidden, got)
		}
	}
}
