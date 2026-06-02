## MODIFIED Requirements

### Requirement: The telegram package exposes one concrete Client over the Bot API

Пакет `telegram` MUST экспортировать **один конкретный** тип `*Client` —
обёртку над `github.com/go-telegram/bot` — и не объявляет собственных
интерфейсов (§20.2). `Client` предоставляет методы `getMe`, `getChat`,
`getChatMember`, `sendMessage`, `setMyCommands`, `setWebhook`,
`deleteWebhook`, а также методы Bot API, нужные Enforcer'у:
`createChatInviteLink`, `revokeChatInviteLink`,
`approveChatJoinRequest`, `declineChatJoinRequest`, `banChatMember` и
`unbanChatMember`. Узкие интерфейсы объявляют пакеты-потребители у себя.

#### Scenario: Поверхность конкретного клиента
- **WHEN** потребитель использует пакет `telegram`
- **THEN** ему доступен конкретный `*Client` с методами `getMe`,
  `getChat`, `getChatMember`, `sendMessage`, `setMyCommands`,
  `setWebhook`, `deleteWebhook`, `createChatInviteLink`,
  `revokeChatInviteLink`, `approveChatJoinRequest`,
  `declineChatJoinRequest`, `banChatMember` и `unbanChatMember`
- **AND** пакет `telegram` не экспортирует интерфейсов для этих методов

### Requirement: The router dispatches updates by type and chat id

Роутер MUST маршрутизировать каждое обновление по типу и `chat.id`.
Реально обрабатываются: `message` в личке -> хендлеры команд бота
(`/start` и некомандный DM запускают grant-access flow); `/here` в
группе или супергруппе от владельца -> ответ с `chat.id` (в каналах
недоступна: нет `channel_post` в `allowed_updates`);
`my_chat_member` -> chat-health; `chat_member` в Boosty source chat ->
нормализация в `SubscriptionEvent` и `engine.handleEvent` внутри `tx2`;
`chat_member` в Tribute source chat -> нормализация в
`SubscriptionEvent` только при `TRIBUTE_MODE=observation`; при
`TRIBUTE_MODE=webhook` membership Tribute-канала MUST NOT истекать
ledger-подписку и MAY обновлять диагностический/audit сигнал как
secondary verification; `chat_join_request` в club chat или club channel
-> admission join-request handler; `chat_member` в club chat или club
channel -> club membership handler. Прочее завершается как `ignored`.

Неоднозначное пересечение source chat id и club resource id отклоняется
на уровне runtime/config до запуска входящего transport, поэтому роутер
получает уже однозначную конфигурацию и не выбирает между двумя
доменными обработчиками для одного update.

#### Scenario: Private message маршрутизируется в bot command handlers
- **WHEN** приходит `message` из приватного чата
- **THEN** оно направляется в хендлеры команд бота

#### Scenario: my_chat_member маршрутизируется в chat-health
- **WHEN** приходит `my_chat_member`
- **THEN** оно направляется в обработку chat-health

#### Scenario: Boosty source chat_member маршрутизируется в движок
- **WHEN** приходит `chat_member` с `chat.id`, равным
  `BOOSTY_GROUP_ID`
- **THEN** оно нормализуется в `SubscriptionEvent` и применяется через
  `engine.handleEvent`

#### Scenario: Tribute observation source chat_member маршрутизируется в движок
- **WHEN** приходит `chat_member` с `chat.id`, равным
  `TRIBUTE_CHANNEL_ID`, и `TRIBUTE_MODE=observation`
- **THEN** оно нормализуется в `SubscriptionEvent` и применяется через
  `engine.handleEvent`

#### Scenario: Tribute webhook mode membership не истекает ledger
- **WHEN** приходит `chat_member` выхода из `TRIBUTE_CHANNEL_ID`, а
  `TRIBUTE_MODE=webhook`
- **THEN** роутер не вызывает domain path, который переводит активную
  Tribute ledger subscription в `expired`
- **AND** доступ продолжает определяться webhook ledger и общим status
  aggregation

#### Scenario: chat_join_request клубного ресурса маршрутизируется в admission
- **WHEN** приходит `chat_join_request` с `chat.id`, равным club chat
  или club channel
- **THEN** оно направляется в grant-access join-request handler

#### Scenario: chat_member клубного ресурса обновляет grants
- **WHEN** приходит `chat_member` с `chat.id`, равным club chat или
  club channel
- **THEN** оно направляется в club membership handler

#### Scenario: Прочие membership-updates игнорируются
- **WHEN** приходит `chat_member` или `chat_join_request` в чате, не
  являющемся настроенным источником или club resource
- **THEN** строка завершается статусом `ignored`

## ADDED Requirements

### Requirement: Telegram webhook intake reuses the durable update pipeline

When `TELEGRAM_MODE=webhook`, `POST {TELEGRAM_WEBHOOK_PATH}` MUST accept
Telegram updates instead of long polling. The handler MUST verify header
`X-Telegram-Bot-Api-Secret-Token` against `TELEGRAM_WEBHOOK_SECRET`
before parsing the body. Missing or invalid secret MUST return `401` and
MUST NOT write `telegram_updates`.

Valid webhook updates MUST be persisted to `telegram_updates` and routed
through the same terminal state machine as polling updates:
`pending -> processed | ignored | failed`. Domain changes, audit rows,
outbox actions and terminal update status MUST still commit atomically
inside handler transactions. Duplicate `update_id` MUST be a no-op.

#### Scenario: Invalid Telegram webhook secret is rejected
- **WHEN** Telegram webhook request lacks the expected
  `X-Telegram-Bot-Api-Secret-Token`
- **THEN** handler returns `401`
- **AND** no `telegram_updates` row is written

#### Scenario: Valid Telegram webhook reaches router
- **WHEN** Telegram sends a valid webhook update with correct secret
- **THEN** the update is inserted into `telegram_updates`
- **AND** the existing router processes it according to update type and
  chat id

#### Scenario: Duplicate webhook update is idempotent
- **WHEN** the same Telegram webhook `update_id` is delivered twice
- **THEN** the second delivery does not create another inbox row
- **AND** domain side effects are not repeated

#### Scenario: Webhook handler uses terminal state machine
- **WHEN** a valid Telegram webhook update handler succeeds
- **THEN** its `telegram_updates` row reaches `processed` or `ignored`
- **AND** if the handler fails, the row reaches `failed` with an
  `admin_alert(severity='error')`

### Requirement: Telegram webhook registration is explicit in runtime

When `TELEGRAM_MODE=webhook`, runtime MUST register the bot webhook
using `TELEGRAM_WEBHOOK_PUBLIC_URL + TELEGRAM_WEBHOOK_PATH`, the
configured secret token and the same explicit `allowed_updates` list as
polling. When `TELEGRAM_MODE=polling`, runtime MUST ensure webhook
delivery is disabled or not configured so `getUpdates` can work.

Registration failure in webhook mode MUST be fatal before the process
reports ready.

#### Scenario: Webhook mode registers public URL
- **WHEN** runtime starts with `TELEGRAM_MODE=webhook`
- **THEN** it calls Telegram Bot API to register the configured public
  URL, secret token and allowed updates
- **AND** readiness is not `200` until registration succeeds

#### Scenario: Polling mode does not receive webhook traffic
- **WHEN** runtime starts with `TELEGRAM_MODE=polling`
- **THEN** it does not rely on Telegram webhook delivery
- **AND** long polling can consume updates through `getUpdates`
