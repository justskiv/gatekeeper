## Context

Gatekeeper уже умеет вычислять статус доступа из подписок и выполнять
исходящие Telegram-действия через durable outbox. При этом пользователь
пока не может пройти основной путь продукта: написать боту, получить
ссылки в клубные ресурсы и быть одобренным при вступлении.

Главные ограничения этой фазы заданы предыдущими changes: handler'ы не
вызывают Telegram из доменной транзакции, durable изменения и
`access_actions` коммитятся вместе с terminal status входящего update,
SQLite работает через одно соединение, а живые проверки источников
выполняются до `tx2`. Внутри `tx2` остаются только доменная запись,
outbox, audit/alert и терминальный статус обработки update.

Автоматический отзыв доступа при потере подписки ещё не готов. После
этой фазы система безопасно выдаёт доступ, но не закрывает его по
`inactive`; это остаётся областью Фазы 06.

## Goals / Non-Goals

**Goals:**

- Реализовать pull-модель выдачи доступа через `/start`, некомандный
  DM и callback повторной проверки.
- Обрабатывать `chat_join_request` для club chat и club channel с живой
  проверкой подписки перед approve/decline.
- Фиксировать `access_grants` для `pending`, `joined`, `left` и
  `external` membership-сценариев без миграций.
- Защитить shared и personal invite-ссылки от пересылки: ссылка сама
  по себе не даёт доступ без проверки источников.
- Отклонять неоднозначную конфигурацию source/club chat id до запуска
  poller.
- Сохранить инварианты I1/I2: сетевые Telegram side effects только
  через Enforcer.

**Non-Goals:**

- Автоматический отзыв доступа, grace-period и `pending_revocations`
  по `inactive` - это Фаза 06.
- Admin-команды `/grant`, `/revoke`, `/ban`, `/unban` и ручной импорт.
- Новые таблицы, миграции или дифференциация доступа по tiers.
- Webhook-production, reconciliation, metrics и HTTP health endpoints.

## Decisions

### D1. Admission flow живёт в доменном слое, а не в transport

Роутер только классифицирует Telegram update и передаёт его в
admission handler. Решение о доступе, запись `access_grants`, аудит и
постановка `access_actions` находятся рядом с текущим `engine`, чтобы
transport не знал бизнес-правил подписок.

Альтернатива - держать весь flow в `internal/telegram` - отвергнута:
это смешало бы parsing update'ов с hard-ban, fallback при `unknown`,
invite modes и состояниями grant'ов.

### D2. Live status считается до `tx2`

`/start`, retry callback, join-request approval и direct compensation
получают живой `effectiveStatus` вне `tx2`. Если результат нужен для
доменных изменений, handler после проверки открывает короткую `tx2` и
атомарно пишет grant/invite/audit/outbox/terminal update.

Альтернатива - держать `tx2` открытой во время probe источников -
отвергнута: источник может ходить в сеть, а это нарушает I2 и блокирует
единственное SQLite-соединение.

### D3. `/start` выдаёт доступ только по pull-запросу

`/start`, некомандный DM и retry callback запускают один grant-access
flow. Handler обеспечивает пользователя, выставляет `dm_state='open'`,
проверяет hard-ban, делает live status и дальше ветвится по вердикту.
При `active` он вызывает `recomputeAccess`, создаёт недостающие
`pending` grants и ставит выдачу ссылок: shared mode читает уже
подготовленные ссылки из `invite_links` и отправляет `MSG_ACTIVE`,
personal/direct mode ставит `send_invite` и отвечает
`MSG_INVITE_SOON`.

`unknown` на `/start` не является немедленным отказом: допускается
fallback на свежую active-подписку из БД в окне
`ADMISSION_FALLBACK_MAX_AGE` (по умолчанию 1 час), иначе пользователь
получает `MSG_TRY_LATER`, а владелец - alert. Автоматическая
push-рассылка ссылок всем active-пользователям отвергнута: она спамит
закрытые лички и плодит ссылки без пользовательского намерения вступить.

### D4. Join-request является настоящим шлагбаумом

`chat_join_request` никогда не доверяет самому факту наличия ссылки.
Handler определяет managed resource, разрешает ссылку через
`invite-links`, проверяет hard-ban и повторно делает live status. Только
финальный `active` приводит к `approve_join`, `access_grants.state` =
`joined`, audit и `MSG_GRANTED`.

