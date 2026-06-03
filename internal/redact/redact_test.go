package redact

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		assert.NotContains(t, redacted, forbidden, "redacted payload leaks %q", forbidden)
	}

	for _, preserved := range []string{
		"telegram_user_id",
		"subscription_id",
		"123",
		"456",
	} {
		assert.Contains(t, redacted, preserved, "redacted payload drops %q", preserved)
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
		assert.NotContains(t, got, forbidden, "redacted string leaks %q", forbidden)
	}
}
