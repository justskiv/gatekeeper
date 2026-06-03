# bot-commands Specification

## Purpose

Описывает команды Telegram-бота, durable-доставку личных сообщений и
тонкие bot-command входы в доменные workflows.
## Requirements
### Requirement: Запрос доступа ограничен по частоте и повторяем кнопкой

Бот MUST направлять retry controls, включая inline-кнопку "Проверить
ещё раз", в тот же grant-access flow, что и `/start`. Запрос доступа
MUST быть ограничен по частоте для каждого пользователя: не больше
одной effective-проверки за 30 секунд. Rate-limited retry MUST NOT
вызывать источники подписки, создавать grants или ставить invite
actions.

#### Scenario: Кнопка повторной проверки использует start flow
- **WHEN** пользователь нажимает кнопку повторной проверки после
  admission-сообщения
- **THEN** бот запускает тот же grant-access flow, что и для `/start`

#### Scenario: Повторная проверка ограничена по пользователю
- **WHEN** пользователь повторно нажимает retry раньше 30 секунд
- **THEN** источники подписки не вызываются
- **AND** новые invite actions не создаются

### Requirement: `/start` registers the user and returns the greeting

`/start` MUST регистрировать пользователя (`ensureUser`,
`dm_state='open'`, обновить `last_seen_at`) и запускать grant-access
flow как запрос доступа. Команда MUST оставаться тонким входом:
регистрация пользователя, прощающий UX для некомандного DM и durable
ответы принадлежат `bot-commands`, а detailed verdict-to-message,
grant, invite и admission правила принадлежат capability
`grant-access`.

Команда MUST подбирать durable ответ по результату grant-access flow:
`active` -> `MSG_ACTIVE` в shared mode или `MSG_INVITE_SOON` в
personal/direct mode; `inactive` -> `MSG_NO_SUB`; `unknown` без
fallback -> `MSG_TRY_LATER`; `banned` -> `MSG_BANNED`; already joined
resources MAY отвечать `MSG_ALREADY_IN`. Любой некомандный текст в
личке MUST обрабатываться так же, как `/start`. Синхронные Telegram
вызовы из handler'а MUST NOT выполняться; личные ответы MUST идти через
durable `send_dm` или `send_invite`.

#### Scenario: Первый /start регистрирует пользователя с открытой личкой
- **WHEN** пользователь впервые отправляет `/start` в личку
- **THEN** в `users` появляется строка с `dm_state='open'` и
  заполненным `last_seen_at`
- **AND** запускается grant-access flow

#### Scenario: Активный подписчик получает admission-ответ
- **WHEN** активный подписчик отправляет `/start`
- **THEN** бот ставит durable `MSG_ACTIVE` или `MSG_INVITE_SOON` в
  зависимости от `INVITE_MODE`

#### Scenario: Неактивный получает MSG_NO_SUB
- **WHEN** `/start` приходит от пользователя без активной подписки
- **THEN** бот ставит durable `MSG_NO_SUB`

#### Scenario: UNKNOWN без fallback получает MSG_TRY_LATER
- **WHEN** живой `effectiveStatus` равен `unknown` и свежей
  active-подписки в БД нет
- **THEN** бот ставит durable `MSG_TRY_LATER`

#### Scenario: Некомандный DM-текст ведёт себя как /start
- **WHEN** обычный пользователь шлёт в личку произвольный текст без команды
- **THEN** бот обрабатывает его как `/start`, включая grant-access flow

#### Scenario: Повторный /start идемпотентен
- **WHEN** пользователь отправляет `/start` повторно
- **THEN** существующая строка `users` обновляется, дубль не создаётся
- **AND** второй `pending` grant и дубль invite actions не создаются

### Requirement: `/help` returns the help text

`/help` MUST отвечать краткой справкой: что делает бот, как оформить
подписку и важное замечание «писать с того же аккаунта Telegram». Текст
берётся из пакета `messages`.

#### Scenario: Help-команда отвечает
- **WHEN** пользователь отправляет `/help` в личку
- **THEN** бот отвечает справочным текстом из пакета `messages`

### Requirement: `/here` answers chat id and type for owners only

