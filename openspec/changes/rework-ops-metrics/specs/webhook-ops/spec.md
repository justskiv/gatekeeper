## MODIFIED Requirements

### Requirement: Metrics endpoint emits bounded Prometheus metrics

When `METRICS_ENABLED=true`, `GET /metrics` MUST emit Prometheus text
format with bounded-label metrics. Labels MUST NOT contain `tg_id`,
username, email, invite URLs, raw error strings or other unbounded PII.

**Тип метрики MUST соответствовать её природе.** Значение, полученное как
`count(*)` по живой таблице, MUST быть объявлено `gauge`: retention удаляет
строки, серия убывает, и `rate()` поверх неё читает убывание как counter reset
и дорисовывает работу, которой не было. Имя MUST NOT оканчиваться на `_total`,
если метрика не является counter'ом — суффикс сам по себе приглашает применить
к серии `rate()`.

Counter MUST вестись в памяти процесса и MUST быть монотонным между
рестартами. Обнуление при рестарте — нормальное поведение counter'а и
MUST NOT считаться дефектом.

Набор метрик MUST включать:

- `gatekeeper_outbox_actions{type,status}` (gauge);
- `gatekeeper_outbox_actions_processed_total{type,result}` (counter);
- `gatekeeper_outbox_pending` (gauge);
- `gatekeeper_outbox_running` (gauge);
- `gatekeeper_outbox_oldest_queued_age_seconds` (gauge);
- `gatekeeper_outbox_oldest_due_age_seconds` (gauge);
- `gatekeeper_enforcer_workers_configured` (gauge);
- `gatekeeper_enforcer_workers_alive` (gauge);
- `gatekeeper_enforcer_workers_stale` (gauge);
- `gatekeeper_enforcer_max_cycle_age_seconds` (gauge);
- `gatekeeper_enforcer_worker_restarts_total` (counter);
- `gatekeeper_admin_alerts{kind,severity,state}` (gauge);
- `gatekeeper_admin_alerts_open{severity}` (gauge);
- `gatekeeper_updates{type}` (gauge);
- `gatekeeper_webhooks{status}` (gauge);
- `gatekeeper_revocations{reason}` (gauge);
- `gatekeeper_subscriptions_active{platform}` (gauge);
- `gatekeeper_access_grants{resource,state}` (gauge);
- `gatekeeper_invite_links{resource,mode,status}` (gauge);
- `gatekeeper_reconcile_last_run_age_seconds` (gauge);
- `gatekeeper_telegram_api_errors_total{method,category}` (counter);
- `gatekeeper_metrics_store_scrape_success` (gauge).

**Zero-fill.** Там, где множество значений метки замкнуто и известно коду,
экспозиция MUST выводить серию для каждого значения, включая нулевые.
Отсутствующая серия и нулевая серия не эквивалентны: правило поверх
отсутствующей серии живёт в `NoData` и начинает возвращать данные только после
того, как отслеживаемое событие уже произошло. Zero-fill MUST применяться к:

- `gatekeeper_outbox_actions` — по всему декартову произведению типов действий
  и статусов;
- `gatekeeper_admin_alerts_open` — по всем severity, которые допускает схема.

Перечни для zero-fill MUST браться из доменных перечислений и ограничений
схемы, а MUST NOT дублироваться списком в коде экспозиции: второй список тех же
значений расходится с первым при добавлении нового значения, и расхождение
проявляется как молча пропавшая серия.

Само доменное перечисление, однако, — тоже поддерживаемый вручную список: Go не
умеет перечислять члены строкового enum, поэтому реестр MUST рассматриваться
как **источник zero-fill**, а MUST NOT — как истина о множестве существующих
значений. Отсюда два требования:

- **Fail-safe экспозиции.** Пара, присутствующая в базе, MUST выводиться, даже
  если реестр её не содержит. Протухший реестр может стоить нулевой серии; он
  MUST NOT приводить к исчезновению серии, за которой стоят реальные строки.
- **Независимая проверка полноты.** Полнота реестра MUST проверяться источником,
  отличным от него самого — объявлениями констант или `CHECK`-ограничением
  схемы. Тест, который перебирает тот же helper, что и проверяет, доказывает
  лишь равенство списка самому себе и остаётся зелёным ровно в том случае,
  который он должен ловить.

`gatekeeper_admin_alerts` MUST оставаться разреженной по метке `kind`. Виды
тревог — строковые литералы в нескольких пакетах, замкнутого перечисления у них
нет, и реестр ради zero-fill протух бы при первой тревоге, заведённой мимо
него. Роль «серии, которая никогда не `NoData`» выполняет свёртка
`gatekeeper_admin_alerts_open{severity}`.

