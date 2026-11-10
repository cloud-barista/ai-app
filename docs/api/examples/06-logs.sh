#!/usr/bin/env bash
# Prints, for every node of a deployment, each playbook run with its Semaphore
# task status and link, and the last lines of its output.
#
#   ./06-logs.sh deploymentId [lines]
#
#   lines   output lines per step, default 20
set -euo pipefail
. "$(dirname "$0")/common.sh"

if [ $# -lt 1 ]; then
	sed -n '2,7p' "$0" >&2
	exit 2
fi
id=$1
lines="${2:-20}"

aiapp GET "/deployments/${id}/logs" | jq -r --argjson n "$lines" '
	"deployment \(.id) status=\(.status)",
	(.nodes[] | "== \(.target.nsId)/\(.target.infraId)/\(.target.nodeId) \(.status)",
		(.steps[]? | "-- \(.playbook) task=\(.taskId // "-") status=\(.status // "-") \(.taskUrl // "")",
			(if .error then "   error: \(.error)" else empty end),
			((.output // [])[-$n:][] | "   \(.)")))'
