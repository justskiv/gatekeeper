# telegram-transport Specification

## Purpose

Описывает Telegram Bot API transport, durable-приём обновлений, routing
и recovery-семантику поллера.
## Requirements
### Requirement: The poller consumes updates via long polling with an explicit allowed_updates list

Поллер MUST забирать обновления через long polling `getUpdates`,
передавая **полный явный список** `allowed_updates`:

```
["message","callback_query","my_chat_member","chat_member","chat_join_request"]
```

Типы вне списка не запрашиваются. Поллер работает **одной горутиной**,
обрабатывая батчи последовательно; долгие операции из обработчиков
не выполняются внутри цикла опроса.

#### Scenario: Явный allowed_updates при каждом poll
- **WHEN** поллер вызывает `getUpdates`
- **THEN** в запрос передаётся ровно список из пяти типов выше
- **AND** обновления типов вне списка ботом не запрашиваются

#### Scenario: Одна последовательная горутина поллера
- **WHEN** поллер запущен
- **THEN** батчи обрабатываются строго последовательно одной горутиной,
  сохраняя порядок `update_id`

### Requirement: The telegram package exposes one concrete Client over the Bot API

Пакет `telegram` MUST экспортировать **один конкретный** тип `*Client` —
обёртку над `github.com/go-telegram/bot` — и не объявляет собственных
интерфейсов (§20.2). `Client` предоставляет методы `getMe`, `getChat`,
`getChatMember`, `sendMessage`, `setMyCommands`, а также методы Bot API,
нужные Enforcer'у: `createChatInviteLink`, `revokeChatInviteLink`,
`approveChatJoinRequest`, `declineChatJoinRequest`, `banChatMember` и
`unbanChatMember`. Узкие интерфейсы объявляют пакеты-потребители у себя.

#### Scenario: Поверхность конкретного клиента
- **WHEN** потребитель использует пакет `telegram`
- **THEN** ему доступен конкретный `*Client` с методами `getMe`,
  `getChat`, `getChatMember`, `sendMessage`, `setMyCommands`,
  `createChatInviteLink`, `revokeChatInviteLink`,
  `approveChatJoinRequest`, `declineChatJoinRequest`, `banChatMember`
  и `unbanChatMember`
- **AND** пакет `telegram` не экспортирует интерфейсов для этих методов

### Requirement: Telegram API errors are normalized into typed categories

`Client` MUST нормализовать ошибки Telegram в различимые категории, чтобы
вызывающий код принимал решения, не разбирая текст ошибки:

- `429 Too Many Requests` — с доступным `retry_after`;
- `403` — для `sendMessage` означает закрытую личку;
- постоянные ошибки прав (`bot is not a member`, `not enough rights`);
- ожидаемые no-op **в контексте конкретного действия** (бан создателя,
  одобрение несуществующей заявки, действие над уже-не-участником) —
  трактуются как успех/no-op и логируются на уровне `warn`.

Классификация no-op зависит от **вызывающего действия** и НЕ применяется
к startup/health-`getChatMember`: там «не участник» или нехватка прав —
реальный сигнал для `meta.health.*`, а не успех. Эти категории нужны
Enforcer'у в Фазе 04 и chat-health в этой фазе.

#### Scenario: Rate limit несёт retry_after
- **WHEN** Telegram отвечает `429` с `parameters.retry_after`
- **THEN** нормализованная ошибка несёт значение `retry_after`
- **AND** отличима от прочих категорий

#### Scenario: Закрытая личка отличима от ошибки процесса
- **WHEN** `sendMessage` возвращает `403`
- **THEN** ошибка классифицируется как закрытая личка, а не сбой процесса

#### Scenario: Постоянная ошибка прав отличима
- **WHEN** вызов возвращает `bot is not a member` или `not enough rights`
- **THEN** ошибка классифицируется как постоянная ошибка прав

#### Scenario: Ожидаемый no-op трактуется как успех внутри действия
- **WHEN** действие Enforcer'а возвращает ожидаемую «ошибку» вроде
  `USER_NOT_PARTICIPANT` (например, kick уже-не-участника)
- **THEN** результат трактуется как no-op и логируется на уровне `warn`

#### Scenario: Та же ошибка является реальным сигналом в health-check
- **WHEN** `getChatMember` в startup/health-проверке показывает, что бот
  не участник чата или не имеет нужных прав
- **THEN** это трактуется как реальная проблема прав/членства, а не no-op

### Requirement: Incoming update batches are persisted to a durable inbox before handling

