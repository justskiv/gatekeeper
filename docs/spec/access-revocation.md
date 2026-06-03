# Отзыв доступа

Безопасный отзыв доступа при потере подписки и hard-ban с инвариантами, которые не дают убрать активного, неопределённого, внешне добавленного или защищённого участника. Читаемое зеркало спеки `access-revocation`. Отзыв триггерится из `recomputeAccess` (см. [status-core](status-core.md)) и из Reconciler'а (см. [reconciliation](reconciliation.md)); сами kick/ban исполняются через durable outbox (см. [outbox-enforcer](outbox-enforcer.md)).

## Когда запускается отзыв

Автоматический отзыв стартует только при `effectiveStatus = inactive`. `unknown` не создаёт `pending_revocation`, `soft_kick`, `hard_ban` или иной revoke action; если хотя бы один источник даёт `active`, автоотзыв не начинается. Поведение задаёт `EXPIRY_MODE`:

- **grace** — при наличии bot-admitted grants в state `joined` или `pending` создаётся одна `pending_revocation` со `scheduled_at = now + GRACE_PERIOD`, пишется `audit_log(revocation_scheduled)` и ставится durable `send_dm` с `MSG_EXPIRY_WARNING`. Повторный пересчёт до `scheduled_at` не плодит вторую запись и второе предупреждение.
- **immediate** — вызывается `revokeNow` без grace-записи.
- **notify_only** — ставится `MSG_EXPIRED_NOTICE`, пишется audit, `access_grants` не меняются.

## Возврат active отменяет запланированный отзыв

Если пользователь с `pending_revocation` снова становится `active`, система удаляет pending revocation, пишет `audit_log(revocation_cancelled)` и ставит durable `send_dm` с `MSG_ACCESS_KEPT`. Если pending revocation не было, active-путь — no-op для revocation state (и `MSG_ACCESS_KEPT` не ставится).

## revokeNow и финальная проверка

`revokeNow(tgID, reason)` перед постановкой kick-действий делает финальную live-перепроверку `effectiveStatus`:

- **active** → удаляет pending revocation, пишет `audit_log(revocation_cancelled)`, ставит `MSG_ACCESS_KEPT` и завершается без kick.
- **unknown** → пользователя не кикает; оставляет или перепланирует pending revocation и создаёт operator-visible alert. Неопределённость никогда не превращается в kick.
- **inactive** → ставит `soft_kick` для каждого club resource, где grant существует, находится в `joined`/`pending` и имеет `admitted_by='bot'`. После постановки grant переходит в `revoked` с `revoked_at`/`revoked_reason`, pending revocation удаляется, audit получает `access_revoked`, а пользователь с открытой личкой — `MSG_REVOKED`.

## Защита от лишних киков

Автоотзыв выбирает только `access_grants` в state `joined`/`pending` с `admitted_by='bot'`. Grants с `admitted_by='external'` не получают автоматический `soft_kick` ни при событийном пересчёте, ни при reconciliation. Перед `soft_kick` система проверяет `isProtected(resource, tgID)` через `getChatMember`: `creator` или `administrator` не кикается — вместо этого создаётся `admin_alert` с kind `protected_admin_lost_subscription`.

## Hard-ban и unban

Hard-ban — явное owner-действие: выставляет `users.banned=1`, отменяет pending revocation и ставит `hard_ban` actions для bot-admitted club resources без последующего `unban`. Он перекрывает любые active subscriptions и whitelist, и до `/unban` join-request не одобряется. `/unban` (или соответствующий domain flow) выставляет `users.banned=0` и ставит `unban` actions только там, где нужно снять постоянный бан; доступ после unban возвращается лишь через обычный active status и pull-модель `/start` — сам по себе unban grants не создаёт.

## Приёмка

- Отзыв запускается только из `inactive`; `unknown` и наличие любого `active` источника отзыв не начинают.
- grace планирует ровно одну `pending_revocation` + `MSG_EXPIRY_WARNING` и идемпотентен; immediate зовёт `revokeNow` без grace; notify_only только уведомляет и не трогает grants.
- Возврат `active` до due удаляет pending и ставит `MSG_ACCESS_KEPT`, без `soft_kick`.
- `revokeNow` на финальной проверке: `active` → отмена + `MSG_ACCESS_KEPT`; `unknown` → без kick, pending сохранён/перепланирован + alert; `inactive` → `soft_kick` по bot-admitted ресурсам, grants `revoked`, `MSG_REVOKED` при открытой личке.
- `external` grants и creator/administrator автоотзывом не кикаются; для защищённого админа поднимается `protected_admin_lost_subscription`.
- Hard-ban: `users.banned=1`, `hard_ban` actions, перекрывает подписку/whitelist, без авто-unban; `/unban` сам доступ не выдаёт.
