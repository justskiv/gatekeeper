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
`getChatMember`, `sendMessage`, `setMyCommands`, `setWebhook`,
`deleteWebhook`, методы in-place правки и подтверждения callback'ов
`answerCallbackQuery`, `editMessageText`, `editMessageReplyMarkup`, а
также методы Bot API, нужные Enforcer'у: `createChatInviteLink`,
`revokeChatInviteLink`, `approveChatJoinRequest`,
`declineChatJoinRequest`, `banChatMember` и `unbanChatMember`. Узкие
интерфейсы объявляют пакеты-потребители у себя.

`sendMessage` и `sendMessageWithReplyMarkup` MUST поддерживать выбранный
parse mode для форматированных сообщений, созданных ботом. Telegram HTML
parse mode (`models.ParseModeHTML`) MUST использоваться только для
сообщений, созданных централизованным renderer'ом. Plain text сообщения
MUST отправляться без parse mode, если они явно не отрендерены как
форматированные. Plain export/CSV сообщения MUST отключать parse mode,
когда форматирование может испортить данные.

`editMessageText` MUST редактировать текст и inline-клавиатуру
существующего сообщения с тем же HTML parse mode, что и форматированные
исходящие сообщения. `editMessageReplyMarkup` MUST менять только
inline-клавиатуру, не трогая текст. Ответ Telegram `message is not
modified` для этих методов MUST трактоваться как успех, потому что
желаемое состояние уже достигнуто. `answerCallbackQuery` MUST
подтверждать callback query, чтобы клиент убрал спиннер на
inline-кнопке.

#### Scenario: Поверхность конкретного клиента
- **WHEN** потребитель использует пакет `telegram`
- **THEN** ему доступен конкретный `*Client` с методами `getMe`,
  `getChat`, `getChatMember`, `sendMessage`, `setMyCommands`,
  `setWebhook`, `deleteWebhook`, `answerCallbackQuery`,
  `editMessageText`, `editMessageReplyMarkup`, `createChatInviteLink`,
  `revokeChatInviteLink`, `approveChatJoinRequest`,
  `declineChatJoinRequest`, `banChatMember` и `unbanChatMember`
- **AND** пакет `telegram` не экспортирует интерфейсов для этих методов

#### Scenario: Форматированное сообщение выставляет HTML parse mode

- **WHEN** клиент отправляет шаблонное сообщение бота
- **THEN** Telegram `sendMessage` получает `parse_mode="HTML"`
- **AND** тот же parse mode используется при отправке с reply markup

#### Scenario: Plain message не получает parse mode

- **WHEN** клиент отправляет сообщение, помеченное как plain text
- **THEN** Telegram `sendMessage` не получает `parse_mode`
- **AND** сырые `<`, `>` или `&` в таком plain text не трактуются как
  Telegram HTML

#### Scenario: Plain export отключает форматирование

- **WHEN** owner export path отправляет plain CSV text
- **THEN** transport отправляет его без parse mode
- **AND** содержимое CSV не трактуется как Telegram formatting

#### Scenario: Правка сообщения сохраняет HTML и переживает not-modified

- **WHEN** клиент вызывает `editMessageText` с форматированным текстом
  и Telegram отвечает `message is not modified`
- **THEN** правка использует `parse_mode="HTML"` для нового текста
- **AND** ответ `message is not modified` трактуется как успех, а не
  ошибка

### Requirement: Telegram HTML rendering безопасен и централизован

Форматированные сообщения бота SHALL использовать Telegram HTML parse
mode через централизованный renderer, а не ad hoc HTML-конкатенацию в
feature-коде. Динамические значения, вставляемые в HTML-сообщения, MUST
экранироваться перед рендером, если это не явно безопасные renderer
fragments, созданные пакетом `messages`.

Renderer MUST экранировать как минимум `<`, `>` и `&` во всём
динамическом тексте, включая значения внутри `<code>` или `<pre>`.
Body-text escape helper MUST быть фиксированным replacer'ом на три
символа, эквивалентным
`strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")`.
Renderer MUST NOT использовать `html.EscapeString` для body text,
потому что он также заменяет кавычки (`"`/`'`) на numeric entities и
портит видимый copy. Renderer также MUST NOT генерировать
неподдержанные Telegram HTML tags. Поддержанное форматирование SHOULD
ограничиваться небольшим стабильным subset: `<b>`, `<i>`, `<code>`,
`<pre>` и `<a href="...">`, где нужно.

