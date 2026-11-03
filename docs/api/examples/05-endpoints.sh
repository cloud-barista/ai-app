#!/usr/bin/env bash
# Lists the inference services and applications serving on the nodes now, with
# their URLs and whether their ports were opened.
#
#   ./05-endpoints.sh [nsId] [infraId] [nodeId]
#
#   TYPE=inference|app   optional filter
set -euo pipefail
. "$(dirname "$0")/common.sh"

query=()
for pair in "nsId=${1:-}" "infraId=${2:-}" "nodeId=${3:-}" "type=${TYPE:-}"; do
	if [ -n "${pair#*=}" ]; then
		query+=(--data-urlencode "$pair")
	fi
done

aiapp GET /endpoints -G ${query[@]+"${query[@]}"} | jq -r '
	if (.endpoints | length) == 0 then "no endpoint serving" else
	.endpoints[] | "\(.nsId)/\(.infraId)/\(.nodeId) \(.workKey) deployment=\(.deploymentId) portsOpened=\(.portsOpened)",
		(if .type == "inference" then "  \(.runtimeId) \(.modelId)/\(.artifactId) url=\(.url) health=\(.healthUrl // "")"
		else "  \(.name) v\(.version) " + ([.urls[]?.url] | join(" ")) end),
		(if .portsError then "  portsError: \(.portsError)" else empty end)
	end'
