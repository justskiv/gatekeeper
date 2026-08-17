## MODIFIED Requirements

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

Строка action MUST уметь нести необязательную ссылку `alert_id` на
операционную тревогу, о которой она сообщает. Ссылка MUST быть durable
колонкой, а не выводиться из `idempotency_key` или `payload_json`:
`idempotency_key` хранит хеш маркера и не сохраняет его текст, а `payload_json`
для repository непрозрачен. Ссылка MUST переживать удаление тревоги
обнулением, а не удалением строки действия: история доставок нужна для
разбора, а retention удаляет резолвнутые тревоги при включённых foreign keys.

Repository MUST предоставить два способа снять действие:

- отмену владельцем lease — терминальный переход в `cancelled` с той же
  fencing-парой `(id, locked_until)` и тем же ожидаемым статусом `running`, что
  и остальные терминальные переходы; перехваченный lease MUST давать
  `ErrLeaseLost`;
- массовую отмену очередей по `alert_id`, возвращающую число снятых строк.

Массовая отмена MUST трогать только строки в статусе `queued`. Строка в
`running` принадлежит воркеру, который её взял, и запись по ней из другого
места MUST NOT происходить — именно это предотвращает fencing. Проверка на
стороне владельца lease остаётся единственным способом снять уже взятое
действие.

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

#### Scenario: Отмена по тревоге не трогает взятое действие
- **WHEN** у тревоги есть одна `running` и одна `queued` доставка, и
  вызывается массовая отмена по её `alert_id`
- **THEN** снимается только `queued` строка
- **AND** `running` строка остаётся за своим воркером

#### Scenario: Отмена владельцем lease защищена fencing
- **WHEN** прежний владелец пытается отменить действие, lease которого уже
  перехвачен другим воркером
- **THEN** переход возвращает `ErrLeaseLost`
- **AND** статус строки не меняется

#### Scenario: Удаление тревоги не удаляет её доставки
- **WHEN** тревога с уже доставленным связанным действием удаляется retention
- **THEN** удаление проходит без нарушения foreign key
- **AND** строка действия сохраняется с пустой ссылкой на тревогу

### Requirement: Alerts repository deduplicates and supports delivery

`Alerts.Create` MUST support stable dedupe keys for open alerts. If an
open alert with the same key already exists, creation MUST return the
existing alert or explicit duplicate result without creating a second
open row. When a new alert is created, callers MUST be able to enqueue
operator delivery in the same transaction.

Alert rows MUST retain severity, kind, machine-readable metadata and
status. Resolving an alert MUST update only alert status/resolution
fields and MUST NOT delete forensic context.

Постановка доставки MUST связывать строку outbox с тревогой, ради которой она
создана. Резолв тревоги MUST снимать её ещё не доставленные уведомления: в
одной транзакции MUST быть выбраны идентификаторы резолвимых тревог, изменён их
статус и отменены их `queued` доставки. Порядок обязателен — после смены
статуса выборка по «открытым» тревогам уже ничего не находит, и доставки теряют
свою ручку. Атомарность обязательна — тревога MUST NOT оказаться резолвнутой с
живой очередью уведомлений о ней.

Repository MUST предоставить чтение «тревога всё ещё открыта» по её
идентификатору, чтобы исполнитель мог проверить актуальность перед отправкой.
Отсутствующая тревога MUST читаться как «не открыта» и MUST NOT быть ошибкой.

Отмена MUST документироваться как best-effort: последовательность «проверили,
что тревога открыта → тревога резолвнулась → отправили» неустранима без
удержания строки тревоги через сетевой вызов Telegram. Механизм сужает окно, но
MUST NOT описываться как гарантия недоставки.

#### Scenario: Duplicate open alert is deduplicated
- **WHEN** the same alert kind and dedupe key are raised twice
- **THEN** there is at most one open alert for that key
- **AND** duplicate creation does not require duplicate owner delivery

#### Scenario: Alert and delivery share one transaction
- **WHEN** a critical alert is created and owner delivery is needed
- **THEN** alert row and `send_dm` or admin-log outbox action can be
  committed atomically

#### Scenario: Resolve preserves alert context
- **WHEN** owner resolves an alert
- **THEN** status changes to resolved
- **AND** original kind, severity and metadata remain readable

#### Scenario: Резолв снимает недоставленные уведомления
- **WHEN** тревога с `queued` доставкой резолвится
- **THEN** доставка переходит в `cancelled` в той же транзакции
- **AND** уже доставленная строка сохраняет свой исход
- **AND** строки, не связанные с этой тревогой, не затрагиваются

#### Scenario: Резолв внутри чужой транзакции работает так же
- **WHEN** repository привязан к транзакции обрабатываемого update, а не к пулу
- **THEN** резолв и отмена доставок выполняются в этой же транзакции
- **AND** отсутствие вложенной транзакции не превращает отмену в no-op

#### Scenario: Проверка открытости отвечает по отсутствующей тревоге
- **WHEN** запрашивается открытость тревоги, строки которой уже нет
- **THEN** ответ — «не открыта»
- **AND** это не ошибка чтения

### Requirement: Cleanup repository operations preserve forensic rows

`store` MUST provide cleanup operations for retention without embedding
policy in SQL call sites. Cleanup MUST support deleting old terminal
`telegram_updates` and `tribute_events`, deleting old settled
`access_actions`, deleting resolved old alerts, applying
`AUDIT_RETENTION` to `audit_log`, expiring personal/direct invite links
and running WAL checkpoint.

Улаженными (`settled`) для retention MUST считаться действия в статусах `done`
и `cancelled`: и те и другие ничего не оставили оператору на разбор — первые
исполнены, вторые сняты до исполнения.

Cleanup operations MUST NOT automatically delete failed inbox rows or
dead outbox actions. Those rows remain available for operator
investigation.

#### Scenario: Processed inbox rows can be deleted by cutoff
- **WHEN** cleanup receives a cutoff from `RAW_RETENTION`
- **THEN** processed or ignored inbox rows older than cutoff are
  deleted
- **AND** failed rows are left untouched

#### Scenario: Dead actions survive cleanup
- **WHEN** an `access_actions` row has `status='dead'`
- **THEN** cleanup does not delete it only because it is old

#### Scenario: Отменённые действия жнутся вместе с исполненными
- **WHEN** cleanup получает cutoff и в таблице есть старые `done`,
  `cancelled` и `dead` строки
- **THEN** удаляются `done` и `cancelled`
- **AND** `dead` строка остаётся

#### Scenario: Expired personal links are selected for maintenance
- **WHEN** active personal or direct invite links have
  `expires_at <= now`
- **THEN** repository can mark them `expired` and return links needing
  `revoke_invite`
