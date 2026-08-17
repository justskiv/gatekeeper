# Tasks

## 1. Перечисления домена

- [x] 1.1 `domain.AllActionTypes()` — все типы действий в порядке объявления,
      свежий слайс на каждый вызов.
- [x] 1.2 `domain.AllActionStatuses()` — все статусы в порядке машины
      состояний. Приёмка: `failed` отсутствует, `cancelled` присутствует.
- [x] 1.3 `store.AlertSeverities()` — четыре severity из CHECK'а
      `admin_alerts.severity`, от менее срочного к более срочному.

## 2. Read-model'ы в store

- [x] 2.1 `Ops.OutboxBacklog(ctx, now)` — `Queued`, `Running`, `Due`,
      `OldestQueuedAge`, `OldestDueAge` одним запросом.
- [x] 2.2 Агрегат через `sum(CASE WHEN ... THEN 1 ELSE 0 END)` и
      `min(CASE WHEN ... THEN col END)`, без `FILTER`; счётчики в `COALESCE`,
      timestamp'ы в `sql.NullString` + `parseTime`. Приёмка: пустая таблица
      даёт нули, а не ошибку скана.
- [x] 2.3 `Ops.AlertCounts(ctx)` — группировка `admin_alerts` по kind,
      severity, status, по существующей форме скана `outboxCounts`.
- [x] 2.4 Комментарий о стоимости: пул `Ops` — одно соединение, `/metrics` уже
      делает несколько запросов за скрейп; два новых приемлемы при 15–30 с, но
      расти дальше без кеша не должны.

## 3. Счётчик пропускной способности в enforcer

- [x] 3.1 `ProcessedResult` с закрытым множеством
      `done|retried|dead|cancelled|noop` и обоснованием каждого значения.
- [x] 3.2 `Enforcer.recordProcessed` + `Enforcer.Processed()` со стабильным
      порядком (type, result).
- [x] 3.3 `handleFailure` возвращает `(ProcessedResult, error)` — исход не
      выводится повторно из ошибки на стороне вызывающего.
- [x] 3.4 `settle` считает исход только при успешно записанном переходе.
      Приёмка: `ErrLeaseLost` не инкрементирует счётчик.

## 4. Счётчик ошибок Telegram API

- [x] 4.1 Счётчик на `*telegram.Client` (не пакетная переменная), ключ
      `(method, category)`, доступ под мьютексом.
- [x] 4.2 Приватный `c.normalize(method, err)` — единственная точка
      инкремента; все методы клиента переведены на него. `NormalizeError`
      остаётся чистой классификацией.
- [x] 4.3 `Client.APIErrorCounts()` со стабильным порядком.

## 5. Экспозиция

- [x] 5.1 `webhook.EnforcerStats`, `webhook.ProcessedCount`,
      `webhook.TelegramErrorCount` объявлены в `webhook`; поля-функции
      `Metrics.Enforcer` и `Metrics.TelegramErrors`. Приёмка: `internal/webhook`
      не импортирует `internal/enforcer`.
- [x] 5.2 `Metrics.Now func() time.Time` для возрастных метрик.
- [x] 5.3 `Write` разбит на `writeInventoryMetrics` / `writeOutboxMetrics` /
      `writeEnforcerMetrics` / `writeAlertMetrics` / `writeTelegramErrorMetrics`
      — без `//nolint:funlen`.
- [x] 5.4 Честные имена + deprecated-алиасы с
      `# HELP ... DEPRECATED: use <new name>`; алиас объявлен `gauge`.
- [x] 5.5 Алиас выводится отдельной смежной группой, а не вперемешку с
      основным семейством.
- [x] 5.6 Zero-fill `gatekeeper_outbox_actions` по декартову произведению из
      `domain.AllActionTypes()` × `domain.AllActionStatuses()`.
- [x] 5.7 `gatekeeper_outbox_pending`, `_running`, `_oldest_queued_age_seconds`,
      `_oldest_due_age_seconds` из одного `OutboxBacklog`.
- [x] 5.8 Пять серий пула воркеров + `gatekeeper_outbox_actions_processed_total`.
- [x] 5.9 `gatekeeper_admin_alerts{kind,severity,state}` разреженная,
      `gatekeeper_admin_alerts_open{severity}` zero-filled по всем четырём
      severity; асимметрия объяснена комментарием.
