## ADDED Requirements

### Requirement: Admission repositories предоставляют grant и invite operations

`store` MUST предоставлять repository-операции, достаточные для
grant-access handlers, чтобы атомарно обновлять admission state внутри
handler transactions. Grants operations MUST поддерживать идемпотентный
переход одной строки `(tg_id, resource)` в `pending`, `joined`, `left`
или `revoked` при сохранении одной строки на пару. Переход в `left`
MUST обновлять `state` и `updated_at`, не требуя несуществующей колонки
времени выхода. Invite operations MUST позволять искать active invite
по `invite_link_hash` и resource, active shared link для resource,
active personal/direct link для `(tg_id, resource, mode)`, а также
помечать link как `used` или `used_by_other` с `attempted_by`.

Эти операции MUST использовать существующие таблицы и правила кодировки
timestamps. Они MUST быть пригодны для `*sql.Tx` через существующий
`DBTX` pattern, чтобы изменения grant и invite status, audit rows,
outbox rows и terminal update status коммитились вместе.

#### Scenario: Pending grant upsert идемпотентен
- **WHEN** grant-access flow больше одного раза помечает ту же пару
  `(tg_id, resource)` как `pending`
- **THEN** `access_grants` содержит одну строку для этой пары
- **AND** последний update не создаёт дубль

#### Scenario: Joined grant сохраняет bot admission metadata
- **WHEN** join-request approval помечает grant как `joined`
- **THEN** строка сохраняет `admitted_by='bot'` и `joined_at`
- **AND** операция может выполняться в той же транзакции, что и
  постановка `approve_join` в outbox

#### Scenario: Left grant использует существующие колонки
- **WHEN** club membership handler наблюдает выход пользователя
- **THEN** grant может перейти в `left` через `state` и `updated_at`
- **AND** операция не пишет отдельный timestamp выхода

#### Scenario: External join представим в схеме
- **WHEN** club membership handler наблюдает добавление пользователя
  помимо admission через бота
- **THEN** grant может быть сохранён как `joined` с
  `admitted_by='external'`

#### Scenario: Invite lookup by hash не раскрывает полный URL
- **WHEN** join-request handler разрешает present `invite_link`
- **THEN** store может найти active invite row по hash и resource
- **AND** callers не нужно логировать полный invite URL

#### Scenario: Personal misuse сохраняет attempted_by
- **WHEN** personal invite используется другим tg_id
- **THEN** store может пометить invite как `used_by_other`
- **AND** `attempted_by` сохраняет tg_id, который попытался её
  использовать
