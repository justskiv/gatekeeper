## 1. Operator Event Log Core

- [x] 1.1 Create `internal/operatorlog` with event kinds (incl. `banned_join_attempt`), event model, reason/admission-method/actor enums and writer interface.
- [x] 1.2 Implement durable writer that enqueues `send_dm` actions to `EVENT_LOG_CHAT_ID` only, atomically in the caller's transaction.
- [x] 1.3 Build idempotency keys from a stable workflow marker (event kind, tg_id, resource, join-request/confirmed-action marker or a source-stable Telegram/provider event time) — never from process wall-clock/render time.
- [x] 1.4 Separate failure classes: feed-build/render/template errors are logged and skip the event without rolling back the domain change; a built event's `access_actions` INSERT is atomic in the caller's transaction and a persistence failure follows normal tx rules.
- [x] 1.5 Unit tests: event-log-chat delivery, no fallback to `ADMIN_LOG_CHAT_ID`/DM, duplicate suppression, render-failure does not fail the domain op.

## 2. Message Rendering

- [x] 2.1 Add `messages` templates for all operator event kinds, owner-facing summary-first via the Telegram HTML renderer.
- [x] 2.2 Resource-specific wording: club chat joined/left vs club channel subscribed/unsubscribed.
- [x] 2.3 Leak tests: invite URLs, raw chat IDs, raw payload JSON, action IDs, idempotency keys and unescaped dynamic text are absent.

## 3. Transport And Runtime Wiring

- [x] 3.1 Extend Telegram membership routing to pass `ChatMemberUpdated.from` actor context into admission membership handling; raw invite URL stays in invite resolution only.
- [x] 3.2 Add `EVENT_LOG_CHAT_ID` config parsing, required-key validation and collision checks against source/club and `ADMIN_LOG_CHAT_ID`.
- [x] 3.3 Construct the operator event writer in `cmd/gatekeeper` after outbox deps; pass it through router/admission/engine/owner-command deps before transport starts.
- [x] 3.4 Implement the `chat-health` delta for `EVENT_LOG_CHAT_ID`: `meta.health.event_log` key, group/supergroup posting ability (member-with-send-rights is OK, admin not required), `severity='error'`, startup probe is non-fatal (warning, never blocks startup), and `my_chat_member` transitions raise/resolve a `bot_rights_lost`-style alert delivered via `ADMIN_LOG_CHAT_ID`/owner DM.
- [x] 3.5 Fix the `outbox-enforcer` 403 categorization: a `send_dm` with an explicit `payload.chat_id` group target (e.g. `EVENT_LOG_CHAT_ID`/`ADMIN_LOG_CHAT_ID`) MUST NOT mark `action.TGID` `dm_state='blocked'` nor count as delivered; route it to the permanent-failure/dead path + `outbox_action_dead` alert. This also fixes the latent admin-alert delivery bug.
- [x] 3.6 Route permanent delivery failures through the existing Enforcer `outbox_action_dead` alert path; no second sender loop, no best-effort direct sends.
- [x] 3.7 Tests: writer wired before poller/webhook; unreachable event-log chat does not block startup and raises a chat-health alert; group-target `send_dm` 403 goes dead + alert without touching subject `dm_state`.

## 4. Admission And Membership Events

- [x] 4.1 Emit `access_granted` only on an effective eligibility transition (engine, or admission fallback when not already logged), deduped per episode with the shared marker; admission that only issues invites/pending grants for an already-eligible user does NOT emit it (the later join emits membership events).
- [x] 4.2 Include all live active source reasons (or flagged fallback active subscriptions) in admission events.
- [x] 4.3 Emit resource membership events for approved join requests and managed `chat_member` joins/leaves; record `bot_link` vs `external` and safe external actor when available.
- [x] 4.4 On join/subscribe events, include the joining user's active access source reasons (boosty/tribute/manual) from stored subscriptions or last decision (no network probe); state source unknown when none is known.
- [x] 4.5 Emit `banned_join_attempt` on a hard-banned external join; emit leave/unsubscribe even when the grant is already `revoked` without overwriting it.
- [x] 4.6 Tests: active/fallback/repeat start, bot-admitted join, external join with/without actor, source reasons shown on join, chat leave, channel unsubscribe, banned-join.

## 5. Source And Access Lifecycle Events

- [x] 5.1 Emit `source_subscription_activated`/`_expired` from engine subscription handling (cancel without immediate deactivation is not expiry).
- [x] 5.2 Compare persisted effective access before/after local events; emit `access_granted` only on real non-active→active transitions; delegate loss to revocation flow.
- [x] 5.3 Ensure `unknown` observations and a still-active alternate source emit no false access-loss; hard-ban loss identifies hard-ban as the overriding reason.
- [x] 5.4 Tests: first active source, last source expiry, one source expiring while another stays active, Tribute cancel, unknown, hard-ban override.

## 6. Revocation And Owner Command Events

- [x] 6.1 Emit `access_lost` only when `revokeNow` actually revokes eligible grants (affected resources + reason); `access_loss_scheduled` on grace scheduling.
- [x] 6.2 Emit `access_kept` when a pending revocation is cancelled by active access; stay silent for active no-op recomputes and `notify_only`.
- [x] 6.3 Suppress access-loss for unknown final checks, protected resources (list only actually-revoked resources) and no-op revocations.
- [x] 6.4 Emit `manual_grant`/`manual_revoke`/`manual_ban`/`manual_unban` after confirmed owner commands; idempotent per confirm-callback; cancelled/expired confirmations emit nothing.
- [x] 6.5 Tests: immediate revoke, grace scheduling, notify-only, cancellation, protected skip, duplicate owner confirm, unban-does-not-grant.

## 7. Verification

- [x] 7.1 Run `openspec validate add-event-log-chat-merge --strict`.
- [x] 7.2 Focused Go tests for operatorlog, messages, admission, engine, bot commands, router and runtime wiring.
- [x] 7.3 Run `task test` and `task lint`.
- [x] 7.4 Manually review queued events: the rendered message text/body must not leak invite URLs, raw chat IDs or secret-like data; the routing `payload_json.chat_id` is allowed and MUST equal `EVENT_LOG_CHAT_ID` (never `ADMIN_LOG_CHAT_ID`/owner DM).
