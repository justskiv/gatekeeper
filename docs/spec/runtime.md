# Жизненный цикл процесса

Порядок старта `gatekeeper` от загрузки конфига до остановки по сигналу.
Читаемое зеркало спеки `runtime`.

## Порядок старта

`cmd/gatekeeper` выполняет шаги в фиксированном порядке:

1. Загрузить конфиг (`config.Load`).
2. Построить slog-логгер (`applog.New`) и сделать его default.
3. Определить Telegram-транспорт (`polling` или `webhook`) и нужен ли
   HTTP-сервер. HTTP поднимается, если `TELEGRAM_MODE=webhook`,
   `TRIBUTE_MODE=webhook` или `METRICS_ENABLED=true`.
4. Установить корневой контекст, отменяемый `SIGINT`/`SIGTERM`.
5. Открыть БД (`store.Open`).
6. Проверить схему (`store.CheckSchema`).
7. Создать Telegram-клиент и задать явный набор `allowed_updates`.
8. Выполнить `getMe`, чтобы проверить токен.
9. Собрать источники подписок (`source.Membership` для Boosty и
   Tribute, `source.Manual`) как `[]SubscriptionSource`. В режиме B
   Tribute source получает ledger reader для
   `subscriptions.expires_at`. Построить `engine` поверх источников и
   прокинуть его в роутер — чтобы `chat_member` источников-чатов
   доходил до `engine.handleEvent`.
10. Зарегистрировать меню команд (`setMyCommands`) и прогнать
    startup health для четырёх настроенных чатов.
11. Собрать `Outbox`, `Invite` service, `Enforcer`, `Reconciler`,
    cleanup service, Telegram router и, если нужен HTTP, webhook
    server.
12. Запустить Enforcer-воркеры под `errgroup.WithContext`.
13. Если `INVITE_MODE=direct`, создать
    `admin_alert(kind='invite_mode_degraded')`.
14. Если `INVITE_MODE=shared_join_request`, поставить `ensure_invite`
    для клубного чата и канала и дождаться по одной активной
    join-request ссылке (`creates_join_request=true`) на каждый
    resource в `invite_links`.
15. Выполнить один immediate Reconciler pass до запуска входящих
    Telegram updates: накопленные до старта due revocations
    обрабатываются через outbox-safe paths.
16. Запустить periodic Reconciler и cleanup ticker под тем же
    `errgroup`.
17. Если нужен HTTP-сервер, запустить его под тем же `errgroup`.
18. Если `TELEGRAM_MODE=polling`, запустить поллер под тем же
    `errgroup`; перед long polling он досканирует оставшиеся
    `pending`-обновления (recovery).
19. Если `TELEGRAM_MODE=webhook`, зарегистрировать Telegram webhook на
    `TELEGRAM_WEBHOOK_PUBLIC_URL + TELEGRAM_WEBHOOK_PATH` с
    `TELEGRAM_WEBHOOK_SECRET` и тем же набором `allowed_updates`;
    поллер при этом не запускается.
20. Ждать отмены контекста или ошибки любой фоновой подсистемы.
21. Отменить подсистемы, дождаться горутин Enforcer, Reconciler,
    cleanup, HTTP-сервера и poller/webhook transport и только потом
    закрыть БД.

Шаги 1–15 строго последовательны: каждый выполняется после успеха
предыдущего, и ошибка любого прерывает старт и попадает в `main`.
Исключение — startup health (шаг 10): он осознанно деградирует,
проблемы прав пишутся в `meta.health.*`, `admin_alerts`, лог и DM
владельцу, но процесс продолжает работать. Reconciler initial pass
(шаг 15) завершается до запуска poller loop или приёма webhook
traffic; его non-fatal per-chat/per-user проблемы оформляются как
`admin_alert`, а не падение процесса.

## Запрет неоднозначных chat ID