`/here` MUST быть доступна только идентификаторам из `OWNER_TG_IDS` и
работать в группах/супергруппах: бот отвечает `chat.id` и типом чата —
это помогает владельцу узнать ID для конфигурации. В каналах `/here`
не работает: посты канала приходят как `channel_post` (его нет в
`allowed_updates`) и не несут надёжного `from` для проверки владельца;
ID канала владелец узнаёт из discovery-DM, когда добавляет бота
админом. Обращение `/here` не от владельца игнорируется; прочие
сообщения в не-личных чатах бот не хранит.

#### Scenario: Владелец получает chat id и тип
- **WHEN** владелец отправляет `/here` в группе или супергруппе
- **THEN** бот отвечает значением `chat.id` и типом чата

#### Scenario: /here не от владельца игнорируется
- **WHEN** `/here` отправляет идентификатор не из `OWNER_TG_IDS`
- **THEN** бот не отвечает и не сохраняет сообщение

### Requirement: setMyCommands registers user and admin command scopes

При старте бот MUST регистрировать меню команд через `setMyCommands`:
пользовательские команды — глобально, админские — scoped на
`OWNER_TG_IDS`. Пользовательские команды MUST включать `/start`,
`/help` и `/status`. Admin scope MUST включать `/here`, `/whois`,
`/grant`, `/revoke`, `/ban`, `/unban`, `/sync`, `/stats`, `/alerts`,
`/export`, `/chats` и `/help_admin`. Ошибка `setMyCommands` —
**best-effort, не фатальна**: логируется на уровне `warn` и не
прерывает старт (меню — лишь подсказка, сами команды работают и без
него).

#### Scenario: Command scopes регистрируются при старте
- **WHEN** бот стартует
- **THEN** пользовательские команды зарегистрированы глобально
- **AND** админские команды зарегистрированы scoped на `OWNER_TG_IDS`

#### Scenario: Ops commands входят в admin scope
- **WHEN** бот регистрирует admin command scope
- **THEN** `/stats`, `/alerts`, `/export`, `/chats` и `/help_admin`
  входят в список admin commands

#### Scenario: Ошибка регистрации команд не прерывает старт
- **WHEN** `setMyCommands` возвращает ошибку при старте
- **THEN** ошибка логируется на уровне `warn` и старт продолжается

### Requirement: DM delivery respects dm_state and handles blocking

Пакет `notify` MUST формировать личные сообщения через узкий
интерфейс-потребитель и для Telegram update handler'ов MUST ставить
durable `send_dm` action вместо прямого Telegram-вызова. Перед
постановкой, если строка пользователя существует, notify MUST сверяться
с `users.dm_state` и **пропускать** заведомо `blocked` пользователя
(не создаёт лишний `send_dm`). Ответ Telegram `403` при фактическом
исполнении Enforcer'ом означает закрытую личку: пользователь
помечается `dm_state='blocked'`, action завершается без retry.

Текущие личные ответы bot-command handler'ов (`/start`, `/help`,
`/status`, `/whois` и некомандный DM-текст, который обрабатывается как
`/start`) MUST enqueue `send_dm` в той же handler-транзакции, где
фиксируются durable изменения и terminal status входящего update.
Admission-specific сообщения (`MSG_ACTIVE`, `MSG_INVITE_SOON`,
`MSG_GRANTED`, `MSG_TRY_LATER`, `MSG_BANNED`, `MSG_ALREADY_IN`) MUST
использовать тот же outbox/Enforcer канал. `send_invite` MAY send the
final invite-bearing DM itself after ensuring personal or direct links.

#### Scenario: Command DM ставится в durable outbox
- **WHEN** bot-command handler должен отправить личное сообщение
  пользователю
- **THEN** в той же handler-транзакции создаётся `send_dm` action
- **AND** прямой `sendMessage` из handler'а не вызывается

#### Scenario: Блокировка выставляет dm_state и не ретраится
- **WHEN** отправка DM через Enforcer возвращает `403`
- **THEN** у пользователя выставляется `users.dm_state='blocked'`
- **AND** action завершается без повторов

#### Scenario: Известный blocked-пользователь пропускается до enqueue
- **WHEN** notify просят отправить DM пользователю с
  `dm_state='blocked'`
- **THEN** `send_dm` action не создаётся
- **AND** вызов `sendMessage` не делается

#### Scenario: Admission replies используют durable delivery
- **WHEN** grant-access handler должен сообщить пользователю результат
  `/start` или join-request
