## ADDED Requirements

### Requirement: Membership routing preserves actor and invite context

Telegram routing MUST preserve safe membership context from
`chat_member` updates for managed club resources. When Telegram provides
the actor (`ChatMemberUpdated.from`), router/admission context MUST carry
that actor as a Telegram user profile so operator event log can explain
whether the user joined by bot flow or was added by an external admin.

Router MUST also continue to pass `via_join_request` and the optional
invite link from the same raw update to admission handlers. The raw
invite URL MUST remain available only to invite resolution logic; it
MUST NOT be rendered in operator event messages.

#### Scenario: External actor reaches admission context

- **WHEN** a managed club `chat_member` update includes `from` for the
  admin who added a user
- **THEN** router passes that actor to the admission membership handler
- **AND** operator event rendering can use a safe actor label

#### Scenario: Missing actor remains unknown external context

- **WHEN** a managed club `chat_member` update has no actor field
- **THEN** admission receives no actor profile
- **AND** operator event log records external admission without naming
  an admin

#### Scenario: Invite URL is used for resolution only

- **WHEN** a managed membership update includes an invite link
- **THEN** router passes it to admission for bot-admission evidence
- **AND** operator event messages do not include the full invite URL
