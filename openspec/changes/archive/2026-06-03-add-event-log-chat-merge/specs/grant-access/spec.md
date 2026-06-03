## ADDED Requirements

### Requirement: Admission emits access-granted only when it establishes eligibility

`access_granted` MUST have a single meaning across the system: the
user's effective eligibility transitioned to active. It MUST be emitted
at most once per active access episode, and engine and admission MUST
share a stable per-episode idempotency marker so the two emit points
never produce a duplicate (see `status-core` for the engine transition).

Admission MUST emit `access_granted` only when admission itself
establishes active eligibility that was not already logged for the
current episode — typically the fallback path where live status is
`unknown` but a fresh active subscription allows admission, or any
admission that grants the first active access without a prior engine
transition event. When the user is already eligible (the engine already
logged `access_granted` for this episode) and `/start` merely issues
invites or creates `pending` grants, admission MUST NOT emit another
`access_granted`; the resulting join later produces resource membership
events instead. User-facing replies remain unchanged.

When emitted, the event MUST include tg_id, safe user label, resource
list, invite mode, admission method `bot_link` and all active source
reasons from the live `AccessDecision` or fallback active subscriptions.

#### Scenario: First admission for an active user logs access-granted once

- **WHEN** an active user is admitted and no `access_granted` was yet
  logged for this episode
- **THEN** exactly one `access_granted` operator event is enqueued in the
  same transaction
- **AND** the event includes all active source reasons and `INVITE_MODE`
- **AND** no full invite URL is included

#### Scenario: Already-logged eligibility does not re-emit on start

- **WHEN** the engine already logged `access_granted` for the active
  episode and `/start` only issues invites or creates pending grants
- **THEN** admission does NOT emit another `access_granted`
- **AND** the subsequent join emits resource membership events instead

#### Scenario: Fallback active start identifies fallback source

- **WHEN** live status is `unknown`, but fresh active subscription
  fallback allows admission and no prior transition was logged
- **THEN** admission emits one `access_granted` marked fallback-based
- **AND** it lists the active subscription platform(s) known in storage

#### Scenario: Repeat start does not duplicate access-granted

- **WHEN** a user repeats `/start` while the same pending grants or
  invite actions already exist
- **THEN** no duplicate `access_granted` operator event is enqueued
- **AND** the user still receives the normal idempotent admission reply

### Requirement: Join-request decisions emit operator admission events

Grant-access flow MUST emit operator admission events when a managed
`chat_join_request` is approved and the user's grant is transitioned to
`joined`. The event MUST record resource, admission method `bot_link`,
source reasons and invite mode. If this approval only completes an
already logged pending access request, the event MUST be a resource
membership event, not a duplicate `access_granted` event.

Declined join requests MUST NOT emit `access_lost`, because no access
was granted. Declines MAY continue to create alerts and audit rows
according to existing grant-access requirements.

#### Scenario: Approved join logs bot admission

- **WHEN** an active user is approved through a managed join request
- **THEN** a resource membership operator event is enqueued
- **AND** the event records admission method `bot_link`
- **AND** source reasons come from the same decision used to approve

#### Scenario: Declined join does not log access loss

- **WHEN** a join request is declined because the user is inactive
- **THEN** no `access_lost` operator event is emitted
- **AND** existing audit and user DM behavior remain unchanged

### Requirement: Club membership updates emit operator membership events

`chat_member` updates for managed club resources MUST emit operator
membership events when observed membership changes. For the club chat,
joins MUST emit `club_chat_joined` and leaves MUST emit
`club_chat_left`. For the club channel, joins MUST emit
`club_channel_subscribed` and leaves MUST emit
`club_channel_unsubscribed`.

Joined events MUST include admission method. The method MUST be
`bot_link` when the handler has bot admission evidence through
join-request, pending bot grant or accepted direct invite. The method
MUST be `external` when no bot evidence exists. If the Telegram update
exposes an actor for an external membership change, the event MUST
include a safe actor label; otherwise it MUST preserve `external`
without guessing an admin.

Joined/subscribed events MUST also include the joining user's active
access source reasons (`boosty`, `tribute`, `manual`) so the operator
sees *why* the user has access at the moment they entered the resource,
not only how they entered. Reasons MUST come from already-available data
— stored active `subscriptions` or the last `AccessDecision` — and MUST
NOT require a network probe inside the handler. When no active source is
known for the joining user (for example an external join by someone with
no eligibility), the event MUST state that the access source is unknown
rather than guess one.

Leave/unsubscribe events MUST be emitted even when the grant is already
`revoked`; the grant state MUST still not be overwritten by the leave.

#### Scenario: Bot-admitted chat join is logged

- **WHEN** a club chat `chat_member` update shows a user joined through
  bot admission evidence
- **THEN** `club_chat_joined` is enqueued
- **AND** the event records admission method `bot_link`

#### Scenario: Membership join lists the access source reasons

- **WHEN** a user with an active Boosty subscription joins the club chat
- **THEN** `club_chat_joined` includes access source reason `boosty`
- **AND** when the user is also active on Tribute, both reasons are listed
- **AND** an external join by a non-eligible user states the source is
  unknown instead of guessing

#### Scenario: External channel subscription is logged

- **WHEN** a club channel `chat_member` update shows a user subscribed
  without bot admission evidence
- **THEN** `club_channel_subscribed` is enqueued
- **AND** the event records admission method `external`
- **AND** the existing `external_join` audit/alert behavior remains

#### Scenario: Chat leave is logged without overwriting revoked grant

- **WHEN** a club chat `chat_member` update shows a user left and the
  grant is already `revoked`
- **THEN** `club_chat_left` is enqueued
- **AND** the grant remains `revoked`

#### Scenario: Channel unsubscribe is logged

- **WHEN** a club channel `chat_member` update shows a user is no longer
  a member
- **THEN** `club_channel_unsubscribed` is enqueued
- **AND** the event includes the best available reason from grant state
  or observed Telegram membership change
