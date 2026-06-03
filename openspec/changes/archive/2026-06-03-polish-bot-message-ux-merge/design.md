## Context

Текущая реализация уже централизует большинство текстов в
`internal/messages`, но доставляет их в Telegram как plain text.
Пользовательский `/status` показывает internal reasons из
`AccessDecision`, включая локальные причины, source details и chat IDs.
Owner/admin команды полезны операционно, но их output выглядит как
сырой dump enum/status/detail значений.

Этот change фиксирует не только copywriting, но и boundary между
product text, diagnostics и Telegram rendering. Пользователь должен
видеть состояние доступа и следующий шаг. Owner/admin должен видеть
summary, impact/action и diagnostics. Transport должен доставлять это
как formatted Telegram message без риска `Bad Request: can't parse
entities`.

## Goals / Non-Goals

**Goals:**

- Сделать user-facing сообщения приватными, понятными и пригодными для
  финального запуска.
- Сделать owner/admin сообщения строгими и сканируемыми без потери
  диагностической ценности.
- Выбрать один default formatting mode для Telegram и закрепить
  escaping rules.
- Провести formatting contract через direct replies, durable outbox,
  reply markup, invites и operator alerts.
- Добавить tests, которые ловят regression по privacy, escaping и parse
  mode.

**Non-Goals:**

- Не менять бизнес-логику доступа, агрегации статуса, revocation,
  reconciliation или invite resolution.
- Не добавлять локализацию на несколько языков.
- Не добавлять новые Telegram commands.
- Не переходить на documents/files для `/export`; CSV может остаться
  plain text в рамках существующего ограничения.
- Не вводить entitlements, тарифные правила или новые access scopes.

## Decisions

### 1. Use OpenSpec, not a free-form phase document

Это изменение хорошо ложится в OpenSpec: меняется observable behavior
команд, user privacy contract, operator alert contract и transport
formatting. Свободная фаза хуже, потому что не дала бы проверяемых
requirements для `/status`, outbox delivery и parse mode.

Alternative considered: отдельная docs-фаза без delta specs. Она проще,
но оставляет риск, что implementation later обновит тексты без
транспортного и privacy-контракта.

### 2. Use Telegram HTML as the default formatted parse mode

Для контролируемых серверно-сгенерированных сообщений выбираем HTML
(`models.ParseModeHTML`) как dialect централизованного renderer-а, а не
как глобальный флаг на любой `SendMessage`. Причина: в проекте много
динамических значений (`username`, reasons, alert details, chat titles,
usage placeholders, links, counters). HTML требует leaf-escaping
динамических значений (`<`, `>`, `&`, quotes for attributes), а
MarkdownV2 требует контекстного escaping большого набора символов и
чаще ломается на обычных diagnostics вроде `pending=3`,
`source=tribute`, `@name_test`, `/whois <tg_id|@username>` или URL.

Alternatives considered:

| Option | Pros | Cons |
| --- | --- | --- |
| HTML | readable templates, narrow escaping surface, good for server rendering | unsupported tags break delivery; placeholders with `<...>` must be escaped |
| MarkdownV2 | expressive, library helper exists | broad escaping surface; fragile with lists, dots, dashes, underscores and diagnostics |
| legacy Markdown | simple for old bots | legacy mode, no nesting and fewer entity types |
| explicit entities | strongest safety when generated correctly | offset bookkeeping is complex with UTF-8/Russian text and not needed yet |

HTML is the default. Legacy Markdown is disallowed for new messages.
MarkdownV2 remains only a future opt-in for a specific converter-backed
scenario. Explicit entities remain an alternative for links, mentions,
code/date spans or future rich text where parse-mode parsing becomes
too risky; if used, offsets must be computed in UTF-16 code units.

Plain text remains valid. CSV/export and any message not produced by
the renderer should be sent without parse mode.

### 3. Rendering belongs to messages, not transport

`internal/messages` should own final Telegram text and escaping helpers.
Transport should not try to escape arbitrary text globally because it
cannot know which parts are template markup and which parts are dynamic
values. The safe boundary is:

- templates may contain a small allowed HTML subset;
- dynamic leaf values are escaped before interpolation via a fixed
  3-char replacer (`&`→`&amp;`, `<`→`&lt;`, `>`→`&gt;`), never
  `html.EscapeString` for body text (it mangles quotes), and including
  values placed inside `<code>`/`<pre>`;
- links validate allowed schemes, reject unsafe URLs, escape `href` and
  escape labels separately;
- raw diagnostics are escaped and can be wrapped in `<code>` or `<pre>`;
- plain messages, especially CSV export, may opt out of parse mode.

This prevents double escaping and keeps user/admin copy testable.

### 4. Preserve formatting through durable delivery

