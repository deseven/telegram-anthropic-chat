#!/usr/bin/env bash
# Rebuild and restart the telegram-anthropic-chat container, then attach to logs.
set -euo pipefail

IMAGE="telegram-anthropic-chat"
CONTAINER="telegram-anthropic-chat"

echo "==> Stopping docker compose services..."
docker compose stop

echo "==> Removing existing container (if any)..."
if docker ps -a --format '{{.Names}}' | grep -qx "${CONTAINER}"; then
  docker rm -f "${CONTAINER}"
fi

echo "==> Removing existing image (if any)..."
if docker images -q "${IMAGE}" | grep -q .; then
  docker rmi -f "${IMAGE}"
fi

echo "==> Building and starting container..."
docker compose up -d --build

echo "==> Attaching to logs (Ctrl+C to detach, container keeps running)..."
docker compose logs -f
