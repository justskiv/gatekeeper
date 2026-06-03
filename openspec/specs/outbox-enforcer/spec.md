# outbox-enforcer Specification

## Purpose
TBD - created by archiving change phase-04-outbox-enforcer-merge. Update Purpose after archive.
## Requirements
### Requirement: Decisions and execution are separated by durable outbox

Доменный код MUST записывать исходящее Telegram-действие в
`access_actions` до исполнения. Если действие связано с изменением
durable состояния, строка action MUST коммититься в той же
handler-транзакции (`handleTx`), что и доменное изменение, `audit_log` и
terminal status входящего update. Telegram-вызов MUST выполняться
асинхронно Enforcer'ом после коммита.

Транзакция БД MUST NOT удерживаться открытой во время сетевого вызова
Telegram: сетевые probe выполняются до `handleTx`, durable action пишется
внутри `handleTx`, фактический side effect выполняется после коммита.

#### Scenario: Action и доменное изменение коммитятся атомарно
- **WHEN** обработчик меняет доменное состояние и должен выполнить
  исходящее Telegram-действие
- **THEN** доменное изменение и строка `access_actions` пишутся в одной
  SQL-транзакции
- **AND** при откате транзакции не сохраняется ни доменное изменение,
  ни строка action

#### Scenario: Telegram-вызов не выполняется внутри handleTx
- **WHEN** обработка требует Telegram side effect
- **THEN** внутри `handleTx` создаётся durable action
- **AND** фактический вызов Telegram выполняет Enforcer после коммита

### Requirement: Actions are idempotent and leased by workers

`access_actions.idempotency_key` MUST быть обязательным и уникальным.
Повторная постановка того же смыслового действия MUST NOT создавать
вторую строку. Готовыми к работе считаются строки
`status='queued' AND run_after<=now` и зависшие строки
`status='running' AND locked_until<now`.

Воркер MUST брать действие короткой lease-транзакцией: атомарно
перевести строку в `running`, выставить новый `locked_until` и
закоммитить до сетевого вызова. Зависшие `running` действия MUST
подхватываться заново после истечения lease.

#### Scenario: Дубль idempotency_key не создаёт второй строки
- **WHEN** action с уже существующим `idempotency_key` ставится
  повторно
- **THEN** в `access_actions` остаётся одна строка для этого ключа
- **AND** caller получает существующее действие или явный no-op без
  ошибки

#### Scenario: Готовое действие лизится одним воркером
- **WHEN** есть `queued` action с `run_after<=now`
- **THEN** один воркер переводит его в `running` и выставляет
  `locked_until`
- **AND** второй воркер не получает это же действие до истечения lease

#### Scenario: Зависшее running подхватывается по locked_until
- **WHEN** action остался в `running` с просроченным `locked_until`
- **THEN** следующий цикл воркера может взять его новым lease
- **AND** падение воркера не теряет действие навсегда

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

### Requirement: Enforcer retries, throttles and raises dead-action alerts

Enforcer MUST применять общий rate limiter перед Telegram-вызовами:
примерно один message-вызов в секунду на chat, общий потолок заметно
ниже 30 запросов в секунду и консервативный лимит для `getChatMember`
около 1-2 запросов в секунду. Telegram `429` MUST перепланировать
action на `now + retry_after`. Прочие retryable ошибки MUST
перепланироваться с backoff и jitter.

Если число попыток достигает `max_attempts`, action MUST перейти в
`dead`, а система MUST создать `admin_alert` с kind
`outbox_action_dead`. Воркер MUST NOT держать открытую DB-транзакцию
во время ожидания rate limiter или сетевого ответа Telegram.

#### Scenario: Telegram 429 планирует retry_after
- **WHEN** Telegram отвечает `429` с `retry_after=17`
- **THEN** action возвращается в очередь с `run_after=now+17s`
- **AND** процесс Enforcer не падает

#### Scenario: Исчерпание попыток создаёт dead alert
- **WHEN** retryable action достигает `max_attempts`
- **THEN** строка получает `status='dead'`
- **AND** создаётся `admin_alert(kind='outbox_action_dead')`

#### Scenario: Сеть выполняется вне DB-транзакции
- **WHEN** Enforcer вызывает Telegram API
- **THEN** lease-транзакция уже закоммичена
- **AND** финальный статус записывается отдельной короткой транзакцией

### Requirement: verify_member applies reconciliation observations safely

`verify_member` action MUST execute `getChatMember` through Enforcer's
rate-limited Telegram client and then apply the observation through the
same domain paths as normal membership events. Source resource
verification MUST update subscription observations and run
`recomputeAccess`; club resource verification MUST update
`access_grants` membership state without overriding `revoked` grants.

If Telegram verification fails with retryable errors, action MUST retry
according to Enforcer retry policy. If verification cannot be performed
because the bot lost rights or the API result is otherwise uncertain,
the domain result MUST be `unknown` and MUST NOT close subscriptions or
kick users.

#### Scenario: Source verify deactivation runs recomputeAccess
- **WHEN** `verify_member` for a source chat confirms the user is no
  longer a member
- **THEN** the matching subscription source is observed as `inactive`
- **AND** `recomputeAccess` runs through the usual safe revocation path

#### Scenario: Verify unknown preserves access
- **WHEN** `verify_member` receives a rights error or retry-exhausted
  uncertain result
- **THEN** the observation is treated as `unknown`
- **AND** active subscriptions and grants are not revoked

#### Scenario: Club verify does not overwrite revoked grant
- **WHEN** `verify_member` for a club resource observes the user is left
  but the grant is already `revoked`
- **THEN** grant state remains `revoked`
- **AND** the action completes as a domain no-op

### Requirement: hard_ban and unban actions support owner ban flows

Enforcer MUST execute `hard_ban` by calling `banChatMember` without a
follow-up `unbanChatMember`. It MUST execute `unban` by calling
`unbanChatMember` with `only_if_banned=true`. Both action types MUST be
idempotent and use the same expected no-op handling as other membership
actions.

`hard_ban` MUST NOT be executed against creator or administrator
members. If Telegram reports that the target is protected or already
not present, Enforcer MUST finish the action as expected no-op when the
domain state already records the ban, and MUST log a warning.

#### Scenario: Hard ban does not unban
- **WHEN** Enforcer executes `hard_ban` for a club resource and user
- **THEN** it calls `banChatMember`
- **AND** it does not call `unbanChatMember`

#### Scenario: Unban uses only_if_banned
- **WHEN** Enforcer executes `unban`
- **THEN** it calls `unbanChatMember` with `only_if_banned=true`
- **AND** already-unbanned target is treated as expected no-op

#### Scenario: Protected admin is not hard-banned by Enforcer
- **WHEN** `hard_ban` targets creator or administrator
- **THEN** Enforcer does not remove the user
- **AND** action finishes as expected no-op with warning

