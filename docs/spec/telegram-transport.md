# Telegram-транспорт

Слой Telegram Bot API: конкретный клиент, long polling или webhook,
durable inbox, маршрутизация и восстановление. Читаемое зеркало спеки
`telegram-transport`.

## Клиент

Пакет `internal/telegram` экспортирует один конкретный `*Client` поверх
`github.com/go-telegram/bot`. Свои интерфейсы пакет не объявляет:
узкие интерфейсы задают потребители. Клиент покрывает `getMe`,
`getChat`, `getChatMember`, `sendMessage`, `setMyCommands`, `setWebhook`,
`deleteWebhook`, методы in-place правки и подтверждения callback'ов
`answerCallbackQuery`, `editMessageText`, `editMessageReplyMarkup`, а
также методы Bot API, нужные Enforcer'у: `createChatInviteLink`,
`revokeChatInviteLink`, `approveChatJoinRequest`,
`declineChatJoinRequest`, `banChatMember`, `unbanChatMember`.

Ошибки Telegram нормализуются в категории, чтобы вызывающий код не
парсил строки:

- `429` несёт `retry_after`.
- `403` на `sendMessage` означает закрытую личку.
- `bot is not a member` и `not enough rights` считаются постоянными
  проблемами прав.
- Ожидаемые no-op ошибки Enforcer'а трактуются как успех только
  внутри конкретного действия. В startup/health-`getChatMember` та же
  ошибка («не участник», нехватка прав) — реальный сигнал для
  `meta.health.*`, а не no-op.

## HTML-рендеринг и parse mode

`sendMessage` и `sendMessageWithReplyMarkup` несут per-message
`parse_mode`. Форматированные сообщения бота уходят в Telegram HTML
(`models.ParseModeHTML`) и только через централизованный renderer пакета
`messages` — ad hoc склейки HTML в feature-коде нет. Сообщение без
объявленного parse mode отправляется plain. То же правило закрывает
cutover durable-очереди: payload без `parse_mode` всегда трактуется как
plain, чтобы литеральный `<` в ранее поставленном сообщении не ловил
`400` и не отравлял outbox (см. [outbox-enforcer](outbox-enforcer.md)).

Renderer экранирует как минимум `<`, `>` и `&` во всех динамических
значениях, в том числе внутри `<code>` и `<pre>`. Эскейпер тела —
фиксированный трёхсимвольный `strings.NewReplacer` на `&`, `<`, `>`;
`html.EscapeString` не используется, потому что он переписывает ещё и
кавычки и портит видимый текст. Набор тегов ограничен небольшим
стабильным подмножеством: `<b>`, `<i>`, `<code>`, `<pre>` и
`<a href="...">`.

Ссылки рендерятся безопасно: helper проверяет разрешённые схемы
(`http`/`https`/`tg`, непустой host, без control-символов) и экранирует
`href` и label раздельно. Небезопасный URL (`javascript:`, значение с
control-символами) не превращается в кликабельную ссылку, а показывается
как экранированный текст. Сырой URL допустим только из доверенного
хранилища invite-ссылок; titles, usernames, reasons, audit- и
alert-детали экранируются всегда.

MarkdownV2 для основного renderer'а не используется — его surface
экранирования шире и ломче для серверной диагностики; legacy Markdown
запрещён для новых сообщений. Guard-тест ловит
`models.ParseModeMarkdownV1` и сырой `"Markdown"` в send-путях.

## Правка сообщений на месте

`editMessageText` редактирует текст и inline-клавиатуру существующего
сообщения с тем же HTML parse mode, что и форматированные исходящие
сообщения; `editMessageReplyMarkup` меняет только клавиатуру, не трогая
текст. Ответ Telegram `message is not modified` для обоих методов
трактуется как успех — желаемое состояние уже достигнуто.
`answerCallbackQuery` подтверждает callback query, чтобы клиент убрал
спиннер на inline-кнопке.

## Long polling

Поллер забирает обновления через `getUpdates` одной горутиной и всегда
передаёт явный список `allowed_updates`:

```json
["message","callback_query","my_chat_member","chat_member","chat_join_request"]
```

Батчи обрабатываются последовательно, с сохранением порядка `update_id`.
`409 Conflict` при polling считается retryable и уходит в backoff;
`401 Unauthorized` фатален, потому что токен неверен.

## Webhook transport

Polling и webhook взаимоисключающи: активен ровно один транспорт. При
`TELEGRAM_MODE=webhook` вместо long polling работает HTTP-эндпоинт
`POST {TELEGRAM_WEBHOOK_PATH}`. Обработчик сначала сверяет заголовок
`X-Telegram-Bot-Api-Secret-Token` с `TELEGRAM_WEBHOOK_SECRET` и только
потом парсит тело. Отсутствующий или неверный секрет → `401`, при этом
строка в `telegram_updates` не пишется.

Валидные webhook-обновления проходят через тот же durable inbox
(`telegram_updates`), тот же роутер и ту же state machine, что и polling:
`pending → processed | ignored | failed`. Доменные изменения, audit,
outbox actions и терминальный статус коммитятся атомарно внутри
транзакции обработчика. Дубликат `update_id` — no-op.

