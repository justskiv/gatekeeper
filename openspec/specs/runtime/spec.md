# runtime Specification

## Purpose

Описывает жизненный цикл процесса `gatekeeper`: загрузку конфигурации,
проверку готовности БД, запуск Telegram-подсистем и остановку по
сигналу.
## Requirements
### Requirement: The `gatekeeper` binary follows a fixed startup order

`cmd/gatekeeper` MUST управлять стартом в таком порядке:

1. Загрузить конфигурацию (`config.Load`).
2. Построить slog-логгер (`applog.New`) и назначить его default.
3. Установить корневой контекст, отменяемый `SIGINT` и `SIGTERM`
   (`signal.NotifyContext`).
4. Открыть базу данных (`store.Open`).
5. Проверить схему (`store.CheckSchema`).
6. Создать Telegram-`Client` и задать явный `allowed_updates`.
7. Выполнить `getMe` — проверка токена и readiness-сигнал.
8. Собрать источники подписок:
   `source.Membership` для Boosty, `source.Membership` для Tribute и
   `source.Manual` как `[]SubscriptionSource`. В режиме B Tribute source
   MUST получать ledger reader для `subscriptions.expires_at`.
9. Построить `engine` поверх источников и прокинуть его в роутер.
10. Зарегистрировать команды (`setMyCommands`) и прогнать chat-health
    для четырёх настроенных чатов; проблемы здоровья деградируют
    (`admin_alert` + DM владельцу), **не** прерывая старт.
11. Собрать `Outbox`, `Invite` service, `Enforcer`, `Reconciler`,
    cleanup service, Telegram router и, если нужен HTTP, webhook server.
12. Запустить Enforcer workers под `errgroup.WithContext`.
13. Если `INVITE_MODE=direct`, создать
    `admin_alert(kind='invite_mode_degraded')`.
14. Если `INVITE_MODE=shared_join_request`, поставить
    `ensure_invite` для клубного чата и канала и дождаться по одной
    активной ссылке в `invite_links` с `creates_join_request=true`.
15. Выполнить один immediate Reconciler pass до запуска входящих
    Telegram updates и webhook traffic.
16. Запустить periodic Reconciler и cleanup ticker под тем же
    `errgroup.WithContext`.
17. Если нужен HTTP server, запустить его под тем же
    `errgroup.WithContext`.
18. Если `TELEGRAM_MODE=polling`, запустить поллер под тем же
    `errgroup.WithContext`, предварительно досканировав оставшиеся
    `pending`-обновления (recovery).
19. Если `TELEGRAM_MODE=webhook`, зарегистрировать Telegram webhook на
    `TELEGRAM_WEBHOOK_PUBLIC_URL + TELEGRAM_WEBHOOK_PATH` с
    `TELEGRAM_WEBHOOK_SECRET` и не запускать поллер.
20. Ждать отмены контекста или ошибки любой фоновой подсистемы.
21. Остановить подсистемы (отмена контекста -> graceful HTTP shutdown,
    затем дождаться горутин Enforcer, Reconciler, cleanup, HTTP server и
    poller/webhook transport), затем закрыть базу данных последней, в
    `defer` после остановки всех писателей.

Шаги 1-15 строго последовательны: каждый выполняется только после
успеха предыдущего, и ошибка любого из них прерывает старт и
пробрасывается в `main`, кроме деградирующего chat-health внутри шага
10. Reconciler initial pass MUST завершиться до запуска poller loop или
приёма webhook traffic, но его non-fatal per-chat/per-user проблемы MUST
оформляться как `admin_alert`, а не падение процесса.

#### Scenario: Ошибка конфигурации
- **WHEN** `config.Load` возвращает ошибку
- **THEN** процесс пишет `"fatal: <error>"` в stderr, выходит с кодом
  `1` и не трогает базу данных

#### Scenario: Немигрированная база при старте
- **WHEN** `store.CheckSchema` возвращает ошибку `ErrUnmigrated`
- **THEN** процесс выходит с кодом `1`, а сообщение об ошибке
  подсказывает оператору выполнить `task migrate:up`