Current durable payloads mostly carry `text`. The implementation should
extend the message envelope enough to preserve formatting intent through
router, notify, admission, engine, alerts and enforcer. The exact code
shape can be a small message struct or payload fields such as
`text`/`parse_mode`/`plain`; the requirement is that renderer-produced
formatted messages reach `SendMessageParams.ParseMode`, and explicit
plain messages do not.

Cutover-контракт durable-outbox:
payload без объявленного parse mode MUST всегда отправляться как plain —
он НЕ трактуется как дефолтный HTML. Это обратное правилу «empty →
default HTML», которое было бы опасным: сообщения, поставленные в
очередь до cutover, отрендерены как plain (а `AdminHelp` и
`AdminCommandUsage` буквально содержат `<tg_id|@username>`), поэтому их
переинтерпретация как HTML вызвала бы `can't parse entities` и отравила
бы outbox бесконечными ретраями. С `parse_mode=HTML` отправляются только
payload'ы, явно помеченные HTML; CSV/export и любая непомеченная legacy-
строка остаются plain. Тесты MUST покрывать существующие payload-пути,
чтобы старые plain-строки никогда не уходили как HTML.

### 5. Split public status from admin diagnostics

`AccessDecision.Reasons` stays important, but it is not user copy.
User `/status` should render public state in this order: status,
human reason and next step. It should cover active/no active
subscription/temporary check issue/blocked, known active platforms,
expiry dates, club resource state and action. `/whois` and alerts
remain the place for internal reasons, raw IDs and audit details.

This keeps troubleshooting available to the owner while removing
internal leakage from regular users.

### 6. Keep emoji as status markers, not decoration

Emoji are allowed only where they make the message easier to scan, from
a single fixed allowlist `✅ ⏳ 🚫 ❔ ⚠️ ℹ️`: users may use only
`✅`/`⏳`/`🚫`/`❔`, while `⚠️`/`ℹ️` are owner/admin-only (see the
`bot-message-ux` capability). At most one emoji per regular message;
owner/admin lists may use one marker per item title line when each item
is a separate alert or operational state. `⚠️` MUST NOT mark
user-facing uncertainty. Diagnostics, IDs, help/usage and raw data rows
stay plain.

## Risks / Trade-offs

- HTML templates can break Telegram delivery if a dynamic value or
  placeholder is not escaped → central helpers, tests with `<`, `>`,
  `&`, quotes and usage placeholders.
- Unsafe URLs can become misleading hidden links → URL allowlist,
  separate href/label escaping, and fallback to escaped plain text.
- Escaping at the wrong layer can double-escape text → renderer-owned
  escaping; transport only forwards parse mode.
- Existing text tests assert internal `/status` reasons → update tests
  to assert public text and add owner-only diagnostics tests.
- `/export` may be corrupted by parse mode → explicit plain-text opt-out
  for CSV/export.
- Admin diagnostics can become too hidden → require summary first, but
  keep diagnostics block with raw values where useful.
- Emoji can make operational alerts look noisy → bounded marker set and
  tests/snapshots for representative templates.
- User copy can imply blame when the system only has uncertainty →
  require neutral phrasing for unconfirmed payment/account failures.
- Admin alerts can become noisy if routine status uses urgent language
  → separate routine dashboard replies from actionable alerts and
  require action/no-action wording.

## Migration Plan

1. Add message rendering helpers and final templates behind tests.
2. Thread formatted message metadata through outbound reply and outbox
   payloads.
3. Update Telegram client send paths to set `ParseModeHTML` for
   formatted messages and preserve reply markup.
4. Update user/admin handlers to use public/admin template data
   separately.
5. Update existing tests and add regression tests for privacy,
   escaping, parse mode and reply markup.
6. Run `go test ./...` and `openspec validate
   polish-bot-message-ux-merge --strict`.

Rollback is code-level: formatted templates can be reverted to plain
text by disabling parse mode at the envelope boundary, but privacy
changes to user `/status` should not be rolled back.

## Open Questions

- Should `/here` continue to answer in the current group with raw
  `chat.id`, or should it DM the owner to avoid exposing setup data to
  group members? Current specs allow group reply; implementation should
  decide whether to keep it as an explicit setup exception.
- Should owner diagnostics use `<pre>` blocks for large details or
  compact `<code>` inline fields? The default should be compact until
  messages approach Telegram length limits.

## References

- Telegram Bot API formatting options and bot features.
- go-telegram/bot `SendMessageParams` and `models.ParseModeHTML`.
- UX guidance reflected in this design: short plain-language UI text,
  no dead-end error messages, non-blaming temporary errors, and
  actionable low-noise operational alerts.

## Per-message redesign (mapping)

Конкретный маппинг сообщений с UX-инвариантами этого change'а (статус →
причина → шаг, без тупиков, non-blaming, admin summary-first). Маркеры
разметки абстрактны
(`<b>`, `<code>`); рендер — HTML. Эмодзи из фиксированного набора.
Каждое динамическое значение (помечено ⚠esc) проходит escaper, в т.ч.
внутри `<code>`.

### Пользовательские

