## MODIFIED Requirements

### Requirement: Migrations are forward-only via goose

Migrations MUST live as `.sql` files in `./migrations/`
(configurable via `--migrations-dir`). They MUST be applied by
`goose v3` with dialect `sqlite3`. Each file MUST use goose
annotations (`-- +goose Up`, etc.) and MUST be recorded in
`goose_db_version` after a successful apply.

Изменение, которое SQLite не умеет выразить как `ALTER` — в первую очередь
правка `CHECK`-ограничения — MUST выполняться перестройкой таблицы:
переименовать, создать заново в новой форме, скопировать строки, удалить
старую таблицу, восстановить индексы. Копирование MUST быть защищено от
значений, которых новая форма не допускает: если ограничение сужается,
непроходящие значения MUST отображаться в допустимые явным выражением, а не
предполагаться отсутствующими. База в проде старше кода, и её содержимое
MUST NOT приниматься на веру.

Каждая миграция MUST иметь работающий `Down`, зеркальный своему `Up`, включая
обратное отображение значений, которых старая форма не знает.

`0003_outbox_alert_link.sql` перестраивает `access_actions`: добавляет
`alert_id INTEGER REFERENCES admin_alerts(id) ON DELETE SET NULL`, сужает
статусный `CHECK` до `('queued','running','done','dead','cancelled')` и
восстанавливает индекс готовности вместе с частичным индексом по `alert_id`.
`ON DELETE SET NULL` здесь обязателен: при включённых foreign keys retention
удаляет резолвнутые тревоги, и обычная ссылка навсегда сломала бы cleanup на
первой же строке, пережившей свою тревогу.

#### Scenario: Initial migration on an empty database
- **WHEN** `migrate up` runs against an empty database
- **THEN** `0001_init.sql` is applied
- **AND** `goose_db_version` records the version with `is_applied=1`

#### Scenario: Re-running `migrate up`
- **WHEN** `migrate up` runs against a fully migrated database
- **THEN** no migration is applied
- **AND** the log emits `"migrations: already up to date"`

#### Scenario: Сужение CHECK перестраивает таблицу и отображает значения
- **WHEN** миграция сужает `CHECK`-ограничение колонки статуса
- **THEN** таблица пересоздаётся, а строки копируются с явным отображением
  значений, которых новое ограничение не допускает
- **AND** миграция применяется на базе, где такие строки уже есть

#### Scenario: `0003` переносит устаревший статус и запрещает его дальше
- **WHEN** `0003_outbox_alert_link.sql` применяется к базе со строкой
  `access_actions.status='failed'`
- **THEN** строка сохраняется со статусом `dead`
- **AND** последующая вставка `status='failed'` отклоняется ограничением
- **AND** вставка `status='cancelled'` принимается

#### Scenario: Down возвращает прежнюю форму таблицы
- **WHEN** `0003_outbox_alert_link.sql` откатывается
- **THEN** колонка `alert_id` отсутствует, а прежний статусный `CHECK`
  восстановлен
- **AND** строки со статусом `cancelled` сохраняются как `done`
