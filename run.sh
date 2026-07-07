#!/usr/bin/env bash
# Rebuild and restart the telegram-anthropic-chat container, then attach to logs.
# Image and container names are read from docker-compose.yml, not hardcoded here.
set -euo pipefail

echo "==> Reading image name from docker-compose.yml..."
IMAGE="$(docker compose config --images | head -n1)"

echo "==> Stopping and removing existing compose containers..."
docker compose down --remove-orphans

echo "==> Removing existing image (if any)..."
if [ -n "${IMAGE}" ] && docker images -q "${IMAGE}" | grep -q .; then
  docker rmi -f "${IMAGE}"
fi

echo "==> Building and starting container..."
docker compose up -d --build

echo "==> Attaching to logs (Ctrl+C to detach, container keeps running)..."
docker compose logs -f
