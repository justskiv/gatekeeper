# Tasks

## 1. Схема

- [x] 1.1 `migrations/0003_outbox_alert_link.sql`, Up: перестройка
      `access_actions` (rename → recreate → copy → drop → reindex) с колонкой
      `alert_id INTEGER REFERENCES admin_alerts(id) ON DELETE SET NULL` и
      статусным CHECK `('queued','running','done','dead','cancelled')`.
      Копирование строк маппит `failed` → `dead`.
- [x] 1.2 Индексы: восстановить `idx_access_actions_ready`, добавить частичный
      `idx_access_actions_alert` по `alert_id WHERE alert_id IS NOT NULL`.
- [x] 1.3 Down: зеркальная перестройка — вернуть старый CHECK, убрать
      `alert_id`, смаппить `cancelled` → `done`.
- [x] 1.4 Header-комментарий объясняет все три изменения и почему нужна
      перестройка, а не ALTER.

## 2. Domain

- [x] 2.1 `internal/domain/access.go`: удалить `ActionFailed`, добавить
      `ActionCancelled`. Приёмка: `rg ActionFailed` не находит ничего.
- [x] 2.2 `AccessAction.AlertID *int64`.

## 3. Store

- [x] 3.1 `outbox.go`: `AlertID` в `AccessActionInput`, в INSERT `Enqueue`, в
      `scanAction` и **во всех** SELECT/RETURNING-списках колонок (их пять).
- [x] 3.2 `outbox.go`: `MarkCancelled(ctx, id, leaseUntil, reason)` —
      fencing по `(id, status='running', locked_until)`, `ErrLeaseLost` на
      перехвате, как у остальных терминальных переходов.
- [x] 3.3 `outbox.go`: `CancelQueuedForAlert(ctx, alertID, reason) (int64, error)`
      с намеренным условием `status='queued'`.
- [x] 3.4 `alerts.go`: `IsOpen(ctx, id) (bool, error)`; отсутствующая тревога —
      не ошибка и не «открыта».
- [x] 3.5 `alerts.go`: `enqueueAlertDM` проставляет `AlertID`.
- [x] 3.6 `alerts.go`: `ResolveOpenByTitle`/`ResolveOpen` внутри `store.WithTx`
      — сначала SELECT id, затем резолв, затем отмена очередей. Порядок
      обязателен: после смены статуса SELECT не найдёт ничего.
- [x] 3.7 `cleanup.go`: `DeleteDoneActions` → `DeleteSettledActions`, жнёт
      `done` и `cancelled`, сохраняет `dead`; обновить вызывающего в
      `internal/reconcile/cleanup.go`.

## 4. Второй производитель (health)

- [x] 4.1 `internal/notify/notify.go`: `SendFormattedAlertOwners`, протаскивание
      `alertID` до `enqueueDM`, маркер `admin_alert:<alertID>:<tgID>`.
      Маркер `manual:` остаётся дефолтом для вызывающих без идентичности.
- [x] 4.2 `internal/telegram/health.go`: `recordHealthFailure` использует id,
      который `CreateOpenIfMissing` уже возвращает. Текст в `internal/messages`
      не трогать.

## 5. Enforcer

- [x] 5.1 `internal/enforcer/ports.go`: `IsOpen` в `AlertStore`,
      `MarkCancelled` в `OutboxStore`.
- [x] 5.2 `runOnce`: после lease и до `execute` — при `AlertID != nil` и
      закрытой тревоге лог уровня info и `MarkCancelled` вместо исполнения.
      Ошибка `IsOpen` проваливается в обычное исполнение.

## 6. Тесты

- [x] 6.1 `TestResolveCancelsQueuedAlertDeliveries` — отменяется `queued`
      связанная строка, не трогаются `done` связанная и `queued` несвязанная.
- [x] 6.2 `TestResolveCancelsQueuedAlertDeliveriesInsideTx` — то же на `*sql.Tx`
      (форма поллера), страж для no-op-ветки `WithTx`.
- [x] 6.3 `TestOutboxCancelQueuedForAlertSparesRunningRow`.
- [x] 6.4 `TestAlertsIsOpenTracksAlertLifecycle` — открыта / резолвнута /
      отсутствует.
