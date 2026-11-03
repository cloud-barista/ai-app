#!/usr/bin/env bash
# Starts Dify with its official docker compose setup (api, worker, web,
# postgres, redis, vector database, nginx and the rest), fetched at a pinned
# release. Run it from the folder it was copied to; it needs git and docker
# compose on the node.
#
# .env in this folder, which the deployment request writes, may set:
#   DIFY_PORT       host port of the Dify web interface (default 80)
#   VLLM_BASE_URL   OpenAI compatible model server, e.g. http://10.0.0.5:8000/v1.
#                   Dify takes model servers in its web interface, so this is
#                   printed at the end to be entered there.
#   DIFY_AGENT_LOCAL_SANDBOX_AUTH_TOKEN
#                   token the agent backend and the local sandbox of Dify share.
#                   Both refuse to start without one and Dify ships it empty,
#                   so when neither this nor dify-src/docker/.env sets it, a
#                   random token is generated on the node and kept in that
#                   .env, where a redeploy finds it again. It is never printed.
set -euo pipefail

DIFY_VERSION=1.18.0
SRC_DIR=dify-src

if [ -f .env ]; then
	set -a
	# shellcheck source=/dev/null
	. ./.env
	set +a
fi

if ! command -v git >/dev/null 2>&1; then
	echo "git is not installed on this node" >&2
	exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
	echo "docker compose is not available on this node" >&2
	exit 1
fi

if [ ! -d "$SRC_DIR" ]; then
	git clone --depth 1 --branch "$DIFY_VERSION" https://github.com/langgenius/dify.git "$SRC_DIR"
fi

cd "$SRC_DIR/docker"
if [ ! -f .env ]; then
	cp .env.example .env
fi
if [ -n "${DIFY_PORT:-}" ]; then
	sed -i "s/^EXPOSE_NGINX_PORT=.*/EXPOSE_NGINX_PORT=${DIFY_PORT}/" .env
fi

sandbox_token=${DIFY_AGENT_LOCAL_SANDBOX_AUTH_TOKEN:-}
if [ -z "$sandbox_token" ]; then
	sandbox_token=$(sed -n 's/^DIFY_AGENT_LOCAL_SANDBOX_AUTH_TOKEN=//p' .env | tail -n 1)
fi
if [ -z "$sandbox_token" ]; then
	sandbox_token=$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
fi
if grep -q '^DIFY_AGENT_LOCAL_SANDBOX_AUTH_TOKEN=' .env; then
	escaped=$(printf '%s' "$sandbox_token" | sed 's/[\\|&]/\\&/g')
	sed -i "s|^DIFY_AGENT_LOCAL_SANDBOX_AUTH_TOKEN=.*|DIFY_AGENT_LOCAL_SANDBOX_AUTH_TOKEN=${escaped}|" .env
else
	printf 'DIFY_AGENT_LOCAL_SANDBOX_AUTH_TOKEN=%s\n' "$sandbox_token" >>.env
fi
unset sandbox_token escaped

docker compose up -d
docker compose ps

echo "Dify ${DIFY_VERSION} listens on port ${DIFY_PORT:-80} of this node; open /install there to create the admin account."
if [ -n "${VLLM_BASE_URL:-}" ]; then
	echo "Add the model server in Settings > Model Provider > OpenAI-API-compatible with API endpoint URL ${VLLM_BASE_URL}"
fi
