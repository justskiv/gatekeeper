## Context

Gatekeeper уже имеет durable inbox и handler-инварианты: входящий update
сначала сохраняется, затем обработчик меняет домен и terminal status в
`tx2`, не выполняя Telegram-вызовов внутри транзакции. Схема v1 уже
содержит `access_actions` и `invite_links`, но пока нет repository,
воркеров и сервисов, которые наполняют эти таблицы и разгружают очередь.

Цель фазы — симметрично закрыть исходящую сторону: доменный код пишет
action в БД вместе с решением, Enforcer исполняет после коммита, а
invite-ссылки получают отдельный lifecycle. Основные ограничения:
SQLite работает одним соединением, Bot API требует троттлинга,
`direct` не должен стать штатным режимом, а archive OpenSpec должен
обновить уже существующие контракты runtime/storage/transport/commands.

## Goals / Non-Goals

**Goals:**

- Durable outbox для доменных исходящих Telegram-действий.
- Enforcer как единая точка исполнения, ретраев, no-op классификации и
  rate limiting.
- Отдельный `invite-links` contract для shared, personal и direct
  режимов.
- Startup readiness: в `shared_join_request` у club chat и club channel
  есть активные join-request ссылки до запуска poller loop.
- Полный перевод текущих bot-command DM replies из update handler'ов на
  `send_dm` enqueue внутри handler-транзакции.

**Non-Goals:**

- Выдача доступа по `/start` и `chat_join_request` approval. Это Фаза
  05, но она будет использовать action types этой фазы.
- Реальный отзыв доступа по `inactive`, включая grace/revokeNow. Это
  Фаза 06.
- Reconciler, cleanup, HTTP endpoints, metrics и webhook-production.
- Новые таблицы или изменение миграции `0001_init.sql`.

## Decisions

### D1. Решение и outbox action коммитятся вместе

`access_actions` — часть durable состояния, а не техническая очередь в
памяти. Любой handler, который после доменного решения должен выполнить
Telegram side effect, пишет action в той же `tx2`, где меняет доменные
таблицы, `audit_log` и terminal status inbox. До коммита не существует
ни решение, ни действие; после коммита оба доступны для recovery.

Альтернатива — отправлять Telegram-вызов после коммита — отвергнута:
крэш между коммитом и вызовом теряет действие, а повтор входящего
update уже не гарантирован.

### D2. Lease отделяет выбор action от сетевого исполнения

Воркер короткой транзакцией выбирает `queued/run_after<=now` или
зависшую `running/locked_until<now` строку, переводит её в `running` и
выставляет новый `locked_until`. Telegram-вызов выполняется только
после коммита lease-транзакции, финальный статус записывается отдельной
короткой транзакцией.

Это сохраняет единственное SQLite-соединение доступным для poller и
других writers. Если процесс падает после lease, следующий воркер
подхватывает строку после истечения `locked_until`. Эффекты остаются
at-least-once, поэтому каждый executor проектируется идемпотентным.

### D3. Enforcer владеет retry, throttling и no-op классификацией

`internal/enforcer` объявляет свои узкие consumer-интерфейсы для
Telegram-вызовов, OutboxStore, InviteService, Users/Audit/Alerts store.
Конкретный `telegram.Client` остаётся одним типом без экспортированных
интерфейсов; зависимости разворачиваются на стороне потребителя.

Enforcer одинаково обрабатывает action types:
`ensure_invite`, `send_invite`, `approve_join`, `decline_join`,
`soft_kick`, `hard_ban`, `unban`, `send_dm`, `verify_member`,
`revoke_invite`. Ожидаемые ошибки вроде отсутствующей заявки, уже
отозванной ссылки или уже-не-участника считаются success/no-op и
логируются как warning. `soft_kick` дополнительно проверяет, что цель
не creator/admin, затем выполняет `banChatMember` и
`unbanChatMember(only_if_banned=true)`.

### D4. `invite-links` отделён от outbox executor'ов

Invite service управляет созданием, поиском, переиспользованием,
истечением и safe logging invite-ссылок. Outbox executor вызывает этот
сервис, но не размазывает правила режимов по worker loop.

- `shared_join_request`: одна активная ссылка на resource,
  `tg_id=NULL`, `creates_join_request=true`, без TTL по умолчанию.
- `personal_join_request`: одна активная ссылка на пользователя и
  resource, `creates_join_request=true`, TTL из `INVITE_TTL`, nonce в
  имени.
