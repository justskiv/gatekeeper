## 1. Message Rendering Foundation

- [x] 1.1 Add a small formatted-message model or envelope that can carry text, parse mode, plain-text opt-out and reply markup intent.
- [x] 1.2 Add HTML rendering helpers in `internal/messages` for escaped text, code spans, preformatted diagnostics and safe links; the body escaper is a fixed `strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")`, never `html.EscapeString` (it mangles quotes), applied even inside `<code>`/`<pre>`.
- [x] 1.3 Define the allowed Telegram HTML subset used by templates and keep dynamic escaping inside the renderer.
- [x] 1.4 Add tests for escaping `<`, `>`, `&`, quotes, underscores, dots, dashes, brackets and usage placeholders like `<tg_id|@username>`.
- [x] 1.5 Add safe URL helper tests for rejected schemes, control characters, empty hosts and escaped labels/href values.
- [x] 1.6 Add a guard test or static check that legacy Markdown parse mode is not used in send paths.

## 2. Telegram Delivery Pipeline

- [x] 2.1 Update Telegram client send paths to pass `models.ParseModeHTML` for formatted messages.
- [x] 2.2 Ensure plain messages, especially CSV/export, are sent without parse mode.
- [x] 2.3 Preserve parse mode for `SendMessageWithReplyMarkup` and retry-button messages.
- [x] 2.4 Thread formatting metadata through bot replies, router outbound messages and durable `send_dm` payloads.
- [x] 2.5 Thread formatting metadata through admission, engine notification, notify and alert-delivery payloads.
- [x] 2.6 Preserve formatting in Enforcer `send_dm` and `send_invite`, while keeping plain CSV/export messages unformatted.
- [x] 2.7 Add transport/enforcer tests proving parse mode survives direct, durable and reply-markup paths.
- [x] 2.8 Add message length checks or tests for Telegram text limits on representative long admin diagnostics.
- [x] 2.9 Treat a payload without a declared parse mode as plain (cutover safety): never send a legacy queued plain message with `parse_mode=HTML`, so a literal `<` in old text cannot poison the outbox.
- [x] 2.10 Make long-message splitting tag/entity-safe: split only on line boundaries, never inside a tag or HTML entity, keeping each fragment balanced; apply to `/whois` and other formatted responses.

## 3. User-Facing Message UX

- [x] 3.1 Replace `/start`, `/help` and `/status` templates with final user-facing Russian copy.
- [x] 3.2 Split user `/status` rendering from admin diagnostics so regular users never see internal reasons.
- [x] 3.3 Update admission and join-request templates for active, no subscription, temporary check issue, blocked, already joined, granted and personal invite misuse states.
- [x] 3.4 Update access kept, expiry warning, notify-only expiry notice and revoked templates with final product wording.
- [x] 3.5 Review every user-facing template for status, human reason and next step, avoiding dead ends and unproven blame.
- [x] 3.6 Add negative tests that user-facing messages do not contain raw internal terms, source verdicts, `chat_id`, `whitelist`, `system` or local database wording.

## 4. Owner/Admin Message UX

- [x] 4.1 Redesign `/whois` as summary-first card with diagnostics grouped after the main status.
- [x] 4.2 Redesign `/stats`, `/alerts`, `/chats` and `/help_admin` with structured sections and stable Russian labels.
- [x] 4.3 Redesign admin action confirmations and results for `/grant`, `/revoke`, `/ban`, `/unban` and `/sync`.
- [x] 4.4 Redesign unknown-chat discovery, chat health and operator alerts to include severity, impact, action and diagnostics.
- [x] 4.5 Ensure admin messages distinguish actionable alerts from routine status summaries and say when no action is required.
- [x] 4.6 Add tests or snapshots for representative owner/admin messages and diagnostics grouping.

## 5. Validation

- [x] 5.1 Update existing bot, admission, enforcer, telegram and messages tests for the new observable text contract.
- [x] 5.2 Run `go test ./...` and fix regressions caused by message rendering or payload changes.
- [x] 5.3 Run `openspec validate polish-bot-message-ux-merge --strict`.
- [x] 5.5 Re-verify that all MODIFIED requirements (telegram-transport, outbox-enforcer, bot-commands, grant-access) preserve their baseline scenarios.
- [x] 5.4 Manually review final templates for tone, privacy, emoji restraint and Telegram length limits.
