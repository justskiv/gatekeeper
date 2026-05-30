## ADDED Requirements

### Requirement: The `gatekeeper` binary follows a fixed startup order

`cmd/gatekeeper` MUST orchestrate startup in this order:

1. Load configuration (`config.Load`).
2. Build the slog logger from the config (`applog.New`) and set it
   as the default.
3. Install a root context cancelled by `SIGINT` and `SIGTERM`
   (`signal.NotifyContext`).
4. Open the database (`store.Open`).
5. Verify the schema (`store.CheckSchema`).
6. Wait for the context to be cancelled.
7. Close the database last, in a deferred call after every other
   resource has stopped.

Each step MUST run only after the previous one has succeeded; an
error from any step MUST abort startup and propagate to `main`.

#### Scenario: Misconfigured startup
- **WHEN** `config.Load` returns an error
- **THEN** the process prints `"fatal: <error>"` to stderr, exits
  with code `1` and never touches the database

#### Scenario: Unmigrated database at startup
- **WHEN** `store.CheckSchema` returns an `ErrUnmigrated` error
- **THEN** the process exits with code `1` and the error message
  instructs the operator to run `task migrate:up`

#### Scenario: Clean shutdown signal
- **WHEN** the process receives `SIGINT` or `SIGTERM` after a
  successful startup
- **THEN** the root context is cancelled
- **AND** the deferred `db.Close()` runs before the process exits with
  code `0`

### Requirement: Phase 01 runs no background subsystems

The Phase 01 binary MUST NOT start a poller, an enforcer, a
reconciler or an HTTP server. After verifying the schema it MUST
log that it has started and MUST block on the shutdown signal.
Later phases SHALL attach their subsystems at this same point.

#### Scenario: Phase 01 idle main loop
- **WHEN** the process has finished startup and reached the wait point
- **THEN** no goroutine is performing Telegram, Tribute, enforcement,
  reconciliation or HTTP work
- **AND** the only ongoing work is waiting on `<-ctx.Done()`

### Requirement: Both binaries share log configuration via `applog`

The `applog` package MUST build a `log/slog.Logger` from
`Config.LogLevel` (`debug`/`info`/`warn`/`error`) and
`Config.LogFormat` (`json`/`text`). The same builder MUST be used
by both the `gatekeeper` and `migrate` binaries so every process
emits logs in the same shape.

#### Scenario: Configured log level
- **WHEN** `LOG_LEVEL=debug`
- **THEN** `applog.New` returns a logger whose handler emits records
  at level `Debug` and above

#### Scenario: Configured log format
- **WHEN** `LOG_FORMAT=text`
- **THEN** `applog.New` returns a logger backed by a text handler
  writing to stdout; otherwise the handler is JSON

### Requirement: Fatal errors print to stderr and exit non-zero

Both binaries MUST use the same fatal shape:

```
fatal: <error>
```

written to stderr, followed by `os.Exit(1)`. The logger may not
yet be configured at the moment a fatal error is detected, so
stderr MUST be the durable channel.

#### Scenario: Any unrecoverable error from `run`
- **WHEN** the inner `run()` function returns a non-nil error
- **THEN** `main` writes `"fatal: <message>"` to stderr and exits with
  code `1`
