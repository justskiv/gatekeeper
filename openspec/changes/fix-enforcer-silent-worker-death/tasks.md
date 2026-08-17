# Tasks

## 1. Граница остановки и классификация ошибок

Задачи 1.1 и 1.2 обязаны попасть в один коммит, 1.1 применяется первой:
пока предикат сломан, `%s` в `rawRequest` работает единственной защитой
транспорта Telegram (см. `design.md`, решение 2).

- [x] 1.1 Удалить `isContextDone` в `internal/enforcer/enforcer.go`,
      `internal/reconcile/reconciler.go`, `internal/reconcile/cleanup.go`,
      `internal/telegram/poller.go`; заменить все вызовы на `ctx.Err() != nil`.
      Приёмка: `rg isContextDone` не находит ничего вне тестов.
- [x] 1.2 `internal/telegram/client.go`: `redactURLError` + `%w` в `rawRequest`.
- [x] 1.3 `internal/telegram/client.go`: категории `ErrorCategoryTimeout` и
      `ErrorCategoryCanceled`, предикаты `IsTimeout` и `IsTransient`;
      классификация после блока `TooManyRequestsError`, с учётом
      `net.Error.Timeout()`.

## 2. Fencing lease

- [x] 2.1 `internal/store/outbox.go`: добавить условие владения
      `AND status = 'running' AND locked_until = ?` в `MarkDone`, `Retry`,
      `MarkDead`; сигнатуры принимают `lease time.Time`.
- [x] 2.2 `internal/store/outbox.go`: `ReleaseLease(ctx, id, lease)` — возврат в
      `queued` без инкремента `attempts`.
- [x] 2.3 Отличать «строка не найдена» от «lease перехвачен»: перехват
      логируется и не считается ошибкой исполнения.
- [x] 2.4 `internal/enforcer/ports.go`: обновить интерфейс `OutboxStore`.

## 3. Супервизор воркеров

- [x] 3.1 `superviseWorker`: перезапуск с backoff, лог каждого выхода с
      причиной, эскалация в ошибку при устойчивой невозможности удержать воркера.
- [x] 3.2 `Run`: `case <-done` возвращает ошибку, если `ctx.Err() == nil`;
      логи старта и остановки пула.
- [x] 3.3 `runOnce`: удалить ветку предиката; таймаут уходит в `handleFailure`;
      при остановке — `ReleaseLease` на detached-контексте с коротким таймаутом.
- [x] 3.4 `WithWorkerRestartPolicy` — тестовый Option для детерминизма.

## 4. Тесты

- [x] 4.1 `TestEnforcerRequestTimeoutRetriesInsteadOfOrphaning` — регрессия
      инцидента на уровне `runOnce`: ошибка вида `NormalizeError("sendMessage",
      fmt.Errorf("...%w", &url.Error{Err: context.DeadlineExceeded}))` при живом
      ctx приводит к ретраю, а не к осиротевшей строке.
- [x] 4.2 `TestEnforcerWorkerSurvivesRequestTimeoutWhileContextLive` — та же
      регрессия на уровне воркера: `Run` не возвращается после трёх подряд
      таймаутов.
- [x] 4.3 `TestEnforcerRunReturnsErrorWhenWorkersExitWhileContextLive`.
- [x] 4.4 `TestEnforcerRestartsCrashedWorkerAndLogs`.
- [x] 4.5 `TestEnforcerShutdownReleasesLeaseWithoutBurningAttempt`.
- [x] 4.6 `TestPollerSurvivesGetUpdatesTimeout` — страж под задачу 1.2: без него
      замена `%s` на `%w` остаётся заряженным ружьём.
- [x] 4.7 `TestOutboxTerminalTransitionsRequireLeaseOwnership` — действие,
      пережившее lease, не может быть завершено прежним владельцем после
      перезахвата.
- [x] 4.8 `TestOutboxReleaseLeaseKeepsAttempts`.
- [x] 4.9 `internal/telegram/client_test.go`: классификация `timeout`,
      `canceled`, `net.Error`; 429 с дедлайном в цепочке остаётся `rate_limited`;
      `rawRequest` сохраняет цепочку и не печатает токен.

## 5. Проверка

- [x] 5.1 `task test`
- [x] 5.2 `task test:cover` (`-race` — тесты супервизора конкурентные)
- [x] 5.3 `task lint`
- [x] 5.4 Запись в `CHANGELOG.md`, секция `[Unreleased]` → `### Fixed`.

## 6. Доработки по внешнему ревью

- [x] 6.1 `internal/telegram/client.go`: редакция токена по всему
      отрендеренному тексту ошибки через обёртку с `Unwrap`, а не только по
      полю URL внутри `*url.Error`; подменённый `RoundTripper` больше не
      способен вынести токен в `last_error` и логи.
- [x] 6.2 `internal/enforcer/enforcer.go`: дедлайн исполнения, выведенный из
      `LeaseDuration` и строго меньший её, — fencing выбирает автора записи, но
      не отменяет уже отправленный DM.
- [x] 6.3 Решение об остановке по-прежнему принимается по родительскому
      контексту воркера; производный контекст исполнения не покидает
      `executeWithin`.
- [x] 6.4 Терминальная запись после подтверждённого эффекта (`MarkDone`)
      выполняется на отвязанном контексте с коротким таймаутом.
- [x] 6.5 Бюджет перезапусков стал скользящим окном; сброс backoff отделён от
      истечения бюджета.
- [x] 6.6 Тесты: `TestRawRequestRedactsTokenFromNestedTransportError`,
      `TestEnforcerExecutionCannotOutliveItsLease`,
      `TestEnforcerWorkerSurvivesExecutionDeadline`,
      `TestEnforcerReclaimedActionIsNotExecutedTwice`,
      `TestEnforcerFinalizesConfirmedSuccessDespiteCancellation`,
      `TestEnforcerRestartBudgetRollsWithTheWindow`,
      `TestEnforcerRestartBudgetStillEscalatesWithinTheWindow`.
