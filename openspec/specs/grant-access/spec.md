# grant-access Specification

## Purpose

Описывает pull-модель выдачи доступа в клубные ресурсы через `/start`,
invite-ссылки, `chat_join_request` approval и фиксацию membership в
`access_grants`.
## Requirements
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

### Requirement: Club membership updates поддерживают access grants

`chat_member` для клубного чата и канала MUST обновлять
`access_grants` как фактический сигнал членства. Если пользователь
становится участником, handler MUST выставить `state='joined'`,
`joined_at=now` и `admitted_by='bot'`, когда update пришёл через
join request или совпадает с активной direct invite, созданной для
этого пользователя; иначе handler MUST выставить
`admitted_by='external'`.

External joins MUST NOT приводить к автоматическому кику. Они MUST
создавать `audit_log(external_join_detected)` и `admin_alert` с
informational-severity. Если пользователь вступает по direct-инвайту,
handler MUST повторно проверить живой статус вне `handleTx` и поставить
`soft_kick`, когда статус не `active`.

Если пользователь покидает managed resource, handler MUST перевести
grant в `left` через `updated_at`, если текущий grant ещё не `revoked`,
и MUST записать `audit_log(member_left)`. Уже `revoked` grant MUST
сохранять состояние `revoked` и не перетираться выходом из чата.

#### Scenario: Вступление через join request записывается как bot-admitted
- **WHEN** club `chat_member` показывает, что пользователь вступил через
  join request
- **THEN** grant для этого resource сохраняется как `joined`
- **AND** `admitted_by` равен `bot`

#### Scenario: External join создаёт alert и не кикает
- **WHEN** пользователь становится участником клубного ресурса без
  подтверждённого ботом admission
- **THEN** grant сохраняется как `joined` с `admitted_by='external'`
- **AND** `admin_alert(kind='external_join')` создан
- **AND** `soft_kick` action не ставится только из-за external join

#### Scenario: Direct join без active status компенсируется
- **WHEN** пользователь вступает по активному direct-инвайту, а живой
  статус не `active`
- **THEN** grant фиксирует наблюдаемое вступление
- **AND** `soft_kick` action ставится для managed resource

#### Scenario: Выход участника помечает grant как left
- **WHEN** club `chat_member` показывает, что joined пользователь покинул
  resource
- **THEN** grant state становится `left`
- **AND** audit фиксирует `member_left`

#### Scenario: Revoked grant не перетирается выходом
- **WHEN** club `chat_member` показывает выход пользователя, чей grant
  уже находится в `revoked`
- **THEN** grant остаётся `revoked`
- **AND** обработчик не записывает `left` поверх отзыва

### Requirement: Admission emits access-granted only when it establishes eligibility

`access_granted` MUST have a single meaning across the system: the
user's effective eligibility transitioned to active. It MUST be emitted
at most once per active access episode, and engine and admission MUST
share a stable per-episode idempotency marker so the two emit points
never produce a duplicate (see `status-core` for the engine transition).

Admission MUST emit `access_granted` only when admission itself
establishes active eligibility that was not already logged for the
current episode — typically the fallback path where live status is
`unknown` but a fresh active subscription allows admission, or any
admission that grants the first active access without a prior engine
transition event. When the user is already eligible (the engine already
logged `access_granted` for this episode) and `/start` merely issues
invites or creates `pending` grants, admission MUST NOT emit another
`access_granted`; the resulting join later produces resource membership
events instead. User-facing replies remain unchanged.

When emitted, the event MUST include tg_id, safe user label, resource
list, invite mode, admission method `bot_link` and all active source
reasons from the live `AccessDecision` or fallback active subscriptions.

#### Scenario: First admission for an active user logs access-granted once

- **WHEN** an active user is admitted and no `access_granted` was yet
  logged for this episode
- **THEN** exactly one `access_granted` operator event is enqueued in the
  same transaction
- **AND** the event includes all active source reasons and `INVITE_MODE`
- **AND** no full invite URL is included

#### Scenario: Already-logged eligibility does not re-emit on start

- **WHEN** the engine already logged `access_granted` for the active
  episode and `/start` only issues invites or creates pending grants
- **THEN** admission does NOT emit another `access_granted`
- **AND** the subsequent join emits resource membership events instead

