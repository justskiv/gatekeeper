## MODIFIED Requirements

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
9. Зарегистрировать команды (`setMyCommands`) и прогнать chat-health
   для четырёх настроенных чатов; проблемы здоровья деградируют
   (`admin_alert` + DM владельцу), **не** прерывая старт.
10. Запустить поллер под `errgroup.WithContext`, предварительно
   досканировав оставшиеся `pending`-обновления (recovery).
11. Ждать отмены контекста.
12. Остановить подсистемы (отмена контекста → дождаться горутины
    поллера), затем закрыть базу данных последней, в `defer` после
    остановки всех писателей.

Шаги 1–8 строго последовательны: каждый выполняется только после успеха
предыдущего, и ошибка любого из них прерывает старт и пробрасывается в
`main`. Шаг 9 (chat-health) — осознанно деградирующий: его проблемы не
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

#### Scenario: Деградированный chat-health не прерывает старт
- **WHEN** chat-health на шаге 9 обнаруживает, что бот не администратор
  в одном из чатов
- **THEN** поднимается `admin_alert` и владельцу уходит DM
- **AND** старт продолжается и поллер запускается

#### Scenario: Чистая остановка дожидается поллера
- **WHEN** процесс получает `SIGINT` или `SIGTERM` после успешного
  старта
- **THEN** корневой контекст отменяется
- **AND** горутина поллера завершается до того, как выполнится
  deferred `db.Close()`, и процесс выходит с кодом `0`

## REMOVED Requirements

### Requirement: Phase 01 runs no background subsystems

**Reason**: Эта фаза запускает Telegram-поллер как supervised
background subsystem, поэтому запрет фоновых подсистем из Фазы 01
больше не действует.

**Migration**: Жизненный цикл поллера теперь задан обновлённым
требованием порядка старта и новым требованием про supervised
`errgroup`. Enforcer и Reconciler подключатся к тому же `errgroup` в
следующих фазах.

## ADDED Requirements

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
