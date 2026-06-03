## ADDED Requirements

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
