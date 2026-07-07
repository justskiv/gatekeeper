# Fix revocation transaction deadlock (decide/apply split)

## Why

Плановый reconcile отзыва доступа зависал в проде: `reconcile.revokeDue`
открывал write-транзакцию и внутри неё выполнял live-решение (`LiveSnapshot`),
а источники читают тот же connection pool — при `SetMaxOpenConns(1)` второй
запрос ждал единственное соединение, занятое той же транзакцией (self-deadlock).
Бот падал и не поднимался: durable due-revocation воспроизводила дедлок на
старте. Второй маршрут того же класса — `/sync`, запускавший reconcile внутри
update-транзакции поллера.

## What Changes

- Отзыв доступа разделён на две фазы: **decide** (live-статус источников и
  live-проверка protection, на пуле, вне транзакции) и **apply** (только
  tx-scoped запись, без live/pool/network I/O). Транзакция больше не
  удерживается через I/O, которым не управляет.
- Reconciler становится **pool-bound** и никогда не исполняется внутри чужой
  транзакции.
- `/sync` больше не запускает reconcile синхронно внутри update-транзакции:
  команда ставит post-commit job, reconcile выполняется на пуле после commit,
  итог приходит отдельным сообщением (admin log). **BREAKING** для UX ответа
  команды: немедленный ответ владельцу — «запущена», не итоговая сводка.
- Apply ревалидирует каждый grant против decide-снимка по идентичности: grant,
  изменившийся или появившийся после decide, пропускается, а pending revocation
  сохраняется для следующего прохода.

## Capabilities

### New Capabilities
- (нет)

### Modified Capabilities
- `reconciliation` — due-revocation исполняется через decide→apply фазы
  access-revocation flow; reconciler pool-bound, decide вне транзакции.
- `bot-commands` — `/sync` выполняет reconcile асинхронно (post-commit, на
  пуле), результат — отдельным сообщением.
- `access-revocation` — добавлена ревалидация grant-снимка при apply (пропуск
  изменённых или появившихся grants с сохранением pending).

## Impact

- Код: `internal/engine` (примитивы decide/apply), `internal/reconcile`
  (pool-bound `New`, `revokeDue`), `internal/store` (`WithTx`, `updated_at` в
  проекции grant), `internal/telegram` (`/sync` post-commit),
  `internal/messages` (`SyncStarted`).
- Схема БД не меняется — используется существующая колонка
  `access_grants.updated_at`.
- Конфигурация, DSN и `SetMaxOpenConns(1)` не меняются (single-connection —
  осознанный дизайн, инвариант сохранён).
