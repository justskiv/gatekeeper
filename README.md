# Gatekeeper

[![CI](https://github.com/justskiv/gatekeeper/actions/workflows/ci.yml/badge.svg)](https://github.com/justskiv/gatekeeper/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/justskiv/gatekeeper.svg)](https://pkg.go.dev/github.com/justskiv/gatekeeper)
[![Release](https://img.shields.io/github/release/justskiv/gatekeeper.svg)](https://github.com/justskiv/gatekeeper/releases)
[![Telegram](https://img.shields.io/badge/Telegram-@ntuzov-blue?logo=telegram&logoColor=white)](https://t.me/ntuzov)

[![Go Report Card](https://goreportcard.com/badge/github.com/justskiv/gatekeeper)](https://goreportcard.com/report/github.com/justskiv/gatekeeper)
[![codecov](https://codecov.io/gh/justskiv/gatekeeper/branch/main/graph/badge.svg)](https://app.codecov.io/gh/justskiv/gatekeeper)
[![Coverage Status](https://coveralls.io/repos/github/justskiv/gatekeeper/badge.svg?branch=main)](https://coveralls.io/github/justskiv/gatekeeper?branch=main)

Self-hosted Telegram access-control bot for paid subscriptions.
Gatekeeper observes Boosty and Tribute subscription sources, stores a
durable local access ledger and manages entry to a private club chat and
channel.

## Requirements

- Go 1.26+
- [Task](https://taskfile.dev) (`go-task`) for canonical commands
- SQLite is embedded through `modernc.org/sqlite`; no cgo or system
  SQLite is required

## Quickstart

```sh
task migrate:up
task build
./bin/gatekeeper
```

The serving binary never runs DDL. Run `task migrate:up` before the
first start and before serving after schema changes. If the database is
not migrated, startup fails with an instruction to run the migration
task.

Create an environment file or set variables directly. Keep `.env` files
at `0600`.

Required baseline variables:

- `BOT_TOKEN`
- `OWNER_TG_IDS`
- `BOOSTY_GROUP_ID`
- `TRIBUTE_CHANNEL_ID`
- `CLUB_CHAT_ID`
- `CLUB_CHANNEL_ID`
- `BOOSTY_SUBSCRIBE_URL`
- `TRIBUTE_SUBSCRIBE_URL_RUB`

Common optional variables:

- `DB_PATH=./data/gatekeeper.db`
- `INVITE_MODE=shared_join_request`
- `TRIBUTE_MODE=observation`
- `TELEGRAM_MODE=polling`
- `METRICS_ENABLED=false`
- `WEBHOOK_LISTEN_ADDR=:8080`
- `TRIBUTE_WEBHOOK_PATH=/webhooks/tribute`
- `TELEGRAM_WEBHOOK_PATH=/webhooks/telegram`

## Telegram Chats And Rights

Configure four different Telegram chats:

- Boosty group: observed source for Boosty membership.
- Tribute channel: observed source in `TRIBUTE_MODE=observation` and a
  secondary verification signal in `TRIBUTE_MODE=webhook`.
- Club chat: managed private chat for subscribers.
- Club channel: managed private channel for subscribers.

The bot must be a member of the source chats. In managed club resources
it must be an administrator with `can_invite_users` and
`can_restrict_members`; those rights are required to create join-request
links, approve requests and revoke access safely.

Use `/here` from an owner account inside a chat or supergroup to discover
its `chat.id`.

## Modes

Polling is the default Telegram transport and does not open an HTTP
listener.

HTTP starts only when at least one HTTP-facing mode is enabled:

- `TRIBUTE_MODE=webhook`
- `TELEGRAM_MODE=webhook`
- `METRICS_ENABLED=true`

Tribute webhook mode requires `TRIBUTE_API_KEY`. Requests are verified
with the `trbt-signature` HMAC over the raw request body. Telegram
webhook mode requires `TELEGRAM_WEBHOOK_PUBLIC_URL` and
`TELEGRAM_WEBHOOK_SECRET`; runtime registers the webhook explicitly with
Telegram.

`TRIBUTE_CANCEL_IS_IMMEDIATE=false` is the safe default: a Tribute
`cancelled_subscription` records cancellation but keeps access until
`expires_at`. Set it to `true` only as an explicit operator override.

## Operations

User commands:

- `/start`
- `/status`
- `/help`
- `/boosty`, `/tribute`

Owner commands:

- `/stats`
- `/alerts`
- `/chats`
- `/export`
- `/help_admin`
- `/whois`, `/grant`, `/revoke`, `/ban`, `/unban`, `/sync`

`/export` is owner-only and private-chat-only. It exports users and
current subscription state, not raw provider payloads or secrets.

When HTTP is enabled:

- `GET /healthz` is lightweight liveness.
- `GET /readyz` checks SQLite/schema, startup `getMe`, chat health,
  reconciliation freshness and Telegram webhook registration when used.
- `GET /metrics` is available only with `METRICS_ENABLED=true`.

## Deployment

The canonical path is a published container image deployed with Docker
Compose; a systemd unit is provided as an alternative. All deployment
artifacts live in `deploy/`; the full runbook is `deploy/DEPLOYMENT.md`.

For Docker Compose (GHCR):

1. Push a `v*` tag. The **Build & publish image** workflow builds and
   pushes `ghcr.io/<owner>/gatekeeper:<tag>` (and `:latest`).
2. Run the **Deploy** workflow (manual dispatch, optional `tag`). It
   copies the compose file, `config.env` and scripts to the droplet and
   brings up the `gatekeeper-migrate` (one-shot) and `gatekeeper`
   services. Non-secret configuration is committed in
   `deploy/config.production.env`; `BOT_TOKEN` and the optional webhook
   secrets are injected from the `Prod` GitHub Environment.

Metrics/health are bound to `127.0.0.1:8090` on the host. Do not bake
tokens or provider keys into the image.

For systemd:

1. Build and install `gatekeeper` to `/usr/local/bin/gatekeeper`.
2. Create a dedicated `gatekeeper` user and group.
3. Create `/var/lib/gatekeeper` owned by that user with `0700`.
4. Put environment variables in `/etc/gatekeeper/gatekeeper.env` with
   `0600`.
5. Run migrations with the same environment before starting the service.
6. Install `deploy/gatekeeper.service` and enable it.

For Docker, pass runtime configuration as environment variables or a
secret-mounted env file. Do not bake tokens or provider keys into the
image.

## Backups

Gatekeeper uses SQLite WAL mode. For consistent backups, either stop the
bot before copying the database or use `VACUUM INTO` from a controlled
maintenance session. If copying live files, include the main database,
`-wal` and `-shm` sidecars, or checkpoint before copying.

Database files and backups contain personal data and access state; treat
them as secrets.

## Known Limitations

- Boosty membership signals can lag by up to about four weeks.
- Telegram does not provide a full member list, so Gatekeeper relies on
  events, local state and targeted membership checks.
- Kicking a user from a Telegram chat can remove their recent messages,
  depending on Telegram behavior and client settings.
- Tribute webhook mode is optional; polling plus observation mode remains
  the simplest deployment.

## Development

```sh
task build
task test
task lint
task tidy
```

Current OpenSpec specs live in `openspec/specs/`; the product reference
document lives in `docs/gatekeeper-product-spec.md`.
