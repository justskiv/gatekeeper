## ADDED Requirements

### Requirement: HTTP server exposes only enabled operational routes

Gatekeeper MUST expose an HTTP server on `WEBHOOK_LISTEN_ADDR` only when
`TRIBUTE_MODE=webhook`, `TELEGRAM_MODE=webhook` or
`METRICS_ENABLED=true`. The server MUST use explicit
`ReadHeaderTimeout`, `ReadTimeout`, request body limits for webhook
routes and graceful `Shutdown` on process cancellation.

Enabled routes MUST be:

- `GET /healthz` when the server is running;
- `GET /readyz` when the server is running;
- `GET /metrics` only when `METRICS_ENABLED=true`;
- `POST {TRIBUTE_WEBHOOK_PATH}` only when `TRIBUTE_MODE=webhook`;
- `POST {TELEGRAM_WEBHOOK_PATH}` only when `TELEGRAM_MODE=webhook`.

Routes disabled by mode MUST return `404`. Unsupported methods on
enabled webhook paths MUST return `405`.

#### Scenario: Clean polling mode opens no HTTP listener
- **WHEN** `TELEGRAM_MODE=polling`, `TRIBUTE_MODE=observation` and
  `METRICS_ENABLED=false`
- **THEN** runtime does not open a listener on `WEBHOOK_LISTEN_ADDR`

#### Scenario: Metrics enables HTTP without webhook modes
- **WHEN** `METRICS_ENABLED=true` while both webhook modes are off
- **THEN** the HTTP server starts
- **AND** `/metrics`, `/healthz` and `/readyz` are available
- **AND** Tribute and Telegram webhook paths return `404`

#### Scenario: Disabled route is not mounted
- **WHEN** `TRIBUTE_MODE=observation`
- **THEN** запрос на `TRIBUTE_WEBHOOK_PATH` возвращает `404`

### Requirement: Health and readiness report process and dependency state

`GET /healthz` MUST return `200` after the HTTP server starts and MUST
NOT perform network or database checks.

`GET /readyz` MUST return `200` only when all readiness inputs are good:
SQLite is reachable, `store.CheckSchema` succeeds, startup `getMe`
succeeded, all four `meta.health.*` keys are `ok`, and
`meta.reconcile.last_run_at` exists and is not older than two
`RECONCILE_INTERVAL` periods. When `TELEGRAM_MODE=webhook`, successful
Telegram webhook registration is also a readiness input. If any input is
bad, `/readyz` MUST return `503` with a compact machine-readable
description of failed checks.

#### Scenario: Liveness succeeds after server start
- **WHEN** HTTP server is running
- **THEN** `GET /healthz` returns `200`

#### Scenario: Readiness succeeds with healthy dependencies
- **WHEN** SQLite/schema are available, `getMe` succeeded, all
  `meta.health.*` values are `ok` and reconciliation is fresh
- **THEN** `GET /readyz` returns `200`

#### Scenario: Lost admin rights make readiness fail
- **WHEN** `meta.health.club_channel` starts with `fail:`
- **THEN** `GET /readyz` returns `503`
- **AND** the response names `club_channel`

#### Scenario: Stale reconciliation makes readiness fail
- **WHEN** `meta.reconcile.last_run_at` is older than two
  `RECONCILE_INTERVAL` periods
- **THEN** `GET /readyz` returns `503`
- **AND** the response names stale reconciliation

#### Scenario: Telegram webhook registration gates readiness
- **WHEN** `TELEGRAM_MODE=webhook` and Telegram webhook registration has
  not succeeded
- **THEN** `GET /readyz` returns `503`
- **AND** the response names webhook registration

### Requirement: Metrics endpoint emits bounded Prometheus metrics

When `METRICS_ENABLED=true`, `GET /metrics` MUST emit Prometheus text
format with bounded-label metrics. Labels MUST NOT contain `tg_id`,
username, email, invite URLs, raw error strings or other unbounded PII.

The metric set MUST include:

- `gatekeeper_updates_total{type}`;
- `gatekeeper_webhooks_total{status}`;
- `gatekeeper_subscriptions_active{platform}`;
- `gatekeeper_access_grants{resource,state}`;
- `gatekeeper_invite_links{resource,mode,status}`;
- `gatekeeper_outbox_actions_total{type,status}`;
- `gatekeeper_outbox_pending`;
- `gatekeeper_telegram_api_errors_total{method,code}`;
- `gatekeeper_reconcile_duration_seconds`;
- `gatekeeper_revocations_total{reason}`.