Ссылки MUST рендериться безопасно. Link helpers MUST валидировать
разрешённые URL schemes и отдельно экранировать `href` attributes и
labels. Небезопасные URL вроде `javascript:` или значения с control
characters MUST NOT рендериться как clickable HTML links. URL text,
показанный пользователю, MAY быть raw link только если он пришёл из
trusted invite-link storage; link labels, названия чатов, usernames,
причины, audit details и alert details MUST экранироваться.

MarkdownV2 SHALL NOT использоваться для основного renderer'а, потому
что его escaping surface шире и хрупче для server-generated diagnostics.
Legacy Markdown SHALL NOT использоваться для новых сообщений.

Explicit Telegram entities MAY быть введены позже для узкого rich text,
который безопаснее строить без parse-mode parsing, но любой такой
builder MUST считать offsets в требуемых Telegram UTF-16 code units.

#### Scenario: Имя пользователя не ломает HTML

- **WHEN** динамический username или display name содержит `<`, `>`,
  `&` или символы кавычек
- **THEN** отрендеренный Telegram text остаётся валидным для HTML parse
  mode
- **AND** динамическое значение показывается как текст, а не как tag

#### Scenario: Admin detail не внедряет markup

- **WHEN** alert detail, audit detail или admin reason содержит HTML или
  Telegram markup characters
- **THEN** diagnostics рендерятся как escaped text
- **AND** Telegram не отклоняет сообщение из-за malformed entities

#### Scenario: Небезопасная ссылка не рендерится как HTML

- **WHEN** динамический URL содержит unsupported scheme, missing host
  или control characters
- **THEN** renderer не выпускает для него ссылку `<a href="...">`
- **AND** значение отклоняется или показывается как escaped plain text

#### Scenario: Usage placeholder безопасен в HTML

- **WHEN** command usage template содержит placeholder вроде
  `<tg_id|@username>`
- **THEN** placeholder рендерится как escaped text или safe code
- **AND** Telegram не интерпретирует его как HTML tag

#### Scenario: Неподдержанные tags не выпускаются

- **WHEN** message templates рендерятся
- **THEN** templates используют только разрешённый Telegram HTML subset
- **AND** тесты падают, если template выпускает unsupported tag

#### Scenario: Legacy Markdown не используется

- **WHEN** кодовая база отправляет formatted Telegram text
- **THEN** она не использует legacy Markdown parse mode
- **AND** тесты или static checks ловят `models.ParseModeMarkdownV1` и
  raw `"Markdown"` usage в send paths

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
обрабатываться. В одной транзакции (`receiveTx`): каждое обновление
пишется `INSERT OR IGNORE INTO telegram_updates(update_id, update_type,
chat_id, tg_id, payload_json, received_at, status='pending')` —
**сырой** raw-JSON обновления сохраняется в `payload_json` (колонка
`NOT NULL`, нужна для forensics и повторного разбора), и в той же
транзакции `meta.update_offset` продвигается до
`max(batch.update_id) + 1`. После коммита `receiveTx` очередь Telegram
чиста: следующий `getUpdates` уже не вернёт эти `update_id`. Обработчики
запускаются **после** `receiveTx`.

Persisted `payload_json` MUST быть сырым update'ом ровно в том виде, в
каком его прислал Telegram. Redaction (`redact`) MUST применяться только
на границе логирования и MUST NOT затрагивать persisted или processed
payload. Обработчик MUST разбирать тот же сырой payload, что был
сохранён: redaction processed-payload'а уничтожает поля, по которым бот
принимает решения (например `invite_link` в join-request), и приводит к
тихим отказам.

#### Scenario: Батч сохранён и offset продвинут атомарно до обработки
- **WHEN** поллер получил батч обновлений
- **THEN** все строки батча вставлены в `telegram_updates` со статусом
  `pending` и сохранённым сырым `payload_json`, а `meta.update_offset`
  продвинут в той же транзакции
- **AND** ни один обработчик не запускается до коммита этой транзакции

#### Scenario: Дубликат update_id является no-op
- **WHEN** обновление с уже существующим `update_id` приходит повторно
- **THEN** `INSERT OR IGNORE` не создаёт второй строки и не меняет
  существующую

#### Scenario: Redaction не затрагивает persisted и processed payload

- **WHEN** update несёт чувствительное поле (например `invite_link` в
  join-request)
- **THEN** в `telegram_updates.payload_json` это поле сохраняется
  сырым, и обработчик разбирает его без redaction