#### Scenario: Fallback active start identifies fallback source

- **WHEN** live status is `unknown`, but fresh active subscription
  fallback allows admission and no prior transition was logged
- **THEN** admission emits one `access_granted` marked fallback-based
- **AND** it lists the active subscription platform(s) known in storage

#### Scenario: Repeat start does not duplicate access-granted

- **WHEN** a user repeats `/start` while the same pending grants or
  invite actions already exist
- **THEN** no duplicate `access_granted` operator event is enqueued
- **AND** the user still receives the normal idempotent admission reply

### Requirement: Join-request decisions emit operator admission events

Grant-access flow MUST emit operator admission events when a managed
`chat_join_request` is approved and the user's grant is transitioned to
`joined`. The event MUST record resource, admission method `bot_link`,
source reasons and invite mode. If this approval only completes an
already logged pending access request, the event MUST be a resource
membership event, not a duplicate `access_granted` event.

Declined join requests MUST NOT emit `access_lost`, because no access
was granted. Declines MAY continue to create alerts and audit rows
according to existing grant-access requirements.

#### Scenario: Approved join logs bot admission

- **WHEN** an active user is approved through a managed join request
- **THEN** a resource membership operator event is enqueued
- **AND** the event records admission method `bot_link`
- **AND** source reasons come from the same decision used to approve

#### Scenario: Declined join does not log access loss

- **WHEN** a join request is declined because the user is inactive
- **THEN** no `access_lost` operator event is emitted
- **AND** existing audit and user DM behavior remain unchanged

### Requirement: Club membership updates emit operator membership events

`chat_member` updates for managed club resources MUST emit operator
membership events when observed membership changes. For the club chat,
joins MUST emit `club_chat_joined` and leaves MUST emit
`club_chat_left`. For the club channel, joins MUST emit
`club_channel_subscribed` and leaves MUST emit
`club_channel_unsubscribed`.

Joined events MUST include admission method. The method MUST be
`bot_link` when the handler has bot admission evidence through
join-request, pending bot grant or accepted direct invite. The method
MUST be `external` when no bot evidence exists. If the Telegram update
exposes an actor for an external membership change, the event MUST
include a safe actor label; otherwise it MUST preserve `external`
without guessing an admin.

Joined/subscribed events MUST also include the joining user's active
access source reasons (`boosty`, `tribute`, `manual`) so the operator
sees *why* the user has access at the moment they entered the resource,
not only how they entered. Reasons MUST come from already-available data
— stored active `subscriptions` or the last `AccessDecision` — and MUST
NOT require a network probe inside the handler. When no active source is
known for the joining user (for example an external join by someone with
no eligibility), the event MUST state that the access source is unknown
rather than guess one.

Leave/unsubscribe events MUST be emitted even when the grant is already
`revoked`; the grant state MUST still not be overwritten by the leave.

#### Scenario: Bot-admitted chat join is logged

- **WHEN** a club chat `chat_member` update shows a user joined through
  bot admission evidence
- **THEN** `club_chat_joined` is enqueued
- **AND** the event records admission method `bot_link`

#### Scenario: Membership join lists the access source reasons

- **WHEN** a user with an active Boosty subscription joins the club chat
- **THEN** `club_chat_joined` includes access source reason `boosty`
- **AND** when the user is also active on Tribute, both reasons are listed
- **AND** an external join by a non-eligible user states the source is
  unknown instead of guessing

#### Scenario: External channel subscription is logged

- **WHEN** a club channel `chat_member` update shows a user subscribed
  without bot admission evidence
- **THEN** `club_channel_subscribed` is enqueued
- **AND** the event records admission method `external`
- **AND** the existing `external_join` audit/alert behavior remains

#### Scenario: Chat leave is logged without overwriting revoked grant

- **WHEN** a club chat `chat_member` update shows a user left and the
  grant is already `revoked`
- **THEN** `club_chat_left` is enqueued
- **AND** the grant remains `revoked`

#### Scenario: Channel unsubscribe is logged

- **WHEN** a club channel `chat_member` update shows a user is no longer
  a member
- **THEN** `club_channel_unsubscribed` is enqueued
- **AND** the event includes the best available reason from grant state
  or observed Telegram membership change