**Окно депрекации.** При переименовании метрики экспозиция MUST выводить и
новое, и старое имя одновременно в течение одного релиза, потому что дашборды
и правила алертов живут вне этого репозитория и переключаются отдельным
деплоем. Устаревшее имя MUST сопровождаться строкой
`# HELP <old> DEPRECATED: use <new>`. Устаревшее имя MUST NOT сохранять
прежнее объявление типа, если оно было неверным: алиас объявляется по реальной
природе метрики. Сэмплы каждого имени MUST выводиться одной смежной группой:
экспозиция обязана держать все сэмплы одного семейства рядом.

На время окна депрекации MUST выводиться алиасы:
`gatekeeper_outbox_actions_total`, `gatekeeper_updates_total`,
`gatekeeper_webhooks_total`, `gatekeeper_revocations_total`.

**Объявленное семейство MUST иметь источник.** Экспозиция MUST NOT содержать
`# TYPE` для метрики, которую не производит ни один путь кода. Когда источник
не подключён в данной конфигурации (например, Enforcer здесь не
супервизируется), семейство MUST отсутствовать целиком — и объявление, и
сэмплы, — а не выводиться пустым.

**Watchdog простоя MUST иметь значение с первой секунды жизни процесса.**
`gatekeeper_reconcile_last_run_age_seconds` MUST выводиться при каждом scrape, в
котором база прочитана, — независимо от того, завершался ли хоть один проход
reconcile. Пока `meta.reconcile.last_run_at` отсутствует, значением MUST быть
время, прошедшее с момента старта процесса: до первого завершённого прохода это
и есть длительность, в течение которой система обходится без reconcile. С
появлением ключа значение MUST считаться от него.

Это не исключение из правила «объявленное семейство MUST иметь источник», а
второй источник у того же семейства: серия определена в обоих состояниях.
Пропуск сэмпла до первого прохода MUST NOT считаться допустимым, даже если
порядок старта гарантирует проход до открытия HTTP-listener'а: потребитель —
правило простоя с `noDataState: Ok`, для которого отсутствующая серия
неотличима от здоровья, поэтому watchdog замолкал бы ровно в том состоянии, о
котором обязан сообщать. Порядок старта — свойство текущей реализации, а не
контракт экспозиции.

Метрики, вычисляемые в памяти процесса, MUST выводиться до метрик, читаемых из
базы: сбой чтения базы прерывает остаток ответа, а состояние фоновых подсистем
нужно оператору именно тогда, когда неисправна база.

**Сбой чтения базы MUST быть наблюдаем в самой экспозиции.** Неполный ответ —
это ответ, в котором серии, читаемые из базы, молча исчезли, а все правила
поверх них ушли в `NoData` при внешне здоровом таргете; лог такой сбой не
закрывает, потому что алертинг смотрит не в лог. Экспозиция MUST выводить
`gatekeeper_metrics_store_scrape_success` со значением `1` при полностью
прочитанной базе и `0` при любом обрыве чтения. Метрика MUST выводиться там и
только там, где источник базы подключён — на общих основаниях правила
«объявленное семейство MUST иметь источник».

Ответ при этом MUST оставаться `200`: код `5xx` выбросил бы и in-process
семейства, которые как раз и нужны оператору в момент отказа базы. Признаком
неполноты служит метрика, а не HTTP-статус.

`gatekeeper_telegram_api_errors_total` MUST иметь метки `method` и `category`,
где `category` — нормализованная категория ошибки Telegram. Метка `code`
MUST NOT использоваться: нормализация схлопывает ответ Bot API в категорию и не
сохраняет HTTP-код, а категория к тому же различает `timeout`, `canceled` и
`rate_limited`, чего код не даёт.

Пропускная способность outbox MUST измеряться счётчиком исходов, который ведёт
исполнитель действий в момент успешно записанного терминального перехода, а
MUST NOT выводиться из числа строк в очереди. Метка `result` MUST принимать
значения из закрытого множества. Переход, не записанный из-за перехваченного
lease, MUST NOT учитываться: исход принадлежит воркеру, который владеет
действием сейчас.

#### Scenario: Metrics disabled hides endpoint
- **WHEN** `METRICS_ENABLED=false`
- **THEN** `GET /metrics` returns `404`

#### Scenario: Metrics enabled returns Prometheus text
- **WHEN** `METRICS_ENABLED=true`
- **THEN** `GET /metrics` returns `200`
- **AND** the response is parseable as Prometheus text format

