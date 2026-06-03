## ADDED Requirements

### Requirement: Owner access commands emit operator events

Owner commands `/grant`, `/revoke`, `/ban` and `/unban` MUST emit
operator events after confirmed execution. The event MUST be enqueued
in the same transaction as the command's durable domain changes, audit
rows and outbox actions.

`/grant` MUST emit `manual_grant` and, when it changes effective access
from non-active to active, `access_granted` with source `manual`.
`/revoke` MUST emit `manual_revoke` and MUST rely on normal
`recomputeAccess` or revocation execution for any `access_lost` event.
`/ban` MUST emit `manual_ban` and MUST rely on hard-ban/revocation
paths for resource access loss events. `/unban` MUST emit
`manual_unban`; it MUST NOT emit `access_granted` unless active status
and normal pull-based admission later grant access.

#### Scenario: Manual grant is logged

- **WHEN** owner confirms `/grant 12345 30d comp`
- **THEN** a `manual_grant` operator event is enqueued
- **AND** it includes tg_id, actor `admin`, duration or expiry and safe
  reason `comp`

#### Scenario: Manual grant can grant effective access

- **WHEN** `/grant` changes a user from inactive to active
- **THEN** an `access_granted` operator event is enqueued with source
  `manual`
- **AND** the event is idempotent for the confirmed action id

#### Scenario: Manual revoke logs command and uses revocation path

- **WHEN** owner confirms `/revoke 12345 cleanup`
- **THEN** a `manual_revoke` operator event is enqueued
- **AND** any `access_lost` event is emitted only by the configured
  revocation flow if access is actually removed

#### Scenario: Unban does not grant access by itself

- **WHEN** owner confirms `/unban 12345 pardon`
- **THEN** a `manual_unban` operator event is enqueued
- **AND** no `access_granted` event is emitted by unban alone

### Requirement: Operator events respect confirmation idempotency

Owner confirmation callbacks MUST use the same command action idempotency
state for operator event delivery. A repeated confirm callback for the
same action MUST NOT enqueue duplicate operator event messages.

Expired, cancelled or mismatched confirmations MUST NOT emit operator
events because the domain action did not execute.

#### Scenario: Repeated confirm does not duplicate event

- **WHEN** owner presses the same `/ban` confirm callback twice
- **THEN** the hard-ban side effects execute once
- **AND** only one `manual_ban` operator event is enqueued

#### Scenario: Cancelled action is not logged as executed

- **WHEN** owner cancels a pending `/grant`
- **THEN** no `manual_grant` operator event is emitted
- **AND** no access lifecycle event is emitted
