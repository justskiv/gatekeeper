package messages

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestUserFacingRussianTextLivesInMessagesPackage(t *testing.T) {
	repoRoot := filepath.Clean("../..")
	cyrillic := regexp.MustCompile(`[А-Яа-яЁё]`)

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "docs", "local", "openspec":
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") ||
			strings.Contains(filepath.ToSlash(path), "/internal/messages/") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if cyrillic.Match(data) {
			t.Errorf("%s contains Cyrillic text outside messages package", path)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
}

func TestProductionSendPathsDoNotUseLegacyMarkdown(t *testing.T) {
	repoRoot := filepath.Clean("../..")

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "docs", "local", "openspec":
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		text := string(data)
		if strings.Contains(text, "ParseModeMarkdownV1") ||
			strings.Contains(text, `"Markdown"`) {
			t.Errorf("%s uses legacy Markdown parse mode", path)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
}

func TestOpsAlertsKeepsEmojiOnAlertTitleLines(t *testing.T) {
	text := OpsAlerts([]OpsAlertData{
		{
			ID:        1,
			Severity:  "warning",
			Kind:      "dead_action",
			Title:     "delivery failed",
			CreatedAt: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
		},
		{
			ID:        2,
			Severity:  "info",
			Kind:      "sync",
			Title:     "routine notice",
			CreatedAt: time.Date(2026, 6, 3, 13, 0, 0, 0, time.UTC),
		},
	})

	markerLines := 0
	for _, line := range strings.Split(text, "\n") {
		hasMarker := strings.Contains(line, "⚠️") || strings.Contains(line, "ℹ️")
		if !hasMarker {
			continue
		}

		markerLines++
		if strings.HasPrefix(line, "• ") {
			t.Fatalf("data row has emoji marker: %q\nfull text:\n%s", line, text)
		}
	}

	if markerLines != 2 {
		t.Fatalf("marker lines = %d, want one marker per alert title\n%s",
			markerLines, text)
	}
}
