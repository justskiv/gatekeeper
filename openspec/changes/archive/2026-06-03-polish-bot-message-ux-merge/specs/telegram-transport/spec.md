## MODIFIED Requirements

### Requirement: The telegram package exposes one concrete Client over the Bot API

Пакет `telegram` MUST экспортировать **один конкретный** тип `*Client` —
обёртку над `github.com/go-telegram/bot` — и не объявляет собственных
интерфейсов (§20.2). `Client` предоставляет методы `getMe`, `getChat`,
`getChatMember`, `sendMessage`, `setMyCommands`, `setWebhook`,
`deleteWebhook`, а также методы Bot API, нужные Enforcer'у:
`createChatInviteLink`, `revokeChatInviteLink`,
`approveChatJoinRequest`, `declineChatJoinRequest`, `banChatMember` и
`unbanChatMember`. Узкие интерфейсы объявляют пакеты-потребители у
себя.

`sendMessage` and `sendMessageWithReplyMarkup` MUST support the chosen
message parse mode for bot-authored formatted messages. Telegram HTML
parse mode (`models.ParseModeHTML`) MUST be used only for messages
produced by the centralized renderer. Plain text messages MUST be sent
without parse mode unless they are explicitly rendered as formatted
messages. Plain export/CSV messages MUST opt out of parse mode when
formatting would corrupt data.

#### Scenario: Поверхность конкретного клиента

- **WHEN** потребитель использует пакет `telegram`
- **THEN** ему доступен конкретный `*Client` с методами `getMe`,
  `getChat`, `getChatMember`, `sendMessage`, `setMyCommands`,
  `setWebhook`, `deleteWebhook`, `createChatInviteLink`,
  `revokeChatInviteLink`, `approveChatJoinRequest`,
  `declineChatJoinRequest`, `banChatMember` и `unbanChatMember`
- **AND** пакет `telegram` не экспортирует интерфейсов для этих методов

#### Scenario: Formatted message sets HTML parse mode

- **WHEN** the client sends a templated bot message
- **THEN** Telegram `sendMessage` receives `parse_mode="HTML"`
- **AND** the same parse mode is used when reply markup is attached

#### Scenario: Plain message has no parse mode

- **WHEN** the client sends a message marked as plain text
- **THEN** Telegram `sendMessage` receives no `parse_mode`
- **AND** raw `<`, `>` or `&` in that plain text are not interpreted as
  Telegram HTML

#### Scenario: Plain export can opt out

- **WHEN** the owner export path sends plain CSV text
- **THEN** the transport may send it without parse mode
- **AND** CSV content is not interpreted as Telegram formatting

## ADDED Requirements

### Requirement: Telegram HTML rendering is safe and centralized

Bot-authored formatted messages SHALL use Telegram HTML parse mode via
the centralized renderer, not ad hoc HTML concatenation in feature code.
Dynamic values inserted into HTML messages MUST be escaped before
rendering unless they are explicitly safe renderer fragments created by
the messages package.

The renderer MUST escape at least `<`, `>` and `&` in all dynamic text,
including values placed inside `<code>` or `<pre>` spans. The body-text
escape helper MUST be a fixed three-character replacer equivalent to
`strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")`. The
renderer MUST NOT use `html.EscapeString` for body text, because it also
rewrites quotes (`"`/`'`) into numeric entities and corrupts visible
copy. It MUST also avoid generating unsupported Telegram HTML tags.
Supported formatting SHOULD be limited to a small stable subset:
`<b>`, `<i>`, `<code>`, `<pre>` and `<a href="...">` where needed.

Links MUST be rendered safely. Link helpers MUST validate allowed URL
schemes and escape `href` attributes and labels separately. Unsafe URLs
such as `javascript:` or values with control characters MUST NOT be
rendered as clickable HTML links. URL text shown to users MAY be the raw
link only when it comes from trusted invite-link storage; link labels,
chat titles, usernames, reasons, audit details and alert details MUST
be escaped.

MarkdownV2 SHALL NOT be used for the main renderer because its escaping
surface is broader and more fragile for server-generated diagnostics.
Legacy Markdown SHALL NOT be used for new messages.

Explicit Telegram entities MAY be introduced later for narrowly scoped
rich text that would be safer without parse-mode parsing, but any such
builder MUST compute offsets in Telegram's required UTF-16 code units.

#### Scenario: User name cannot break HTML

- **WHEN** a dynamic username or display name contains `<`, `>`, `&` or
  quote-like characters
- **THEN** the rendered Telegram text remains valid HTML parse mode
- **AND** the dynamic value is displayed as text, not interpreted as a
  tag

#### Scenario: Admin detail cannot inject markup

- **WHEN** an alert detail, audit detail or admin reason contains HTML
  or Telegram markup characters
- **THEN** diagnostics render as escaped text
- **AND** Telegram does not reject the message because of malformed
  entities

#### Scenario: Unsafe link is not rendered as HTML

- **WHEN** a dynamic URL has an unsupported scheme, missing host or
  control characters
- **THEN** the renderer does not emit an `<a href="...">` link for it
- **AND** the value is rejected or shown as escaped plain text

#### Scenario: Usage placeholder is safe in HTML

- **WHEN** a command usage template contains a placeholder like
  `<tg_id|@username>`
- **THEN** the placeholder is rendered as escaped text or safe code
- **AND** Telegram does not interpret it as an HTML tag

#### Scenario: Unsupported tags are not emitted

- **WHEN** message templates are rendered
- **THEN** templates use only the allowed Telegram HTML subset
- **AND** tests fail if a template emits an unsupported tag

#### Scenario: Legacy Markdown is not used

- **WHEN** the codebase sends formatted Telegram text
- **THEN** it does not use legacy Markdown parse mode
- **AND** tests or static checks catch `models.ParseModeMarkdownV1` and
  raw `"Markdown"` usage in send paths
