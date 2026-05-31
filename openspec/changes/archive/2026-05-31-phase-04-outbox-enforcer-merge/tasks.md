## 1. Store и domain-модели

- [x] 1.1 Добавить domain-типы для `AccessAction`, `ActionType` и
  `ActionStatus` по enum'ам таблицы `access_actions`
- [x] 1.2 Зафиксировать helper/конвенцию построения `idempotency_key`
  для каждого action type
- [x] 1.3 Реализовать `internal/store/outbox.go`: enqueue по
  `idempotency_key`, lease ready action, retry, done и dead transitions
- [x] 1.4 Реализовать `internal/store/invites.go`: lookup active
  shared/personal ссылок, сохранение, sent/used/revoked/expired/failed
  transitions
- [x] 1.5 Покрыть store-тестами idempotent enqueue, reclaim expired
  lease, retry metadata, active invite uniqueness и истечение personal
  links

## 2. Telegram client

- [x] 2.1 Расширить `internal/telegram/client.go` методами
  `CreateChatInviteLink`, `RevokeChatInviteLink`,
  `ApproveChatJoinRequest`, `DeclineChatJoinRequest`, `BanChatMember`
  и `UnbanChatMember`
- [x] 2.2 Поддержать параметры Bot API, нужные режимам ссылок:
  `creates_join_request`, `expire_date`, `member_limit`, `name`,
  `only_if_banned`
- [x] 2.3 Нормализовать Telegram-ошибки, нужные Enforcer'у: `429` с
  `retry_after`, закрытая личка, permanent rights и expected no-op
  категории
- [x] 2.4 Добавить unit-тесты клиента на сериализацию параметров и
  нормализацию ошибок через fake Bot API server

## 3. Invite-links service

- [x] 3.1 Создать `internal/invite/ports.go` с узкими интерфейсами
  `linkManager` и Store
- [x] 3.2 Реализовать shared mode: одна активная join-request ссылка на
  `chat` и одна на `channel`, idempotent reuse при повторном
  `ensure_invite`
- [x] 3.3 Реализовать personal mode: персональная join-request ссылка с
  TTL, nonce, безопасным именем и переиспользованием до истечения
- [x] 3.4 Реализовать direct mode: `creates_join_request=false`,
  `member_limit=1`, TTL не больше одного часа и отсутствие превращения
  режима в default path
- [x] 3.5 Запретить техническое логирование полного invite URL,
  используя `invite_link_hash` для correlation
- [x] 3.6 Добавить `invite`-тесты для shared idempotency, personal reuse,
  direct TTL guard и отсутствия полного URL в логируемых полях

## 4. Enforcer

- [x] 4.1 Создать `internal/enforcer/ports.go` с consumer-интерфейсами
  Telegram-вызовов, outbox store, invite service, users/audit/alerts
  store
- [x] 4.2 Реализовать dispatcher и payload decoding для всех action
  types: `ensure_invite`, `send_invite`, `approve_join`,
  `decline_join`, `soft_kick`, `hard_ban`, `unban`, `send_dm`,
  `verify_member`, `revoke_invite`
- [x] 4.3 Реализовать worker loop: lease, исполнение вне DB transaction,
  done, retry, dead и context cancellation
- [x] 4.4 Реализовать rate limiter и backoff: message per chat,
  общий потолок, `getChatMember` limit, `429 retry_after` и jitter
- [x] 4.5 Обработать expected no-op ошибки как успешные warning events
  без retry
- [x] 4.6 Реализовать `send_dm` поведение: `403` выставляет
  `dm_state='blocked'` и завершает action без retry
- [x] 4.7 Реализовать `soft_kick` как проверку creator/admin,
  `banChatMember` и `unbanChatMember(only_if_banned=true)`
- [x] 4.8 Создать `admin_alert(kind='outbox_action_dead')` при
  исчерпании `max_attempts`
- [x] 4.9 Добавить Enforcer-тесты на lease reclaim, `429`, `403`,
  no-op, creator/admin guard, soft-kick sequence, dead alert и
  отсутствие сетевого вызова внутри DB transaction

## 5. Runtime wiring

- [x] 5.1 Подключить outbox, invite service и Enforcer в
  `cmd/gatekeeper/main.go`
- [x] 5.2 Запустить Enforcer workers под тем же supervised
  `errgroup.WithContext`, что и poller
- [x] 5.3 При `INVITE_MODE=direct` создавать
  `admin_alert(kind='invite_mode_degraded')` на старте
- [x] 5.4 При `INVITE_MODE=shared_join_request` ставить два
  `ensure_invite` action и ждать активные ссылки для club chat и
  club channel до запуска poller loop
- [x] 5.5 Обновить runtime-тесты на startup order, shared invite
  readiness, direct warning и graceful shutdown Enforcer workers до
  `db.Close()`

## 6. Durable DM delivery

- [x] 6.1 Расширить notify слой интерфейсом enqueue `send_dm` и
  сохранением прежней проверки `dm_state='blocked'`
- [x] 6.2 Перевести текущие bot-command DM replies из update handler'ов
  на постановку `send_dm` в outbox внутри handler-транзакции вместо
  прямого `sendMessage`
- [x] 6.3 Явно оставить admission-specific DM сообщения до Фазы 05,
  но закрепить, что они используют уже введённый outbox канал
- [x] 6.4 Добавить bot/notify-тесты: enqueue вместо direct send,
  blocked-пользователь пропускается до enqueue, `403` закрывает DM
  через Enforcer

## 7. Верификация

- [x] 7.1 Запустить `task test`
- [x] 7.2 Запустить `task lint`
- [x] 7.3 Запустить `openspec validate phase-04-outbox-enforcer-merge
  --strict`
- [x] 7.4 Проверить локально `INVITE_MODE=shared_join_request`: в
  `invite_links` появляются active ссылки для chat и channel
- [x] 7.5 Проверить локально `INVITE_MODE=direct` без
  `ALLOW_DIRECT_INVITES=true`: процесс падает на конфигурации до
  открытия БД
- [x] 7.6 Проверить локально outbox: дубль `idempotency_key` не создаёт
  вторую строку, `send_dm` завершается `done`, `429` перепланирует
  `run_after`
