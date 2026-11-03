#!/usr/bin/env bash
# Lists what can be deployed: inference runtimes, models with their artifacts,
# and uploaded applications with their versions and ports.
#
#   ./01-catalog.sh
set -euo pipefail
. "$(dirname "$0")/common.sh"

echo "# runtimes"
aiapp GET /runtimes | jq -r '.runtimes[] | "\(.id)  image=\(.image) port=\(.port) formats=\(.formats | join(","))"'

echo "# models (modelId / artifactId -> runtimeId)"
aiapp GET /models | jq -r '.models[] | . as $m | .artifacts[] |
	"\($m.id) / \(.id) -> \([.runtimes[].runtimeId] | join(","))  tasks=\($m.tasks // [] | join(",")) \(.source):\(.ref) \(.precision // "")"'

echo "# apps (name version ports)"
aiapp GET /apps | jq -r '.apps[] | .name as $n | .versions[] | "\($n) v\(.version) ports=\(.ports // [] | map(tostring) | join(","))"'
