## ADDED Requirements

### Requirement: Invite service supports three link modes

Пакет `invite` MUST управлять lifecycle строк `invite_links` и
созданием Telegram invite links. Он MUST объявлять узкий
`linkManager`-интерфейс для `createChatInviteLink` и
`revokeChatInviteLink`, а также узкий Store для `invite_links`.

Сервис MUST поддерживать три режима с активной уникальностью per-mode:

- `shared_join_request`: максимум одна активная ссылка на
  `(resource, mode)`, `tg_id` MUST быть `NULL`,
  `creates_join_request=true`.
- `personal_join_request`: персональная join-request ссылка с TTL и
  `nonce`, максимум одна активная на `(tg_id, resource, mode)`,
  `tg_id` MUST быть `NOT NULL`.
- `direct`: аварийная персональная ссылка без join request, с
  `member_limit=1`, TTL не больше одного часа и `tg_id NOT NULL`.

#### Scenario: Одна активная shared-ссылка на ресурс
- **WHEN** для resource в `shared_join_request` уже есть активная
  ссылка и запрашивается ещё одна
- **THEN** переиспользуется существующая активная ссылка
- **AND** вторая активная shared-ссылка не создаётся

#### Scenario: Personal ссылка переиспользуется до TTL
- **WHEN** для `(user, resource, personal_join_request)` есть активная
  ссылка и её TTL не истёк
- **THEN** возвращается та же ссылка
- **AND** Telegram `createChatInviteLink` повторно не вызывается

#### Scenario: Истёкшая personal ссылка освобождает слот
- **WHEN** personal ссылка для `(user, resource, mode)` помечена
  `expired`
- **THEN** сервис может создать новую active ссылку для той же тройки

### Requirement: Shared links are ensured on startup

При старте в режиме `shared_join_request` система MUST поставить
`ensure_invite` для клубного чата и канала и MUST дождаться по одной
активной ссылке (`creates_join_request=true`) в `invite_links` для
каждого resource, прежде чем считать Telegram runtime готовым к poller
loop.

#### Scenario: При старте появляется по одной active shared ссылке
- **WHEN** процесс стартует в `shared_join_request`
- **THEN** для club chat ставится `ensure_invite`
- **AND** для club channel ставится `ensure_invite`
- **AND** startup дожидается активной join-request ссылки на каждый
  resource

### Requirement: Direct mode is degraded and guarded

`direct` MUST работать только при `INVITE_MODE=direct` и
`ALLOW_DIRECT_INVITES=true`; иначе конфигурация MUST завершаться
ошибкой до открытия БД. `INVITE_TTL` для direct MUST быть не больше
одного часа. При успешном старте в direct система MUST создать
`admin_alert(kind='invite_mode_degraded')`. Direct-ссылка MUST
создаваться с `creates_join_request=false` и `member_limit=1`.

#### Scenario: direct без разрешающего флага не стартует
- **WHEN** `INVITE_MODE=direct`, но `ALLOW_DIRECT_INVITES` не равен
  `true`
- **THEN** конфигурация считается невалидной
- **AND** процесс завершается до открытия БД

#### Scenario: direct TTL больше часа не стартует
- **WHEN** `INVITE_MODE=direct` и `INVITE_TTL > 1h`
- **THEN** конфигурация считается невалидной
- **AND** direct-ссылка не создаётся

#### Scenario: Старт в direct поднимает degraded alert
- **WHEN** процесс стартует с валидным `INVITE_MODE=direct`
- **THEN** создаётся `admin_alert(kind='invite_mode_degraded')`
- **AND** direct-ссылки создаются с `member_limit=1` и без join request

### Requirement: Invite links are not leaked to technical logs

Полные invite URLs MUST NOT записываться в технические логи. Для
корреляции MUST использоваться `invite_link_hash`; полный `invite_link`
MUST храниться только в БД и отправляться пользователю через outbox.

#### Scenario: Лог содержит только hash ссылки
- **WHEN** invite service создаёт или переиспользует ссылку и пишет
  технический лог
- **THEN** в записи присутствует `invite_link_hash`
- **AND** полного `invite_link` в записи нет