| Сообщение | Глиф | Финальный вид |
|---|---|---|
| `/start` (Welcome) | — | `<b>` приветствие, абзац-инструкция, акцент *«с того же аккаунта»* курсивом, указатель на /status |
| ActiveShared | ✅ | `<b>Подписка активна.</b>` + список `• <b>Ресурс</b> — ссылка`; ссылка как `<a href>` через safe-link helper ⚠esc |
| ActiveDirect / InviteSoon | ⏳ | `<b>Подписка активна.</b>` + «готовлю персональные ссылки…»; один глиф ⏳ |
| Granted | ✅ | `<b>Доступ подтверждён.</b>` + «добро пожаловать» |
| TryLater (temporary check) | ⏳ | `<b>Не удалось проверить подписку.</b>` + *«временный сбой на нашей стороне, попробуйте позже»* + retry; не ❔/⚠️ |
| Banned | 🚫 | `<b>Доступ заблокирован.</b>` + «если ошибка — напишите владельцу»; ban reason пользователю не показываем |
| AlreadyIn | ✅ | `<b>Доступ уже выдан.</b>` + «вы уже во всех ресурсах» |
| PersonalInviteMisused | 🚫 | `<b>Ссылка не для вашего аккаунта.</b>` + «напишите со своего аккаунта» |
| Help | — | `<b>Как это работает</b>`, абзацы, **Важно:** + курсивный caveat, список только user-команд; без эмодзи; `&lt;…&gt;` экранированы |
| `/status` | ✅/🚫/❔ | статус → подписки (платформа+дата) → членство простыми словами → одна подсказка; **без** `Reasons`, chat id, enum, datastore-терминов |
| AccessKept | ✅ | `<b>Подписка снова активна.</b>` + «отзыв отменён» |
| ExpiryWarning | ⏳ | `<b>Подписка не найдена.</b>` + дедлайн **датой** + *«продлите, чтобы остаться»* |
| ExpiredNotice | ⏳ | `<b>Подписка не найдена.</b>` + «доступ пока сохранён, продлите» |
| Revoked | 🚫 | `<b>Доступ в клуб отозван.</b>` + путь возврата |

### Владельческие (summary → impact → action → diagnostics)

| Сообщение | Глиф | Финальный вид |
|---|---|---|
| Here (`/here`) | — | `<b>Этот чат</b>` + `ID/Тип` в `<code>` ⚠esc(type) |
| Whois (`/whois`) | 🚫/✅/❔ на строке статуса | summary (identity, статус, основание, гранты) → события → diagnostics-блок: полный `Reasons` дословно, сырые chat id/enum, аудит (≤8); машинные значения `<code>` ⚠esc |
| AdminConfirm | ⚠️ | summary-строки Действие/Цель/Срок/Причина; kind→русский глагол; id/срок в `<code>` ⚠esc(reason) |
| AdminConfirmed | ✅ | `<b>Готово.</b>` + эхо `/cmd 123` в `<code>`; для `/sync` — prose SyncSummary |
| AdminCancelled | — | `<b>Действие отменено.</b>` |
| AdminConfirmationExpired | ⏳ | `<b>Действие устарело.</b>` + «подтверждение живёт 5 минут» |
| OpsStats (`/stats`) | — | `<b>Сводка состояния</b>`, секции, счётчики `<code>`, enum'ы переведены; routine, без тревожных формулировок |
| OpsAlerts (`/alerts`) | severity→глиф | `<b>Открытые тревоги (n)</b>`, по строке: severity-глиф + проблема, kind/время в `<code>`; пустой случай `✅ Открытых тревог нет.` ⚠esc(title) |
| OpsChats (`/chats`) | — | `<b>Настроенные чаты</b>` + `• Роль: <code>id</code>` (роль впереди id) |
| SyncSummary | ✅/⚠️ | `<b>Сверка завершена[ с ошибками].</b>` + `• обработано/ошибок: <code>N</code>` |
| OperatorAlert | severity→глиф | summary проблемы → impact/action → diagnostics (Уровень/Тип в `<code>`, детали ⚠esc) |
| UnknownChat | ⚠️ | `<b>Бот добавлен в новый чат.</b>` + Название/ID/Тип + курсивная подсказка ⚠esc(title,type) |
| HealthFailure / HealthRestored | ⚠️ / ✅ | проблема/восстановление + Чат/ID; причина ⚠esc |
| AdminHelp (`/help_admin`) | — | `<b>Команды владельца</b>`, сгруппировано по задачам, синтаксис в `<code>`, `&lt;…&gt;` статически экранированы; без эмодзи |
| WhoisNotFound | ❔ | `<b>Пользователь {query} не найден.</b>` ⚠esc(query) |

Кнопки (`RetryAccessButtonText`, `AdminConfirmButtonText`,
`AdminCancelButtonText`) — без форматирования и без экранирования: текст
кнопки Telegram не парсит под parse mode.
