## ADDED Requirements

### Requirement: SQLite database is opened with the required pragmas

The database MUST be opened via `database/sql` using the
`modernc.org/sqlite` driver (registered as `sqlite`). The DSN MUST
set `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`,
`synchronous=NORMAL` and `_txlock=immediate` (every transaction
MUST begin with `BEGIN IMMEDIATE`). The pool MUST be sized at one
connection (`SetMaxOpenConns(1)`) with a one-hour
`ConnMaxLifetime` so WAL checkpoints can run.

#### Scenario: Database opens successfully
- **WHEN** `Open(ctx, dbPath)` is called against a writable path
- **THEN** a `*sql.DB` is returned with `foreign_keys=ON` and the pool
  configured to a single connection
- **AND** a subsequent `PingContext` succeeds

#### Scenario: Database directory is missing
- **WHEN** the parent directory of `dbPath` does not exist
- **THEN** `Open` creates it (and parents) with mode `0700`

### Requirement: Database file is created with mode 0600

The database file MUST NOT be world-readable. `Open` MUST
pre-create the file with mode `0600` before passing the path to the
SQLite driver (otherwise the driver would default to `0644`).

#### Scenario: New database file
- **WHEN** `Open` is called against a path with no existing file
- **THEN** the file is created with permission bits `0600`

### Requirement: Schema-presence check refuses an unmigrated database

The application MUST NOT apply DDL itself; it MUST only verify that
the schema is in place. `CheckSchema` MUST read goose's bookkeeping
table (`goose_db_version`) and MUST return `ErrUnmigrated` if the
table is missing or its `max(version_id) WHERE is_applied = 1` is
`< 1`. The wrapped error message MUST instruct the operator to run
`task migrate:up`.

#### Scenario: Empty database
- **WHEN** `CheckSchema` runs against a freshly opened, unmigrated db
- **THEN** the returned error wraps `ErrUnmigrated`
- **AND** the error message contains the string `"task migrate:up"`

#### Scenario: Migrated database
- **WHEN** `CheckSchema` runs after `migrate up` applied at least one
  migration
- **THEN** the call returns `nil`

### Requirement: Initial schema defines the v1 data model

Migration `0001_init.sql` MUST create twelve tables that the rest
of the system depends on: `users`, `subscriptions`, `access_grants`,
`pending_revocations`, `whitelist`, `invite_links`,
`telegram_updates`, `tribute_events`, `access_actions`,
`audit_log`, `admin_alerts`, `meta`. All timestamp columns MUST
store RFC3339 strings in UTC. Domain enums (platform, state, mode,
status, severity, action_type) MUST be enforced via `CHECK`
constraints.

#### Scenario: Tables exist after migrating
- **WHEN** the test harness opens a fresh database and runs goose `Up`
- **THEN** all twelve tables exist in `sqlite_master`
- **AND** `goose_db_version` records at least one applied migration

### Requirement: At most one active subscription per (user, platform)

The schema MUST reject a second active subscription for the same
`(tg_id, platform)` pair. This is enforced by the partial unique
index `idx_subscriptions_active_unique` on
`subscriptions(tg_id, platform) WHERE status = 'active'`. Expired
subscriptions MUST NOT occupy the slot, and the same user MUST be
allowed active subscriptions on different platforms.

#### Scenario: Duplicate active subscription
- **WHEN** an active subscription exists for `(user, platform)` and a
  second active subscription is inserted for the same pair
- **THEN** the insert fails with a unique-constraint violation

#### Scenario: Active subscription after an expired one
- **WHEN** an active subscription for `(user, platform)` was expired
  (`status='expired'`, `ended_at` set)
- **THEN** a new active subscription for the same pair is allowed

### Requirement: Invite links honour per-mode active uniqueness

Two partial unique indexes on `invite_links` MUST enforce one
active link per slot, where "active" means
`status IN ('created','sent')`:

- `shared_join_request`: at most one active link per
  `(resource, mode)`; the `tg_id` column MUST be `NULL` for shared
  links.
- `personal_join_request` and `direct`: at most one active link per
  `(tg_id, resource, mode)`; `tg_id` MUST be `NOT NULL` for these.

A `CHECK` constraint MUST enforce the `tg_id` nullability rule by
mode.

#### Scenario: Second active shared link rejected
- **WHEN** an active shared link exists for `(chat, shared_join_request)`
  and a second active shared link is inserted for the same pair
- **THEN** the insert fails with a unique-constraint violation

#### Scenario: Personal link slot freed by an expired link
- **WHEN** the prior personal link for `(user, resource, mode)` is
  marked `expired`
- **THEN** a new active personal link for the same triple is allowed

### Requirement: Foreign keys are enforced

`PRAGMA foreign_keys` MUST be `1` on every connection so that
referential integrity is checked at write time.

#### Scenario: Subscription without a user
- **WHEN** a subscription row referencing a non-existent `tg_id` is
  inserted
- **THEN** the insert fails with a foreign-key violation

### Requirement: Repositories expose narrow methods on the v1 schema

Phase 01 MUST ship hand-written repository types for the tables the
next phases need. Each repository MUST wrap `*sql.DB` and MUST be
constructed via a `NewX(db)` helper. The following methods MUST be
available:

- `Users.Upsert(ctx, User) error`, `Users.Get(ctx, tgID) (User, error)`
- `Subscriptions.Create(ctx, Subscription) (id, error)`,
  `Subscriptions.GetActive(ctx, tgID, platform) (Subscription, ok, error)`
- `Grants.Upsert(ctx, AccessGrant) error`,
  `Grants.Get(ctx, tgID, resource) (AccessGrant, error)`
- `Meta.Get(ctx, key) (value, ok, error)`,
  `Meta.Set(ctx, key, value) error`

Additional repositories (`Revocations`, `Whitelist`, `Audit`,
`Alerts`) are constructable in Phase 01 but their full method sets
are added by the phases that need them.

#### Scenario: Getter on a missing row
- **WHEN** a `Get` method is called for a key that has no row
- **THEN** the returned error wraps `ErrNotFound`

#### Scenario: Upsert is idempotent on the key
- **WHEN** `Users.Upsert` is called twice with the same `tg_id`
- **THEN** the second call updates the mutable columns instead of
  failing on the primary key

### Requirement: Timestamps cross the boundary as RFC3339 UTC

The `store` package MUST own the only places where `time.Time` is
encoded to or decoded from a SQL row. Encoding MUST be
`time.RFC3339` in UTC; decoding MUST parse the same shape.
Optional timestamps MUST round-trip through `*time.Time` with
`NULL` ↔ `nil`.

#### Scenario: Round-trip of an optional timestamp
- **WHEN** a row with a non-nil `ExpiresAt` is written and read back
- **THEN** the returned `*time.Time` is non-nil and equal to the
  original (to RFC3339-second precision)

#### Scenario: NULL maps to nil
- **WHEN** a row stores `NULL` in an optional timestamp column
- **THEN** the decoded value is a nil `*time.Time`