В webhook-режиме runtime регистрирует webhook: публичный URL
`TELEGRAM_WEBHOOK_PUBLIC_URL + TELEGRAM_WEBHOOK_PATH`, секрет-токен и тот
же явный список `allowed_updates`, что и при polling. Ошибка регистрации
фатальна до того, как процесс отдаст readiness. В polling-режиме runtime
гарантирует, что webhook-доставка отключена/не настроена, чтобы работал
`getUpdates`.

## Durable inbox

Каждый батч сначала сохраняется в `telegram_updates`, и только после
коммита начинается обработка. В одной транзакции (`receiveTx`) пишутся
`pending` строки с raw JSON payload (`payload_json`, `NOT NULL`) и
продвигается `meta.update_offset` до `max(update_id)+1`. После этого
Telegram уже не вернёт эти update id, а локальная обработка восстановима
из БД.

`payload_json` хранит сырое обновление ровно в том виде, в каком его
прислал Telegram. Redaction (`redact`) применяется *только* на границе
логирования и не затрагивает persisted или processed payload. Обработчик
разбирает тот же сырой payload, что был сохранён: redaction
processed-payload'а уничтожала поля, по которым бот принимает решения
(например `invite_link` в join-request), и приводила к тихим отказам.

Для каждого обновления открывается отдельная транзакция обработчика
(`handleTx`). Два инварианта:

- **I1** — доменные изменения, `audit_log`-записи, INSERT'ы в
  `access_actions` и терминальный статус `processed`/`ignored`
  коммитятся одной транзакцией. Side-effect'ов вне `handleTx`, влияющих на
  durable-состояние, нет: иначе крэш между ними дал бы двойную
  обработку или потерянное исходящее действие.
- **I2** — Telegram-вызовы внутри `handleTx` запрещены. Если нужно отправить
  сообщение, выдать invite, approve/decline join request и т. п.,
  обработчик ставит соответствующий `access_actions` row в `handleTx`;
  фактический вызов делает Enforcer после коммита (см.
  [outbox-enforcer](outbox-enforcer.md)).

Если обработчик падает, его транзакция откатывается, домен не тронут.
Отдельная транзакция помечает обновление как `failed`, сохраняет текст
ошибки и поднимает `admin_alert(severity='error')`. Автоматического
retry для `failed` нет — возврат в `pending` только руками оператора.

## Восстановление

При старте поллер вычисляет offset как максимум между
`meta.update_offset` и `MAX(telegram_updates.update_id)+1`. Перед
новым polling он досканирует все оставшиеся `pending` строки по
`update_id`; это закрывает падения между сохранением батча и обработкой.

## Маршрутизация

Роутер диспатчит каждое обновление по типу и `chat.id`:

- `message` в личке → хендлеры команд бота (`/start` и некомандный DM
  запускают grant-access flow; см. [bot-commands](bot-commands.md)).
- `/here` в группе/супергруппе от владельца → ответ с `chat.id` и типом.
- `my_chat_member` → chat health, DM-state и discovery.
- `chat_member` в Boosty source chat (`BOOSTY_GROUP_ID`) → нормализация
  в `SubscriptionEvent` и `engine.handleEvent` внутри `handleTx`.
- `chat_member` в Tribute source chat (`TRIBUTE_CHANNEL_ID`) →
  нормализация в `SubscriptionEvent` только при
  `TRIBUTE_MODE=observation`. При `TRIBUTE_MODE=webhook` членство
  Tribute-канала — вторичный verification-сигнал: оно не истекает
  webhook-fed ledger-подписку (доступ определяется webhook ledger и общим
  status aggregation) и может обновлять диагностический/audit сигнал.
- `chat_join_request` в клубном чате или канале → admission
  join-request handler ([grant-access](grant-access.md)).
- `chat_member` в клубном чате или канале → club membership handler.

Прочее завершается как `ignored`. Неоднозначное пересечение source chat
id и club resource id отклоняется на уровне runtime/config до запуска
поллера, поэтому роутер не выбирает между двумя доменными обработчиками
для одного update. Канальные посты не запрашиваются, поэтому `/here` в
канале недоступна; ID канала владелец получает через discovery.

## Нормализация событий источника

`chat_member` из источника-чата приводится к `SubscriptionEvent` до
передачи в движок. Платформа определяется по `chat.id` (`boosty` или
`tribute`). Членство до и после считается по
`old_chat_member`/`new_chat_member`: переход «не-участник → участник»
даёт `Activated`, «участник → не-участник» — `Deactivated`. Участием
считаются `creator`/`administrator`/`member` и `restricted` с
`is_member=true`. Изменения, затрагивающие ботов (включая самого бота),
игнорируются; смена прав без смены членства — no-op. Применение идёт
внутри `handleTx` с соблюдением I1/I2: членство берётся из payload, не из
сети.

## Подтверждение retry-access нажатий

Retry-access нажатие получает мгновенную обратную связь до того, как
запустится медленный admission preflight. Для update'а с
`callback_query`, чья `data` равна retry-access callback data, поллер
подтверждает callback query (`answerCallbackQuery`, убирает спиннер
inline-кнопки) и меняет inline-кнопку на лейбл «проверяем»
(`editMessageReplyMarkup`), не трогая тело сообщения.

Обе операции best-effort: сбой логируется на `warn` и не прерывает
обработку update'а — она продолжается обычным admission-флоу. Отменённый
контекст просто завершает подтверждение без ошибки. Подтверждение идёт
вне `handleTx`, как обычный preflight Telegram-вызов (инвариант I2).
