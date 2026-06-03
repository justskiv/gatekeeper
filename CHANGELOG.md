# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Telegram bot with `/start`, `/help`, and `/status` for subscribers.
- `/status` reports current subscription state and what to do next.
- Automatic club access via single-use join-request invite links.
- Boosty and Tribute subscriptions grant and revoke club access automatically.
- Tribute webhook mode for real-time subscription events, verified by HMAC.
- Owner reporting: `/stats`, `/alerts`, `/chats`, `/whois`, `/export`.
- Owner actions: `/grant`, `/revoke`, `/ban`, `/unban`, `/sync`.
- `/here` reports a chat's ID to help with configuration.
- `/help_admin` lists owner commands.
- `/healthz` and `/readyz` health endpoints, plus optional `/metrics`.

### Changed

- Telegram bot replies now use formatted, safer copy for users and owners.

### Security

- Subscriber messages never expose internal access reasons, chat IDs, or
  raw errors; such diagnostics stay in owner-only replies.
