## ADDED Requirements

### Requirement: Production deployment artifacts are provided

Repository MUST include production deployment artifacts:

- `deploy/gatekeeper.service` systemd unit;
- `deploy/Dockerfile` for a static Go binary without cgo;
- documentation showing how to run migrations before the serving
  binary.

The systemd unit MUST support `EnvironmentFile=`, set a stable working
directory next to the SQLite database, restart the service on failure,
run under a dedicated unprivileged `User=`/`Group=` and avoid embedding
secrets in the unit file.

The Docker image MUST run the `gatekeeper` binary as the container
entrypoint and MUST NOT require cgo at runtime. Runtime secrets MUST be
provided by environment variables, not baked into the image.

#### Scenario: systemd unit reads environment file
- **WHEN** operator installs `deploy/gatekeeper.service`
- **THEN** service configuration can point to an external env file
- **AND** the unit file itself does not contain bot or provider secrets

#### Scenario: Docker image builds static runtime
- **WHEN** operator builds `deploy/Dockerfile`
- **THEN** the image contains the compiled `gatekeeper` binary
- **AND** runtime configuration is supplied through environment variables

#### Scenario: Migration order is documented
- **WHEN** operator follows deployment docs
- **THEN** they run `migrate up` before starting `gatekeeper`
- **AND** an unmigrated database is treated as a startup error, not
  silently migrated by the serving binary

### Requirement: README gives an English quickstart for self-hosting

`README.md` MUST provide an English quickstart sufficient to bring up a
bot without reading the full product spec. It MUST cover installation,
configuration, migrations, running the bot, the four Telegram chats,
why the bot needs `can_invite_users` and `can_restrict_members`,
`TRIBUTE_MODE` selection, backup guidance and a manual verification
checklist.

Backup guidance MUST explain SQLite WAL considerations: use `VACUUM
INTO` or stop the bot before copying the database; if copying live files,
include `-wal` and `-shm` or checkpoint first. Backup copies MUST be
treated as secrets.

Known limitations MUST include Boosty lag up to about four weeks, lack
of a full Telegram member list, kick behavior that can remove messages,
and optional nature of Tribute webhook mode.

#### Scenario: Operator can configure required chats
- **WHEN** a new operator reads the README quickstart
- **THEN** they can identify the Boosty group, Tribute channel, club
  chat and club channel IDs
- **AND** they understand required bot admin rights for managed resources

#### Scenario: Backup instructions protect WAL state
- **WHEN** operator follows README backup guidance
- **THEN** they do not copy only the main SQLite file from a live WAL
  database without checkpointing or copying sidecar files

#### Scenario: Known limitations are visible
- **WHEN** operator evaluates the project before production use
- **THEN** README states the main v1 limitations and mode trade-offs

### Requirement: OSS metadata and security process are present

Repository MUST include:

- `LICENSE` with MIT terms;
- `CONTRIBUTING.md` describing build, test, lint, style expectations and
  how to add a new `Source`;
- `SECURITY.md` describing how to report vulnerabilities without
  disclosing secrets publicly.

These files MUST NOT contain generated-by/vendor attribution markers and
MUST NOT include secrets or real tokens.

#### Scenario: License is present
- **WHEN** repository is published
- **THEN** `LICENSE` declares MIT terms

#### Scenario: Contributor docs explain local checks
- **WHEN** contributor opens `CONTRIBUTING.md`
- **THEN** they can find the canonical build, test and lint commands
- **AND** they can find the expected pattern for adding a subscription
  source

#### Scenario: Security policy avoids public secret disclosure
- **WHEN** reporter finds a vulnerability
- **THEN** `SECURITY.md` tells them how to report it privately
- **AND** warns not to include bot tokens, provider keys or invite URLs
  in public issues

### Requirement: CI runs build, test and lint on push and pull requests

Repository MUST include GitHub Actions workflow
`.github/workflows/ci.yml`. The workflow MUST run on push and pull
request events and MUST execute the canonical build, test and lint
commands from `Taskfile.yml`.

CI MUST NOT require real Telegram or Tribute credentials. Test
configuration MUST use fake values or isolated temporary databases.

#### Scenario: CI validates pull requests
- **WHEN** a pull request is opened
- **THEN** GitHub Actions runs build, test and lint jobs

#### Scenario: CI does not require production secrets
- **WHEN** workflow runs in a fork or without repository secrets
- **THEN** build, test and lint still run with fake test configuration
