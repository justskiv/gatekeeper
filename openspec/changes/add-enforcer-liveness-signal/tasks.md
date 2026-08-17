# Tasks

## 1. Liveness-снимок в Enforcer

- [x] 1.1 `Health{WorkersConfigured, WorkersAlive, WorkersStale, MaxCycleAge,
      Restarts}` и `Enforcer.Health()` в `internal/enforcer/enforcer.go`.
- [x] 1.2 `heartbeats []atomic.Int64` — слот на воркера, сайзится в `New` по
      `e.cfg.Workers` (после применения Option'ов) и засевается текущим
      временем, чтобы пул не читался как залипший между конструированием и
      первой итерацией.
- [x] 1.3 `beat(workerID)`; вызов в `runWorker` после **каждой** завершённой
      итерации, включая холостую, и в `superviseWorker` при старте воркера.
- [x] 1.4 Счётчики `workersAlive` и `restarts`.
- [x] 1.5 `Enforcer.Alive()` — вердикт по `WorkersStale == 0`. Приёмка:
      `WorkersAlive` в вердикт не входит (заклиненный воркер — живая горутина).
- [x] 1.6 Константа `cycleStaleAfter = 5 * time.Minute` с обоснованием порога
      в комментарии; в конфигурацию не выносится.

## 2. HTTP-поверхность

- [x] 2.1 `internal/webhook/readiness.go`: поле `Readiness.EnforcerAlive
      func() bool`; `Check` добавляет `enforcer` в `Failed`, когда поле не
      `nil` и возвращает `false`.
- [x] 2.2 `internal/webhook/server.go`: маршрут `GET /livez` в `buildMux`
      рядом с `/healthz` и `/readyz` — без гейтинга режимом.
- [x] 2.3 Хендлер `livez`: `200 {"status":"ok"}` либо
      `503 {"status":"not_live","failed":["enforcer"]}`. Приёмка: никаких
      обращений к БД и сети, ответ считается по состоянию в памяти.
- [x] 2.4 `cmd/gatekeeper/main.go`: поле `runtimeGroup.enforcer`, параметр
      `newHTTPServer(..., enforcerAlive func() bool, logger)`, проброс
      `runtime.enforcer.Alive`.

## 3. Тестируемость пула

- [x] 3.1 `WithWorkers(n int)` — Option, задающий размер пула **внутри** `New`,
      до появления горутин. Мотивация в `design.md`, решение 7.
- [x] 3.2 Перевести тесты, правившие `enf.cfg.Workers` после конструирования,
      на `WithWorkers`. Приёмка: собранный `Enforcer` в тестах не мутируется.
- [x] 3.3 `backdateHeartbeat(e, workerID, age)` — тестовый хелпер, отматывающий
      один слот назад, чтобы смоделировать заклиненный воркер без ожидания
      реальных пяти минут.
- [x] 3.4 Сделать `fakeTelegram` пригодным для конкурентного использования:
      пул из нескольких воркеров вызывает один и тот же fake одновременно, и
      незащищённый `append` в `calls` — настоящая гонка, которую находит
      `-race`.

## 4. Тесты

- [x] 4.1 `TestEnforcerHealthTracksWorkers` — снимок работающего пула:
      `WorkersConfigured`, `WorkersAlive`, нулевой `WorkersStale`, `MaxCycleAge`
      меньше порога; после остановки живых воркеров не остаётся.
- [x] 4.2 `TestEnforcerAliveDetectsStuckWorker` — смысл per-worker heartbeat:
      один залипший воркер валит liveness, пока второй продолжает работать.
- [x] 4.3 `TestReadinessFailsWhenEnforcerIsDead` — `enforcer` попадает в
      `Failed` при отрицательном вердикте и отсутствует при `nil` и при `true`.
- [x] 4.4 `TestLivezReportsEnforcerState` — `200` при `nil`/`true`, `503` со
      списком `["enforcer"]` при `false`. Ключевая часть: `Readiness` строится
      с `DB: nil`, и ответ `/livez` всё равно не называет ни `db`, ни `schema`,
      тогда как `/readyz` при той же конфигурации краснеет. Это и есть
      свойство, отделяющее `/livez` от `/readyz`.
- [x] 4.5 `TestHealthzUnchangedByLiveness` — `/healthz` возвращает `200` при
      любом состоянии enforcer'а; страж того, что существующий контракт не
      поехал.

## 5. Проверка

- [x] 5.1 `task test`
- [x] 5.2 `task test:cover` (`-race` — тесты liveness конкурентные)
- [x] 5.3 `task lint` (0 issues)
- [x] 5.4 `openspec validate add-enforcer-liveness-signal --strict`
- [x] 5.5 Запись в `CHANGELOG.md`, секция `[Unreleased]` → `### Added`.

## 6. Follow-up (отдельным change)

- [ ] 6.1 `add-db-liveness-watchdog`: обобщить `Pinger` в набор именованных
      проб, чтобы лог и дамп называли провалившуюся пробу.
- [ ] 6.2 `add-db-liveness-watchdog`: добавить `Enforcer.Health()` второй
      пробой — залипший пул относится к тому же классу «процесс жив, но не
      обслуживает» и лечится тем же способом.
