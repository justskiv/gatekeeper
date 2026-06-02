## 1. Config and Runtime Wiring

- [x] 1.1 Уточнить config tests для `TRIBUTE_CANCEL_IS_IMMEDIATE`,
  webhook defaults и mode-dependent HTTP activation.
- [x] 1.2 Добавить runtime decision helper: нужен ли HTTP server для
  Tribute webhook, Telegram webhook или metrics.
- [x] 1.3 Перестроить startup order: убрать fail-fast для
  `TELEGRAM_MODE=webhook`, сохранить polling как default.
- [x] 1.4 Запускать HTTP server, poller/webhook transport, Enforcer,
  Reconciler и cleanup под общим `errgroup`.
- [x] 1.5 Реализовать graceful shutdown HTTP server до закрытия БД.
- [x] 1.6 В режиме Telegram webhook регистрировать webhook URL, secret
  token и explicit `allowed_updates`; в polling не запускать webhook.

## 2. HTTP Ops Server

- [x] 2.1 Создать `internal/webhook/server.go` с `net/http` mux,
  `ReadHeaderTimeout`, `ReadTimeout` и ограничением webhook body.
- [x] 2.2 Реализовать `GET /healthz` как lightweight liveness.
- [x] 2.3 Реализовать `GET /readyz`: DB ping, `CheckSchema`, startup
  `getMe`, `meta.health.*`, freshness `meta.reconcile.last_run_at` и
  Telegram webhook registration при `TELEGRAM_MODE=webhook`.
- [x] 2.4 Реализовать `/metrics` только при `METRICS_ENABLED=true` в
  Prometheus text format с bounded labels.
- [x] 2.5 Покрыть route gating: disabled endpoints возвращают `404`,
  unsupported webhook methods возвращают `405`.

## 3. Tribute Webhook and Events

- [x] 3.1 Перед кодированием parser'а сверить актуальную Tribute OpenAPI
  форму `renewed_subscription` и `cancelled_subscription`.
- [x] 3.2 Реализовать `internal/store/events.go` repository для
  `tribute_events`: insert, dedup lookup, terminal status, errors.
- [x] 3.3 Реализовать redaction raw payload перед записью
  `tribute_events.payload_json`.
- [x] 3.4 Реализовать `internal/webhook/tribute.go`: raw body HMAC по
  `trbt-signature`, constant-time compare и invalid-signature audit.
- [x] 3.5 Реализовать `dedup_key` по Tribute fields с fallback на
  `sha256(raw_body)`; duplicate request должен быть `200` no-op.
- [x] 3.6 Парсить `new_subscription` и `renewed_subscription` в
  `SubscriptionEvent{Activated}` с `EventAt`, `ExpiresAt`, ids и tier.
- [x] 3.7 Парсить `cancelled_subscription` в отдельный cancel path с
  учётом `TRIBUTE_CANCEL_IS_IMMEDIATE`.
- [x] 3.8 Помечать unsupported Tribute events как `ignored` без domain
  side effects.

## 4. Status Core and Telegram Transport

- [x] 4.1 Расширить `domain.SubscriptionEvent` полями `EventAt`,
  `ExpiresAt`, `ExternalID`, `PeriodID`, `Tier` и provider event name.
- [x] 4.2 Обновить `engine.handleEvent`: применять Tribute webhook
  events только если `EventAt` новее `last_event_at`.
- [x] 4.3 Сохранять `expires_at`, external ids, tier,
  `last_signal='webhook'` и `last_event_at` для новых Tribute events.
- [x] 4.4 Реализовать безопасный `cancelled_subscription`: audit без
  отзыва при default и immediate deactivate при override.
- [x] 4.5 В режиме B не позволять `chat_member` Tribute channel
  истекать ledger-подписку из webhook.
- [x] 4.6 Реализовать Telegram webhook HTTP handler с проверкой
  `X-Telegram-Bot-Api-Secret-Token`.
- [x] 4.7 Прогнать Telegram webhook update через тот же durable
  `telegram_updates` inbox и router terminal state machine.

## 5. Ops Commands and Read Models

- [x] 5.1 Добавить store read methods для stats: subscriptions, grants,
  due revocations, health, reconcile freshness, outbox и alerts.
- [x] 5.2 Добавить read methods для open alerts list и configured chats.
- [x] 5.3 Добавить export read model для users + current subscriptions
  без raw payloads и secrets.
- [x] 5.4 Реализовать owner-only `/stats` с durable response.
- [x] 5.5 Реализовать owner-only `/alerts` с open alert summary.
- [x] 5.6 Реализовать owner-only `/chats` и `/help_admin`.
- [x] 5.7 Реализовать `/export` только в private chat и только для
  owners; не публиковать CSV в группах.
- [x] 5.8 Обновить `messages` и `setMyCommands` для final user/admin
  help и новых admin commands.

## 6. Privacy, Deploy and OSS Artifacts

- [x] 6.1 Добавить общий redaction helper для Telegram/Tribute raw
  payloads, логов, email, `web_app_link`, адресов и full invite URLs.
- [x] 6.2 Создать `deploy/gatekeeper.service` с `EnvironmentFile=`,
  restart policy и без embedded secrets.
- [x] 6.3 Создать `deploy/Dockerfile` для static Go binary без cgo.
- [x] 6.4 Обновить English `README.md`: quickstart, chats/rights, env,
  modes, backups, manual checklist и known limitations.
- [x] 6.5 Добавить `LICENSE` с MIT terms.
- [x] 6.6 Добавить `CONTRIBUTING.md` с build/test/lint/style и pattern
  for adding a new `Source`.
- [x] 6.7 Добавить `SECURITY.md` с private vulnerability reporting и
  secret-handling guidance.
- [x] 6.8 Добавить `.github/workflows/ci.yml` для build, test и lint на
  push/PR без production secrets.
- [x] 6.9 Подтвердить file-permission hardening: БД-файл `0600`,
  каталог данных `0700`; README рекомендует `0600` для `.env`.

## 7. Verification

- [x] 7.1 Добавить unit tests для Tribute HMAC: valid raw body,
  invalid signature и JSON re-marshal safety.
- [x] 7.2 Добавить tests для Tribute dedup, ignored events, failed
  event status и invalid signature audit.
- [x] 7.3 Добавить tests для `renewed_subscription` extension,
  out-of-order ignore, `cancelled_subscription` с будущим `expires_at`
  (default не отзывает) и `TRIBUTE_CANCEL_IS_IMMEDIATE=true` (отзывает).
- [x] 7.4 Добавить integration test: Tribute webhook creates active
  subscription, then `/start` can issue access links.
- [x] 7.5 Добавить tests для `/healthz`, `/readyz`, `/metrics` и route
  gating.
- [x] 7.6 Добавить tests для Telegram webhook secret, duplicate update
  id and shared router processing.
- [x] 7.7 Добавить tests для owner-only `/stats`, `/alerts`, `/export`,
  `/chats` и `/help_admin`.
- [x] 7.8 Проверить redaction: raw stored payloads do not contain email,
  `web_app_link`, secrets or full invite URLs.
- [x] 7.9 Запустить `task test`, `task build` и `task lint`.
- [x] 7.10 Запустить `openspec validate phase-07-webhook-ops-merge
  --strict` перед implementation.
- [x] 7.11 Проверить `openspec status --change phase-07-webhook-ops-merge`
  — все артефакты `done`.
