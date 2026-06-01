## Why

После Фаз 03 и 04 у Gatekeeper уже есть объяснимый статус подписки и
durable outbox для Telegram-действий, но главный пользовательский
сценарий ещё не замкнут: подписчик не может сам получить доступ в
клубные ресурсы. Эта фаза соединяет `/start`, invite-ссылки и
`chat_join_request` в pull-модель выдачи доступа без синхронных
сетевых вызовов из handler'ов.

## What Changes

- Вводится capability `grant-access`: пользователь с активной подпиской
  запрашивает доступ через `/start` или некомандный DM, получает ссылки
  в клубный чат и канал, а его join-request одобряется только после
  живой проверки подписки.
- `/start` перестаёт быть только приветствием и становится запросом
  доступа: регистрирует пользователя, проверяет ban/status, создаёт
  `pending` grants и ставит outbox-действия или отправляет shared-ссылки
  через durable DM.
- `chat_join_request` для клубных ресурсов маршрутизируется в admission
  handler: active-пользователь получает `approve_join`, inactive или
  unknown после ретраев получает `decline_join` и понятное сообщение.
- `chat_member` в клубном чате или канале начинает обновлять
  `access_grants`: bot-admitted участники фиксируются как `joined`,
  ручные внешние добавления помечаются `external` и дают `admin_alert`,
  но не приводят к автоматическому кику.
- Invite-link lifecycle дополняется разрешением ссылки из join-request,
  защитой personal-ссылок от использования другим пользователем и
  fallback'ом, если Telegram не прислал `invite_link`.
- Автоматический отзыв доступа при потере подписки остаётся вне области
  этой фазы и будет реализован отдельно.

## Capabilities

### New Capabilities

- `grant-access`: pull-модель выдачи доступа через `/start`,
  invite-ссылки, `chat_join_request` approval и фиксацию membership в
  `access_grants`.

### Modified Capabilities

- `bot-commands`: `/start` и некомандный DM становятся запросом
  доступа, добавляются admission-тексты и rate limit повторной проверки.
- `telegram-transport`: роутер перестаёт игнорировать
  `chat_join_request` и `chat_member` клубных ресурсов.
- `runtime`: startup не запускает poller при неоднозначной конфигурации,
  где source chat id пересекается с club resource id.
- `invite-links`: сервис должен разрешать ссылку из join-request,
  фиксировать misuse personal-ссылок и поддерживать fallback без поля
  `invite_link`.
- `storage`: repository-граница расширяется операциями, нужными для
  admission flow поверх `access_grants` и `invite_links`.

## Impact

- Основные зоны кода: `internal/bot`, `internal/engine`,
  `internal/telegram`, `internal/store`, `internal/invite`,
  `internal/messages`, `cmd/gatekeeper`.
- Схема БД не меняется: используются существующие `users`,
  `subscriptions`, `access_grants`, `invite_links`, `access_actions`,
  `audit_log` и `admin_alerts`.
- Новых внешних зависимостей не планируется.
- Фаза опирается на уже реализованные action types Enforcer'а:
  `send_dm`, `send_invite`, `approve_join`, `decline_join` и
  `soft_kick`.
