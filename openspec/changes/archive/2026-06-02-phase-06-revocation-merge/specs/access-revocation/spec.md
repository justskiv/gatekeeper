## ADDED Requirements

### Requirement: Достоверный inactive запускает configured revocation flow

Access revocation MUST запускаться только когда `effectiveStatus`
равен `inactive`. Статус `unknown` MUST NOT создавать
`pending_revocation`, `soft_kick`, `hard_ban` или другой revoke action.
Если хотя бы один источник даёт `active`, автоматический отзыв MUST NOT
начинаться.

При `EXPIRY_MODE=grace` и наличии bot-admitted grants в state
`joined` или `pending` система MUST создать одну
`pending_revocation` для пользователя с `scheduled_at = now +
GRACE_PERIOD`, записать `audit_log(revocation_scheduled)` и поставить
durable `send_dm` с `MSG_EXPIRY_WARNING`. Повторный пересчёт до
`scheduled_at` MUST NOT создавать дубль pending revocation или дубль
warning.

При `EXPIRY_MODE=immediate` система MUST вызвать `revokeNow` без
создания grace-записи. При `EXPIRY_MODE=notify_only` система MUST
поставить `MSG_EXPIRED_NOTICE`, записать audit и не менять
`access_grants`.

#### Scenario: Grace планирует один отзыв
- **WHEN** пользователь с bot-admitted `joined` grant становится
  `inactive` при `EXPIRY_MODE=grace`
- **THEN** создаётся одна `pending_revocation` с due time после
  `GRACE_PERIOD`
- **AND** ставится durable `MSG_EXPIRY_WARNING`
- **AND** повторный пересчёт не создаёт вторую запись или второе
  предупреждение

#### Scenario: Immediate отзывает без grace
- **WHEN** пользователь с bot-admitted grant становится `inactive` при
  `EXPIRY_MODE=immediate`
- **THEN** `revokeNow` исполняется сразу
- **AND** `pending_revocation` не создаётся

#### Scenario: Notify only не трогает grants
- **WHEN** пользователь становится `inactive` при
  `EXPIRY_MODE=notify_only`
- **THEN** пользователю ставится `MSG_EXPIRED_NOTICE`
- **AND** состояние `access_grants` не меняется

#### Scenario: Unknown не планирует отзыв
- **WHEN** `effectiveStatus` равен `unknown`
- **THEN** `pending_revocation` не создаётся
- **AND** revoke actions не ставятся

### Requirement: Active status cancels pending revocation

System MUST cancel pending revocation when `effectiveStatus` стал
`active` для пользователя с
`pending_revocation`, система MUST удалить pending revocation, записать
`audit_log(revocation_cancelled)` и поставить durable `send_dm` с
`MSG_ACCESS_KEPT`. Если pending revocation нет, active path MUST быть
no-op для revocation state.

#### Scenario: Подписка вернулась в grace
- **WHEN** пользователь получает `active` статус до
  `pending_revocation.scheduled_at`
- **THEN** `pending_revocation` удаляется
- **AND** ставится `MSG_ACCESS_KEPT`
- **AND** `soft_kick` не ставится

#### Scenario: Active без pending revocation ничего не меняет
- **WHEN** пользователь имеет `active` статус и pending revocation
  отсутствует
- **THEN** revocation tables и grants не меняются
- **AND** `MSG_ACCESS_KEPT` не ставится

### Requirement: revokeNow performs a final safety check before kick

`revokeNow(tgID, reason)` MUST перед постановкой kick actions выполнить
финальную live-перепроверку `effectiveStatus`. Если результат равен
`active`, система MUST удалить pending revocation, записать
`audit_log(revocation_cancelled)`, поставить `MSG_ACCESS_KEPT` и
завершить без kick actions. Если результат равен `unknown`, система
MUST NOT kick пользователя; она MUST оставить или перепланировать
pending revocation и создать operator-visible alert.

Если финальный статус остаётся `inactive`, `revokeNow` MUST поставить
`soft_kick` для каждого club resource, где grant существует,
находится в state `joined` или `pending` и имеет `admitted_by='bot'`.
После постановки actions grant MUST перейти в `revoked` с
`revoked_at` и `revoked_reason`, pending revocation MUST быть удалён,
audit MUST получить `access_revoked`, а пользователь с открытой личкой
MUST получить `MSG_REVOKED`.

#### Scenario: Active между warning и due отменяет kick
- **WHEN** due revocation исполняется, но финальный
  `effectiveStatus` стал `active`
- **THEN** pending revocation удаляется
- **AND** `soft_kick` actions не ставятся
- **AND** пользователю ставится `MSG_ACCESS_KEPT`

#### Scenario: Unknown на финальной проверке блокирует kick
- **WHEN** due revocation исполняется, но финальный
  `effectiveStatus` равен `unknown`
- **THEN** `soft_kick` actions не ставятся
- **AND** grant state не переводится в `revoked`
- **AND** владелец получает alert о невозможности безопасного отзыва

#### Scenario: Inactive исполняет soft-kick из обоих ресурсов
- **WHEN** финальный `effectiveStatus` равен `inactive`, а у
  пользователя есть bot-admitted grants в chat и channel
- **THEN** для каждого resource ставится `soft_kick`
- **AND** оба grants переходят в `revoked`
- **AND** пользователю ставится `MSG_REVOKED`, если его личка открыта

### Requirement: Protected and external members are not auto-kicked

Автоматический отзыв MUST выбирать только `access_grants` со state
`joined` или `pending` и `admitted_by='bot'`. Grants с
`admitted_by='external'` MUST NOT получать automatic `soft_kick` при
событийном пересчёте или reconciliation.

Перед постановкой `soft_kick` для resource система MUST проверить
`isProtected(resource, tgID)` через `getChatMember`. Пользователь со
статусом `creator` или `administrator` MUST NOT быть kicked; вместо
этого система MUST создать `admin_alert` с kind
`protected_admin_lost_subscription`.

#### Scenario: External grant не кикается
- **WHEN** `external` участник клубного ресурса теряет подписку
- **THEN** automatic `soft_kick` для него не ставится
- **AND** grant не переводится в `revoked` автоматическим отзывом

#### Scenario: Creator или admin защищён от auto-kick
- **WHEN** bot-admitted grant принадлежит creator или administrator
  club resource
- **THEN** `soft_kick` для этого resource не ставится
- **AND** создаётся alert `protected_admin_lost_subscription`

### Requirement: Hard-ban revokes without automatic unban

Hard-ban MUST быть явным owner action, который выставляет
`users.banned=1`, отменяет pending revocation и ставит `hard_ban`
actions для bot-admitted club resources без последующего `unban`.
Hard-ban MUST перекрывать любые active subscriptions и whitelist.

`/unban` или соответствующий domain flow MUST выставлять
`users.banned=0` и ставить `unban` actions только для ресурсов, где
нужно снять постоянный бан. Возврат доступа после unban MUST зависеть
от обычного active status и pull-модели `/start`.

#### Scenario: Hard-ban перекрывает active subscription
- **WHEN** owner hard-bans пользователя с активной подпиской
- **THEN** `users.banned` становится `1`
- **AND** ставятся `hard_ban` actions
- **AND** дальнейшая join-request не одобряется до `/unban`

#### Scenario: Unban не выдаёт доступ сам по себе
- **WHEN** owner выполняет `/unban` для пользователя
- **THEN** `users.banned` становится `0`
- **AND** новые grants не создаются без active status и запроса доступа
