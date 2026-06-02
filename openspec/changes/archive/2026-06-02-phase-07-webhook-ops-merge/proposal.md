## Why

Фаза 07 завершает эксплуатационный контур Gatekeeper: в режиме B Tribute
должен давать точные даты подписки через webhook, а оператору нужны
health/readiness, метрики, сводки, экспорт и артефакты развёртывания.
Без этого проект функционален в observation-режиме, но не готов к
production-эксплуатации и open-source публикации.

## What Changes

- Добавить HTTP runtime, который включается только при
  `TRIBUTE_MODE=webhook`, `TELEGRAM_MODE=webhook` или
  `METRICS_ENABLED=true`.
- Добавить `/healthz`, `/readyz` и `/metrics`, где readiness
  переиспользует существующие `meta.health.*` и
  `meta.reconcile.last_run_at`.
- Добавить приём Tribute webhook с HMAC-SHA256 по raw body, durable
  inbox-строкой `tribute_events`, дедупликацией и обработкой edge-cases
  `expires_at`, out-of-order и `cancelled_subscription`.
- В режиме B учитывать ledger-сигнал Tribute как первичный источник
  точной даты окончания, оставляя membership Tribute-канала сверочным
  сигналом.
- Добавить опциональный Telegram webhook-транспорт с проверкой
  `X-Telegram-Bot-Api-Secret-Token` и тем же роутером, что у polling.
- Добавить owner ops-команды `/stats`, `/alerts`, `/export`, `/chats`
  и `/help_admin`.
- Усилить privacy/redaction для raw payload и логов: не сохранять сырой
  email, секреты, полные invite-ссылки и чувствительные поля payload.
- Добавить deployment/OSS артефакты: systemd unit, Dockerfile,
  английский README quickstart, MIT license, CONTRIBUTING, SECURITY и
  GitHub Actions CI.

## Capabilities

### New Capabilities

- `webhook-ops`: HTTP server, health/readiness, Prometheus metrics,
  Tribute webhook ingestion and optional Telegram webhook transport.
- `deployment-oss`: production deployment artifacts, open-source
  metadata and CI contract.

### Modified Capabilities

- `config`: добавить/уточнить эксплуатационные параметры webhook,
  metrics и immediate-cancel override.
- `runtime`: заменить fail-fast для Telegram webhook на условный запуск
  HTTP-сервера и согласовать порядок shutdown фоновых подсистем.
- `telegram-transport`: описать общий router entrypoint для polling и
  webhook, проверку Telegram webhook secret и роль Tribute channel
  membership в режиме B.
- `status-core`: уточнить обработку Tribute ledger events с
  `expires_at`, out-of-order защитой и cancel semantics.
- `storage`: добавить repository-контракт для `tribute_events`,
  redaction raw inbox payload и чтения, нужные ops-командам.
- `bot-commands`: добавить owner ops-команды, CSV export и финальный
  набор help/admin commands.

## Impact

Затрагиваются `cmd/gatekeeper`, конфигурация, HTTP/webhook слой,
Telegram router, source/engine/store пакеты, admin command handlers,
metrics, deploy/docs/CI артефакты и тесты webhook/ops flows. Внешне
появляются HTTP endpoints на `WEBHOOK_LISTEN_ADDR`, Prometheus metrics
при включённом флаге, CSV export только для owners и новые документы
для запуска self-hosted экземпляра.
