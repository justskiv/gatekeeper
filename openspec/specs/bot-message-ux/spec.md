# bot-message-ux Specification

## Purpose

Описывает финальный продуктовый голос сообщений Telegram-бота,
privacy-границы между пользователем и владельцем, правила emoji,
summary-first diagnostics и тестовые guardrails для копирайта.
## Requirements
### Requirement: Сообщения бота используют финальный продуктовый голос

Все сообщения бота SHALL использовать единый финальный продуктовый
голос. Пользовательские сообщения MUST быть спокойными, краткими и
ориентированными на действие. Сообщения владельца/админа MUST быть
операционными, структурированными и явно указывать влияние и следующее
действие.

Каждое пользовательское сообщение о статусе, успехе, отказе, повторе,
предупреждении и отзыве MUST следовать порядку: статус, человеческая
причина, следующий шаг. Сообщение MUST NOT создавать тупик, когда
осмысленный следующий шаг существует. Если система не может доказать
негативный факт, копирайт MUST использовать нейтральную формулировку
(«активная подписка не найдена») вместо обвиняющей («подписка не
оплачена»).

Эмодзи MAY использоваться только как статусный маркер и только когда он
улучшает читаемость. Общий allowlist фиксирован: `✅` (активно/успех),
`⏳` (ожидание/подготовка), `🚫` (нет доступа/отозвано/бан), `❔`
(неизвестно/проверяется), `⚠️` (внимание оператора) и `ℹ️` (info).
Пользовательские сообщения MAY использовать только `✅`/`⏳`/`🚫`/`❔`;
`⚠️` и `ℹ️` — только для владельца/админа. На одно пользовательское
сообщение и обычный owner/admin-ответ MUST приходиться не более одного
эмодзи. Owner/admin-списки и дашборды MAY использовать один статусный
маркер на строку объекта, когда строка представляет отдельную тревогу
или операционное состояние и маркер улучшает сканирование. Сообщения
MUST NOT украшать эмодзи каждую строку, ставить эмодзи рядом с
техническими идентификаторами, в середине предложения, в help/usage или
в строках данных. `⚠️` MUST NOT использоваться в пользовательской
аудитории как маркер неопределённости.

Даты и время в человекочитаемом тексте MUST использовать один
консистентный русский человекочитаемый формат. Внутренние
RFC3339-таймстампы MAY появляться только в диагностике для
владельца/админа.

#### Scenario: Пользовательское сообщение даёт одно ясное действие

- **WHEN** бот отправляет пользовательское сообщение о статусе,
  admission, предупреждении или отзыве
- **THEN** сообщение называет состояние доступа человеческими словами
- **AND** даёт человекочитаемую причину, когда её безопасно назвать
- **AND** даёт следующее действие, когда оно доступно
- **AND** не включает посторонние diagnostics

#### Scenario: Временная ошибка не обвиняет пользователя

- **WHEN** источник, Telegram или внутренняя проверка временно
  недоступны
- **THEN** пользовательское сообщение описывает временный сбой проверки
- **AND** не утверждает, что пользователь не оплатил или использовал не
  тот аккаунт, пока это не подтверждено
- **AND** даёт тайминг повтора, кнопку повтора или другой следующий шаг

#### Scenario: Эмодзи ограничен

- **WHEN** сообщение использует эмодзи-маркер статуса
- **THEN** маркер встречается не более одного раза, в ведущей строке
  обычного сообщения
- **AND** списки владельца/админа MAY ставить не более одного маркера на
  строку объекта, когда объект является отдельной тревогой или
  операционным состоянием
- **AND** строки технических данных не несут эмодзи-украшения
- **AND** `⚠️` не появляется в пользовательской аудитории

### Requirement: Пользовательские сообщения скрывают внутреннюю диагностику доступа

Пользовательские сообщения MUST NOT раскрывать внутреннюю механику
решения о доступе. Они MUST NOT содержать сырые `AccessDecision.Reasons`,
enum-значения вердиктов источников, `system`, `whitelist`, формулировки
локальной БД, сырые provider ID, сырые chat ID, outbox/action ID,
alert kind, сырые ошибки Telegram или внутренние idempotency-маркеры.

Пользовательский копирайт MUST переводить внутренние состояния в
продуктовые термины: доступ активен, активная подписка не найдена,
временный сбой проверки, доступ сохранён, предупреждение перед
остановкой доступа, доступ остановлен, аккаунт заблокирован или уже
вступил.

