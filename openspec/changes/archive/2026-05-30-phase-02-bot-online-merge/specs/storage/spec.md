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