- **THEN** сообщение ставится через `send_dm` или `send_invite`
- **AND** handler не вызывает `sendMessage` напрямую

### Requirement: User-facing texts come from the messages package

Все пользовательские тексты (русский, §15.4) MUST жить в пакете
`messages`; inline-литералов пользовательских сообщений в коде нет
(§20.3) — это упрощает будущую локализацию. Admission-сообщения
`MSG_ACTIVE`, `MSG_INVITE_SOON`, `MSG_GRANTED`, `MSG_TRY_LATER`,
`MSG_BANNED`, `MSG_ALREADY_IN` и вариант `MSG_ACTIVE` для `direct` без
обещания approve-заявки MUST жить в `messages`. Текст
`MSG_ACCESS_KEPT` MUST браться из `messages`, когда его готовит
`recomputeAccess`.

Revocation-сообщения `MSG_EXPIRY_WARNING`, `MSG_EXPIRED_NOTICE` и
`MSG_REVOKED` MUST жить в `messages` и использоваться только через
durable delivery. Owner/admin тексты для подтверждения `/grant`,
`/revoke`, `/ban`, `/unban`, `/sync`, результата `/sync` и operator
alerts MUST также жить в `messages`.

#### Scenario: Сообщения берутся из пакета, а не инлайнятся
- **WHEN** бот отправляет пользовательское сообщение
- **THEN** текст берётся из пакета `messages`, а не из строкового
  литерала в месте вызова

#### Scenario: MSG_STATUS доступен в пакете
- **WHEN** `/status` формирует ответ
- **THEN** он использует `MSG_STATUS` из пакета `messages`

#### Scenario: Admission messages доступны в messages
- **WHEN** grant-access flow формирует ответ пользователю
- **THEN** он использует admission-текст из пакета `messages`

#### Scenario: Revocation messages доступны в messages
- **WHEN** access-revocation flow предупреждает, отменяет или исполняет
  отзыв
- **THEN** он использует `MSG_EXPIRY_WARNING`, `MSG_EXPIRED_NOTICE`,
  `MSG_ACCESS_KEPT` или `MSG_REVOKED` из пакета `messages`

### Requirement: `/status` показывает пользователю его подписки и членство

`/status` MUST работать в личке и отвечать пользователю его текущим
статусом: активен ли доступ, какие активные источники подписки
известны пользователю, `expires_at`, если он известен, состояние
клубных чата и канала, и следующее действие. Как любое сообщение в
личку, команда MUST обеспечить строку пользователя (`ensureUser`) и
выставить `dm_state='open'` (пользователь нам написал — личка открыта).
Команда MUST NOT выдавать или отзывать доступ.

Живой вердикт источников (включая `unknown` при потере ботом прав) MUST
считаться **вне `handleTx`**, чтобы не нарушать инвариант I2. Текст MUST
строиться из пакета `messages` (`MSG_STATUS`; при отсутствии активной
подписки — финальный текст об отсутствии активной подписки).

Ответ `/status` для обычного пользователя MUST NOT показывать
`AccessDecision.Reasons`, сырые verdict'ы источников, `whitelist`,
`system`, локальную БД, сырые chat IDs или diagnostics. Эти детали
доступны только владельцу/админу через `/whois` или alerts.

#### Scenario: Пользователь видит активные подписки
- **WHEN** пользователь с активной подпиской отправляет `/status`
- **THEN** бот отвечает человекочитаемым статусом доступа
- **AND** ответ показывает активные источники и (если известно) даты
  `expires_at`
- **AND** ответ показывает состояние клубных ресурсов без сырых chat IDs

#### Scenario: Нет активных подписок
- **WHEN** `/status` отправляет пользователь без активных подписок
- **THEN** ответ сообщает, что активная подписка не найдена
- **AND** ответ даёт следующее действие для оформления или повторной
  проверки
- **AND** доступ не выдаётся

#### Scenario: Status открывает личку
- **WHEN** пользователь впервые пишет `/status`
- **THEN** строка `users` существует и `dm_state='open'`

#### Scenario: Пользовательский status скрывает internal reasons

- **WHEN** `AccessDecision` содержит internal `Reasons`
- **THEN** `/status` не включает эти reasons в пользовательский текст
- **AND** ответ не содержит сырые имена внутренних источников, raw
  `chat_id` или формулировки локального хранилища

