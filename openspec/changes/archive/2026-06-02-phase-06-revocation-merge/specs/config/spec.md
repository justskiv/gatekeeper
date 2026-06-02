## ADDED Requirements

### Requirement: ADMIN_LOG_CHAT_ID is an optional operator alert target

Configuration MUST support optional `ADMIN_LOG_CHAT_ID` as a Telegram
chat id for operator alerts. When set, it MUST parse as a negative
`int64` chat id. When absent or empty, operator alerts MUST fall back to
DM delivery for `OWNER_TG_IDS`.

`ADMIN_LOG_CHAT_ID` MUST NOT be required for startup and MUST NOT be
part of the four configured source/club chat IDs. If it equals one of
the four source/club IDs, configuration MUST fail to avoid leaking
operator alerts into managed or observed resources.

#### Scenario: Admin log chat absent
- **WHEN** `ADMIN_LOG_CHAT_ID` is unset or empty
- **THEN** configuration loading succeeds
- **AND** operator alerts use owner DM delivery

#### Scenario: Admin log chat is negative
- **WHEN** `ADMIN_LOG_CHAT_ID=-1005555555555`
- **THEN** configuration loading succeeds
- **AND** alert delivery can target that chat id

#### Scenario: Admin log chat must not collide with managed chats
- **WHEN** `ADMIN_LOG_CHAT_ID` equals `CLUB_CHAT_ID`
- **THEN** configuration loading fails naming the collision
