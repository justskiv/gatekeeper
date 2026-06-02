# Команды бота

Telegram-команды, durable-доставка личных сообщений и тонкие
bot-command входы в доменные workflows. Читаемое зеркало спеки
`bot-commands`.

## `/start` и запрос доступа

`/start` регистрирует пользователя (`ensureUser`, `dm_state='open'`,
обновить `last_seen_at`) и запускает grant-access flow как запрос
доступа. Команда остаётся тонким входом: регистрация пользователя,
прощающий UX для некомандного DM и durable ответы принадлежат
`bot-commands`, а правила verdict-to-message, grants, invites и
admission — за [grant-access](grant-access.md). Любой
некомандный текст в личке обрабатывается так же, как `/start`.

Durable ответ подбирается по результату flow: `active` → `MSG_ACTIVE`
(shared mode) или `MSG_INVITE_SOON` (personal/direct mode); `inactive`
→ `MSG_NO_SUB`; `unknown` без fallback → `MSG_TRY_LATER`; `banned` →
`MSG_BANNED`; уже joined ресурсы могут отвечать `MSG_ALREADY_IN`.
Синхронных Telegram-вызовов из handler'а нет: личные ответы идут через
durable `send_dm` или `send_invite`. Повторный `/start` идемпотентен —
строка `users` обновляется без дубля, второй `pending` grant и дубли
invite actions не создаются.

Retry controls (включая inline-кнопку «Проверить ещё раз») ведут в тот
же grant-access flow. Запрос доступа ограничен по частоте: не больше
одной effective-проверки за 30 секунд на пользователя. Rate-limited
retry не вызывает источники подписки, не создаёт grants и не ставит
invite actions.

## `/help`

`/help` отвечает краткой справкой: что делает бот, как оформить
подписку и почему важно писать с того же Telegram-аккаунта. Текст
берётся из пакета `messages`.

## `/here`

`/here` доступна только `OWNER_TG_IDS` и работает в группах или
супергруппах. В ответ бот пишет `chat.id` и тип чата, чтобы владелец
мог перенести ID в конфигурацию.

В каналах команда не работает: посты канала приходят как `channel_post`,
которого нет в `allowed_updates`, и не дают надёжного `from` для
проверки владельца. ID канала берётся из discovery-DM, когда бота
добавляют администратором.

Сообщения `/here` не от владельца игнорируются. Прочие сообщения в
не-личных чатах бот не сохраняет.

## `/status`

`/status` работает в личке и отвечает пользователю его текущим
статусом: активные источники подписок с `expires_at` (если известен) и
членство в клубных чате и канале. Как любое сообщение в личку, команда
обеспечивает строку `users` и выставляет `dm_state='open'`. Доступ она
не выдаёт и не отзывает. Живой вердикт источников (включая `unknown`
при потере ботом прав) считается вне `tx2`, чтобы не нарушать I2. Текст
строится из `messages` (`MSG_STATUS`; при отсутствии активной подписки
— `MSG_NO_SUB`).

## `/whois`

`/whois <tg_id|@username>` доступна только `OWNER_TG_IDS` и возвращает
карточку пользователя: профиль, подписки, доступы, флаги whitelist/ban,
`AccessDecision` с `Reasons` («почему есть или нет доступ») и последние
записи `audit_log`. Поиск по `@username` работает только если
пользователь уже известен в БД (`Users.FindByUsername`), без внешнего
Telegram-lookup. Обращение `/whois` не от владельца игнорируется, как
прочий некомандный текст.

## Команды владельца: доступ и баны

Команды `/grant`, `/revoke`, `/ban`, `/unban` и `/sync` работают только
в личке для `OWNER_TG_IDS`. Запросы от не-owner игнорируются, как
обычный некомандный текст, и не раскрывают данных пользователя.

`/grant <tg_id> [срок] [причина]` создаёт manual access: без срока —
whitelist entry, со сроком — `manual` subscription с `expires_at`.
Команда по числовому `tg_id` создаёт stub `users` row, если
пользователя ещё нет; lookup по `@username` работает только по
локальной БД.

`/revoke <tg_id> [причина]` снимает whitelist/manual access, пишет audit
и вызывает `recomputeAccess`; если других active sources нет, отзыв
идёт через configured `EXPIRY_MODE`.

`/ban <tg_id>` выставляет `users.banned=1` и немедленно запускает
hard-ban revocation. `/unban <tg_id>` снимает hard-ban; доступ после
unban возвращается только при active status и обычном запросе доступа.

`/sync [tg_id]` запускает reconciliation после подтверждения владельцем:
по одному пользователю, если аргумент указан, или полный pass, если
аргумента нет.

### Inline-подтверждение

Команды `/grant`, `/revoke`, `/ban`, `/unban` и `/sync` не применяют
изменение и не запускают reconciliation сразу после текстовой команды.
Бот показывает владельцу краткое summary будущего действия и
inline-кнопки confirm/cancel. Confirmation action id короткоживущий и
привязан к owner id, виду команды, optional target `tg_id` и
нормализованным аргументам.

Повторный callback идемпотентен: side effects выполняются один раз.
Expired, mismatched или уже исполненное подтверждение не повторяет side
effects и сообщает владельцу terminal result.