#### Scenario: Metrics disabled hides endpoint
- **WHEN** `METRICS_ENABLED=false`
- **THEN** `GET /metrics` returns `404`

#### Scenario: Metrics enabled returns Prometheus text
- **WHEN** `METRICS_ENABLED=true`
- **THEN** `GET /metrics` returns `200`
- **AND** the response is parseable as Prometheus text format

#### Scenario: Metrics labels do not expose users
- **WHEN** metrics are emitted for subscriptions, grants and actions
- **THEN** labels use enum values such as `platform`, `resource`,
  `state`, `type`, `status`, `method` and `code`
- **AND** labels do not include `tg_id`, username, email or raw errors

### Requirement: Tribute webhook validates HMAC before parsing JSON

`POST {TRIBUTE_WEBHOOK_PATH}` MUST read the raw request body before JSON
parsing, cap it to approximately 64 KiB, and validate header
`trbt-signature` against
`hex(HMAC-SHA256(key=TRIBUTE_API_KEY, msg=raw_body))` using
constant-time comparison. The handler MUST NOT validate signatures over
re-marshaled JSON.

On invalid signature, the handler MUST write a redacted
`tribute_events` row with `signature_valid=0` and `status='failed'`,
append `audit_log(webhook_rejected)`, update webhook metrics and return
`401`. Invalid signature MUST NOT call engine handlers.

#### Scenario: Valid signature allows parsing
- **WHEN** Tribute sends `POST {TRIBUTE_WEBHOOK_PATH}` with a signature
  computed over the exact raw body
- **THEN** the handler parses JSON and continues processing

#### Scenario: Re-marshaled body is not used for HMAC
- **WHEN** the raw body has JSON whitespace or key order that would
  change after marshaling
- **THEN** validation uses the original bytes and accepts the correct
  raw-body signature

#### Scenario: Invalid signature is rejected and recorded
- **WHEN** `trbt-signature` does not match the raw body
- **THEN** the response is `401`
- **AND** `tribute_events.signature_valid=0` with status `failed`
- **AND** `audit_log(webhook_rejected)` is written
- **AND** no subscription state changes

### Requirement: Tribute webhook processing is idempotent and event-driven

For a valid Tribute webhook, Gatekeeper MUST compute `dedup_key` as
`sha256(name|payload.subscription_id|payload.period_id|created_at)`.
When any of those fields are unavailable, it MUST fall back to
`sha256(raw_body)`. A duplicate `dedup_key` MUST return `200` and MUST
NOT repeat domain side effects.

The handler MUST persist a `tribute_events` row with
`signature_valid=1`, redacted `payload_json`, provider event name,
optional `tg_id`, subscription id, status and timestamps. Supported
subscription events are `new_subscription`, `renewed_subscription` and
`cancelled_subscription`. Other Tribute events MUST be recorded as
`ignored` and return `200`.

`new_subscription` and `renewed_subscription` MUST create an
`Activated` domain event for platform `tribute` with `EventAt`,
`ExpiresAt`, `ExternalID`, `PeriodID` and tier from Tribute payload.
`cancelled_subscription` MUST use the status-core cancellation semantics.

#### Scenario: Duplicate webhook is a no-op
- **WHEN** the same Tribute webhook is delivered twice with the same
  `dedup_key`
- **THEN** the second request returns `200`
- **AND** no second subscription update, audit event or recompute side
  effect is executed

#### Scenario: Renewed subscription extends expiry
- **WHEN** a valid `renewed_subscription` event has a newer
  `created_at` and later `expires_at`
- **THEN** the active Tribute subscription stores the later `expires_at`
- **AND** `tribute_events.status` becomes `processed`

#### Scenario: Unsupported Tribute event is ignored
- **WHEN** a valid Tribute event name is not a subscription event
- **THEN** `tribute_events.status` becomes `ignored`
- **AND** the response is `200`

#### Scenario: Internal processing error is retryable by Tribute
- **WHEN** a valid non-duplicate webhook cannot be committed because of
  an internal error
- **THEN** the response is `5xx`
- **AND** the stored event remains failed or received with error details
  sufficient for operator investigation