#### Scenario: Неверный токен прерывает старт
- **WHEN** `getMe` не проходит (например, неверный токен)
- **THEN** старт прерывается и процесс завершается с кодом `1`

#### Scenario: Движок и источники собраны до запуска входящих updates
- **WHEN** старт дошёл до запуска poller или Telegram webhook endpoint
- **THEN** `[]SubscriptionSource` и `engine` уже построены и движок
  прокинут в роутер
- **AND** `chat_member` источников-чатов доходит до корректного
  domain path для текущего `TRIBUTE_MODE`

#### Scenario: Деградированный chat-health не прерывает старт
- **WHEN** chat-health на шаге 10 обнаруживает, что бот не администратор
  в одном из чатов
- **THEN** поднимается `admin_alert` и владельцу уходит DM
- **AND** старт продолжается до запуска Enforcer, Reconciler и входного
  transport

#### Scenario: Shared invite links готовы до incoming transport
- **WHEN** `INVITE_MODE=shared_join_request` и startup успешен
- **THEN** в `invite_links` есть активная join-request ссылка для
  club chat
- **AND** есть активная join-request ссылка для club channel
- **AND** poller или Telegram webhook endpoint запускается только после
  этого

#### Scenario: Initial reconcile runs before incoming transport
- **WHEN** startup успешен и Enforcer workers запущены
- **THEN** один Reconciler pass выполнен до запуска poller или Telegram
  webhook endpoint
- **AND** due revocations, накопленные до старта, обработаны через
  outbox-safe paths

#### Scenario: Direct mode creates degraded alert
- **WHEN** startup успешен с `INVITE_MODE=direct`
- **THEN** в `admin_alerts` создано предупреждение
  `invite_mode_degraded`

#### Scenario: Telegram webhook mode does not start poller
- **WHEN** конфигурация загрузилась с `TELEGRAM_MODE=webhook`
- **THEN** runtime запускает HTTP endpoint для Telegram webhook
- **AND** long polling loop не запускается

#### Scenario: Чистая остановка дожидается фоновых подсистем
- **WHEN** процесс получает `SIGINT` или `SIGTERM` после успешного
  старта
- **THEN** корневой контекст отменяется
- **AND** горутины Enforcer, Reconciler, cleanup, HTTP server и
  poller/webhook transport завершаются до deferred `db.Close()`
- **AND** процесс выходит с кодом `0`

### Requirement: Background subsystems run under a supervised errgroup

Фоновые подсистемы MUST запускаться под `errgroup.WithContext`: поллер
или Telegram webhook transport, optional HTTP server, Enforcer workers,
periodic Reconciler и cleanup ticker. Первая фатальная ошибка любой
подсистемы или сигнал отменяют общий контекст и останавливают остальных.
Порядок остановки: отмена контекста -> graceful HTTP shutdown ->
дождаться завершения горутин -> `db.Close()` в самом конце (закрывать
БД, пока живы писатели, нельзя).

Поллеру отдельная финализация offset не нужна: `update_offset` durable
после каждого батча. Enforcer не требует отдельной финализации lease:
незавершённые `running` actions будут подобраны после `locked_until`.
Reconciler и cleanup ticker MUST завершаться по context cancellation
без удержания DB transaction. HTTP server MUST stop accepting new
requests during shutdown and allow in-flight requests to finish until
its shutdown context expires.

#### Scenario: Ошибка поллера отменяет группу
- **WHEN** горутина поллера возвращает ошибку
- **THEN** контекст группы отменяется
- **AND** Enforcer, Reconciler, cleanup и HTTP server завершаются
- **AND** ошибка пробрасывается в `main`

#### Scenario: Ошибка HTTP server отменяет группу
- **WHEN** HTTP server возвращает ошибку, отличную от normal shutdown
- **THEN** контекст группы отменяется
- **AND** poller/webhook transport, Enforcer, Reconciler и cleanup
  завершаются
