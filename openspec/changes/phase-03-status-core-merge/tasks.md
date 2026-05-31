## 1. Источники подписок (`status-core`)

- [x] 1.1 `internal/source/membership.go`: структура `source.Membership`
  с `Platform()` и `Verdict()` через узкий `memberChecker`
  (`getChatMember`) по своему `chat_id`; маппинг §11.1,
  сетевые/`5xx`/`429` → `unknown`; probe — чистое чтение, без записи в БД
- [x] 1.2 Tribute membership с учётом локального ledger-сигнала
  (`expires_at` активной подписки), комбинатор `verdictTribute` целиком
- [x] 1.3 `internal/source/manual.go`: `source.Manual` — `active` при
  whitelist или активной `manual`-подписке с непрошедшим `expires_at`;
  иначе `no_signal`
- [x] 1.4 Источники не импортируют `engine`; связывает структурная
  типизация

## 2. Ядро движка (`status-core`)

- [x] 2.1 `internal/engine/ports.go`: интерфейс-потребитель
  `SubscriptionSource` и узкий `Store`
- [x] 2.2 `internal/engine/status.go`: чистый агрегатор
  `aggregate(verdicts, banned)` → `EffectiveStatus` + `AccessDecision`;
  живой снимок (сбор вердиктов по сети, вне `tx2`) и событийный пересчёт
  (по персистентному состоянию, внутри `tx2`) — два явных входа
- [x] 2.3 Применение source observations к `subscriptions` (`active`
  апсертит активную строку, `inactive` закрывает, `unknown`/`no_signal`
  не закрывают)
- [x] 2.4 `internal/engine/engine.go`: `handleEvent(SubscriptionEvent)` —
  upsert `users`, `audit_log`, создание/обновление/закрытие
  `subscriptions`, особый случай `cancelled_subscription`, вызов
  `recomputeAccess`; всё в `tx2`
- [x] 2.5 `recomputeAccess(tgID)` — ветки `active` (снять
  `pending_revocation`, если был, + `MSG_ACCESS_KEPT`) и `unknown`
  (no-op + `audit_log(status_unknown)`); `inactive` — `// TODO Фаза 06`
- [x] 2.6 `internal/engine/lock.go`: процессный keyed-mutex
  `map[int64]*sync.Mutex`; без ветвления на `tier`/платформу (инвариант 13)

## 3. Маршрутизация источников (`telegram-transport`)

- [x] 3.1 В роутере: `chat_member` с `chat.id == BOOSTY_GROUP_ID` или
  (режим A) `== TRIBUTE_CHANNEL_ID` → нормализация в `SubscriptionEvent`
  (`inChat` до/после) → `engine.handleEvent`
- [x] 3.2 Игнор ботов (включая себя) и смены прав без смены членства;
  применение события внутри `tx2` (членство из payload, не из сети)

## 4. Команды (`bot-commands`)

- [x] 4.1 `internal/bot/user.go`: `/status` (§15.1) — ensure user +
  `dm_state=open`; активные источники, `expires_at`, членство в клубных
  ресурсах; текст `MSG_STATUS`/`MSG_NO_SUB`; живой вердикт — вне `tx2`
- [x] 4.2 `internal/bot/admin.go`: `/whois <tg_id|@username>` (§15.2) —
  профиль, подписки, доступы, whitelist/ban, `AccessDecision` с
  `Reasons`, последние `audit_log`; `@username` только через
  `Users.FindByUsername`, без внешнего lookup
- [x] 4.3 `internal/messages/messages.go`: `MSG_STATUS` без
  inline-литералов

## 5. Расширение store (`storage`, `access-domain`)

- [x] 5.1 `internal/store/subscriptions.go`: `UpsertActive`,
  `ExpireActive(...)→ok` (append-only), `ListActiveByUser`, `ListByUser`
  (от новых к старым)
- [x] 5.2 `internal/store/users.go`: `FindByUsername` (локальный lookup)
- [x] 5.3 `internal/store/grants.go`: `ListByUser`;
  `internal/store/audit.go`: `ListRecentByUser(limit)`;
  `internal/store/revocations.go`: `Get` (различимое отсутствие);
  подтвердить `Whitelist.Has`
- [x] 5.4 `internal/domain`: подтвердить 4-значный `Verdict`
  (`no_signal` отличим от `inactive`), 3-значный `EffectiveStatus`

## 6. Тесты

- [x] 6.1 `status_test.go`: комбинации вердиктов
  `active/inactive/unknown/no_signal` через чистый `aggregate`; `active`
  выигрывает, `unknown` блокирует, все `no_signal` → `inactive`;
  приоритет hard-ban; `unknown` не становится `inactive`
- [x] 6.2 `engine_test.go`: `handleEvent` — `Activated`/`Deactivated`,
  `cancelled_subscription`, идемпотентность повтора, per-user locking
- [x] 6.3 `membership_test.go` и `manual_test.go`: маппинг статусов
  Telegram → вердикт; whitelist/manual → `active`/`no_signal`
- [x] 6.4 Store-тесты: `UpsertActive` (уникальность), `ExpireActive`
  (идемпотентность/`ok`), сортировка `ListByUser`/`ListRecentByUser`,
  локальный `FindByUsername`, `Revocations.Get`
- [x] 6.5 Router/poller-тест: `chat_member` источника → engine; явная
  проверка, что `getChatMember` **не** вызывается внутри `tx2`;
  атомарность terminal status с engine writes
- [x] 6.6 Bot-тесты: `/status`, `/whois <tg_id>`, `/whois @username`
  (известный/неизвестный), non-owner ignore

## 7. Верификация

- [x] 7.1 `task test` зелёный
- [ ] 7.2 Тестовый аккаунт вступает в Boosty-группу → строка
  `subscriptions` `platform='boosty'`, `status='active'`; выход → строка
  `expired` с `ended_at`
- [ ] 7.3 `/whois <tg_id>` → карточка с `AccessDecision.Reasons`;
  `/status` → список подписок и членства
- [ ] 7.4 Потеря ботом админ-прав в источнике-чате → вердикт `unknown`,
  `effectiveStatus` не становится `inactive` (fail-open)
- [x] 7.5 `task lint` и `openspec validate phase-03-status-core-merge
  --strict` зелёные