Старт отклоняет конфигурацию, где один Telegram `chat.id` одновременно
является source chat и managed club resource: иначе один update мог бы
уйти в два доменных обработчика. Проверка выполняется до запуска poller
loop, а ошибка называет конфликтующие ключи и общее значение. Это
гарантирует роутеру однозначную конфигурацию (см. [telegram-transport](telegram-transport.md)).

## Фоновые подсистемы

Фоновые подсистемы запускаются под `errgroup.WithContext`: поллер либо
Telegram webhook transport, опциональный HTTP-сервер, Enforcer-воркеры,
periodic Reconciler и cleanup ticker. Первая ошибка любой подсистемы
или сигнал завершения отменяют общий контекст и останавливают
остальных. Порядок остановки важен: отмена контекста → graceful HTTP
`Shutdown` с ограниченным дедлайном → ожидание горутин → `db.Close()`
последней операцией — закрывать БД, пока живы писатели, нельзя.

Поллеру не нужна отдельная финализация offset: `meta.update_offset`
durable после каждого сохранённого батча. Enforcer'у не нужна
финализация lease: незавершённые `running` actions подберутся после
истечения `locked_until`. Reconciler и cleanup ticker завершаются по
отмене контекста, не удерживая открытую DB-транзакцию. HTTP-сервер во
время shutdown перестаёт принимать новые запросы и доводит in-flight
запросы до конца, пока не истечёт его shutdown-контекст.

## Webhook и HTTP

`TELEGRAM_MODE=webhook` — поддерживаемый опциональный транспорт. При
нём вместо поллера runtime поднимает HTTP-сервер и регистрирует
Telegram webhook на `TELEGRAM_WEBHOOK_PUBLIC_URL +
TELEGRAM_WEBHOOK_PATH` с `TELEGRAM_WEBHOOK_SECRET` и тем же набором
`allowed_updates`; long polling loop при этом не запускается.

В дефолтном режиме (`TELEGRAM_MODE=polling`,
`TRIBUTE_MODE=observation`, `METRICS_ENABLED=false`) HTTP-server не
открывается: единственная долгоживущая внешняя подсистема — поллер.
Готовность выражается через startup health, `meta.health.*`,
`admin_alerts` и DM владельцу. `/healthz` и `/readyz` существуют, но
доступны только когда HTTP-сервер поднят (webhook или metrics
режимом); они переиспользуют `meta.health.*` и reconcile-сигналы (см.
[webhook-ops](webhook-ops.md)).

## Логирование

`applog` строит `log/slog.Logger` из `LOG_LEVEL`
(`debug`/`info`/`warn`/`error`) и `LOG_FORMAT` (`json`/`text`). Один и
тот же построитель используют оба бинаря (`gatekeeper` и `migrate`),
чтобы логи были одинаковой формы. `json` остаётся машинным форматом, а
`text` даёт компактный локальный вывод в консоль.

## Fatal-ошибки

Оба бинаря пишут неустранимую ошибку в stderr как `fatal: <error>` и
делают `os.Exit(1)`. stderr — надёжный канал: в момент обнаружения
ошибки логгер может быть ещё не настроен.

## Поведение

- Битый конфиг → `fatal: <error>` в stderr, выход `1`, БД не трогается.
- `TELEGRAM_MODE=webhook` → runtime поднимает HTTP endpoint, регистрирует
  Telegram webhook, long polling loop не запускается.
- Немигрированная БД (`ErrUnmigrated`) → выход `1`, сообщение велит
  выполнить `task migrate:up`.
- Неверный `BOT_TOKEN` → `getMe` падает, старт прерывается.
- Бот без прав в настроенном чате → health деградирует, но Enforcer,
  Reconciler и входной transport стартуют.
- Source chat id совпал с club resource id → старт падает с ошибкой
  конфигурации до запуска поллера, ошибка называет оба ключа.
- Сигнал после успешного старта → контекст отменяется, HTTP-сервер
  делает graceful `Shutdown`, горутины Enforcer, Reconciler, cleanup и
  poller/webhook transport завершаются, `db.Close()` отрабатывает
  последней до выхода с кодом `0`.
