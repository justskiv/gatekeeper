## REMOVED Requirements

### Requirement: recomputeAccess в этой фазе обрабатывает только active и unknown

**Reason**: Фаза 06 реализует ранее отложенную `inactive` ветку
автоматического отзыва доступа.

**Migration**: Поведение заменяется требованием
`recomputeAccess безопасно координирует отзыв доступа`, которое
сохраняет прежние `active` и `unknown` инварианты и добавляет
configured revocation flow для `inactive`.

## ADDED Requirements

### Requirement: recomputeAccess безопасно координирует отзыв доступа

`recomputeAccess(tgID)` MUST быть идемпотентной entry point для
пересчёта доступа после subscription events, manual admin changes и
reconciliation. При `active` она MUST отменять existing
`pending_revocation`, писать `audit_log(revocation_cancelled)` и
готовить `MSG_ACCESS_KEPT`; без pending revocation active path MUST
быть no-op для access grants.

При `unknown` она MUST писать `audit_log(status_unknown)` и MUST NOT
создавать, исполнять или удалять revocation actions. При `inactive` она
MUST делегировать access-revocation flow: выбрать только bot-admitted
grants, применить `EXPIRY_MODE`, создать `pending_revocation`,
поставить warning или вызвать `revokeNow`.

Hard-ban MUST применяться до обычного status aggregation: если
`users.banned=1`, `recomputeAccess` MUST идти по hard-ban revocation
path даже при active source verdicts.

#### Scenario: Active снимает запланированный отзыв
- **WHEN** статус стал `active` и существует `pending_revocation`
- **THEN** запись отзыва удаляется
- **AND** пишется `audit_log(revocation_cancelled)`
- **AND** готовится `MSG_ACCESS_KEPT`

#### Scenario: Active без отзыва остаётся no-op
- **WHEN** статус `active`, но `pending_revocation` нет
- **THEN** доступ не меняется
- **AND** `MSG_ACCESS_KEPT` и revocation audit не пишутся

#### Scenario: Unknown ничего не трогает
- **WHEN** статус `unknown`
- **THEN** пишется `audit_log(status_unknown)`
- **AND** `pending_revocation`, `access_grants` и revoke actions не
  меняются

#### Scenario: Inactive планирует или исполняет отзыв
- **WHEN** статус стал `inactive` и есть bot-admitted grant в state
  `joined` или `pending`
- **THEN** `recomputeAccess` применяет `EXPIRY_MODE`
- **AND** результат соответствует access-revocation capability

#### Scenario: Active в одном источнике блокирует отзыв
- **WHEN** один source verdict равен `active`, а другой равен
  `inactive`
- **THEN** итоговый статус остаётся `active`
- **AND** `pending_revocation` не создаётся

#### Scenario: Hard-ban сильнее active source
- **WHEN** `users.banned=1`, но source verdict равен `active`
- **THEN** итоговый доступ считается `inactive` для пользователя
- **AND** запускается hard-ban revocation path
