# status-core Specification

## Purpose
TBD - created by archiving change phase-03-status-core-merge. Update Purpose after archive.
## Requirements
### Requirement: Источники подписки реализуют общий интерфейс-потребитель

`engine` MUST объявлять у себя тонкий интерфейс `SubscriptionSource`
(`Platform()` и `Verdict(ctx, tgID)`), а конкретные источники MUST быть
структурами в пакете `source`. Пакет `engine` MUST NOT импортировать
пакет `source` и MUST работать только со списком `[]SubscriptionSource`,
переданным при сборке зависимостей. Новый источник SHALL подключаться
добавлением структуры-реализации без правки ядра.

#### Scenario: Источник подключается без правки ядра
- **WHEN** в систему добавляется новая платформа-источник
- **THEN** появляется структура в пакете `source`, удовлетворяющая
  `SubscriptionSource` по сигнатурам
- **AND** она передаётся движку в составе `[]SubscriptionSource`, а
  агрегатор не меняется

#### Scenario: Пакет engine не зависит от конкретных источников
- **WHEN** компилируется пакет `engine`
- **THEN** он не импортирует пакет `source` и обращается к источникам
  только через `[]SubscriptionSource`

### Requirement: Источник даёт один из четырёх вердиктов чистым чтением

Каждый источник MUST возвращать ровно один вердикт `active`, `inactive`,
`unknown` или `no_signal` и человекочитаемую причину для `AccessDecision`.
Проверка источника MUST быть **чистым чтением**: probe MUST NOT писать в
БД, чтобы повтор на ретрае был безопасен. Если источнику нужен Telegram
API, вызов MUST выполняться **до** `tx2`. Допустимый набор вердиктов
MUST определяться архетипом:

- **membership** (живой `getChatMember`): `active` для
  `member`/`creator`/`administrator`/(`restricted` с `is_member=true`);
  `inactive` для `left`/`kicked`/(`restricted` с `is_member=false`);
  `unknown` при сетевой ошибке/`5xx`/`429`/таймауте. `no_signal` MUST
  NOT возвращаться — ответ есть всегда.
- **ledger** (запись с `expires_at` в БД): запись есть и не истекла →
  `active`; есть, но `expires_at` прошёл → `inactive`; записи нет →
  `no_signal`; ошибка БД → `unknown`.
- **override** (ручное основание): пользователь в `whitelist` **или**
  есть `manual`-подписка `status='active'` с (`expires_at IS NULL` или
  `now < expires_at`) → `active`; иначе `no_signal`. `inactive` MUST NOT
  возвращаться.

Источник `unknown`/`no_signal` MUST NOT трактовать как `inactive`.

#### Scenario: Membership-источник маппит статусы Telegram
- **WHEN** `source.Membership` получает статус
  `member`/`creator`/`administrator` или `restricted` с `is_member=true`
- **THEN** вердикт — `active`
- **AND** для `left`/`kicked`/`restricted` с `is_member=false` — `inactive`

#### Scenario: Сетевой сбой membership-источника даёт unknown
- **WHEN** `getChatMember` падает по сети, `5xx`, `429` или таймауту
- **THEN** вердикт — `unknown`, а не `inactive`

#### Scenario: Override-источник без основания не говорит "нет"
- **WHEN** пользователя нет ни в `whitelist`, ни среди активных
  `manual`-подписок
- **THEN** `source.Manual` возвращает `no_signal`, а не `inactive`

#### Scenario: Вердикт Tribute комбинирует membership и ledger
- **WHEN** membership-сигнал `a` и ledger-сигнал `b` вычислены
- **THEN** результат `active`, если активен `a` **или** `b`
- **AND** `inactive`, если `a` неактивен, а `b` равен `inactive` или
  `no_signal`; иначе `unknown`

#### Scenario: Probe не пишет в БД
- **WHEN** источник вычисляет вердикт
- **THEN** он не выполняет записей в БД, поэтому повтор probe на ретрае
  безопасен

### Requirement: Агрегация статуса — чистая функция с безопасными приоритетами

