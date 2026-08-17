## MODIFIED Requirements

### Requirement: Ops read models expose statistics, alerts and export data

`store` MUST expose narrow read methods for owner ops commands without
leaking write concerns into bot handlers. The read model MUST support:

- active subscriptions grouped by platform;
- club grants grouped by resource and state;
- due or pending revocations;
- health values from `meta.health.*`;
- `meta.reconcile.last_run_at`;
- outbox queue counts by status and count of `dead`;
- open alerts with ids, severity, kind, title, detail and timestamps;
- known chats and configured chat IDs;
- CSV export rows for users and current subscriptions.

These reads MUST be bounded or paginated where result size can grow and
MUST NOT include raw provider payload JSON in owner summaries or CSV
unless explicitly required by a future change.

**Давление очереди MUST читаться одним запросом.** Read-model MUST предоставить
выборку, возвращающую за один скан: число `queued` строк, число `running`
строк, число `queued` строк, чей `run_after` уже прошёл, возраст самой старой
`queued` строки по `created_at` и возраст самой старой просроченной строки по
`run_after`. Счётчики и возрасты MUST описывать один и тот же момент: двумя
запросами они расходятся между собой, а метрика очереди, противоречащая самой
себе, хуже отсутствующей.

Момент отсчёта возрастов MUST передаваться вызывающим, а не браться внутри: тот
же момент задаёт границу «просрочено», и два разных `now` внутри одной выборки
давали бы несогласованный ответ.

Просроченность MUST определяться по `run_after`, а не по факту нахождения в
очереди: действие, ждущее своего retry-backoff, ожидает законно, и учёт его как
просроченного красил бы нормальный retry в отказ.

Выборка MUST возвращать нули на пустой таблице, а MUST NOT завершаться ошибкой.
Агрегат без `GROUP BY` возвращает на пустой таблице одну строку из `NULL`, и
read-model обязан это учитывать.

**Тревоги MUST читаться сгруппированными по ограниченным измерениям.**
Read-model MUST предоставить счётчики `admin_alerts` с группировкой по kind,
severity и status, со стабильным порядком. Это чтение существует для
экспозиции метрик: открытые тревоги до сих пор наблюдались только через
доставку владельцу, то есть исчезали вместе с доставкой.

Перечень допустимых severity MUST быть доступен потребителям из `store`, а
MUST NOT переписываться литералами на стороне вызывающего: ограничение задаёт
схема, и второй список тех же значений расходится с ней молча.

#### Scenario: Stats can read operational counters
- **WHEN** `/stats` builds its response
- **THEN** store can provide counts for subscriptions, grants,
  revocations, health, reconcile freshness, outbox and open alerts

#### Scenario: Alerts list returns open alerts
- **WHEN** `/alerts` asks for unresolved operator alerts
- **THEN** store returns open alerts ordered by severity and creation
  time

#### Scenario: Export excludes raw payloads
- **WHEN** `/export` generates CSV
- **THEN** rows include users and current subscriptions
- **AND** raw `telegram_updates.payload_json` and
  `tribute_events.payload_json` are not included

#### Scenario: Давление очереди читается одним сканом
- **WHEN** в очереди есть просроченная строка, строка, запланированная на
  будущее, и строка, взятая воркером
- **THEN** выборка возвращает по одной в `queued`-счётчике на каждую
  невзятую строку, единицу в `running` и единицу в `due`
- **AND** возраст просроченного считается от `run_after`, а не от `created_at`
- **AND** будущая строка не попадает ни в `due`, ни в возраст просроченного

#### Scenario: Пустая очередь читается нулями
- **WHEN** в `access_actions` нет ни одной строки
- **THEN** выборка возвращает нулевые счётчики и нулевые возрасты
- **AND** это не ошибка чтения

#### Scenario: Тревоги группируются по kind, severity и status
- **WHEN** открыты две тревоги одного вида и резолвнута одна другого
- **THEN** выборка возвращает две группы с их количествами
- **AND** порядок групп стабилен между вызовами

#### Scenario: Пустая таблица тревог
- **WHEN** тревог не было заведено ни разу
- **THEN** выборка возвращает пустой результат без ошибки
