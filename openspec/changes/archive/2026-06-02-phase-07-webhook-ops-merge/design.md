## Context

Gatekeeper уже имеет доменную модель подписок, durable Telegram inbox,
outbox, reconciliation, chat-health сигналы и таблицу `tribute_events`.
Фаза 07 добавляет production-контур вокруг этих механизмов: HTTP entry
points, Tribute mode B, ops-команды, метрики и deploy/OSS артефакты.

Главные ограничения:

- polling остаётся дефолтом, чтобы бот можно было запускать без
  публичного HTTPS;
- исходящие Telegram side effects по-прежнему идут только через outbox;
- `readyz` не изобретает отдельную модель здоровья, а читает уже
  существующие `meta.health.*` и `meta.reconcile.last_run_at`;
- HMAC Tribute считается только по raw body до JSON parse;
- raw payload хранится только после redaction.

## Goals / Non-Goals

**Goals:**

- Включить HTTP-сервер только для webhook/metrics режимов.
- Обработать Tribute webhooks идемпотентно и безопасно к ретраям,
  дубликатам и out-of-order событиям.
- Дать оператору `/healthz`, `/readyz`, `/metrics`, `/stats`,
  `/alerts`, `/export`, `/chats` и `/help_admin`.
- Подготовить production packaging: systemd, Dockerfile, README,
  LICENSE, CONTRIBUTING, SECURITY и CI.
- Сохранить существующие invariants: единый доступ от любого active
  source, `unknown` не отзывает доступ, hard-ban сильнее подписки.

**Non-Goals:**

- Не вводить тарифы, entitlements, access scopes или разные уровни
  доступа.
- Не добавлять неофициальный Boosty API.
- Не делать webhook режим Telegram дефолтом.
- Не добавлять поиск по email. Если позже понадобится email index, он
  должен храниться только как keyed HMAC в отдельном change.
- Не менять текущую SQLite-схему без необходимости: `tribute_events`,
  `subscriptions.expires_at` и `subscriptions.last_event_at` уже есть.

## Decisions

### HTTP lifecycle

HTTP-сервер живёт в отдельном пакете `internal/webhook` и стартует под
общим `errgroup` только если включён хотя бы один HTTP-facing режим:
`TRIBUTE_MODE=webhook`, `TELEGRAM_MODE=webhook` или
`METRICS_ENABLED=true`. В чистом polling+observation процессе listener
не открывается.

Альтернатива — всегда поднимать `/healthz`/`/readyz`. Она проще для
оркестраторов, но ломает важное свойство проекта: дефолтный запуск не
требует открытого HTTP-порта.

### Tribute webhook security and inbox

Обработчик Tribute читает тело через `http.MaxBytesReader`, сохраняет
raw bytes в памяти, проверяет `trbt-signature` как
`hex(HMAC-SHA256(TRIBUTE_API_KEY, rawBody))` через constant-time
comparison и только после этого парсит JSON. Invalid signature всё равно
создаёт forensic row в `tribute_events` с redacted payload, но отвечает
`401` и не вызывает domain handlers.

Дедупликация идёт по stable `dedup_key`:
`sha256(name|subscription_id|period_id|created_at)`, fallback —
`sha256(rawBody)`, если provider payload не содержит нужных полей.
Дубликат получает `200` и не повторяет domain side effects.

### Tribute event ordering

Webhook events конвертируются в доменные `SubscriptionEvent` с
`EventAt`, `ExpiresAt`, external identifiers и tier. `Activated`
применяется только если `EventAt` новее текущего
`subscriptions.last_event_at` для active Tribute row. Старые события
помечаются ignored и не укорачивают `expires_at`.

`cancelled_subscription` по умолчанию не превращается в отзыв доступа:
это сигнал "не продлю", а доступ живёт до `expires_at`. Флаг
`TRIBUTE_CANCEL_IS_IMMEDIATE=true` оставлен как явный emergency override
и обрабатывает cancel как immediate deactivation.

### Telegram webhook transport