## Команды владельца: runtime-состояние

Команды `/stats`, `/alerts`, `/chats` и `/help_admin` работают только
для `OWNER_TG_IDS`. Запросы от не-owner игнорируются, как прочие
админские команды, и не раскрывают операционных данных. Эти команды не
меняют подписки, гранты, отзывы и состояние outbox — единственный их
побочный эффект — durable-доставка собственного ответа.

`/stats` отдаёт сводку: активные подписки, количество членств/грантов в
клубных ресурсах, pending revocations, здоровье четырёх настроенных
чатов, время последней reconciliation, размер outbox и счётчик dead
actions, число открытых тревог.

`/alerts` перечисляет открытые `admin_alerts` с id, severity, kind,
title, временем создания и признаком resolved/open. Если открытых
тревог нет, команда так и сообщает.

`/chats` показывает известные настроенные chat ID и их роли: Boosty
group, Tribute channel, клубный чат, клубный канал и опциональный
`ADMIN_LOG_CHAT_ID`.

`/help_admin` перечисляет команды владельца/админа с кратким
назначением; текст берётся из `messages` и не раскрывает секретов и
сырых операционных данных.

## `/export`

`/export` работает только для `OWNER_TG_IDS` и только в личке. Команда
формирует CSV с пользователями и текущим состоянием подписок: `tg_id`,
кэшированные username/профиль, активные платформы подписок, `expires_at`
(если известен), флаги whitelist/ban и текущие grant states. В группах
или супергруппах команда не публикует пользовательские данные.

CSV не содержит сырых payload'ов `telegram_updates` и `tribute_events`,
провайдерских секретов, email, полных invite-URL и токенов бота. Файл
доставляется через durable Telegram delivery; если клиент пока не умеет
слать документы, реализация может отправить текстовый CSV или безопасно
разбить вывод, но команда остаётся owner-only и private-chat-only.

## Меню команд

При старте бот регистрирует команды через `setMyCommands`:
пользовательские команды глобально, админские команды scoped на
`OWNER_TG_IDS`. Пользовательский scope — `/start`, `/help`, `/status`.
Admin scope (на `OWNER_TG_IDS`) — `/here`, `/whois`, `/grant`,
`/revoke`, `/ban`, `/unban`, `/sync`, `/stats`, `/alerts`, `/export`,
`/chats` и `/help_admin`. Ошибка регистрации меню не фатальна: она
логируется на `warn`, а старт продолжается, потому что сами команды
работают и без меню Telegram.

## DM-доставка

Пакет `notify` формирует личные сообщения через узкий
интерфейс-потребитель и для update-хендлеров ставит durable `send_dm`
action в той же handler-транзакции, где фиксируются durable изменения и
терминальный статус входящего update — вместо прямого Telegram-вызова.
Это касается всех личных ответов команд (`/start`, `/help`, `/status`,
`/whois`, некомандный DM-текст) и admission-сообщений (`MSG_ACTIVE`,
`MSG_INVITE_SOON`, `MSG_GRANTED`, `MSG_TRY_LATER`, `MSG_BANNED`,
`MSG_ALREADY_IN`). `send_invite` может сам отправить итоговый DM со
ссылкой после ensure personal/direct links.

Перед постановкой, если строка пользователя существует, notify
сверяется с `users.dm_state` и пропускает заведомо `blocked`
пользователя — лишний `send_dm` не создаётся. Ответ Telegram `403` при
исполнении Enforcer'ом означает закрытую личку: пользователь
помечается `dm_state='blocked'`, action завершается без retry (см.
[outbox-enforcer](outbox-enforcer.md)).

## Тревоги операторам

Каждое создание `admin_alert` приводит к durable operator delivery:
`send_dm` владельцам из `OWNER_TG_IDS` или сообщение в
`ADMIN_LOG_CHAT_ID`, если он настроен. Delivery ставится в outbox в той
же транзакции, где создаётся alert, либо идемпотентно восстановима по
alert id после рестарта. Критическая тревога уходит сама — владельцу не
нужно вызывать `/alerts`.

Delivery соблюдает идемпотентность: повторное создание той же открытой
тревоги по stable dedupe key не спамит владельца дубликатами. Если
владелец заблокировал личку, `send_dm` failure помечает
`dm_state='blocked'`, но alert row остаётся открытой.

## Тексты

Все пользовательские тексты (русский) живут в пакете `messages`,
inline-литералов пользовательских сообщений в коде нет — это упрощает
будущую локализацию и ревью. Там же лежат admission-сообщения,
`MSG_STATUS` и `MSG_ACCESS_KEPT` (последний готовит `recomputeAccess`,
см. [status-core](status-core.md)).

Revocation-сообщения `MSG_EXPIRY_WARNING`, `MSG_EXPIRED_NOTICE` и
`MSG_REVOKED` живут в `messages` и используются только через
durable-доставку. В `messages` лежат и owner/admin тексты:
подтверждения `/grant`, `/revoke`, `/ban`, `/unban`, `/sync`, результат
`/sync`, тексты operator alerts, финальный admin help (`/help_admin`) и
финальный user help (`/help`). Финальный `/help` описывает бота, ссылки
на оформление подписки и требование писать с того же Telegram-аккаунта.