Каждый батч MUST сначала **durable-сохраняться**, и только потом
обрабатываться. В одной транзакции (`tx1`): каждое обновление пишется
`INSERT OR IGNORE INTO telegram_updates(update_id, update_type, chat_id,
tg_id, payload_json, received_at, status='pending')` — raw-JSON
обновления сохраняется в `payload_json` (колонка `NOT NULL`, нужна для
forensics и повторного разбора), и в той же транзакции
`meta.update_offset` продвигается до `max(batch.update_id) + 1`. После
коммита `tx1` очередь Telegram чиста: следующий `getUpdates` уже не
вернёт эти `update_id`. Обработчики запускаются **после** `tx1`.

#### Scenario: Батч сохранён и offset продвинут атомарно до обработки
- **WHEN** поллер получил батч обновлений
- **THEN** все строки батча вставлены в `telegram_updates` со статусом
  `pending` и сохранённым `payload_json`, а `meta.update_offset`
  продвинут в той же транзакции
- **AND** ни один обработчик не запускается до коммита этой транзакции

#### Scenario: Дубликат update_id является no-op
- **WHEN** обновление с уже существующим `update_id` приходит повторно
- **THEN** `INSERT OR IGNORE` не создаёт второй строки и не меняет
  существующую

### Requirement: Each update reaches a terminal status through the inbox state machine

`telegram_updates.status` MUST быть state machine с единственным
транзитным статусом `pending` и тремя терминальными: `processed`,
`ignored`, `failed`. Каждая `pending`-строка обрабатывается в порядке
`update_id` в своей транзакции (`tx2`), которая завершается переходом в
`processed` (обработчик применил доменные изменения) или `ignored`
(валидное обновление, нам не интересное). При любой ошибке обработчика
`tx2` откатывается, домен не тронут, и отдельная транзакция (`tx3`)
переводит строку в `failed`, записывает `error` и поднимает
`admin_alert(severity='error')`. `failed` — терминальный: авто-retry
нет, возврат в `pending` — только руками оператора.

#### Scenario: Успешный обработчик помечает processed
- **WHEN** обработчик успешно применил доменные изменения
- **THEN** строка переходит в `processed` с `processed_at`

#### Scenario: Намеренно неинтересное обновление помечается ignored
- **WHEN** обновление валидно, но обработчику нечего делать
- **THEN** строка переходит в `ignored`, отличимый от `processed`

#### Scenario: Ошибка обработчика помечает failed и поднимает alert
- **WHEN** обработчик возвращает ошибку
- **THEN** `tx2` откатывается, домен не изменён
- **AND** `tx3` переводит строку в `failed` с текстом ошибки и поднимает
  `admin_alert(severity='error')`
- **AND** авто-retry не выполняется

### Requirement: Update handlers commit atomically and keep Telegram calls out of the transaction

Обработчики обновлений MUST соблюдать два инварианта. **I1**: доменные
изменения, `audit_log`-записи, `access_actions`-INSERT'ы и
терминальный `UPDATE telegram_updates.status` коммитятся **в одной
транзакции** (`tx2`). Side-effect'ов вне `tx2`, влияющих на
durable-состояние, нет — иначе крэш между ними дал бы двойную обработку
на старте или потерянное исходящее действие.

Инвариант **I2**: вызовы Telegram внутри `tx2` запрещены — транзакция
не должна зависеть от сетевых таймаутов. Если обработчику нужно
отправить сообщение, выдать invite, approve/decline join request или
выполнить другое доменное Telegram-действие, обработчик MUST поставить
соответствующий `access_actions` row в `tx2`; Enforcer выполнит
Telegram-вызов после коммита.

#### Scenario: Доменное изменение, outbox action и terminal status делят транзакцию
- **WHEN** обработчик применяет доменные изменения и должен выполнить
  Telegram side effect
- **THEN** доменные записи, `access_actions` и терминальный
  `UPDATE telegram_updates.status` коммитятся одной транзакцией
- **AND** при крэше до коммита строка update остаётся `pending` и будет
  переобработана

#### Scenario: В handler-транзакции нет Telegram-вызова
- **WHEN** обработчику нужно отправить сообщение или изменить состояние
  пользователя в Telegram
- **THEN** внутри `tx2` создаётся outbox action
- **AND** прямой вызов Telegram выполняется только Enforcer'ом после
  коммита

### Requirement: The poller resolves its offset and recovers pending updates on startup

