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
9. Собрать источники подписок (`source.Membership` для Boosty и для
   Tribute в режиме A, `source.Manual`) как `[]SubscriptionSource`,
   построить `engine` поверх них и прокинуть его в роутер, чтобы
   `chat_member` источников-чатов доходил до `engine.handleEvent`.
10. Зарегистрировать команды (`setMyCommands`) и прогнать chat-health
    для четырёх настроенных чатов; проблемы здоровья деградируют
    (`admin_alert` + DM владельцу), **не** прерывая старт.
11. Собрать `Outbox`, `Invite` service и `Enforcer`.
12. Запустить Enforcer workers под `errgroup.WithContext`.
13. Если `INVITE_MODE=direct`, создать
    `admin_alert(kind='invite_mode_degraded')`.
14. Если `INVITE_MODE=shared_join_request`, поставить
    `ensure_invite` для клубного чата и канала и дождаться по одной
    активной ссылке в `invite_links` с `creates_join_request=true`.
15. Запустить поллер под тем же `errgroup.WithContext`, предварительно
    досканировав оставшиеся `pending`-обновления (recovery).
16. Ждать отмены контекста или ошибки любой фоновой подсистемы.
17. Остановить подсистемы (отмена контекста → дождаться горутин
    Enforcer и поллера), затем закрыть базу данных последней, в
    `defer` после остановки всех писателей.

Шаги 1–14 строго последовательны: каждый выполняется только после
успеха предыдущего, и ошибка любого из них прерывает старт и
пробрасывается в `main`. Шаг 10 (chat-health) — осознанно
деградирующий: его проблемы не прерывают старт.

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
- **AND** старт продолжается до запуска Enforcer и поллера

#### Scenario: Shared invite links готовы до poller loop
- **WHEN** `INVITE_MODE=shared_join_request` и startup успешен
- **THEN** в `invite_links` есть активная join-request ссылка для
  club chat
- **AND** есть активная join-request ссылка для club channel
- **AND** poller loop запускается только после этого

#### Scenario: Direct mode creates degraded alert
- **WHEN** startup успешен с `INVITE_MODE=direct`
- **THEN** в `admin_alerts` создано предупреждение
  `invite_mode_degraded`

#### Scenario: Чистая остановка дожидается фоновых подсистем
- **WHEN** процесс получает `SIGINT` или `SIGTERM` после успешного
  старта
- **THEN** корневой контекст отменяется
- **AND** горутины Enforcer и поллера завершаются до deferred
  `db.Close()`
- **AND** процесс выходит с кодом `0`

### Requirement: Background subsystems run under a supervised errgroup

Фоновые подсистемы MUST запускаться под `errgroup.WithContext`: в этой
фазе это поллер и Enforcer workers. Первая ошибка любой подсистемы или
сигнал отменяют общий контекст и останавливают остальных. Порядок
остановки: отмена контекста → дождаться завершения горутин →
`db.Close()` в самом конце (закрывать БД, пока живы писатели, нельзя).
Поллеру отдельная финализация offset не нужна: `update_offset` durable
после каждого батча. Enforcer не требует отдельной финализации lease:
незавершённые `running` actions будут подобраны после `locked_until`.

#### Scenario: Ошибка поллера отменяет группу
- **WHEN** горутина поллера возвращает ошибку
- **THEN** контекст группы отменяется
- **AND** Enforcer workers завершаются
- **AND** ошибка пробрасывается в `main`

#### Scenario: Ошибка Enforcer отменяет группу
- **WHEN** горутина Enforcer возвращает ошибку, несовместимую с
  продолжением работы
- **THEN** контекст группы отменяется
- **AND** poller завершается
- **AND** ошибка пробрасывается в `main`

#### Scenario: Сигнал дожидается горутин до закрытия базы
- **WHEN** процесс получает сигнал завершения
- **THEN** контекст отменяется, горутины Enforcer и поллера
  возвращаются
- **AND** `db.Close()` выполняется только после остановки горутин
