#!/usr/bin/env bash
# Starts Langflow from docker-compose.yml in this folder. Run it from this
# folder; .env here must set LANGFLOW_SUPERUSER_PASSWORD and may set
# LANGFLOW_SUPERUSER, MODEL_BASE_URL, MODEL_API_KEY,
# LANGFLOW_SSRF_ALLOWED_HOSTS and LANGFLOW_PORT.
#
# MODEL_BASE_URL (an OpenAI compatible model server, e.g.
# http://203.0.113.7:8000/v1) is stored in Langflow as the Generic global
# variable MODEL_BASE_URL once Langflow answers, so that the OpenAI
# component's "OpenAI API Base" field can select it. Its host is allowed for
# plain http unless LANGFLOW_SSRF_ALLOWED_HOSTS is given. MODEL_API_KEY (the
# apiKey of a vLLM service on GET /aiapp/endpoints) is stored as the
# Credential global variable MODEL_API_KEY, for the "OpenAI API Key" field.
# Needs python3 on the node for the API calls; the password and the key are
# passed through the environment and never printed.
set -euo pipefail

if [ -f .env ]; then
	set -a
	# shellcheck source=/dev/null
	. ./.env
	set +a
fi

if [ -z "${LANGFLOW_SUPERUSER_PASSWORD:-}" ]; then
	echo "LANGFLOW_SUPERUSER_PASSWORD is not set: give it in the deployment env" >&2
	exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
	echo "docker compose is not available on this node" >&2
	exit 1
fi

if [ -n "${MODEL_BASE_URL:-}" ] && [ -z "${LANGFLOW_SSRF_ALLOWED_HOSTS:-}" ]; then
	LANGFLOW_SSRF_ALLOWED_HOSTS=$(printf '%s' "$MODEL_BASE_URL" | sed -E 's#^[A-Za-z]+://##; s#[/:].*$##')
	export LANGFLOW_SSRF_ALLOWED_HOSTS
fi

docker compose up -d
docker compose ps

if [ -n "${MODEL_BASE_URL:-}" ] || [ -n "${MODEL_API_KEY:-}" ]; then
	if ! command -v python3 >/dev/null 2>&1; then
		echo "python3 is not installed on this node: set MODEL_BASE_URL and MODEL_API_KEY in Langflow by hand" >&2
		exit 1
	fi
	LANGFLOW_URL="http://127.0.0.1:${LANGFLOW_PORT:-7860}" python3 - <<'PY'
"""Stores MODEL_BASE_URL in Langflow as a Generic global variable and
MODEL_API_KEY as a Credential one."""
import json, os, sys, time, urllib.error, urllib.parse, urllib.request

base = os.environ["LANGFLOW_URL"]


def call(method, path, body=None, token=None, form=False):
    headers = {}
    data = None
    if token:
        headers["Authorization"] = "Bearer " + token
    if body is not None:
        if form:
            data = urllib.parse.urlencode(body).encode()
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        else:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base + path, data=data, method=method, headers=headers)
    with urllib.request.urlopen(req, timeout=30) as resp:
        raw = resp.read()
        return json.loads(raw) if raw else None


# Langflow takes a while to migrate its database on the first start.
for _ in range(60):
    try:
        call("GET", "/health")
        break
    except (urllib.error.URLError, ConnectionError, OSError):
        time.sleep(5)
else:
    sys.exit("Langflow did not answer on /health within 5 minutes")

# The form fields of /api/v1/login, filled from the deployment env.
login = dict(zip(("username", "password"),
                 (os.environ.get("LANGFLOW_SUPERUSER") or "admin", os.environ["LANGFLOW_SUPERUSER_PASSWORD"])))
token = None
for _ in range(12):
    try:
        token = call("POST", "/api/v1/login", login, form=True)["access_token"]
        break
    except urllib.error.HTTPError as err:
        if err.code in (401, 403):
            sys.exit("Langflow refused the admin login: check LANGFLOW_SUPERUSER_PASSWORD")
        time.sleep(5)
if not token:
    sys.exit("Langflow login did not succeed")



def store(name, kind):
    """Creates or updates the global variable name, of type kind, from the env."""
    value = os.environ.get(name)
    if not value:
        return
    for var in call("GET", "/api/v1/variables/", token=token) or []:
        if var.get("name") == name:
            if var.get("type") == kind:
                call("PATCH", "/api/v1/variables/" + var["id"],
                     {"id": var["id"], "name": name, "value": value, "type": kind}, token=token)
                print("updated the Langflow global variable " + name)
                return
            # An earlier version passed MODEL_BASE_URL through the environment, which made it a Credential.
            call("DELETE", "/api/v1/variables/" + var["id"], token=token)
    call("POST", "/api/v1/variables/",
         {"name": name, "value": value, "type": kind, "default_fields": []}, token=token)
    print("created the Langflow global variable " + name)


store("MODEL_BASE_URL", "Generic")
store("MODEL_API_KEY", "Credential")
PY
fi

echo "Langflow listens on port ${LANGFLOW_PORT:-7860} of this node."
if [ -n "${MODEL_BASE_URL:-}" ]; then
	echo "Pick the global variable MODEL_BASE_URL in the OpenAI API Base field of an OpenAI model component."
fi
if [ -n "${MODEL_API_KEY:-}" ]; then
	echo "Pick the global variable MODEL_API_KEY in the OpenAI API Key field of that component."
fi
