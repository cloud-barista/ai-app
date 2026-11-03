# shellcheck shell=bash
# Shared by the example scripts in this folder; source it, do not run it.
#
#   AIAPP_URL          AI-App API base, default http://localhost:8090/aiapp
#   AIAPP_AUTH_TOKEN   bearer token, when the service sets AIAPP_AUTH_TOKEN
#   POLL_MAX           how many times a deployment is read while it runs (default 90)
#   POLL_INTERVAL      seconds between reads (default 10)
#
# The scripts need curl and jq.

AIAPP_URL="${AIAPP_URL:-http://localhost:8090/aiapp}"
AIAPP_URL="${AIAPP_URL%/}"
POLL_MAX="${POLL_MAX:-90}"
POLL_INTERVAL="${POLL_INTERVAL:-10}"

for tool in curl jq; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "$tool is required" >&2
		exit 1
	fi
done

AUTH=()
if [ -n "${AIAPP_AUTH_TOKEN:-}" ]; then
	AUTH=(-H "Authorization: Bearer ${AIAPP_AUTH_TOKEN}")
fi

# aiapp METHOD PATH [curl arguments...]
# Prints the response body on stdout and "METHOD PATH -> HTTP code" on stderr.
# A status outside 2xx prints the body ({"message": ...}) on stderr and fails.
aiapp() {
	local method=$1 path=$2
	shift 2
	local resp code body
	resp=$(curl -sS -w '\n%{http_code}' -X "$method" ${AUTH[@]+"${AUTH[@]}"} "$@" "${AIAPP_URL}${path}")
	code=${resp##*$'\n'}
	body=${resp%$'\n'*}
	if [ "${AIAPP_QUIET:-0}" != 1 ]; then
		echo "$method $path -> HTTP $code" >&2
	fi
	if [ "$code" -lt 200 ] || [ "$code" -ge 300 ]; then
		printf '%s\n' "$body" >&2
		return 1
	fi
	printf '%s\n' "$body"
}

# wait_deployment ID
# Reads the deployment until its status is no longer pending, deploying or
# removing, at most POLL_MAX times, and prints the last record.
wait_deployment() {
	local id=$1 i body status
	for ((i = 1; i <= POLL_MAX; i++)); do
		body=$(AIAPP_QUIET=1 aiapp GET "/deployments/${id}")
		status=$(jq -r '.status' <<<"$body")
		case "$status" in
		pending | deploying | removing)
			echo "[$i/$POLL_MAX] $id $status" >&2
			sleep "$POLL_INTERVAL"
			;;
		*)
			printf '%s\n' "$body"
			return 0
			;;
		esac
	done
	echo "deployment $id still $status after $POLL_MAX reads; read it later with GET /deployments/$id" >&2
	return 1
}

# summarize_deployment reads a deployment record on stdin and prints one line
# per node.
summarize_deployment() {
	jq -r '"deployment \(.id) \(.type) status=\(.status) dryRun=\(.dryRun)",
		(.nodes[] | "  \(.target.nsId)/\(.target.infraId)/\(.target.nodeId) \(.status)"
			+ (if .sshUser then " sshUser=\(.sshUser)" else "" end)
			+ (if .serviceUrl then " serviceUrl=\(.serviceUrl)" else "" end)
			+ (if .ports then " ports=\(.ports | map(tostring) | join(","))" else "" end)
			+ (if .error then " error=\(.error)" else "" end)),
		(.nodes[] | .notes[]? | "  note: \(.)")'
}