Поллер MUST вычислять стартовый offset как
`resolved_offset = max(meta.update_offset, MAX(telegram_updates.update_id) + 1)`,
что страхует от рассинхрона `meta` и inbox при экзотических крэшах.
Перед входом в цикл опроса поллер MUST **досканировать** оставшиеся
`pending`-строки в порядке `update_id` — это закрывает падения между
`tx1` и `tx2` и между откатом `tx2` и `tx3`. Отдельной операции
«сохранить offset при остановке» нет: значение durable после каждой
`tx1`.

#### Scenario: Оставшиеся pending-строки переобрабатываются на старте
- **WHEN** при старте в `telegram_updates` есть строки в статусе
  `pending`
- **THEN** поллер до входа в цикл обрабатывает их в порядке `update_id`

#### Scenario: Restart не переобрабатывает обновления в polling window
- **WHEN** бот перезапущен после простоя меньше 24 часов
- **THEN** опрос возобновляется с `resolved_offset`
- **AND** обновления, уже сохранённые по `update_id`, не обрабатываются
  повторно

### Requirement: The router dispatches updates by type and chat id

Роутер MUST маршрутизировать каждое обновление по типу и `chat.id`.
В этой фазе **реально обрабатываются**: `message` в личке → хендлеры
команд бота; `/here` в группе/супергруппе от владельца → ответ с
`chat.id` (в каналах недоступна: нет `channel_post` в
`allowed_updates`); `my_chat_member` → chat-health; `chat_member` в
источнике-чате (`BOOSTY_GROUP_ID`, либо `TRIBUTE_CHANNEL_ID` в режиме A)
→ нормализация в `SubscriptionEvent` и `engine.handleEvent` внутри
`tx2`. Обновления `chat_member` в прочих чатах и `chat_join_request`
маршрутов ещё не имеют и завершаются как `ignored` (наполнят Фаза 05).
Прочее — `ignored`.

#### Scenario: Private message маршрутизируется в bot command handlers
- **WHEN** приходит `message` из приватного чата
- **THEN** оно направляется в хендлеры команд бота

#### Scenario: my_chat_member маршрутизируется в chat-health
- **WHEN** приходит `my_chat_member`
- **THEN** оно направляется в обработку chat-health

#### Scenario: chat_member источника-чата маршрутизируется в движок
- **WHEN** приходит `chat_member` с `chat.id`, равным `BOOSTY_GROUP_ID`
  или (в режиме A) `TRIBUTE_CHANNEL_ID`
- **THEN** оно нормализуется в `SubscriptionEvent` и применяется через
  `engine.handleEvent`

#### Scenario: Прочие membership-updates игнорируются в этой фазе
- **WHEN** приходит `chat_member` в чате, не являющемся настроенным
  источником, или `chat_join_request`
- **THEN** в этой фазе строка завершается статусом `ignored`

### Requirement: Membership-события источников нормализуются в SubscriptionEvent

Роутер MUST приводить `chat_member` из источника-чата к нормализованному
`SubscriptionEvent` до передачи в движок. Платформа MUST определяться по
`chat.id` (`boosty` или `tribute`). Обработчик MUST вычислить членство
до и после по `old_chat_member`/`new_chat_member`: переход
«не-участник → участник» MUST давать `Activated`, «участник →
не-участник» — `Deactivated`. Признак участия MUST включать статусы
`creator`/`administrator`/`member` и `restricted` с `is_member=true`.
Изменения, затрагивающие ботов (включая самого бота), MUST
игнорироваться; смена прав без смены членства (членство до == после)
MUST быть no-op. Применение события MUST идти внутри `tx2`, сохраняя
инварианты I1 (атомарность с терминальным статусом) и I2 (без вызовов
Telegram в транзакции — членство берётся из payload, не из сети).

#### Scenario: Вступление даёт Activated
- **WHEN** в источнике-чате пользователь перешёл из не-участника в
  участники
- **THEN** формируется `SubscriptionEvent{Kind: Activated}` для платформы
  этого чата

#### Scenario: Выход даёт Deactivated
- **WHEN** в источнике-чате пользователь перестал быть участником
- **THEN** формируется `SubscriptionEvent{Kind: Deactivated}`

#### Scenario: Смена прав без смены членства игнорируется
- **WHEN** членство до и после совпадает (изменились только права)
- **THEN** событие не формируется, обновление завершается без доменных
  изменений

#### Scenario: События ботов игнорируются
- **WHEN** затронутый пользователь — бот (в том числе сам бот)
- **THEN** событие не формируется

