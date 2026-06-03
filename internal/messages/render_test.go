package messages

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
)

func TestEscapeKeepsQuotesAndEscapesTelegramHTML(t *testing.T) {
	input := `<tg_id|@username> & "quotes" 'single' name_test.a-b [x]`
	want := `&lt;tg_id|@username&gt; &amp; "quotes" 'single' name_test.a-b [x]`

	if got := Escape(input); got != want {
		t.Fatalf("Escape() = %q, want %q", got, want)
	}

	if got := Code(input); got != "<code>"+want+"</code>" {
		t.Fatalf("Code() = %q, want escaped code span", got)
	}

	if got := Pre(input); got != "<pre>"+want+"</pre>" {
		t.Fatalf("Pre() = %q, want escaped pre block", got)
	}
}

func TestSafeLinkValidatesURLAndEscapesHrefAndLabel(t *testing.T) {
	got := SafeLink(`https://example.com/a?x=1&y=<bad>"`, `<label&>`)
	want := `<a href="https://example.com/a?x=1&amp;y=&lt;bad&gt;&quot;">` +
		`&lt;label&amp;&gt;</a>`
	if got != want {
		t.Fatalf("SafeLink() = %q, want %q", got, want)
	}

	for _, rawURL := range []string{
		"",
		"javascript:alert(1)",
		"https:///missing-host",
		"https://example.com/a\nb",
	} {
		if got := SafeLink(rawURL, `<plain&>`); got != `&lt;plain&amp;&gt;` {
			t.Fatalf("SafeLink(%q) = %q, want escaped plain label", rawURL, got)
		}
	}
}

func TestRenderedTemplatesUseAllowedTelegramHTMLTags(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	samples := []string{
		Welcome(),
		ActiveShared([]InviteLinkLine{{
			Resource: domain.ResourceChat,
			URL:      "https://t.me/+club?x=1&y=2",
		}}),
		Help(),
		Status(domain.AccessDecision{Status: domain.StatusActive}, nil, nil, nil, false),
		AdminHelp(),
		AdminCommandUsage("whois"),
		Whois(WhoisData{
			User: domain.User{
				TGID:         42,
				Username:     `name<&`,
				FirstName:    `First <`,
				LastName:     `Last &`,
				DMState:      domain.DMOpen,
				BannedReason: `<reason&>`,
			},
			Decision: domain.AccessDecision{
				Status: domain.StatusUnknown,
				Reasons: []domain.AccessReason{{
					Source:  domain.PlatformBoosty,
					Verdict: domain.VerdictUnknown,
					Detail:  `<detail&>`,
				}},
			},
			Audit: []AuditLine{{
				Kind:      `<audit>`,
				Source:    `system`,
				Detail:    `<detail>`,
				CreatedAt: now,
			}},
		}),
		OperatorAlert("warning", "kind", `<title&>`, `<detail&>`),
		UnknownChat(-1001, "supergroup", `<chat&>`),
	}

	tagRE := regexp.MustCompile(`<(/?)([a-zA-Z0-9_]+)(?:\s[^>]*)?>`)
	allowed := map[string]bool{"a": true, "b": true, "i": true, "code": true, "pre": true}
	for _, sample := range samples {
		for _, match := range tagRE.FindAllStringSubmatch(sample, -1) {
			if !allowed[strings.ToLower(match[2])] {
				t.Fatalf("template emitted unsupported tag %q in %q", match[0], sample)
			}
		}
	}
}
