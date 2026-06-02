## Context

Gatekeeper уже умеет принимать Telegram updates, вычислять
`effectiveStatus`, выдавать доступ через grant-access flow и выполнять
Telegram side effects через durable outbox. Незаполненная часть
жизненного цикла — потеря подписки: `recomputeAccess` пока снимает
только запланированный отзыв при `active`, а ветка `inactive` намеренно
оставлена до этой фазы.

Главные ограничения сохраняются прежними: Telegram-вызовы не выполняются
внутри handler transaction (`tx2`), durable domain state и
`access_actions` коммитятся вместе, `unknown` никогда не приводит к
отзыву, а доступ выдаётся только pull-моделью по запросу пользователя.
Эта фаза добавляет только закрытие доступа, operator controls и
background reconciliation.

## Goals / Non-Goals

**Goals:**

- Реализовать `inactive` ветку `recomputeAccess` для режимов
  `grace`, `immediate` и `notify_only`.
- Сделать `revokeNow` безопасным за счёт финальной живой перепроверки
  `effectiveStatus` перед постановкой `soft_kick`.
- Отзывать только bot-admitted grants и никогда автоматически не трогать
  `external` участников, creator/admin клубных ресурсов и `unknown`.
- Запустить Reconciler и cleanup ticker под тем же supervision model,
  что poller и Enforcer.
- Доставлять `admin_alerts` владельцу через durable outbox без ожидания
  ручного `/alerts`.
- Добавить owner-команды управления доступом с подтверждением опасных
  действий.

**Non-Goals:**

- Tribute webhook mode, HTTP health endpoints, metrics и production
  readiness Фазы 07.
- Новые таблицы, миграции или rule engine для tiers/entitlements.
- Массовая выдача доступа без пользовательского `/start` или
  join-request.
- Автоматический кик `external` участников клубных ресурсов.
- Синхронные Telegram-вызовы из bot/admin handlers или engine.

## Decisions

### D1. Отзыв остаётся частью engine, а Telegram side effects идут через outbox

`recomputeAccess` принимает доменное решение, пишет audit,
`pending_revocations`, grant state и outbox actions в короткой
transaction. Фактический `banChatMember`/`unbanChatMember` выполняет
Enforcer.

Альтернатива — вызывать Telegram прямо из `revokeNow` — отвергнута:
это нарушает I2, держит SQLite transaction во время сети и делает
рестарт между state change и Telegram side effect неустойчивым.

### D2. `revokeNow` всегда делает финальную live-перепроверку

Due revocation может исполняться спустя часы после warning. За это
время пользователь мог продлить подписку, а событие восстановления могло
не дойти. Поэтому `revokeNow` перед soft-kick делает live
`effectiveStatus`; если статус стал `active`, pending revocation
удаляется, пишется `revocation_cancelled`, пользователю уходит
`MSG_ACCESS_KEPT`, а kick не ставится.

Альтернатива — доверять только сохранённому due state — отвергнута как
опасная: она удаляет платящего пользователя в самой чувствительной
гонке фазы.

### D3. Protection проверяется до постановки kick-действия

Engine выбирает только grants со state `joined`/`pending` и
`admitted_by='bot'`, затем для каждого resource вызывает
`isProtected(resource, tgID)`. Creator/admin не получает kick action;
вместо этого создаётся `admin_alert` с kind
`protected_admin_lost_subscription`.

Альтернатива — положиться только на Enforcer no-op для creator/admin —
оставила бы grant в неоднозначном состоянии и не дала бы владельцу
явного сигнала.

### D4. Reconciler не принимает access-решений сам

Reconciler исполняет due revocations через `revokeNow`, ставит
`verify_member` actions для кандидатов, проверяет health и invite-link
целостность. Решения об active/inactive/unknown остаются в
source/engine paths, чтобы один источник truth не раздвоился между
пакетами.

Альтернатива — делать `getChatMember` и менять grants прямо в
Reconciler — отвергнута: это обходит outbox throttling и усложняет
идемпотентность при больших базах.

### D5. Admin commands проходят через compact confirmation flow

`/grant`, `/revoke`, `/ban`, `/unban` и `/sync` сначала создают
короткоживущий action id для callback confirmation. Повторный callback
идемпотентен: после успешного исполнения он видит уже terminal action
state и не дублирует grants, bans, reconciliation passes или outbox
actions.

Альтернатива — выполнять опасные команды сразу после текста — отвергнута
из-за риска ошибочного hard-ban, массового revoke по опечатке в `tg_id`
или незапланированного full `/sync`.

### D6. Alert delivery привязана к созданию alert, а не к конкретному caller

`Alerts.Create` остаётся единой точкой создания тревоги. Когда alert
создана, тот же transactional path ставит `send_dm` владельцам или
сообщение в `ADMIN_LOG_CHAT_ID`. Это даёт одинаковую durable-доставку
для chat-health, Enforcer dead actions, Reconciler и admin flows.

Альтернатива — вручную отправлять DM в каждом месте raise alert —
быстро расходится: новые alert kinds легко забывают push-доставку.

## Risks / Trade-offs

- **R1. Reconciler может не уложиться в интервал на большой базе.** →
  Проходы идемпотентны, сетевые проверки идут через throttled outbox;
  при росте базы интервал можно увеличить или добавить шардирование в
  следующей фазе.
- **R2. Финальная live-перепроверка может вернуть `unknown`.** → Отзыв
  не исполняется при `unknown`; pending revocation остаётся или
  перепланируется, а владелец получает alert о невозможности проверить
  источник.
- **R3. Hard-ban может оставить пользователя в ресурсе, если бот потерял
  права.** → `hard_ban` action ретраится через Enforcer, dead action
  создаёт alert, а health/reconcile дополнительно подсветят потерю прав.
- **R4. Alert delivery может создать дубли при повторе одной причины.** →
  Alert repository должен использовать стабильные dedupe keys для
  открытых alerts, а outbox idempotency key строится от alert id или
  dedupe key.
- **R5. `/grant` по неизвестному `tg_id` создаёт неполный user profile.**
  → Stub row допустима только для числового `tg_id`; username lookup
  остаётся локальным и не ходит в Telegram.

## Migration Plan

1. Расширить store repositories для revocations, grants, whitelist,
   manual subscriptions, alerts и cleanup.
2. Добавить engine paths для `inactive`, `revokeNow`, `isProtected` и
   hard-ban/manual flows.
3. Реализовать Reconciler и cleanup ticker без новых миграций.
4. Добавить admin command handlers, confirmation callbacks и messages.
5. Подключить runtime wiring: Reconciler initial run до poller loop,
   periodic loops под supervised context.
6. Покрыть safety-инварианты и end-to-end soft-kick тестами.
7. Проверить `task test`, `task lint` и
   `openspec validate phase-06-revocation-merge --strict`.

Rollback не требует schema rollback: код можно откатить, а уже
существующие строки `pending_revocations`, `access_actions`,
`admin_alerts` и `audit_log` остаются валидными данными v1. Оператор
может вручную остановить процесс, разобрать queued/dead actions и снова
запустить предыдущий бинарь.

## Open Questions

- Нужен ли отдельный `admin_alert` при `notify_only`, или достаточно DM
  пользователю и audit записи?
- Насколько подробным должен быть `/sync` summary в callback message:
  только счётчики или список проблемных ресурсов тоже?
