## Context

This is the very first runnable build. There is no prior system to
migrate from, no users in production, no contract to preserve. The
only existing artifact is the planning document now copied to
`docs/gatekeeper-product-spec.md` (v1.2) and its phase decomposition under
`local/tasks/init_claude/`. The Phase 01 task file at
`local/archive/tasks/init_claude/phase-01-foundation.md` is the
direct source for the technical decisions captured here.

Phase 01 is everything that has to be in place before any Telegram or
domain logic can be written: a buildable binary, validated config, a
real SQLite database with the v1 schema, and a clean process lifecycle.

## Goals / Non-Goals

**Goals:**

- A single static binary (`CGO_ENABLED=0`) per process — easy to ship.
- Fail loudly and early on bad configuration; never start with a
  half-valid config.
- The serving binary must not perform DDL. Schema state is an
  operator concern owned by a separate `migrate` CLI.
- File-system writes are private to the operator (`0700` dir,
  `0600` db file) by default — the secrets in the database
  (Telegram bot token by way of WAL, OAuth-like data) must not
  leak via filesystem perms.
- The hot path through `database/sql` is concrete and SQL-only. No
  ORM, no query builder.

**Non-Goals:**

- No Telegram transport in Phase 01.
- No HTTP server; the webhook listener and metrics arrive in Phase 07.
- No background subsystems (poller, enforcer, reconciler) yet.
- No down-migrations. Schema evolves forward-only.

## Decisions

### Decision: SQLite via `modernc.org/sqlite` (no cgo)

The pure-Go driver lets `go build` produce a static binary without a
C toolchain. The trade-off is a measurable performance hit versus
`mattn/go-sqlite3`, but our write rate is bot-traffic-paced (well
under one write/sec at steady state), so the gap is invisible. The
deploy simplicity (a single file copy or `scp`, no glibc version
worries) outweighs the cost.

### Decision: Migrations run as a separate `cmd/migrate` binary

Earlier the migration runner lived inside the serving binary
(`internal/store/migrate.go`, commit before `b2844db`). It was
refactored out so:

- The bot binary never carries the goose package in its image.
- An operator can hold a "bot does not start until schema is good"
  invariant without giving the bot the right to mutate DDL.
- The same DSN/config logic is reused by both binaries via the
  `config` and `applog` packages.

`store.CheckSchema` is the boundary check: it reads
`goose_db_version` directly with hand-written SQL, so the bot
binary does not link the goose runtime.

### Decision: One big initial migration, not many small files

`0001_init.sql` contains every table for the whole product. The
trade-off is that the schema's "history" starts as a single artifact
(no commit-by-commit migration trail). For a greenfield project this
is fine — the trail starts from the next migration. Splitting the
initial DDL across many small files would add ceremony without any
real benefit on day zero.

### Decision: Pool sized at one connection

`SetMaxOpenConns(1)` serialises every write through a single
connection. With WAL on, readers do not block writers at the SQLite
level, but a single Go-level connection removes a whole class of
"database is locked" issues we would otherwise have to retry. The
bounded `ConnMaxLifetime(1h)` lets the connection close periodically
so WAL checkpoints can run cleanly.

This is a soft bottleneck (one-writer Telegram bot), not a hard one
— the design assumption is that we never need more than one writer.
If we do, this decision needs revisiting before scaling up.

### Decision: Config errors are accumulated, not short-circuited

`config.Load` runs every check and returns a single error that lists
every problem. An operator with a half-filled `.env` sees the whole
diff in one shot rather than fixing one error at a time. The
implementation uses a tiny `loader` struct that appends to a
`[]string` of errors.

### Decision: `domain` is types-only, no interfaces

The domain package has no behaviour, no interfaces, no imports
beyond `time`. Repositories return domain types but live in
`internal/store`. Dependency direction is always toward `domain`.
This keeps the type graph acyclic and avoids the common pitfall of
domain interfaces drifting away from the data they describe.

### Decision: Five capabilities for this baseline

The Phase 01 code splits into five OpenSpec capabilities so future
deltas have meaningful seams:

- `config` — process-level configuration (loaded once at startup).
- `access-domain` — shared value types for users, subscription
  sources, access decisions, grants, invites and revocations. This is
  separate so dependency-direction rules are explicit.
- `storage` — SQL connection lifecycle, schema-presence check,
  hand-written repositories. Owns the only places that touch
  `*sql.DB`.
- `migrations` — DDL ownership; deliberately separated from
  `storage` so future schema changes don't pollute the serving
  binary's spec.
- `runtime` — the orchestration that ties config + storage + the
  shutdown signal together. Later phases (poller, enforcer,
  reconciler) extend this capability rather than reorganising it.

The alternative — one big `foundation` capability — would have made
phase-02 deltas point at a 600-line spec. Splitting now is cheaper
than splitting later.

## Risks / Trade-offs

- **Single-writer assumption.** If access patterns ever change so
  that two goroutines need to write concurrently, `SetMaxOpenConns(1)`
  must be revisited. Until then it is a feature, not a bug.
- **Two binaries to deploy.** The split between `gatekeeper` and
  `migrate` doubles the artefact count. Acceptable cost for the
  cleaner DDL ownership; both share the same image in practice.
- **Retroactive spec capture.** This change is paper that follows
  code, not the other way round. The risk is that some shipped
  behaviour is not reflected in the spec. Mitigation: every
  scenario here is grounded in an existing test or call site
  (`store_test.go`, `config_test.go`, `cmd/gatekeeper/main.go`,
  `cmd/migrate/main.go`).
