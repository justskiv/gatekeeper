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
- `Users.FindByUsername(ctx, username) (User, ok, error)`
- `Users.SetDMState(ctx, tgID, state) error`
- `Subscriptions.Create(ctx, Subscription) (id, error)`
- `Subscriptions.GetActive(ctx, tgID, platform) (Subscription, ok, error)`
- `Subscriptions.UpsertActive(ctx, Subscription) (id, error)`
- `Subscriptions.ExpireActive(ctx, tgID, platform, endedAt, signal) (ok, error)`
- `Subscriptions.ListActiveByUser(ctx, tgID) ([]Subscription, error)`
- `Subscriptions.ListByUser(ctx, tgID) ([]Subscription, error)`
- `Grants.Upsert(ctx, AccessGrant) error`
- `Grants.Get(ctx, tgID, resource) (AccessGrant, error)`
- `Grants.ListByUser(ctx, tgID) ([]AccessGrant, error)`
- `Whitelist.Has(ctx, tgID) (bool, error)`
- `Revocations.Get(ctx, tgID) (PendingRevocation, ok, error)`
- `Revocations.Delete(ctx, tgID) error`
- `Meta.Get(ctx, key) (value, ok, error)`
- `Meta.Set(ctx, key, value) error`
- `Meta.GetUpdateOffset(ctx) (offset, ok, error)`
- `Meta.SetUpdateOffset(ctx, offset) error`
- `Meta.SetHealth(ctx, key, value) error`
- `TelegramUpdates.InsertBatch(ctx, updates, nextOffset) error`
- `TelegramUpdates.ListPending(ctx, limit) ([]TelegramUpdate, error)`
- `TelegramUpdates.MarkTerminal(ctx, updateID, status, errorText) error`
- `Audit.Append(ctx, AuditEntry) error`
- `Audit.ListRecentByUser(ctx, tgID, limit) ([]AuditEntry, error)`
- `Alerts.Create(ctx, AlertInput) (id, error)`

Методы `Subscriptions.UpsertActive`/`ExpireActive`/`ListActiveByUser`/
`ListByUser`, `Users.FindByUsername`, `Grants.ListByUser`,
`Audit.ListRecentByUser`, `Whitelist.Has` и `Revocations.Get` нужны
доменному ядру (`status-core`) и командам `/status`/`/whois`. История
подписок append-only: `UpsertActive` MUST обновлять активную строку
`(tg_id, platform)` вместо создания второй, не нарушая частичный
уникальный индекс `idx_subscriptions_active_unique`; `ExpireActive` MUST
переводить активную строку в `status='expired'` с `ended_at`, не удаляя
её, и MUST возвращать признак, была ли закрыта строка. Чтения для
команд (`ListByUser`, `ListRecentByUser`) MUST быть упорядочены от новых
записей к старым. Полные наборы методов прочих repository добавят фазы,
которым они нужны.

#### Scenario: Getter по отсутствующей строке
- **WHEN** метод `Get` вызывается для ключа, у которого нет строки
- **THEN** возвращённая ошибка оборачивает `ErrNotFound`

#### Scenario: Upsert идемпотентен по ключу
- **WHEN** `Users.Upsert` дважды вызывается с одним `tg_id`
- **THEN** второй вызов обновляет изменяемые колонки вместо падения на
  primary key

#### Scenario: Поиск по username не ходит в Telegram
- **WHEN** `/whois @username` ищет пользователя
- **THEN** `Users.FindByUsername` ищет только локальную строку в БД
- **AND** внешний Telegram-lookup не выполняется

#### Scenario: DM-state обновляется узко
- **WHEN** `Users.SetDMState` обновляет состояние личной переписки
  пользователя
- **THEN** меняются только `dm_state`, `updated_at` (и при необходимости
  `last_seen_at`), а кэш профиля сохраняется

#### Scenario: UpsertActive не нарушает уникальность активной подписки
- **WHEN** `Subscriptions.UpsertActive` вызывается для уже активной пары
  `(tg_id, platform)`