- **AND** redaction применяется только при логировании update'а

### Requirement: Each update reaches a terminal status through the inbox state machine

`telegram_updates.status` MUST быть state machine с единственным
транзитным статусом `pending` и тремя терминальными: `processed`,
`ignored`, `failed`. Каждая `pending`-строка обрабатывается в порядке
`update_id` в своей транзакции (`handleTx`), которая завершается переходом в
`processed` (обработчик применил доменные изменения) или `ignored`
(валидное обновление, нам не интересное). При любой ошибке обработчика
`handleTx` откатывается, домен не тронут, и отдельная транзакция (`failTx`)
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
- **THEN** `handleTx` откатывается, домен не изменён
- **AND** `failTx` переводит строку в `failed` с текстом ошибки и поднимает
  `admin_alert(severity='error')`
- **AND** авто-retry не выполняется

### Requirement: Update handlers commit atomically and keep Telegram calls out of the transaction

Обработчики обновлений MUST соблюдать два инварианта. **I1**: доменные
изменения, `audit_log`-записи, `access_actions`-INSERT'ы и
терминальный `UPDATE telegram_updates.status` коммитятся **в одной
транзакции** (`handleTx`). Side-effect'ов вне `handleTx`, влияющих на
durable-состояние, нет — иначе крэш между ними дал бы двойную обработку
на старте или потерянное исходящее действие.

Инвариант **I2**: вызовы Telegram внутри `handleTx` запрещены — транзакция
не должна зависеть от сетевых таймаутов. Если обработчику нужно
отправить сообщение, выдать invite, approve/decline join request или
выполнить другое доменное Telegram-действие, обработчик MUST поставить
соответствующий `access_actions` row в `handleTx`; Enforcer выполнит
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
- **THEN** внутри `handleTx` создаётся outbox action
- **AND** прямой вызов Telegram выполняется только Enforcer'ом после
  коммита

### Requirement: The poller resolves its offset and recovers pending updates on startup

Поллер MUST вычислять стартовый offset как
`resolved_offset = max(meta.update_offset, MAX(telegram_updates.update_id) + 1)`,
что страхует от рассинхрона `meta` и inbox при экзотических крэшах.
Перед входом в цикл опроса поллер MUST **досканировать** оставшиеся
`pending`-строки в порядке `update_id` — это закрывает падения между
`receiveTx` и `handleTx` и между откатом `handleTx` и `failTx`. Отдельной операции
«сохранить offset при остановке» нет: значение durable после каждой
`receiveTx`.

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
Реально обрабатываются: `message` в личке -> хендлеры команд бота
(`/start` и некомандный DM запускают grant-access flow); `/here` в
группе или супергруппе от владельца -> ответ с `chat.id` (в каналах
недоступна: нет `channel_post` в `allowed_updates`);
`my_chat_member` -> chat-health; `chat_member` в Boosty source chat ->
нормализация в `SubscriptionEvent` и `engine.handleEvent` внутри `handleTx`;
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
MUST быть no-op. Применение события MUST идти внутри `handleTx`, сохраняя
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

### Requirement: The poller acknowledges retry-access callbacks before the slow preflight

Поллер MUST давать retry-access нажатию мгновенную обратную связь до
того, как запустится медленный admission preflight. Для update'а с
`callback_query`, чья `data` равна retry-access callback data, поллер
MUST подтвердить callback query (убрать спиннер inline-кнопки) и сменить
inline-кнопку на лейбл «проверяем», не трогая тело сообщения.

Обе операции MUST быть best-effort: сбой логируется на уровне `warn` и
MUST NOT прерывать обработку update'а. Отменённый контекст MUST просто
завершать подтверждение без ошибки. Подтверждение MUST выполняться вне
`handleTx`, как обычный preflight Telegram-вызов, чтобы не держать
транзакцию открытой во время сети (инвариант I2).

#### Scenario: Retry-нажатие мгновенно подтверждается
- **WHEN** приходит `callback_query` с retry-access callback data
- **THEN** поллер подтверждает callback query до запуска admission
  preflight
- **AND** inline-кнопка меняется на лейбл «проверяем», тело сообщения не
  меняется

#### Scenario: Сбой подтверждения не роняет обработку
- **WHEN** подтверждение callback или правка клавиатуры возвращает
  ошибку
- **THEN** ошибка логируется на уровне `warn`
- **AND** обработка update'а продолжается обычным admission-флоу

