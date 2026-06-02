## MODIFIED Requirements

### Requirement: setMyCommands registers user and admin command scopes

При старте бот MUST регистрировать меню команд через `setMyCommands`:
пользовательские команды — глобально, админские — scoped на
`OWNER_TG_IDS`. Пользовательские команды MUST включать `/start`,
`/help` и `/status`. Admin scope MUST включать `/here`, `/whois`,
`/grant`, `/revoke`, `/ban`, `/unban`, `/sync`, `/stats`, `/alerts`,
`/export`, `/chats` и `/help_admin`. Ошибка `setMyCommands` —
**best-effort, не фатальна**: логируется на уровне `warn` и не
прерывает старт (меню — лишь подсказка, сами команды работают и без
него).

#### Scenario: Command scopes регистрируются при старте
- **WHEN** бот стартует
- **THEN** пользовательские команды зарегистрированы глобально
- **AND** админские команды зарегистрированы scoped на `OWNER_TG_IDS`

#### Scenario: Ops commands входят в admin scope
- **WHEN** бот регистрирует admin command scope
- **THEN** `/stats`, `/alerts`, `/export`, `/chats` и `/help_admin`
  входят в список admin commands

#### Scenario: Ошибка регистрации команд не прерывает старт
- **WHEN** `setMyCommands` возвращает ошибку при старте
- **THEN** ошибка логируется на уровне `warn` и старт продолжается

## ADDED Requirements

### Requirement: Owner ops commands expose runtime state without side effects

Owner commands `/stats`, `/alerts`, `/chats` and `/help_admin` MUST work
only for `OWNER_TG_IDS`. Requests from non-owners MUST be ignored like
other non-owner admin commands and MUST NOT disclose operational data.
The commands MUST NOT change subscriptions, grants, revocations or
outbox state except for durable delivery of their own response.

`/stats` MUST summarize active subscriptions, club resource membership
or grant counts, pending revocations, health of the four configured
chats, last reconciliation time, outbox size and dead action count, and
open alert count.

`/alerts` MUST list open `admin_alerts` with id, severity, kind, title,
created time and whether each is resolved/open. If there are no open
alerts, it MUST say so.

`/chats` MUST show known configured chat IDs and their roles: Boosty
group, Tribute channel, club chat, club channel and optional
`ADMIN_LOG_CHAT_ID`.

`/help_admin` MUST list owner/admin commands and their short purpose.

#### Scenario: Owner sees stats summary
- **WHEN** owner sends `/stats`
- **THEN** bot returns a durable message with subscription, grant,
  revocation, health, reconcile, outbox and alert summary
- **AND** no domain access state changes

#### Scenario: Non-owner cannot read stats
- **WHEN** a non-owner sends `/stats`
- **THEN** command is ignored and operational data is not disclosed

#### Scenario: Alerts command lists open alerts
- **WHEN** owner sends `/alerts` and open alerts exist
- **THEN** bot returns their id, severity, kind, title and created time

#### Scenario: Chats command shows configured roles
- **WHEN** owner sends `/chats`
- **THEN** bot returns configured chat IDs grouped by operational role

#### Scenario: Admin help lists admin commands
- **WHEN** owner sends `/help_admin`
- **THEN** bot returns the admin command list from `messages`

### Requirement: Owner export command sends CSV with users and subscriptions

`/export` MUST work only for `OWNER_TG_IDS` and only in a private chat.
It MUST produce CSV containing users and current subscription state,
including `tg_id`, cached username/profile fields, active subscription
platforms, `expires_at` when known, whitelist/ban flags and current
grant states. The export MUST NOT include raw `telegram_updates` or
`tribute_events` payloads, provider secrets, email, full invite URLs or
bot tokens.

The CSV MUST be delivered through durable Telegram delivery. If the
Telegram client path cannot send documents yet, implementation MAY send
a text CSV or split output safely, but the command MUST remain
owner-only and private-chat-only.

#### Scenario: Owner receives CSV export
- **WHEN** owner sends `/export` in a private chat
- **THEN** bot delivers CSV with users and current subscriptions
- **AND** raw inbox payloads and secrets are absent

#### Scenario: Export is not available in group chats
- **WHEN** owner sends `/export` in a group or supergroup
- **THEN** bot does not publish user data in that chat

#### Scenario: Non-owner cannot export
- **WHEN** a non-owner sends `/export`
- **THEN** command is ignored and no CSV is produced

### Requirement: User and admin texts include final help entries

The `messages` package MUST include final user help and admin help text.
`/help` MUST describe the bot, subscription links and the requirement to
write from the same Telegram account. `/help_admin` MUST describe owner
commands without exposing secrets or raw operational data.

#### Scenario: User help mentions same Telegram account
- **WHEN** user sends `/help`
- **THEN** response explains how to subscribe and says to write from the
  same Telegram account

#### Scenario: Admin help is sourced from messages
- **WHEN** owner sends `/help_admin`
- **THEN** response text comes from `messages`, not inline handler
  literals
