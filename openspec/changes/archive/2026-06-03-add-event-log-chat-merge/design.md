## Context

Gatekeeper уже пишет факты в `audit_log`, создаёт `admin_alerts`, хранит
`access_grants` / `subscriptions` / `pending_revocations` и выполняет
Telegram side effects через durable `access_actions`. Доставка алертов в
`ADMIN_LOG_CHAT_ID` уже реализована тем же приёмом (`send_dm` с
`payload_json.chat_id`). Не хватает читаемого owner-facing потока событий
доступа и членства.

Этот документ — мердж `-claude` и `-codex`. База контракта — `-codex`
(typed events в точке доменного решения). Из `-claude` — продуктовая
форма и два инварианта надёжности. Инварианты системы сохраняются:
Telegram-вызовы не делаются внутри handler-транзакции; `unknown` не
ведёт к отзыву; invite URL / raw payload / секреты / chat ID не попадают
в owner-facing сообщения; причины берутся только из уже доступных данных
(`AccessDecision.Reasons`, `subscriptions`, `access_grants`, invite mode,
actor текущего workflow).

## Goals / Non-Goals

**Goals:**
- Owner-facing поток в отдельный `EVENT_LOG_CHAT_ID`: членство managed
  ресурсов (joined/left чат, subscribed/unsubscribed канал) и жизненный
  цикл доступа (granted, loss-scheduled, lost, kept, manual, ban/unban,
  banned-join).
- Безопасная причина из существующих данных: активные источники,
  manual/admin reason, admission method, resource.
- Атомарность: доменное изменение, audit и event-action — в одной
  транзакции.

**Non-Goals:**
- Новая таблица/миграция; новые уровни доступа/правила по платформам.
- Новые сообщения пользователю (это operator-only поток).
- Полный invite URL / raw webhook / email / secret в фиде.
- Replay/backfill operator log из `audit_log`.

## Decisions

### D1. Typed event в точке события (контракт), projection — лишь оптимизация

Каждый workflow, уже знающий контекст (admission, engine access
lifecycle, manual-команды, runtime wiring), вызывает общий
`operatorlog.Writer` с типизированным `OperatorEvent`. Writer ставит
durable `send_dm` с `payload_json.chat_id = EVENT_LOG_CHAT_ID`.

*Почему контракт типизированный, а не проекция `audit_log`:* текущий
`audit_log.detail` местами не несёт структурного «почему/как» — все
активные source-reasons, actor внешнего входа, invite/admission context,
`scheduled_at` без парсинга текста. Проекция как реализация допустима
там, где audit-строка уже несёт достаточно, но контракт обязан позволять
typed-контекст рядом с доменным решением (иначе теряется корректность,
см. D5/D6). Альтернатива «фоновый поллер по `audit_log`» отвергнута:
detail не всегда полон, replay усложняет offsets/dedupe/формат.

### D2. Существующий outbox action, без новой миграции

`access_actions` уже поддерживает `send_dm` с `chat_id` в payload, а
Enforcer умеет слать в произвольный чат. Новый action type не вводится.
Idempotency-ключ строится из стабильного маркера события (kind, tg_id,
resource, стабильный workflow-marker — join-request date/resource или
confirmed action id; допустим source-stable event-time из Telegram/
provider), но **не из process wall-clock/render time** (`time.Now()`),
который отличается между ретраями и ломает dedupe.

### D3. Grace = два события

При `EXPIRY_MODE=grace` истечение основания не означает фактическую
потерю: пишем `access_loss_scheduled` при создании `pending_revocation`,
затем `access_lost` только когда due-revocation реально перевёл гранты в
`revoked`. Если подписка вернулась до due — `access_kept`. `notify_only`
не пишет resource-loss (гранты не меняются).

### D4. `access_granted` — на переходе, не на каждом сигнале

Engine сравнивает persisted effective decision до/после локального
события. `access_granted` эмитится только на переходе non-active→active.
Если статус остался active из-за другого источника — только `source_*`,
без lifecycle-потери/выдачи. `source_subscription_activated/_expired` —
отдельный, сырой per-platform сигнал. `unknown` не порождает потерю;
если переход нельзя определить без сети в транзакции — не выдумываем,
полагаемся на source/admission/revocation события.

*Почему так, а не «каждый `subscription_activated` = получил доступ»:*
плоская проекция over-report'ит «получил доступ» уже-активному
пользователю и теряет multi-source контекст. Это была ошибка `-claude`.

### D5. Admission method и все активные причины

Method — малый enum: `bot_link` (есть bot-evidence), `external` (нет
evidence; админ добавил — сюда), `admin` (owner-команда), `provider`
(source-событие), `job` (reconcile/revocation). Если Telegram отдал
`from` для external join — показываем безопасный label актора; если нет
— `external` без догадок. `access_granted` перечисляет *все* активные
основания (например boosty + tribute сразу), fallback по свежей active-
подписке помечается отдельно.

### D6. Privacy — общая граница с operator messages

Тексты живут в `messages`, проходят HTML-renderer. Полные invite URL и
raw payload в renderer не передаются; invite-контекст — только mode,
resource, `invite_link_hash`/safe label. Chat ID не показываются в
обычных событиях. Redaction — отказом от небезопасного поля на входе, не
пост-обработкой готового HTML.

### D7. Health лог-чата — non-fatal (правка `-codex`)

Сбой проверки доступности `EVENT_LOG_CHAT_ID` на старте — **warning, не
boot-error**. Бот стартует и обслуживает апдейты независимо от
доступности фида: observability не имеет права ронять ядро контроля
доступа. Право бота писать в группу мониторит существующий chat-health
(`bot_rights_lost`-стиль alert) в runtime; permanent delivery fail идёт
в `outbox_action_dead` alert. Это покрывает codex'ово «бот должен уметь
писать», но fail-open, а не fail-closed.

