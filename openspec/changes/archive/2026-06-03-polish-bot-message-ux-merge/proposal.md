## Why

Бот выходит в финальное состояние, а текущие Telegram-сообщения всё ещё
выглядят как plain text diagnostics: обычный пользователь может видеть
внутренние reasons, chat IDs и локальные статусы, а owner/admin получает
сырые enum-heavy сводки без явной структуры.

Нужно закрепить профессиональный message UX как продуктовый контракт:
пользовательские ответы должны быть спокойными, понятными и приватными,
а админские ответы должны быстро показывать состояние, влияние и
диагностику без перегруза и без риска сломать Telegram formatting.

## What Changes

- Ввести единый Telegram message style guide для user-facing и
  owner/admin-facing текстов: строгий тон, короткие блоки, умеренные
  emoji только как статусные маркеры, единые форматы дат и статусов.
- Переработать пользовательские тексты `/start`, `/help`, `/status`,
  admission, join-request, warning и revocation сообщений так, чтобы
  они отвечали на вопросы "что со статусом", "что делать дальше" и не
  раскрывали внутреннюю модель доступа.
- Запретить user-facing вывод внутренних `AccessDecision.Reasons`,
  source details, `chat_id`, raw provider/internal ids, `whitelist`,
  `system`, локальной БД и raw errors; такие details остаются только
  owner/admin diagnostics.
- Переработать owner/admin сообщения `/whois`, `/stats`, `/alerts`,
  `/chats`, `/help_admin`, confirmations и operator alerts в формат
  "summary first, diagnostics last".
- Выбрать HTML parse mode для серверно-сгенерированных сообщений и
  закрепить безопасное HTML escaping всех динамических значений.
- Обновить Telegram transport/outbox contract так, чтобы обычные
  `send_dm`, `send_invite` и messages with inline keyboard доставлялись
  с одинаковым parse mode и не ломались из-за пользовательских имён,
  reasons, titles, alert details или invite links.
- Добавить тестовые требования для escaping, parse mode, user/admin
  separation и отсутствия internal diagnostics в пользовательских
  сообщениях.
- Закрепить cutover-контракт durable-outbox: payload без `parse_mode`
  уходит plain, а уже стоящие в очереди сообщения не
  переинтерпретируются как HTML (иначе `400` и отравление outbox).
- Зафиксировать escaper (`strings.NewReplacer` на `& < >`, не
  `html.EscapeString`) и сделать разбиение длинных ответов
  тег-безопасным (резать по строкам, не внутри тега/сущности).

## Capabilities

### New Capabilities

- `bot-message-ux`: единый продуктовый контракт для текста, структуры,
  privacy boundary, emoji usage, user/admin message templates и
  diagnostics separation.

### Modified Capabilities

- `bot-commands`: user и owner command replies меняют observable text
  shape, `/status` перестаёт раскрывать internal reasons пользователю,
  а owner ops replies получают структурированный summary/diagnostics.
- `grant-access`: admission/join-request/revocation-facing тексты
  получают финальный user UX contract и не должны раскрывать внутренние
  причины отказа или временного сбоя.
- `telegram-transport`: исходящие сообщения должны поддерживать
  выбранный parse mode и безопасный rendering boundary для HTML.
- `outbox-enforcer`: durable `send_dm`/`send_invite` actions и inline
  keyboard messages должны сохранять/использовать formatting contract
  при фактической отправке Telegram API.

## Impact

- `internal/messages`: новые шаблоны сообщений, helpers для HTML
  rendering/escaping, единые human-readable labels, даты и status text.
- `internal/bot`: `/status`, `/whois`, owner ops и confirmation replies
  должны использовать новые user/admin templates.
- `internal/admission`, `internal/engine`, `internal/reconcile`,
  `internal/telegram/health`: user-facing и operator alert тексты
  должны перейти на новые шаблоны.
- `internal/telegram`: `Client.SendMessage` и
  `SendMessageWithReplyMarkup` должны выставлять parse mode или
  принимать message object, не теряя reply markup.
- `internal/enforcer`, `internal/notify`, `internal/store/alerts`:
  durable delivery payloads должны оставаться совместимыми с новым
  formatting contract.
- Tests: message snapshot/substring tests, transport tests for parse
  mode, escaping tests for dynamic fields and negative tests against
  user-facing internal detail leaks.
