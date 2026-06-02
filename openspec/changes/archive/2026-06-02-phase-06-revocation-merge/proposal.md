## Why

После выдачи доступа Gatekeeper всё ещё не закрывает основной
жизненный цикл: пользователь, потерявший подписку, остаётся в клубных
ресурсах до ручного вмешательства. Эта фаза добавляет безопасный отзыв
доступа, грейс-период, фоновую сверку и owner-команды, чтобы продукт
стал управляемым в режиме observation.

## What Changes

- Вводится автоматический отзыв доступа при достоверном `inactive`:
  `grace`, `immediate` и `notify_only` режимы `EXPIRY_MODE`,
  `pending_revocations`, предупреждение пользователю и soft-kick через
  durable outbox.
- `revokeNow` получает финальную перепроверку `effectiveStatus`, чтобы
  не удалить пользователя, если подписка вернулась между warning и
  due-revoke.
- Отзыв затрагивает только grants, выданные ботом
  (`admitted_by='bot'`), и не трогает внешних участников,
  creator/admin клубных ресурсов и пользователей с `unknown` статусом.
- Добавляется Reconciler: исполнение due revocations, постановка
  проверок членства, health-сверка четырёх чатов, контроль shared
  invite links, обслуживание персональных ссылок и фиксация
  `meta.reconcile.last_run_at`.
- Добавляется cleanup ticker для retention старых inbox/outbox/audit
  данных, истечения персональных ссылок и WAL checkpoint.
- `admin_alerts` становятся push-доставляемыми: при создании тревога
  ставит durable `send_dm` владельцу или сообщение в
  `ADMIN_LOG_CHAT_ID`.
- Добавляются owner-команды `/grant`, `/revoke`, `/ban`, `/unban` и
  `/sync` с inline-подтверждением опасных действий.

## Capabilities

### New Capabilities

- `access-revocation`: автоматический и ручной отзыв доступа,
  pending revocations, soft-kick, hard-ban и safety-инварианты
  повторной проверки.
- `reconciliation`: фоновая сверка due revocations, членства, health,
  invite links и периодическая cleanup-обвязка.

### Modified Capabilities

- `status-core`: `recomputeAccess` больше не откладывает `inactive`,
  а планирует или исполняет отзыв по `EXPIRY_MODE`.
- `bot-commands`: добавляются owner-команды управления доступом,
  подтверждения опасных действий и revocation/admin-alert тексты.
- `storage`: расширяются repository-операции для
  `pending_revocations`, manual/whitelist/ban flows, cleanup и
  durable-доставки `admin_alerts`.
- `config`: `ADMIN_LOG_CHAT_ID` фиксируется как опциональный
  отрицательный Telegram chat id для доставки operator alerts.
- `runtime`: startup запускает Reconciler, делает один немедленный
  проход до poller loop и supervises новые фоновые tickers.
- `outbox-enforcer`: `verify_member` получает доменную семантику
  сверки членства, а `hard_ban`/`unban` используются admin flows.

## Impact

- Основные зоны кода: `internal/engine`, `internal/reconcile`,
  `internal/store`, `internal/notify`, `internal/bot`,
  `internal/messages`, `internal/enforcer`, `cmd/gatekeeper`.
- Используется существующая схема БД: `pending_revocations`,
  `access_grants`, `access_actions`, `admin_alerts`, `whitelist`,
  `subscriptions`, `audit_log`, `meta`.
- Миграции и новые внешние зависимости не требуются.
- Поведение зависит от уже объявленных config keys:
  `EXPIRY_MODE`, `GRACE_PERIOD`, `RECONCILE_INTERVAL`,
  `CLEANUP_INTERVAL`, `RAW_RETENTION`, `AUDIT_RETENTION` и
  `ADMIN_LOG_CHAT_ID` при его наличии.