### Requirement: `/whois` объясняет владельцу статус пользователя

`/whois <tg_id|@username>` MUST быть доступна только идентификаторам из
`OWNER_TG_IDS` и возвращать карточку пользователя: профиль, подписки,
доступы, флаги whitelist/ban, `AccessDecision` с `Reasons` («почему есть
или нет доступ») и последние записи `audit_log`. Поиск по `@username`
MUST работать только если пользователь уже известен в БД
(`Users.FindByUsername`), без внешнего Telegram-lookup. Обращение
`/whois` не от владельца MUST игнорироваться так же, как прочий
не-командный текст.

Карточка MUST начинаться с summary: сначала имя/username/TG ID, общий
статус доступа, источник или основание доступа, сроки и состояние
клубных ресурсов; затем последние события; затем отдельный diagnostics
block с raw reasons, provider/internal details и technical IDs. Сырые
enum-like values MUST переводиться в стабильные русские labels в
основном summary.

#### Scenario: Владелец получает объяснимую карточку
- **WHEN** владелец отправляет `/whois <tg_id>` по известному пользователю
- **THEN** бот отвечает профилем, подписками, доступами и
  `AccessDecision` с `Reasons`
- **AND** человеческое summary идёт до diagnostics

#### Scenario: Поиск по username вне БД
- **WHEN** владелец указывает `@username`, которого нет в БД
- **THEN** бот сообщает, что пользователь не найден, без обращения к сети

#### Scenario: /whois не от владельца игнорируется
- **WHEN** `/whois` отправляет идентификатор не из `OWNER_TG_IDS`
- **THEN** команда игнорируется как обычный текст, данные не раскрываются

#### Scenario: Whois группирует diagnostics

- **WHEN** `/whois` включает raw reason details, audit detail или IDs
- **THEN** эти значения появляются только после основного status summary
- **AND** владелец всё ещё видит достаточно diagnostics для отладки доступа

### Requirement: Owner access commands manage manual access and bans

Owner commands `/grant`, `/revoke`, `/ban`, `/unban` and `/sync` MUST
работать только в личке для `OWNER_TG_IDS`. Запросы от не-owner MUST
игнорироваться как обычный некомандный текст и MUST NOT раскрывать
данные пользователя.

`/grant <tg_id> [срок] [причина]` MUST создавать manual access: без
срока — whitelist entry, со сроком — `manual` subscription с
`expires_at`. Команда по числовому `tg_id` MUST создавать stub
`users` row, если пользователя ещё нет; lookup по `@username` MUST
работать только по локальной БД.

`/revoke <tg_id> [причина]` MUST снять whitelist/manual access,
записать audit и вызвать `recomputeAccess`; если других active sources
нет, отзыв MUST пойти через configured `EXPIRY_MODE`. `/ban <tg_id>`
MUST выставить `users.banned=1` и немедленно запустить hard-ban
revocation. `/unban <tg_id>` MUST снять hard-ban; доступ после unban
возвращается только при active status и обычном запросе доступа.

`/sync [tg_id]` MUST запускать reconciliation после owner confirmation:
для одного пользователя, если аргумент указан, или полный pass, если
аргумента нет.

#### Scenario: Grant создаёт stub user по числовому tg_id
- **WHEN** owner выполняет `/grant 12345` для неизвестного пользователя
- **THEN** создаётся stub `users` row с `tg_id=12345`
- **AND** добавляется whitelist или manual subscription
- **AND** вызывается `recomputeAccess`

#### Scenario: Revoke запускает configured revocation
- **WHEN** owner выполняет `/revoke <tg_id>` и других active sources нет
- **THEN** manual access снимается
- **AND** `recomputeAccess` применяет `EXPIRY_MODE`

#### Scenario: Ban перекрывает active subscription
- **WHEN** owner подтверждает `/ban <tg_id>` для active пользователя
- **THEN** `users.banned` становится `1`
- **AND** hard-ban revocation ставится через outbox

#### Scenario: Sync одного пользователя не запускает полный pass
- **WHEN** owner выполняет `/sync <tg_id>`
- **THEN** Reconciler проверяет только указанного пользователя и
  связанные resources
- **AND** full reconciliation pass не запускается

### Requirement: Owner action commands require inline confirmation

