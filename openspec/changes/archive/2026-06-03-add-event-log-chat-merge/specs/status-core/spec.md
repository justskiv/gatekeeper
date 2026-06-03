## ADDED Requirements

### Requirement: Subscription events emit source operator events

`handleEvent(SubscriptionEvent)` MUST emit operator events for durable
subscription source changes. `Activated` events MUST emit
`source_subscription_activated`; `Deactivated` events MUST emit
`source_subscription_expired`; `cancelled_subscription` MUST NOT emit an
expired event unless configuration applies it as immediate
deactivation.

Source operator events MUST include platform (`boosty`, `tribute` or
`manual`), tg_id, safe user label, provider event name when available,
tier and expiry when present. Raw provider payload and provider secrets
MUST NOT be included.

#### Scenario: Boosty activation is logged

- **WHEN** a Boosty source event activates a user's subscription
- **THEN** `source_subscription_activated` is enqueued
- **AND** the event identifies `boosty` as the platform

#### Scenario: Tribute webhook activation includes safe metadata

- **WHEN** a Tribute webhook `new_subscription` activates a user with
  tier and `expires_at`
- **THEN** `source_subscription_activated` is enqueued
- **AND** the event includes tier and expiry
- **AND** the event omits raw webhook JSON

#### Scenario: Cancel without immediate deactivation is not expiry

- **WHEN** Tribute sends `cancelled_subscription` and
  `TRIBUTE_CANCEL_IS_IMMEDIATE=false`
- **THEN** no `source_subscription_expired` event is emitted
- **AND** existing cancellation audit behavior remains unchanged

### Requirement: Effective access transitions emit grant events and delegate loss

The engine MUST compare the persisted effective access decision before
and after applying a source or manual event when enough local data is
available. If status transitions from non-active to `active`, it MUST
emit `access_granted` once for that active episode, using the
per-episode idempotency marker shared with admission (see `grant-access`)
so the engine transition and a later admission never double-emit. If
status transitions from `active` to
`inactive`, the engine MUST NOT emit `access_lost` directly; it MUST let
the configured revocation flow emit `access_loss_scheduled`,
`access_lost` or no resource-loss event according to `EXPIRY_MODE` and
actual grant changes. If status remains active because another source is
still active, the engine MUST emit only the source event and MUST NOT
emit loss lifecycle events.

`unknown` MUST NOT cause `access_loss_scheduled` or `access_lost`. When
the engine cannot determine a transition without network access inside
the transaction, it MUST not invent one; it MUST rely on the source
event and later admission or revocation events.

#### Scenario: First active source grants access

- **WHEN** a user with no active source receives an active Boosty event
- **THEN** the engine emits `source_subscription_activated`
- **AND** it emits `access_granted` with reason `boosty`

#### Scenario: Last active source expires

- **WHEN** a user's only active source expires
- **THEN** the engine emits `source_subscription_expired`
- **AND** the configured revocation flow emits
  `access_loss_scheduled`, `access_lost` or no resource-loss event
  according to expiry mode and actual grant changes

#### Scenario: One source expires while another remains active

- **WHEN** a user's Boosty source expires but Tribute remains active
- **THEN** the engine emits `source_subscription_expired` for Boosty
- **AND** it does not emit loss lifecycle events
- **AND** the remaining Tribute reason is available for later access
  events

#### Scenario: Unknown does not log access loss

- **WHEN** a source observation is `unknown`
- **THEN** no `access_loss_scheduled` or `access_lost` operator event is
  emitted
- **AND** active subscriptions are not closed because of the unknown
  observation

### Requirement: Access lifecycle reasons use existing decision data

Access lifecycle operator events emitted by the engine MUST use existing
`AccessDecision.Reasons`, active `subscriptions`, hard-ban state and
manual source state. The event MUST NOT introduce new source semantics.

When whitelist or manual subscription grants access, the event MUST use
source `manual` and include the safe admin reason if available. When
hard-ban removes access, loss events emitted by admin/revocation paths
MUST identify hard-ban as the overriding reason and MUST not claim that
Boosty or Tribute expired.

#### Scenario: Manual source is rendered as manual access

- **WHEN** manual access changes the effective status to active
- **THEN** `access_granted` uses source `manual`
- **AND** a safe admin reason is included when one exists

#### Scenario: Hard-ban overrides active source in log

- **WHEN** hard-ban changes an otherwise active user to inactive
- **THEN** `access_lost` identifies hard-ban as the overriding reason
- **AND** it does not report the active source as expired
