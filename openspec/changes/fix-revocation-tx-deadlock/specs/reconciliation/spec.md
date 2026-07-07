## MODIFIED Requirements

### Requirement: Reconciler executes due revocations through revokeNow

Reconciler MUST выбирать `pending_revocations` с `scheduled_at <= now` и для
каждой записи исполнять отзыв через access-revocation flow (`revokeNow`),
реализованный как две фазы: **decide** (live-статус источников и protection, на
пуле, ВНЕ транзакции) и **apply** (запись, в транзакции). Reconciler MUST NOT
сам принимать решение о kick, менять `access_grants` в обход этого flow или
выполнять Telegram-вызовы напрямую.

Reconciler MUST быть pool-bound: он владеет собственными короткими
транзакциями и MUST NOT исполняться внутри чужой транзакции. Decide-фаза MUST
выполняться до открытия транзакции: live/pool-обращения внутри транзакции при
единственном соединении приводят к self-deadlock.

Если flow не может безопасно исполнить отзыв из-за `unknown` или потери прав,
Reconciler MUST сохранить состояние, достаточное для повторной попытки, и
поднять durable `admin_alert`.

#### Scenario: Due revocation исполняется через access-revocation flow
- **WHEN** есть `pending_revocation` с `scheduled_at` в прошлом
- **THEN** Reconciler исполняет отзыв через `revokeNow` flow (decide→apply)
- **AND** `soft_kick` ставится только внутри access-revocation flow

#### Scenario: Reconciler не делает Telegram-вызовы напрямую
- **WHEN** due revocation требует удаления пользователя из ресурса
- **THEN** Reconciler не вызывает Telegram API
- **AND** исходящее действие появляется как durable outbox action

#### Scenario: Live-решение считается вне транзакции
- **WHEN** Reconciler исполняет due revocation
- **THEN** live-статус источников и protection считаются до открытия транзакции
- **AND** транзакция содержит только запись, без live/pool/network обращений
