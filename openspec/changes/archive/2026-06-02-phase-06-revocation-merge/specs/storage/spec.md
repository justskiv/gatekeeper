## ADDED Requirements

### Requirement: Revocation repository supports due scheduling and cancellation

`store` MUST provide repository operations for `pending_revocations`
using the existing `DBTX` pattern. The repository MUST support
idempotent create-if-absent, get by `tg_id`, delete by `tg_id`, list
due revocations ordered by `scheduled_at`, and mark warning as notified
without creating duplicate rows.

All timestamp values MUST cross the SQL boundary as RFC3339 UTC. Create
and delete operations MUST be safe inside handler transactions so
subscription changes, audit rows, outbox rows and revocation state can
commit atomically.

#### Scenario: Create pending revocation is idempotent
- **WHEN** `Revocations.CreateIfAbsent` is called twice for one `tg_id`
- **THEN** `pending_revocations` contains one row
- **AND** the original `scheduled_at` is preserved unless caller
  explicitly reschedules

#### Scenario: Due revocations are ordered
- **WHEN** Reconciler asks for due revocations at time `now`
- **THEN** repository returns rows with `scheduled_at <= now`
- **AND** rows are ordered by `scheduled_at` from oldest to newest

#### Scenario: Delete missing revocation is no-op
- **WHEN** `Revocations.Delete` is called for a user without pending
  revocation
- **THEN** the method returns success without changing other rows

### Requirement: Access-control repositories support manual grants and bans

`store` MUST expose narrow operations for owner access commands:
whitelist add/remove/check, manual subscription upsert/expire with
optional `expires_at`, `users.banned` set/unset, and listing grants
eligible for revoke by `tg_id`. These operations MUST be usable inside
the same transaction as audit rows and outbox actions.

`/grant` by numeric `tg_id` MUST be able to create a minimal stub user
without username or profile fields. Username lookup MUST remain local
through `Users.FindByUsername` and MUST NOT call Telegram.

#### Scenario: Manual subscription with expiry can be upserted
- **WHEN** owner grants a user manual access until a timestamp
- **THEN** store creates or updates one active `manual` subscription for
  that user
- **AND** `expires_at` is preserved as RFC3339 UTC

#### Scenario: Whitelist removal is idempotent
- **WHEN** owner revokes whitelist for a user who is not whitelisted
- **THEN** repository returns success without deleting other manual
  access data

#### Scenario: Banned flag updates narrowly
- **WHEN** owner bans or unbans a user
- **THEN** store changes `users.banned` and `updated_at`
- **AND** profile, username and dm state are preserved

### Requirement: Alerts repository deduplicates and supports delivery

`Alerts.Create` MUST support stable dedupe keys for open alerts. If an
open alert with the same key already exists, creation MUST return the
existing alert or explicit duplicate result without creating a second
open row. When a new alert is created, callers MUST be able to enqueue
operator delivery in the same transaction.

Alert rows MUST retain severity, kind, machine-readable metadata and
status. Resolving an alert MUST update only alert status/resolution
fields and MUST NOT delete forensic context.

#### Scenario: Duplicate open alert is deduplicated
- **WHEN** the same alert kind and dedupe key are raised twice
- **THEN** there is at most one open alert for that key
- **AND** duplicate creation does not require duplicate owner delivery

#### Scenario: Alert and delivery share one transaction
- **WHEN** a critical alert is created and owner delivery is needed
- **THEN** alert row and `send_dm` or admin-log outbox action can be
  committed atomically

#### Scenario: Resolve preserves alert context
- **WHEN** owner resolves an alert
- **THEN** status changes to resolved
- **AND** original kind, severity and metadata remain readable

### Requirement: Cleanup repository operations preserve forensic rows

`store` MUST provide cleanup operations for retention without embedding
policy in SQL call sites. Cleanup MUST support deleting old terminal
`telegram_updates` and `tribute_events`, deleting old done
`access_actions`, deleting resolved old alerts, applying
`AUDIT_RETENTION` to `audit_log`, expiring personal/direct invite links
and running WAL checkpoint.

Cleanup operations MUST NOT automatically delete failed inbox rows or
dead outbox actions. Those rows remain available for operator
investigation.

#### Scenario: Processed inbox rows can be deleted by cutoff
- **WHEN** cleanup receives a cutoff from `RAW_RETENTION`
- **THEN** processed or ignored inbox rows older than cutoff are
  deleted
- **AND** failed rows are left untouched

#### Scenario: Dead actions survive cleanup
- **WHEN** an `access_actions` row has `status='dead'`
- **THEN** cleanup does not delete it only because it is old

#### Scenario: Expired personal links are selected for maintenance
- **WHEN** active personal or direct invite links have
  `expires_at <= now`
- **THEN** repository can mark them `expired` and return links needing
  `revoke_invite`