- [x] 5.10 `gatekeeper_reconcile_last_run_age_seconds` из
      `stats.ReconcileLastRunAt`, а до первого завершённого прохода — от старта
      процесса (см. 10.6); `gatekeeper_reconcile_duration_seconds` удалена.
- [x] 5.11 `# TYPE` объявляется только для семейств с живым источником;
      in-process семейства пишутся до обращений к базе.

## 6. Проброс в main

- [x] 6.1 `enforcerStats(runtime.enforcer)` — адаптер `Health()` + `Processed()`
      к `webhook.EnforcerStats`.
- [x] 6.2 `telegramErrorCounts(tgClient.APIErrorCounts)` — адаптер к
      `webhook.TelegramErrorCount`.
- [x] 6.3 Оба параметра проброшены через `newHTTPServer`.

## 7. Тесты

- [x] 7.1 `internal/webhook/metrics_test.go` (файла не было):
      zero-fill всех пар type × status на пустой базе, включая явную проверку
      `gatekeeper_outbox_actions{status="dead",...} 0`.
- [x] 7.2 Серии пула присутствуют при заданном поле-функции и полностью
      отсутствуют (без паники) при `nil`.
- [x] 7.3 Возрасты очереди считаются по засеянным строкам; будущий `run_after`
      не считается просроченным.
- [x] 7.4 Серии тревог, включая zero-filled свёртку по severity.
- [x] 7.5 Deprecated-алиас выводится и объявлен `gauge` — чтобы его удаление
      было сознательным действием.
- [x] 7.6 Обе фантомные метрики отсутствуют в теле ответа.
- [x] 7.7 `internal/store/ops_test.go`: `OutboxBacklog` на пустой таблице и на
      засеянной; `AlertCounts` на пустой таблице и с группировкой.
- [x] 7.8 `internal/enforcer/processed_test.go`: каждый исход попадает в свою
      корзину; `ErrLeaseLost` не считается; счётчик монотонен между вызовами.
- [x] 7.9 `internal/telegram/error_counts_test.go`: ошибки копятся по
      (method, category), успехи не копятся.

## 8. Проверка

- [x] 8.1 `task test`
- [x] 8.2 `task test:cover` (`-race`)
- [x] 8.3 `task lint` (0 issues)
- [x] 8.4 `openspec validate rework-ops-metrics --strict`
- [x] 8.5 Запись в `CHANGELOG.md`, секция `[Unreleased]` → `### Changed`.

## 9. Follow-up (отдельными change'ами)

- [ ] 9.1 Снять deprecated-алиасы после того, как потребитель переключён;
      список переключений — в `design.md`, `## Dashboard migration`.
- [ ] 9.2 In-process кеш для `/metrics`, если набор запросов за скрейп будет
      расти дальше: пул `Ops` — одно соединение.

## 10. Доработки по внешнему ревью

- [x] 10.1 `gatekeeper_metrics_store_scrape_success` — сбой чтения базы виден в
      экспозиции, а не только в логе; ответ остаётся `200`, in-process
      семейства сохраняются.
- [x] 10.2 Fail-safe zero-fill: пара, присутствующая в базе, выводится даже
      если её нет в доменном реестре.
- [x] 10.3 Проверка полноты реестра независима от него самого:
      `TestAllActionTypesCoversEveryDeclaredConstant` (парсит константы) и
      `TestActionEnumsMatchSchemaCheckConstraints` (читает `CHECK` схемы).
- [x] 10.4 Комментарии `AllActionTypes`/`AllActionStatuses` больше не обещают
      невозможности забыть значение — обещание держат тесты.
- [x] 10.5 Тесты: `TestMetricsFailedStoreReadIsObservable`,
      `TestMetricsHealthyScrapeReportsSuccess`,
      `TestMetricsEmitsPairsMissingFromTheRegistry`.
- [x] 10.6 `gatekeeper_reconcile_last_run_age_seconds` выводится всегда, когда
      база прочитана: при отсутствующем `meta.reconcile.last_run_at` возраст
      считается от старта процесса (`Metrics.ProcessStart`, проброшен из `main`),
      а не пропускается. Приёмка: правило простоя с `noDataState: Ok` не может
      прочитать «нет прохода» как здоровье. Тесты:
      `TestMetricsReportsReconcileAgeBeforeFirstPass`,
      `TestMetricsReportsReconcileAgeFromLastRun`,
      `TestMetricsReconcileAgeSurvivesUnwiredProcessStart`.
