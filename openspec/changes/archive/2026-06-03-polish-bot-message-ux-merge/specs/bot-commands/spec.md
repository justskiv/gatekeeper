## MODIFIED Requirements

### Requirement: `/status` показывает пользователю его подписки и членство

`/status` MUST работать в личке и отвечать пользователю его текущим
статусом: активен ли доступ, какие активные источники подписки
известны пользователю, `expires_at`, если он известен, состояние
клубных чата и канала, и следующее действие. Как любое сообщение в
личку, команда MUST обеспечить строку пользователя (`ensureUser`) и
выставить `dm_state='open'` (пользователь нам написал — личка открыта).
Команда MUST NOT выдавать или отзывать доступ.

Живой вердикт источников (включая `unknown` при потере ботом прав) MUST
считаться **вне `handleTx`**, чтобы не нарушать инвариант I2. Текст MUST
строиться из пакета `messages` (`MSG_STATUS`; при отсутствии активной
подписки — финальный no-sub status text).

Ответ `/status` для обычного пользователя MUST NOT показывать
`AccessDecision.Reasons`, raw source verdicts, `whitelist`, `system`,
локальную БД, raw chat IDs или diagnostics. Эти детали доступны только
owner/admin через `/whois` или alerts.

#### Scenario: Пользователь видит активные подписки

- **WHEN** пользователь с активной подпиской отправляет `/status`
- **THEN** бот отвечает human-readable статусом доступа
- **AND** ответ показывает активные источники и (если известно) даты
  `expires_at`
- **AND** ответ показывает состояние клубных ресурсов без raw chat IDs

#### Scenario: Нет активных подписок

- **WHEN** `/status` отправляет пользователь без активных подписок
- **THEN** ответ сообщает, что активная подписка не найдена
- **AND** ответ даёт следующее действие для оформления или повторной
  проверки
- **AND** доступ не выдаётся

#### Scenario: Status открывает личку

- **WHEN** пользователь впервые пишет `/status`
- **THEN** строка `users` существует и `dm_state='open'`

#### Scenario: User status hides internal reasons

- **WHEN** `AccessDecision` содержит internal `Reasons`
- **THEN** `/status` не включает эти reasons в user-facing text
- **AND** ответ не содержит raw internal source names, raw `chat_id` or
  local storage wording

### Requirement: `/whois` объясняет владельцу статус пользователя

`/whois <tg_id|@username>` MUST быть доступна только идентификаторам из
`OWNER_TG_IDS` и возвращать карточку пользователя: профиль, подписки,
доступы, флаги whitelist/ban, `AccessDecision` с `Reasons` («почему есть
или нет доступ») и последние записи `audit_log`. Поиск по `@username`
MUST работать только если пользователь уже известен в БД
(`Users.FindByUsername`), без внешнего Telegram-lookup. Обращение
`/whois` не от владельца MUST игнорироваться так же, как прочий
не-командный текст.

Карточка MUST быть summary-first: сначала имя/username/TG ID, общий
статус доступа, источник или основание доступа, сроки и состояние
клубных ресурсов; затем последние события; затем отдельный diagnostics
block с raw reasons, provider/internal details и technical IDs. Raw
enum-like values MUST be translated to stable Russian labels in the
main summary.

#### Scenario: Владелец получает объяснимую карточку

- **WHEN** владелец отправляет `/whois <tg_id>` по известному
  пользователю
- **THEN** бот отвечает профилем, подписками, доступами и
  `AccessDecision` с `Reasons`
- **AND** human summary appears before diagnostics

#### Scenario: Поиск по username вне БД

- **WHEN** владелец указывает `@username`, которого нет в БД
- **THEN** бот сообщает, что пользователь не найден, без обращения к
  сети

#### Scenario: /whois не от владельца игнорируется

- **WHEN** `/whois` отправляет идентификатор не из `OWNER_TG_IDS`
- **THEN** команда игнорируется как обычный текст, данные не
  раскрываются

#### Scenario: Whois diagnostics are grouped

- **WHEN** `/whois` includes raw reason details, audit detail or IDs
- **THEN** those values appear only after the main status summary
- **AND** the owner can still see enough diagnostics to debug access

