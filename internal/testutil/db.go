// Package testutil holds shared helpers for the test suite: currently a
// single migrate-and-open database fixture (NewDB). It is imported only
// from _test.go files, so the goose dependency it pulls in never reaches
// the runtime binary. It is carved out of the strict internal depguard
// rule for the same reason the domain package is: its allowed imports
// differ (goose and testify, neither of which belongs in production code).
package testutil

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/store"
)

// NewDB opens a fresh on-disk database in a temp directory and applies
// every migration from the repo's top-level migrations/ directory. The
// database is closed automatically when the test finishes.
//
// The store package keeps its own copy of this helper: store's tests
// live in package store, and importing testutil (which imports store)
// from them would form an import cycle. Every other package uses NewDB.
func NewDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := store.Open(context.Background(),
		filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err, "open database")

	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(migrationsDir(t)))
	require.NoError(t, err, "new goose provider")

	_, err = provider.Up(context.Background())
	require.NoError(t, err, "apply migrations")

	return db
}

// migrationsDir resolves the repo's top-level migrations/ directory
// relative to this file, so the path holds regardless of the `go test`
// invocation directory.
func migrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed; cannot resolve migrations dir")

	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}
