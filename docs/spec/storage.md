# Хранилище (SQLite)

Жизненный цикл соединения, проверка готовности схемы и repository-граница над схемой v1. Читаемое зеркало спеки `storage`.

## Открытие БД

`database/sql` поверх драйвера `modernc.org/sqlite` (зарегистрирован как `sqlite`). DSN выставляет `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`, `_txlock=immediate` (каждая транзакция начинается `BEGIN IMMEDIATE`). Пул — одно соединение (`SetMaxOpenConns(1)`) с `ConnMaxLifetime=1h`, чтобы WAL мог чекпойнтиться. Каталог данных создаётся с правами `0700`, файл БД — `0600` (иначе драйвер дал бы `0644`).

## Проверка схемы

Приложение само DDL не применяет — только проверяет готовность. `CheckSchema` читает бухгалтерию goose (`goose_db_version`) и возвращает `ErrUnmigrated`, если таблицы нет или `max(version_id) WHERE is_applied = 1` меньше `1`. Сообщение велит выполнить `task migrate:up`.

## Схема v1

`0001_init.sql` создаёт 12 таблиц: `users`, `subscriptions`, `access_grants`, `pending_revocations`, `whitelist`, `invite_links`, `telegram_updates`, `tribute_events`, `access_actions`, `audit_log`, `admin_alerts`, `meta`. Все timestamp-колонки — строки RFC3339 в UTC. Доменные enum'ы (platform, state, mode, status, severity, action_type) держатся `CHECK`-ограничениями.

## Инварианты целостности

- **≤ 1 активной подписки** на `(tg_id, platform)` — partial unique `idx_subscriptions_active_unique` на `subscriptions(tg_id, platform) WHERE status='active'`. Expired слот не занимает; на разных платформах активные подписки допустимы.
- **Уникальность invite-ссылок по режиму** («активная» = `status IN ('created','sent')`): `shared_join_request` — ≤ 1 на `(resource, mode)`, `tg_id` обязан быть `NULL`; `personal_join_request` и `direct` — ≤ 1 на `(tg_id, resource, mode)`, `tg_id` обязан быть `NOT NULL`. Нулабельность `tg_id` по режиму держит `CHECK`.
- **Внешние ключи** включены на каждом соединении (`PRAGMA foreign_keys=1`) — целостность проверяется на записи.

## Репозитории

Рукописные repository-типы работают поверх узкого `DBTX`-интерфейса:
его удовлетворяют и `*sql.DB`, и `*sql.Tx`. Это позволяет строить один
и тот же repository либо поверх обычного соединения, либо поверх
транзакции обработчика. Для Telegram inbox это критично: доменные
изменения, audit-записи и терминальный статус обновления коммитятся одной
SQL-транзакцией.

Доступны узкие методы:

- `Users`: `Upsert`, `Get`, `SetDMState`.
- `Subscriptions`: `Create`, `GetActive`.
- `Grants`: `Upsert`, `Get`.
- `Meta`: `Get`, `Set`, `GetUpdateOffset`, `SetUpdateOffset`,
  `SetHealth`.
- `TelegramUpdates`: `InsertBatch`, `ListPending`, `MarkTerminal`.
- `Audit`: `Append`.
- `Alerts`: `Create`, `CreateOpenIfMissing`, `ResolveOpenByTitle`.

Репозитории `Revocations` и `Whitelist` конструируемы, но их полные
наборы методов добавят фазы, которым они нужны. `Get` по отсутствующей
строке возвращает ошибку, оборачивающую `ErrNotFound`; `Upsert`
идемпотентен по ключу.

`Users.Upsert` обновляет профильные поля из Telegram, но не затирает
admin-owned поля (`banned`, `banned_reason`, `notes`). `SetDMState`
меняет только состояние лички, `updated_at` и, для `open`, `last_seen_at`.

## Telegram inbox

`telegram_updates` — durable inbox для Bot API updates. Батч сохраняется
перед обработкой: новые строки вставляются как `pending`, raw JSON
кладётся в `payload_json`, а `meta.update_offset` продвигается до
следующего offset в той же транзакции. Дубликаты `update_id` игнорируются
через `INSERT OR IGNORE`.

Каждое pending-обновление доходит до одного терминального статуса:
`processed`, `ignored` или `failed`. `MarkTerminal` работает только по
`status='pending'`, поэтому повторная терминальная пометка не меняет уже
закрытую строку. При ошибке обработчика доменная транзакция
откатывается, а отдельная транзакция фиксирует `failed`, текст ошибки и
`admin_alert`.

`Meta.GetUpdateOffset` вместе с максимумом `telegram_updates.update_id`
используется для recovery: стартовый offset вычисляется как максимум
между сохранённым offset и `MAX(update_id)+1`.

## Audit и alerts

`audit_log` фиксирует операторски важные события, например потерю и
восстановление прав бота в чате. `admin_alerts` хранит открытые и
закрытые тревоги. Для startup health используется дедуп:
`CreateOpenIfMissing` не плодит одинаковые открытые alerts при
повторных рестартах, а `ResolveOpenByTitle` закрывает stale-тревогу,
если права восстановились во время простоя.

## Время на границе

`store` — единственное место, где `time.Time` кодируется в SQL и обратно: запись `time.RFC3339` в UTC, чтение того же формата. Опциональные метки round-trip'ят через `*time.Time` с соответствием `NULL ↔ nil`.
