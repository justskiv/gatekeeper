## ADDED Requirements

### Requirement: verify_member applies reconciliation observations safely

`verify_member` action MUST execute `getChatMember` through Enforcer's
rate-limited Telegram client and then apply the observation through the
same domain paths as normal membership events. Source resource
verification MUST update subscription observations and run
`recomputeAccess`; club resource verification MUST update
`access_grants` membership state without overriding `revoked` grants.

If Telegram verification fails with retryable errors, action MUST retry
according to Enforcer retry policy. If verification cannot be performed
because the bot lost rights or the API result is otherwise uncertain,
the domain result MUST be `unknown` and MUST NOT close subscriptions or
kick users.

#### Scenario: Source verify deactivation runs recomputeAccess
- **WHEN** `verify_member` for a source chat confirms the user is no
  longer a member
- **THEN** the matching subscription source is observed as `inactive`
- **AND** `recomputeAccess` runs through the usual safe revocation path

#### Scenario: Verify unknown preserves access
- **WHEN** `verify_member` receives a rights error or retry-exhausted
  uncertain result
- **THEN** the observation is treated as `unknown`
- **AND** active subscriptions and grants are not revoked

#### Scenario: Club verify does not overwrite revoked grant
- **WHEN** `verify_member` for a club resource observes the user is left
  but the grant is already `revoked`
- **THEN** grant state remains `revoked`
- **AND** the action completes as a domain no-op

### Requirement: hard_ban and unban actions support owner ban flows

Enforcer MUST execute `hard_ban` by calling `banChatMember` without a
follow-up `unbanChatMember`. It MUST execute `unban` by calling
`unbanChatMember` with `only_if_banned=true`. Both action types MUST be
idempotent and use the same expected no-op handling as other membership
actions.

`hard_ban` MUST NOT be executed against creator or administrator
members. If Telegram reports that the target is protected or already
not present, Enforcer MUST finish the action as expected no-op when the
domain state already records the ban, and MUST log a warning.

#### Scenario: Hard ban does not unban
- **WHEN** Enforcer executes `hard_ban` for a club resource and user
- **THEN** it calls `banChatMember`
- **AND** it does not call `unbanChatMember`

#### Scenario: Unban uses only_if_banned
- **WHEN** Enforcer executes `unban`
- **THEN** it calls `unbanChatMember` with `only_if_banned=true`
- **AND** already-unbanned target is treated as expected no-op

#### Scenario: Protected admin is not hard-banned by Enforcer
- **WHEN** `hard_ban` targets creator or administrator
- **THEN** Enforcer does not remove the user
- **AND** action finishes as expected no-op with warning
