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

### Requirement: Admission repositories предоставляют grant и invite operations

`store` MUST предоставлять repository-операции, достаточные для
grant-access handlers, чтобы атомарно обновлять admission state внутри
handler transactions. Grants operations MUST поддерживать идемпотентный
переход одной строки `(tg_id, resource)` в `pending`, `joined`, `left`
или `revoked` при сохранении одной строки на пару. Переход в `left`
MUST обновлять `state` и `updated_at`, не требуя несуществующей колонки
времени выхода. Invite operations MUST позволять искать active invite
по `invite_link_hash` и resource, active shared link для resource,
active personal/direct link для `(tg_id, resource, mode)`, а также
помечать link как `used` или `used_by_other` с `attempted_by`.

Эти операции MUST использовать существующие таблицы и правила кодировки
timestamps. Они MUST быть пригодны для `*sql.Tx` через существующий
`DBTX` pattern, чтобы изменения grant и invite status, audit rows,
outbox rows и terminal update status коммитились вместе.

#### Scenario: Pending grant upsert идемпотентен
- **WHEN** grant-access flow больше одного раза помечает ту же пару
  `(tg_id, resource)` как `pending`
- **THEN** `access_grants` содержит одну строку для этой пары
- **AND** последний update не создаёт дубль

#### Scenario: Joined grant сохраняет bot admission metadata
- **WHEN** join-request approval помечает grant как `joined`
- **THEN** строка сохраняет `admitted_by='bot'` и `joined_at`
- **AND** операция может выполняться в той же транзакции, что и
  постановка `approve_join` в outbox

#### Scenario: Left grant использует существующие колонки
- **WHEN** club membership handler наблюдает выход пользователя
- **THEN** grant может перейти в `left` через `state` и `updated_at`
- **AND** операция не пишет отдельный timestamp выхода

#### Scenario: External join представим в схеме
- **WHEN** club membership handler наблюдает добавление пользователя
  помимо admission через бота
- **THEN** grant может быть сохранён как `joined` с
  `admitted_by='external'`

#### Scenario: Invite lookup by hash не раскрывает полный URL
- **WHEN** join-request handler разрешает present `invite_link`
- **THEN** store может найти active invite row по hash и resource
- **AND** callers не нужно логировать полный invite URL

#### Scenario: Personal misuse сохраняет attempted_by
- **WHEN** personal invite используется другим tg_id
- **THEN** store может пометить invite как `used_by_other`
- **AND** `attempted_by` сохраняет tg_id, который попытался её
  использовать

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

### Requirement: Revocation repository supports due scheduling and cancellation

`store` MUST provide repository operations for `pending_revocations`
using the existing `DBTX` pattern. The repository MUST support
idempotent create-if-absent, get by `tg_id`, delete by `tg_id`, list
due revocations ordered by `scheduled_at`, and mark warning as notified
without creating duplicate rows.

All timestamp values MUST cross the SQL boundary as RFC3339 UTC. Create
and delete operations MUST be safe inside handler transactions so
subscription changes, audit rows, outbox rows and revocation state can
commit atomically.

#### Scenario: Create pending revocation is idempotent
- **WHEN** `Revocations.CreateIfAbsent` is called twice for one `tg_id`
- **THEN** `pending_revocations` contains one row
- **AND** the original `scheduled_at` is preserved unless caller
  explicitly reschedules

#### Scenario: Due revocations are ordered
- **WHEN** Reconciler asks for due revocations at time `now`
- **THEN** repository returns rows with `scheduled_at <= now`
- **AND** rows are ordered by `scheduled_at` from oldest to newest

#### Scenario: Delete missing revocation is no-op
- **WHEN** `Revocations.Delete` is called for a user without pending
  revocation
- **THEN** the method returns success without changing other rows

### Requirement: Access-control repositories support manual grants and bans

`store` MUST expose narrow operations for owner access commands:
whitelist add/remove/check, manual subscription upsert/expire with
optional `expires_at`, `users.banned` set/unset, and listing grants
eligible for revoke by `tg_id`. These operations MUST be usable inside
the same transaction as audit rows and outbox actions.

`/grant` by numeric `tg_id` MUST be able to create a minimal stub user
without username or profile fields. Username lookup MUST remain local
through `Users.FindByUsername` and MUST NOT call Telegram.

#### Scenario: Manual subscription with expiry can be upserted
- **WHEN** owner grants a user manual access until a timestamp
- **THEN** store creates or updates one active `manual` subscription for
  that user
- **AND** `expires_at` is preserved as RFC3339 UTC

#### Scenario: Whitelist removal is idempotent
- **WHEN** owner revokes whitelist for a user who is not whitelisted
- **THEN** repository returns success without deleting other manual
  access data

#### Scenario: Banned flag updates narrowly
- **WHEN** owner bans or unbans a user
- **THEN** store changes `users.banned` and `updated_at`
- **AND** profile, username and dm state are preserved

### Requirement: Alerts repository deduplicates and supports delivery

`Alerts.Create` MUST support stable dedupe keys for open alerts. If an
open alert with the same key already exists, creation MUST return the
existing alert or explicit duplicate result without creating a second
open row. When a new alert is created, callers MUST be able to enqueue
operator delivery in the same transaction.

Alert rows MUST retain severity, kind, machine-readable metadata and
status. Resolving an alert MUST update only alert status/resolution
fields and MUST NOT delete forensic context.

#### Scenario: Duplicate open alert is deduplicated
- **WHEN** the same alert kind and dedupe key are raised twice
- **THEN** there is at most one open alert for that key
- **AND** duplicate creation does not require duplicate owner delivery

#### Scenario: Alert and delivery share one transaction
- **WHEN** a critical alert is created and owner delivery is needed
- **THEN** alert row and `send_dm` or admin-log outbox action can be
  committed atomically

#### Scenario: Resolve preserves alert context
- **WHEN** owner resolves an alert
- **THEN** status changes to resolved
- **AND** original kind, severity and metadata remain readable

### Requirement: Cleanup repository operations preserve forensic rows

`store` MUST provide cleanup operations for retention without embedding
policy in SQL call sites. Cleanup MUST support deleting old terminal
`telegram_updates` and `tribute_events`, deleting old done
`access_actions`, deleting resolved old alerts, applying
`AUDIT_RETENTION` to `audit_log`, expiring personal/direct invite links
and running WAL checkpoint.

Cleanup operations MUST NOT automatically delete failed inbox rows or
dead outbox actions. Those rows remain available for operator
investigation.

#### Scenario: Processed inbox rows can be deleted by cutoff
- **WHEN** cleanup receives a cutoff from `RAW_RETENTION`
- **THEN** processed or ignored inbox rows older than cutoff are
  deleted
- **AND** failed rows are left untouched

#### Scenario: Dead actions survive cleanup
- **WHEN** an `access_actions` row has `status='dead'`
- **THEN** cleanup does not delete it only because it is old

#### Scenario: Expired personal links are selected for maintenance
- **WHEN** active personal or direct invite links have
  `expires_at <= now`
- **THEN** repository can mark them `expired` and return links needing
  `revoke_invite`
