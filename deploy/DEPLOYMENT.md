# Руководство по развёртыванию

Gatekeeper развёртывается одним контуром (только production) на общем дроплете, где уже живёт `garvis-bot`. Механизм единообразен с garvis: образ в GHCR, Docker Compose с двумя сервисами, ручной deploy через GitHub Actions по SSH. Staging-контура нет.

## Контур

| Параметр | Значение |
|---|---|
| GitHub Environment | `Prod` |
| Каталог на сервере | `/srv/gatekeeper` |
| Compose project | `gatekeeper` |
| Контейнеры | `gatekeeper` (бот), `gatekeeper-migrate` (миграции, one-shot) |
| Порт метрик (host) | `127.0.0.1:8090` → контейнер `:8080` |
| База данных | `/srv/gatekeeper/data/gatekeeper.db` (+ `-wal`, `-shm`) |
| Образ | `ghcr.io/<owner>/gatekeeper:<tag>` |

Порт `8090` выбран потому, что garvis на этом же дроплете занимает `8080` (prod) и `8081` (staging). Не переиспользуйте `8090` под другие сервисы.

## Начальная настройка (один раз)

### 1. GitHub Environment и секреты

В Settings → Environments создайте environment `Prod` (при желании с Required reviewers). Положите в него секреты:

| Секрет | Назначение |
|---|---|
| `DEPLOY_HOST` | IP или домен дроплета |
| `DEPLOY_USER` | SSH-пользователь |
| `DEPLOY_KEY` | приватный SSH-ключ |
| `GHCR_READ_TOKEN` | PAT с `read:packages` для pull образа на сервере |
| `BOT_TOKEN` | токен Telegram-бота (единственный всегда обязательный секрет приложения) |

Условно (только если включаете соответствующий режим в `config.production.env`):

| Секрет | Когда нужен |
|---|---|
| `TRIBUTE_API_KEY` | `TRIBUTE_MODE=webhook` |
| `TELEGRAM_WEBHOOK_SECRET` | `TELEGRAM_MODE=webhook` |

Build-workflow дополнительных секретов не требует — он пушит в GHCR через встроенный `GITHUB_TOKEN`.

### 2. Сервер

Предпосылки (скрипты унаследованы от garvis и рассчитаны на ту же среду): `DEPLOY_USER` — root или пользователь с беспарольным sudo (`install-docker.sh` вызывает `systemctl`/`apt-get`, `deploy-compose.sh` делает `chown`); дистрибутив — Ubuntu/Debian с доступным `curl` (установка Docker идёт через `curl … | sh`). На минимальном образе без `curl` первый деплой упадёт.

Добавьте публичный SSH-ключ на дроплет:

```bash
echo "ВАШ_ПУБЛИЧНЫЙ_КЛЮЧ" >> ~/.ssh/authorized_keys
```

Остальное deploy-workflow делает сам: ставит Docker и `jq` (`install-docker.sh`), создаёт `/srv/gatekeeper`, копирует compose/конфиг/скрипты, тянет образ и поднимает контейнеры (`deploy-compose.sh`).

### 3. Конфигурация

Заполните реальными значениями `deploy/config.production.env` (chat ID, ссылки на подписку, владельцы, режимы) и закоммитьте. Это несекретный слой — он копируется на сервер как `config.env` и грузится обоими контейнерами через `env_file`. Секреты в этот файл не кладутся: `BOT_TOKEN` (и при webhook-режимах `TRIBUTE_API_KEY` / `TELEGRAM_WEBHOOK_SECRET`) приходят из секретов окружения `Prod` и переопределяют `config.env`.

## Процесс развёртывания

### Сборка образа

Push тега `v*` автоматически запускает **Build & publish image** и публикует `ghcr.io/<owner>/gatekeeper:<tag>` и `:latest`. Можно также запустить сборку вручную (Actions → Build & publish image → Run workflow) с произвольным тегом.

### Деплой

1. Actions → **Deploy** → Run workflow.
2. Укажите `tag`. По умолчанию `latest`, но для prod указывайте явный immutable-тег (`vX.Y.Z`) — это гарантирует, что выкатывается именно тот артефакт, и упрощает откат.
3. Run.

Workflow раскатывает образ на дроплет: миграции (`gatekeeper-migrate`) применяются и завершаются, затем стартует `gatekeeper`, после чего скрипт ждёт healthcheck.

### Откат

Запустите Deploy повторно, указав предыдущий тег (`vX.Y.Z`). Теговые образы при чистке не удаляются, поэтому откат не требует пересборки.

## Мониторинг

```bash
docker inspect gatekeeper --format '{{json .State.Health}}' | jq
docker compose -p gatekeeper logs -f
curl -s 127.0.0.1:8090/healthz
curl -s 127.0.0.1:8090/readyz
curl -s 127.0.0.1:8090/metrics
```

`/healthz` — лёгкая liveness. `/readyz` — строгая готовность (БД/схема, стартовый `getMe`, здоровье чатов, свежесть reconcile). Контейнерный healthcheck намеренно использует только `pgrep` (liveness), чтобы транзиентные сбои `getMe`/reconcile не валили контейнер и деплой.

## Скрипты

### `scripts/install-docker.sh`
Идемпотентно ставит Docker и `jq`, если их нет.

### `scripts/deploy-compose.sh`
Готовит каталог данных (`mkdir` + `chown 1000:1000` — нужно для WAL), логинится в GHCR, тянет образ, поднимает контейнеры, ждёт healthcheck, чистит dangling-слои с фильтром по label `com.gatekeeper.service=gatekeeper` (только мусор этого сервиса; теговые версии и чужие образы не трогаются).

Параметризован через env: `DIR`, `TAG`, `OWNER`, `GHCR_READ_TOKEN`, `BOT_TOKEN` (обязательны), `TRIBUTE_API_KEY`, `TELEGRAM_WEBHOOK_SECRET`, `COMPOSE_PROJECT_NAME` (`gatekeeper`), `METRICS_HOST_PORT` (`8090`), `CONTAINER_NAME` (`gatekeeper`).

Локальный прогон для отладки:

```bash
export DIR=/srv/gatekeeper TAG=latest OWNER=<owner>
export GHCR_READ_TOKEN=... BOT_TOKEN=...
./deploy/scripts/deploy-compose.sh
```

## Бэкапы

База — на host bind-volume `/srv/gatekeeper/data`, поэтому её видно с хоста. Для консистентной копии либо остановите контейнер и скопируйте `gatekeeper.db` вместе с `-wal` и `-shm`, либо используйте `VACUUM INTO` из контролируемой сессии. Файлы БД содержат персональные данные и состояние доступа — обращайтесь с ними как с секретами; каталог не должен быть world-readable.

## Отладка

```bash
journalctl -u docker -f
docker compose -p gatekeeper logs -f
```

Для appleboy-action добавьте `APPLEBOY_DEBUG: "true"` в env шага.
