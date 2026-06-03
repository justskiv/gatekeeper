## MODIFIED Requirements

### Requirement: Enforcer executes all supported action types

Enforcer MUST быть единственной точкой доменных исходящих вызовов
Telegram. Он MUST читать `access_actions`, декодировать `payload_json`,
маппить `resource` на настроенный club chat или club channel и
исполнять action через узкие consumer-интерфейсы, объявленные в пакете
`enforcer`.

Поддержанные `action_type` MUST включать:
`ensure_invite`, `send_invite`, `approve_join`, `decline_join`,
`soft_kick`, `hard_ban`, `unban`, `send_dm`, `edit_message`,
`verify_member`, `revoke_invite`. Ожидаемые no-op ошибки в контексте
конкретного action MUST считаться успешным исполнением и логироваться на
уровне `warn`.

`send_dm` и `send_invite` MUST сохранять контракт форматированного
сообщения от durable payload до Telegram API call. Если payload содержит
HTML-текст, созданный renderer'ом, или reply markup, Enforcer MUST
отправить его с выбранным parse mode. Если payload явно plain text,
Enforcer MUST отправить его без parse mode. Payload без объявленного
parse mode (включая сообщения, поставленные в очередь до HTML cutover)
MUST трактоваться как plain и отправляться без parse mode; включение
HTML MUST NOT задним числом переинтерпретировать такие queued тексты как
HTML и вызывать `can't parse entities`, отравляя outbox постоянными
ретраями. Inline keyboard button labels и callback data MUST оставаться
plain reply-markup fields, а не HTML-rendered content.

Категоризация Telegram `403` для `send_dm` MUST зависеть от типа
таргета, определяемого по `chat_id`, а не по сравнению с `action.TGID`.
**Private-DM таргет** — payload без `chat_id` либо положительный
`chat_id` личного чата пользователя (включая `user_chat_id`, который
admission проставляет в grant-DM при одобрении join-request): для него
`403` MUST трактоваться как блокировка ботом в личке — action помечает
`users.dm_state='blocked'` по `action.TGID` и завершается без retry.
**Group/feed таргет** — отрицательный `chat_id` группы/супергруппы/
канала, в частности настроенные `EVENT_LOG_CHAT_ID` или
`ADMIN_LOG_CHAT_ID`: для него `403`/forbidden MUST NOT трактоваться как
блокировка субъектом — action MUST NOT помечать `users.dm_state='blocked'`
по `action.TGID` и MUST NOT считаться доставленным; сбой прав MUST идти
по permanent-failure пути (`status='dead'` и
`admin_alert(kind='outbox_action_dead')`), чтобы недоступность feed-чата
была видна оператору, а не терялась тихо.

`edit_message` MUST редактировать текст и inline-клавиатуру
существующего сообщения по `chat_id` и `message_id` из payload с тем же
HTML parse mode. Payload без `chat_id`, `message_id` или текста MUST
считаться невалидным. Если payload требует retry-кнопку, Enforcer MUST
выставить retry-клавиатуру; иначе он MUST очистить клавиатуру непустым
(non-nil) пустым inline-keyboard, чтобы Telegram не отклонил правку.
Ответ Telegram `message is not modified` MUST трактоваться как успех.

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
- **WHEN** `send_dm` без `chat_id` либо с положительным `chat_id`
  личного чата (включая `user_chat_id` grant-DM из join-request)
  получает Telegram `403`
- **THEN** пользователь помечается `dm_state='blocked'`
- **AND** action завершается без retry

#### Scenario: 403 в feed-чате не блокирует субъекта и поднимает dead alert
- **WHEN** `send_dm` с отрицательным `payload.chat_id` группы feed-чата
  (`EVENT_LOG_CHAT_ID` или `ADMIN_LOG_CHAT_ID`) получает Telegram `403`
  из-за отсутствия прав постинга
- **THEN** `action.TGID` НЕ помечается `dm_state='blocked'`
- **AND** action НЕ помечается `done` как доставленный
- **AND** сбой идёт по permanent-failure пути: `status='dead'` и
  `admin_alert(kind='outbox_action_dead')`

#### Scenario: Durable DM сохраняет parse mode

- **WHEN** Enforcer выполняет форматированный `send_dm` action
- **THEN** Telegram `sendMessage` получает текст сообщения с
  `parse_mode="HTML"`
- **AND** тот же parse mode сохраняется при отправке с inline reply
  markup

#### Scenario: Plain durable DM остаётся plain

- **WHEN** Enforcer выполняет явно plain `send_dm` action
- **THEN** Telegram `sendMessage` не получает parse mode
- **AND** raw CSV или diagnostic text не парсится как HTML

#### Scenario: Pre-cutover plain DM не переинтерпретируется как HTML

- **WHEN** `send_dm` payload не содержит объявленный parse mode
  (поставлен в очередь до HTML cutover)
- **THEN** Enforcer отправляет его без parse mode
- **AND** literal `<` в таком legacy text не вызывает Telegram-ошибку
  `can't parse entities`

#### Scenario: Invite message сохраняет форматирование

- **WHEN** Enforcer выполняет `send_invite` с форматированным текстом
- **THEN** Telegram получает текст с выбранным parse mode
- **AND** fallback raw invite URL остаётся отправляемым, если
  форматированный текст не передан

#### Scenario: edit_message редактирует существующее сообщение

- **WHEN** Enforcer выполняет `edit_message` action с `chat_id`,
  `message_id` и форматированным текстом
- **THEN** текст и inline-клавиатура сообщения редактируются на месте с
  `parse_mode="HTML"`
- **AND** не-retry результат очищает клавиатуру непустым пустым
  inline-keyboard, а ответ `message is not modified` трактуется как
  успех
