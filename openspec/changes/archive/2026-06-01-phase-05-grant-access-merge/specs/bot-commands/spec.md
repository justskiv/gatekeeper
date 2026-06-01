## ADDED Requirements

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

## MODIFIED Requirements

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
- **WHEN** обычный пользователь шлёт в личку произвольный текст без
  команды
- **THEN** бот обрабатывает его как `/start`, включая grant-access flow

#### Scenario: Повторный /start идемпотентен
- **WHEN** пользователь отправляет `/start` повторно
- **THEN** существующая строка `users` обновляется
- **AND** второй `pending` grant и дубль invite actions не создаются

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
Admission-specific сообщения этой фазы (`MSG_ACTIVE`, `MSG_INVITE_SOON`,
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
(§20.3) - это упрощает будущую локализацию. К уже существующим текстам
добавляются admission-сообщения: `MSG_ACTIVE`, `MSG_INVITE_SOON`,
`MSG_GRANTED`, `MSG_TRY_LATER`, `MSG_BANNED`, `MSG_ALREADY_IN` и
вариант `MSG_ACTIVE` для `direct` без обещания approve-заявки. Текст
`MSG_ACCESS_KEPT` MUST браться из `messages`, когда его готовит
`recomputeAccess`.

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
