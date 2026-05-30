## Why

После Фазы 01 есть запускаемый бинарь с валидным конфигом и SQLite, но
он не общается с Telegram: ни одного входящего обновления, ни одной
команды, ни сигнала о том, что бота вообще добавили туда, куда нужно.
Это первая MVP-фаза, которая выводит бота в эфир.

Цель — поднять транспорт (long polling), научиться надёжно принимать и
не терять обновления, ответить на базовые команды и на старте
проверить, что бот — администратор во всех четырёх настроенных чатах.
Доменной логики доступа здесь ещё нет: `/start` лишь регистрирует
пользователя и показывает инструкцию, реальную проверку подписки
добавит Фаза 05.

## What Changes

- Long-polling транспорт: цикл `getUpdates` с полным явным
  `allowed_updates`, durable inbox `telegram_updates` со state machine
  `pending → processed | ignored | failed`, продвижение offset и
  последовательная обработка одной горутиной-поллером. На старте —
  досканирование оставшихся `pending` строк (recovery).
- Конкретный `*Client`-обёртка над `go-telegram/bot` (`getMe`,
  `getChat`, `getChatMember`, `sendMessage`, `setMyCommands`) с
  нормализацией ошибок Telegram (`429`/`retry_after`, `403`, ожидаемые
  no-op, постоянные ошибки прав).
- Router: маршрутизация по типу обновления и `chat.id`. Реально
  обрабатываются `message` (личка), `/here` в группе и `my_chat_member`;
  `chat_member` и `chat_join_request` пока завершаются как `ignored`
  (наполнят Фазы 03/05).
- Базовые команды: `/start` (регистрирует пользователя,
  `dm_state=open`, отвечает приветствием с инструкцией — без проверки
  подписки), `/help`, `/here` (только `OWNER_TG_IDS`). Любой
  не-командный текст в личке обрабатывается как `/start`.
- Пакет `notify` (формирование и отправка DM через узкий
  `messageSender`, учёт `users.dm_state`, `403 → blocked` без ретраев)
  и пакет `messages` (русские тексты пользовательских сообщений без
  inline-литералов в коде).
- Startup health-checks четырёх чатов (`getChat` + `getChatMember`)
  с записью в `meta.health.*`; обработка `my_chat_member` для здоровья
  и обнаружения чатов; `admin_alert` + DM владельцу при потере или
  восстановлении прав и при добавлении в незнакомый чат. Процесс при
  проблемах **не падает**, а осознанно деградирует.
- HTTP-сервер в дефолтном режиме (polling + observation, метрики off)
  **не поднимается**. Readiness до Фазы 07 обеспечивают startup
  health-checks, `meta.health.*`, `admin_alerts` и DM владельцу;
  `/healthz`/`/readyz` появятся в Фазе 07 и переиспользуют эти сигналы.

## Capabilities

### New Capabilities

- `telegram-transport`: транспортный контракт long polling — цикл
  `getUpdates` с явным `allowed_updates`, обёртка `*Client` и
  нормализация ошибок Telegram, durable inbox `telegram_updates`
  (state machine, инварианты I1/I2), разрешение offset, последовательная
  маршрутизация одной горутиной и recovery `pending` на старте.
- `bot-commands`: командная поверхность бота — `/start`, `/help`,
  `/here` и прощающий UX для не-командного текста; доставка DM через
  `notify` с учётом `dm_state`; внешние тексты сообщений в пакете
  `messages`.
- `chat-health`: здоровье присутствия бота — startup-проверка
  админ-прав в четырёх настроенных чатах, обработка `my_chat_member`
  (личка/известный чат/незнакомый чат), стабильные сигналы
  `meta.health.*`, `admin_alert` и DM владельцу, политика severity
  (источник — `error`, клубный ресурс — `critical`).

### Modified Capabilities

- `runtime`: порядок старта расширяется — создать Telegram-бота, задать
  `allowed_updates`, выполнить `getMe`, прогнать chat-health и запустить
  поллер под `errgroup.WithContext`. Требование «Phase 01 runs no
  background subsystems» заменяется жизненным циклом с работающим
  поллером; штатная остановка теперь дожидается завершения горутины
  поллера перед `db.Close()`. `TELEGRAM_MODE=webhook` (ещё не реализован)
  завершается fatal-ошибкой, а не молчаливым polling.
- `storage`: контракт repository расширяется методами для durable inbox
  (`TelegramUpdates.*`), `meta.update_offset`/`health.*`,
  `users.dm_state`, `audit_log` и `admin_alerts`; методы, участвующие в
  обработке обновлений, должны работать внутри одной транзакции, чтобы
  выдержать инвариант атомарности I1.

## Impact

- Новые файлы: `internal/telegram/{client,poller,router}.go`,
  `internal/store/updates.go`, `internal/notify/notify.go`,
  `internal/bot/user.go`, `internal/messages/messages.go`.
- Изменяются: `cmd/gatekeeper/main.go` (wiring транспорта, health,
  errgroup), `internal/store/{meta,users}.go` (ключи offset и `health.*`,
  `SetDMState`) и tx-aware repository-методы для атомарных обработчиков.
- Зависимости: подключаются `github.com/go-telegram/bot` и
  `golang.org/x/sync/errgroup` (заявлены в стеке §20.1, в сборку
  входят впервые).
- Данные: пишется существующая таблица `telegram_updates`, ключи `meta`
  (`update_offset`, `health.*`), поле `users.dm_state`.
- Конфигурация: используются уже загружаемые с Фазы 01
  `BOT_TOKEN`, `OWNER_TG_IDS` и идентификаторы четырёх чатов —
  новых переменных не вводится.
- Вне области: доменная проверка доступа (`/start` не проверяет
  подписку — Фаза 05), Enforcer и outbox (Фаза 04), HTTP-эндпоинты и
  `/readyz` (Фаза 07), обработка `chat_member`/`chat_join_request`
  (Фазы 03/05).
