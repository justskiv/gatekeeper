# config Specification

## Purpose

Defines the environment-driven configuration contract shared by the
serving binary and operational CLIs.
## Requirements
### Requirement: Config is loaded from the environment

The application MUST read its configuration from environment
variables. In development a `.env` file in the working directory
MUST be loaded first as a source of defaults; a missing `.env` file
MUST NOT be treated as an error. Real environment variables MUST
take precedence over values from `.env`.

#### Scenario: .env present in working directory
- **WHEN** the process starts with a `.env` file in the working directory
- **THEN** variables from `.env` are loaded into the process environment
- **AND** real environment variables still take precedence over `.env`

#### Scenario: .env absent
- **WHEN** the process starts without a `.env` file
- **THEN** configuration is read from the process environment only
- **AND** no error is raised about the missing file

### Requirement: Required keys must be present

Loading MUST fail when any required key is absent or empty. The
required keys are: `BOT_TOKEN`, `OWNER_TG_IDS`, `BOOSTY_GROUP_ID`,
`TRIBUTE_CHANNEL_ID`, `CLUB_CHAT_ID`, `CLUB_CHANNEL_ID`,
`EVENT_LOG_CHAT_ID`, `BOOSTY_SUBSCRIBE_URL` and
`TRIBUTE_SUBSCRIBE_URL`.

#### Scenario: Required key missing

- **WHEN** any required key is absent or empty
- **THEN** loading fails with an error listing every missing key

### Requirement: Telegram chat IDs are negative and distinct

The four chat IDs MUST each parse as a negative `int64`, and they
MUST be pairwise distinct so the bot cannot mistake an observed
source for a managed club resource. The keys are
`BOOSTY_GROUP_ID`, `TRIBUTE_CHANNEL_ID`, `CLUB_CHAT_ID` and
`CLUB_CHANNEL_ID`.

#### Scenario: Non-negative chat ID
- **WHEN** any of the four chat IDs is `0` or positive
- **THEN** loading fails with `"<KEY> must be a negative chat ID"`

#### Scenario: Two chat IDs collide
- **WHEN** any two of the four IDs are equal
- **THEN** loading fails naming both keys and the shared value

### Requirement: OWNER_TG_IDS is a list of positive IDs

`OWNER_TG_IDS` MUST be a comma-separated list of positive `int64`
user IDs. An empty list, a missing key, or any non-positive entry
MUST be rejected.

#### Scenario: Empty owner list
- **WHEN** `OWNER_TG_IDS` is empty or contains only whitespace
- **THEN** loading fails with `"OWNER_TG_IDS is required"`

#### Scenario: Non-positive owner ID
- **WHEN** any entry is `<= 0`
- **THEN** loading fails naming the offending value

### Requirement: ADMIN_LOG_CHAT_ID is an optional operator alert target

Configuration MUST support optional `ADMIN_LOG_CHAT_ID` as a Telegram
chat id for operator alerts. When set, it MUST parse as a negative
`int64` chat id. When absent or empty, operator alerts MUST fall back to
DM delivery for `OWNER_TG_IDS`.

`ADMIN_LOG_CHAT_ID` MUST NOT be required for startup and MUST NOT be
part of the four configured source/club chat IDs. If it equals one of
the four source/club IDs, configuration MUST fail to avoid leaking
operator alerts into managed or observed resources.

#### Scenario: Admin log chat absent
- **WHEN** `ADMIN_LOG_CHAT_ID` is unset or empty
- **THEN** configuration loading succeeds
- **AND** operator alerts use owner DM delivery

#### Scenario: Admin log chat is negative
- **WHEN** `ADMIN_LOG_CHAT_ID=-1005555555555`
- **THEN** configuration loading succeeds
- **AND** alert delivery can target that chat id

#### Scenario: Admin log chat must not collide with managed chats
- **WHEN** `ADMIN_LOG_CHAT_ID` equals `CLUB_CHAT_ID`
- **THEN** configuration loading fails naming the collision

### Requirement: Conditional secrets are required for active modes

Mode flags MUST imply secrets:

- `TRIBUTE_MODE=webhook` MUST require `TRIBUTE_API_KEY`.
- `TELEGRAM_MODE=webhook` MUST require both
  `TELEGRAM_WEBHOOK_PUBLIC_URL` and `TELEGRAM_WEBHOOK_SECRET`.
