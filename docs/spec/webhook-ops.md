# Webhook и операционные эндпойнты

Читаемое зеркало спеки `webhook-ops`.

## Когда поднимается HTTP-сервер

HTTP-сервер на `WEBHOOK_LISTEN_ADDR` поднимается только если включён хотя бы один из режимов: `TRIBUTE_MODE=webhook`, `TELEGRAM_MODE=webhook` или `METRICS_ENABLED=true`. В чистом polling-режиме (`TELEGRAM_MODE=polling`, `TRIBUTE_MODE=observation`, `METRICS_ENABLED=false`) listener не открывается вовсе.

Сервер настроен жёстко: явные `ReadHeaderTimeout` и `ReadTimeout`, ограничение тела запроса на webhook-маршрутах через `MaxBytesReader` (около 64 KiB) и graceful `Shutdown` при отмене контекста процесса.

## Маршруты

Набор маршрутов зависит от включённых режимов:

- `GET /healthz` — пока сервер запущен;
- `GET /readyz` — пока сервер запущен;
- `GET /metrics` — только при `METRICS_ENABLED=true`;
- `POST {TRIBUTE_WEBHOOK_PATH}` — только при `TRIBUTE_MODE=webhook`;
- `POST {TELEGRAM_WEBHOOK_PATH}` — только при `TELEGRAM_MODE=webhook`.

Маршрут, отключённый режимом, не монтируется и отдаёт `404`. Неподдерживаемый метод на включённом webhook-пути отдаёт `405`. То есть при `METRICS_ENABLED=true` и выключенных webhook-режимах работают `/metrics`, `/healthz`, `/readyz`, а Tribute- и Telegram-пути возвращают `404`.

## Liveness и readiness

`GET /healthz` — это liveness: возвращает `200` сразу после старта HTTP-сервера и не делает ни сетевых, ни обращений к БД.

`GET /readyz` отдаёт `200` только когда все входы готовности в порядке: SQLite доступна, `store.CheckSchema` проходит, стартовый `getMe` отработал, все четыре ключа `meta.health.*` равны `ok`, а `meta.reconcile.last_run_at` присутствует и не старше двух периодов `RECONCILE_INTERVAL`. В режиме `TELEGRAM_MODE=webhook` дополнительным входом готовности становится успешная регистрация Telegram-webhook'а.

Если хотя бы один вход плох, `/readyz` отдаёт `503` с компактным машиночитаемым описанием упавших проверок. Например, при `meta.health.club_channel` со значением, начинающимся с `fail:`, ответ называет `club_channel`; при просроченной реконсиляции ответ называет stale-реконсиляцию; при незарегистрированном webhook'е (в webhook-режиме) ответ называет регистрацию webhook'а.

## Метрики

При `METRICS_ENABLED=true` эндпойнт `GET /metrics` отдаёт Prometheus text format с метриками на ограниченных (enum) лейблах. Лейблы не содержат `tg_id`, username, email, invite-URL, сырых строк ошибок и прочих неограниченных PII — только перечислимые значения вроде `platform`, `resource`, `state`, `type`, `status`, `method`, `code`.

Набор метрик:

- `gatekeeper_updates_total{type}`;
- `gatekeeper_webhooks_total{status}`;
- `gatekeeper_subscriptions_active{platform}`;
- `gatekeeper_access_grants{resource,state}`;
- `gatekeeper_invite_links{resource,mode,status}`;
- `gatekeeper_outbox_actions_total{type,status}`;
- `gatekeeper_outbox_pending`;
- `gatekeeper_telegram_api_errors_total{method,code}`;
- `gatekeeper_reconcile_duration_seconds`;
- `gatekeeper_revocations_total{reason}`.

## Tribute webhook: проверка подписи

`POST {TRIBUTE_WEBHOOK_PATH}` сначала читает сырое тело запроса (с лимитом ~64 KiB) — *до* разбора JSON — и проверяет заголовок `trbt-signature` против `hex(HMAC-SHA256(key=TRIBUTE_API_KEY, msg=raw_body))` сравнением в постоянном времени. Подпись считается по исходным байтам, а не по повторно сериализованному JSON: пробелы и порядок ключей в теле не влияют на проверку, корректная подпись по raw-body принимается.

При неверной подписи обработчик пишет редактированную строку `tribute_events` с `signature_valid=0` и `status='failed'`, добавляет запись `audit_log(webhook_rejected)`, обновляет webhook-метрики и возвращает `401`. Движок (engine handlers) при неверной подписи не вызывается, состояние подписок не меняется.

## Tribute webhook: идемпотентность и обработка событий

Для валидного webhook'а вычисляется `dedup_key` как `sha256(name|payload.subscription_id|payload.period_id|created_at)`. Если какие-то из этих полей недоступны, используется fallback `sha256(raw_body)`. Повторный `dedup_key` возвращает `200` и не повторяет доменные side-эффекты — ни второго обновления подписки, ни audit-события, ни пересчёта доступа.

Обработчик сохраняет строку `tribute_events` с `signature_valid=1`, редактированным `payload_json`, именем события провайдера, опциональным `tg_id`, id подписки, статусом и временными метками. Поддерживаемые события подписки — `new_subscription`, `renewed_subscription`, `cancelled_subscription`. Прочие события Tribute фиксируются со статусом `ignored` и отдают `200`.

`new_subscription` и `renewed_subscription` создают доменное событие `Activated` для платформы `tribute` с `EventAt`, `ExpiresAt`, `ExternalID`, `PeriodID` и tier из payload'а Tribute (для `renewed_subscription` с более свежим `created_at` и более поздним `expires_at` активная Tribute-подписка сохраняет поздний `expires_at`, а `tribute_events.status` становится `processed`). `cancelled_subscription` использует семантику отмены из status-core.

При внутренней ошибке обработки валидного неповторного webhook'а ответ — `5xx` (Tribute повторит доставку), а сохранённое событие остаётся `failed`/`received` с деталями ошибки, достаточными для разбора оператором.

## Поведение

- Чистый polling (`TELEGRAM_MODE=polling`, `TRIBUTE_MODE=observation`, `METRICS_ENABLED=false`) → listener не открывается.
- `METRICS_ENABLED=true` без webhook-режимов → сервер стартует; `/metrics`, `/healthz`, `/readyz` доступны; Tribute/Telegram webhook-пути → `404`.
- Отключённый режимом маршрут → `404`; неподдерживаемый метод на включённом webhook-пути → `405`.
- `/healthz` после старта → `200`, без обращений к БД и сети.
- `/readyz` при всех здоровых входах → `200`; при любом плохом → `503` с именами упавших проверок.
- `meta.health.club_channel` начинается с `fail:` → `/readyz` `503`, назван `club_channel`.
- `meta.reconcile.last_run_at` старше двух `RECONCILE_INTERVAL` → `/readyz` `503`, названа stale-реконсиляция.
- `TELEGRAM_MODE=webhook` и webhook не зарегистрирован → `/readyz` `503`, названа регистрация webhook'а.
- `METRICS_ENABLED=false` → `/metrics` `404`; `true` → `200`, парсится как Prometheus text.
- Валидная подпись `trbt-signature` по сырому телу → JSON разбирается, обработка продолжается.
- Неверная подпись → `401`, `tribute_events.signature_valid=0`/`status='failed'`, `audit_log(webhook_rejected)`, движок не вызывается, состояние не меняется.
- Повторный `dedup_key` → `200`, без повторных side-эффектов.
- Валидное событие, не относящееся к подписке → `tribute_events.status='ignored'`, `200`.
- Внутренняя ошибка обработки → `5xx`, событие остаётся `failed`/`received` с деталями.
