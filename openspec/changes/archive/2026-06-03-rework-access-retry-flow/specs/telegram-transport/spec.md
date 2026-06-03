## MODIFIED Requirements

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

## ADDED Requirements

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
