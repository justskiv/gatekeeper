# migrations Specification

## Purpose

Defines the dedicated migration CLI and forward-only schema ownership
rules for applying Gatekeeper database changes.

## Requirements
### Requirement: Schema changes are owned by a separate CLI

Schema migrations MUST be applied by a dedicated `migrate` binary
(`cmd/migrate`), and MUST NOT be applied by the serving
`gatekeeper` binary. The two binaries MUST share the `config` and
`applog` packages so the migrate CLI runs against the same DSN and
logs in the same shape as the bot.

#### Scenario: Bot binary on an unmigrated database
- **WHEN** `gatekeeper` starts against a database with no applied
  migrations
- **THEN** it fails fast with `ErrUnmigrated` and does **not** apply
  any DDL itself

#### Scenario: Both binaries share configuration
- **WHEN** the `migrate` CLI runs in the same environment as the bot
- **THEN** it resolves `DB_PATH` and every other configuration value
  from the same source (env + `.env`), with the same validation rules

### Requirement: Migrations are forward-only via goose

Migrations MUST live as `.sql` files in `./migrations/`
(configurable via `--migrations-dir`). They MUST be applied by
`goose v3` with dialect `sqlite3`. Each file MUST use goose
annotations (`-- +goose Up`, etc.) and MUST be recorded in
`goose_db_version` after a successful apply.

#### Scenario: Initial migration on an empty database
- **WHEN** `migrate up` runs against an empty database
- **THEN** `0001_init.sql` is applied
- **AND** `goose_db_version` records the version with `is_applied=1`

#### Scenario: Re-running `migrate up`
- **WHEN** `migrate up` runs against a fully migrated database
- **THEN** no migration is applied
- **AND** the log emits `"migrations: already up to date"`

### Requirement: `migrate` provides `up` and `status` subcommands

The CLI MUST accept exactly one positional subcommand: `up` or
`status`. Anything else MUST print usage to stderr and MUST exit
with code `2`.

- `up` MUST apply every pending migration in order and MUST log
  each one as it is applied.
- `status` MUST log the current `max(version_id)` and the
  per-migration state from goose's view of the filesystem.

#### Scenario: Unknown subcommand
- **WHEN** `migrate` is invoked without a subcommand or with an unknown
  one
- **THEN** the process writes `"usage: migrate [--migrations-dir DIR] <up|status>"`
  to stderr and exits with code `2`

#### Scenario: `migrate status` on a freshly migrated database
- **WHEN** `migrate status` runs after `migrate up`
- **THEN** the log shows the maximum applied version and one entry per
  migration file with its state

### Requirement: Failures exit non-zero with a `fatal:` prefix

Any unrecoverable error from `migrate` MUST be printed to stderr in
the form `"fatal: <error>"` and the process MUST exit with code
`1`. This MUST match the shape `gatekeeper` uses so an operator
can pipe both binaries the same way.

#### Scenario: Migration fails to apply
- **WHEN** a migration file is malformed or its statements fail
- **THEN** the CLI prints `"fatal: apply migrations: <reason>"` to
  stderr and exits with code `1`
