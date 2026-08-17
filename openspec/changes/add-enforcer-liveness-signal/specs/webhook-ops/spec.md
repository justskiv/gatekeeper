## ADDED Requirements

### Requirement: Liveness endpoint reports in-process subsystems without external calls

`GET /livez` MUST быть смонтирован всегда, когда работает HTTP-сервер, — как
`/healthz` и `/readyz`, и по той же причине: операционная тройка не гейтится
режимом, в отличие от `/metrics` и webhook-путей.

`GET /livez` MUST отвечать **только** по состоянию внутрипроцессных фоновых
подсистем, которые обязаны работать. Он MUST NOT обращаться к базе данных,
Telegram API или любой другой внешней зависимости: ответ MUST вычисляться по
состоянию в памяти процесса. Именно отсутствие внешних обращений делает его
пригодным для частого опроса и неспособным краснеть по чужой вине.

Входом MUST быть liveness-вердикт Enforcer'а (пул воркеров, поворачивающий
цикл). Отсутствующий вход (Enforcer здесь не супервизируется) MUST
пропускаться, а не проваливаться.

Все входы хороши — `GET /livez` MUST возвращать `200`. Иначе он MUST возвращать
`503` с компактным machine-readable перечислением провалившихся подсистем, и
это перечисление MUST содержать только внутрипроцессные входы.

Три операционных эндпоинта MUST отвечать на три разных вопроса и MUST NOT
сливаться:

- `GET /healthz` — HTTP-сервер поднят. Контракт не меняется: `200` без единой
  проверки, ни сетевой, ни database. Никакое состояние фоновых подсистем
  MUST NOT влиять на его ответ.
- `GET /livez` — внутрипроцессные подсистемы работают. Без внешних
  зависимостей, поэтому флапать он не может.
- `GET /readyz` — процессу можно слать трафик. Строгий намеренно: включает
  внешние зависимости и MAY законно флапать на транзиентном сбое, поэтому
  MUST NOT использоваться там, где нужен быстрый ответ «жив ли процесс».

#### Scenario: Liveness succeeds while the worker pool turns
- **WHEN** HTTP-сервер работает и пул воркеров Enforcer поворачивает цикл
- **THEN** `GET /livez` возвращает `200`

#### Scenario: Stopped worker pool fails liveness
- **WHEN** liveness-вердикт Enforcer'а отрицательный (воркеры мертвы или
  заклинены)
- **THEN** `GET /livez` возвращает `503`
- **AND** ответ называет `enforcer` среди провалившихся подсистем

#### Scenario: Liveness does not touch the database
- **WHEN** база данных недоступна или не сконфигурирована, а пул воркеров
  поворачивает цикл
- **THEN** `GET /livez` возвращает `200`
- **AND** ответ не называет ни `db`, ни `schema`, ни любой другой внешний вход
- **AND** `GET /readyz` при той же конфигурации возвращает `503` и называет
  недоступную базу

#### Scenario: Unsupervised enforcer does not fail liveness
- **WHEN** HTTP-сервер работает в конфигурации, где Enforcer не супервизируется
- **THEN** вход пропускается
- **AND** `GET /livez` возвращает `200`

#### Scenario: Liveness is available whenever the HTTP server runs
- **WHEN** HTTP-сервер поднят любым из включающих его режимов
  (`METRICS_ENABLED=true`, `TRIBUTE_MODE=webhook` или
  `TELEGRAM_MODE=webhook`)
- **THEN** `GET /livez` смонтирован и отвечает
- **AND** он не гейтится ни одним из этих режимов по отдельности

## MODIFIED Requirements

### Requirement: HTTP server exposes only enabled operational routes

Gatekeeper MUST expose an HTTP server on `WEBHOOK_LISTEN_ADDR` only when
`TRIBUTE_MODE=webhook`, `TELEGRAM_MODE=webhook` or
`METRICS_ENABLED=true`. The server MUST use explicit
`ReadHeaderTimeout`, `ReadTimeout`, request body limits for webhook
routes and graceful `Shutdown` on process cancellation.

Enabled routes MUST be:

- `GET /healthz` when the server is running;
- `GET /livez` when the server is running;
- `GET /readyz` when the server is running;
- `GET /metrics` only when `METRICS_ENABLED=true`;
- `POST {TRIBUTE_WEBHOOK_PATH}` only when `TRIBUTE_MODE=webhook`;
- `POST {TELEGRAM_WEBHOOK_PATH}` only when `TELEGRAM_MODE=webhook`.

