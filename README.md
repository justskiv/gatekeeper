# Gatekeeper

Self-hosted Telegram access-control bot for paid subscriptions. Gatekeeper
observes subscription sources (Boosty group, Tribute channel) and manages
access to the club chat and channel accordingly.

The project is built phase by phase from the specification in
`local/specs/init/SPEC.md`. This is the foundation phase: build tooling,
configuration, the SQLite schema and the domain model.

## Requirements

- Go 1.26+
- [Task](https://taskfile.dev) (`go-task`) for the build commands
- SQLite is embedded via `modernc.org/sqlite` — no cgo, no system SQLite

## Local run

```sh
cp .env.example .env      # then fill in real values
task build                # builds bin/gatekeeper (static, CGO_ENABLED=0)
task run                  # or run directly
```

On startup the binary loads and validates the configuration, applies the
SQLite migrations and waits for `SIGINT`/`SIGTERM` to shut down cleanly.

## Commands

| Command | Description |
|---|---|
| `task build` | Build a static binary into `bin/gatekeeper` |
| `task run` | Run the application |
| `task test` | Run all tests |
| `task lint` | `go vet` + `golangci-lint` |
| `task tidy` | `go mod tidy` |

## Configuration

All configuration comes from environment variables (12-factor); in
development a `.env` file is loaded automatically. See `.env.example` for
the full list and `local/specs/init/SPEC.md` §18 for the reference.

## Specification

The full design lives in `local/specs/init/SPEC.md`.