#### Scenario: Metrics labels do not expose users
- **WHEN** metrics are emitted for subscriptions, grants and actions
- **THEN** labels use enum values such as `platform`, `resource`,
  `state`, `type`, `status`, `result`, `method` and `category`
- **AND** labels do not include `tg_id`, username, email or raw errors

#### Scenario: Статус без строк выводится нулём
- **WHEN** в очереди нет ни одного действия в статусе `dead`
- **THEN** серия `gatekeeper_outbox_actions{status="dead",...}` присутствует
  со значением `0` для каждого типа действия
- **AND** правило поверх неё возвращает данные, а не `NoData`

#### Scenario: Новый тип действия появляется в метриках сам
- **WHEN** новое значение добавлено и в константы, и в доменный реестр типов
  действий
- **THEN** экспозиция выводит его пары с каждым статусом без правок в коде
  метрик

#### Scenario: Тип из базы, отсутствующий в реестре, не пропадает
- **WHEN** в базе есть строки с типом действия, которого нет в доменном реестре
- **THEN** экспозиция всё равно выводит серию для этой пары с её реальным
  значением
- **AND** протухший реестр стоит не более чем нулевых серий

#### Scenario: Полнота реестра проверяется независимо от него самого
- **WHEN** новое значение объявлено константой или добавлено в `CHECK` схемы,
  но забыто в реестре
- **THEN** проверка полноты падает
- **AND** проверка получает ожидаемый перечень из объявлений или схемы, а не из
  самого реестра

#### Scenario: Возраст reconcile выводится до первого завершённого прохода
- **WHEN** база прочитана, а ключ `meta.reconcile.last_run_at` ещё не записан ни
  одним проходом
- **THEN** серия `gatekeeper_reconcile_last_run_age_seconds` присутствует
- **AND** её значение равно времени, прошедшему с момента старта процесса
- **AND** после первого завершённого прохода значение считается от времени этого
  прохода

#### Scenario: Сбой чтения базы виден в экспозиции
- **WHEN** чтение базы во время scrape завершается ошибкой
- **THEN** ответ остаётся `200` и содержит in-process семейства
- **AND** `gatekeeper_metrics_store_scrape_success` равна `0`
- **AND** при полностью прочитанной базе та же метрика равна `1`

#### Scenario: Открытых critical-тревог нет
- **WHEN** ни одной открытой тревоги с severity `critical` не существует
- **THEN** `gatekeeper_admin_alerts_open{severity="critical"}` равна `0`
- **AND** серии для остальных severity также присутствуют

#### Scenario: Вид тревоги без строк не выводится
- **WHEN** тревог вида `invite_mode_degraded` не существует
- **THEN** серия с этой меткой `kind` отсутствует
- **AND** это не мешает свёртке по severity отвечать нулём

#### Scenario: Retention не создаёт всплеск пропускной способности
- **WHEN** cleanup удаляет старые `done`-строки из очереди
- **THEN** `gatekeeper_outbox_actions{status="done",...}` уменьшается, как и
  положено gauge
- **AND** `gatekeeper_outbox_actions_processed_total` не уменьшается

#### Scenario: Переименованная метрика доступна под обоими именами
- **WHEN** экспозиция отдаётся в течение окна депрекации
- **THEN** одни и те же сэмплы присутствуют и под честным именем, и под
  устаревшим
- **AND** устаревшее имя сопровождается `# HELP ... DEPRECATED: use <new>`
- **AND** сэмплы каждого имени идут одной смежной группой

#### Scenario: Остановившийся пул воркеров виден в метриках
- **WHEN** один из воркеров не завершил ни одной итерации дольше порога
  залипания
- **THEN** `gatekeeper_enforcer_workers_stale` больше нуля
- **AND** `gatekeeper_enforcer_max_cycle_age_seconds` превышает порог

#### Scenario: Пул не супервизируется в этой конфигурации
- **WHEN** HTTP-сервер работает без источника состояния Enforcer'а
- **THEN** ни одна серия семейства `gatekeeper_enforcer_*` не выводится
- **AND** для них не выводится и `# TYPE`
- **AND** ответ отдаётся без ошибки

#### Scenario: Очередь просрочена
- **WHEN** действие, чей `run_after` уже прошёл, не выполняется
- **THEN** `gatekeeper_outbox_oldest_due_age_seconds` растёт
- **AND** действие, запланированное на будущее, в эту метрику не попадает

#### Scenario: Таймауты Telegram видны до того, как очередь встала
- **WHEN** вызовы Bot API завершаются таймаутом
- **THEN** `gatekeeper_telegram_api_errors_total{category="timeout"}` растёт
- **AND** метка `method` называет метод Bot API