Сообщения владельца/админа MAY содержать diagnostics, но только в
отдельной diagnostics-секции после человеческого summary.

#### Scenario: Пользовательский статус не содержит reasons

- **WHEN** обычный пользователь запрашивает `/status`
- **THEN** ответ не содержит `AccessDecision.Reasons`
- **AND** ответ не упоминает `whitelist`, `system`, локальную БД, сырой
  `chat_id` или enum-имена вердиктов источников

#### Scenario: Admin diagnostics остаются разрешены

- **WHEN** владелец запрашивает `/whois`
- **THEN** ответ MAY содержать decision reasons и технические ID
- **AND** эти детали сгруппированы после основного summary

### Requirement: Пользовательские шаблоны покрывают финальные состояния доступа

Пакет `messages` SHALL предоставлять финальные пользовательские шаблоны
для:

- приветствие `/start` и вход запроса доступа;
- `/help` только с пользовательскими командами и гайдом по подписке;
- страницы оформления подписки `/boosty` и `/tribute` со ссылками на
  оформление и требованием членства/канала, которое сверяет бот;
- `/status` для состояний active, inactive, unknown и blocked;
- active shared links, ожидание personal invite и подготовка direct
  invite;
- join request approved, declined по отсутствию подписки, declined по
  временному сбою, declined по нераспознанной invite-ссылке и misuse
  personal-ссылки;
- retry/rate-limit временный сбой проверки;
- access kept, предупреждение об истечении, notify-only уведомление и
  отозванный доступ.

Вход в оформление подписки MUST вести через tappable in-bot команды
`/boosty` и `/tribute`, а не через `/start` deep-link с скрытым payload:
Telegram схлопывает deep-link до голого `/start`, который перехватывает
access-флоу, поэтому страница подписки иначе не показывается. Шаблоны,
направляющие пользователя оформить подписку (no-sub, `/status` без
активной подписки, `/help`), MUST указывать на эти команды как на вход.

Каждый шаблон MUST быть безопасен для рендера с динамическими
платформами, датами, метками ресурсов и ссылками. Шаблоны MUST избегать
обвинения пользователя в проблемах доступности провайдера или Telegram.

#### Scenario: Временный сбой проверки fail-open в копирайте

- **WHEN** проверка источника временно недоступна
- **THEN** пользовательское сообщение говорит, что проверка временно
  недоступна
- **AND** не говорит, что доступ снят, если отзыв ещё не завершён

#### Scenario: Help остаётся только пользовательским

- **WHEN** обычный пользователь запрашивает `/help`
- **THEN** ответ перечисляет только пользовательские команды и гайд по
  подписке
- **AND** не упоминает owner/admin-команды

#### Scenario: Вход в подписку идёт через команды, а не deep-link

- **WHEN** пользователю без активной подписки показывают, как оформить
  доступ
- **THEN** копирайт направляет на команды `/boosty` или `/tribute`
- **AND** не полагается на `/start` deep-link со скрытым payload

### Requirement: Сообщения владельца начинают с резюме и отделяют диагностику

Сообщения владельца/админа SHALL быть структурированы как:

1. `Summary`: короткое человеческое резюме;
2. `Impact`: затронутые пользователи, ресурсы, провайдеры или счётчики;
3. `Action`: рекомендуемое действие или «действие не требуется»;
4. `Diagnostics`: сырые ID, enum-подобные значения, retry-состояние или
   сырые детали.

Diagnostics-блок MUST опускаться, когда диагностической ценности нет.
Сырые ошибки, сырые детали тревог и сырые ID MUST NOT располагаться
перед summary.

Это требование применяется к `/whois`, `/stats`, `/alerts`, `/chats`,
`/help_admin`, подтверждениям действий, результатам действий,
уведомлениям об экспорте, discovery нового чата, chat-health тревогам и
operator alerts.

#### Scenario: Тревога начинается с impact

- **WHEN** владелец получает operator alert
- **THEN** сообщение начинается с severity и человеческого резюме
  проблемы
- **AND** включает user impact или рекомендуемое действие, когда оно
  известно
- **AND** сырые kind/detail появляются только в diagnostics

#### Scenario: Рутинное состояние не является тревогой

- **WHEN** владелец запрашивает рутинное состояние через `/stats`,
  `/chats` или `/alerts`
- **THEN** ответ оформлен как status summary или dashboard
- **AND** не использует тревожные формулировки, если действие владельца
  не требуется

