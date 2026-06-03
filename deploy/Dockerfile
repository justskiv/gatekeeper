# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Build both binaries from the same source tree so one image can serve as
# either the bot or the migrate CLI via an entrypoint override. The build
# is CGO-free (modernc.org/sqlite), so no gcc/sqlite-dev are needed.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/gatekeeper ./cmd/gatekeeper && \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/migrate ./cmd/migrate

FROM alpine:3.20

# Label so deploy-time `docker image prune` targets only this service's
# dangling layers and leaves other services on the shared droplet alone.
LABEL com.gatekeeper.service="gatekeeper"

# tzdata is required: TIMEZONE (e.g. Europe/Moscow) is resolved at runtime
# and an image without zoneinfo would silently fall back to UTC.
# ca-certificates is needed for the Telegram HTTPS API.
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 gatekeeper

COPY --from=build /out/gatekeeper /usr/local/bin/gatekeeper
COPY --from=build /out/migrate /usr/local/bin/migrate
# Migrations are read from disk by the migrate CLI (os.DirFS), not embedded,
# so they must ship in the image.
COPY --from=build /src/migrations /app/migrations

RUN mkdir -p /app/data && chown -R gatekeeper:gatekeeper /app

USER gatekeeper
WORKDIR /app

# Liveness only: pgrep proves the bot process is up regardless of the
# Telegram transport mode (polling opens no HTTP listener). Readiness
# (/readyz) is deliberately NOT used here — it would fail the container on
# a transient Telegram getMe or reconciliation hiccup.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD pgrep gatekeeper || exit 1

ENTRYPOINT ["/usr/local/bin/gatekeeper"]
