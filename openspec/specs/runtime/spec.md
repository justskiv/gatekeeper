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
3. Если `TELEGRAM_MODE=webhook`, завершиться с понятной fatal-ошибкой
   до открытия БД.
4. Установить корневой контекст, отменяемый `SIGINT` и `SIGTERM`
   (`signal.NotifyContext`).
5. Открыть базу данных (`store.Open`).
6. Проверить схему (`store.CheckSchema`).
7. Создать Telegram-`Client` и задать явный `allowed_updates`.
8. Выполнить `getMe` — проверка токена.
9. Собрать источники подписок (`source.Membership` для Boosty и для
   Tribute в режиме A, `source.Manual`) как `[]SubscriptionSource`,
   построить `engine` поверх них и прокинуть его в роутер, чтобы
   `chat_member` источников-чатов доходил до `engine.handleEvent`.
10. Зарегистрировать команды (`setMyCommands`) и прогнать chat-health
    для четырёх настроенных чатов; проблемы здоровья деградируют
    (`admin_alert` + DM владельцу), **не** прерывая старт.
11. Запустить поллер под `errgroup.WithContext`, предварительно
    досканировав оставшиеся `pending`-обновления (recovery).
12. Ждать отмены контекста.
13. Остановить подсистемы (отмена контекста → дождаться горутины
    поллера), затем закрыть базу данных последней, в `defer` после
    остановки всех писателей.

Шаги 1–9 строго последовательны: каждый выполняется только после успеха
предыдущего, и ошибка любого из них прерывает старт и пробрасывается в
`main`. Шаг 10 (chat-health) — осознанно деградирующий: его проблемы не
прерывают старт.

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

#### Scenario: Движок и источники собраны до запуска поллера
- **WHEN** старт дошёл до запуска поллера
- **THEN** `[]SubscriptionSource` и `engine` уже построены и движок
  прокинут в роутер
- **AND** `chat_member` источника-чата маршрутизируется в
  `engine.handleEvent`

#### Scenario: Деградированный chat-health не прерывает старт
- **WHEN** chat-health на шаге 10 обнаруживает, что бот не администратор
  в одном из чатов
- **THEN** поднимается `admin_alert` и владельцу уходит DM
- **AND** старт продолжается и поллер запускается

#### Scenario: Чистая остановка дожидается поллера
- **WHEN** процесс получает `SIGINT` или `SIGTERM` после успешного
  старта
- **THEN** корневой контекст отменяется
- **AND** горутина поллера завершается до того, как выполнится
  deferred `db.Close()`, и процесс выходит с кодом `0`

### Requirement: Background subsystems run under a supervised errgroup

Фоновые подсистемы MUST запускаться под `errgroup.WithContext` (в этой
фазе — поллер; позже Enforcer, Reconciler, HTTP-сервер). Первая ошибка
любой подсистемы или сигнал отменяют общий контекст и останавливают
остальных. Порядок остановки: отмена контекста → дождаться завершения
горутин → `db.Close()` в самом конце (закрывать БД, пока живы писатели,
нельзя). Поллеру отдельная финализация offset не нужна: `update_offset`
durable после каждого батча.

#### Scenario: Ошибка подсистемы отменяет группу
- **WHEN** горутина поллера возвращает ошибку
- **THEN** контекст группы отменяется и ошибка пробрасывается в `main`

#### Scenario: Сигнал дожидается горутин до закрытия базы
- **WHEN** процесс получает сигнал завершения
- **THEN** контекст отменяется, горутина поллера возвращается
- **AND** `db.Close()` выполняется только после остановки горутин

### Requirement: Unsupported Telegram webhook mode fails fast

`gatekeeper` MUST при `TELEGRAM_MODE=webhook` завершаться с понятной
fatal-ошибкой, а не молча уходить в polling. Webhook-транспорт Telegram
в этой фазе не реализован, и тихий запуск в нём не обрабатывал бы
обновления.

#### Scenario: Запрошен webhook-транспорт
- **WHEN** конфигурация загрузилась с `TELEGRAM_MODE=webhook`
- **THEN** `gatekeeper` завершается с ненулевым кодом и ошибкой о том,
  что webhook-режим Telegram в этой фазе не поддержан
- **AND** поллер не запускается

### Requirement: Default polling runtime starts no HTTP server

`gatekeeper` MUST NOT поднимать HTTP-сервер в дефолтном режиме. При
`TELEGRAM_MODE=polling`, `TRIBUTE_MODE=observation` и
`METRICS_ENABLED=false` единственная долгоживущая внешняя подсистема —
поллер; readiness фазы выражается через startup health-checks,
`meta.health.*`, `admin_alerts` и DM владельцу. `/healthz`/`/readyz`
появятся в Фазе 07 и переиспользуют эти сигналы.

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