- **AND** ошибка пробрасывается в `main`

#### Scenario: Ошибка Enforcer отменяет группу
- **WHEN** горутина Enforcer возвращает ошибку, несовместимую с
  продолжением работы
- **THEN** контекст группы отменяется
- **AND** poller/webhook transport, Reconciler, cleanup и HTTP server
  завершаются
- **AND** ошибка пробрасывается в `main`

#### Scenario: Ошибка Reconciler pass не ломает процесс при recoverable alert
- **WHEN** Reconciler обнаруживает потерю прав или `unknown` source
- **THEN** создаётся `admin_alert`
- **AND** подсистема продолжает работу, если ошибка recoverable

#### Scenario: Сигнал дожидается горутин до закрытия базы
- **WHEN** процесс получает сигнал завершения
- **THEN** контекст отменяется, HTTP server выполняет graceful shutdown,
  а горутины Enforcer, Reconciler, cleanup и poller/webhook transport
  возвращаются
- **AND** `db.Close()` выполняется только после остановки горутин

### Requirement: Runtime отклоняет неоднозначные source и club chat IDs

Startup MUST отклонять конфигурацию, в которой один Telegram `chat.id`
одновременно является source chat и managed club resource. Проверка
MUST выполняться до запуска poller loop, чтобы один Telegram update не
мог быть направлен в два доменных обработчика. Ошибка MUST называть
конфликтующие configuration keys и shared value.

#### Scenario: Source chat id совпадает с club resource id
- **WHEN** один и тот же `chat.id` настроен, например, как
  `BOOSTY_GROUP_ID` и `CLUB_CHAT_ID`
- **THEN** startup завершается ошибкой конфигурации до запуска poller
- **AND** ошибка называет оба конфликтующих key и shared value

### Requirement: Default polling runtime starts no HTTP server

`gatekeeper` MUST NOT поднимать HTTP-сервер в дефолтном режиме. При
`TELEGRAM_MODE=polling`, `TRIBUTE_MODE=observation` и
`METRICS_ENABLED=false` единственная долгоживущая внешняя подсистема —
поллер; readiness фазы выражается через startup health-checks,
`meta.health.*`, `admin_alerts` и DM владельцу. `/healthz`/`/readyz`
доступны только когда HTTP server включён webhook или metrics режимом.

#### Scenario: Чистый polling-режим не открывает HTTP-listener
- **WHEN** процесс стартует с polling, observation и выключенными
  метриками
- **THEN** HTTP-listener не открывается
- **AND** поллер — единственная запущенная фоновая интеграция

### Requirement: Both binaries share log configuration via `applog`

Пакет `applog` MUST строить `log/slog.Logger` из `Config.LogLevel`
(`debug`/`info`/`warn`/`error`) и `Config.LogFormat` (`json`/`text`).
Один и тот же построитель MUST использоваться бинарями `gatekeeper` и
`migrate`, чтобы все процессы писали логи в согласованном формате.

#### Scenario: Настроенный уровень логирования
- **WHEN** `LOG_LEVEL=debug`
- **THEN** `applog.New` возвращает логгер, чей handler пропускает записи
  уровня `Debug` и выше

#### Scenario: Настроенный формат логирования
- **WHEN** `LOG_FORMAT=text`
- **THEN** `applog.New` возвращает логгер с текстовым handler'ом,
  пишущим в stdout; иначе handler использует JSON

### Requirement: Fatal errors print to stderr and exit non-zero

Оба бинаря MUST использовать единый формат fatal-ошибки:

```
fatal: <error>
```

Строка пишется в stderr, затем процесс вызывает `os.Exit(1)`. В момент
обнаружения fatal-ошибки логгер может ещё не быть настроен, поэтому
stderr MUST оставаться надёжным каналом.

#### Scenario: Любая неустранимая ошибка из `run`
- **WHEN** внутренняя функция `run()` возвращает ненулевую ошибку
- **THEN** `main` пишет `"fatal: <message>"` в stderr и выходит с
  кодом `1`

