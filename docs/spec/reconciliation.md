# Сверка: Reconciler и cleanup

Периодическая сверка durable-состояния с Telegram, health-checks, восстановление invite-ссылок и retention/maintenance cleanup процесса `gatekeeper`. Читаемое зеркало спеки `reconciliation`. Reconciler не принимает доменных решений и не делает Telegram-вызовов сам: он ставит durable actions в outbox и зовёт `revokeNow` (см. [outbox-enforcer](outbox-enforcer.md), [access-revocation](access-revocation.md)).

## Периодичность и старт

Reconciler работает с периодом `RECONCILE_INTERVAL`. После успешного startup wiring и до запуска poller loop выполняется один pass — он закрывает gap простоя и исполняет due revocations, накопившиеся, пока процесс был выключен. Каждый pass идемпотентен: повтор после рестарта не создаёт дублей исполнения due revocation, verify-действий и shared-invite ensures. В конце успешного прохода пишутся `meta.reconcile.last_run_at` и audit-summary с машиночитаемыми счётчиками.

## Исполнение due revocations

Reconciler выбирает `pending_revocations` со `scheduled_at <= now` и на каждую запись вызывает `revokeNow` из access-revocation flow. Сам он не решает о kick, не меняет `access_grants` в обход `revokeNow` и не вызывает Telegram напрямую. Если `revokeNow` не может безопасно исполнить отзыв (`unknown` или потеря прав), Reconciler сохраняет состояние, достаточное для повторной попытки, и поднимает durable `admin_alert`.

## Сверка членства

Candidates на сверку формируются из union релевантных пользователей: active subscriptions, bot-admitted `joined` grants и whitelist-строки. На каждого ставится идемпотентный `verify_member` через outbox — самостоятельного `getChatMember` Reconciler не делает. Результаты применяются теми же domain-путями, что и обычные membership-события: source membership обновляет подписки и запускает `recomputeAccess`, club membership обновляет `access_grants`. `unknown` при verify не закрывает active-подписку и не отзывает доступ.

## Health и invite-ссылки

Каждый pass проверяет health четырёх настроенных чатов тем же chat-health contract, что и startup/`my_chat_member`, и обновляет стабильные ключи `meta.health.*`; потеря прав создаёт durable `admin_alert`, но не роняет процесс. При `INVITE_MODE=shared_join_request` Reconciler проверяет наличие активной shared join-request ссылки для club chat и club channel и при её отсутствии ставит идемпотентный `ensure_invite`. При `personal_join_request` или `direct` он переводит истёкшие active links в `expired` и при необходимости ставит `revoke_invite`.

## Cleanup

Cleanup loop работает с периодом `CLEANUP_INTERVAL` и применяет retention/обслуживание: удаляет terminal raw inbox rows старше `RAW_RETENTION`, done outbox actions старше retention-окна, старые resolved alerts, истекает personal/direct invite links, применяет `AUDIT_RETENTION` к audit log и делает SQLite WAL checkpoint. Cleanup не удаляет автоматически по возрасту `telegram_updates`/`tribute_events` со status `failed` и `access_actions` со status `dead` — эти строки остаются forensic material до ручного разбора.

## Приёмка

- Один pass выполняется до poller loop и исполняет накопленные due revocations; успешный проход пишет `meta.reconcile.last_run_at` и audit-summary.
- Due revocations исполняются строго через `revokeNow`; `soft_kick` появляется только внутри access-revocation flow; прямых Telegram-вызовов из Reconciler нет.
- Verify-кандидаты берутся из union подписок/grants/whitelist, ставятся как идемпотентные `verify_member`; `unknown` при verify не закрывает доступ.
- Health-проход обновляет `meta.health.*` и поднимает alert при потере прав, не прерывая остальные проверки; shared-режим восстанавливает ссылку через `ensure_invite`, истёкшая personal link → `expired` + `revoke_invite`.
- Cleanup чистит terminal/retention-строки и делает WAL checkpoint, но `failed`/`dead` строки по возрасту не трогает.