Для `unknown` approval делает настраиваемые ретраи
`ADMISSION_JOIN_REQUEST_RETRIES` (по умолчанию 2) и затем fail-closed:
`decline_join` + `MSG_TRY_LATER`. Для personal-ссылки владелец ссылки
должен совпадать с `req.from.id`; чужая ссылка помечается
`used_by_other` с `attempted_by`. Если Telegram не прислал
`invite_link`, active-пользователь не отклоняется только из-за пустого
поля: shared mode опирается на resource, personal mode может найти
последнюю активную personal-ссылку пользователя для этого resource.

### D5. Club `chat_member` фиксирует фактическое membership

После вступления или выхода пользователя из club resource handler
обновляет `access_grants`. Если вступление пришло через join-request
или активную direct-ссылку пользователя, `admitted_by='bot'`; иначе
`admitted_by='external'`. Внешний участник не кикается автоматически:
это могло быть ручное решение владельца или наследие существующего
чата. Система пишет audit и `admin_alert(info,'external_join')`.

В `direct` режиме нет approve-шлагбаума, поэтому вступивший по direct
ссылке пользователь дополнительно перепроверяется; если статус уже не
`active`, ставится `soft_kick`. При выходе grant переводится в `left`,
но уже `revoked` grant не перетирается.

### D6. Идемпотентность держится на domain keys

`access_grants` upsert'ится по `(tg_id, resource)`, поэтому повторный
`/start` не создаёт второй `pending` grant. Outbox actions получают
stable `idempotency_key`; для `approve_join` ключ включает resource,
tg_id и дату/идентификатор Telegram join-request (§13.2), чтобы новая
заявка после выхода пользователя считалась новым действием.

Personal invite помечается `used` только на успешном approve. При
misuse она получает `used_by_other` и `attempted_by`, чтобы повторный
разбор той же заявки не терял причину отказа.

### D7. Границы runtime и сообщений остаются явными

Неоднозначность source chat id == club resource id отклоняется как
ошибка конфигурации до запуска poller; роутер не должен выбирать между
двумя доменными обработчиками для одного update. Пользовательские
тексты (`MSG_ACTIVE`, `MSG_INVITE_SOON`, `MSG_GRANTED`,
`MSG_TRY_LATER`, `MSG_BANNED`, `MSG_ALREADY_IN`) живут в `messages` и
доставляются через durable outbox.

`recomputeAccess` остаётся без `inactive`-ветки отзыва: в этой фазе он
может сохранить/починить active-доступ, но не кикает и не создаёт
revocation-действий. Это явная граница до Фазы 06.

## Risks / Trade-offs

- **R1. Status меняется между `/start` и join-request.** Митигация:
  join-request всегда делает повторную живую проверку перед approve.
- **R2. Telegram не присылает `invite_link`.** Митигация: fallback по
  resource для shared и по активной personal-ссылке пользователя для
  personal mode.
- **R3. `access_grants` может быть `joined` до фактического approve.**
  Митигация: Enforcer ретраит `approve_join`, а последующий
  `chat_member` подтверждает фактическое членство; dead action создаёт
  operator alert.
- **R4. Частые `/start` могут плодить outbox.** Митигация:
  per-user rate limit 30 секунд и idempotency keys для действий.
- **R5. Direct mode слабее join-request.** Митигация: режим уже
  guarded, TTL ограничен, а `chat_member` после direct вступления
  делает проверку и ставит `soft_kick` при отсутствии active.
- **R6. Между Фазами 05 и 06 истёкший подписчик может сохранить
  доступ.** Это принято осознанно: автоотзыв будет отдельной фазой.

## Migration Plan

1. Добавить admission ports/handler и расширить runtime wiring.
2. Расширить store/invite операции для разрешения ссылок и admission
   transitions поверх существующей схемы.
3. Перевести `/start`, некомандный DM и retry callback на новый flow.
4. Подключить маршруты `chat_join_request` и club `chat_member`.
5. Проверить startup-валидацию пересекающихся source/club chat id.
6. Покрыть `/start`, join-request, personal misuse, external join,
   direct compensation и idempotency key интеграционными тестами.
7. Проверить `task test`, `task lint` и
   `openspec validate phase-05-grant-access-merge --strict`.

Rollback не требует миграций: код можно откатить, а уже созданные
`pending` grants, invite rows и outbox actions останутся данными v1 и
не будут исполняться старым бинарём.

## Open Questions

- Нужен ли отдельный audit kind для rate-limit срабатываний, или
  достаточно пользовательского no-op ответа без записи?
- Должен ли `MSG_ACTIVE` в shared mode перечислять уже joined ресурсы
  отдельными строками или коротко отвечать `MSG_ALREADY_IN`, если
  недостающих ресурсов нет?
