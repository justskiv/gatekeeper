package messages

import (
	"net/url"
	"strings"
	"unicode"
)

const (
	// ParseModeHTML is the only formatted Telegram parse mode used by the
	// centralized renderer.
	ParseModeHTML = "HTML"
)

var (
	bodyEscaper = strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)

	attrEscaper = strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
)

// Escape renders dynamic text as visible Telegram HTML text.
func Escape(text string) string {
	return bodyEscaper.Replace(text)
}

// Code renders dynamic text as an inline code span.
func Code(text string) string {
	return "<code>" + Escape(text) + "</code>"
}

// Pre renders dynamic text as a preformatted diagnostics block.
func Pre(text string) string {
	return "<pre>" + Escape(text) + "</pre>"
}

// Italic renders text that is already controlled template copy.
func Italic(text string) string {
	return "<i>" + text + "</i>"
}

// Blockquote groups already-rendered lines into a Telegram quote block, which
// draws an accent bar and indent — the only "card" container HTML mode offers.
func Blockquote(inner string) string {
	return "<blockquote>" + inner + "</blockquote>"
}

// CustomEmoji renders a Telegram custom emoji with a plain-emoji fallback.
// The fallback is shown wherever the custom emoji cannot be displayed (for
// example when the sender loses Premium); id is a controlled numeric constant.
func CustomEmoji(id, fallback string) string {
	return `<tg-emoji emoji-id="` + escapeAttr(id) + `">` + fallback + "</tg-emoji>"
}

// SafeLink renders a clickable link only for URLs safe for Telegram HTML.
func SafeLink(rawURL, label string) string {
	escapedLabel := Escape(label)
	if escapedLabel == "" {
		escapedLabel = Escape(rawURL)
	}

	if !safeURL(rawURL) {
		return escapedLabel
	}

	return `<a href="` + escapeAttr(rawURL) + `">` + escapedLabel + "</a>"
}

func escapeAttr(text string) string {
	return attrEscaper.Replace(text)
}

func safeURL(rawURL string) bool {
	if rawURL == "" || hasControl(rawURL) {
		return false
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	switch parsed.Scheme {
	case "http", "https":
		return parsed.Host != ""
	case "tg":
		return parsed.Host != "" || parsed.Opaque != ""
	default:
		return false
	}
}

func hasControl(text string) bool {
	for _, r := range text {
		if unicode.IsControl(r) {
			return true
		}
	}

	return false
}
