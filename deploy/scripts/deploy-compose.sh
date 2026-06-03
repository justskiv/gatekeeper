#!/usr/bin/env bash
set -euo pipefail

# Required environment variables
: "${DIR:?DIR environment variable is required}"
: "${TAG:?TAG environment variable is required}"
: "${OWNER:?OWNER environment variable is required}"
: "${GHCR_READ_TOKEN:?GHCR_READ_TOKEN environment variable is required}"
: "${BOT_TOKEN:?BOT_TOKEN environment variable is required}"

# Optional secrets — empty in the default polling+observation deploy, only
# needed when the matching mode is set to webhook in config.env.
TRIBUTE_API_KEY="${TRIBUTE_API_KEY:-}"
TELEGRAM_WEBHOOK_SECRET="${TELEGRAM_WEBHOOK_SECRET:-}"

# Knobs with prod defaults.
COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-gatekeeper}"
METRICS_HOST_PORT="${METRICS_HOST_PORT:-8090}"
CONTAINER_NAME="${CONTAINER_NAME:-gatekeeper}"

# The DB lives on a host bind mount so backups (VACUUM INTO, or stop+copy of
# the db plus its -wal/-shm sidecars) reach it from the host. modernc.org/
# sqlite in WAL mode needs write access to the *directory*, not just the
# file, so chown it to the image's uid (1000).
echo "Preparing data directory..."
mkdir -p "$DIR/data"
chown -R 1000:1000 "$DIR/data"

cd "$DIR"

echo "Logging in to GitHub Container Registry..."
echo "$GHCR_READ_TOKEN" | docker login ghcr.io -u "$OWNER" --password-stdin

# Export everything docker compose interpolates from config + secrets.
export REGISTRY_OWNER="$OWNER"
export TAG COMPOSE_PROJECT_NAME METRICS_HOST_PORT
export BOT_TOKEN TRIBUTE_API_KEY TELEGRAM_WEBHOOK_SECRET

echo "Pulling image with tag: $TAG"
docker compose pull

echo "Starting containers..."
docker compose up -d --remove-orphans

echo "Waiting for $CONTAINER_NAME to become healthy..."
max_attempts=30
attempt=1
while [ $attempt -le $max_attempts ]; do
    health_status=$(docker inspect "$CONTAINER_NAME" --format='{{.State.Health.Status}}' 2>/dev/null || echo "unknown")

    if [ "$health_status" = "healthy" ]; then
        echo "Container is healthy!"
        break
    fi

    echo "Health check attempt $attempt/$max_attempts: $health_status"

    if [ $attempt -eq $max_attempts ]; then
        echo "ERROR: $CONTAINER_NAME failed to become healthy after $max_attempts attempts"
        docker compose logs --tail=50
        exit 1
    fi

    sleep 2
    ((attempt++))
done

echo "=== Container Status ==="
docker compose ps

echo "=== Health Check Details ==="
docker inspect "$CONTAINER_NAME" --format '{{json .State.Health}}' | jq .

# Prune dangling layers left over from the previous image of this deploy.
# Scoped by label so on a shared host only gatekeeper images are pruned,
# never other services. Only <none> layers go; tagged versions (vX.Y.Z)
# are kept for rollback. Tolerate failure so cleanup never fails an
# otherwise-healthy deploy.
echo "Pruning dangling gatekeeper images..."
docker image prune -f --filter "label=com.gatekeeper.service=gatekeeper" || true

echo "Deployment completed successfully!"