Polling остаётся основным transport. При `TELEGRAM_MODE=webhook`
runtime не запускает poller, а монтирует POST endpoint
`TELEGRAM_WEBHOOK_PATH`, проверяет
`X-Telegram-Bot-Api-Secret-Token` и передаёт update в тот же durable
inbox/router pipeline. Это удерживает одну семантику terminal statuses,
audit, outbox и recovery.

Runtime регистрирует Telegram webhook через Bot API с
`TELEGRAM_WEBHOOK_PUBLIC_URL + TELEGRAM_WEBHOOK_PATH`, secret token и
тем же `allowed_updates`, что у polling. Успешная регистрация является
условием readiness при `TELEGRAM_MODE=webhook`.

Режим B меняет роль `TRIBUTE_CHANNEL_ID`: membership updates канала не
должны истекать ledger-подписку из webhook. Они остаются сверочным
сигналом для live status и диагностики.

### Readiness and metrics

`/readyz` объединяет проверку SQLite/schema, успешный startup `getMe`,
четыре `meta.health.*`, свежесть `meta.reconcile.last_run_at` и, только
для `TELEGRAM_MODE=webhook`, успешную регистрацию Telegram webhook.
Отдельная readiness-модель не нужна: Reconciler и chat-health уже
владеют основными health-сигналами, а runtime владеет фактом webhook
registration.

Метрики собираются из durable state и lightweight counters. Для базовой
эксплуатации они необязательны, поэтому `/metrics` существует только при
`METRICS_ENABLED=true`.

### Redaction boundary

Redaction применяется до записи raw payload в `telegram_updates` и
`tribute_events`, а также перед логированием provider payload. Правило
простое: секреты, email, `web_app_link`, трекинговые/адресные поля и
полные invite URLs не сохраняются в открытом виде. Domain fields,
нужные для обработки (`telegram_user_id`, `expires_at`,
`subscription_id`, `period_id`, `subscription_name`), извлекаются из
raw body до redaction и передаются дальше типизированно.

## Risks / Trade-offs

- [Risk] Актуальная Tribute OpenAPI схема отличается от локальной
  product spec → Mitigation: перед реализацией parsing layer сверить
  payload `renewed_subscription` и `cancelled_subscription` с
  официальной схемой; parser покрыть table tests.
- [Risk] HTTP server может начать принимать Telegram webhook до полной
  готовности dependencies → Mitigation: монтировать handler только
  после startup wiring и initial reconcile; `/readyz` возвращает `503`
  до готовности.
- [Risk] Invalid Tribute signatures могут зашумить inbox и alerts →
  Mitigation: ограничить body size, писать compact redacted row и
  дедуплицировать/агрегировать `webhook_signature_failures`.
- [Risk] `/export` раскрывает персональные данные владельцу случайно в
  общем чате → Mitigation: команда работает только для owners и только
  в DM, результат отправляется durable DM/document path.
- [Risk] Metrics cardinality может вырасти из-за labels → Mitigation:
  labels только из малых enum'ов (`platform`, `resource`, `state`,
  `type`, `status`, `method`, `code`), без `tg_id` и raw error text.

## Migration Plan

1. Добавить HTTP/webhook/metrics код без изменения дефолтного режима.
2. Реализовать `tribute_events` repository и redaction перед raw writes.
3. Подключить Tribute webhook parser к engine events и покрыть HMAC,
   dedup, ordering и cancel tests.
4. Подключить Telegram webhook endpoint к существующему inbox/router.
5. Добавить ops-команды и read-only store methods для сводок/export.
6. Добавить deploy/OSS артефакты и CI.

Rollback: вернуть `TRIBUTE_MODE=observation`,
`TELEGRAM_MODE=polling`, `METRICS_ENABLED=false` и не направлять внешний
трафик на HTTP endpoint. Durable rows `tribute_events` остаются
forensic material и не мешают observation mode.

## Open Questions

- Точная актуальная форма `renewed_subscription` и
  `cancelled_subscription` в Tribute OpenAPI должна быть подтверждена
  перед кодированием parser'а.