- `INVITE_MODE=direct` MUST require `ALLOW_DIRECT_INVITES=true` and
  `INVITE_TTL <= 1h` (Telegram's cap on direct invite links).

#### Scenario: webhook mode without secret
- **WHEN** `TRIBUTE_MODE=webhook` is set without `TRIBUTE_API_KEY`
- **THEN** loading fails with a message naming the missing key

#### Scenario: Direct invites without the override flag
- **WHEN** `INVITE_MODE=direct` and `ALLOW_DIRECT_INVITES` is unset or false
- **THEN** loading fails with `"ALLOW_DIRECT_INVITES must be true when INVITE_MODE=direct"`

#### Scenario: Direct invites with a too-long TTL
- **WHEN** `INVITE_MODE=direct` and `INVITE_TTL > 1h`
- **THEN** loading fails with `"INVITE_TTL must be less than or equal to 1h"`

### Requirement: URLs and webhook paths are validated

URL keys MUST parse as absolute `http(s)` URLs, and webhook-path
keys MUST start with `/` so the handler does not silently
mismount. The URL keys are `BOOSTY_SUBSCRIBE_URL`,
`TRIBUTE_SUBSCRIBE_URL` and (when set) `TELEGRAM_WEBHOOK_PUBLIC_URL`;
the webhook-path keys are `TRIBUTE_WEBHOOK_PATH` and
`TELEGRAM_WEBHOOK_PATH`.

#### Scenario: URL without scheme or host
- **WHEN** a URL key is set to a value that is not an absolute http(s) URL
- **THEN** loading fails naming the key

#### Scenario: Webhook path without leading slash
- **WHEN** a webhook path key is set to a value not starting with `/`
- **THEN** loading fails naming the key

### Requirement: Durations, booleans, integers, enums and timezone are typed

Every value MUST be parsed into its target type.
`time.ParseDuration` MUST be used for durations and durations MUST
be strictly positive. Booleans MUST follow `strconv.ParseBool`.
Enums (`INVITE_MODE`, `TRIBUTE_MODE`, `TELEGRAM_MODE`,
`EXPIRY_MODE`, `LOG_LEVEL`, `LOG_FORMAT`) MUST accept only their
listed values. `ENFORCER_WORKERS` MUST be `> 0`.
`ADMISSION_JOIN_REQUEST_RETRIES` MUST be zero or greater. `TIMEZONE`
MUST load via `time.LoadLocation`.

#### Scenario: Invalid duration
- **WHEN** `GRACE_PERIOD` is `"not-a-duration"`
- **THEN** loading fails with `"GRACE_PERIOD must be a valid duration"`

#### Scenario: Enum out of set
- **WHEN** `LOG_LEVEL` is set to a value outside `{debug, info, warn, error}`
- **THEN** loading fails listing the allowed values

#### Scenario: Unknown timezone
- **WHEN** `TIMEZONE` is set to a name `time.LoadLocation` rejects
- **THEN** loading fails

### Requirement: Defaults are applied for optional keys

Optional keys MUST have defaults so a minimal `.env` is enough to
run. The defaults are:

- `DB_PATH` → `./data/gatekeeper.db`
- `INVITE_MODE` → `shared_join_request`, `INVITE_TTL` → `24h`
- `ADMISSION_FALLBACK_MAX_AGE` → `1h`,
  `ADMISSION_JOIN_REQUEST_RETRIES` → `2`
- `TRIBUTE_MODE` → `observation`
- `TRIBUTE_CANCEL_IS_IMMEDIATE` → `false`
- `WEBHOOK_LISTEN_ADDR` → `:8080`
- `TRIBUTE_WEBHOOK_PATH` → `/webhooks/tribute`
- `TELEGRAM_MODE` → `polling`
- `TELEGRAM_WEBHOOK_PATH` → `/webhooks/telegram`
- `TELEGRAM_WEBHOOK_PUBLIC_URL` → empty string
- `TELEGRAM_WEBHOOK_SECRET` → empty string
- `EXPIRY_MODE` → `grace`, `GRACE_PERIOD` → `72h`
- `RECONCILE_INTERVAL` → `1h`, `CLEANUP_INTERVAL` → `24h`
- `RAW_RETENTION` → `720h`, `AUDIT_RETENTION` → `8760h`
- `ENFORCER_WORKERS` → `2`
- `TIMEZONE` → `UTC`
- `LOG_LEVEL` → `info`, `LOG_FORMAT` → `json`,
  `METRICS_ENABLED` → `false`

#### Scenario: Optional key absent
- **WHEN** an optional key is unset
- **THEN** the parsed value equals the listed default

#### Scenario: Immediate Tribute cancellation defaults to safe behavior
- **WHEN** `TRIBUTE_CANCEL_IS_IMMEDIATE` is unset
- **THEN** the parsed value is `false`

### Requirement: All errors are reported together

Validation MUST accumulate every problem and surface them in a
single error of the shape
`"invalid configuration:\n  - <issue>\n  - ..."`. Loading MUST NOT
stop at the first error.

#### Scenario: Multiple validation problems
- **WHEN** a configuration has several independent errors
- **THEN** the returned error names each one on its own line

### Requirement: Tribute cancellation override is explicit

Configuration MUST expose `TRIBUTE_CANCEL_IS_IMMEDIATE` as a boolean
operator override. The default `false` means Tribute
`cancelled_subscription` records cancellation but keeps access until
`expires_at`. The value `true` means the webhook handler may convert
`cancelled_subscription` into immediate deactivation according to
status-core requirements.

Invalid boolean values MUST be reported with the same accumulated
configuration error format as other invalid values.

#### Scenario: Immediate cancellation enabled
- **WHEN** `TRIBUTE_CANCEL_IS_IMMEDIATE=true`
- **THEN** configuration loading succeeds
- **AND** the parsed config carries the immediate-cancel override

#### Scenario: Invalid immediate cancellation value
- **WHEN** `TRIBUTE_CANCEL_IS_IMMEDIATE=soon`
- **THEN** configuration loading fails
- **AND** the returned error names `TRIBUTE_CANCEL_IS_IMMEDIATE`

### Requirement: EVENT_LOG_CHAT_ID targets the operator event feed

Configuration MUST support `EVENT_LOG_CHAT_ID` as a Telegram chat id for
the operator event feed. `EVENT_LOG_CHAT_ID` MUST parse as a negative
`int64` chat id and MUST represent a dedicated Telegram group or
supergroup where the bot and the administrator are members.

`EVENT_LOG_CHAT_ID` MUST be distinct from `ADMIN_LOG_CHAT_ID` and from
the four configured source/club chat IDs: `BOOSTY_GROUP_ID`,
`TRIBUTE_CHANNEL_ID`, `CLUB_CHAT_ID` and `CLUB_CHANNEL_ID`. If it equals
any of those IDs, configuration MUST fail and name the collision.

`ADMIN_LOG_CHAT_ID` remains the target for ops alerts such as lost bot
rights, unsafe revoke and dead outbox actions. Operator event feed
messages MUST NOT be delivered through `ADMIN_LOG_CHAT_ID`.

#### Scenario: Event log chat is configured

- **WHEN** `EVENT_LOG_CHAT_ID=-1007777777777`
- **THEN** configuration loading succeeds
- **AND** operator event feed delivery targets that chat id

#### Scenario: Event log chat is required

- **WHEN** `EVENT_LOG_CHAT_ID` is unset or empty
- **THEN** configuration loading fails with
  `"EVENT_LOG_CHAT_ID is required"`

#### Scenario: Event log chat must be negative

- **WHEN** `EVENT_LOG_CHAT_ID=12345`
- **THEN** configuration loading fails with
  `"EVENT_LOG_CHAT_ID must be a negative chat ID"`

#### Scenario: Event log chat must not collide with admin alert chat

- **WHEN** `EVENT_LOG_CHAT_ID` equals `ADMIN_LOG_CHAT_ID`
- **THEN** configuration loading fails naming the collision

#### Scenario: Event log chat must not collide with managed resources

- **WHEN** `EVENT_LOG_CHAT_ID` equals `CLUB_CHAT_ID`
- **THEN** configuration loading fails naming the collision

#### Scenario: Event log chat must not collide with observed sources

- **WHEN** `EVENT_LOG_CHAT_ID` equals `BOOSTY_GROUP_ID`
- **THEN** configuration loading fails naming the collision