### D8. Build-ошибки и persistence-ошибки разведены (правки `-claude` + ревью)

Две разные категории сбоев, чтобы «atomic» и «не валит access-control» не
противоречили друг другу (finding ревью):
- **feed-build** (рендер/шаблон до записи): логируется, событие
  пропускается, доменную операцию НЕ откатывает; exactly-once на такое
  событие не обещается.
- **persistence** (сам INSERT `access_actions`): для успешно собранного
  события идёт в той же транзакции, что доменное изменение (atomic,
  exactly-once); сбой INSERT = сбой БД и подчиняется общим правилам
  транзакции.

### D9. Enforcer 403 для group-таргета (правка по ревью)

Текущий Enforcer считает любой `sendMessage 403` для `send_dm` как
DM-blocked и метит `users.dm_state='blocked'` по `action.TGID`, затем
`done`. Для feed-таргета (`payload.chat_id` = группа) это (а) метит
случайного субъекта заблокировавшим личку, (б) «доставляет» событие без
`outbox_action_dead`. Дельта `outbox-enforcer`: `403` для send_dm с явным
group `chat_id` идёт в permanent/dead + alert, без мутации `dm_state`.
Это чинит и латентный баг существующей alert-доставки в `ADMIN_LOG_CHAT_ID`.

### D10. Источники доступа на событиях входа (запрос владельца)

`club_chat_joined` / `club_channel_subscribed` несут не только способ
входа, но и активные основания доступа (boosty/tribute/manual) из
сохранённых `subscriptions` / последнего `AccessDecision` (без сетевого
probe в handler'е), чтобы оператор видел *почему* вошедший имеет доступ.
Если активного источника нет — «источник неизвестен», без догадок.

## Decision log (что откуда)

| Аспект | Источник | Решение |
|---|---|---|
| Контракт = typed event в точке решения | codex | принят |
| Capability name | claude | `event-log` |
| `EVENT_LOG_CHAT_ID` отдельный, required, distinct | codex | принят |
| Health-check как boot-gate | codex | **отклонён** → non-fatal + chat-health alert (D7) |
| Эмит не валит доменную операцию | claude | добавлен (D8) |
| `banned_join_attempt` | claude | возвращён (codex потерял) |
| grace=2 события, access_kept | оба | принят |
| `access_granted` на переходе, multi-source | codex | принят |
| actor external join из `from` | codex | принят |
| privacy boundary + leak tests | codex | принят |
| forward-only / no backfill | claude | принят |
| компактность design/tasks | claude | принят, где не режет требования |
| Enforcer 403 для group-таргета | ревью | дельта `outbox-enforcer` (D9) |
| chat-health для `EVENT_LOG_CHAT_ID` | ревью | дельта `chat-health`, posting-not-admin |
| `access_granted` — одна семантика + dedupe | ревью | уточнён в `grant-access`/`status-core` |
| build vs persistence ошибки | ревью | разведены (D8) |
| idempotency: source-time да, wall-clock нет | ревью | уточнён |
| источники на событиях входа | владелец | добавлено (D10) |

## Event mapping (product shape)

| Event kind | Эмитится | Безопасный контекст |
|---|---|---|
| `source_subscription_activated` / `_expired` | engine `handleEvent` | platform, tier, expiry |
| `access_granted` | engine-переход / admission / `/grant` | все active reasons, method, resources, invite mode |
| `access_loss_scheduled` | revocation scheduling (grace) | reason, `scheduled_at` |
| `access_lost` | `revokeNow` фактический отзыв | revoked resources, reason, hard-ban override |
| `access_kept` | отмена pending revocation | active reasons |
| `club_chat_joined` / `club_chat_left` | club chat `chat_member` | method, actor (если есть), активные источники доступа |
| `club_channel_subscribed` / `unsubscribed` | club channel `chat_member` | method, actor (если есть), активные источники доступа |
| `manual_grant` / `manual_revoke` / `manual_ban` / `manual_unban` | confirmed owner-команда | actor admin, safe reason |
| `banned_join_attempt` | banned external join | resource |

## Risks / Trade-offs

- [Дубли при retry handler'а] → idempotency по стабильному workflow-
  marker, не timestamp (D2).
- [Причина недоступна для external join] → явный `external`, без догадок
  об админе (D5).
- [Лог-чат недоступен / бот не может писать] → fail-open: старт не
  блокируется, chat-health alert + `outbox_action_dead` alert (D7).
- [`access_granted` vs membership joined похожи] → разные kinds и
  заголовки: право vs фактическое членство.
- [Шум] → scope ограничен названными + смежным циклом; технический
  health/metrics в фид не идут.
- [Спек-поверхность шире проекции] → осознанно: typed-контракт покупает
  корректность (D4/D5), которой плоская проекция не даёт.

## Migration Plan

1. `internal/operatorlog`: typed event model + writer (durable `send_dm`).
2. `messages`: renderer событий + escaping/leak-тесты.
3. Подключить writer в admission (access request, join-request, club
   `chat_member`).
4. Подключить в engine revocation/access lifecycle.
5. Подключить в owner-команды (grant/revoke/ban/unban).
6. Прокинуть writer из runtime через router/engine/admin до старта
   транспорта; health — non-fatal + chat-health monitor.
7. Тесты: idempotency, разделение каналов, privacy, grace two-event,
   unknown/protected suppression, non-fatal feed health.

Rollback не требует schema rollback: queued/done `send_dm` остаются
валидными; отключение writer прекращает новые события.

## Open Questions

- Показывать ли display-name внешнего актора (`from`), когда Telegram его
  прислал, или достаточно `external` — по умолчанию показываем safe
  label, если он есть.
