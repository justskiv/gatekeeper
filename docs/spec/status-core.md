# Ядро статусов (status-core)

Ядро вычисляет эффективный доступ пользователя из вердиктов источников. Оно построено вокруг чистого агрегатора и двух явных входов — живого и событийного, — чтобы событийный путь не мог обратиться к сети внутри `handleTx` (инварианты I1/I2 описаны в [telegram-transport](telegram-transport.md)).

## Источники за общим интерфейсом

`engine` объявляет у себя тонкий интерфейс `SubscriptionSource` (`Platform()` и `Verdict(ctx, tgID)`); конкретные источники — структуры в пакете `source`. `engine` не импортирует `source` и работает только со списком `[]SubscriptionSource`, переданным при сборке зависимостей. Новый источник подключается добавлением структуры-реализации без правки ядра.

## Вердикт источника — чистое чтение

Каждый источник возвращает ровно один вердикт (`active`/`inactive`/`unknown`/`no_signal`) и человекочитаемую причину для `AccessDecision`. Проверка источника — чистое чтение: probe не пишет в БД, поэтому повтор на ретрае безопасен. Если источнику нужен Telegram API, вызов идёт до `handleTx`.

Допустимый набор вердиктов задаёт архетип:

- **membership** (живой `getChatMember`): `active` для `member`/`creator`/`administrator`/(`restricted` с `is_member=true`); `inactive` для `left`/`kicked`/(`restricted` с `is_member=false`); `unknown` при сетевой ошибке/`5xx`/`429`/таймауте. `no_signal` не возвращается — ответ есть всегда.
- **ledger** (запись с `expires_at` в БД): есть и не истекла → `active`; есть, но `expires_at` прошёл → `inactive`; записи нет → `no_signal`; ошибка БД → `unknown`.
- **override** (ручное основание): пользователь в `whitelist` или есть `manual`-подписка `status='active'` с (`expires_at IS NULL` или `now < expires_at`) → `active`; иначе `no_signal`. `inactive` не возвращается.

`unknown`/`no_signal` источник не трактует как `inactive`.

Вердикт Tribute комбинирует membership-сигнал `a` и ledger-сигнал `b`: `active`, если активен `a` или `b`; `inactive`, если `a` неактивен, а `b` равен `inactive` или `no_signal`; иначе `unknown`. Так закрыта гонка «вебхук пришёл, в канал ещё не добавили»: активный ledger перекрывает membership-`inactive`.

## Агрегатор — чистая функция

Объединение вердиктов — чистая функция от списка вердиктов и флага hard-ban, без сети и БД. Приоритеты: hard-ban (`users.banned`) перекрывает всё → `inactive`; иначе любой `active` → `active`; иначе любой `unknown` → `unknown`; иначе любой `inactive` → `inactive`; иначе (все `no_signal`) → `inactive`. `no_signal` наружу не выходит. На выходе — `AccessDecision` с `Reasons` (по одному на источник плюс whitelist/ban).

Смысл приоритетов — fail-open: `unknown` блокирует отзыв (не превращается в `inactive`), а `active` хотя бы одного источника достаточно для доступа.

## Два входа: живой и событийный

- **Живой снимок** собирает вердикты источников по сети вне `handleTx`, затем зовёт агрегатор. Используется `/status` и `/whois`. На время сетевого опроса `handleTx` не удерживается.
- **Событийный пересчёт** (`recomputeAccess`) определяет статус из персистентного состояния (активные подписки + hard-ban) внутри `handleTx`, без сетевых probe и Telegram-вызовов. Используется при обработке `SubscriptionEvent`.

## Source observations → история подписок

Успешные observations применяются к `subscriptions` в handler-транзакции: `active` создаёт или обновляет активную строку `(tg_id, platform)` (заполнить `started_at` при создании, очистить `ended_at`, обновить `last_checked_at`/`last_signal`); `inactive` закрывает активную строку платформы (`status='expired'`, `ended_at`), при её отсутствии — no-op; `no_signal` не создаёт отрицательную строку; `unknown` не закрывает активную подписку.

## handleEvent

`handleEvent(SubscriptionEvent)` в одной транзакции `handleTx` обновляет `users`, пишет `audit_log`, по `Kind` управляет строкой `subscriptions`, затем зовёт `recomputeAccess`. Событие может нести `EventAt`, `ExpiresAt`, `ExternalID`, `PeriodID`, `Tier`, `Signal` и provider event name (для Tribute webhooks). `Activated` — обновить активную `(tgID, platform)` или создать (`status='active'`, `started_at=now`); `Deactivated` — перевести активную в `status='expired'` с `ended_at=now`. Обработка идемпотентна: повтор не создаёт вторую активную строку, повторная деактивация отсутствующей строки — no-op.

### Tribute webhooks: порядок событий

Для Tribute webhook events обработчик берёт `EventAt = event.created_at` и пишет его в `subscriptions.last_event_at`. `Activated` (`new_subscription`/`renewed_subscription`) применяется, только если `EventAt` строго новее `last_event_at` активной Tribute строки; событие старше-или-равно — no-op для subscription period и не укорачивает `expires_at`. Применение нового события сохраняет `expires_at`, `external_id`, `external_period_id`, tier и `last_signal='webhook'`, а также обновляет `last_event_at`. Эта защита по порядку делает модель устойчивой к out-of-order доставке вебхуков.

### Tribute webhooks: отмена

`cancelled_subscription` — отдельный provider event. При `TRIBUTE_CANCEL_IS_IMMEDIATE=false` (по умолчанию) обработчик пишет факт отмены в `audit_log`, но сохраняет активную подписку и не сокращает `expires_at` и не запускает отзыв; при отсутствии активной строки это audit-only no-op (expired-строка не создаётся). При `TRIBUTE_CANCEL_IS_IMMEDIATE=true` cancel трактуется как `Deactivated` (expire плюс обычный inactive-путь пересчёта).

## recomputeAccess

Идемпотентная entry point для пересчёта доступа после subscription events, ручных admin-изменений и reconciliation. Реализует три ветки:

- `active`: если `pending_revocation` существовал — отменить его, записать `audit_log(revocation_cancelled)`, подготовить `MSG_ACCESS_KEPT`; без pending revocation active-путь — no-op для access grants.
- `unknown`: записать `audit_log(status_unknown)`; не создаёт, не исполняет и не удаляет revocation actions.
- `inactive`: делегирует access-revocation flow — выбрать только bot-admitted grants, применить `EXPIRY_MODE`, создать `pending_revocation`, поставить warning или вызвать `revokeNow`.

Hard-ban применяется до обычной агрегации статуса: при `users.banned=1` `recomputeAccess` идёт по hard-ban revocation path даже при active source verdicts.

## Сериализация по пользователю

`handleEvent`/`recomputeAccess` для одного `tgID` сериализуются процессным keyed-mutex — общим между обработчиками, не пересоздаётся на каждую транзакцию. События разных пользователей друг друга не блокируют. Агрегатор и `handleEvent` не ветвятся на `tier` или конкретную платформу.
