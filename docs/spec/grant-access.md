# Выдача доступа

Pull-модель доступа к клубным ресурсам: запрос через `/start`, invite-ссылки, одобрение `chat_join_request` после живой проверки и фиксация членства в `access_grants`. Читаемое зеркало спеки `grant-access`.

## Запрос доступа (`/start`, DM, retry)

`/start`, любой некомандный текст в личке и callback кнопки «Проверить ещё раз» попадают в один и тот же grant-access flow как запрос доступа. Flow обеспечивает строку `users`, выставляет `dm_state='open'`, применяет hard-ban *до* проверки источников и считает живой `effectiveStatus` вне `handleTx` (сетевой опрос не держит транзакцию). Решение зависит от итогового статуса:

- **active** — flow зовёт `recomputeAccess`, находит клубные ресурсы (чат и канал), где grant отсутствует или не равен `joined`, и в короткой handler-транзакции записывает `access_grants.state='pending'`, `audit_log` и нужные outbox actions. Дальнейшее зависит от `INVITE_MODE`:
  - `shared_join_request` — `send_dm` с `MSG_ACTIVE` и ссылками из активных shared-строк `invite_links`;
  - `personal_join_request` или `direct` — `send_invite`, а немедленный durable ответ пользователю — `MSG_INVITE_SOON`.
- **inactive** — `send_dm` с `MSG_NO_SUB`; grants и invite actions не создаются.
- **unknown** — fallback только на свежую active-подписку из БД в пределах `ADMISSION_FALLBACK_MAX_AGE`; такой fallback обрабатывается как `active`, а audit фиксирует, что выдача была основана на нём. Без fallback — `send_dm` с `MSG_TRY_LATER` и `admin_alert` о недоступности источника.
- **banned** (`users.banned=1`) — `send_dm` с `MSG_BANNED`; источники подписки доступа не дают.

Повторный запрос идемпотентен: на пару `(tg_id, resource)` остаётся одна строка grant, одинаковые invite actions не плодятся за счёт outbox idempotency keys, а уже `joined` ресурсы сообщаются как уже доступные. До Фазы 06 `recomputeAccess` не содержит `inactive`-ветки отзыва — ни soft-kick, ни revocation actions.

## Одобрение join-request

`chat_join_request` для управляемых клубных ресурсов — admission-шлагбаум. Handler сопоставляет `chat.id` с resource, обеспечивает пользователя, проверяет hard-ban, разрешает invite link по настроенному режиму (см. [invite-links](invite-links.md)) и считает живой `effectiveStatus` вне `handleTx`. Для `unknown` живая проверка повторяется согласно `ADMISSION_JOIN_REQUEST_RETRIES` перед решением.

Решение fail-closed — одобряет только финальный `active`:

- **active** → `approve_join`, grant переходит в `joined` с `admitted_by='bot'`, personal invite помечается `used`, пишется `audit_log(join_approved)`, ставится `MSG_GRANTED`.
- **inactive**, hard-ban или неразрешённый misuse personal-ссылки → `decline_join`, audit и DM пользователю (`MSG_NO_SUB`).
- устойчивый **unknown** после ретраев → `decline_join` и `MSG_TRY_LATER`.

DM-ответы используют `user_chat_id` из Telegram, когда он доступен, чтобы пользователь, ранее не запускавший бота, всё равно получил результат admission. Idempotency key для `approve_join` включает resource, tg_id и дату/идентификатор заявки: новая заявка после выхода считается новым действием и не подавляется старым approve.

Если personal-ссылка принадлежит tg_id A, а заявка пришла от tg_id B, handler отклоняет B, помечает invite `used_by_other` с `attempted_by=B` и пишет audit о misuse. Отсутствие поля `invite_link` в shared-режиме не отклоняет active-пользователя: решение опирается на resource и live status.

## Обновления членства в клубе

`chat_member` клубного чата и канала обновляет `access_grants` как фактический сигнал членства.

- Пользователь стал участником → `state='joined'`, `joined_at=now`. `admitted_by='bot'`, если update пришёл через join request или совпадает с активным direct-инвайтом, созданным для этого пользователя; иначе `admitted_by='external'`.
- External joins не приводят к автоматическому кику: пишется `audit_log(external_join_detected)` и informational-severity `admin_alert`. Если пользователь вступил по direct-инвайту, живой статус перепроверяется вне `handleTx`, и при не-`active` статусе ставится `soft_kick`.
- Пользователь покинул resource → grant переходит в `left` через `updated_at` (отдельной колонки времени выхода нет), пишется `audit_log(member_left)`. Уже `revoked` grant выходом не перетирается — отзыв сохраняется.

## Приёмка

- Active в shared-режиме получает `pending` grants на чат и канал плюс `send_dm` с `MSG_ACTIVE` и ссылками; inactive — `MSG_NO_SUB` без grants.
- Unknown со свежей active-подпиской обрабатывается как active (с пометкой fallback в audit); без неё — `MSG_TRY_LATER` и `admin_alert`.
- Active join-request → `approve_join`, grant `joined`/`bot`, `MSG_GRANTED`; inactive → `decline_join`, `MSG_NO_SUB`; устойчивый unknown → `decline_join`, `MSG_TRY_LATER`.
- Personal-ссылка, использованная другим tg_id, → `decline_join`, `used_by_other` с `attempted_by`, audit о misuse.
- External join → `joined`/`external` + `admin_alert`, без `soft_kick`; выход → `left` и `member_left`; выход поверх `revoked` grant состояние не меняет.
