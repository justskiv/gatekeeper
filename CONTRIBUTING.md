# Contributing

## Local Checks

Use the Taskfile commands as the canonical local and CI entry points:

```sh
task build
task test
task lint
```

Run `task migrate:up` before starting the serving binary against a new
or upgraded database. The application checks schema state at startup but
does not run migrations itself.

## Style

- Follow Effective Go and the Uber Go Style Guide.
- Keep package boundaries narrow: consumers define the small interfaces
  they need; concrete repositories and clients stay in their packages.
- Keep user-visible Russian Telegram text in `internal/messages`.
- Keep secrets, provider payloads and full invite links out of logs and
  persisted raw inbox rows.

## Adding A Subscription Source

Add a source by implementing the `engine.SubscriptionSource` consumer
interface in `internal/source`. The source should return a four-valued
verdict: `active`, `inactive`, `unknown` or `no_signal`.

Wire the source into `cmd/gatekeeper` only after configuration,
readiness and routing are explicit. Domain changes should still flow
through `engine.HandleEvent` or `engine.ApplyObservations`; outgoing
Telegram effects must go through durable outbox paths.
