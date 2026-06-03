## Why

Владелец видит активность клуба только по сырым `audit_log`, ops-алертам
и логам. Нет одного читаемого Telegram-потока: кто зашёл/вышел из
клубного чата, подписался/отписался от клубного канала, получил/потерял
доступ — и, главное, *почему* (Boosty/Tribute/manual) и *как* (бот выдал
линк, админ добавил, выдано командой). Эта инфа уже есть в системе, но не
доходит до человека в понятном виде.

Этот change — мердж двух параллельных вариантов. База — `-codex`
(типизированный контракт событий, эмитящийся в точке доменного решения,
где доступен полный контекст). Из `-claude` взяты продуктовая форма и два
инварианта надёжности (см. ниже). Среднего арифметического нет: контракт
codex + product shape claude.

## What Changes

- Новый отдельный `EVENT_LOG_CHAT_ID` — Telegram-группа, где состоят бот
  и админ. Поток событий отделён от `ADMIN_LOG_CHAT_ID` (ops-алерты) и
  никогда не fallback'ается в owner DM или alert-чат.
- Событие — **типизированный `OperatorEvent`**, а не парсинг
  `audit_log.detail`. Эмитится в точке workflow, где известен контекст:
  live `AccessDecision.Reasons`, admission method, actor, resource,
  invite mode. Доставка — durable `send_dm` с `payload_json.chat_id`
  (новый тип действия и миграция не нужны), атомарно в одной транзакции
  с доменным изменением и audit-строкой.
- Набор событий первого среза = названные владельцем + смежный цикл,
  нужный чтобы их объяснить:
  - членство: `club_chat_joined` / `club_chat_left`,
    `club_channel_subscribed` / `club_channel_unsubscribed` (метка по
    ресурсу: чат → зашёл/вышел, канал → подписался/отписался);
  - способ входа: `bot_link` / `external` (+ безопасный actor, если
    Telegram отдал `from`) / `admin` / `provider` / `job`;
  - право доступа: `access_granted` (только на реальном переходе
    non-active→active, с перечислением *всех* активных оснований),
    `access_loss_scheduled` (старт грейса) и `access_lost` (фактический
    отзыв) как два события, `access_kept` (подписка вернулась до
    отзыва);
  - источник: `source_subscription_activated` / `_expired`;
  - ручное: `manual_grant` / `manual_revoke` / `manual_ban` /
    `manual_unban`;
  - `banned_join_attempt` — попытка входа забаненного.
- Разделение «право» и «членство»: access-событие описывает право,
  membership-событие — фактическое нахождение в конкретном ресурсе.
  `unknown` от источника не порождает событий потери. Hard-ban в loss-
  событии указывается как перекрывающая причина.
- Privacy: в фид не попадают полные invite URL, raw payload, секреты,
  email, внутренние chat ID и idempotency-ключи; динамика экранируется
  HTML-renderer'ом; redaction — не пост-обработкой готового HTML.
- Фид **forward-only**: события эмитятся в момент workflow, бэкфилл
  истории не делается; восстановление — только через уже поставленные в
  outbox durable-действия.

### Изменения относительно `-codex` (правки из `-claude`)

- Capability названа `event-log`, а не `operator-event-log`.
- Добавлен kind `banned_join_attempt` (в codex был потерян).
- **Health-check лог-чата — НЕ boot-блокер.** Сбой проверки на старте —
  non-fatal warning; бот стартует и обслуживает апдейты независимо от
  доступности фида. Право бота писать в группу мониторит существующий
  chat-health и поднимает `bot_rights_lost`-алерт. Observability не
  имеет права ронять ядро контроля доступа.
- Явный инвариант: **построение** события (рендер/шаблон) не может
  уронить доменную операцию — ошибка логируется, событие пропускается, и
  grant/revoke/ban/audit не откатываются. Постановка собранного события
  атомарна с доменным изменением (один INSERT в той же транзакции); сбой
  самого INSERT — это сбой БД и подчиняется обычным правилам транзакции.
- Сохранена компактность tasks/design там, где это не режет требования.

### Правки по ревью (round 2)