Объединение вердиктов MUST быть **чистой функцией** от собранного
списка вердиктов и флага hard-ban: без обращений к сети и БД. Она MUST
сводить 4-значные вердиктами к трёхзначному итогу
(`active`/`inactive`/`unknown`): hard-ban (`users.banned`) перекрывает
всё → `inactive`; иначе любой `active` → `active`; иначе любой `unknown`
→ `unknown`; иначе любой `inactive` → `inactive`; иначе (все
`no_signal`) → `inactive`. Вердикт `no_signal` MUST оставаться
внутренним и наружу не выходить. Функция MUST возвращать `AccessDecision`
с `Reasons` (по одному на источник плюс whitelist/ban).

#### Scenario: Один active выигрывает
- **WHEN** хотя бы один источник вернул `active`
- **THEN** итог — `active`, независимо от прочих вердиктов

#### Scenario: unknown блокирует отзыв
- **WHEN** ни один источник не `active`, но хотя бы один `unknown`
- **THEN** итог — `unknown`, а не `inactive`

#### Scenario: Все no_signal сводятся к inactive
- **WHEN** все источники вернули `no_signal`
- **THEN** итог — `inactive`

#### Scenario: Hard-ban перекрывает active
- **WHEN** у пользователя `users.banned=1`
- **THEN** итог — `inactive`, даже если источник вернул `active`

#### Scenario: Решение объяснимо
- **WHEN** агрегация завершилась
- **THEN** возвращён `AccessDecision` с `Reasons`, где у каждого
  источника указан вердикт и человекочитаемая причина

### Requirement: effectiveStatus имеет живой и событийный входы

Ядро MUST предоставлять два явных входа поверх чистого агрегатора, чтобы
событийный путь не мог обратиться к сети внутри `tx2`:

- **живой снимок**: собирает вердикты источников по сети **вне `tx2`**,
  затем вызывает агрегатор; используется командами `/status` и `/whois`.
- **событийный пересчёт**: определяет статус из **персистентного**
  состояния (активные подписки + hard-ban) **внутри `tx2`**, без сетевых
  probe; используется в обработке `SubscriptionEvent`.

Живой снимок MUST NOT удерживать `tx2` на время сетевого опроса;
событийный пересчёт MUST NOT выполнять Telegram-вызовов.

#### Scenario: Живой снимок не вызывается внутри tx2
- **WHEN** команда `/status` или `/whois` считает живой статус
- **THEN** сетевой опрос источников выполняется вне `tx2`

#### Scenario: Событийный пересчёт не ходит в сеть
- **WHEN** `handleEvent` пересчитывает доступ внутри `tx2`
- **THEN** он использует персистентное состояние и не вызывает Telegram

### Requirement: Source observations обновляют историю подписок

Успешные source observations MUST применяться к таблице `subscriptions`
в handler-транзакции. `active` MUST создавать или обновлять активную
строку `(tg_id, platform)` (заполнить `started_at` при создании,
очистить `ended_at`, обновить `last_checked_at`/`last_signal`).
`inactive` MUST закрывать активную строку платформы (`status='expired'`,
`ended_at`); отсутствие активной строки — no-op. `no_signal` MUST NOT
создавать отрицательную строку. `unknown` MUST NOT закрывать активную
подписку.

#### Scenario: Active observation апсертит активную подписку
- **WHEN** источник подтвердил активную подписку пользователя
- **THEN** в `subscriptions` ровно одна active-строка `(tg_id, platform)`
- **AND** строка отражает последний сигнал проверки

#### Scenario: Inactive observation закрывает активную подписку
- **WHEN** источник достоверно сообщил, что подписки больше нет
- **THEN** активная строка платформы переходит в `expired` с `ended_at`

#### Scenario: Unknown observation сохраняет активную строку
- **WHEN** источник вернул `unknown`
- **THEN** существующая активная подписка не переводится в `expired`

### Requirement: handleEvent применяет нормализованное событие подписки

