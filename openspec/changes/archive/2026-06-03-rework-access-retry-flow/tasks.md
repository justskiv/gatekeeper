## 1. Raw update payload

- [x] 1.1 Remove `redact.JSONPayload` from the persisted update batch so `telegram_updates.payload_json` stores the raw Telegram update.
- [x] 1.2 Keep `redact` applied only at the logging boundary, never on the persisted or processed payload.

## 2. Remove access throttle

- [x] 2.1 Remove the per-user 30s `admissionRateLimiter` from the poller and the `rateLimited` preflight path.
- [x] 2.2 Drop the `RateLimited`/`AdmissionRateLimited` fields from `AccessRequest` and `RoutePreflight`.
- [x] 2.3 Make `accessMarker` unique per call (`UnixNano`) so every access request gets a reply; remove `rateLimitMarker`.

## 3. Retry callback in-place UX

- [x] 3.1 Add `AnswerCallbackQuery`, `EditMessageText`, `EditMessageReplyMarkup` to the Telegram client; treat `message is not modified` as success.
- [x] 3.2 Acknowledge a retry callback before the slow preflight: answer the query and swap the inline button to a "checking" label, best-effort.
- [x] 3.3 Thread an edit target (`chat_id`, `message_id`) from the callback through `AccessRequest`.
- [x] 3.4 Route an access-result reply to an in-place `edit_message` when an edit target is present; fall back to a fresh DM otherwise.
- [x] 3.5 Clear the inline keyboard on a non-retry result with a non-nil empty keyboard slice.

## 4. No-sub retry button and invite-unresolved message

- [x] 4.1 Carry the «Проверить ещё раз» retry button on the no-subscription reply.
- [x] 4.2 Add `InviteNotRecognized()` and use it for an invite-unresolved decline instead of the temporary-check message.

## 5. Decline-with-active-sub alert

- [x] 5.1 Raise `admin_alert(kind='join_declined_active_sub', severity='warning')` when an invite-unresolved decline targets a user with a fresh active subscription.
- [x] 5.2 Keep normal inactive declines non-alerting.

## 6. Subscription command pages

- [x] 6.1 Add `/boosty` and `/tribute` in-bot command pages with subscription links.
- [x] 6.2 Make `NoSub` a function carrying the Boosty/Tribute command entries.
- [x] 6.3 Register `/boosty` and `/tribute` in the user command scope via `SetMyCommands`.

## 7. edit_message outbox action

- [x] 7.1 Add `ActionEditMessage` action type and enqueue `edit_message` payloads from the handler.
- [x] 7.2 Execute `edit_message` in the Enforcer via `EditMessageText`.
- [x] 7.3 Add migration `0002_outbox_edit_message.sql` widening the `access_actions.action_type` CHECK to include `edit_message`.

## 8. Validation

- [x] 8.1 Run `go test ./...` and fix regressions.
- [x] 8.2 Run `openspec validate rework-access-retry-flow --strict`.
- [x] 8.3 Re-verify that every MODIFIED requirement preserves its baseline scenarios.
