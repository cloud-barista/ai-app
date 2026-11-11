#!/usr/bin/env bash
set -euo pipefail

app_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
command_name="${1:---help}"
if [[ $# -gt 0 ]]; then
  shift
fi

usage() {
  cat <<'USAGE'
GPU 배포 시험

  ./scripts/gpu-test.sh up
  ./scripts/gpu-test.sh status
  ./scripts/gpu-test.sh dry-run NS_ID INFRA_ID NODE_ID
  ./scripts/gpu-test.sh deploy NS_ID INFRA_ID NODE_ID
  ./scripts/gpu-test.sh test NS_ID INFRA_ID NODE_ID

up은 Compose의 deploy 프로필을 빌드하고 기동합니다.
dry-run과 deploy는 서버의 AIAPP_TUMBLEBUG_DRY_RUN 설정을 확인합니다.
deploy는 Ollama + Qwen3-VL-8B를 배포하고 실제 추론 응답까지 확인합니다.
test는 실행 중인 같은 모델의 응답을 확인합니다.

AIAPP_URL과 AIAPP_AUTH_TOKEN은 기존 API 예제와 같은 환경변수입니다.
POLL_MAX는 기본 180회, POLL_INTERVAL은 기본 10초입니다.
USAGE
}

fail() {
  printf '%s\n' "$*" >&2
  exit 1
}

case "$command_name" in
  --help|-h)
    usage
    exit 0
    ;;
  up)
    [[ $# -eq 0 ]] || fail "up은 추가 인자를 받지 않습니다."
    command -v docker >/dev/null || fail "Docker와 Compose 플러그인을 설치하세요."
    cd "$app_root"
    exec docker compose --profile deploy up -d --build
    ;;
  status)
    [[ $# -eq 0 ]] || fail "status는 추가 인자를 받지 않습니다."
    exec bash "$app_root/docs/api/examples/00-status.sh"
    ;;
  dry-run|deploy|test)
    [[ $# -eq 3 ]] || fail "사용법은 gpu-test.sh $command_name NS_ID INFRA_ID NODE_ID입니다."
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

namespace=$1
infra=$2
node=$3
export POLL_MAX="${POLL_MAX:-180}"
# shellcheck source=../docs/api/examples/common.sh
source "$app_root/docs/api/examples/common.sh"

test_inference() {
  local endpoints service_url answer
  endpoints=$(aiapp GET /endpoints -G \
    --data-urlencode "nsId=$namespace" \
    --data-urlencode "infraId=$infra" \
    --data-urlencode "nodeId=$node" \
    --data-urlencode 'type=inference')
  service_url=$(jq -er '
    .endpoints
    | map(select(.available == true and .runtimeId == "ollama" and .modelId == "qwen3-vl-8b"))
    | select(length == 1)
    | .[0].url
    | select(type == "string" and length > 0)
  ' <<<"$endpoints") || fail "이 노드에서 사용 가능한 Ollama + Qwen3-VL-8B 엔드포인트를 확인하세요."

  answer=$(curl --fail-with-body --silent --show-error \
    --proto '=http,https' --connect-timeout 10 --max-time 300 \
    -H 'Content-Type: application/json' \
    -d '{"model":"qwen3-vl:8b","prompt":"What is 2 + 2? Reply with the number only.","stream":false,"think":false,"options":{"num_predict":512,"temperature":0}}' \
    "${service_url%/v1}/api/generate")
  jq '{model, done, response, eval_count}' <<<"$answer"
  jq -e '
    .done == true and (.response | type == "string" and test("\\S")) and .eval_count > 0
  ' <<<"$answer" >/dev/null || fail "추론이 완료된 응답과 생성 토큰 수를 확인하세요."
}

if [[ "$command_name" == test ]]; then
  test_inference
  exit 0
fi

expected_dry_run=true
expected_status=dry-run
if [[ "$command_name" == deploy ]]; then
  expected_dry_run=false
  expected_status=running
fi

status=$(aiapp GET /status)
[[ "$(jq -r '.dryRun' <<<"$status")" == "$expected_dry_run" ]] || \
  fail "AI-App의 AIAPP_TUMBLEBUG_DRY_RUN=$expected_dry_run 설정을 반영한 뒤 다시 실행하세요."
jq -e '.tumblebug.reachable == true and .stage.writable == true' <<<"$status" >/dev/null || \
  fail "status 명령에서 Tumblebug 연결과 stage 쓰기 권한을 확인하세요."
if [[ "$command_name" == deploy ]]; then
  jq -e '.semaphore.authenticated == true' <<<"$status" >/dev/null || \
    fail "status 명령에서 Semaphore 인증을 확인하세요."
fi

body=$(jq -n --arg ns "$namespace" --arg infra "$infra" --arg node "$node" '{
  type: "inference",
  targets: [{kind: "node", nsId: $ns, infraId: $infra, nodeId: $node}],
  inference: {modelId: "qwen3-vl-8b", artifactId: "ollama-gguf", runtimeId: "ollama", gpu: true}
}')
record=$(aiapp POST /deployments -H 'Content-Type: application/json' -d "$body")
deployment_id=$(jq -er '.id | select(type == "string" and length > 0)' <<<"$record")
printf 'deployment id: %s\n' "$deployment_id" >&2
record=$(wait_deployment "$deployment_id")
summarize_deployment <<<"$record"
jq -e --arg expected "$expected_status" '
  .status == $expected and (.nodes | length) == 1 and .nodes[0].status == $expected
' <<<"$record" >/dev/null || \
  fail "배포 실패 또는 미완료. 로그는 docs/api/examples/06-logs.sh $deployment_id 명령으로 확인하세요."

if [[ "$command_name" == deploy ]]; then
  jq -e '.nodes[0].gpu == true and (.nodes[0].gpuCheck | type == "string" and length > 0)' \
    <<<"$record" >/dev/null || fail "배포 기록의 GPU 사용 확인 결과가 비어 있습니다."
  test_inference
fi