#### Scenario: Whois отделяет профиль от diagnostics

- **WHEN** владелец запрашивает `/whois <tg_id>`
- **THEN** ответ сначала показывает identity, статус доступа, основание
  подписки и состояние грантов
- **AND** audit, сырые детали источников и технические ID появляются
  после основного summary

### Requirement: Тесты сообщений защищают приватность и контракт рендера

Автоматические тесты SHALL защищать финальный контракт сообщений. Тесты
MUST покрывать privacy пользовательских сообщений, группировку admin
diagnostics, HTML-экранирование, использование parse mode и
репрезентативные финальные шаблоны.

Тесты MUST включать негативные проверки, что пользовательские сообщения
не содержат внутренних слов или сырых полей: `whitelist`, `system`,
`local`, `chat.id`, `reason=`, `action_id` или сырые имена вердиктов
источников.

#### Scenario: Регрессия privacy ловится

- **WHEN** `/status` рендерится для пользователя, чьё решение содержит
  внутренние reasons
- **THEN** тесты падают, если в пользовательском тексте есть эти сырые
  reasons

#### Scenario: Динамический текст экранируется

- **WHEN** сообщение включает динамические username, reason, title,
  detail или текст ссылки с `<`, `>`, `&`, кавычками или
  Telegram-разметкой
- **THEN** тесты проверяют, что вывод безопасен для выбранного parse
  mode

### Requirement: Operator event messages use owner-facing templates

Package `messages` MUST provide templates for every operator event kind
defined by `event-log`. The templates MUST be owner-facing:
summary first, then impact, then safe diagnostics only when useful.

Operator event messages MUST use Telegram HTML renderer and the same
safe escaping rules as other formatted bot messages. They MUST be
distinct from user-facing admission/revocation messages and MUST NOT be
sent to ordinary users; delivery MUST target the dedicated event-log
group.

#### Scenario: Access-granted event has summary first

- **WHEN** an `access_granted` event is rendered
- **THEN** the first line summarizes who received access and why
- **AND** resources, admission method and safe diagnostics follow after
  the summary

#### Scenario: Membership event uses resource-specific wording

- **WHEN** a `club_channel_subscribed` event is rendered
- **THEN** the text uses channel subscription wording
- **AND** it is not phrased as joining the club chat

### Requirement: Operator event templates hide unsafe details

Operator event templates MUST NOT render full invite URLs, internal
chat IDs, raw provider payload, secrets, internal outbox IDs or raw
idempotency keys. Source reasons, admin reasons, usernames, tiers and
provider event labels MUST be escaped before rendering.

If an unsafe detail is present in the input event, the renderer MUST
omit it or replace it with a safe label such as invite mode, resource or
hash. The renderer MUST NOT attempt to redact by partial string
replacement after building HTML.

This rule governs the **rendered message text** only. The delivery
routing envelope — `payload_json.chat_id` set to `EVENT_LOG_CHAT_ID` —
is required for the Enforcer to route the message and is not rendered
content; it is allowed and is not a leak.

#### Scenario: Full invite URL is not rendered

- **WHEN** an operator event input includes an invite URL
- **THEN** the rendered message does not contain the URL
- **AND** the message can include invite mode and resource instead

#### Scenario: Raw chat ID is not leading content

- **WHEN** an operator event is rendered for a configured club resource
- **THEN** the message identifies the resource by role label
- **AND** raw chat IDs are absent from the ordinary event text

#### Scenario: Dynamic reason is escaped

- **WHEN** an admin reason is `"><b>owned</b>&`
- **THEN** the rendered operator event shows that reason as text
- **AND** Telegram HTML remains valid

### Requirement: Message tests protect operator event privacy

Automated tests MUST cover representative operator event templates and
privacy guardrails. Tests MUST fail if operator event messages leak full
invite URLs, raw chat IDs, provider payload JSON, `action_id`,
`idempotency_key`, or unescaped HTML from dynamic fields.

Tests MUST also verify that channel events and chat events use different
labels and that owner-facing messages remain formatted with
`ParseModeHTML`.

#### Scenario: Privacy regression is caught

- **WHEN** tests render an operator event with invite URL, raw chat id
  and unsafe detail
- **THEN** the rendered message omits unsafe values
- **AND** the test fails if those values appear

#### Scenario: Parse mode is preserved

- **WHEN** operator event delivery enqueues a message
- **THEN** payload includes `parse_mode="HTML"`
- **AND** Enforcer sends it as a formatted message

