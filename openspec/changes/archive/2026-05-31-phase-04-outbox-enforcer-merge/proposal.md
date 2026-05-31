## Why

После Фазы 03 бот умеет принимать входящие события и обновлять
доменное состояние, но исходящие Telegram-действия всё ещё не имеют
durable исполнения. Синхронные вызовы из handler'ов удерживали бы
`tx2`, блокировали poller и теряли действие при рестарте между
коммитом состояния и сетевым вызовом.

Эта фаза вводит исполнительный слой: durable outbox поверх
`access_actions`, Enforcer-воркеры с троттлингом и сервис
invite-ссылок. После неё фазы 05 и 06 смогут строить выдачу и отзыв
доступа на одном надёжном канале исходящих действий.

## What Changes

- Новая capability `outbox-enforcer`: repository над
  `access_actions`, идемпотентная постановка действий, lease/reclaim
  зависших `running`, worker loop, retry/backoff, обработка `429` по
  `retry_after`, dead-letter и `admin_alert`.
- Новая capability `invite-links`: сервис ссылок для
  `shared_join_request`, `personal_join_request` и аварийного
  `direct`; активная уникальность по режимам, safe logging только через
  `invite_link_hash`, предохранители `direct`.
- Enforcer становится единственной точкой доменных исходящих вызовов
  Telegram: `ensure_invite`, `send_invite`, `approve_join`,
  `decline_join`, `soft_kick`, `hard_ban`, `unban`, `send_dm`,
  `verify_member`, `revoke_invite`.
- Runtime запускает Enforcer под supervised `errgroup`; в штатном
  `shared_join_request` режиме startup ставит `ensure_invite` для
  клубного чата и канала и дожидается активных join-request ссылок до
  запуска poller loop.
- Текущие bot-command DM replies, которые формируются из Telegram
  update handler'ов, переводятся на durable `send_dm` action внутри
  handler-транзакции; admission-flow сообщения Фазы 05 будут
  использовать тот же action type, но сам flow остаётся вне этой фазы.

## Capabilities

### New Capabilities

- `outbox-enforcer`: durable очередь исходящих Telegram-действий,
  Enforcer-воркеры, rate limiting, retry/dead semantics,
  idempotency и lease/reclaim.
- `invite-links`: lifecycle invite-ссылок управляемых ресурсов,
  режимы `shared_join_request`, `personal_join_request`, `direct`,
  безопасное логирование и startup readiness для shared-ссылок.

### Modified Capabilities

- `storage`: repository-граница расширяется методами для
  `access_actions` и `invite_links`, включая идемпотентную вставку,
  лизинг, завершение, ретраи и выбор активных ссылок.
- `runtime`: порядок старта добавляет Enforcer, direct-warning и
  ожидание shared invite-ссылок до poller loop; supervised errgroup
  теперь управляет и poller, и Enforcer.
- `telegram-transport`: конкретный `telegram.Client` получает методы,
  нужные Enforcer'у; handler-инвариант I1 теперь включает
  `access_actions` inserts как durable side effects внутри `tx2`.
- `bot-commands`: DM delivery для update handler'ов уходит через
  durable `send_dm` action с сохранением `dm_state` semantics.

## Impact

- Новые пакеты: `internal/enforcer` и `internal/invite`.
- Новые repository-файлы: `internal/store/outbox.go` и
  `internal/store/invites.go`.
- Изменяются: `internal/telegram/client.go`, `cmd/gatekeeper/main.go`,
  notify/bot-command пути, которые отправляют личные сообщения из
  update handler'ов.
- Новых таблиц и миграций не требуется: используются уже существующие
  `access_actions`, `invite_links` и `admin_alerts`.
- Новых внешних зависимостей не планируется; используются уже
  загружаемые `INVITE_MODE`, `INVITE_TTL`, `ALLOW_DIRECT_INVITES` и
  `ENFORCER_WORKERS`.
- Вне области: пользовательский flow выдачи доступа через `/start` и
  обработка `chat_join_request` (Фаза 05), реальный отзыв доступа по
  `inactive` (Фаза 06), reconciliation, cleanup, HTTP endpoints,
  metrics и webhook-production режим.
