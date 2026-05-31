## Why

После Фазы 02 бот в эфире: принимает обновления через durable inbox,
отвечает на команды и следит за здоровьем чатов. Но он не понимает, кто
подписчик: членство в Boosty-группе или Tribute-канале ни во что не
превращается, а у владельца нет способа объяснить, почему у человека
есть или нет доступ.

Цель фазы — доменное ядро статусов. Появляются источники подписок за
общим интерфейсом, функция `effectiveStatus` (объединение вердиктов
источников) и обработчик `SubscriptionEvent`. После неё вступление и
выход в источнике-чате меняют строки `subscriptions`, а владелец видит
объяснимый вердикт через `/whois`. Выдачи и отзыва доступа здесь ещё
нет: выдачу добавит Фаза 05, отзыв — Фаза 06 (для него нужен outbox из
Фазы 04).

## What Changes

- Пакет `source` с реализациями-структурами: `source.Membership`
  (по экземпляру для Boosty и Tribute, обёртка над `getChatMember`) и
  `source.Manual` (whitelist + активные `manual`-подписки). Реализации
  не импортируют `engine` — их связывает структурная типизация. Probe
  источника — **чистое чтение** без записи в БД.
- Пакет `engine`: тонкий интерфейс-потребитель `SubscriptionSource` и
  узкий `Store`; агрегатор статусов как **чистая функция** от собранных
  4-значных вердиктов (`active/inactive/unknown/no_signal`) к 3-значному
  итогу (`active/inactive/unknown`); два явных входа — *живой снимок* по
  сети (вне `tx2`) и *событийный пересчёт* по персистентному состоянию
  (внутри `tx2`); `handleEvent(SubscriptionEvent)` и `recomputeAccess`.
- Per-user сериализация: keyed-mutex в `engine`. Hard-ban перекрывает
  все вердикты; `unknown` никогда не трактуется как `inactive`.
- Маршрутизация `chat_member` в источниках-чатах
  (`BOOSTY_GROUP_ID`, `TRIBUTE_CHANNEL_ID` в режиме A): нормализация в
  `SubscriptionEvent` (`Activated`/`Deactivated` по смене членства),
  применение в `engine.handleEvent` внутри `tx2`. Боты и смена прав без
  смены членства игнорируются.
- Команды: `/status` (пользователю — его подписки и членство в клубных
  ресурсах; как любое сообщение в личку — ensure user и `dm_state=open`)
  и `/whois <tg_id|@username>` (владельцу — профиль, подписки, доступы,
  whitelist/ban, `AccessDecision` с `Reasons`, последние записи
  `audit_log`).
- Расширение repository: `Subscriptions` (история, `UpsertActive`,
  `ExpireActive`), `Users.FindByUsername`, `Grants.ListByUser`,
  `Audit.ListRecentByUser`, `Revocations.Get`, `Whitelist.Has`.
- Тексты команд (`MSG_STATUS` и пр.) — в пакете `messages`.

## Capabilities

### New Capabilities

- `status-core`: доменное ядро статусов — абстракция источника и три
  архетипа (membership/ledger/override), агрегация вердиктов в
  `effectiveStatus` (чистая функция + живой/событийный входы) с
  инвариантами безопасности (`active` выигрывает, `unknown` блокирует
  отзыв, hard-ban перекрывает всё), применение source observations к
  истории `subscriptions`, обработка `SubscriptionEvent` (`handleEvent`)
  и идемпотентный `recomputeAccess` (в этой фазе — только ветки
  `active`/`unknown`), per-user keyed-mutex.

### Modified Capabilities

- `access-domain`: source verdict фиксируется как 4-значный
  (`active/inactive/unknown/no_signal`, где `no_signal` отличим от
  `inactive`), итоговый `EffectiveStatus` остаётся 3-значным.
- `telegram-transport`: роутер начинает обрабатывать `chat_member` в
  источниках-чатах — нормализует смену членства в `SubscriptionEvent` и
  применяет через `engine.handleEvent` внутри `tx2`. Прежде эти
  обновления завершались как `ignored`.
- `bot-commands`: добавляются `/status` (пользовательская) и
  `/whois` (админская); каталог `messages` расширяется текстом
  `MSG_STATUS`.
- `storage`: контракт repository расширяется методами `Subscriptions`,
  `Users.FindByUsername`, `Grants.ListByUser`, `Audit.ListRecentByUser`,
  `Revocations.Get` и `Whitelist.Has`, нужными ядру и командам.
- `runtime`: порядок старта собирает `[]SubscriptionSource` и `engine`
  и прокидывает движок в роутер, чтобы события источников доходили до
  ядра.

## Impact

- Новые файлы: `internal/source/{membership,manual}.go`,
  `internal/engine/{ports,engine,status,lock}.go`,
  `internal/bot/admin.go` (`/whois`), тесты ядра
  (`status_test.go`, `engine_test.go`, `membership_test.go`,
  `manual_test.go`).
- Изменяются: `internal/telegram/router.go` (маршрут источников →
  engine), `internal/bot/user.go` (`/status`),
  `internal/store/{subscriptions,users,grants,audit,revocations,
  whitelist}.go` (новые методы), `internal/messages/messages.go`
  (`MSG_STATUS`), `cmd/gatekeeper/main.go` (сборка sources + engine).
- Данные: активно пишется `subscriptions`, читаются
  `whitelist`/`access_grants`/`audit_log`, при возврате подписки
  удаляется `pending_revocations`; новых таблиц и миграций нет.
- Зависимости и конфигурация: новых внешних зависимостей и env-ключей
  не вводится — используются уже загружаемые `BOOSTY_GROUP_ID`,
  `TRIBUTE_CHANNEL_ID`, `OWNER_TG_IDS`.
- Вне области: выдача доступа и invite-ссылки (`/start` поток, Фаза 05),
  Enforcer и outbox с реальным отзывом — ветка `inactive` в
  `recomputeAccess` (Фаза 06), вебхуки Tribute режима B (Фаза 07),
  фоновая реконсиляция и `chat_member` в клубных ресурсах (Фазы 05/06).
