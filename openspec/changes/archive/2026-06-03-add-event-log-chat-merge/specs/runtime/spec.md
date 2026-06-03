## ADDED Requirements

### Requirement: Runtime wires operator event log before incoming updates

`cmd/gatekeeper` MUST construct an operator event log writer after
configuration, database and outbox dependencies are available, and
before poller or Telegram webhook transport starts. The writer MUST be
passed to admission/router dependencies, engine/revocation paths and
owner command handlers.

The writer MUST target `EVENT_LOG_CHAT_ID` through durable `send_dm`
payload `chat_id`. Runtime MUST NOT fall back to `ADMIN_LOG_CHAT_ID` or
owner DMs for operator event feed delivery. Runtime MUST NOT start with
a partially wired operator event log when the code path can emit events.

Startup MAY probe that `EVENT_LOG_CHAT_ID` resolves to a Telegram group
or supergroup and that the bot can post there. A failed probe MUST be a
non-fatal warning, not a startup error: the bot MUST start and serve
incoming updates regardless of event-feed reachability, because an
observability feed MUST NOT gate the access-control core. The bot's
ability to post to `EVENT_LOG_CHAT_ID` MUST instead be monitored by the
existing chat-health mechanism, which MUST raise an operator alert (the
same `bot_rights_lost`-style alert used for managed chats) when posting
rights are missing, and resolve it when restored.

#### Scenario: Writer is available before poller

- **WHEN** runtime reaches poller startup
- **THEN** admission router dependencies include an operator event log
  writer
- **AND** incoming membership updates can emit operator events durably

#### Scenario: Writer targets event log chat

- **WHEN** `EVENT_LOG_CHAT_ID` is configured
- **THEN** runtime constructs the writer with that chat id
- **AND** operator event actions use `payload_json.chat_id`

#### Scenario: Writer does not fall back to admin alerts

- **WHEN** `ADMIN_LOG_CHAT_ID` is configured
- **THEN** runtime still constructs the event writer with
  `EVENT_LOG_CHAT_ID`
- **AND** operator events are not delivered through the ops-alert chat

#### Scenario: Event log chat unreachable does not block startup

- **WHEN** `EVENT_LOG_CHAT_ID` points to a chat where the bot cannot
  post messages
- **THEN** startup still completes and incoming updates are served
- **AND** chat-health raises a `bot_rights_lost`-style operator alert
- **AND** no event feed messages are redirected to owner DMs or
  `ADMIN_LOG_CHAT_ID`

### Requirement: Operator event delivery shares outbox lifecycle

Operator event delivery MUST share the existing Enforcer lifecycle,
rate limits, retries and dead-action alerts. Runtime MUST NOT add a
second Telegram sender loop for operator events.

When operator event action delivery fails permanently, existing outbox
dead-action alert behavior MUST surface the failure to the owner. The
operator event writer MUST not bypass that behavior with best-effort
direct sends.

#### Scenario: Event delivery uses Enforcer workers

- **WHEN** an operator event action is queued
- **THEN** Enforcer workers execute it through the normal outbox path
- **AND** rate limits and retry policy are the same as other messages

#### Scenario: Permanent delivery failure creates alert

- **WHEN** an operator event message reaches max attempts
- **THEN** the action becomes `dead`
- **AND** existing `outbox_action_dead` alert behavior applies
