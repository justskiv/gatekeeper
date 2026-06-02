package messages

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
