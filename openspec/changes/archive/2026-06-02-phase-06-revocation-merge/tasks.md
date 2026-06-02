## 1. Store, config и сообщения

- [x] 1.1 Зафиксировать `ADMIN_LOG_CHAT_ID` в config validation:
  optional negative chat id, fallback на owner DM, collision check с
  source/club chat ids
- [x] 1.2 Добавить `store.Revocations`: create-if-absent, get, delete,
  list due, mark notified, RFC3339 UTC и unit-тесты идемпотентности
- [x] 1.3 Расширить store для whitelist/manual access:
  add/remove/check whitelist, upsert/expire `manual` subscription,
  stub user по числовому `tg_id`
- [x] 1.4 Расширить store для `users.banned`, eligible grants на отзыв,
  alert dedupe/resolve и cleanup операций retention
- [x] 1.5 Добавить messages для `MSG_EXPIRY_WARNING`,
  `MSG_EXPIRED_NOTICE`, `MSG_REVOKED`, admin confirmations,
  `/sync` summary и operator alerts
- [x] 1.6 Покрыть config/store/messages unit-тестами, включая отсутствие
  inline user-facing текстов вне пакета `messages`

## 2. Access revocation в engine

- [x] 2.1 Реализовать `inactive` ветку `recomputeAccess` для
  `grace`, `immediate` и `notify_only` без Telegram-вызовов внутри tx
- [x] 2.2 Реализовать отмену pending revocation при `active`:
  delete, `audit_log(revocation_cancelled)`, durable
  `MSG_ACCESS_KEPT`
- [x] 2.3 Реализовать `revokeNow` с финальной live-перепроверкой
  `effectiveStatus` и безопасным поведением для `active`/`unknown`
- [x] 2.4 Ограничить автоматический отзыв grants со state
  `joined`/`pending` и `admitted_by='bot'`; `external` не трогать
- [x] 2.5 Добавить `isProtected(resource, tgID)` через `getChatMember`;
  creator/admin не кикать, поднимать
  `protected_admin_lost_subscription`
- [x] 2.6 Реализовать hard-ban path: `users.banned=1`,
  отмена pending revocation, `hard_ban` outbox без автоматического
  unban
- [x] 2.7 Добавить unit-тесты `recomputeAccess` для
  `grace`/`immediate`/`notify_only`, unknown no-op, active source
  priority и hard-ban priority
- [x] 2.8 Добавить отдельные safety-тесты, по кейсу на инвариант:
  active между warning и due-revoke (pending удалён, `soft_kick` не
  ставится); active в одном источнике при inactive в другом (отзыв не
  создаётся); `unknown` на планировании и финальной проверке (отзыв не
  создаётся); protected creator/administrator не кикается и поднимает
  `protected_admin_lost_subscription`; `external` grant не кикается ни
  при событийном пересчёте, ни при reconciliation

## 3. Durable alerts и Enforcer actions

- [x] 3.1 Сделать `Alerts.Create` единой точкой durable alert delivery:
  owner DM или `ADMIN_LOG_CHAT_ID` через outbox с idempotency key
- [x] 3.2 Обеспечить alert dedupe, чтобы повтор одной открытой тревоги
  не создавал duplicate delivery
- [x] 3.3 Расширить Enforcer `verify_member`: source observations через
  normal domain paths, club observations без перетирания `revoked`
  grants, `unknown` без отзыва
- [x] 3.4 Проверить `hard_ban` и `unban` action execution:
  no auto-unban для hard-ban, `only_if_banned=true` для unban,
  protected admin no-op
- [x] 3.5 Добавить Enforcer/alert тесты для delivery, dedupe,
  verify-member inactive/unknown и hard-ban/unban idempotency

## 4. Reconciler и cleanup

- [x] 4.1 Создать `internal/reconcile` с `RunOnce`, periodic loop,
  зависимостями на store, outbox, engine, chat-health и invite service
- [x] 4.2 В `RunOnce` исполнять due `pending_revocations` только через
  `revokeNow`
