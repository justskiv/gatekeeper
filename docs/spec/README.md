# Спецификация — читаемое зеркало

Человеческая версия спеки проекта. Источник правды — OpenSpec
(`openspec/specs/`); эти страницы держатся в синхроне с ним и
обновляются перед архивацией каждого change.

Текущее состояние — Фаза 02: бот запущен в polling-режиме, принимает
Telegram-обновления через durable inbox, отвечает на базовые команды и
следит за health настроенных чатов. Change `phase-02-bot-online-merge`
архивирован, его дельты влиты в `openspec/specs/`.

| Раздел | Что описывает |
|---|---|
| [config](config.md) | Загрузка конфига из окружения, валидация, дефолты |
| [access-domain](access-domain.md) | Доменные value-типы: подписки, статусы, доступы |
| [storage](storage.md) | SQLite: соединение, схема v1, инварианты, репозитории |
| [migrations](migrations.md) | Отдельный `migrate` CLI, forward-only через goose |
| [runtime](runtime.md) | Порядок старта `gatekeeper`, supervision и остановка |
| [telegram-transport](telegram-transport.md) | Telegram-клиент, long polling, durable inbox, маршрутизация |
| [bot-commands](bot-commands.md) | `/start`, `/help`, `/here`, DM-доставка и тексты |
| [chat-health](chat-health.md) | Проверка прав бота, health-ключи, discovery чатов |

## История

Архив выполненных changes — `openspec/changes/archive/`. Зеркало
обновляется по дельтам очередного change перед его архивацией.
