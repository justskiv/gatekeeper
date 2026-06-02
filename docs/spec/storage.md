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

- `Users`: `Upsert`, `Get`, `FindByUsername`, `SetDMState`.
- `Subscriptions`: `Create`, `GetActive`, `UpsertActive`, `ExpireActive`,
  `ListActiveByUser`, `ListByUser`.
- `Grants`: `Upsert`, `Get`, `ListByUser`.
- `Whitelist`: `Has`.
- `Revocations`: `Get`, `Delete`.
- `Meta`: `Get`, `Set`, `GetUpdateOffset`, `SetUpdateOffset`,
  `SetHealth`.
- `TelegramUpdates`: `InsertBatch`, `ListPending`, `MarkTerminal`.
- `Audit`: `Append`, `ListRecentByUser`.
- `Alerts`: `Create`, `CreateOpenIfMissing`, `ResolveOpenByTitle`.

История подписок append-only: `UpsertActive` обновляет активную строку
`(tg_id, platform)` вместо создания второй (не нарушая
`idx_subscriptions_active_unique`); `ExpireActive` переводит активную
строку в `status='expired'` с `ended_at`, не удаляя её, и возвращает
признак, была ли строка закрыта. Чтения для команд (`ListByUser`,
`ListRecentByUser`) упорядочены от новых записей к старым. Методы
`FindByUsername`, `ListActiveByUser`, `Grants.ListByUser`,
`Audit.ListRecentByUser`, `Whitelist.Has` и `Revocations.Get` нужны
ядру [status-core](status-core.md) и командам `/status`/`/whois`.

`Get` по отсутствующей строке возвращает ошибку, оборачивающую
`ErrNotFound`; `Upsert` идемпотентен по ключу. `Users.Upsert` обновляет
профильные поля из Telegram, но не затирает admin-owned поля (`banned`,
`banned_reason`, `notes`); `FindByUsername` ищет только локальную строку,
без Telegram-lookup. `SetDMState` меняет только состояние лички,
`updated_at` и, для `open`, `last_seen_at`.

## Admission: grants и invites

Grant-операции поддерживают идемпотентный переход одной строки
`(tg_id, resource)` в `pending`, `joined`, `left` или `revoked` при
сохранении одной строки на пару. Переход в `left` обновляет `state` и
`updated_at` — отдельной колонки времени выхода нет. Joined-строка от
approval сохраняет `admitted_by='bot'` и `joined_at`; external join
представим как `joined` с `admitted_by='external'`.

Invite-операции позволяют искать active invite по `invite_link_hash` и
resource, active shared link для resource, active personal/direct link
для `(tg_id, resource, mode)` и помечать link как `used` или
`used_by_other` с `attempted_by`. Lookup по hash не требует логировать
полный URL. Все эти операции пригодны для `*sql.Tx` через `DBTX`, чтобы
изменения grant/invite, audit, outbox и terminal update status
коммитились вместе (см. [grant-access](grant-access.md),
[invite-links](invite-links.md)).

## Outbox repository

Repository для `access_actions` (паттерн `NewX(q DBTX)`) умеет
идемпотентно ставить action, брать одно готовое действие lease'ом,
завершать его как `done`, перепланировать retry с новым `run_after`,
писать `last_error`, увеличивать счётчик попыток и помечать action
`dead`. Готовым считается `queued` с `run_after<=now` либо просроченный
`running` с `locked_until<now`. `payload_json` не интерпретируется за
пределами хранения/чтения. Подробности исполнения — в
[outbox-enforcer](outbox-enforcer.md).

## Invite links repository

Repository для `invite_links` (тот же паттерн) умеет искать активную
shared ссылку по resource, активную personal/direct по
`(tg_id, resource, mode)`, сохранять созданную Telegram-ссылку,
помечать строку `sent`/`used`/`used_by_other`/`revoked`/`expired`/`failed`
и выбирать истёкшие активные ссылки для обслуживания. Полный
`invite_link` хранится только в БД; `invite_link_hash` доступен
вызывающему коду для логов и аудита. Правила активной уникальности из
схемы остаются наблюдаемыми через методы repository.

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

## Tribute events inbox

Repository для `tribute_events` (тот же паттерн `NewX(q DBTX)`) даёт
webhook-обработчику inbox-семантику. Полученное событие вставляется с
`dedup_key`, именем события, опциональными `tg_id` и subscription id,
`signature_valid`, редактированным `payload_json` и `received_at`. Перед
обработкой существующее событие распознаётся по `dedup_key` —
идемпотентность держится `ON CONFLICT(dedup_key) DO NOTHING`, повторная
доставка не плодит строку. Событие доводится до терминального статуса
`processed`, `ignored` или `failed` с `processed_at` и опциональным
текстом ошибки. Набор статусов — ровно `received|processed|ignored|failed`;
`error` — это колонка, а не значение статуса. Failed-строки сохраняются
для расследования оператором, пока retention-правила явно не разрешат
очистку. Методы пригодны для `*sql.Tx`, поэтому изменение подписки,
audit-запись и терминальный статус события коммитятся одной транзакцией.

## Редакция raw payload

Перед записью `telegram_updates.payload_json` и
`tribute_events.payload_json` из raw JSON удаляются/маскируются PII и
секреты: email, `web_app_link`, поля адреса/трекинга, provider keys,
bot tokens, Telegram webhook secrets и полные invite-URL. Forensic-поля,
нужные для разбора (имя события, timestamps, `tg_id`, subscription id,
period id, status), сохраняются. Типизированные поля для доменной
обработки извлекаются до/во время парсинга, но в БД ложится только
редактированный payload. Технические логи следуют тому же правилу — raw
payload с нередактированными PII или секретами в логи не попадает.

## Ops read models

Для owner-команд `store` отдаёт узкие read-методы, не протаскивая
write-заботы в bot-обработчики: активные подписки по платформам; club
grants по resource и state; due/pending revocations; значения
`meta.health.*`; `meta.reconcile.last_run_at`; счётчики outbox по статусу
и число `dead`; открытые alerts с id, severity, kind, title, detail и
timestamps; известные чаты и сконфигурированные chat ID; CSV-экспорт
строк по users и текущим подпискам. Чтения, где размер результата может
расти, ограничены или пагинируются, и не включают raw provider payload
JSON в owner-сводки или CSV.

## Audit и alerts

`audit_log` фиксирует операторски важные события, например потерю и
восстановление прав бота в чате. `admin_alerts` хранит открытые и
закрытые тревоги. Для startup health используется дедуп:
`CreateOpenIfMissing` не плодит одинаковые открытые alerts при
повторных рестартах, а `ResolveOpenByTitle` закрывает stale-тревогу,
если права восстановились во время простоя.

## Время на границе

`store` — единственное место, где `time.Time` кодируется в SQL и обратно: запись `time.RFC3339` в UTC, чтение того же формата. Опциональные метки round-trip'ят через `*time.Time` с соответствием `NULL ↔ nil`. Метки, которым нужна суб-секундная упорядоченность (например `last_event_at`), round-trip'ят как RFC3339Nano.