- [x] 4.3 Формировать candidates из active subscriptions,
  bot-admitted joined grants и whitelist; ставить idempotent
  `verify_member` actions
- [x] 4.4 Подключить health-сверку четырёх чатов с записью
  `meta.health.*` и durable alerts без падения процесса
- [x] 4.5 Поддержать invite-link integrity: ensure shared links,
  expire personal/direct links, enqueue `revoke_invite` при
  необходимости
- [x] 4.6 Обновлять `meta.reconcile.last_run_at` и audit summary с
  counters после successful pass
- [x] 4.7 Реализовать cleanup ticker: raw retention, done outbox
  retention, resolved alerts, audit retention, invite expiry,
  WAL checkpoint; failed/dead forensic rows не удалять
- [x] 4.8 Добавить `reconciler_test.go` для due revocations,
  candidates, health degradation, missing shared link, cleanup
  retention и failed/dead preservation

## 5. Owner commands и callbacks

- [x] 5.1 Добавить owner-only handlers `/grant`, `/revoke`, `/ban`,
  `/unban`, `/sync`; non-owner обращения игнорировать без раскрытия
  данных
- [x] 5.2 Реализовать compact confirmation flow для `/grant`,
  `/revoke`, `/ban`, `/unban`, `/sync`: action id, expiry, owner
  binding, confirm/cancel callbacks
- [x] 5.3 Реализовать `/grant`: whitelist без срока, manual
  subscription со сроком, stub user по числовому `tg_id`,
  local-only username lookup
- [x] 5.4 Реализовать `/revoke`: снять whitelist/manual access,
  записать audit, вызвать `recomputeAccess`
- [x] 5.5 Реализовать `/ban` и `/unban`: banned flag, hard-ban/unban
  outbox actions, audit, идемпотентные повторные callbacks
- [x] 5.6 Реализовать `/sync [tg_id]`: после confirm callback
  single-user reconciliation или full pass, durable owner result
- [x] 5.7 Обновить `setMyCommands` admin scope и тесты меню команд
- [x] 5.8 Добавить `admin_test.go` для grant/revoke/ban/unban/sync,
  confirmations, expired callbacks, duplicate confirms и access
  effects

## 6. Runtime wiring

- [x] 6.1 Собрать Reconciler и cleanup service в `cmd/gatekeeper/main.go`
  после Outbox/Invite/Enforcer dependencies
- [x] 6.2 Запустить Enforcer workers до initial Reconciler pass, чтобы
  pass мог безопасно ставить durable actions
- [x] 6.3 Выполнить один Reconciler pass до poller loop и запускать
  periodic Reconciler/cleanup под общим `errgroup.WithContext`
- [x] 6.4 Обеспечить shutdown order: cancel context, дождаться
  Enforcer/Reconciler/cleanup/poller, закрыть DB последней
- [x] 6.5 Добавить runtime тесты startup order, initial reconcile before
  poller, recoverable reconcile alerts и graceful shutdown

## 7. Верификация

- [x] 7.1 Запустить `task test`
- [x] 7.2 Запустить `task lint`
- [x] 7.3 Запустить `task smoke`, если существующий smoke не требует
  реальных Telegram secrets (`task smoke` отсутствует в Taskfile)
- [x] 7.4 Запустить `openspec validate phase-06-revocation-merge
  --strict`
- [x] 7.5 Запустить `openspec status --change
  phase-06-revocation-merge`
- [x] 7.6 Провести локальный fake-client scenario: source
  `member -> left` -> grace warning -> due reconcile -> soft-kick из
  chat и channel
- [x] 7.7 Провести локальный fake-client scenario: подписка возвращается
  в grace -> pending revocation удалён -> `MSG_ACCESS_KEPT` -> kick не
  ставится
- [x] 7.8 Проверить вручную admin flows: `/grant`, `/revoke`, `/ban`,
  `/unban`, `/sync`, protected admin alert и durable alert delivery
