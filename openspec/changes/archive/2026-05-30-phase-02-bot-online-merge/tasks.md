## 1. Telegram client (`telegram-transport`)

- [x] 1.1 Add deps `github.com/go-telegram/bot` и
  `golang.org/x/sync/errgroup` (актуальные версии, Go 1.26)
- [x] 1.2 `internal/telegram/client.go`: конкретный `*Client` над
  `go-telegram/bot` с методами `getMe`, `getChat`, `getChatMember`,
  `sendMessage`, `setMyCommands`; собственных интерфейсов не объявляет
- [x] 1.3 Нормализация ошибок Telegram: `429` (`retry_after`), `403`,
  ожидаемые no-op, постоянные ошибки прав — различимые категории

## 2. Durable inbox and store (`telegram-transport`, `storage`)

- [x] 2.1 Узкий querier-интерфейс (исполнитель для `*sql.DB` и
  `*sql.Tx`); сделать repo-методы обработчиков tx-aware (инвариант I1)
- [x] 2.2 `internal/store/updates.go`: `TelegramUpdates.InsertBatch`
  (батч pending + `payload_json` + advance offset одной tx),
  `ListPending` по `update_id`, `MarkTerminal`
  (`processed`/`ignored`/`failed`)
- [x] 2.3 `internal/store/meta.go`: `GetUpdateOffset`/`SetUpdateOffset`
  и `SetHealth` (ключи `update_offset`, `health.*`)
- [x] 2.4 `internal/store/users.go`: `Users.SetDMState` (узкое
  обновление `dm_state`/`last_seen_at`); `Audit.Append`,
  `Alerts.Create`, пригодные к работе внутри tx обработчика
- [x] 2.5 `resolved_offset = max(meta.update_offset,
  MAX(update_id)+1)` при старте

## 3. Poller and router (`telegram-transport`)

- [x] 3.1 `internal/telegram/poller.go`: цикл `getUpdates` с явным
  `allowed_updates`; стадия `tx1` (persist батча + advance offset),
  стадия `tx2` (обработка строки + терминальный статус), `tx3`
  (`failed` + `admin_alert`)
- [x] 3.2 Recovery на старте: досканировать `pending` в порядке
  `update_id` до входа в цикл
- [x] 3.3 `internal/telegram/router.go`: маршрутизация по типу и
  `chat.id`; реально — `message` (личка), `/here` (группа),
  `my_chat_member`; `chat_member`/`chat_join_request` → `ignored`
- [x] 3.4 Обеспечить инварианты I1 (атомарность terminal status) и I2
  (нет Telegram-вызовов внутри `tx2`)

## 4. Messages and notify (`bot-commands`)

- [x] 4.1 `internal/messages/messages.go`: RU-тексты (минимум —
  приветствие, `/help`, `MSG_NO_SUB`), без inline-литералов в коде
- [x] 4.2 `internal/notify/notify.go`: формирование и отправка DM через
  узкий `messageSender`, учёт `users.dm_state`; `403 → blocked` без
  ретрая

## 5. Bot commands (`bot-commands`)

- [x] 5.1 `internal/bot/user.go`: `/start` (`ensureUser`,
  `dm_state=open`, приветствие+инструкция, без проверки подписки);
  не-командный текст в личке → как `/start`
- [x] 5.2 `/help` (текст из `messages`)
- [x] 5.3 `/here` (только `OWNER_TG_IDS`, ответ `chat.id` + тип; иначе
  игнор)
- [x] 5.4 `setMyCommands`: пользовательские глобально, админские scoped
  на `OWNER_TG_IDS`

## 6. Chat health and discovery (`chat-health`)

- [x] 6.1 Startup health: для каждого из 4 чатов `getChat` +
  `getChatMember(bot)` → `meta.health.*`; проблемы → лог +
  `admin_alert` + DM владельцу; не падать (клуб `critical`, источник
  `error`)
- [x] 6.2 `my_chat_member` в личке → `users.dm_state` (`open`/`blocked`)
- [x] 6.3 `my_chat_member` в известном чате → `meta.health.*`,
  `audit_log` и `admin_alert` при потере/восстановлении прав
- [x] 6.4 `my_chat_member` в незнакомом чате → лог `chat.id`/тип/
  название + DM владельцу (ссылки/токены в лог не писать)

## 7. Wiring and lifecycle (`runtime`)

- [x] 7.1 `cmd/gatekeeper/main.go`: создать `*Client`, задать
  `allowed_updates`, `getMe`, `setMyCommands`, прогнать chat-health
- [x] 7.2 Запустить поллер под `errgroup.WithContext`; штатная
  остановка (контекст → горутины → `db.Close()` последней)
- [x] 7.3 `getMe`-ошибка прерывает старт; health-проблемы деградируют,
  не прерывая старт
- [x] 7.4 `TELEGRAM_MODE=webhook` → fatal «not implemented», без тихого
  fallback в polling

## 8. Tests

- [x] 8.1 `client_test.go`: маппинг ошибок Telegram (`429`/`403`/no-op/
  постоянные права)
- [x] 8.2 `poller_test.go`: повторный `update_id` не обрабатывается
  дважды; recovery `pending` на старте
- [x] 8.3 store-тесты: атомарность `InsertBatch` (offset+pending в одной
  tx) и терминальный статус в одной tx с доменной записью
- [x] 8.4 bot-команды: `/start` (+ повтор, не-командный текст), `/help`,
  `/here` (владелец/не-владелец); notify `403→blocked` и skip-blocked
- [x] 8.5 chat-health: startup (healthy / нет прав / неверный токен),
  `my_chat_member` (dm_state, известный чат, незнакомый чат, ensureUser)
- [x] 8.6 runtime: в дефолтном режиме HTTP-listener не открыт;
  `TELEGRAM_MODE=webhook` → fatal
- [x] 8.7 `task test` зелёный; `task lint` без замечаний

## 9. Verification

- [x] 9.1 Бот стартует с тестовым токеном: «getMe ok», результаты
  health 4 чатов, `meta.health.*` заполнены
- [x] 9.2 Отзыв админ-прав в одном из 4 чатов → `admin_alert` + DM
  владельцу + обновлённый `meta.health.*`
- [x] 9.3 Добавление в новый чат → DM владельцу с `chat.id`/типом/
  названием
- [x] 9.4 `/here` в группе, `/start`+`/help` в личке отвечают; строка
  `users` с `dm_state='open'`; блокировка в личке → `dm_state='blocked'`
- [x] 9.5 Перезапуск: обновления из простоя (< 24 ч) не обрабатываются
  дважды (проверить `telegram_updates` по `update_id`)