Commands `/grant`, `/revoke`, `/ban`, `/unban` and `/sync` MUST NOT
применять изменение или запускать reconciliation сразу после текстовой
команды. Bot MUST показать owner'у краткое summary будущего действия и
inline-кнопки confirm/cancel. Confirmation action id MUST быть
короткоживущим, привязанным к owner id, command kind, optional target
tg_id и normalized arguments.

Повторный callback MUST быть идемпотентен. Expired, mismatched или уже
исполненный confirmation MUST NOT повторять side effects и MUST
сообщать owner'у terminal result.

#### Scenario: Ban требует подтверждения
- **WHEN** owner отправляет `/ban 12345 abuse`
- **THEN** `users.banned` не меняется до confirm callback
- **AND** owner получает inline confirmation summary

#### Scenario: Sync требует подтверждения
- **WHEN** owner отправляет `/sync`
- **THEN** full reconciliation pass не запускается до confirm callback
- **AND** owner получает inline confirmation summary

#### Scenario: Повторный confirm не дублирует действие
- **WHEN** owner дважды нажимает confirm для одного action id
- **THEN** side effects выполняются один раз
- **AND** второй callback получает terminal result без новых outbox rows

#### Scenario: Истёкшее подтверждение не исполняется
- **WHEN** owner нажимает confirm после expiry action id
- **THEN** command side effects не выполняются
- **AND** owner получает сообщение, что действие устарело

### Requirement: Admin alerts are delivered durably to operators

Каждое создание `admin_alert` MUST приводить к durable operator
delivery: `send_dm` владельцам из `OWNER_TG_IDS` или message в
`ADMIN_LOG_CHAT_ID`, если он настроен. Delivery MUST ставиться в outbox
в той же transaction, где создаётся alert, либо быть идемпотентно
восстановимой по alert id после рестарта.

Alert delivery MUST respect idempotency: повторное создание той же
открытой тревоги по stable dedupe key MUST NOT спамить владельца
дубликатами. Если владелец заблокировал личку, `send_dm` failure MUST
помечать `dm_state='blocked'`, но alert row MUST оставаться открытой.

#### Scenario: Critical alert уходит без ручного /alerts
- **WHEN** система создаёт `admin_alert(severity='critical')`
- **THEN** в outbox появляется operator delivery action
- **AND** owner не должен вызывать `/alerts`, чтобы узнать о тревоге

#### Scenario: ADMIN_LOG_CHAT_ID получает alert вместо лички
- **WHEN** `ADMIN_LOG_CHAT_ID` настроен
- **THEN** alert delivery адресуется в этот chat id
- **AND** личные DM владельцам не обязательны для этого alert

#### Scenario: Duplicate alert не спамит owner'а
- **WHEN** та же открытая тревога создаётся повторно по dedupe key
- **THEN** новая alert row не создаётся или связывается с прежней
- **AND** duplicate delivery action не ставится

### Requirement: Owner ops commands expose runtime state without side effects

Owner commands `/stats`, `/alerts`, `/chats` и `/help_admin` MUST
работать только для `OWNER_TG_IDS`. Запросы от non-owner MUST
игнорироваться как другие non-owner admin commands и MUST NOT раскрывать
операционные данные. Команды MUST NOT менять subscriptions, grants,
revocations или outbox state, кроме durable delivery собственного
ответа.

`/stats` MUST суммировать active subscriptions, club resource membership
или grant counts, pending revocations, health четырёх настроенных чатов,
время последней reconciliation, outbox size, dead action count и open
alert count. Ответ MUST быть оформлен так, чтобы сначала шло
человекочитаемое summary, затем компактные counters; raw diagnostics
опускаются, если они не полезны.

`/alerts` MUST перечислять open `admin_alerts` с id, severity, kind,
title, created time и признаком resolved/open. Если open alerts нет,
ответ MUST сказать об этом. Alerts MUST группироваться или сортироваться
по severity и MUST показывать проблему и влияние до raw kind/detail
fields.

`/chats` MUST показывать известные configured chat IDs и их roles:
Boosty group, Tribute channel, club chat, club channel и optional
`ADMIN_LOG_CHAT_ID`. Человекочитаемая role/status MUST идти перед raw
chat ID.

`/help_admin` MUST перечислять owner/admin commands и их короткое
назначение, сгруппированное по task area, где это практично.