`handleEvent(SubscriptionEvent)` MUST в одной handler-транзакции (`tx2`)
обновить `users`, записать `audit_log`, по `Kind` управлять строкой
`subscriptions` и затем вызвать событийный пересчёт `recomputeAccess`.
Событие MAY нести `EventAt`, `ExpiresAt`, `ExternalID`, `PeriodID`,
`Tier`, `Signal` и provider event name.

Для `Activated` обработчик MUST обновить активную подписку
`(tgID, platform)` или создать её (`status='active'`, `started_at=now`);
для `Deactivated` — перевести активную подписку в `status='expired'` с
`ended_at=now`. Обработка MUST быть идемпотентной: повтор события не
создаёт вторую активную строку, а повторная деактивация отсутствующей
активной строки — no-op.

Для Tribute webhook events обработчик MUST использовать
`EventAt=event.created_at` и записывать его в
`subscriptions.last_event_at`. `Activated` MUST применяться только если
`EventAt` новее текущего `last_event_at` этой активной Tribute
подписки; старое или равное событие MUST быть no-op для subscription
period и MUST NOT укорачивать `expires_at`. Новое
`new_subscription`/`renewed_subscription` MUST сохранить
`expires_at`, `external_id`, `external_period_id`, tier и
`last_signal='webhook'`.

`cancelled_subscription` MUST быть отдельным provider event. При
`TRIBUTE_CANCEL_IS_IMMEDIATE=false` обработчик MUST записать факт отмены
в `audit_log`, сохранить активную Tribute subscription и не сокращать
`expires_at`; при отсутствии активной строки expired-строка не
создаётся. При `TRIBUTE_CANCEL_IS_IMMEDIATE=true` обработчик MUST
применить cancel как `Deactivated`.

#### Scenario: Activated создаёт активную подписку
- **WHEN** приходит `Activated` для пользователя без активной подписки на
  этой платформе
- **THEN** создаётся строка `subscriptions` со `status='active'` и
  `started_at`
- **AND** пишется `audit_log(subscription_activated)`

#### Scenario: Deactivated закрывает активную подписку
- **WHEN** приходит `Deactivated` при наличии активной подписки
- **THEN** строка переходит в `status='expired'` с `ended_at`
- **AND** пишется `audit_log(subscription_expired)`

#### Scenario: Повторный Activated идемпотентен
- **WHEN** `Activated` для той же `(tgID, platform)` приходит повторно
- **THEN** обновляется существующая активная строка, вторая активная не
  создаётся (частичный уникальный индекс соблюдён)

#### Scenario: Tribute webhook Activated сохраняет expires_at
- **WHEN** приходит `new_subscription` или `renewed_subscription` с
  `EventAt`, `ExpiresAt`, `subscription_id`, `period_id` и tier
- **THEN** активная Tribute subscription хранит эти значения
- **AND** `last_signal='webhook'`
- **AND** `last_event_at` равен `EventAt`

#### Scenario: Устаревшее Tribute событие не укорачивает expires_at
- **WHEN** активная Tribute subscription имеет более новый
  `last_event_at`, чем входящий webhook `EventAt`
- **THEN** обработчик не меняет `expires_at`
- **AND** не запускает отзыв доступа из-за старого события

#### Scenario: Renewed subscription продлевает expires_at
- **WHEN** `renewed_subscription` имеет `EventAt` новее текущего
  `last_event_at` и более поздний `ExpiresAt`
- **THEN** active Tribute subscription обновляется до нового
  `expires_at`
- **AND** `recomputeAccess` видит active source

#### Scenario: cancelled_subscription не отзывает доступ сразу
- **WHEN** приходит особое событие отмены Tribute
  (`cancelled_subscription`) и `TRIBUTE_CANCEL_IS_IMMEDIATE=false`
- **THEN** факт отмены пишется в `audit_log`, `expires_at` активной
  подписки не сокращается
- **AND** активная строка не переводится в `expired`
- **AND** отзыв доступа не запускается до наступления `expires_at`

