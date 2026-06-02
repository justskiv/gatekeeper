# Спецификация — читаемое зеркало

Человеческая версия спеки проекта. Источник правды — OpenSpec
(`openspec/specs/`); эти страницы держатся в синхроне с ним и
обновляются перед архивацией каждого change.

Текущее состояние — Фаза 07: бот функционально полон и готов к
production. Базовый режим — polling + observation без HTTP-порта:
обновления идут через durable inbox, эффективный статус подписки
вычисляется из источников, исходящие Telegram-действия исполняются через
durable outbox и Enforcer, доступ в клубные ресурсы выдаётся по
pull-модели (`/start`, invite-ссылки, join-request) и отзывается через
Reconciler. Опционально включаются: приём вебхуков Tribute (режим B с
точными `expires_at`), webhook-транспорт самого бота, HTTP-эндпоинты
`/healthz`/`/readyz`/`/metrics` и owner ops-команды. Поставляются
артефакты развёртывания (systemd, Dockerfile, CI) и OSS-метаданные.
Архивированы changes `phase-02..07`, их дельты влиты в
`openspec/specs/`.

| Раздел | Что описывает |
|---|---|
| [config](config.md) | Загрузка конфига из окружения, валидация, дефолты |
| [access-domain](access-domain.md) | Доменные value-типы: подписки, статусы, доступы |
| [storage](storage.md) | SQLite: соединение, схема v1, инварианты, репозитории |
| [migrations](migrations.md) | Отдельный `migrate` CLI, forward-only через goose |
| [runtime](runtime.md) | Порядок старта `gatekeeper`, supervision и остановка |
| [telegram-transport](telegram-transport.md) | Telegram-клиент, long polling, durable inbox, маршрутизация |
| [bot-commands](bot-commands.md) | `/start`, `/help`, `/here`, `/status`, `/whois`, DM-доставка |
| [chat-health](chat-health.md) | Проверка прав бота, health-ключи, discovery чатов |
| [status-core](status-core.md) | Вердикты источников, агрегатор статуса, события подписки |
| [outbox-enforcer](outbox-enforcer.md) | Durable outbox `access_actions` и исполнение Enforcer'ом |
| [invite-links](invite-links.md) | Режимы invite-ссылок, lifecycle, разрешение для admission |
| [grant-access](grant-access.md) | Выдача доступа: `/start`, join-request, фиксация членства |
| [webhook-ops](webhook-ops.md) | HTTP-сервер, healthz/readyz/metrics, приём вебхуков Tribute |
| [deployment-oss](deployment-oss.md) | systemd, Dockerfile, README-quickstart, CI и OSS-метаданные |

## История

Архив выполненных changes — `openspec/changes/archive/`. Зеркало
обновляется по дельтам очередного change перед его архивацией.
