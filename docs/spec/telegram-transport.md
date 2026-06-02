# Telegram-транспорт

Слой Telegram Bot API: конкретный клиент, long polling или webhook,
durable inbox, маршрутизация и восстановление. Читаемое зеркало спеки
`telegram-transport`.

## Клиент

Пакет `internal/telegram` экспортирует один конкретный `*Client` поверх
`github.com/go-telegram/bot`. Свои интерфейсы пакет не объявляет:
узкие интерфейсы задают потребители. Клиент покрывает `getMe`,
`getChat`, `getChatMember`, `sendMessage`, `setMyCommands`, `setWebhook`,
`deleteWebhook`, а также методы Bot API, нужные Enforcer'у:
`createChatInviteLink`, `revokeChatInviteLink`, `approveChatJoinRequest`,
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
коммита начинается обработка. В одной транзакции пишутся `pending`
строки с raw JSON payload и продвигается `meta.update_offset` до
`max(update_id)+1`. После этого Telegram уже не вернёт эти update id, а
локальная обработка восстановима из БД.

Для каждого обновления открывается отдельная транзакция обработчика
(`tx2`). Два инварианта:

- **I1** — доменные изменения, `audit_log`-записи, INSERT'ы в
  `access_actions` и терминальный статус `processed`/`ignored`
  коммитятся одной транзакцией. Side-effect'ов вне `tx2`, влияющих на
  durable-состояние, нет: иначе крэш между ними дал бы двойную
  обработку или потерянное исходящее действие.
- **I2** — Telegram-вызовы внутри `tx2` запрещены. Если нужно отправить
  сообщение, выдать invite, approve/decline join request и т. п.,
  обработчик ставит соответствующий `access_actions` row в `tx2`;
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
  в `SubscriptionEvent` и `engine.handleEvent` внутри `tx2`.
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
внутри `tx2` с соблюдением I1/I2: членство берётся из payload, не из
сети.
