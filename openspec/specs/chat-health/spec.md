# chat-health Specification

## Purpose

Описывает проверки здоровья Telegram-чатов, тревоги по правам бота и
discovery незнакомых чатов для настройки оператором.
## Requirements
### Requirement: Startup verifies the bot is an admin in all four configured chats

При старте бот MUST для каждого из четырёх настроенных чатов
(boosty group, tribute channel, club chat, club channel) выполнять
`getChat` и `getChatMember(chat, botID)` и проверять, что он
администратор с нужными правами. Результат пишется в стабильные ключи
`meta.health.*`: `health.boosty_group`, `health.tribute_channel`,
`health.club_chat`, `health.club_channel`. Значение каждого ключа —
строка `ok` либо `fail:<reason>` (короткая машиночитаемая причина),
чтобы Фаза 07 `/readyz` читала их без переформатирования. Эти ключи —
основа `/readyz`, их нельзя переизобретать там заново. При проблеме бот
логирует её, поднимает `admin_alert` и шлёт DM владельцу, но **не
падает**: severity — `critical` для клубного ресурса, `error` для
источника.

#### Scenario: Исправный чат записан в health
- **WHEN** бот — администратор с нужными правами в чате
- **THEN** соответствующий ключ `meta.health.*` отражает исправность

#### Scenario: Отсутствие прав деградирует без падения
- **WHEN** при старте бот не администратор в одном из четырёх чатов
- **THEN** ключ `meta.health.*` отражает проблему, поднимается
  `admin_alert` и владельцу уходит DM
- **AND** процесс продолжает работу (severity `critical` для клубного
  ресурса, `error` для источника)

### Requirement: `my_chat_member` in a private chat updates dm_state

`my_chat_member` в личке MUST обновлять `users.dm_state` в `open` или
`blocked`, отражая блокировку бота пользователем. Если строки
пользователя ещё нет (заблокировал, не запуская `/start`), обработчик
сначала создаёт минимальную запись (`ensureUser`), затем выставляет
`dm_state` — апдейт по отсутствующей строке не должен быть no-op.

#### Scenario: Бот заблокирован в личке
- **WHEN** приходит `my_chat_member` из приватного чата о блокировке бота
- **THEN** у пользователя выставляется `users.dm_state='blocked'`

#### Scenario: Блокировка от незнакомого пользователя сначала создаёт строку
- **WHEN** `my_chat_member` о блокировке приходит от пользователя без
  строки в `users`
- **THEN** обработчик создаёт минимальную запись и выставляет
  `dm_state='blocked'`

#### Scenario: Бот разблокирован в личке
- **WHEN** приходит `my_chat_member` из приватного чата о разблокировке
- **THEN** у пользователя выставляется `users.dm_state='open'`

### Requirement: `my_chat_member` in a known chat updates health and alerts on transitions

`my_chat_member` в одном из четырёх настроенных чатов MUST обновлять
`meta.health.*`. На переходе прав бот пишет `audit_log` и управляет
тревогой: при потере прав — `audit_log(bot_rights_lost)` и
`admin_alert` (`error` для источника, `critical` для клубного ресурса);
при восстановлении — `audit_log(bot_rights_restored)` и resolve
соответствующей тревоги.

#### Scenario: Права потеряны в клубном ресурсе
- **WHEN** бот теряет права в клубном чате или канале
- **THEN** `meta.health.*` обновлён, записан `audit_log(bot_rights_lost)`
  и поднят `admin_alert(severity='critical')`

#### Scenario: Права восстановлены
- **WHEN** бот восстанавливает права в ранее проблемном чате
- **THEN** записан `audit_log(bot_rights_restored)` и соответствующая
  тревога переведена в resolved

### Requirement: `my_chat_member` in an unknown chat reports the chat for discovery

Когда бота добавляют в незнакомый чат, бот MUST залогировать `chat.id`,
тип и название и отправить владельцу DM с этими данными — так владелец
узнаёт ID для конфигурации. Полные инвайт-ссылки и токены в логи не
пишутся (§23.1).

