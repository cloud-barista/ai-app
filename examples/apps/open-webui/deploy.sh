#!/usr/bin/env bash
# Starts Open WebUI from docker-compose.yml in this folder. Run it from this
# folder; .env here must set OLLAMA_BASE_URL.
set -euo pipefail

if [ -f .env ]; then
	set -a
	# shellcheck source=/dev/null
	. ./.env
	set +a
fi

if ! docker compose version >/dev/null 2>&1; then
	echo "docker compose is not available on this node" >&2
	exit 1
fi

docker compose up -d
docker compose ps
echo "Open WebUI listens on port ${OPEN_WEBUI_PORT:-3000} of this node."
