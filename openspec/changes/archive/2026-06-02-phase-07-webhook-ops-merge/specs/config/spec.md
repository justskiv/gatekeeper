## MODIFIED Requirements

### Requirement: Defaults are applied for optional keys

Optional keys MUST have defaults so a minimal `.env` is enough to
run. The defaults are:

- `DB_PATH` → `./data/gatekeeper.db`
- `INVITE_MODE` → `shared_join_request`, `INVITE_TTL` → `24h`
- `ADMISSION_FALLBACK_MAX_AGE` → `1h`,
  `ADMISSION_JOIN_REQUEST_RETRIES` → `2`
- `TRIBUTE_MODE` → `observation`
- `TRIBUTE_CANCEL_IS_IMMEDIATE` → `false`
- `WEBHOOK_LISTEN_ADDR` → `:8080`
- `TRIBUTE_WEBHOOK_PATH` → `/webhooks/tribute`
- `TELEGRAM_MODE` → `polling`
- `TELEGRAM_WEBHOOK_PATH` → `/webhooks/telegram`
- `TELEGRAM_WEBHOOK_PUBLIC_URL` → empty string
- `TELEGRAM_WEBHOOK_SECRET` → empty string
- `EXPIRY_MODE` → `grace`, `GRACE_PERIOD` → `72h`
- `RECONCILE_INTERVAL` → `1h`, `CLEANUP_INTERVAL` → `24h`
- `RAW_RETENTION` → `720h`, `AUDIT_RETENTION` → `8760h`
- `ENFORCER_WORKERS` → `2`
- `TIMEZONE` → `UTC`
- `LOG_LEVEL` → `info`, `LOG_FORMAT` → `json`,
  `METRICS_ENABLED` → `false`

#### Scenario: Optional key absent
- **WHEN** an optional key is unset
- **THEN** the parsed value equals the listed default

#### Scenario: Immediate Tribute cancellation defaults to safe behavior
- **WHEN** `TRIBUTE_CANCEL_IS_IMMEDIATE` is unset
- **THEN** the parsed value is `false`

## ADDED Requirements

### Requirement: Tribute cancellation override is explicit

Configuration MUST expose `TRIBUTE_CANCEL_IS_IMMEDIATE` as a boolean
operator override. The default `false` means Tribute
`cancelled_subscription` records cancellation but keeps access until
`expires_at`. The value `true` means the webhook handler may convert
`cancelled_subscription` into immediate deactivation according to
status-core requirements.

Invalid boolean values MUST be reported with the same accumulated
configuration error format as other invalid values.

#### Scenario: Immediate cancellation enabled
- **WHEN** `TRIBUTE_CANCEL_IS_IMMEDIATE=true`
- **THEN** configuration loading succeeds
- **AND** the parsed config carries the immediate-cancel override

#### Scenario: Invalid immediate cancellation value
- **WHEN** `TRIBUTE_CANCEL_IS_IMMEDIATE=soon`
- **THEN** configuration loading fails
- **AND** the returned error names `TRIBUTE_CANCEL_IS_IMMEDIATE`