Routes disabled by mode MUST return `404`. Unsupported methods on
enabled webhook paths MUST return `405`.

#### Scenario: Clean polling mode opens no HTTP listener
- **WHEN** `TELEGRAM_MODE=polling`, `TRIBUTE_MODE=observation` and
  `METRICS_ENABLED=false`
- **THEN** runtime does not open a listener on `WEBHOOK_LISTEN_ADDR`

#### Scenario: Metrics enables HTTP without webhook modes
- **WHEN** `METRICS_ENABLED=true` while both webhook modes are off
- **THEN** the HTTP server starts
- **AND** `/metrics`, `/healthz`, `/livez` and `/readyz` are available
- **AND** Tribute and Telegram webhook paths return `404`

#### Scenario: Disabled route is not mounted
- **WHEN** `TRIBUTE_MODE=observation`
- **THEN** запрос на `TRIBUTE_WEBHOOK_PATH` возвращает `404`

### Requirement: Health and readiness report process and dependency state

`GET /healthz` MUST return `200` after the HTTP server starts and MUST
NOT perform network or database checks.

`GET /readyz` MUST return `200` only when all readiness inputs are good:
SQLite is reachable, `store.CheckSchema` succeeds, startup `getMe`
succeeded, all four `meta.health.*` keys are `ok`, and
`meta.reconcile.last_run_at` exists and is not older than two
`RECONCILE_INTERVAL` periods. When `TELEGRAM_MODE=webhook`, successful
Telegram webhook registration is also a readiness input. If any input is
bad, `/readyz` MUST return `503` with a compact machine-readable
description of failed checks.

Liveness-вердикт Enforcer'а MUST быть дополнительным входом readiness. В
отличие от `getMe` и свежести reconcile, остановившийся пул воркеров не бывает
транзиентным: сам он не восстановится, а значит процесс не готов принимать
трафик. Отсутствующий вход (Enforcer здесь не супервизируется) MUST
пропускаться, а не проваливаться.

Присутствие enforcer'а в обоих эндпоинтах MUST NOT делать `/livez`
подмножеством `/readyz`: один и тот же факт законно отвечает на оба вопроса, а
различает их остальной состав входов. `/readyz` MUST оставаться строгим и MAY
флапать на транзиентном сбое внешней зависимости; `/livez` MUST оставаться
process-local.

#### Scenario: Liveness succeeds after server start
- **WHEN** HTTP server is running
- **THEN** `GET /healthz` returns `200`

#### Scenario: Healthz ignores background subsystem state
- **WHEN** пул воркеров Enforcer остановлен или заклинен
- **THEN** `GET /healthz` всё равно возвращает `200`
- **AND** его контракт остаётся прежним: он утверждает только то, что
  HTTP-сервер поднят

#### Scenario: Readiness succeeds with healthy dependencies
- **WHEN** SQLite/schema are available, `getMe` succeeded, all
  `meta.health.*` values are `ok` and reconciliation is fresh
- **THEN** `GET /readyz` returns `200`

#### Scenario: Lost admin rights make readiness fail
- **WHEN** `meta.health.club_channel` starts with `fail:`
- **THEN** `GET /readyz` returns `503`
- **AND** the response names `club_channel`

#### Scenario: Stale reconciliation makes readiness fail
- **WHEN** `meta.reconcile.last_run_at` is older than two
  `RECONCILE_INTERVAL` periods
- **THEN** `GET /readyz` returns `503`
- **AND** the response names stale reconciliation

#### Scenario: Telegram webhook registration gates readiness
- **WHEN** `TELEGRAM_MODE=webhook` and Telegram webhook registration has
  not succeeded
- **THEN** `GET /readyz` returns `503`
- **AND** the response names webhook registration

#### Scenario: Dead enforcer gates readiness
- **WHEN** liveness-вердикт Enforcer'а отрицательный, а остальные входы
  readiness хороши
- **THEN** `GET /readyz` returns `503`
- **AND** the response names `enforcer`

#### Scenario: Unsupervised enforcer does not gate readiness
- **WHEN** Enforcer здесь не супервизируется, а остальные входы readiness
  хороши
- **THEN** `GET /readyz` returns `200`
- **AND** the response does not name `enforcer`
