## ADDED Requirements

### Requirement: Operator event messages use owner-facing templates

Package `messages` MUST provide templates for every operator event kind
defined by `event-log`. The templates MUST be owner-facing:
summary first, then impact, then safe diagnostics only when useful.

Operator event messages MUST use Telegram HTML renderer and the same
safe escaping rules as other formatted bot messages. They MUST be
distinct from user-facing admission/revocation messages and MUST NOT be
sent to ordinary users; delivery MUST target the dedicated event-log
group.

#### Scenario: Access-granted event has summary first

- **WHEN** an `access_granted` event is rendered
- **THEN** the first line summarizes who received access and why
- **AND** resources, admission method and safe diagnostics follow after
  the summary

#### Scenario: Membership event uses resource-specific wording

- **WHEN** a `club_channel_subscribed` event is rendered
- **THEN** the text uses channel subscription wording
- **AND** it is not phrased as joining the club chat

### Requirement: Operator event templates hide unsafe details

Operator event templates MUST NOT render full invite URLs, internal
chat IDs, raw provider payload, secrets, internal outbox IDs or raw
idempotency keys. Source reasons, admin reasons, usernames, tiers and
provider event labels MUST be escaped before rendering.

If an unsafe detail is present in the input event, the renderer MUST
omit it or replace it with a safe label such as invite mode, resource or
hash. The renderer MUST NOT attempt to redact by partial string
replacement after building HTML.

This rule governs the **rendered message text** only. The delivery
routing envelope — `payload_json.chat_id` set to `EVENT_LOG_CHAT_ID` —
is required for the Enforcer to route the message and is not rendered
content; it is allowed and is not a leak.

#### Scenario: Full invite URL is not rendered

- **WHEN** an operator event input includes an invite URL
- **THEN** the rendered message does not contain the URL
- **AND** the message can include invite mode and resource instead

#### Scenario: Raw chat ID is not leading content

- **WHEN** an operator event is rendered for a configured club resource
- **THEN** the message identifies the resource by role label
- **AND** raw chat IDs are absent from the ordinary event text

#### Scenario: Dynamic reason is escaped

- **WHEN** an admin reason is `"><b>owned</b>&`
- **THEN** the rendered operator event shows that reason as text
- **AND** Telegram HTML remains valid

### Requirement: Message tests protect operator event privacy

Automated tests MUST cover representative operator event templates and
privacy guardrails. Tests MUST fail if operator event messages leak full
invite URLs, raw chat IDs, provider payload JSON, `action_id`,
`idempotency_key`, or unescaped HTML from dynamic fields.

Tests MUST also verify that channel events and chat events use different
labels and that owner-facing messages remain formatted with
`ParseModeHTML`.

#### Scenario: Privacy regression is caught

- **WHEN** tests render an operator event with invite URL, raw chat id
  and unsafe detail
- **THEN** the rendered message omits unsafe values
- **AND** the test fails if those values appear

#### Scenario: Parse mode is preserved

- **WHEN** operator event delivery enqueues a message
- **THEN** payload includes `parse_mode="HTML"`
- **AND** Enforcer sends it as a formatted message
