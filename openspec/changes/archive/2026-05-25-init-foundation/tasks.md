<!--
All boxes below are pre-checked: Phase 01 was implemented before
OpenSpec adoption (commits f471a73 → b2844db). This file records the
work as done so archive can merge the spec deltas into
openspec/specs/ and treat the baseline as honestly delivered.
-->

## 1. Project scaffold

- [x] 1.1 Go module `github.com/justskiv/gatekeeper` (Go 1.26.0)
- [x] 1.2 Directory layout (`cmd/{gatekeeper,migrate}`,
  `internal/{applog,config,domain,store}`, `migrations/`)
- [x] 1.3 `Taskfile.yml` with `build`, `build:migrate`, `run`,
  `migrate:up`, `migrate:status`, `test`, `lint`, `tidy`
- [x] 1.4 `.env.example`, `.gitignore`, `README.md`

## 2. `config` capability

- [x] 2.1 `Config` struct covering every variable from SPEC §18.1
- [x] 2.2 Loader that reads from a `lookup func(string) (string, bool)`
  so tests do not depend on the process environment
- [x] 2.3 Typed parsers: `chatID`, `optionalChatID`, `idList`,
  `enum`, `duration`, `boolean`, `intVal`, `location`, `httpURL`
- [x] 2.4 Cross-field validation (pairwise distinct chat IDs;
  webhook-mode → secrets; `INVITE_MODE=direct` → flag + TTL cap;
  webhook paths start with `/`)
- [x] 2.5 Aggregated error reporting (every issue surfaced together)
- [x] 2.6 `.env` autoload via `godotenv` (best-effort, missing file
  is not an error)
- [x] 2.7 `config_test.go` table-driven tests covering each rule

## 3. `access-domain` capability

- [x] 3.1 Value types for users, subscription platforms, events,
  verdicts, effective access status, grants, invites and revocations
- [x] 3.2 Dependency direction points toward `internal/domain`
- [x] 3.3 No behavioral interfaces or infrastructure imports in
  `internal/domain`

## 4. `storage` capability

- [x] 4.1 `Open` with WAL/busy_timeout/foreign_keys/synchronous
  /_txlock pragmas
- [x] 4.2 Parent dir `0700`, db file `0600`
- [x] 4.3 `SetMaxOpenConns(1)`, `SetConnMaxLifetime(1h)`
- [x] 4.4 `CheckSchema` reads `goose_db_version` directly; returns
  `ErrUnmigrated` with a `task migrate:up` hint
- [x] 4.5 Time/null helpers (`rfc3339`, `nullTime`, `parseTime`,
  `parseNullTime`, `nullString`)
- [x] 4.6 Repository types: `users`, `subscriptions`, `grants`,
  `revocations`, `whitelist`, `audit`, `alerts`, `meta`
- [x] 4.7 `store_test.go` covers: schema presence, file mode,
  foreign keys, active-subscription uniqueness, invite-link partial
  uniques, basic repository round-trips

## 5. `migrations` capability

- [x] 5.1 `cmd/migrate` binary with `up` and `status` subcommands
- [x] 5.2 `--migrations-dir` flag (default `./migrations`)
- [x] 5.3 goose v3 provider over `os.DirFS(dir)` with dialect SQLite3
- [x] 5.4 Shared `config` and `applog` packages with the bot binary
- [x] 5.5 `0001_init.sql` carrying every table from SPEC §10.2
- [x] 5.6 Migration files moved out of `internal/store/migrations/`
  to top-level `migrations/` (commit b2844db)

## 6. `runtime` capability

- [x] 6.1 `applog.New` builds `slog` handler from `LogLevel` +
  `LogFormat`
- [x] 6.2 `cmd/gatekeeper/main.go` orchestrates startup in the
  documented order
- [x] 6.3 Root context cancelled by `SIGINT`/`SIGTERM`
- [x] 6.4 `db.Close` deferred last; logged on error
- [x] 6.5 Fatal errors print `"fatal: <error>"` to stderr with
  `os.Exit(1)`
- [x] 6.6 No HTTP server, no background subsystems

## 7. Verification

- [x] 7.1 `task test` is green (`go test ./...`)
- [x] 7.2 `task lint` is green (`go vet` + golangci-lint)
- [x] 7.3 `task build` produces a static binary
- [x] 7.4 `task migrate:up` applies `0001_init.sql` on an empty db
- [x] 7.5 Re-running `task migrate:up` reports "already up to date"
- [x] 7.6 `gatekeeper` against an empty db exits with the
  `ErrUnmigrated` error
- [x] 7.7 `sqlite3 data/gatekeeper.db ".tables"` shows all twelve
  tables plus `goose_db_version`
