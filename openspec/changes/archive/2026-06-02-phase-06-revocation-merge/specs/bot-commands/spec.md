## MODIFIED Requirements

### Requirement: User-facing texts come from the messages package

Все пользовательские тексты (русский, §15.4) MUST жить в пакете
`messages`; inline-литералов пользовательских сообщений в коде нет
(§20.3) — это упрощает будущую локализацию. Admission-сообщения
`MSG_ACTIVE`, `MSG_INVITE_SOON`, `MSG_GRANTED`, `MSG_TRY_LATER`,
`MSG_BANNED`, `MSG_ALREADY_IN` и вариант `MSG_ACTIVE` для `direct` без
обещания approve-заявки MUST жить в `messages`. Текст
`MSG_ACCESS_KEPT` MUST браться из `messages`, когда его готовит
`recomputeAccess`.

Revocation-сообщения `MSG_EXPIRY_WARNING`, `MSG_EXPIRED_NOTICE` и
`MSG_REVOKED` MUST жить в `messages` и использоваться только через
durable delivery. Owner/admin тексты для подтверждения `/grant`,
`/revoke`, `/ban`, `/unban`, `/sync`, результата `/sync` и operator
alerts MUST также жить в `messages`.

#### Scenario: Сообщения берутся из пакета, а не инлайнятся
- **WHEN** бот отправляет пользовательское сообщение
- **THEN** текст берётся из пакета `messages`, а не из строкового
  литерала в месте вызова

#### Scenario: MSG_STATUS доступен в пакете
- **WHEN** `/status` формирует ответ
- **THEN** он использует `MSG_STATUS` из пакета `messages`

#### Scenario: Admission messages доступны в messages
- **WHEN** grant-access flow формирует ответ пользователю
- **THEN** он использует admission-текст из пакета `messages`

#### Scenario: Revocation messages доступны в messages
- **WHEN** access-revocation flow предупреждает, отменяет или исполняет
  отзыв
- **THEN** он использует `MSG_EXPIRY_WARNING`, `MSG_EXPIRED_NOTICE`,
  `MSG_ACCESS_KEPT` или `MSG_REVOKED` из пакета `messages`

## ADDED Requirements

### Requirement: Owner access commands manage manual access and bans

Owner commands `/grant`, `/revoke`, `/ban`, `/unban` and `/sync` MUST
работать только в личке для `OWNER_TG_IDS`. Запросы от не-owner MUST
игнорироваться как обычный некомандный текст и MUST NOT раскрывать
данные пользователя.

`/grant <tg_id> [срок] [причина]` MUST создавать manual access: без
срока — whitelist entry, со сроком — `manual` subscription с
`expires_at`. Команда по числовому `tg_id` MUST создавать stub
`users` row, если пользователя ещё нет; lookup по `@username` MUST
работать только по локальной БД.

`/revoke <tg_id> [причина]` MUST снять whitelist/manual access,
записать audit и вызвать `recomputeAccess`; если других active sources
нет, отзыв MUST пойти через configured `EXPIRY_MODE`. `/ban <tg_id>`
MUST выставить `users.banned=1` и немедленно запустить hard-ban
revocation. `/unban <tg_id>` MUST снять hard-ban; доступ после unban
возвращается только при active status и обычном запросе доступа.

`/sync [tg_id]` MUST запускать reconciliation после owner confirmation:
для одного пользователя, если аргумент указан, или полный pass, если
аргумента нет.

#### Scenario: Grant создаёт stub user по числовому tg_id
- **WHEN** owner выполняет `/grant 12345` для неизвестного пользователя
- **THEN** создаётся stub `users` row с `tg_id=12345`
- **AND** добавляется whitelist или manual subscription
- **AND** вызывается `recomputeAccess`

#### Scenario: Revoke запускает configured revocation
- **WHEN** owner выполняет `/revoke <tg_id>` и других active sources нет
- **THEN** manual access снимается
- **AND** `recomputeAccess` применяет `EXPIRY_MODE`

#### Scenario: Ban перекрывает active subscription
- **WHEN** owner подтверждает `/ban <tg_id>` для active пользователя
- **THEN** `users.banned` становится `1`
- **AND** hard-ban revocation ставится через outbox

#### Scenario: Sync одного пользователя не запускает полный pass
- **WHEN** owner выполняет `/sync <tg_id>`
- **THEN** Reconciler проверяет только указанного пользователя и
  связанные resources
- **AND** full reconciliation pass не запускается

### Requirement: Owner action commands require inline confirmation

Commands `/grant`, `/revoke`, `/ban`, `/unban` and `/sync` MUST NOT
применять изменение или запускать reconciliation сразу после текстовой
команды. Bot MUST показать owner'у краткое summary будущего действия и
inline-кнопки confirm/cancel. Confirmation action id MUST быть
короткоживущим, привязанным к owner id, command kind, optional target
tg_id и normalized arguments.

Повторный callback MUST быть идемпотентен. Expired, mismatched или уже
исполненный confirmation MUST NOT повторять side effects и MUST
сообщать owner'у terminal result.

#### Scenario: Ban требует подтверждения
- **WHEN** owner отправляет `/ban 12345 abuse`
- **THEN** `users.banned` не меняется до confirm callback
- **AND** owner получает inline confirmation summary

#### Scenario: Sync требует подтверждения
- **WHEN** owner отправляет `/sync`
- **THEN** full reconciliation pass не запускается до confirm callback
- **AND** owner получает inline confirmation summary

#### Scenario: Повторный confirm не дублирует действие
- **WHEN** owner дважды нажимает confirm для одного action id
- **THEN** side effects выполняются один раз
- **AND** второй callback получает terminal result без новых outbox rows

#### Scenario: Истёкшее подтверждение не исполняется
- **WHEN** owner нажимает confirm после expiry action id
- **THEN** command side effects не выполняются
- **AND** owner получает сообщение, что действие устарело

### Requirement: Admin alerts are delivered durably to operators

Каждое создание `admin_alert` MUST приводить к durable operator
delivery: `send_dm` владельцам из `OWNER_TG_IDS` или message в
`ADMIN_LOG_CHAT_ID`, если он настроен. Delivery MUST ставиться в outbox
в той же transaction, где создаётся alert, либо быть идемпотентно
восстановимой по alert id после рестарта.

Alert delivery MUST respect idempotency: повторное создание той же
открытой тревоги по stable dedupe key MUST NOT спамить владельца
дубликатами. Если владелец заблокировал личку, `send_dm` failure MUST
помечать `dm_state='blocked'`, но alert row MUST оставаться открытой.

#### Scenario: Critical alert уходит без ручного /alerts
- **WHEN** система создаёт `admin_alert(severity='critical')`
- **THEN** в outbox появляется operator delivery action
- **AND** owner не должен вызывать `/alerts`, чтобы узнать о тревоге

#### Scenario: ADMIN_LOG_CHAT_ID получает alert вместо лички
- **WHEN** `ADMIN_LOG_CHAT_ID` настроен
- **THEN** alert delivery адресуется в этот chat id
- **AND** личные DM владельцам не обязательны для этого alert

#### Scenario: Duplicate alert не спамит owner'а
- **WHEN** та же открытая тревога создаётся повторно по dedupe key
- **THEN** новая alert row не создаётся или связывается с прежней
- **AND** duplicate delivery action не ставится
