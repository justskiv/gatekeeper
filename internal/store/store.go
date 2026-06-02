// Package store owns the SQLite database: opening it with the right
// pragmas, verifying that the schema is in place and the concrete
// repository types built on hand-written SQL. Migrations themselves
// live outside the application binary (see cmd/migrate). The
// database/sql primitives stay concrete inside this package;
// consumers depend on narrow interfaces of their own.
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

// DBTX is the narrow SQL executor shared by *sql.DB and *sql.Tx.
// Repository constructors accept it so handlers can bind repositories to
// the transaction that owns the update's terminal state transition.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type txStarter interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// ErrNotFound is returned by repository getters when no row matches.
var ErrNotFound = errors.New("store: record not found")

// ErrUnmigrated is returned by CheckSchema when the database has no
// applied migrations. The wrapped message instructs the operator to
// run the migrate CLI.
var ErrUnmigrated = errors.New(
	"database is not migrated; run `task migrate:up`")

// Open opens the SQLite database at dbPath, creating its parent
// directory (mode 0700) when missing, and configures the connection
// pool. Migrations are applied separately by the migrate CLI (see
// cmd/migrate); call CheckSchema to verify that schema is in place
// before serving traffic.
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

// CheckSchema returns ErrUnmigrated if the database has no applied
// migrations. The application never creates schema on its own; this
// is the boundary check between "operator forgot to run migrate" and
// "schema is good enough to serve". It speaks plain SQL on purpose so
// the goose package does not get linked into the runtime binary.
func CheckSchema(ctx context.Context, db *sql.DB) error {
	var present int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'goose_db_version'`,
	).Scan(&present); err != nil {
		return fmt.Errorf("check schema: %w", err)
	}

	if present == 0 {
		return ErrUnmigrated
	}

	// Goose seeds the table with version_id=0 / is_applied=1 when it
	// creates it; a real applied migration carries version_id >= 1.
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `
		SELECT max(version_id) FROM goose_db_version
		WHERE is_applied = 1`,
	).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if !version.Valid || version.Int64 < 1 {
		return ErrUnmigrated
	}

	return nil
}

// rfc3339 formats a time as RFC3339 in UTC — the on-disk representation
// for timestamp columns that are compared lexicographically in SQLite.
func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// rfc3339Nano formats event timestamps whose sub-second ordering matters.
func rfc3339Nano(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// nullTime formats an optional timestamp for storage: nil becomes a SQL
// NULL, a value becomes an RFC3339 UTC string.
func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}

	return rfc3339(*t)
}

// nullEventTime formats an optional provider event timestamp. These values are
// only read back for in-process ordering, so preserving nanoseconds is safe.
func nullEventTime(t *time.Time) any {
	if t == nil {
		return nil
	}

	return rfc3339Nano(*t)
}

// parseTime parses an RFC3339 timestamp stored in the database.
func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// parseNullTime parses an optional RFC3339 timestamp from a nullable
// SQL string. NULL or empty becomes nil.
func parseNullTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}

	t, err := parseTime(v.String)
	if err != nil {
		return nil, err
	}

	return &t, nil
}

// nullString stores an empty string as a SQL NULL and any non-empty
// string as itself.
func nullString(s string) any {
	if s == "" {
		return nil
	}

	return s
}
