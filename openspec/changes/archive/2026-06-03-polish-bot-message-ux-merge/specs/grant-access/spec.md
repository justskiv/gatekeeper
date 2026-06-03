## MODIFIED Requirements

### Requirement: Запрос доступа выдаёт pending только eligible-пользователям

Grant-access flow MUST обрабатывать `/start`, некомандный DM и retry
callback как запрос доступа. Handler MUST обеспечить строку `users`,
выставить `dm_state='open'`, применить hard-ban до проверки источников
и выполнить живой `effectiveStatus` вне `handleTx`. При `active`
handler MUST вызвать `recomputeAccess`, определить club resources, где
grant отсутствует или не равен `joined`, и в короткой
handler-транзакции записать `access_grants.state='pending'`, audit и
нужные outbox actions.

При `shared_join_request` handler MUST использовать активные shared
ссылки из `invite_links` и ставить `send_dm` с финальным active
message. При `personal_join_request` или `direct` handler MUST ставить
`send_invite`, а немедленный ответ пользователю MUST быть финальным
invite-pending/direct-preparing message. При `inactive` handler MUST
отправить финальный no-sub message и не создавать grants. При
`unknown` handler MUST использовать только свежую active-подписку из БД
как fallback в пределах `ADMISSION_FALLBACK_MAX_AGE`; без такого
fallback он MUST отправить temporary-check message и создать
`admin_alert`.

Повторный запрос MUST быть идемпотентным: он не создаёт дубли grants,
не плодит одинаковые invite actions и сообщает уже joined resources как
уже доступные. `recomputeAccess` MUST оставаться без `inactive`-ветки
отзыва, soft-kick и revocation actions до Фазы 06.

All user-facing admission messages MUST be product-facing. They MUST
NOT expose source verdicts, fallback markers, alert kinds, invite
resolution internals, raw chat IDs or raw errors. Temporary source
issues MUST be described as a temporary check issue and MUST give a
retry path when one exists.

#### Scenario: Активный пользователь получает shared join-request ссылки

- **WHEN** пользователь с живым статусом `active` отправляет `/start` в
  `shared_join_request` режиме
- **THEN** для club chat и club channel, где пользователь ещё не
  `joined`, создаются или обновляются `pending` grants
- **AND** в outbox ставится `send_dm` с final active message и ссылками
  из активных shared rows `invite_links`
- **AND** текст не раскрывает internal admission details

#### Scenario: Неактивный пользователь получает сообщение об отсутствии подписки

- **WHEN** пользователь с живым статусом `inactive` отправляет `/start`
- **THEN** бот ставит `send_dm` с final no-sub message
- **AND** сообщение даёт следующее действие для подписки или повторной
  проверки
- **AND** `access_grants` и invite actions для пользователя не
  создаются

#### Scenario: Unknown-статус использует свежую active-подписку

- **WHEN** live-проверка вернула `unknown`, но в БД есть свежая active
  подписка пользователя
- **THEN** grant-access flow продолжает обработку как `active`
- **AND** audit сохраняет, что выдача была основана на fallback
- **AND** user-facing text не раскрывает fallback как internal reason

#### Scenario: Unknown-статус без fallback просит повторить позже

- **WHEN** live-проверка вернула `unknown` и свежей active-подписки в БД
  нет
- **THEN** бот ставит `send_dm` с final temporary-check message
- **AND** сообщение содержит retry path
- **AND** создаётся `admin_alert` о недоступности источника
- **AND** pending grants не создаются

#### Scenario: Забаненный пользователь не может запросить доступ

- **WHEN** пользователь с `users.banned=1` отправляет `/start`
- **THEN** бот ставит `send_dm` с final blocked-account message
- **AND** источники подписки не дают пользователю доступ
- **AND** сообщение не раскрывает admin ban reason unless product copy
  explicitly allows a safe public reason

#### Scenario: Повторный start идемпотентен

- **WHEN** eligible пользователь повторяет `/start` в пределах уже
  созданных pending grants или active invite actions
- **THEN** в `access_grants` остаётся одна строка на `(tg_id, resource)`
- **AND** outbox idempotency keys не позволяют создать дубли действия

#### Scenario: recomputeAccess не отзывает inactive-доступ

- **WHEN** пользователь стал `inactive` до Фазы 06
- **THEN** `recomputeAccess` не ставит `soft_kick` или revoke actions
- **AND** автоматический отзыв остаётся вне этой фазы

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
final granted message. Финальный `inactive`, hard-ban или unresolved
personal misuse MUST поставить `decline_join`, записать audit и
поставить DM пользователю. Устойчивый `unknown` после ретраев MUST
fail-closed: `decline_join` и final temporary-check message.

`user_chat_id` из Telegram MUST использоваться для DM-ответов, когда он
доступен, чтобы пользователь, который раньше не запускал бота, всё
равно получил admission result. `approve_join` idempotency key MUST
включать resource, tg_id и дату/идентификатор join-request (§13.2),
чтобы новая заявка после выхода считалась новым действием.

Join-request user messages MUST NOT expose invite resolution status,
resource chat IDs, raw source verdicts or internal decline reasons.

#### Scenario: Активная join-request одобряется

- **WHEN** active пользователь создаёт join-request в managed resource
- **THEN** handler ставит `approve_join` для этого resource и tg_id
- **AND** grant становится `joined` с `admitted_by='bot'`
- **AND** пользователю ставится DM с final granted message

#### Scenario: Неактивная join-request отклоняется

- **WHEN** пользователь без активного основания создаёт join-request
- **THEN** handler ставит `decline_join`
- **AND** grant не становится `joined`
- **AND** пользователю ставится DM с final no-sub message
- **AND** сообщение не раскрывает internal decline reason

#### Scenario: Устойчивый unknown отклоняется

- **WHEN** live-проверка join-request остаётся `unknown` после ретраев
- **THEN** handler ставит `decline_join`
- **AND** пользователю ставится DM с final temporary-check message

#### Scenario: Personal invite, использованный другим пользователем, отклоняется

- **WHEN** `personal_join_request` ссылка принадлежит tg_id A, но
  join-request пришёл от tg_id B
- **THEN** handler ставит `decline_join` для tg_id B
- **AND** invite row помечается `used_by_other` с `attempted_by=B`
- **AND** пишется audit о misuse personal-ссылки
- **AND** пользователь получает safe product-facing misuse message

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
