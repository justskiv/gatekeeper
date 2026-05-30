# storage Specification

## Purpose

Описывает жизненный цикл SQLite-соединения, проверку готовности схемы и
repository-границу над схемой БД v1.

## Requirements

### Requirement: SQLite database is opened with the required pragmas

База MUST открываться через `database/sql` с драйвером
`modernc.org/sqlite` (зарегистрирован как `sqlite`). DSN MUST задавать
`journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`,
`synchronous=NORMAL` и `_txlock=immediate` (каждая транзакция MUST
начинаться как `BEGIN IMMEDIATE`). Пул MUST быть ограничен одним
соединением (`SetMaxOpenConns(1)`) с `ConnMaxLifetime` в один час, чтобы
WAL checkpoint'ы могли выполняться.

#### Scenario: База успешно открывается
- **WHEN** `Open(ctx, dbPath)` вызван для доступного на запись пути
- **THEN** возвращается `*sql.DB` с `foreign_keys=ON` и пулом в одно
  соединение
- **AND** последующий `PingContext` успешен

#### Scenario: Каталога базы нет
- **WHEN** родительский каталог `dbPath` отсутствует
- **THEN** `Open` создаёт его и родителей с правами `0700`

### Requirement: Database file is created with mode 0600

Файл базы MUST NOT быть world-readable. `Open` MUST заранее создать
файл с правами `0600` перед передачей пути SQLite-драйверу (иначе
драйвер создал бы файл с дефолтными `0644`).

#### Scenario: Новый файл базы
- **WHEN** `Open` вызван для пути, где файла ещё нет
- **THEN** файл создаётся с permission bits `0600`

### Requirement: Schema-presence check refuses an unmigrated database

Приложение MUST NOT применять DDL самостоятельно; оно MUST только
проверять, что схема уже на месте. `CheckSchema` MUST читать служебную
таблицу goose (`goose_db_version`) и MUST возвращать `ErrUnmigrated`,
если таблицы нет или `max(version_id) WHERE is_applied = 1` меньше `1`.
Текст ошибки MUST подсказывать оператору выполнить `task migrate:up`.

#### Scenario: Пустая база
- **WHEN** `CheckSchema` выполняется на свежей немигрированной базе
- **THEN** возвращённая ошибка оборачивает `ErrUnmigrated`
- **AND** текст ошибки содержит строку `"task migrate:up"`

#### Scenario: Мигрированная база
- **WHEN** `CheckSchema` выполняется после `migrate up`, применившего
  хотя бы одну миграцию
- **THEN** вызов возвращает `nil`

### Requirement: Initial schema defines the v1 data model

Миграция `0001_init.sql` MUST создавать 12 таблиц, от которых зависит
остальная система: `users`, `subscriptions`, `access_grants`,
`pending_revocations`, `whitelist`, `invite_links`, `telegram_updates`,
`tribute_events`, `access_actions`, `audit_log`, `admin_alerts`,
`meta`. Все timestamp-колонки MUST хранить строки RFC3339 в UTC.
Доменные enum'ы (`platform`, `state`, `mode`, `status`, `severity`,
`action_type`) MUST фиксироваться через `CHECK` constraints.

#### Scenario: Таблицы существуют после миграции
- **WHEN** тестовый harness открывает свежую базу и запускает goose `Up`
- **THEN** все 12 таблиц есть в `sqlite_master`
- **AND** `goose_db_version` содержит хотя бы одну применённую миграцию

### Requirement: At most one active subscription per (user, platform)

Схема MUST отклонять вторую активную подписку для той же пары
`(tg_id, platform)`. Это обеспечивает partial unique index
`idx_subscriptions_active_unique` на
`subscriptions(tg_id, platform) WHERE status = 'active'`. Истёкшие
подписки MUST NOT занимать слот, а один пользователь MUST иметь
возможность держать активные подписки на разных платформах.

#### Scenario: Дубликат активной подписки
- **WHEN** активная подписка уже есть для `(user, platform)`, и вторая
  активная подписка вставляется для той же пары
- **THEN** вставка падает с unique-constraint violation

#### Scenario: Активная подписка после истёкшей
- **WHEN** активная подписка для `(user, platform)` была истекшей
  (`status='expired'`, `ended_at` заполнен)
- **THEN** новая активная подписка для той же пары разрешена

### Requirement: Invite links honour per-mode active uniqueness

Два partial unique index'а на `invite_links` MUST обеспечивать одну
активную ссылку на слот, где «активная» означает
`status IN ('created','sent')`:

- `shared_join_request`: максимум одна активная ссылка на
  `(resource, mode)`; колонка `tg_id` MUST быть `NULL` для shared
  ссылок.
- `personal_join_request` и `direct`: максимум одна активная ссылка на
  `(tg_id, resource, mode)`; `tg_id` MUST быть `NOT NULL` для этих
  режимов.

`CHECK` constraint MUST фиксировать правило nullable/non-nullable
`tg_id` по режиму.

#### Scenario: Вторая активная shared-ссылка отклоняется
- **WHEN** активная shared-ссылка уже есть для
  `(chat, shared_join_request)`, и вторая активная shared-ссылка
  вставляется для той же пары
