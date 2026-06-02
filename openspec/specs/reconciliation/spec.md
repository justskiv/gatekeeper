# reconciliation Specification

## Purpose

Описывает периодическую сверку durable state с Telegram-состоянием,
health-checks, восстановление invite links и cleanup-проходы процесса
`gatekeeper`.

## Requirements

### Requirement: Reconciler runs periodically and once on startup

Gatekeeper MUST запускать Reconciler с периодом `RECONCILE_INTERVAL`.
После successful startup wiring и до запуска poller loop система MUST
выполнить один reconciliation pass, чтобы закрыть gap простоя и
исполнить due revocations, накопившиеся во время выключенного процесса.

Каждый pass MUST быть идемпотентным: повтор после рестарта не должен
создавать duplicate due revocation execution, duplicate verify actions
или duplicate shared invite ensures. В конце successful pass система
MUST записать `meta.reconcile.last_run_at` и audit summary с
машиночитаемыми счётчиками.

#### Scenario: Первый проход выполняется до poller loop
- **WHEN** runtime успешно собрал dependencies и Enforcer готов
- **THEN** Reconciler выполняет один pass до запуска long polling
- **AND** due revocations, накопленные до старта, ставятся на исполнение

#### Scenario: Успешный проход обновляет last run
- **WHEN** reconciliation pass завершился без фатальной ошибки
- **THEN** `meta.reconcile.last_run_at` равен времени прохода
- **AND** audit содержит summary reconciliation pass

### Requirement: Reconciler executes due revocations through revokeNow

Reconciler MUST выбирать `pending_revocations` с
`scheduled_at <= now` и для каждой записи вызывать `revokeNow` из
access-revocation flow. Reconciler MUST NOT сам принимать решение о
kick, менять `access_grants` в обход `revokeNow` или выполнять
Telegram-вызовы напрямую.

Если `revokeNow` не может безопасно исполнить отзыв из-за `unknown` или
потери прав, Reconciler MUST сохранить состояние, достаточное для
повторной попытки, и поднять durable `admin_alert`.

#### Scenario: Due revocation исполняется через revokeNow
- **WHEN** есть `pending_revocation` с `scheduled_at` в прошлом
- **THEN** Reconciler вызывает `revokeNow`
- **AND** `soft_kick` ставится только внутри access-revocation flow

#### Scenario: Reconciler не делает Telegram-вызовы напрямую
- **WHEN** due revocation требует удаления пользователя из ресурса
- **THEN** Reconciler не вызывает Telegram API
- **AND** исходящее действие появляется как durable outbox action

### Requirement: Reconciler schedules membership verification candidates

Reconciler MUST формировать candidates для сверки из union известных
релевантных пользователей: active subscriptions, bot-admitted joined
grants и whitelist rows. Для каждого candidate Reconciler MUST ставить
idempotent `verify_member` actions через outbox, а не выполнять
`getChatMember` самостоятельно.

Результаты `verify_member` MUST применяться теми же domain paths, что и
обычные membership events: source membership обновляет subscriptions и
запускает `recomputeAccess`, club membership обновляет
`access_grants`. `unknown` при verify MUST NOT закрывать active
subscription или отзывать доступ.

#### Scenario: Active subscription попадает в candidates
- **WHEN** у пользователя есть active subscription
- **THEN** Reconciler ставит verify work для этого пользователя
- **AND** duplicate pass не создаёт duplicate verify action с тем же
  смысловым ключом

#### Scenario: Verify unknown не отзывает доступ
- **WHEN** verify action получает сетевую ошибку или потерю прав
- **THEN** результат применяется как `unknown`
- **AND** active subscription и grants не закрываются

### Requirement: Reconciler maintains chat health and invite links

Каждый reconciliation pass MUST проверять health четырёх настроенных
чатов через тот же chat-health contract, который используется при
startup и `my_chat_member`. Результат MUST обновлять стабильные ключи
`meta.health.*`; потеря прав MUST создавать durable `admin_alert`, но
MUST NOT падать весь процесс.

При `INVITE_MODE=shared_join_request` Reconciler MUST проверять наличие
активной shared join-request ссылки для club chat и club channel. Если
активной ссылки нет, он MUST поставить idempotent `ensure_invite`.
При `personal_join_request` или `direct` Reconciler MUST переводить
истёкшие active links в `expired` и при необходимости ставить
`revoke_invite`.

#### Scenario: Потеря прав обновляет health и alert
- **WHEN** health-check показывает, что бот потерял admin права в club
  channel
- **THEN** `meta.health.club_channel` отражает failure
- **AND** создаётся critical `admin_alert`
- **AND** reconciliation pass продолжает остальные проверки

#### Scenario: Missing shared link восстанавливается через outbox
- **WHEN** в shared mode нет active invite link для club chat
- **THEN** Reconciler ставит `ensure_invite` для chat resource
- **AND** Telegram API напрямую из Reconciler не вызывается

#### Scenario: Истёкшая personal link закрывается
- **WHEN** personal invite link имеет `expires_at <= now`
- **THEN** ссылка переводится в `expired`
- **AND** при необходимости ставится `revoke_invite`

### Requirement: Cleanup ticker applies retention and maintenance

Gatekeeper MUST запускать cleanup loop с периодом `CLEANUP_INTERVAL`.
Cleanup MUST удалять terminal raw inbox rows старше `RAW_RETENTION`,
удалять done outbox actions старше retention окна, очищать старые
resolved alerts, истекать personal/direct invite links, применять
`AUDIT_RETENTION` к audit log и выполнять SQLite WAL checkpoint.

Cleanup MUST NOT автоматически удалять `telegram_updates` или
`tribute_events` со status `failed`, а также `access_actions` со
status `dead`: эти строки остаются forensic material до ручного разбора.

#### Scenario: Terminal processed updates очищаются по retention
- **WHEN** `telegram_updates.status` равен `processed` или `ignored` и
  строка старше `RAW_RETENTION`
- **THEN** cleanup может удалить эту строку

#### Scenario: Failed update не удаляется автоматически
- **WHEN** `telegram_updates.status` равен `failed`
- **THEN** cleanup не удаляет строку только по возрасту

#### Scenario: WAL checkpoint выполняется обслуживанием
- **WHEN** cleanup pass доходит до maintenance шага
- **THEN** выполняется SQLite WAL checkpoint
- **AND** ошибка checkpoint логируется и поднимает alert, но не ломает
  доменные данные
