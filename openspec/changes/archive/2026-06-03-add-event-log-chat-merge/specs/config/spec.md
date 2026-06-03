## MODIFIED Requirements

### Requirement: Required keys must be present

Loading MUST fail when any required key is absent or empty. The
required keys are: `BOT_TOKEN`, `OWNER_TG_IDS`, `BOOSTY_GROUP_ID`,
`TRIBUTE_CHANNEL_ID`, `CLUB_CHAT_ID`, `CLUB_CHANNEL_ID`,
`EVENT_LOG_CHAT_ID`, `BOOSTY_SUBSCRIBE_URL` and
`TRIBUTE_SUBSCRIBE_URL`.

#### Scenario: Required key missing

- **WHEN** any required key is absent or empty
- **THEN** loading fails with an error listing every missing key

## ADDED Requirements

### Requirement: EVENT_LOG_CHAT_ID targets the operator event feed

Configuration MUST support `EVENT_LOG_CHAT_ID` as a Telegram chat id for
the operator event feed. `EVENT_LOG_CHAT_ID` MUST parse as a negative
`int64` chat id and MUST represent a dedicated Telegram group or
supergroup where the bot and the administrator are members.

`EVENT_LOG_CHAT_ID` MUST be distinct from `ADMIN_LOG_CHAT_ID` and from
the four configured source/club chat IDs: `BOOSTY_GROUP_ID`,
`TRIBUTE_CHANNEL_ID`, `CLUB_CHAT_ID` and `CLUB_CHANNEL_ID`. If it equals
any of those IDs, configuration MUST fail and name the collision.

`ADMIN_LOG_CHAT_ID` remains the target for ops alerts such as lost bot
rights, unsafe revoke and dead outbox actions. Operator event feed
messages MUST NOT be delivered through `ADMIN_LOG_CHAT_ID`.

#### Scenario: Event log chat is configured

- **WHEN** `EVENT_LOG_CHAT_ID=-1007777777777`
- **THEN** configuration loading succeeds
- **AND** operator event feed delivery targets that chat id

#### Scenario: Event log chat is required

- **WHEN** `EVENT_LOG_CHAT_ID` is unset or empty
- **THEN** configuration loading fails with
  `"EVENT_LOG_CHAT_ID is required"`

#### Scenario: Event log chat must be negative

- **WHEN** `EVENT_LOG_CHAT_ID=12345`
- **THEN** configuration loading fails with
  `"EVENT_LOG_CHAT_ID must be a negative chat ID"`

#### Scenario: Event log chat must not collide with admin alert chat

- **WHEN** `EVENT_LOG_CHAT_ID` equals `ADMIN_LOG_CHAT_ID`
- **THEN** configuration loading fails naming the collision

#### Scenario: Event log chat must not collide with managed resources

- **WHEN** `EVENT_LOG_CHAT_ID` equals `CLUB_CHAT_ID`
- **THEN** configuration loading fails naming the collision

#### Scenario: Event log chat must not collide with observed sources

- **WHEN** `EVENT_LOG_CHAT_ID` equals `BOOSTY_GROUP_ID`
- **THEN** configuration loading fails naming the collision
