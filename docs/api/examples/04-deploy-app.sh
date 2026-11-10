#!/usr/bin/env bash
# Deploys an uploaded application onto a node and waits until it ends. Every
# KEY=VALUE argument goes into app.env, for example the inference URL that
# 05-endpoints.sh prints.
#
#   ./04-deploy-app.sh nsId infraId nodeId appName [KEY=VALUE ...]
#
#   ./04-deploy-app.sh etri travel vm2 open-webui OLLAMA_BASE_URL=http://203.0.113.12:11434
#
#   APP_VERSION=N    optional; default the latest version
#   APP_PORTS=a,b    optional; replaces the ports the version was uploaded with
#   SSH_USER         optional; targets[].sshUser, the SSH user to run the
#                    playbooks as instead of the CB-Tumblebug one
set -euo pipefail
. "$(dirname "$0")/common.sh"

if [ $# -lt 4 ]; then
	sed -n '2,14p' "$0" >&2
	exit 2
fi
ns=$1 infra=$2 node=$3 app=$4
shift 4

env_json='{}'
for pair in "$@"; do
	case "$pair" in
	*=*) env_json=$(jq --arg k "${pair%%=*}" --arg v "${pair#*=}" '. + {($k): $v}' <<<"$env_json") ;;
	*)
		echo "not KEY=VALUE: $pair" >&2
		exit 2
		;;
	esac
done

body=$(jq -n --arg ns "$ns" --arg infra "$infra" --arg node "$node" --arg app "$app" \
	--arg version "${APP_VERSION:-}" --arg ports "${APP_PORTS:-}" --arg user "${SSH_USER:-}" --argjson env "$env_json" '{
	type: "app",
	targets: [{kind: "node", nsId: $ns, infraId: $infra, nodeId: $node}
		+ (if $user != "" then {sshUser: $user} else {} end)],
	app: ({name: $app, env: $env}
		+ (if $version != "" then {version: ($version | tonumber)} else {} end)
		+ (if $ports != "" then {ports: ($ports | split(",") | map(tonumber))} else {} end))}')

record=$(aiapp POST /deployments -H 'Content-Type: application/json' -d "$body")
id=$(jq -r '.id' <<<"$record")
echo "deployment id: $id" >&2
wait_deployment "$id" | summarize_deployment
