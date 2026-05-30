## Why

This is the retroactive baseline change for Phase 01 — the foundation
that every later capability stands on. The code was already written and
merged (commits `f471a73`..`b2844db`) before the project adopted
OpenSpec; this change captures that shipped state as the initial spec
so future deltas have a real baseline to modify.

Without a runnable scaffold — a static binary, a validated config, a
real SQLite database with the full schema in place and a clean startup
that fails loudly on misconfiguration — no Telegram, no observability,
no access logic can be built. Phase 01 puts that scaffold in place.

## What Changes

Everything below already exists in the repository; this change records
it as the contract.

- A Go 1.26.0 module at `github.com/justskiv/gatekeeper`, built static
  (`CGO_ENABLED=0`) via Taskfile targets.
- A types-only `domain` package that defines shared value types for
  users, subscription sources, access decisions, grants, invites and
  revocations without importing infrastructure packages.
- A `config` package that loads from the environment (and a `.env`
  file in development), parses every value with a type, and reports
  all validation errors at once.
- A `store` package that opens SQLite with the required pragmas
  (`WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`,
  `_txlock=immediate`), creates the data directory `0700` and the
  database file `0600`, and refuses to serve a non-migrated database.
- A migrations directory at `migrations/` with the initial schema
  (`0001_init.sql`) carrying all 12 domain/ops tables.
- A separate `migrate` CLI (`cmd/migrate`) that owns schema changes
  via goose. The gatekeeper binary never applies DDL.
- A minimal `cmd/gatekeeper` entry point that loads config, opens the
  database, verifies the schema, waits for `SIGINT`/`SIGTERM` and
  shuts down cleanly.
- A shared `applog` package that configures `log/slog` for every
  binary from the same config keys.

## Capabilities

### New Capabilities

- `config`: typed application configuration loaded from the
  environment, with cross-field validation and aggregated error
  reporting.
- `access-domain`: shared value types for Gatekeeper's business model,
  with dependency direction always pointing toward `internal/domain`.
- `storage`: SQLite database lifecycle — opening with the required
  pragmas, enforcing file/dir permissions, schema-presence check,
  shared time/null helpers, and the hand-written repository types
  over the v1 schema.
- `migrations`: a dedicated CLI tool (`cmd/migrate up|status`) that
  owns forward-only schema changes via goose. The serving binary
  never applies DDL.
- `runtime`: process lifecycle of the `gatekeeper` binary — load
  config → init logger → open database → verify schema → wait for
  shutdown signal → close database. Background subsystems (poller,
  enforcer, reconciler) will be added by later phases under this
  same orchestration.

### Modified Capabilities

None. This is the initial baseline.

## Impact

- New files under `cmd/{gatekeeper,migrate}`, `internal/{config,
  applog,domain,store}`, `migrations/`, plus `Taskfile.yml`,
  `.env.example`, `go.mod`, `go.sum`, `README.md`.
- No deployed system to migrate — Phase 01 is the first shipping
  artifact.
- All later phases (`bot-online`, `status-core`, `outbox-enforcer`,
  `grant-access`, `revocation`, `webhook-ops`) build on top of these
  five capabilities.
