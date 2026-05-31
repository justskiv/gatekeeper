package applog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/justskiv/gatekeeper/internal/config"
)

func TestConsoleHandlerFormatsHumanLine(t *testing.T) {
	var buf bytes.Buffer

	logger := newWithWriter(config.Config{
		LogFormat: "text",
		LogLevel:  "debug",
	}, &buf, true)

	logger.Info("getMe ok",
		slog.Int64("bot_id", 123),
		slog.String("username", "test bot"))

	got := buf.String()
	if !strings.Contains(got, "\x1b[32mINFO ") {
		t.Fatalf("console log %q does not contain colored info level", got)
	}

	plain := stripANSI(got)
	for _, want := range []string{"getMe ok", "bot_id=123", `username="test bot"`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("console log %q does not contain %q", plain, want)
		}
	}
}

func TestJSONFormatStillEmitsJSON(t *testing.T) {
	var buf bytes.Buffer

	logger := newWithWriter(config.Config{
		LogFormat: "json",
		LogLevel:  "info",
	}, &buf, false)

	logger.Info("started", slog.String("mode", "polling"))

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("decode json log %q: %v", buf.String(), err)
	}

	if record["msg"] != "started" || record["mode"] != "polling" {
		t.Fatalf("record = %#v, want msg and mode", record)
	}
}

func stripANSI(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(s, "")
}