- [x] 6.5 `TestCleanupReapsSettledActionsAndKeepsDead`.
- [x] 6.6 `TestDeleteResolvedAlertsKeepsLinkedAction` — FK-регрессия:
      `DeleteResolvedAlerts` не падает, строка выживает с `alert_id IS NULL`.
- [x] 6.7 `TestMigration0003RebuildsOutboxStatusMachine` — Up/Down, включая
      миграцию существующей `failed`-строки в `dead` и отказ вставить
      `status='failed'` после.
- [x] 6.8 `TestOutboxTerminalTransitionsRequireLeaseOwnership` дополнен
      проверкой fencing у `MarkCancelled`.
- [x] 6.9 `TestEnforcerCancelsNotificationWhoseAlertResolved`,
      `TestEnforcerDeliversWhenAlertStateIsUnreadable`,
      `TestEnforcerSkipsAlertCheckForUnlinkedAction`.
- [x] 6.10 `TestAlertOwnerDMLinksAlertAndDedupes` и
      `TestOwnerDMWithoutAlertKeepsUniqueMarker`.
- [x] 6.11 `TestBotRightsLostOwnerDMIsCancelledWhenTheAlertResolves` — регрессия
      самого инцидента: DM владельцу о потере прав связана со своей тревогой и
      отменяется, когда права возвращаются.

## 7. Проверка

- [x] 7.1 `task test`
- [x] 7.2 `task test:cover` (`-race`)
- [x] 7.3 `task lint` — 0 issues
- [x] 7.4 `task migrate:up` на пустой scratch-базе
- [x] 7.5 `openspec validate fix-alert-notification-cancellation --strict`
- [x] 7.6 Запись в `CHANGELOG.md`, секция `[Unreleased]` → `### Fixed`.

## 8. Доработки по внешнему ревью

- [x] 8.1 `internal/telegram/health.go`: живой путь `my_chat_member` больше не
      выбрасывает id тревоги — он уходит в исходящий эффект
      (`OutboundMessage.AlertID`), и durable-строка несёт `alert_id`.
- [x] 8.2 `internal/notify/notify.go`: `SendAlertDM` — связанная доставка для
      одного получателя, с маркером идемпотентности от тревоги.
- [x] 8.3 `internal/telegram/router.go`: связанные эффекты ставятся в очередь
      через связанную доставку, а не через маркер апдейта.
- [x] 8.4 `TestRealtimeRightsLostDMIsCancelledWhenRightsReturn` — регрессия на
      уровне роутера: права потеряны → права вернулись до прихода воркера →
      уведомление снято, а не доставлено.

## 9. Схлопывание дубля уведомления

- [x] 9.1 `internal/store/alerts.go`: `AlertInput.OwnerNotifiedByCaller` —
      явный отказ от обобщённой DM владельцу для тревоги, о которой
      создающий её код сообщает сам. Поведение доставки для всех остальных
      видов тревог не меняется.
- [x] 9.2 Отказ гасит только владельческую DM: при заданном
      `ADMIN_LOG_CHAT_ID` запись в операторскую ленту сохраняется — она
      адресована не владельцу.
- [x] 9.3 `internal/telegram/health.go`: оба места, поднимающих
      `bot_rights_lost`, ставят флаг. На пути поллера это убирает дубль; на
      startup/reconcile-пути репозиторий и так без доставки, и флаг там —
      страховка от будущей проводки доставки.
- [x] 9.4 `TestPollerRightsLossNotifiesTheOwnerOnce` — поллер-уровневая
      регрессия: одна потеря прав → ровно одна строка `send_dm` владельцу;
      после возврата прав ни одной `queued`/`running` строки про тревогу.
- [x] 9.5 `TestStartupRightsLossNotifiesOwnerWithoutRepositoryDelivery` —
      страж от немоты: путь без доставки в репозитории продолжает
      уведомлять владельца.
- [x] 9.6 `TestOwnerNotifiedByCallerSkipsTheGenericOwnerDM` и
      `TestOwnerNotifiedByCallerKeepsTheAdminLogDelivery` — граница отказа
      на уровне хранилища.
