## MODIFIED Requirements

### Requirement: Enforcer executes all supported action types

Enforcer MUST быть единственной точкой доменных исходящих вызовов
Telegram. Он MUST читать `access_actions`, декодировать `payload_json`,
маппить `resource` на настроенный club chat или club channel и
исполнять action через узкие consumer-интерфейсы, объявленные в пакете
`enforcer`.

Поддержанные `action_type` MUST включать:
`ensure_invite`, `send_invite`, `approve_join`, `decline_join`,
`soft_kick`, `hard_ban`, `unban`, `send_dm`, `verify_member`,
`revoke_invite`. Ожидаемые no-op ошибки в контексте конкретного action
MUST считаться успешным исполнением и логироваться на уровне `warn`.

`send_dm` and `send_invite` MUST preserve the formatted message
contract from durable payload to Telegram API call. If a payload carries
renderer-produced HTML text or reply markup, Enforcer MUST send it with
the chosen parse mode. If a payload is explicitly plain text, Enforcer
MUST send it without parse mode. A payload without a declared parse mode
(including messages queued before the HTML cutover) MUST be treated as
plain and sent without parse mode; enabling HTML MUST NOT retroactively
reinterpret such queued text as HTML and trigger `can't parse entities`
failures that would poison the outbox with permanent retries. Inline
keyboard button labels and callback data MUST remain plain reply-markup
fields, not HTML-rendered content.

#### Scenario: soft_kick выполняет ban и unban

- **WHEN** Enforcer исполняет `soft_kick` для resource и пользователя
- **THEN** он проверяет, что пользователь не creator/admin
- **AND** вызывает `banChatMember`
- **AND** затем вызывает `unbanChatMember` с `only_if_banned=true`

#### Scenario: Creator или admin не кикается soft_kick

- **WHEN** `soft_kick` нацелен на creator или administrator ресурса
- **THEN** Enforcer не вызывает `banChatMember`
- **AND** action завершается как expected no-op с warning

#### Scenario: Ожидаемый no-op завершает action успешно

- **WHEN** `approve_join`, `decline_join`, `soft_kick` или
  `revoke_invite` получает Telegram-ошибку, означающую уже выполненное
  или уже невозможное действие
- **THEN** action помечается как `done`
- **AND** событие логируется как warning, а не ретраится

#### Scenario: Закрытая личка не ретраится

- **WHEN** `send_dm` получает Telegram `403`
- **THEN** пользователь помечается `dm_state='blocked'`
- **AND** action завершается без retry

#### Scenario: Durable DM preserves parse mode

- **WHEN** Enforcer executes a formatted `send_dm` action
- **THEN** Telegram `sendMessage` receives the message text with
  `parse_mode="HTML"`
- **AND** the same parse mode is preserved when inline reply markup is
  attached

#### Scenario: Plain durable DM stays plain

- **WHEN** Enforcer executes an explicitly plain `send_dm` action
- **THEN** Telegram `sendMessage` receives no parse mode
- **AND** raw CSV or diagnostic text is not parsed as HTML

#### Scenario: Pre-cutover plain DM is not reinterpreted as HTML

- **WHEN** a `send_dm` payload has no declared parse mode (queued before
  the HTML cutover)
- **THEN** Enforcer sends it without parse mode
- **AND** a literal `<` in that legacy text does not cause a Telegram
  `can't parse entities` failure

#### Scenario: Invite message preserves formatting

- **WHEN** Enforcer executes `send_invite` with formatted text
- **THEN** Telegram receives the text with the chosen parse mode
- **AND** a fallback raw invite URL remains sendable when no formatted
  text is provided
