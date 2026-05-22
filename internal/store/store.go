// Package store owns the SQLite database: opening it with the right
// pragmas, applying migrations and the concrete repository types built
// on hand-written SQL. The database/sql primitives stay concrete inside
// this package; consumers depend on narrow interfaces of their own.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNotFound is returned by repository getters when no row matches.
var ErrNotFound = errors.New("store: record not found")

// Open opens the SQLite database at dbPath, creating its parent
// directory (mode 0700) when missing, and configures the connection
// pool. Migrations are applied separately via Migrate.
func Open(ctx context.Context, dbPath string) (*sql.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
	}

	// Pre-create the database file with 0600 so DB contents are not
	// world-readable; sql.Open would otherwise create it 0644.
	f, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close database file: %w", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return nil, fmt.Errorf("set database file permissions: %w", err)
	}

	// DSN per SPEC §20.1: WAL, busy_timeout, foreign keys on, NORMAL
	// synchronous, and BEGIN IMMEDIATE transactions.
	dsn := "file:" + dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// A single connection serialises writes and removes "database is
	// locked" errors; the bounded lifetime lets the connection reopen
	// so WAL checkpoints can run.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

// rfc3339 formats a time as RFC3339 in UTC — the on-disk representation
// for every timestamp column.
func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// nullTime formats an optional timestamp for storage: nil becomes a SQL
// NULL, a value becomes an RFC3339 UTC string.
func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return rfc3339(*t)
}

// parseTime parses an RFC3339 timestamp stored in the database.
func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

// nullString stores an empty string as a SQL NULL and any non-empty
// string as itself.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
