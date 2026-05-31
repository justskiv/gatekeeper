## MODIFIED Requirements

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