- **THEN** вставка падает с unique-constraint violation

#### Scenario: Personal-слот освобождён истёкшей ссылкой
- **WHEN** предыдущая personal-ссылка для `(user, resource, mode)`
  помечена `expired`
- **THEN** новая активная personal-ссылка для той же тройки разрешена

### Requirement: Foreign keys are enforced

`PRAGMA foreign_keys` MUST быть `1` на каждом соединении, чтобы
referential integrity проверялась при записи.

#### Scenario: Подписка без пользователя
- **WHEN** вставляется строка подписки, ссылающаяся на несуществующий
  `tg_id`
- **THEN** вставка падает с foreign-key violation

### Requirement: Repositories expose narrow methods on the v1 schema

`store` MUST поставлять рукописные repository-типы для таблиц, которые
нужны рантайм-фазам. Механизм транзакционности фиксируется так: пакет
объявляет узкий querier-интерфейс `DBTX` (методы `ExecContext`,
`QueryContext`, `QueryRowContext`), удовлетворяемый и `*sql.DB`, и
`*sql.Tx`; repository конструируется хелпером `NewX(q DBTX)` поверх
этого исполнителя. Чтобы выдержать инвариант I1 (`telegram-transport`),
поллер открывает `tx2` и конструирует repo обработчика **поверх этой
`*sql.Tx`**, поэтому доменные изменения и терминальный
`telegram_updates.status` коммитятся атомарно. Исполнитель приходит из
конструктора, поэтому сигнатуры методов остаются на `ctx` (без
per-call executor-аргумента).

Доступны MUST быть методы:

- `Users.Upsert(ctx, User) error`
- `Users.Get(ctx, tgID) (User, error)`
- `Users.SetDMState(ctx, tgID, state) error`
- `Subscriptions.Create(ctx, Subscription) (id, error)`
- `Subscriptions.GetActive(ctx, tgID, platform) (Subscription, ok, error)`
- `Grants.Upsert(ctx, AccessGrant) error`
- `Grants.Get(ctx, tgID, resource) (AccessGrant, error)`
- `Meta.Get(ctx, key) (value, ok, error)`
- `Meta.Set(ctx, key, value) error`
- `Meta.GetUpdateOffset(ctx) (offset, ok, error)`
- `Meta.SetUpdateOffset(ctx, offset) error`
- `Meta.SetHealth(ctx, key, value) error`
- `TelegramUpdates.InsertBatch(ctx, updates, nextOffset) error`
- `TelegramUpdates.ListPending(ctx, limit) ([]TelegramUpdate, error)`
- `TelegramUpdates.MarkTerminal(ctx, updateID, status, errorText) error`
- `Audit.Append(ctx, AuditEntry) error`
- `Alerts.Create(ctx, AlertInput) (id, error)`

Остальные repository (`Revocations`, `Whitelist`) конструируемы в этой
фазе, но их полные наборы методов добавят фазы, которым они нужны.

#### Scenario: Getter по отсутствующей строке
- **WHEN** метод `Get` вызывается для ключа, у которого нет строки
- **THEN** возвращённая ошибка оборачивает `ErrNotFound`

#### Scenario: Upsert идемпотентен по ключу
- **WHEN** `Users.Upsert` дважды вызывается с одним `tg_id`
- **THEN** второй вызов обновляет изменяемые колонки вместо падения на
  primary key

#### Scenario: DM-state обновляется узко
- **WHEN** `Users.SetDMState` обновляет состояние личной переписки
  пользователя
- **THEN** меняются только `dm_state`, `updated_at` (и при необходимости
  `last_seen_at`), а кэш профиля сохраняется

#### Scenario: Telegram batch insert атомарно продвигает offset
- **WHEN** `TelegramUpdates.InsertBatch` сохраняет батч, заканчивающийся
  на `update_id = 42`
- **THEN** каждая новая строка зафиксирована со статусом `pending` и
  заполненным `payload_json`
- **AND** `meta.update_offset` зафиксирован как `43` в той же транзакции

#### Scenario: Terminal status делит транзакцию с состоянием handler'а
- **WHEN** обработчик меняет строку `users` и помечает обновление
  `processed`
- **THEN** обе записи можно зафиксировать в одной SQL-транзакции

### Requirement: Timestamps cross the boundary as RFC3339 UTC

Пакет `store` MUST владеть единственными местами, где `time.Time`
кодируется в SQL-строку и декодируется обратно. Запись MUST быть
`time.RFC3339` в UTC; чтение MUST парсить тот же формат. Опциональные
timestamp'ы MUST round-trip'иться через `*time.Time` с соответствием
`NULL` ↔ `nil`.

#### Scenario: Round-trip опционального timestamp
- **WHEN** строка с non-nil `ExpiresAt` записана и прочитана обратно
- **THEN** возвращённый `*time.Time` non-nil и равен исходному значению
  с точностью до секунды RFC3339

#### Scenario: NULL отображается в nil
- **WHEN** строка хранит `NULL` в опциональной timestamp-колонке
- **THEN** декодированное значение равно nil `*time.Time`
