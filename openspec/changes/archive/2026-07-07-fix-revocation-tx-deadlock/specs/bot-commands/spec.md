## MODIFIED Requirements

### Requirement: Owner access commands manage manual access and bans

Owner commands `/grant`, `/revoke`, `/ban`, `/unban` and `/sync` MUST
работать только в личке для `OWNER_TG_IDS`. Запросы от не-owner MUST
игнорироваться как обычный некомандный текст и MUST NOT раскрывать
данные пользователя.

`/grant <tg_id> [срок] [причина]` MUST создавать manual access: без
срока — whitelist entry, со сроком — `manual` subscription с
`expires_at`. Команда по числовому `tg_id` MUST создавать stub
`users` row, если пользователя ещё нет; lookup по `@username` MUST
работать только по локальной БД.

`/revoke <tg_id> [причина]` MUST снять whitelist/manual access,
записать audit и вызвать `recomputeAccess`; если других active sources
нет, отзыв MUST пойти через configured `EXPIRY_MODE`. `/ban <tg_id>`
MUST выставить `users.banned=1` и немедленно запустить hard-ban
revocation. `/unban <tg_id>` MUST снять hard-ban; доступ после unban
возвращается только при active status и обычном запросе доступа.

`/sync [tg_id]` MUST запускать reconciliation после owner confirmation:
для одного пользователя, если аргумент указан, или полный pass, если
аргумента нет. Reconciliation MUST выполняться асинхронно после commit
update-транзакции (на пуле), а не синхронно внутри неё: reconcile внутри
update-транзакции дедлочит единственное SQLite-соединение на своей live
decide-фазе. Немедленный ответ владельцу MUST подтверждать запуск; итоговая
сводка MUST приходить отдельным сообщением (admin log, когда задан).

#### Scenario: Grant создаёт stub user по числовому tg_id
- **WHEN** owner выполняет `/grant 12345` для неизвестного пользователя
- **THEN** создаётся stub `users` row с `tg_id=12345`
- **AND** добавляется whitelist или manual subscription
- **AND** вызывается `recomputeAccess`

#### Scenario: Revoke запускает configured revocation
- **WHEN** owner выполняет `/revoke <tg_id>` и других active sources нет
- **THEN** manual access снимается
- **AND** `recomputeAccess` применяет `EXPIRY_MODE`

#### Scenario: Ban перекрывает active subscription
- **WHEN** owner подтверждает `/ban <tg_id>` для active пользователя
- **THEN** `users.banned` становится `1`
- **AND** hard-ban revocation ставится через outbox

#### Scenario: Sync одного пользователя не запускает полный pass
- **WHEN** owner выполняет `/sync <tg_id>`
- **THEN** Reconciler проверяет только указанного пользователя и
  связанные resources
- **AND** full reconciliation pass не запускается

#### Scenario: Sync выполняется вне update-транзакции
- **WHEN** owner подтверждает `/sync`
- **THEN** reconcile не исполняется внутри update-транзакции
- **AND** reconcile выполняется на пуле после её commit
- **AND** владелец получает немедленное подтверждение запуска, а сводка
  приходит отдельным сообщением