#### Scenario: Immediate cancel override отзывает доступ
- **WHEN** приходит `cancelled_subscription` и
  `TRIBUTE_CANCEL_IS_IMMEDIATE=true`
- **THEN** событие применяется как `Deactivated`
- **AND** `recomputeAccess` запускает обычный inactive path, если других
  active sources нет

#### Scenario: Cancel без активной строки не создаёт expired period
- **WHEN** приходит `cancelled_subscription` для пользователя без
  active Tribute subscription
- **THEN** expired subscription row не создаётся
- **AND** audit фиксирует provider cancellation как no-op

### Requirement: recomputeAccess безопасно координирует отзыв доступа

`recomputeAccess(tgID)` MUST быть идемпотентной entry point для
пересчёта доступа после subscription events, manual admin changes и
reconciliation. При `active` она MUST отменять existing
`pending_revocation`, писать `audit_log(revocation_cancelled)` и
готовить `MSG_ACCESS_KEPT`; без pending revocation active path MUST
быть no-op для access grants.

При `unknown` она MUST писать `audit_log(status_unknown)` и MUST NOT
создавать, исполнять или удалять revocation actions. При `inactive` она
MUST делегировать access-revocation flow: выбрать только bot-admitted
grants, применить `EXPIRY_MODE`, создать `pending_revocation`,
поставить warning или вызвать `revokeNow`.

Hard-ban MUST применяться до обычного status aggregation: если
`users.banned=1`, `recomputeAccess` MUST идти по hard-ban revocation
path даже при active source verdicts.

#### Scenario: Active снимает запланированный отзыв
- **WHEN** статус стал `active` и существует `pending_revocation`
- **THEN** запись отзыва удаляется
- **AND** пишется `audit_log(revocation_cancelled)`
- **AND** готовится `MSG_ACCESS_KEPT`

#### Scenario: Active без отзыва остаётся no-op
- **WHEN** статус `active`, но `pending_revocation` нет
- **THEN** доступ не меняется
- **AND** `MSG_ACCESS_KEPT` и revocation audit не пишутся

#### Scenario: Unknown ничего не трогает
- **WHEN** статус `unknown`
- **THEN** пишется `audit_log(status_unknown)`
- **AND** `pending_revocation`, `access_grants` и revoke actions не
  меняются

#### Scenario: Inactive планирует или исполняет отзыв
- **WHEN** статус стал `inactive` и есть bot-admitted grant в state
  `joined` или `pending`
- **THEN** `recomputeAccess` применяет `EXPIRY_MODE`
- **AND** результат соответствует access-revocation capability

#### Scenario: Active в одном источнике блокирует отзыв
- **WHEN** один source verdict равен `active`, а другой равен
  `inactive`
- **THEN** итоговый статус остаётся `active`
- **AND** `pending_revocation` не создаётся

#### Scenario: Hard-ban сильнее active source
- **WHEN** `users.banned=1`, но source verdict равен `active`
- **THEN** итоговый доступ считается `inactive` для пользователя
- **AND** запускается hard-ban revocation path

### Requirement: Операции движка сериализуются по пользователю

`engine` MUST сериализовать `handleEvent`/`recomputeAccess` для одного
`tgID` процессным keyed-mutex (`map[int64]*sync.Mutex` под общим замком).
Замок MUST быть процессным (общим между обработчиками), а не
создаваться заново на каждую транзакцию; события разных пользователей
MUST NOT блокировать друг друга. Агрегатор и `handleEvent` MUST NOT
ветвиться на `tier` или конкретную платформу (инвариант 13).

#### Scenario: Конкурентные операции по пользователю не пересекаются
- **WHEN** для одного `tgID` инициируются два пути обработки
- **THEN** они выполняются последовательно под одним keyed-mutex

#### Scenario: Разные пользователи независимы
- **WHEN** события относятся к разным `tgID`
- **THEN** они могут выполняться независимо друг от друга

#### Scenario: Логика не зависит от tier
- **WHEN** агрегатор или `handleEvent` принимают решение
- **THEN** результат не зависит от значения `tier` или имени платформы

