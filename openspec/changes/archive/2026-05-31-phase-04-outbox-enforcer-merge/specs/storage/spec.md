## ADDED Requirements

### Requirement: Outbox repository exposes durable action operations

`store` MUST предоставить repository для `access_actions`, построенный
по существующему паттерну `NewX(q DBTX)`. Repository MUST уметь
идемпотентно ставить action, брать одно готовое действие lease'ом,
завершать action как `done`, перепланировать retry с новым
`run_after`, записывать `last_error`, увеличивать счётчик попыток и
помечать action как `dead`.

Repository MUST кодировать timestamp-колонки в RFC3339 UTC и не
интерпретировать `payload_json` за пределами хранения/чтения. Выборка
готового действия MUST учитывать `queued` строки с `run_after<=now` и
просроченные `running` строки с `locked_until<now`.

#### Scenario: Enqueue идемпотентен
- **WHEN** `Outbox.Enqueue` вызывается дважды с одним
  `idempotency_key`
- **THEN** в таблице `access_actions` остаётся одна строка
- **AND** caller может отличить новую вставку от существующей строки

#### Scenario: Lease не выдаёт одно действие двум воркерам
- **WHEN** два воркера одновременно вызывают lease ready action
- **THEN** только один получает конкретный action
- **AND** строка получает `status='running'` и `locked_until`

#### Scenario: Retry сохраняет причину и время следующего запуска
- **WHEN** action перепланируется после retryable ошибки
- **THEN** `attempts` увеличивается
- **AND** `last_error` и новый `run_after` сохраняются в БД

### Requirement: Invite links repository exposes active-link operations

`store` MUST предоставить repository для `invite_links`, построенный
по паттерну `NewX(q DBTX)`. Repository MUST уметь искать активную
shared ссылку по resource, искать активную personal/direct ссылку по
`tg_id`, resource и mode, сохранять созданную Telegram-ссылку, отмечать
ссылку как `sent`, `used`, `used_by_other`, `revoked`, `expired` или
`failed`, и выбирать истёкшие активные ссылки для обслуживания.

Repository MUST сохранять полный `invite_link` только в БД, а
`invite_link_hash` MUST быть доступен вызывающему коду для логов и
аудита. Правила активной уникальности из схемы MUST оставаться
наблюдаемыми через методы repository.

#### Scenario: Active shared lookup возвращает одну ссылку
- **WHEN** в БД есть активная `shared_join_request` ссылка для resource
- **THEN** repository возвращает её как текущую ссылку ресурса

#### Scenario: Истёкшая personal ссылка освобождает слот
- **WHEN** personal ссылка помечена `expired`
- **THEN** repository позволяет сохранить новую active ссылку для того
  же пользователя, resource и mode

#### Scenario: Mark failed сохраняет last_error
- **WHEN** создание или отзыв invite-ссылки завершается ошибкой
- **THEN** repository может пометить строку `failed`
- **AND** `last_error` содержит диагностическое сообщение