- **Enforcer 403 для group-таргета** (`outbox-enforcer` delta): без этого
  фид silently «доставлялся» и метил случайного субъекта DM-blocked, а
  `outbox_action_dead` не поднимался. Заодно фикс латентного бага
  alert-доставки в `ADMIN_LOG_CHAT_ID`.
- **chat-health delta** для `EVENT_LOG_CHAT_ID`: baseline знал только
  четыре чата; добавлен мониторинг по праву постинга (не админству).
- **`access_granted` — одна семантика**: переход eligibility, единожды на
  эпизод, dedupe между engine и admission (была двусмысленность).
- **atomic vs best-effort разведены**: feed-build ошибки глотаются, а
  persistence INSERT подчиняется правилам транзакции.
- **idempotency**: source-stable event-time допустим, process wall-clock
  — нет.
- **`banned_join_attempt`** не утверждает «kept out» — «removal enqueued».
- **Источники доступа на событиях входа** (запрос владельца): join/
  subscribe несут активные основания (boosty/tribute/manual), чтобы было
  видно *почему* вошедший имеет доступ, а не только *как* вошёл.

## Capabilities

### New Capabilities

- `event-log`: контракт операторских событий — типы, типизированный
  контекст, выбор безопасных причин, durable-доставка в `EVENT_LOG_CHAT_ID`,
  forward-only и инвариант «фид не валит доменную операцию».

### Modified Capabilities

- `config`: `EVENT_LOG_CHAT_ID` (negative `int64`), required для этой
  фичи, distinct от `ADMIN_LOG_CHAT_ID` и четырёх source/club чатов.
- `status-core`: subscription-события эмитят `source_*`; engine сравнивает
  persisted-decision до/после и эмитит `access_granted` только на реальном
  переходе, делегируя потерю revocation-флоу; `unknown` не порождает
  потерю.
- `grant-access`: `access_granted` = переход eligibility, единожды на
  эпизод, dedupe между engine и admission; membership-события несут
  admission method, actor и **активные источники доступа** входящего
  пользователя.
- `access-revocation`: `access_lost` только при фактическом отзыве,
  `access_loss_scheduled` на грейсе, `access_kept` на отмене; unsafe/
  protected/notify_only не дают ложной потери.
- `bot-commands`: `/grant` `/revoke` `/ban` `/unban` эмитят manual-события
  с идемпотентностью по confirm-callback.
- `bot-message-ux`: owner-facing шаблоны событий в `messages`, HTML-
  renderer, leak-тесты.
- `telegram-transport`: routing `chat_member` сохраняет actor (`from`) и
  invite-контекст; raw invite URL — только для invite-резолвинга.
- `runtime`: writer собирается до старта транспорта, прокидывается в
  route/engine/admin; доставка через Enforcer (retry + `outbox_action_dead`
  alert); health-чата — non-fatal + chat-health alert, без fallback.
- `outbox-enforcer`: `send_dm` с явным `payload.chat_id` group-таргетом
  (`EVENT_LOG_CHAT_ID`/`ADMIN_LOG_CHAT_ID`) при `403` НЕ метит субъекта
  `dm_state='blocked'` и НЕ считается доставленным — идёт в dead +
  `outbox_action_dead` alert. Чинит и латентный баг alert-доставки.
- `chat-health`: мониторинг `EVENT_LOG_CHAT_ID` по способности **постить**
  (member с правом отправки достаточно, админство не требуется),
  `meta.health.event_log`, severity `error`, non-fatal.

## Impact

- Packages: `internal/operatorlog` (new), `messages`, `store`, `engine`,
  `admission`, `bot`, `telegram/router`, `telegram/health`,
  `cmd/gatekeeper`, `config`.
- Миграции нет: доставка использует существующий `access_actions
  .action_type='send_dm'` с `payload_json.chat_id`, как доставка алертов.
- `audit_log` остаётся append-only журналом фактов; event log — отдельный
  owner-facing Telegram sink поверх typed-контекста workflow.
- Tests: durable enqueue, idempotency/dedupe, privacy (no URL/chat ID/
  payload leaks), разделение `EVENT_LOG_CHAT_ID` и `ADMIN_LOG_CHAT_ID`,
  grace two-event, unknown/protected suppression, non-fatal feed health.
- Вне объёма: новая таблица событий, фильтры/дайджест на лету, replay/
  export из `audit_log`, новые сообщения пользователю.
