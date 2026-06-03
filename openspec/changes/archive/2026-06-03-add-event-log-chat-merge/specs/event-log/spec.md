## ADDED Requirements

### Requirement: Operator event log delivers selected events durably

System SHALL expose an operator event log for human-readable access and
membership events. Event delivery MUST target `EVENT_LOG_CHAT_ID`, a
dedicated Telegram group or supergroup where the bot and administrator
are members. Operator event delivery MUST NOT use `ADMIN_LOG_CHAT_ID`,
which remains reserved for ops alerts.

Operator event delivery MUST use durable `access_actions` and MUST be
written in the same transaction as the domain state change, audit row
and terminal incoming update status whenever the event is caused by an
incoming update. Handler code MUST NOT call Telegram synchronously to
send an operator event.

Each event MUST use an idempotency marker stable for the event source:
event kind, tg_id, resource, source workflow and either an existing
durable action/audit marker or a **source-stable** timestamp that comes
from the originating update (Telegram `chat_member`/join-request date,
provider event time). The marker MUST NOT be derived from process
wall-clock or render time (`time.Now()`), which would differ across
retries and defeat dedupe. Replaying the same update, callback or admin
confirmation MUST NOT create duplicate operator event messages.

#### Scenario: Event goes to event log chat

- **WHEN** `EVENT_LOG_CHAT_ID` is configured and a club member joins
- **THEN** a durable `send_dm` action is enqueued with
  `payload_json.chat_id` set to `EVENT_LOG_CHAT_ID`
- **AND** the action text is the rendered operator event
- **AND** no direct Telegram `sendMessage` call is made in the handler

#### Scenario: Admin alert chat is not used

- **WHEN** `ADMIN_LOG_CHAT_ID` is configured and an operator event is
  emitted
- **THEN** the operator event action still targets `EVENT_LOG_CHAT_ID`
- **AND** the event is not delivered to the ops-alert chat

#### Scenario: Replayed update does not duplicate event

- **WHEN** the same Telegram update is handled again after a crash
- **THEN** the operator event idempotency key resolves to the existing
  `access_actions` row
- **AND** a second operator event message is not enqueued

### Requirement: Operator events carry structured safe context

Operator events SHALL be rendered from typed event data, not by parsing
human-readable audit text. The event model MUST support:

- event kind;
- Telegram user id and cached username/display name;
- resource (`chat` or `channel`) when applicable;
- actor (`system`, `user`, `admin`, `provider`, `job`);
- admission method (`bot_link`, `external`, `admin`, `provider`,
  `job`);
- active access reasons from existing `AccessDecision.Reasons` or
  active `subscriptions`;
- invite mode and safe invite metadata;
- revocation or manual reason when present;
- event time.

When multiple sources currently grant access, the event MUST list all
active reasons available from existing data. If Telegram provides an
external actor for a membership update, the event MAY include a safe
display label for that actor; if actor data is absent, the event MUST
say that the admission was external without guessing who did it.

#### Scenario: Access grant lists active source reasons

- **WHEN** a user's live access decision has active Boosty and Tribute
  reasons
- **THEN** the operator access-granted event lists both `boosty` and
  `tribute`
- **AND** it does not collapse the reason to only one source

#### Scenario: Fallback grant uses active subscriptions

- **WHEN** admission proceeds because live status is `unknown` but a
  fresh active subscription fallback exists
- **THEN** the operator event marks the reason as fallback-based
- **AND** it lists the active subscription platform(s) available in the
  database

#### Scenario: External join keeps unknown actor explicit

- **WHEN** a user joins a club resource without bot admission evidence
  and Telegram does not expose an actor
- **THEN** the operator event uses admission method `external`
- **AND** it does not claim that a specific admin added the user

### Requirement: Operator event log protects secrets and unsafe details

Operator event messages MUST NOT include full invite URLs, bot tokens,
provider secrets, raw Telegram update JSON, raw Tribute webhook body,
email, internal outbox IDs, raw idempotency keys or internal chat IDs.
Invite-related context MUST be limited to safe fields such as resource,
invite mode and `invite_link_hash` when useful.

Dynamic text in operator event messages MUST be escaped through the
Telegram HTML renderer before delivery. Unsafe strings from usernames,
manual reasons, audit details, alert details or provider names MUST NOT
be concatenated into HTML directly.

#### Scenario: Invite URL is redacted from event

- **WHEN** a join-request event contains a full Telegram invite URL
- **THEN** the operator event message omits the full URL
- **AND** it may include invite mode, resource and invite hash

#### Scenario: Dynamic reason cannot inject markup

- **WHEN** an admin reason contains `<`, `>`, `&` or Telegram HTML
  markup
- **THEN** the rendered operator event remains valid HTML
- **AND** the reason is displayed as text, not interpreted as markup

### Requirement: Operator event kinds cover access and membership lifecycle