Тревога привязана к переходу вступления (`left/kicked → member/admin`):
Telegram шлёт отдельный `my_chat_member` на каждое изменение статуса бота,
и discovery-DM MUST уходить только один раз, а не на каждый последующий
апдейт (повышение до админа, правка прав).

#### Scenario: Бота добавили в незнакомый чат
- **WHEN** бота добавляют в чат, которого нет среди четырёх настроенных
- **THEN** в лог пишутся `chat.id`, тип и название
- **AND** владельцу уходит DM с этими данными

#### Scenario: Повторный `my_chat_member` в том же незнакомом чате не дублирует DM
- **WHEN** в незнакомом чате приходит ещё один `my_chat_member`, где бот уже
  присутствовал в `old_chat_member` (например `member → administrator` или
  правка прав админа)
- **THEN** DM владельцу НЕ отправляется повторно

### Requirement: Event-log chat posting ability is monitored without admin requirement

Когда задан `EVENT_LOG_CHAT_ID`, chat-health MUST мониторить способность
бота **постить** в эту группу/супергруппу, а не его админство. По
продуктовому требованию бот и админ лишь *состоят* в группе; бот не
обязан быть администратором. Ожидание для `EVENT_LOG_CHAT_ID` —
присутствие бота как участника с правом отправки сообщений (member с
правом постинга либо administrator). Plain-member без ограничений на
постинг MUST трактоваться как исправный, а не как `not_admin`.

При старте бот MUST выполнить `getChat` и `getChatMember(eventLog, botID)`
и записать результат в стабильный ключ `meta.health.event_log` со
значением `ok` либо `fail:<reason>` — той же форме, что и остальные
`health.*`-ключи, чтобы `/readyz` мог читать его единообразно. Проблема
MUST деградировать **без падения процесса**: бот логирует её, поднимает
`admin_alert` и продолжает обслуживать апдейты. Severity — `error`
(событийный фид — observability, не access-control-критичный ресурс).

Поскольку фид — observability, его ключ `meta.health.event_log` MUST NOT
блокировать `/readyz`: readiness ядра контроля доступа не зависит от
доступности фид-чата. Ключ пишется и алертится для наблюдаемости, но в
gate readiness (в отличие от четырёх source/club чатов) не входит.
Доставка алертов про `EVENT_LOG_CHAT_ID` MUST идти обычным путём в
`ADMIN_LOG_CHAT_ID`/owner DM и MUST NOT зависеть от самого event-log
чата.

`my_chat_member`-апдейты в `EVENT_LOG_CHAT_ID` MUST обновлять
`meta.health.event_log` и управлять тревогой на переходе: при потере
права постинга — `audit_log(bot_rights_lost)` и `admin_alert(error)`;
при восстановлении — `audit_log(bot_rights_restored)` и resolve тревоги.
`EVENT_LOG_CHAT_ID` при этом MUST NOT учитываться как один из четырёх
source/club чатов и MUST NOT трактоваться как managed/observed ресурс.

#### Scenario: Бот-участник с правом постинга исправен
- **WHEN** при старте бот — участник `EVENT_LOG_CHAT_ID` с правом
  отправки сообщений, но не администратор
- **THEN** `meta.health.event_log` = `ok`
- **AND** тревога не поднимается из-за отсутствия админства

#### Scenario: Нет права постинга деградирует без падения
- **WHEN** при старте бот не может постить в `EVENT_LOG_CHAT_ID`
- **THEN** `meta.health.event_log` отражает проблему
- **AND** поднимается `admin_alert(severity='error')` через
  `ADMIN_LOG_CHAT_ID`/owner DM
- **AND** процесс продолжает обслуживать входящие апдейты

#### Scenario: Потеря права постинга в рантайме поднимает тревогу
- **WHEN** `my_chat_member` сообщает, что бот потерял право постинга в
  `EVENT_LOG_CHAT_ID`
- **THEN** `meta.health.event_log` обновлён, записан
  `audit_log(bot_rights_lost)` и поднят `admin_alert(severity='error')`
- **AND** при восстановлении тревога резолвится

