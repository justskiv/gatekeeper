## ADDED Requirements

### Requirement: Invite service безопасно разрешает join-request ссылки

Invite service MUST предоставлять операцию разрешения join-request
ссылки для grant-access handlers. Resolution MUST принимать managed
resource, optional Telegram `invite_link` и requesting tg_id, а
возвращать один из результатов: shared-ссылка принята,
personal-ссылка принята, personal-ссылка использована другим
пользователем, информации о ссылке нет, но fallback безопасен, или
ссылка не разрешена.

Когда `invite_link` присутствует, resolution MUST сравнивать по
сохранённому `invite_link_hash`, а не через логирование или раскрытие
полного URL. В `shared_join_request` любая активная shared link для
resource допустима, потому что реальным шлагбаумом остаётся
`approveChatJoinRequest` с live status. В `personal_join_request`
ссылка MUST принадлежать requesting tg_id; иначе она MUST помечаться
`used_by_other` с `attempted_by=<requesting tg_id>`. Если Telegram не
прислал `invite_link`, active пользователь MUST NOT отклоняться только
из-за пустого поля: shared mode использует fallback по resource, а
personal mode MAY использовать последнюю active personal link
пользователя для этого resource.

#### Scenario: Shared join request разрешается по resource
- **WHEN** join-request приходит для managed resource в
  `shared_join_request` режиме
- **THEN** активная shared link для этого resource принимается
- **AND** доступ всё равно зависит от live eligibility check

#### Scenario: Personal link принадлежит requester
- **WHEN** join-request содержит personal invite link, созданную для
  того же tg_id
- **THEN** resolution принимает personal link

#### Scenario: Personal link, использованная другим пользователем, помечается
- **WHEN** join-request содержит personal invite link, созданную для
  другого tg_id
- **THEN** invite row помечается `used_by_other`
- **AND** `attempted_by` сохраняет requesting tg_id

#### Scenario: Missing invite_link использует безопасный fallback
- **WHEN** Telegram не прислал `invite_link` в join-request update
- **THEN** shared mode может разрешить заявку по resource
- **AND** personal mode может разрешить её по активной personal link
  requester'а для этого resource

#### Scenario: Полный invite URL не логируется при resolution
- **WHEN** resolution читает или сопоставляет invite link
- **THEN** технические логи содержат максимум `invite_link_hash`
- **AND** полный `invite_link` не пишется в логи
