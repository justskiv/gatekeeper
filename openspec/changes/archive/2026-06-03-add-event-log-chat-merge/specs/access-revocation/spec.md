## ADDED Requirements

### Requirement: Revocation execution emits operator access-lost events

System MUST emit an `access_lost` operator event when `revokeNow`
actually revokes one or more bot-admitted grants. The event MUST be
written in the same transaction as grant revocation, audit rows and
`soft_kick` actions. The event MUST include tg_id, affected resources,
revocation reason, actor `system` or `job`, and whether the revocation
came from `grace`, `immediate` or manual/admin flow when that context is
available.

Scheduling a grace revocation MUST emit `access_loss_scheduled`, because
the user is in the warning window and still has access until the due
revocation executes. `notify_only` MUST NOT emit resource access loss
because grants are not changed.

#### Scenario: Immediate revoke logs access lost

- **WHEN** `EXPIRY_MODE=immediate` revokes bot-admitted chat and channel
  grants
- **THEN** one `access_lost` operator event is enqueued
- **AND** it lists both affected resources
- **AND** it includes the revocation reason

#### Scenario: Grace scheduling logs scheduled loss

- **WHEN** `EXPIRY_MODE=grace` creates `pending_revocation`
- **THEN** `access_loss_scheduled` is enqueued with `scheduled_at`
- **AND** no `access_lost` operator event is emitted yet
- **AND** the existing warning DM and audit behavior remain unchanged

#### Scenario: Notify-only does not claim resource removal

- **WHEN** `EXPIRY_MODE=notify_only` sends an expired notice without
  changing grants
- **THEN** no resource `access_lost` operator event is emitted
- **AND** source/access lifecycle events may still describe effective
  status changes according to status-core

### Requirement: Revocation cancellation emits access-kept events

System MUST emit `access_kept` when a pending revocation is cancelled
because effective status became `active` again. The event MUST include
tg_id, active source reasons that preserved access and the reason marker
`revocation_cancelled`.

If there is no pending revocation, active recompute remains a no-op and
MUST NOT emit `access_kept`.

#### Scenario: Active subscription cancels pending revoke

- **WHEN** a user renews before `pending_revocation.scheduled_at`
- **THEN** `pending_revocation` is deleted
- **AND** an `access_kept` operator event is enqueued
- **AND** active source reasons are included

#### Scenario: Scheduled loss is followed by access kept

- **WHEN** a grace-period revocation was previously logged as
  `access_loss_scheduled` and the user renews before removal
- **THEN** `access_kept` is enqueued
- **AND** no final `access_lost` event is emitted

#### Scenario: Active without pending revoke is silent

- **WHEN** recompute sees active status but no pending revocation exists
- **THEN** no `access_kept` operator event is emitted

### Requirement: Unsafe revocation paths do not emit false access loss

System MUST NOT emit `access_lost` when final revocation safety check
returns `unknown`, protected admin check blocks a kick, or no eligible
bot-admitted grants are revoked. It MUST continue to use existing audit
and `admin_alert` behavior for unsafe or protected cases.

When a protected admin blocks revocation for one resource but another
resource is successfully revoked, the `access_lost` event MUST include
only the resources actually revoked and alerts MUST cover the protected
resource.

#### Scenario: Unknown final check blocks access-lost event

- **WHEN** due revocation reaches final check and effective status is
  `unknown`
- **THEN** no `access_lost` operator event is emitted
- **AND** the existing unsafe revocation alert is created

#### Scenario: Protected resource is excluded from access-lost

- **WHEN** revocation skips a protected admin in the club chat but
  revokes the club channel grant
- **THEN** `access_lost` lists only the channel
- **AND** a protected-admin alert explains the skipped chat resource
