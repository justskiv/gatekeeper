## MODIFIED Requirements

### Requirement: Запрос доступа выдаёт pending только eligible-пользователям

Grant-access flow MUST обрабатывать `/start`, некомандный DM и retry
callback как запрос доступа. Handler MUST обеспечить строку `users`,
выставить `dm_state='open'`, применить hard-ban до проверки источников
и выполнить живой `effectiveStatus` вне `handleTx`. При `active` handler
MUST вызвать `recomputeAccess`, определить club resources, где grant
отсутствует или не равен `joined`, и в короткой handler-транзакции
записать `access_grants.state='pending'`, audit и нужные outbox actions.

При `shared_join_request` handler MUST использовать активные shared
ссылки из `invite_links` и ставить `send_dm` с финальным сообщением об
активном доступе. При `personal_join_request` или `direct` handler MUST
ставить `send_invite`, а немедленный ответ пользователю MUST быть
финальным сообщением об ожидании или подготовке invite. При `inactive`
handler MUST отправить финальное сообщение об отсутствии активной
подписки и не создавать grants; это сообщение MUST нести retry-кнопку
«Проверить ещё раз», чтобы пользователь после оплаты мог перепроверить
доступ. При `unknown` handler MUST использовать только свежую
active-подписку из БД как fallback в пределах
`ADMISSION_FALLBACK_MAX_AGE`; без такого fallback он MUST отправить
сообщение о временной проблеме проверки и создать `admin_alert`.

Каждый запрос доступа MUST получать ответ: idempotency-маркер
user-facing reply MUST быть per-request (не 30s time-bucket), чтобы
повторный `/start` или повторное нажатие retry снова отвечали
пользователю, а не подавлялись общим ключом. Когда запрос пришёл
retry-callback'ом и несёт edit-target (`chat_id` и `message_id`
исходного сообщения), результат MUST доставляться правкой того же
сообщения на месте (`edit_message`), а не новым DM; при отсутствии или
недоступности edit-target ответ MUST падать обратно на свежий DM.
Durable-побочные эффекты выдачи доступа (grants, invite actions) MUST
оставаться неизменными — меняется только форма доставки user-facing
reply.

Повторный запрос MUST быть идемпотентным по durable-эффектам: он не
создаёт дубли grants, не плодит одинаковые invite actions и сообщает
уже joined resources как уже доступные. `recomputeAccess` MUST
оставаться без `inactive`-ветки отзыва, soft-kick и revocation actions
до Фазы 06.

Все пользовательские admission-сообщения MUST быть продуктовыми. Они
MUST NOT раскрывать verdict'ы источников, fallback markers, alert
kinds, внутреннюю механику invite resolution, сырые chat IDs или сырые
ошибки. Временные проблемы источников MUST описываться как временная
проблема проверки и MUST давать путь повтора, когда он существует.

#### Scenario: Активный пользователь получает shared join-request ссылки
- **WHEN** пользователь с живым статусом `active` отправляет `/start` в
  `shared_join_request` режиме
- **THEN** для club chat и club channel, где пользователь ещё не
  `joined`, создаются или обновляются `pending` grants
- **AND** в outbox ставится `send_dm` с финальным сообщением об
  активном доступе и ссылками из активных shared rows `invite_links`
- **AND** текст не раскрывает внутренние детали admission

#### Scenario: Неактивный пользователь получает сообщение об отсутствии подписки
- **WHEN** пользователь с живым статусом `inactive` отправляет `/start`
- **THEN** бот ставит `send_dm` с финальным сообщением об отсутствии
  активной подписки
- **AND** сообщение несёт retry-кнопку «Проверить ещё раз» как
  следующее действие
- **AND** `access_grants` и invite actions для пользователя не создаются

#### Scenario: Unknown-статус использует свежую active-подписку
- **WHEN** live-проверка вернула `unknown`, но в БД есть свежая active
  подписка пользователя
- **THEN** grant-access flow продолжает обработку как `active`
- **AND** audit сохраняет, что выдача была основана на fallback
- **AND** пользовательский текст не раскрывает fallback как внутреннюю
  причину

#### Scenario: Unknown-статус без fallback просит повторить позже
- **WHEN** live-проверка вернула `unknown` и свежей active-подписки в БД
  нет
- **THEN** бот ставит `send_dm` с финальным сообщением о временной
  проблеме проверки
- **AND** сообщение содержит путь повтора
- **AND** создаётся `admin_alert` о недоступности источника
- **AND** pending grants не создаются

#### Scenario: Забаненный пользователь не может запросить доступ
- **WHEN** пользователь с `users.banned=1` отправляет `/start`
- **THEN** бот ставит `send_dm` с финальным сообщением о блокировке
  аккаунта
- **AND** источники подписки не дают пользователю доступ
- **AND** сообщение не раскрывает admin ban reason, если продуктовый
  copy явно не разрешает безопасную публичную причину

#### Scenario: Повторный start идемпотентен
- **WHEN** eligible пользователь повторяет `/start` в пределах уже
  созданных pending grants или active invite actions
- **THEN** в `access_grants` остаётся одна строка на `(tg_id, resource)`
- **AND** outbox idempotency keys не позволяют создать дубли действия

#### Scenario: recomputeAccess не отзывает inactive-доступ
- **WHEN** пользователь стал `inactive` до Фазы 06
- **THEN** `recomputeAccess` не ставит `soft_kick` или revoke actions
- **AND** автоматический отзыв остаётся вне этой фазы

