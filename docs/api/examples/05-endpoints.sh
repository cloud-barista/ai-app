#!/usr/bin/env bash
# Lists the inference services and applications serving on the nodes now, with
# the node status CB-Tumblebug gives, their URLs on the current IP of the node
# (only when it is Running) and whether their ports were opened. The API key
# of a vLLM service is shown masked down to its last 4 characters; set
# SHOW_API_KEY=true to print it whole.
#
#   ./05-endpoints.sh [nsId] [infraId] [nodeId]
#
#   TYPE=inference|app     optional filter
#   AVAILABLE=true|false   optional filter: Running nodes with URLs, or the others
#   LIMIT=n                endpoints per page, 1 to 1000 (default 100)
#   OFFSET=n               endpoints to skip; the first line gives the total
set -euo pipefail
. "$(dirname "$0")/common.sh"

query=()
for pair in "nsId=${1:-}" "infraId=${2:-}" "nodeId=${3:-}" "type=${TYPE:-}" "available=${AVAILABLE:-}" \
	"limit=${LIMIT:-}" "offset=${OFFSET:-}"; do
	if [ -n "${pair#*=}" ]; then
		query+=(--data-urlencode "$pair")
	fi
done

aiapp GET /endpoints -G ${query[@]+"${query[@]}"} | jq -r --arg show "${SHOW_API_KEY:-}" '
	"\(.endpoints | length) of \(.total) endpoints from offset \(.offset)",
	if (.endpoints | length) == 0 then "no endpoint serving" else
	.endpoints[] | "\(.nsId)/\(.infraId)/\(.nodeId) \(.workKey) deployment=\(.deploymentId) node=\(.nodeStatus) available=\(.available) portsOpened=\(.portsOpened)",
		(if .type == "inference" then "  \(.runtimeId) \(.modelId)/\(.artifactId) url=\(.url // "") health=\(.healthUrl // "")"
			+ (if .apiKey then " apiKey=" + (if $show == "true" then .apiKey else "****" + .apiKey[-4:] end) else "" end)
		else "  \(.name) v\(.version) " + ([.urls[]?.url] | join(" ")) end),
		(if .portsError then "  portsError: \(.portsError)" else empty end)
	end'
