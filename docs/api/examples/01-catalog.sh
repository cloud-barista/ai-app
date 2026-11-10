#!/usr/bin/env bash
# Lists what can be deployed: inference runtimes, models with their artifacts,
# and uploaded applications with their versions and ports.
#
#   ./01-catalog.sh
#
#   LIMIT=n    models and apps per page, 1 to 1000 (default 100)
#   OFFSET=n   models and apps to skip (default 0)
#
# The model and app lists are paged: this prints one page of each and the
# total. A catalog filled by the catalog sync holds more than 100 models, so
# read the next page with OFFSET=100, or add DEPLOYABLE=true to list only the
# models that have a runtime to deploy them with.
set -euo pipefail
. "$(dirname "$0")/common.sh"

page=(--data-urlencode "limit=${LIMIT:-100}" --data-urlencode "offset=${OFFSET:-0}")
filter=()
if [ -n "${DEPLOYABLE:-}" ]; then
	filter=(--data-urlencode "deployable=${DEPLOYABLE}")
fi

echo "# runtimes"
aiapp GET /runtimes | jq -r '.runtimes[] | "\(.id)  image=\(.image) port=\(.port) formats=\(.formats | join(","))"'

echo "# models (modelId / artifactId -> runtimeId)"
aiapp GET /models -G "${page[@]}" ${filter[@]+"${filter[@]}"} | jq -r '
	"\(.models | length) of \(.total) models from offset \(.offset)",
	(.models[] | . as $m | .artifacts[] |
	"\($m.id) / \(.id) -> \([.runtimes[].runtimeId] | join(","))  tasks=\($m.tasks // [] | join(",")) \(.source):\(.ref) \(.precision // "")")'

echo "# apps (name version ports)"
aiapp GET /apps -G "${page[@]}" | jq -r '
	"\(.apps | length) of \(.total) apps from offset \(.offset)",
	(.apps[] | .name as $n | .versions[] | "\($n) v\(.version) ports=\(.ports // [] | map(tostring) | join(","))")'