- **THEN** существующая строка обновляется, вторая активная не создаётся

#### Scenario: ExpireActive идемпотентен
- **WHEN** `Subscriptions.ExpireActive` вызывается повторно для уже
  закрытой или отсутствующей активной строки
- **THEN** метод возвращает `ok=false` без ошибки

#### Scenario: Активные подписки читаются по всем платформам
- **WHEN** `Subscriptions.ListActiveByUser` вызывается для пользователя с
  активными подписками на нескольких платформах
- **THEN** возвращаются все активные строки этого пользователя

#### Scenario: История подписок упорядочена для показа
- **WHEN** `Subscriptions.ListByUser` читает подписки пользователя
- **THEN** результат пригоден для `/status` и `/whois` и отсортирован от
  новых периодов к старым

#### Scenario: Get по отсутствующему отзыву различим
- **WHEN** `Revocations.Get` вызывается для пользователя без
  `pending_revocation`
- **THEN** возвращается признак отсутствия (`ok=false`) без ошибки

#### Scenario: Недавний audit ограничен и упорядочен
- **WHEN** `Audit.ListRecentByUser` вызывается с `limit`
- **THEN** возвращается не больше `limit` записей, отсортированных от
  новых к старым

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

### Requirement: Outbox repository exposes durable action operations

`store` MUST предоставить repository для `access_actions`, построенный
по существующему паттерну `NewX(q DBTX)`. Repository MUST уметь
идемпотентно ставить action, брать одно готовое действие lease'ом,
завершать action как `done`, перепланировать retry с новым
`run_after`, записывать `last_error`, увеличивать счётчик попыток и
помечать action как `dead`.

Repository MUST кодировать timestamp-колонки в RFC3339 UTC и не
интерпретировать `payload_json` за пределами хранения/чтения. Выборка
готового действия MUST учитывать `queued` строки с `run_after<=now` и
просроченные `running` строки с `locked_until<now`.

#### Scenario: Enqueue идемпотентен
- **WHEN** `Outbox.Enqueue` вызывается дважды с одним
  `idempotency_key`
- **THEN** в таблице `access_actions` остаётся одна строка
- **AND** caller может отличить новую вставку от существующей строки

#### Scenario: Lease не выдаёт одно действие двум воркерам
- **WHEN** два воркера одновременно вызывают lease ready action
- **THEN** только один получает конкретный action
- **AND** строка получает `status='running'` и `locked_until`

#### Scenario: Retry сохраняет причину и время следующего запуска
- **WHEN** action перепланируется после retryable ошибки
- **THEN** `attempts` увеличивается
- **AND** `last_error` и новый `run_after` сохраняются в БД

### Requirement: Invite links repository exposes active-link operations

`store` MUST предоставить repository для `invite_links`, построенный
по паттерну `NewX(q DBTX)`. Repository MUST уметь искать активную
shared ссылку по resource, искать активную personal/direct ссылку по
`tg_id`, resource и mode, сохранять созданную Telegram-ссылку, отмечать
ссылку как `sent`, `used`, `used_by_other`, `revoked`, `expired` или
`failed`, и выбирать истёкшие активные ссылки для обслуживания.

Repository MUST сохранять полный `invite_link` только в БД, а
`invite_link_hash` MUST быть доступен вызывающему коду для логов и
аудита. Правила активной уникальности из схемы MUST оставаться
наблюдаемыми через методы repository.

#### Scenario: Active shared lookup возвращает одну ссылку
- **WHEN** в БД есть активная `shared_join_request` ссылка для resource
- **THEN** repository возвращает её как текущую ссылку ресурса

#### Scenario: Истёкшая personal ссылка освобождает слот
- **WHEN** personal ссылка помечена `expired`
- **THEN** repository позволяет сохранить новую active ссылку для того
  же пользователя, resource и mode

#### Scenario: Mark failed сохраняет last_error
- **WHEN** создание или отзыв invite-ссылки завершается ошибкой
- **THEN** repository может пометить строку `failed`
- **AND** `last_error` содержит диагностическое сообщение