#### Scenario: Владелец видит stats summary
- **WHEN** владелец отправляет `/stats`
- **THEN** бот возвращает durable message со сводкой subscriptions,
  grants, revocations, health, reconcile, outbox и alerts
- **AND** сообщение начинается с читаемого state summary
- **AND** domain access state не меняется

#### Scenario: Non-owner не читает stats
- **WHEN** non-owner отправляет `/stats`
- **THEN** команда игнорируется и operational data не раскрываются

#### Scenario: Alerts command перечисляет open alerts
- **WHEN** владелец отправляет `/alerts`, и open alerts существуют
- **THEN** бот возвращает их id, severity, kind, title и created time
- **AND** problem summary идёт до raw alert diagnostics

#### Scenario: Chats command показывает configured roles
- **WHEN** владелец отправляет `/chats`
- **THEN** бот возвращает configured chat IDs, сгруппированные по
  operational role
- **AND** chat IDs не являются leading content каждой строки

#### Scenario: Admin help перечисляет admin commands
- **WHEN** владелец отправляет `/help_admin`
- **THEN** бот возвращает список admin commands из `messages`

### Requirement: Owner export command sends CSV with users and subscriptions

`/export` MUST work only for `OWNER_TG_IDS` and only in a private chat.
It MUST produce CSV containing users and current subscription state,
including `tg_id`, cached username/profile fields, active subscription
platforms, `expires_at` when known, whitelist/ban flags and current
grant states. The export MUST NOT include raw `telegram_updates` or
`tribute_events` payloads, provider secrets, email, full invite URLs or
bot tokens.

The CSV MUST be delivered through durable Telegram delivery. If the
Telegram client path cannot send documents yet, implementation MAY send
a text CSV or split output safely, but the command MUST remain
owner-only and private-chat-only.

#### Scenario: Owner receives CSV export
- **WHEN** owner sends `/export` in a private chat
- **THEN** bot delivers CSV with users and current subscriptions
- **AND** raw inbox payloads and secrets are absent

#### Scenario: Export is not available in group chats
- **WHEN** owner sends `/export` in a group or supergroup
- **THEN** bot does not publish user data in that chat

#### Scenario: Non-owner cannot export
- **WHEN** a non-owner sends `/export`
- **THEN** command is ignored and no CSV is produced

### Requirement: Пользовательская и admin-справка содержит финальные entries

Пакет `messages` MUST включать финальный user help и admin help text.
`/help` MUST описывать бота, subscription flow и требование писать с
того же Telegram account. Он MUST перечислять только user commands и
MUST NOT раскрывать owner/admin commands, raw operational data или
внутренние implementation details.

`/help_admin` MUST описывать owner commands без раскрытия secrets или
raw operational data. Он MUST быть структурирован для сканирования по
задачам: lookup, access management, reconciliation, runtime state и
export.

#### Scenario: User help упоминает тот же Telegram account
- **WHEN** пользователь отправляет `/help`
- **THEN** ответ объясняет, как оформить подписку, и говорит писать с
  того же Telegram account
- **AND** ответ перечисляет только user commands

#### Scenario: Admin help берётся из messages
- **WHEN** владелец отправляет `/help_admin`
- **THEN** текст ответа приходит из `messages`, а не из inline handler
  literals
- **AND** owner commands сгруппированы по operational purpose

### Requirement: Ответы владельцу укладываются в лимиты Telegram без поломки форматирования

Длинные владельческие ответы MUST укладываться в лимит сообщения
Telegram (`/whois`, `/export` и любой ответ, способный его превысить) —
через ограничение объёма (потолок строк) или разбиение. Разбиение MUST
происходить только по границам строк и MUST NOT разрывать HTML-тег или
сущность; форматирование MUST оставаться сбалансированным в каждом
фрагменте, поэтому теги держатся в пределах одной строки. CSV `/export`
отправляется как plain (без `parse_mode`), поэтому его построчное
разбиение безопасно.

#### Scenario: Длинный ответ разбивается без поломки тегов

- **WHEN** владельческий ответ превышает лимит сообщения
- **THEN** он разбивается по границам строк
- **AND** ни один фрагмент не содержит незакрытого тега или сущности

#### Scenario: CSV export отправляется без parse_mode

- **WHEN** владелец запрашивает `/export`
- **THEN** CSV-чанки отправляются как plain без `parse_mode`
