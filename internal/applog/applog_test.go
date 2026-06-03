package applog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.Contains(t, got, "\x1b[32mINFO ", "console log must color the info level")

	plain := stripANSI(got)
	for _, want := range []string{"getMe ok", "bot_id=123", `username="test bot"`} {
		assert.Contains(t, plain, want)
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
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record),
		"decode json log %q", buf.String())

	assert.Equal(t, "started", record["msg"])
	assert.Equal(t, "polling", record["mode"])
}

func stripANSI(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(s, "")
}
