# Telegram-транспорт

Слой Telegram Bot API: конкретный клиент, long polling, durable inbox,
маршрутизация и восстановление. Читаемое зеркало спеки
`telegram-transport`.

## Клиент

Пакет `internal/telegram` экспортирует один конкретный `*Client` поверх
`github.com/go-telegram/bot`. Свои интерфейсы пакет не объявляет:
узкие интерфейсы задают потребители. В Фазе 02 клиент покрывает
`getMe`, `getChat`, `getChatMember`, `sendMessage` и `setMyCommands`.

Ошибки Telegram нормализуются в категории, чтобы вызывающий код не
парсил строки:

- `429` несёт `retry_after`.
- `403` на `sendMessage` означает закрытую личку.
- `bot is not a member` и `not enough rights` считаются постоянными
  проблемами прав.
- Ожидаемые no-op ошибки будущего Enforcer'а трактуются как успех
  только внутри конкретного действия, а не в startup health.

## Long polling

Поллер забирает обновления через `getUpdates` одной горутиной и всегда
передаёт явный список `allowed_updates`:

```json
["message","callback_query","my_chat_member","chat_member","chat_join_request"]
```

Батчи обрабатываются последовательно, с сохранением порядка `update_id`.
`409 Conflict` при polling считается retryable и уходит в backoff;
`401 Unauthorized` фатален, потому что токен неверен.

## Durable inbox

Каждый батч сначала сохраняется в `telegram_updates`, и только после
коммита начинается обработка. В одной транзакции пишутся `pending`
строки с raw JSON payload и продвигается `meta.update_offset` до
`max(update_id)+1`. После этого Telegram уже не вернёт эти update id, а
локальная обработка восстановима из БД.

Для каждого обновления открывается отдельная транзакция обработчика. В ней
фиксируются доменные изменения, audit-записи и терминальный статус
`processed` или `ignored`. Telegram-вызовы внутри этой транзакции
запрещены: ответы и DM отправляются после коммита, best-effort, пока
durable outbox ещё не реализован.

Если обработчик падает, его транзакция откатывается. Отдельная
транзакция помечает обновление как `failed`, сохраняет текст ошибки и
поднимает `admin_alert`. Автоматического retry для `failed` нет.

## Восстановление

При старте поллер вычисляет offset как максимум между
`meta.update_offset` и `MAX(telegram_updates.update_id)+1`. Перед
новым polling он досканирует все оставшиеся `pending` строки по
`update_id`; это закрывает падения между сохранением батча и обработкой.

## Маршрутизация

В этой фазе реально обрабатываются:

- `message` в личке: `/start`, `/help` и fallback на `/start`.
- `/here` в группе или супергруппе от владельца: ответ с `chat.id` и
  типом.
- `my_chat_member`: chat health, DM-state и discovery.

`chat_member` и `chat_join_request` пока завершаются как `ignored`:
их наполнят следующие фазы. Канальные посты не запрашиваются, поэтому
`/here` в канале недоступна; ID канала владелец получает через discovery
при добавлении бота.
