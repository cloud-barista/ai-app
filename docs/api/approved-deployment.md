# 승인된 추론·응용 배포

AI-Ops에서 승인한 단일 노드 추론 요청의 파일을 준비하고, 기존 엔진의 Run과 배포 ID를 돌려준다. 노드·모델 조회와 준비 파일 생성은 기존 `deploy_application` 워크플로가 맡는다. [기존 배포 API](README.md#10-배포-요청-모양)

## Dry-run 서버 설정

AI-App 서버와 자동화 엔진에 같은 `AIAPP_ENGINE_APPROVAL_TOKEN`을 넣는다. AI-Ops는 이 값을 `AIOPS_AIAPP_ENGINE_APPROVAL_TOKEN`으로 사용한다. 승인 토큰은 기존 `AIAPP_AUTH_TOKEN`, 엔진 등록용 토큰, 운영자 토큰과 구분한다. `AIAPP_TUMBLEBUG_DRY_RUN=true`로 기동한다.

AI-App까지 재시작한 뒤 dry-run 결과를 복구하려면 `AIAPP_DEPLOYMENT_STORE_FILE`을 영속 볼륨에 두고, 엔진 아카이브와 준비 파일의 볼륨을 유지한다.

MCP 주소는 8090의 `/aiapp/engine/automation/mcp`다. `Authorization: Bearer <승인 토큰>`과 `X-AIMCMP-Namespace`를 보낸다. 초기화 응답의 서버 이름은 `ai-mcmp-app-approved-deployment`다. 엔진의 일반 조회·실행 도구는 기존 `/aiapp/engine/mcp`에서 조회한다.

## 승인 요청과 결과

`stage_approved_deployment`에 AI-Ops 계획의 `correlationId`, `revision`과 준비된 추론 `deployment` 한 건을 보낸다. 현재 입력은 초기 Revision 1과 `kind: node` 대상 한 건이다. `nsId`는 MCP 헤더와 일치해야 한다. 모델·아티팩트·런타임을 고정하고 환경 값과 API 키는 비워 둔다.

```json
{
  "correlationId": "approved-inference-01",
  "revision": 1,
  "deployment": {
    "type": "inference",
    "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"}],
    "inference": {
      "modelId": "ministral-3-8b",
      "artifactId": "hf-fp8",
      "runtimeId": "vllm",
      "gpu": true
    }
  }
}
```

| 결과 | 확인할 내용 |
| --- | --- |
| `blocked` | 파일 준비에 성공한 dry-run이다. 일반 응답의 `engineRunId`는 생성 `blocked`와 대기 `waiting` 단계를 기록한다. 응답 유실 후 복구한 Run에는 `get_deployment` 단계가 남는다. `deploymentId`로 조회한 배포는 `dry-run`이다. |
| `failed` | 파일 준비 전에 요청을 거부했거나 실패한 결과를 확인했다. Run이 발급된 경우 해당 기록에서 실패한 단계를 확인한다. |
| `confirmation_pending` | 요청을 저장한 상태다. dry-run 환경에서 같은 승인 요청을 다시 보내면 예약한 배포 ID의 결과를 조회한다. |

노드의 스펙·이미지가 `placementReview`와 다르면 AI-App이 배포를 수락하기 전에 거절한다. 응답은 `failed`이며 `engineRunId`로 원인을 확인할 수 있다. 검토 내용이나 대상을 바꿀 때는 새 승인 요청을 준비한다.

승인 dry-run과 실제 적용에서 GPU 추론은 CB-Tumblebug 스펙의 장치 벤더를 확인한다. 현행 GPU Compose는 NVIDIA 드라이버를 요구하므로 카탈로그가 AMD 등 다른 벤더를 명시하면 파일 준비 전에 `failed`가 된다. 벤더를 확인할 수 없는 형상은 실제 적용에서 기존 GPU 실행 점검을 거친다.

엔진은 첫 호출 전에 요청 해시, Revision, 예약한 배포 ID를 `AIAPP_ENGINE_ARCHIVE_DIR` 아래 `approved-deployments/runs.jsonl`에 저장한다. 같은 승인 요청을 반복하면 확정된 응답을 반환한다. 확인 대기 중이면 예약한 ID로 기존 dry-run을 읽어 요청 해시를 대조하고, 확인한 결과를 저장한다. 배포 기록을 조회할 수 없거나 예약 ID가 없는 이전 기록은 확인 대기 상태로 반환한다. 같은 ID에서 다른 본문이나 네임스페이스는 거부한다. `get_approved_deployment`에 `correlationId`를 넣으면 같은 네임스페이스에 저장된 응답을 읽을 수 있다.

엔진과 AI-App 배포 서비스 모두 승인 경로의 요청이 dry-run에서 처리되는지 확인한다. 예약한 ID를 포함한 생성 요청은 승인 전용 MCP로 제출한다. 일반 REST와 엔진 MCP에 제출 ID를 보내면 거부한다. 현재는 하나의 엔진 인스턴스가 아카이브 파일을 관리한다.

## 승인된 Revision 적용

초기 추론 요청의 `blocked` 영수증을 확인한다. 실제 배포 환경에서 AI-App은 `AIAPP_TUMBLEBUG_DRY_RUN=false`와 `AIAPP_DEPLOYMENT_STORE_FILE`로, 자동화 엔진은 `AIAPP_ENGINE_APPLY_ENABLED=true`로 실행한다. Dry-run에 사용한 엔진 아카이브와 배포 저장소 경로를 유지한다. 적용 도구는 기존 승인 MCP 경로에 추가된다. 엔진은 승인 토큰으로 AI-App의 `/aiapp/mcp/approval`에 제출한다. 일반 배포 요청은 기존 MCP·REST 경로를 사용한다.

운영자는 AI-Ops의 `POST /api/v1/agent-control/flows/{correlation_id}/aiapp/apply`에 `revision`, `previousRevision`과 준비한 추론·응용 요청을 보낸다. 초기값은 `1`과 `0`이다. 추론 요청은 dry-run 본문과 일치해야 하며, 응용의 대상·이름·버전은 준비한 계획과 일치해야 한다.

준비 응답을 `prepared.json`에 저장했다면 다음처럼 Langflow 연결 설정을 넣을 수 있다. `AIAPP_DEPLOY_SECRET_LANGFLOW_PASSWORD`는 AI-App 서버의 환경에 둔다.

Compose에서는 `.env`에 이 변수를 설정하면 `ai-app-server` 컨테이너로 전달된다. 호스트에서 `make run-app`으로 실행할 때는 `conf/setup.env`의 같은 설정을 사용한다. 다른 응용의 `AIAPP_DEPLOY_SECRET_*` 참조는 해당 AI-App 서비스의 환경에 이름별로 추가한다.

CB-Tumblebug 사전 검토 내용을 첨부한 Flow는 준비 응답의 `placementReview`를 dry-run, 추론·응용 적용, 후속 Revision에 그대로 전달한다. 접수와 실행 직전에 실제 노드의 스펙과 이미지를 확인한다. [사전 검토 입력](infra-recommendation.md#사전-검토와-배포-대상-대조)

```sh
jq '{
  revision,
  previousRevision: 0,
  inference: .deployments[0],
  application: (.deployments[1]
    | .app.inferenceBinding = {
        baseUrlEnv: "MODEL_BASE_URL",
        apiKeyEnv: "MODEL_API_KEY"
      }
    | .app.envFrom = {
        LANGFLOW_SUPERUSER_PASSWORD: "AIAPP_DEPLOY_SECRET_LANGFLOW_PASSWORD"
      })
}' prepared.json
```

`app.inferenceBinding`에는 연결 변수 이름을 넣는다. AI-App이 같은 노드에서 실행 중인 승인된 추론 배포의 주소와 키를 조회하고 응용의 `.env`를 만든다. Ollama 기본 API 주소가 필요한 응용은 `native: true`를 넣고 `baseUrlEnv: "OLLAMA_BASE_URL"`을 지정한다. `app.envFrom`은 `AIAPP_DEPLOY_SECRET_*` 환경변수를 참조한다. 승인 본문과 엔진 Run에는 참조 이름을 기록하고, 값은 배포용 파일에 넣는다.

엔진의 `apply_approved_revision`은 `correlationId`와 위 본문을 받아 예약한 뒤 `queued`를 반환한다. `get_applied_revision`에 `correlationId`·`revision`을 보내거나 AI-Ops의 `GET /api/v1/agent-control/flows/{correlation_id}/aiapp/apply/{revision}`으로 상태를 읽는다. `inference`와 `application`에는 단계 상태, `engineRunId`와 `deploymentId`가 담긴다.

| 결과 | 확인할 내용 |
| --- | --- |
| `queued` | 배포 작업이 진행 중이다. 조회 경로에서 두 단계 결과를 확인한다. |
| `succeeded` | 두 배포가 준비됐다. 변경하지 않은 추론 서비스를 사용한 Revision은 이전 추론 배포 ID를 반환한다. |
| `failed` | 엔진 Run으로 실패 단계를 확인한다. 응용 단계만 실패했으면 같은 구성을 새 Revision으로 승인해 응용부터 다시 적용한다. |
| `confirmation_pending` | 생성 응답이 유실됐거나 후속 상태를 확인하지 못한 경우다. 기존 Run과 배포 기록을 대조한다. 같은 승인 본문을 보내면 예약한 제출 ID로 이미 수락된 배포의 상태부터 확인한다. AI-App 재시작으로 배포 상태가 `interrupted`이면 노드 상태 확인이 필요하다. |

업데이트는 직전 완료 또는 실패 Revision 다음 번호를 사용한다. 노드, 런타임 ID와 응용 이름으로 같은 배포 위치를 확인한다. 응용 버전만 바꾸면 추론 서비스를 재사용한다. vLLM의 `inference.inference.args`에 `["--max-num-seqs", "16"]`을 넣으면 추론 서비스를 재배포하고 응용을 새 추론 배포에 연결한다. 이전 구성으로 복구할 때는 호환성을 확인한 뒤 해당 구성을 새 Revision으로 승인한다.

승인 본문은 AI-Ops의 `runs/aiapp-apply/{correlation_id}/{revision}.json`, 실행 단계 기록은 엔진 아카이브의 `approved-deployments/apply/runs.jsonl`에 저장한다. 배포 서비스는 제출 ID가 같은 요청을 한 번만 수락하고, 현재 배포 ID가 승인된 이전 ID와 달라졌으면 교체를 거부한다.
