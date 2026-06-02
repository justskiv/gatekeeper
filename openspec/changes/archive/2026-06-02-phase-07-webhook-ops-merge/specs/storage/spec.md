## ADDED Requirements

### Requirement: Tribute events repository supports webhook inbox semantics

`store` MUST provide a repository for `tribute_events` using the
existing `DBTX` pattern. The repository MUST support:

- insert received event with `dedup_key`, event name, optional `tg_id`,
  subscription id, `signature_valid`, redacted `payload_json` and
  `received_at`;
- detect existing event by `dedup_key` before processing;
- mark event as `processed`, `ignored` or `failed` with
  `processed_at` and optional error text;
- preserve failed rows for operator investigation until retention rules
  explicitly allow cleanup.

Repository methods MUST be usable inside the same transaction as
subscription changes, audit rows and terminal event status.

#### Scenario: Insert valid Tribute event
- **WHEN** webhook handler receives a valid non-duplicate Tribute event
- **THEN** repository inserts one `tribute_events` row with
  `signature_valid=1` and status `received`

#### Scenario: Dedup key detects retry
- **WHEN** a `tribute_events` row already exists for a `dedup_key`
- **THEN** repository reports duplicate without inserting another row

#### Scenario: Terminal status can share transaction
- **WHEN** webhook processing updates a subscription and marks the event
  `processed`
- **THEN** both changes can commit in one SQL transaction

#### Scenario: Failed event keeps forensic context
- **WHEN** webhook processing fails after event insertion
- **THEN** repository can mark the event `failed` with an error
- **AND** cleanup does not remove failed events only because they are old

### Requirement: Raw inbox payloads are redacted before storage

Gatekeeper MUST redact PII and secrets from raw payload JSON before
writing `telegram_updates.payload_json` or
`tribute_events.payload_json`. Redaction MUST remove or mask email
addresses, `web_app_link`, address/tracking fields, provider keys, bot
tokens, Telegram webhook secrets and full invite URLs. Redaction MUST
preserve non-sensitive fields needed for forensics, such as event name,
timestamps, Telegram user id, subscription id, period id and status.

Code MUST extract typed fields needed by domain processing before or
during parsing, but raw stored payload MUST be redacted. Technical logs
MUST follow the same rule and MUST NOT print raw payloads that contain
unredacted PII or secrets.

#### Scenario: Tribute email is not stored raw
- **WHEN** Tribute payload contains an email field
- **THEN** `tribute_events.payload_json` does not contain the raw email
- **AND** domain processing can still use Telegram user id and
  subscription period fields

#### Scenario: Telegram web_app_link is redacted
- **WHEN** Telegram update payload contains `web_app_link`
- **THEN** `telegram_updates.payload_json` stores a redacted value

#### Scenario: Full invite URL is not logged
- **WHEN** invite URL appears in a payload or diagnostic context
- **THEN** logs and raw inbox rows contain only a redacted value or
  stable hash

### Requirement: Ops read models expose statistics, alerts and export data

`store` MUST expose narrow read methods for owner ops commands without
leaking write concerns into bot handlers. The read model MUST support:

- active subscriptions grouped by platform;
- club grants grouped by resource and state;
- due or pending revocations;
- health values from `meta.health.*`;
- `meta.reconcile.last_run_at`;
- outbox queue counts by status and count of `dead`;
- open alerts with ids, severity, kind, title, detail and timestamps;
- known chats and configured chat IDs;
- CSV export rows for users and current subscriptions.

These reads MUST be bounded or paginated where result size can grow and
MUST NOT include raw provider payload JSON in owner summaries or CSV
unless explicitly required by a future change.

#### Scenario: Stats can read operational counters
- **WHEN** `/stats` builds its response
- **THEN** store can provide counts for subscriptions, grants,
  revocations, health, reconcile freshness, outbox and open alerts

#### Scenario: Alerts list returns open alerts
- **WHEN** `/alerts` asks for unresolved operator alerts
- **THEN** store returns open alerts ordered by severity and creation
  time

#### Scenario: Export excludes raw payloads
- **WHEN** `/export` generates CSV
- **THEN** rows include users and current subscriptions
- **AND** raw `telegram_updates.payload_json` and
  `tribute_events.payload_json` are not included
