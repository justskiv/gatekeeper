## MODIFIED Requirements

### Requirement: Запрос доступа ограничен по частоте и повторяем кнопкой

Бот MUST направлять retry controls, включая inline-кнопку «Проверить
ещё раз», в тот же grant-access flow, что и `/start`. Каждый запрос
доступа — `/start`, некомандный DM или retry-нажатие — MUST получать
ответ: per-user throttle, схлопывавший повторные тапы в один ответ,
снят, поэтому второй `/start` или повторное нажатие retry больше не
уходят в тишину и не возвращают фейковый сбой.

Idempotency-маркер access-ответа MUST быть per-request (а не
time-bucket), чтобы каждый тап давал свой durable reply. Action-маркеры
grant/invite свою идемпотентность сохраняют. Защита от нагрузки на
повторные проверки (in-memory decision cache) отложена и не входит в
этот контракт; до неё каждое нажатие запускает живую проверку.

#### Scenario: Кнопка повторной проверки использует start flow
- **WHEN** пользователь нажимает кнопку повторной проверки после
  admission-сообщения
- **THEN** бот запускает тот же grant-access flow, что и для `/start`

#### Scenario: Повторное нажатие retry снова отвечает пользователю
- **WHEN** пользователь нажимает retry повторно подряд
- **THEN** каждое нажатие запускает живую проверку доступа
- **AND** каждое нажатие получает durable ответ, а не подавляется
  per-user throttle или общим time-bucket idempotency key

### Requirement: setMyCommands registers user and admin command scopes

При старте бот MUST регистрировать меню команд через `setMyCommands`:
пользовательские команды — глобально, админские — scoped на
`OWNER_TG_IDS`. Пользовательские команды MUST включать `/start`,
`/help`, `/status`, `/boosty` и `/tribute`. Admin scope MUST включать
`/here`, `/whois`, `/grant`, `/revoke`, `/ban`, `/unban`, `/sync`,
`/stats`, `/alerts`, `/export`, `/chats` и `/help_admin`. Ошибка
`setMyCommands` — **best-effort, не фатальна**: логируется на уровне
`warn` и не прерывает старт (меню — лишь подсказка, сами команды
работают и без него).

#### Scenario: Command scopes регистрируются при старте
- **WHEN** бот стартует
- **THEN** пользовательские команды зарегистрированы глобально
- **AND** админские команды зарегистрированы scoped на `OWNER_TG_IDS`

#### Scenario: Подписочные команды входят в user scope
- **WHEN** бот регистрирует user command scope
- **THEN** `/boosty` и `/tribute` входят в список user commands рядом с
  `/start`, `/help` и `/status`

#### Scenario: Ops commands входят в admin scope
- **WHEN** бот регистрирует admin command scope
- **THEN** `/stats`, `/alerts`, `/export`, `/chats` и `/help_admin`
  входят в список admin commands

#### Scenario: Ошибка регистрации команд не прерывает старт
- **WHEN** `setMyCommands` возвращает ошибку при старте
- **THEN** ошибка логируется на уровне `warn` и старт продолжается

## ADDED Requirements

### Requirement: `/boosty` и `/tribute` показывают страницы оформления подписки

Бот MUST поддерживать пользовательские команды `/boosty` и `/tribute`
как in-bot страницы оформления подписки. Каждая команда MUST
регистрировать пользователя (`rememberPrivateUser`/`ensureUser`) как
обычный приватный вход и отвечать durable сообщением со ссылками на
оформление соответствующей подписки и требованием членства/канала,
которое бот сверяет. Текст обеих страниц MUST браться из пакета
`messages`.

Эти команды — отдельный вход в подписку вместо `/start` deep-link с
скрытым payload: Telegram схлопывает deep-link до голого `/start`,
который перехватывается access-флоу, поэтому страница подписки иначе не
показывается. Видимая команда (`/boosty` или `/tribute`) MUST оставаться
честной — без скрытого payload.

#### Scenario: /boosty показывает страницу Boosty
- **WHEN** пользователь отправляет `/boosty` в личку
- **THEN** бот регистрирует пользователя и отвечает страницей Boosty со
  ссылкой на оформление подписки
- **AND** текст берётся из пакета `messages`

#### Scenario: /tribute показывает страницу Tribute
- **WHEN** пользователь отправляет `/tribute` в личку
- **THEN** бот регистрирует пользователя и отвечает страницей Tribute с
  вариантами оплаты
- **AND** текст берётся из пакета `messages`
