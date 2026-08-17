package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
)

// migration0003 is the version that rebuilds access_actions around alert_id and
// the narrowed status CHECK.
const migration0003 int64 = 3

// TestMigration0003RebuildsOutboxStatusMachine exercises the rebuild in both
// directions against a database that already holds rows. Up must carry a
// pre-existing `failed` row over as `dead` — the narrower CHECK would otherwise
// reject the copy and the migration would fail on exactly the databases that
// have history — and must reject `failed` afterwards. Down must restore the old
// shape, mapping `cancelled` back to `done`.
func TestMigration0003RebuildsOutboxStatusMachine(t *testing.T) {
	ctx := context.Background()
	tgID := random.TGID()
	db, provider := newDBMigratedTo(t, migration0003-1)

	require.NoError(t, NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	now := rfc3339(time.Now())
	_, err := db.ExecContext(ctx, `
		INSERT INTO access_actions (
			action_type, tg_id, idempotency_key, payload_json, status,
			run_after, created_at, updated_at)
		VALUES ('send_dm', ?, 'legacy-failed', '{}', 'failed', ?, ?, ?)`,
		tgID, now, now, now)
	require.NoError(t, err, "seed a legacy failed action")

	_, err = provider.UpTo(ctx, migration0003)
	require.NoError(t, err, "apply 0003")

	var (
		status  string
		alertID sql.NullInt64
	)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status, alert_id FROM access_actions
		WHERE idempotency_key = 'legacy-failed'`).Scan(&status, &alertID),
		"read the migrated row")
	assert.Equal(t, "dead", status, "a legacy failed row migrates to dead")
	assert.False(t, alertID.Valid, "existing rows carry no alert link")

	_, err = db.ExecContext(ctx, `
		INSERT INTO access_actions (
			action_type, tg_id, idempotency_key, payload_json, status,
			run_after, created_at, updated_at)
		VALUES ('send_dm', ?, 'rejected-failed', '{}', 'failed', ?, ?, ?)`,
		tgID, now, now, now)
	require.Error(t, err, "the narrowed CHECK must reject status='failed'")

	_, err = db.ExecContext(ctx, `
		INSERT INTO access_actions (
			action_type, tg_id, idempotency_key, payload_json, status,
			run_after, created_at, updated_at)
		VALUES ('send_dm', ?, 'accepted-cancelled', '{}', 'cancelled', ?, ?, ?)`,
		tgID, now, now, now)
	require.NoError(t, err, "the new CHECK must accept status='cancelled'")

	_, err = provider.Down(ctx)
	require.NoError(t, err, "roll 0003 back")

	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status FROM access_actions
		WHERE idempotency_key = 'accepted-cancelled'`).Scan(&status),
		"read the rolled-back row")
	assert.Equal(t, "done", status, "cancelled maps to done on the way down")

	var linked int

	err = db.QueryRowContext(ctx,
		`SELECT count(alert_id) FROM access_actions`).Scan(&linked)
	require.Error(t, err, "alert_id must be gone after the rollback")
}

// newDBMigratedTo opens a fresh database and applies migrations up to and
// including upTo, returning the provider so the test can drive the remaining
// migrations itself.
func newDBMigratedTo(t *testing.T, upTo int64) (*sql.DB, *goose.Provider) {
	t.Helper()

	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err, "open database")

	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(migrationsDir(t)))
	require.NoError(t, err, "new goose provider")

	_, err = provider.UpTo(context.Background(), upTo)
	require.NoError(t, err, "apply migrations")

	return db, provider
}