### Requirement: Owner ops commands expose runtime state without side effects

Owner commands `/stats`, `/alerts`, `/chats` and `/help_admin` MUST work
only for `OWNER_TG_IDS`. Requests from non-owners MUST be ignored like
other non-owner admin commands and MUST NOT disclose operational data.
The commands MUST NOT change subscriptions, grants, revocations or
outbox state except for durable delivery of their own response.

`/stats` MUST summarize active subscriptions, club resource membership
or grant counts, pending revocations, health of the four configured
chats, last reconciliation time, outbox size and dead action count, and
open alert count. The response MUST be formatted as human summary first
and compact counters second, with raw diagnostics omitted unless useful.

`/alerts` MUST list open `admin_alerts` with id, severity, kind, title,
created time and whether each is resolved/open. If there are no open
alerts, it MUST say so. Alerts MUST be grouped or sorted by severity and
MUST show problem and impact before raw kind/detail fields.

`/chats` MUST show known configured chat IDs and their roles: Boosty
group, Tribute channel, club chat, club channel and optional
`ADMIN_LOG_CHAT_ID`. The human role/status MUST appear before raw chat
ID.

`/help_admin` MUST list owner/admin commands and their short purpose,
grouped by task area where practical.

#### Scenario: Owner sees stats summary

- **WHEN** owner sends `/stats`
- **THEN** bot returns a durable message with subscription, grant,
  revocation, health, reconcile, outbox and alert summary
- **AND** the message starts with a readable state summary
- **AND** no domain access state changes

#### Scenario: Non-owner cannot read stats

- **WHEN** a non-owner sends `/stats`
- **THEN** command is ignored and operational data is not disclosed

#### Scenario: Alerts command lists open alerts

- **WHEN** owner sends `/alerts` and open alerts exist
- **THEN** bot returns their id, severity, kind, title and created time
- **AND** the problem summary appears before raw alert diagnostics

#### Scenario: Chats command shows configured roles

- **WHEN** owner sends `/chats`
- **THEN** bot returns configured chat IDs grouped by operational role
- **AND** chat IDs are not the leading content of each line

#### Scenario: Admin help lists admin commands

- **WHEN** owner sends `/help_admin`
- **THEN** bot returns the admin command list from `messages`

### Requirement: User and admin texts include final help entries

The `messages` package MUST include final user help and admin help text.
`/help` MUST describe the bot, subscription flow and the requirement to
write from the same Telegram account. It MUST list only user commands
and MUST NOT expose owner/admin commands, raw operational data or
internal implementation details.

`/help_admin` MUST describe owner commands without exposing secrets or
raw operational data. It MUST be structured for scanning by task:
lookup, access management, reconciliation, runtime state and export.

#### Scenario: User help mentions same Telegram account

- **WHEN** user sends `/help`
- **THEN** response explains how to subscribe and says to write from the
  same Telegram account
- **AND** response lists only user commands

#### Scenario: Admin help is sourced from messages

- **WHEN** owner sends `/help_admin`
- **THEN** response text comes from `messages`, not inline handler
  literals
- **AND** owner commands are grouped by operational purpose

## ADDED Requirements

### Requirement: Owner responses stay within Telegram message limits without breaking formatting

Длинные владельческие ответы MUST укладываться в лимит сообщения
Telegram (`/whois`, `/export` и любой ответ, способный его превысить) —
через ограничение объёма (потолок строк) или разбиение. Разбиение MUST
происходить только по границам строк и MUST NOT разрывать HTML-тег или
сущность; форматирование MUST оставаться сбалансированным в каждом
фрагменте, поэтому теги держатся в пределах одной строки. CSV `/export`
отправляется как plain (без `parse_mode`), поэтому его построчное
разбиение безопасно.

#### Scenario: Длинный ответ разбивается без поломки тегов

- **WHEN** владельческий ответ превышает лимит сообщения
- **THEN** он разбивается по границам строк
- **AND** ни один фрагмент не содержит незакрытого тега или сущности

#### Scenario: CSV export отправляется без parse_mode

- **WHEN** владелец запрашивает `/export`
- **THEN** CSV-чанки отправляются как plain без `parse_mode`
