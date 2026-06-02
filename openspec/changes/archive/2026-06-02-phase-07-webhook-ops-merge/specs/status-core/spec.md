## MODIFIED Requirements

### Requirement: handleEvent применяет нормализованное событие подписки

`handleEvent(SubscriptionEvent)` MUST в одной handler-транзакции (`tx2`)
обновить `users`, записать `audit_log`, по `Kind` управлять строкой
`subscriptions` и затем вызвать событийный пересчёт `recomputeAccess`.
Событие MAY нести `EventAt`, `ExpiresAt`, `ExternalID`, `PeriodID`,
`Tier`, `Signal` и provider event name.

Для `Activated` обработчик MUST обновить активную подписку
`(tgID, platform)` или создать её (`status='active'`, `started_at=now`);
для `Deactivated` — перевести активную подписку в `status='expired'` с
`ended_at=now`. Обработка MUST быть идемпотентной: повтор события не
создаёт вторую активную строку, а повторная деактивация отсутствующей
активной строки — no-op.

Для Tribute webhook events обработчик MUST использовать
`EventAt=event.created_at` и записывать его в
`subscriptions.last_event_at`. `Activated` MUST применяться только если
`EventAt` новее текущего `last_event_at` этой активной Tribute
подписки; старое или равное событие MUST быть no-op для subscription
period и MUST NOT укорачивать `expires_at`. Новое
`new_subscription`/`renewed_subscription` MUST сохранить
`expires_at`, `external_id`, `external_period_id`, tier и
`last_signal='webhook'`.

`cancelled_subscription` MUST быть отдельным provider event. При
`TRIBUTE_CANCEL_IS_IMMEDIATE=false` обработчик MUST записать факт отмены
в `audit_log`, сохранить активную Tribute subscription и не сокращать
`expires_at`; при отсутствии активной строки expired-строка не
создаётся. При `TRIBUTE_CANCEL_IS_IMMEDIATE=true` обработчик MUST
применить cancel как `Deactivated`.

#### Scenario: Activated создаёт активную подписку
- **WHEN** приходит `Activated` для пользователя без активной подписки на
  этой платформе
- **THEN** создаётся строка `subscriptions` со `status='active'` и
  `started_at`
- **AND** пишется `audit_log(subscription_activated)`

#### Scenario: Deactivated закрывает активную подписку
- **WHEN** приходит `Deactivated` при наличии активной подписки
- **THEN** строка переходит в `status='expired'` с `ended_at`
- **AND** пишется `audit_log(subscription_expired)`

#### Scenario: Повторный Activated идемпотентен
- **WHEN** `Activated` для той же `(tgID, platform)` приходит повторно
- **THEN** обновляется существующая активная строка, вторая активная не
  создаётся (частичный уникальный индекс соблюдён)

#### Scenario: Tribute webhook Activated сохраняет expires_at
- **WHEN** приходит `new_subscription` или `renewed_subscription` с
  `EventAt`, `ExpiresAt`, `subscription_id`, `period_id` и tier
- **THEN** активная Tribute subscription хранит эти значения
- **AND** `last_signal='webhook'`
- **AND** `last_event_at` равен `EventAt`

#### Scenario: Устаревшее Tribute событие не укорачивает expires_at
- **WHEN** активная Tribute subscription имеет более новый
  `last_event_at`, чем входящий webhook `EventAt`
- **THEN** обработчик не меняет `expires_at`
- **AND** не запускает отзыв доступа из-за старого события

#### Scenario: Renewed subscription продлевает expires_at
- **WHEN** `renewed_subscription` имеет `EventAt` новее текущего
  `last_event_at` и более поздний `ExpiresAt`
- **THEN** active Tribute subscription обновляется до нового
  `expires_at`
- **AND** `recomputeAccess` видит active source

#### Scenario: cancelled_subscription не отзывает доступ сразу
- **WHEN** приходит особое событие отмены Tribute
  (`cancelled_subscription`) и `TRIBUTE_CANCEL_IS_IMMEDIATE=false`
- **THEN** факт отмены пишется в `audit_log`, `expires_at` активной
  подписки не сокращается
- **AND** активная строка не переводится в `expired`
- **AND** отзыв доступа не запускается до наступления `expires_at`

#### Scenario: Immediate cancel override отзывает доступ
- **WHEN** приходит `cancelled_subscription` и
  `TRIBUTE_CANCEL_IS_IMMEDIATE=true`
- **THEN** событие применяется как `Deactivated`
- **AND** `recomputeAccess` запускает обычный inactive path, если других
  active sources нет

#### Scenario: Cancel без активной строки не создаёт expired period
- **WHEN** приходит `cancelled_subscription` для пользователя без
  active Tribute subscription
- **THEN** expired subscription row не создаётся
- **AND** audit фиксирует provider cancellation как no-op
