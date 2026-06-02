// Package redact removes secrets and personal data from provider payloads
// before those payloads are stored or logged.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

var (
	emailPattern = regexp.MustCompile(
		`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	botTokenPattern = regexp.MustCompile(`\b\d{5,}:[A-Za-z0-9_-]{20,}\b`)
)

// JSONPayload redacts a JSON payload while preserving non-sensitive
// forensic fields. Invalid JSON is treated as text and still redacted.
func JSONPayload(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte(`{}`)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var value any
	if err := dec.Decode(&value); err != nil {
		return []byte(String(string(raw)))
	}

	value = scrub(value, "")

	out, err := json.Marshal(value)
	if err != nil {
		return []byte(String(string(raw)))
	}

	return out
}

// String masks sensitive substrings in free-form diagnostic text.
func String(s string) string {
	s = emailPattern.ReplaceAllString(s, "[redacted_email]")
	s = botTokenPattern.ReplaceAllString(s, "[redacted_token]")
	s = redactInviteURL(s)

	return s
}

func scrub(value any, key string) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = scrub(item, k)
		}

		return out
	case []any:
		for i := range v {
			v[i] = scrub(v[i], key)
		}

		return v
	case string:
		return scrubString(key, v)
	default:
		return value
	}
}

func scrubString(key, value string) string {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch {
	case normalized == "email":
		return "[redacted_email]"
	case normalized == "web_app_link":
		return "[redacted_web_app_link]"
	case strings.Contains(normalized, "secret"),
		strings.Contains(normalized, "token"),
		strings.Contains(normalized, "api_key"),
		strings.Contains(normalized, "apikey"),
		strings.Contains(normalized, "provider_key"),
		strings.Contains(normalized, "password"):
		return "[redacted_secret]"
	case strings.Contains(normalized, "address"),
		strings.Contains(normalized, "tracking"),
		strings.Contains(normalized, "phone"):
		return "[redacted]"
	case normalized == "invite_link" ||
		(normalized == "url" && looksLikeInviteURL(value)):
		return "[redacted_invite_link]"
	}

	return String(value)
}

func redactInviteURL(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		if looksLikeInviteURL(s) {
			return "[redacted_invite_link]"
		}

		return s
	}

	for i, field := range fields {
		if looksLikeInviteURL(field) {
			fields[i] = "[redacted_invite_link]"
		}
	}

	return strings.Join(fields, " ")
}

func looksLikeInviteURL(s string) bool {
	s = strings.TrimSpace(s)

	return strings.Contains(s, "t.me/+") ||
		strings.Contains(s, "t.me/joinchat/") ||
		strings.Contains(s, "telegram.me/joinchat/")
}
