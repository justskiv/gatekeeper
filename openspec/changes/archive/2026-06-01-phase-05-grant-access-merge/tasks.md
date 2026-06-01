## 1. Store и invite resolution

- [x] 1.1 Добавить в `store.Invites` lookup активной ссылки по
  `invite_link_hash` и resource без логирования полного URL
- [x] 1.2 Расширить `invite.Store`/`invite.Service` операцией
  разрешения join-request ссылки для shared, personal и missing-link
  fallback
- [x] 1.3 Обеспечить marking `used`, `used_by_other` и `attempted_by`
  для personal-ссылок при join-request
- [x] 1.4 Добавить store/invite тесты для hash lookup, personal misuse,
  missing `invite_link` fallback и отсутствия полного URL в логах

## 2. Messages, callbacks и rate limit

- [x] 2.1 Добавить admission-тексты в `internal/messages`:
  `MSG_ACTIVE`, `MSG_INVITE_SOON`, `MSG_GRANTED`, `MSG_TRY_LATER`,
  `MSG_BANNED`, `MSG_ALREADY_IN` и direct-вариант active-сообщения
- [x] 2.2 Добавить callback handler для кнопки "Проверить ещё раз",
  маршрутизирующий в тот же flow, что и `/start`
- [x] 2.3 Реализовать per-user rate limit 30 секунд для `/start`,
  некомандного DM и retry callback без вызова источников и invite
  actions при throttling
- [x] 2.4 Покрыть messages/callback/rate-limit unit-тестами

## 3. Access request flow

- [x] 3.1 Добавить admission handler/ports для live status,
  `recomputeAccess`, grants, invites, outbox, audit и alerts
- [x] 3.2 Перевести `/start` и некомандный DM на grant-access flow:
  `ensureUser`, `dm_state=open`, hard-ban, live status вне `tx2` и
  unknown fallback на свежую active-подписку из БД
- [x] 3.3 Для `active` реализовать idempotent pending grants по
  недостающим resources и delivery через shared links или `send_invite`
  в зависимости от `INVITE_MODE`
- [x] 3.4 Для `inactive`, `unknown` без fallback и hard-ban реализовать
  durable DM, audit и alerts без создания grants
- [x] 3.5 Подтвердить, что `recomputeAccess` остаётся без
  `INACTIVE`-ветки отзыва, soft-kick и revocation actions до Фазы 06
- [x] 3.6 Добавить интеграционные тесты `/start`: active shared links,
  inactive `MSG_NO_SUB`, unknown fallback, unknown retry-later, banned и
  повторный идемпотентный запрос

## 4. Join-request admission

- [x] 4.1 Расширить parsing/router для `chat_join_request` payload,
  включая managed resource, `user_chat_id` и дату/идентификатор заявки
- [x] 4.2 Реализовать join-request handler: resolve invite, hard-ban,
  live status вне `tx2` с 1-2 ретраями при `unknown`,
  approve/decline через outbox и audit
- [x] 4.3 Реализовать personal misuse path:
  `used_by_other`, `attempted_by`, `decline_join` и audit
- [x] 4.4 Реализовать missing `invite_link` fallback для shared и
  personal modes без отклонения active-пользователя только из-за пустого
  поля
- [x] 4.5 Сделать `approve_join` idempotency key из resource, tg_id и
  даты/идентификатора заявки (§13.2), чтобы повторная заявка после
  выхода была новым действием
- [x] 4.6 Добавить тесты join-request: active approved, inactive
  declined, persistent unknown declined, personal misuse declined,
  missing link fallback, repeated request after leave и DM через
  `user_chat_id`

## 5. Club membership tracking

- [x] 5.1 Расширить router для `chat_member` в club chat и club channel
  отдельно от source membership events
- [x] 5.2 Реализовать membership handler: `joined`/`left` grants,
  `admitted_by='bot'` для join-request/direct evidence и
  `admitted_by='external'` иначе
- [x] 5.3 Для external joins создавать audit и
  `admin_alert(kind='external_join')` без постановки `soft_kick`
- [x] 5.4 Для direct joins перепроверять live status вне `tx2` и
  ставить `soft_kick`, если статус не `active`
- [x] 5.5 При выходе переводить grant в `left` через `updated_at`, но
  не перетирать уже `revoked` grant
- [x] 5.6 Добавить тесты club membership: bot-admitted join, external
  join alert/no kick, direct compensation, member left и revoked
  priority over left

## 6. Wiring, runtime и boundaries

- [x] 6.1 Проверить конфигурацию на пересечение source chat ids и club
  resource ids до запуска poller; ambiguous config должна падать на
  startup, а не решаться роутером
- [x] 6.2 Подключить admission dependencies в `cmd/gatekeeper/main.go`
  без новых глобальных интерфейсов в `telegram`
- [x] 6.3 Убедиться, что все Telegram side effects из новых handler'ов
  идут через `access_actions` внутри `tx2`
- [x] 6.4 Убедиться, что живой `effectiveStatus` нигде не выполняется в
  рамках `tx2`
- [x] 6.5 Обновить runtime/router тесты на маршрутизацию
  `chat_join_request`, club `chat_member` и ambiguous chat id

## 7. Верификация

- [x] 7.1 Запустить `task test`
- [x] 7.2 Запустить `task lint`
- [x] 7.3 Быстрым grep проверить, что specs/tasks не ссылаются на
  несуществующие колонки схемы
- [x] 7.4 Запустить `openspec validate phase-05-grant-access-merge
  --strict`
- [x] 7.5 Запустить `openspec status --change
  phase-05-grant-access-merge`
- [x] 7.6 Провести локальный smoke для shared mode: active account
  `/start` -> links -> join-request approved -> grants `joined`
- [x] 7.7 Провести локальный smoke для неактивного аккаунта и
  пересланной ссылки: `/start` даёт `MSG_NO_SUB`, join-request
  отклоняется