- `direct`: аварийный персональный режим,
  `creates_join_request=false`, `member_limit=1`, TTL не больше одного
  часа.

Полный invite URL не пишется в технические логи. Для correlation
используется `invite_link_hash`, а сама ссылка хранится в БД и
отправляется пользователю только через outbox.

### D5. Startup: сначала Enforcer, затем ожидание shared-ссылок

`ensure_invite` требует реального Telegram `createChatInviteLink`,
поэтому в `shared_join_request` readiness невозможна без работающего
Enforcer. Runtime собирает outbox/invite/enforcer, запускает workers
под `errgroup`, ставит два `ensure_invite` action для `chat` и
`channel`, затем ограниченно ждёт активные строки `invite_links` с
`creates_join_request=true`. Только после этого запускается poller loop.

Если Telegram недоступен или у бота нет прав создать ссылки, startup
падает. Это осознанная readiness-граница MVP: без shared-ссылок Фаза 05
не сможет штатно выдавать доступ.

### D6. Текущие командные DM мигрируют полностью, admission DM ждёт Фазу 05

В этой фазе не вводится user admission flow, но уже существующие
личные ответы update handler'ов (`/start`, `/help`, `/status`,
`/whois` и аналогичные command replies) должны ставить `send_dm` action
в `tx2`, а не вызывать Telegram напрямую после коммита. Это убирает
best-effort разрыв для текущих команд и сохраняет I1/I2.

Сообщения, которые появятся вместе с выдачей доступа (`send_invite`,
`MSG_INVITE_SOON`, admission-specific replies), остаются задачей Фазы
05, но используют action types и инфраструктуру этой фазы. Startup или
ops-пути без handler-транзакции должны оставаться явно best-effort или
получить отдельную durable постановку в будущей фазе.

### D7. `direct` остаётся аварийным режимом

`direct` допустим только при уже валидной конфигурации
`INVITE_MODE=direct` и `ALLOW_DIRECT_INVITES=true`; `INVITE_TTL` для
direct не может превышать один час. При успешном старте в direct
runtime создаёт `admin_alert(kind='invite_mode_degraded')`. Это
предотвращает тихое превращение direct в штатный путь без join-request
шлагбаума.

## Risks / Trade-offs

- **R1. Lease too short** → действие может выполниться дважды при
  долгом Telegram-вызове. Митигация: executor'ы идемпотентны,
  lease выбирается выше нормального timeout, idempotency key не даёт
  создать дубль.
- **R2. Startup зависит от Telegram availability** → бот может не
  стартовать, если невозможно создать shared-ссылки. Митигация:
  это правильная readiness-граница для MVP; без ссылок выдача доступа
  неработоспособна.
- **R3. `direct` станет постоянной настройкой** → система потеряет
  join-request шлагбаум. Митигация: явный allow flag, TTL <= 1h и
  persistent warning при старте.
- **R4. Полные invite URLs утекут в логи** → ссылка может быть
  переслана вне контроля. Митигация: логировать только hash и служебное
  имя, полный URL хранить в `invite_links`.
- **R5. Единый Enforcer станет bottleneck** → массовые операции займут
  время. Митигация: несколько workers, общий rate limiter держит
  Telegram лимиты, backlog виден через `access_actions`.

## Migration Plan

1. Добавить domain-модели и store-репозитории для `access_actions` и
   `invite_links` поверх существующей схемы.
2. Добавить `invite.Service`, затем Enforcer executor'ы и rate limiter.
3. Расширить `telegram.Client` нужными Bot API методами.
4. Подключить Enforcer в runtime и startup `ensure_invite`.
5. Перевести текущие command DM replies на `send_dm` enqueue.
6. Проверить `task test`, `task lint` и `openspec validate
   phase-04-outbox-enforcer-merge --strict`.

Rollback простой: код фазы можно откатить без миграции БД. Уже
созданные строки `access_actions` и `invite_links` останутся в таблицах
и не будут исполняться старым бинарём.

## Open Questions

- Какой точный формат `idempotency_key` закреплять для каждого action
  type: только `type:tg_id:resource:marker` или JSON-derived hash для
  сложных payload?
- Нужен ли operator command для восстановления `dead` actions, или в v1
  достаточно ручного SQL reset после расследования?
- Нужен ли отдельный startup timeout для ожидания shared-ссылок, или
  достаточно общего контекста старта?
