#!/usr/bin/env bash
# Takes a deployment down from its nodes (docker compose down; named volumes
# and the node folder stay) and waits until it ends. Opened ports stay open.
#
#   ./07-remove.sh deploymentId
set -euo pipefail
. "$(dirname "$0")/common.sh"

if [ $# -lt 1 ]; then
	sed -n '2,5p' "$0" >&2
	exit 2
fi
id=$1

record=$(aiapp DELETE "/deployments/${id}")
if [ "$(jq -r '.status' <<<"$record")" = removing ]; then
	record=$(wait_deployment "$id")
fi
summarize_deployment <<<"$record"
