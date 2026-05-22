package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every pending migration embedded in the migrations
// directory. Each migration runs in a single transaction together with
// its schema_migrations bookkeeping row, so a partially applied
// migration can never be recorded. Migrations are forward-only and a
// repeated run is a no-op.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT    NOT NULL,
			applied_at TEXT    NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}

	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)

	for _, file := range files {
		version, name, err := parseMigrationName(file)
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		body, err := migrationsFS.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", file, err)
		}
		if err := applyMigration(db, version, name, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", file, err)
		}
	}
	return nil
}

// appliedVersions returns the set of migration versions already applied.
func appliedVersions(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan migration version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

// parseMigrationName turns "migrations/0001_init.sql" into (1, "init").
func parseMigrationName(file string) (int, string, error) {
	base := strings.TrimSuffix(path.Base(file), ".sql")
	prefix, name, ok := strings.Cut(base, "_")
	if !ok {
		return 0, "", fmt.Errorf("malformed migration filename %q", file)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, "", fmt.Errorf("malformed migration version in %q: %w", file, err)
	}
	return version, name, nil
}

// applyMigration runs one migration body and records it, atomically.
func applyMigration(db *sql.DB, version int, name, body string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once the tx is committed

	if _, err := tx.Exec(body); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
		version, name, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return err
	}
	return tx.Commit()
}
