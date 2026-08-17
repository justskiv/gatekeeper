## MODIFIED Requirements

### Requirement: Production deployment artifacts are provided

Repository MUST include production deployment artifacts:

- `deploy/gatekeeper.service` systemd unit;
- `deploy/Dockerfile` for a static Go binary without cgo;
- `deploy/docker-compose.production.yml` for the container deployment;
- documentation showing how to run migrations before the serving
  binary.

The systemd unit MUST support `EnvironmentFile=`, set a stable working
directory next to the SQLite database, restart the service on failure,
run under a dedicated unprivileged `User=`/`Group=` and avoid embedding
secrets in the unit file.

The Docker image MUST run the `gatekeeper` binary as the container
entrypoint and MUST NOT require cgo at runtime. Runtime secrets MUST be
provided by environment variables, not baked into the image.

**Контракт восстановления MUST быть явным.** Смерть фоновой подсистемы MUST
приводить к ненулевому коду выхода процесса (первая фатальная ошибка отменяет
общий контекст и пробрасывается в `main`), а рестарт-политика деплоя —
`restart: always` в compose и `Restart=` в systemd unit — MUST пересоздавать
процесс. Восстановление MUST держаться на этой паре, а не на healthcheck'е
контейнера.

Healthcheck контейнера MUST оставаться проверкой наличия процесса (`pgrep`).
Он MUST NOT переводиться ни на `/readyz`, ни на `/livez`:

- `/readyz` строгий намеренно (БД, схема, `getMe`, `meta.health.*`, свежесть
  reconcile, регистрация webhook) и флап любого его входа убивал бы исправный
  контейнер;
- `/livez` не флапает, но существует только когда поднят HTTP-сервер, а
  дефолтная посылка деплоя (`TELEGRAM_MODE=polling`,
  `TRIBUTE_MODE=observation`, `METRICS_ENABLED=false`) HTTP-listener не
  открывает вовсе. Healthcheck на HTTP красил бы полностью исправную
  конфигурацию в `unhealthy`, а привязка healthcheck'а к `METRICS_ENABLED`
  завела бы две разные посылки восстановления для одного образа.

`/livez` MUST рассматриваться как вход внешнего мониторинга там, где
HTTP-сервер и так включён, а не как механизм рестарта.

#### Scenario: systemd unit reads environment file
- **WHEN** operator installs `deploy/gatekeeper.service`
- **THEN** service configuration can point to an external env file
- **AND** the unit file itself does not contain bot or provider secrets

#### Scenario: Docker image builds static runtime
- **WHEN** operator builds `deploy/Dockerfile`
- **THEN** the image contains the compiled `gatekeeper` binary
- **AND** runtime configuration is supplied through environment variables

#### Scenario: Migration order is documented
- **WHEN** operator follows deployment docs
- **THEN** they run `migrate up` before starting `gatekeeper`
- **AND** an unmigrated database is treated as a startup error, not
  silently migrated by the serving binary

#### Scenario: Dead background subsystem is recovered by the restart policy
- **WHEN** фоновая подсистема (например, пул воркеров Enforcer) не может быть
  удержана в работе и эскалирует ошибку
- **THEN** процесс завершается ненулевым кодом
- **AND** `restart: always` пересоздаёт контейнер со свежим процессом
- **AND** восстановление не зависит от результата healthcheck'а

#### Scenario: Container healthcheck stays a process check
- **WHEN** оператор читает `deploy/Dockerfile` и
  `deploy/docker-compose.production.yml`
- **THEN** healthcheck проверяет наличие процесса (`pgrep`)
- **AND** обоснование выбора зафиксировано рядом с ним: `/readyz` строгий и
  флапает, а `/livez` отсутствует в дефолтном polling-режиме

#### Scenario: Default polling deployment exposes no liveness endpoint
- **WHEN** деплой идёт с `TELEGRAM_MODE=polling`, `TRIBUTE_MODE=observation`
  и `METRICS_ENABLED=false`
- **THEN** HTTP-listener не открывается и `/livez` недоступен
- **AND** контейнер остаётся `healthy`, потому что healthcheck проверяет
  процесс
- **AND** отказ подсистемы всё равно восстанавливается через ненулевой выход
  и `restart: always`
