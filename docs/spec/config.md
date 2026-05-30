# Конфигурация

Типизированная загрузка конфига из окружения с агрегированной валидацией. Читаемое зеркало спеки `config`.

## Источник значений

Конфиг читается из переменных окружения. В разработке сначала подгружается `.env` из рабочей директории как источник дефолтов; отсутствие `.env` ошибкой не считается. Реальные переменные окружения перекрывают значения из `.env`.

## Обязательные ключи

Загрузка падает, если любой из них отсутствует или пуст (пустое = отсутствует): `BOT_TOKEN`, `OWNER_TG_IDS`, `BOOSTY_GROUP_ID`, `TRIBUTE_CHANNEL_ID`, `CLUB_CHAT_ID`, `CLUB_CHANNEL_ID`, `BOOSTY_SUBSCRIBE_URL`, `TRIBUTE_SUBSCRIBE_URL`.

## Правила значений

- **Chat ID** (`BOOSTY_GROUP_ID`, `TRIBUTE_CHANNEL_ID`, `CLUB_CHAT_ID`, `CLUB_CHANNEL_ID`) — каждый отрицательный `int64` и попарно различны, чтобы бот не спутал наблюдаемый источник с управляемым клубным ресурсом. Не-отрицательный → `"<KEY> must be a negative chat ID"`; коллизия → ошибка называет оба ключа и общее значение.
- **`OWNER_TG_IDS`** — список положительных `int64` через запятую. Пусто/пробелы → `"OWNER_TG_IDS is required"`; любой `<= 0` → ошибка с конкретным значением.
- **Условные секреты** — режим тянет за собой секрет: `TRIBUTE_MODE=webhook` требует `TRIBUTE_API_KEY`; `TELEGRAM_MODE=webhook` требует `TELEGRAM_WEBHOOK_PUBLIC_URL` и `TELEGRAM_WEBHOOK_SECRET`; `INVITE_MODE=direct` требует `ALLOW_DIRECT_INVITES=true` и `INVITE_TTL <= 1h` (лимит Telegram на direct-ссылки).
- **URL и пути** — URL-ключи (`BOOSTY_SUBSCRIBE_URL`, `TRIBUTE_SUBSCRIBE_URL`, при наличии `TELEGRAM_WEBHOOK_PUBLIC_URL`) парсятся как абсолютные `http(s)`; webhook-пути (`TRIBUTE_WEBHOOK_PATH`, `TELEGRAM_WEBHOOK_PATH`) начинаются с `/`, иначе handler тихо смонтируется не туда.
- **Типы** — durations через `time.ParseDuration`, строго `> 0`; булевы через `strconv.ParseBool`; enum'ы (`INVITE_MODE`, `TRIBUTE_MODE`, `TELEGRAM_MODE`, `EXPIRY_MODE`, `LOG_LEVEL`, `LOG_FORMAT`) — только из своего набора; `ENFORCER_WORKERS > 0`; `TIMEZONE` через `time.LoadLocation`.

## Дефолты опциональных ключей

Минимального `.env` с обязательными ключами достаточно для запуска.

| Ключ | Дефолт |
|---|---|
| `DB_PATH` | `./data/gatekeeper.db` |
| `INVITE_MODE` / `INVITE_TTL` | `shared_join_request` / `24h` |
| `TRIBUTE_MODE` / `WEBHOOK_LISTEN_ADDR` | `observation` / `:8080` |
| `TRIBUTE_WEBHOOK_PATH` | `/webhooks/tribute` |
| `TELEGRAM_MODE` / `TELEGRAM_WEBHOOK_PATH` | `polling` / `/webhooks/telegram` |
| `EXPIRY_MODE` / `GRACE_PERIOD` | `grace` / `72h` |
| `RECONCILE_INTERVAL` / `CLEANUP_INTERVAL` | `1h` / `24h` |
| `RAW_RETENTION` / `AUDIT_RETENTION` | `720h` / `8760h` |
| `ENFORCER_WORKERS` | `2` |
| `TIMEZONE` | `UTC` |
| `LOG_LEVEL` / `LOG_FORMAT` / `METRICS_ENABLED` | `info` / `json` / `false` |

## Отчёт об ошибках

Валидация копит все проблемы и выдаёт их одним сообщением вида `invalid configuration:\n  - <issue>\n  - ...`, не останавливаясь на первой.