The operator event log MUST support the following first-version event
kinds:

- `access_granted`: the user's effective eligibility transitioned to
  active (resource admission and actual entry are reported by membership
  events, not by this kind);
- `access_loss_scheduled`: access loss was scheduled by a grace-period
  revocation while access is still present;
- `access_lost`: effective access or resource access was revoked;
- `access_kept`: a pending revocation was cancelled because access
  became active again;
- `club_chat_joined`;
- `club_chat_left`;
- `club_channel_subscribed`;
- `club_channel_unsubscribed`;
- `source_subscription_activated`;
- `source_subscription_expired`;
- `manual_grant`;
- `manual_revoke`;
- `manual_ban`;
- `manual_unban`;
- `banned_join_attempt`: a hard-banned user attempted to enter a managed
  resource; the attempt was detected and a hard-ban removal was enqueued
  (the actual removal is asynchronous via the outbox).

This first-version scope MUST cover the events named by the owner and
the adjacent access cycle needed to explain them: source activation and
expiry, grace scheduling, actual revocation and cancellation. It MUST
NOT include unrelated technical health, metrics or deploy events beyond
existing ops-alert delivery.

For `chat` resources, rendered labels MUST use club-chat wording
(`joined`/`left`). For `channel` resources, rendered labels MUST use
club-channel wording (`subscribed`/`unsubscribed`). Access lifecycle
events MUST remain distinct from membership events: an access event
describes eligibility or domain permission, while a membership event
describes observed Telegram membership in a specific resource.

#### Scenario: Chat and channel membership use different labels

- **WHEN** the same user joins the club chat and the club channel
- **THEN** the chat event is rendered as a club chat join
- **AND** the channel event is rendered as a club channel subscription

#### Scenario: Access loss is separate from member left

- **WHEN** a bot revocation removes a user from a resource
- **THEN** an `access_lost` event describes the revoked access and reason
- **AND** a later Telegram membership update can still emit a resource
  leave/unsubscribe event without changing the access-loss reason

#### Scenario: Grace produces scheduled and final loss events

- **WHEN** a user's last active source expires in grace mode and the due
  revocation later executes
- **THEN** the event log first receives `access_loss_scheduled`
- **AND** it later receives `access_lost` when access is actually
  revoked

#### Scenario: Banned user join attempt is logged

- **WHEN** a hard-banned user joins or is added to a managed resource and
  the bot enqueues a hard-ban removal
- **THEN** a `banned_join_attempt` operator event is enqueued
- **AND** it records the resource and that a hard-ban removal was
  enqueued (not that removal already completed)

### Requirement: Event emission separates feed-build errors from persistence errors

Two distinct failure classes MUST be handled differently so the
"atomic" and "never blocks access-control" goals do not contradict:

- **Feed-build errors** — rendering, a missing template, or any
  feed-specific construction problem that happens *before* the event row
  is persisted. These MUST be logged and the event MUST be skipped; they
  MUST NOT roll back or abort the domain change, revocation, ban, audit
  write or user-facing reply that caused them. The feed is observability
  and MUST NOT gate access-control behavior.
- **Persistence errors** — a failure to INSERT the durable
  `access_actions` row itself. The enqueue of a successfully built event
  MUST share the same transaction as the domain change and audit row, so
  a successfully built event is atomic and exactly-once with that commit.
  A persistence failure is a database failure and MUST follow the normal
  transaction rules (the whole transaction fails), exactly as any other
  durable write in that transaction would.

In short: a built event is persisted atomically with the domain change;
an event that cannot be built is dropped without harming the domain
change. The system MUST NOT promise exactly-once delivery for an event
it failed to build.

#### Scenario: Build/render failure does not roll back the domain change

- **WHEN** an operator event cannot be built or rendered for an otherwise
  valid revocation
- **THEN** the grant revocation, audit row and removal actions still
  commit
- **AND** the event is skipped and the failure is logged

#### Scenario: Built event is atomic with the domain change

- **WHEN** an operator event is built for a committing domain change
- **THEN** its `access_actions` row is written in the same transaction
- **AND** a failure to persist that row fails the transaction like any
  other durable write

#### Scenario: Feed build problem does not block admission

- **WHEN** the operator event for an access grant cannot be built
- **THEN** the admission decision and user-facing reply proceed normally
- **AND** the failure is logged without aborting the request

### Requirement: Event feed is forward-only

The operator event log MUST be forward-only: events are emitted at the
moment the causing workflow runs. The system MUST NOT replay or backfill
historical `audit_log` rows or prior domain state into the feed when the
feature is enabled or after a restart. Recovery of unsent events relies
only on durable `access_actions` already enqueued before the restart.

#### Scenario: No historical backfill on enable

- **WHEN** the event feed is configured and the bot starts with existing
  `audit_log` history
- **THEN** past events are not resent to the event-log chat
- **AND** only newly caused events produce feed messages