#### Scenario: Retry-callback правит исходное сообщение на месте
- **WHEN** запрос доступа пришёл retry-нажатием с edit-target исходного
  сообщения
- **THEN** результат доставляется правкой того же сообщения
  (`edit_message`), а не новым DM
- **AND** durable grants и invite actions создаются как обычно

#### Scenario: Retry без доступного edit-target отвечает новым DM
- **WHEN** retry-запрос не несёт доступный edit-target (сообщение
  недоступно или слишком старое)
- **THEN** результат доставляется свежим `send_dm`

### Requirement: Join-request одобряется только после живой проверки доступа

`chat_join_request` MUST быть admission-шлагбаумом для managed club
resources. Handler MUST сопоставить `chat.id` с resource, обеспечить
пользователя, проверить hard-ban, разрешить invite link по настроенному
mode и выполнить живой `effectiveStatus` вне `handleTx`. Для `unknown`
handler MUST повторить живую проверку согласно
`ADMISSION_JOIN_REQUEST_RETRIES` перед решением.

Только финальный `active` статус MUST поставить `approve_join`,
перевести grant в `joined` с `admitted_by='bot'`, пометить personal
invite как `used`, записать `audit_log(join_approved)` и поставить
финальное сообщение о выдаче доступа. Финальный `inactive`, hard-ban
или unresolved personal misuse MUST поставить `decline_join`, записать
audit и поставить DM пользователю. Устойчивый `unknown` после ретраев
MUST fail-closed: `decline_join` и финальное сообщение о временной
проблеме проверки.

Decline по неразрешённой invite-ссылке (`invite_unresolved`) MUST нести
отдельное сообщение о нераспознанной ссылке, а не сообщение о временной
проблеме проверки: это проблема приглашения, а не подписки, и copy MUST
направлять пользователя запросить доступ заново через `/start` за
свежими ссылками. Если такой decline приходит пользователю со свежей
**активной** подпиской, handler MUST поднять
`admin_alert(kind='join_declined_active_sub', severity='warning')`:
развернуть eligible-пользователя — аномалия, обычно означающая
сломанную invite-ссылку или логический баг. Обычный `inactive`-decline
(нет подписки) ожидаем и MUST NOT поднимать этот alert.

`user_chat_id` из Telegram MUST использоваться для DM-ответов, когда он
доступен, чтобы пользователь, который раньше не запускал бота, всё
равно получил admission result. `approve_join` idempotency key MUST
включать resource, tg_id и дату/идентификатор join-request (§13.2),
чтобы новая заявка после выхода считалась новым действием.

Пользовательские сообщения join-request MUST NOT раскрывать invite
resolution status, resource chat IDs, сырые source verdicts или
внутренние причины decline.

#### Scenario: Активная join-request одобряется
- **WHEN** active пользователь создаёт join-request в managed resource
- **THEN** handler ставит `approve_join` для этого resource и tg_id
- **AND** grant становится `joined` с `admitted_by='bot'`
- **AND** пользователю ставится DM с финальным сообщением о выдаче
  доступа

#### Scenario: Неактивная join-request отклоняется
- **WHEN** пользователь без активного основания создаёт join-request
- **THEN** handler ставит `decline_join`
- **AND** grant не становится `joined`
- **AND** пользователю ставится DM с финальным сообщением об отсутствии
  активной подписки
- **AND** сообщение не раскрывает внутреннюю причину decline

#### Scenario: Устойчивый unknown отклоняется
- **WHEN** live-проверка join-request остаётся `unknown` после ретраев
- **THEN** handler ставит `decline_join`
- **AND** пользователю ставится DM с финальным сообщением о временной
  проблеме проверки

#### Scenario: Personal invite, использованный другим пользователем, отклоняется
- **WHEN** `personal_join_request` ссылка принадлежит tg_id A, но
  join-request пришёл от tg_id B
- **THEN** handler ставит `decline_join` для tg_id B
- **AND** invite row помечается `used_by_other` с `attempted_by=B`
- **AND** пишется audit о misuse personal-ссылки
- **AND** пользователь получает безопасное продуктовое сообщение о
  misuse personal-ссылки

#### Scenario: Отсутствующий invite_link не блокирует active shared request
- **WHEN** Telegram прислал join-request без поля `invite_link` в
  `shared_join_request` режиме
- **THEN** active пользователь не отклоняется только из-за пустого поля
- **AND** решение всё равно основано на resource и live status

#### Scenario: Повторная заявка после выхода получает новый approve key
- **WHEN** пользователь вышел из managed resource и позже подал новую
  join-request
- **THEN** `approve_join` получает idempotency key с новой датой или
  идентификатором заявки
- **AND** новое действие не подавляется старым approve-действием

#### Scenario: Нераспознанная invite-ссылка отклоняется отдельным сообщением
- **WHEN** join-request отклоняется по `invite_unresolved`
- **THEN** пользователю ставится DM с сообщением о нераспознанной
  ссылке, направляющим запросить доступ заново через `/start`
- **AND** это не сообщение о временной проблеме проверки

#### Scenario: Decline активного подписчика поднимает alert
- **WHEN** `invite_unresolved` decline приходит пользователю со свежей
  активной подпиской
- **THEN** создаётся `admin_alert(kind='join_declined_active_sub',
  severity='warning')`
- **AND** обычный `inactive`-decline этот alert не поднимает
