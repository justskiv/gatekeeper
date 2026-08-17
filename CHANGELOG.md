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
- A `/livez` endpoint that answers whether the bot is still working its queue.
  Unlike `/readyz`, it never touches the database or Telegram, so it stays
  green through a passing outage and turns red only when the bot has genuinely
  stopped delivering — including the case where a worker is stuck mid-request
  rather than gone. `/readyz` now fails on a stopped worker pool too.

### Changed

- Telegram bot replies now use formatted, safer copy for users and owners.
- `/metrics` now tells the truth about the bot's own health. Counts that were
  labelled `_total` while shrinking whenever old rows were cleaned up are
  renamed without the suffix, and a real counter of completed work sits
  alongside them — so a graph of "actions per hour" no longer invents spikes out
  of routine housekeeping. The old names keep being published for one release,
  so dashboards can be switched over without a gap in coverage. New signals
  cover how long the queue has been waiting, whether the workers are still
  turning, how many operator alerts are open, and how often Telegram is timing
  out. A status with nothing in it now reports zero instead of vanishing, so an
  alert on "any failed action" works before the first failure rather than after
  it. A scrape that cannot read the database now says so in the response
  instead of quietly returning a shorter one, so a broken database can no
  longer look like a healthy bot with nothing to report. "Time since the last
  housekeeping pass" is published from the moment the bot starts rather than
  only once a pass has finished, so a watchdog for a stalled pipeline cannot
  read a bot that has never run one as healthy.

### Fixed

- A Telegram request timeout no longer stops the outbox worker pool. Timeouts
  are retried like any other transient failure, so the bot keeps delivering
  invites, kicks and messages through an API outage instead of going quietly
  idle while its queue grows.
- A worker that dies is now restarted and reported instead of disappearing
  silently, so a stalled queue surfaces in minutes rather than hours.
- An action that runs longer than its lease can no longer be executed twice.
  A slow Telegram call is now cut short before the queue can hand the same work
  to a second worker, so a stalled request no longer costs the subject two
  identical messages or a repeated ban.
- A message the bot had already delivered when it was told to stop is no longer
  sent a second time after the restart.
- Rare, isolated worker restarts no longer add up: the bot stops itself only
  when restarts crowd into one short window, not when a handful of them are
  spread across a day of otherwise normal operation.
- Adding the bot to an unknown chat no longer sends the owner several
  duplicate discovery notices; the alert now fires only on the join.
- Alerts that resolve on their own no longer deliver their notification
  afterwards. A warning about lost chat rights is dropped once the rights come
  back, instead of arriving hours later and sending the owner to fix a problem
  that is already over. This now holds for rights the bot notices losing in
  real time as well, not only for those found by a periodic check. A repeated
  failure for a problem already reported now reuses the same notification
  rather than queueing another copy.
- One lost chat right now costs the owner one message instead of two. The bot
  used to send both a generic operator alert and a specific warning about the
  very same failure; only the message that names the chat and the reason is
  kept. Alerts the bot has no specific wording for are unaffected, and an
  operator log chat, when configured, still gets its entry.

### Security

- Subscriber messages never expose internal access reasons, chat IDs, or
  raw errors; such diagnostics stay in owner-only replies.
- The bot token is stripped from the whole text of a failed Telegram request
  before it is logged or stored, not only from the address inside it, so a
  network error can no longer carry the token into a log file or the database.
