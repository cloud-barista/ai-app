#!/usr/bin/env bash
# Starts Langflow from docker-compose.yml in this folder. Run it from this
# folder; .env here may set MODEL_BASE_URL and LANGFLOW_PORT.
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
echo "Langflow listens on port ${LANGFLOW_PORT:-7860} of this node."
