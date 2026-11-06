# BESP: AI-Web 이 부를 AI-App API

> 대상: AI-Web 화면에서 런타임·모델·응용을 고르고 배포·상태·주소·내리기를 보여 줄 BESP 개발자.
> 공통 내용(인증, 오류 형식, 상태 코드, 배포 상태 목록)은 [README.md](README.md) 에 있습니다.
> API 가 어디로 열려 있는지는 [README.md 1-1절](README.md#1-1-api-는-어디로-열려-있나), Semaphore 는 [3-1절](README.md#3-1-semaphore-가-무엇인가),
> 예시 응용 셋의 동작과 실제 화면은 [12절](README.md#12-예시-응용-셋은-어떻게-동작하나) 에 있습니다.
> 아래 응답 예시는 Swagger(`api/docs/swagger.yaml`)의 필드 예시를 모아 줄인 것이고, 실제 응답에는 필드가 더 있습니다.

## 1. 인증

지금은 **bearer 토큰 하나**입니다. AI-Web 서버가 모든 요청에 `Authorization: Bearer <토큰>` 을 붙입니다.
토큰은 AI-App 운영자가 `AIAPP_AUTH_TOKEN` 으로 정한 값이고, 브라우저에 내려보내지 말고 AI-Web 서버 쪽에 둡니다.
사용자별·기관별 인증 연동은 나중에 정합니다. 그때 바뀌는 것은 헤더뿐이고 경로와 본문은 그대로 두는 것을 목표로 합니다.

```sh
AUTH="Authorization: Bearer $AIAPP_AUTH_TOKEN"
AIAPP_URL=http://<AI-App 호스트>:8090/aiapp
```

## 2. 화면별로 부를 것

| 화면 | 부를 것 |
|---|---|
| 시작할 때 연결 확인 | `GET /aiapp/status` |
| 런타임 목록 | `GET /aiapp/runtimes` |
| 모델 목록·상세 | `GET /aiapp/models` (필터 `task`, `modality`, `format`, `runtimeId`, `source`; 페이지 `limit`, `offset`), `GET /aiapp/models/{modelId}` |
| 응용 목록·상세 | `GET /aiapp/apps?limit=&offset=`, `GET /aiapp/apps/{name}`, `GET /aiapp/apps/{name}/versions/{version}` |
| 응용 업로드 | `POST /aiapp/apps` (multipart) |
| 배포 요청 | `POST /aiapp/deployments` |
| 배포 진행·완료 확인 | `GET /aiapp/deployments/{id}/progress` 를 `done` 이 `true` 가 될 때까지 |
| 배포 기록 전체 | `GET /aiapp/deployments/{id}` |
| 배포 목록 | `GET /aiapp/deployments?type=&nsId=&status=&limit=&offset=` |
| 배포 로그 | `GET /aiapp/deployments/{id}/logs` |
| 지금 떠 있는 것 | `GET /aiapp/endpoints?nsId=&infraId=&nodeId=&type=&available=&limit=&offset=` |
| 내리기 | `DELETE /aiapp/deployments/{id}` |
| VM 정지·재개·재부팅·종료 | `POST /aiapp/deployments/{id}/control` |

목록(모델, 응용, 배포, 주소)은 페이지로 나뉩니다. `limit` 은 기본 100, 1~1000 이고 `offset` 은 기본 0 입니다.
응답의 `total` 이 필터를 거친 전체 수라서, 화면에는 "`total` 건 중 몇 건" 을 보여 주고 다음 페이지는 `offset` 에 `limit` 을 더해 부릅니다.
자동 수집이 켜져 있으면 모델이 100 개를 넘으니 첫 페이지만 보고 전부라고 하지 않습니다. 자세한 것은 [README.md](README.md) 9-0절입니다.

## 3. 연결 확인

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/status"
```

```json
{
  "checkedAt": "2026-11-03T12:00:00Z",
  "dryRun": false,
  "openPorts": true,
  "semaphore": {
    "configured": true, "reachable": true, "authenticated": true, "latencyMs": 3,
    "version": "v2.12.17-5767d93-1741607759", "project": "aiapp", "projectId": 1,
    "uiUrl": "http://203.0.113.5:3001",
    "templates": [{"name": "aiapp-compose-up", "playbook": "compose-up.yml", "exists": true, "id": 3}]
  },
  "tumblebug": {"url": "http://cb-tumblebug:1323/tumblebug", "reachable": true, "message": "CB-Tumblebug is ready"},
  "stage": {"path": "/var/lib/aiapp/stage", "writable": true, "message": "writable"}
}
```

- `dryRun: true` 면 배포가 파일 배치까지만 하고 노드에는 아무것도 안 올립니다. 화면에 "시험 모드" 처럼 보여 주는 것이 좋습니다.
- 언제나 `200` 이고, 각 항목의 `reachable`·`authenticated`·`writable` 로 판단합니다. `templates` 의 `exists: false` 는 오류가 아닙니다(첫 배포가 만듦).

## 4. 카탈로그

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/runtimes"
curl -sS -H "$AUTH" "$AIAPP_URL/models?task=vision-chat"
curl -sS -H "$AUTH" "$AIAPP_URL/apps"
```

```json
{"runtimes": [
  {"id": "vllm", "image": "vllm/vllm-openai:v0.32.0", "formats": ["hf"], "port": 8000,
   "healthPath": "/health", "apiBasePath": "/v1"}
]}
```

```json
{"models": [
  {"id": "qwen3-vl-8b", "name": "Qwen3-VL-8B-Instruct", "family": "qwen3-vl", "publisher": "Qwen",
   "parameters": "8B", "tasks": ["vision-chat"], "inputModalities": ["text", "image"],
   "contextLength": 262144, "license": "apache-2.0",
   "artifacts": [
     {"id": "hf-bf16", "source": "huggingface", "ref": "Qwen/Qwen3-VL-8B-Instruct", "format": "hf",
      "precision": "BF16", "sizeGiB": 16.3, "runtimes": [{"runtimeId": "vllm", "minVersion": "0.11.0"}]}
   ]}
]}
```

```json
{"apps": [
  {"name": "open-webui", "versions": [
    {"version": 1, "script": "docker compose up -d", "ports": [3000], "createdAt": "2026-11-03T12:00:00Z",
     "files": [{"name": "docker-compose.yml", "sizeBytes": 812, "sha256": "9f86d081..."}]}
  ]}
]}
```

- 배포 화면에서 사용자가 고를 것은 **모델 + 아티팩트 + 런타임** 한 쌍입니다. 런타임 선택지는 아티팩트의 `runtimes[].runtimeId` 입니다.
  런타임과 모델은 따로 배포하지 않고 항상 함께 배포합니다.
- 값이 비어 있으면(`contextLength`, `sizeGiB`, `precision`) "모름" 입니다. 0 이나 무제한으로 보여 주지 않습니다.

## 5. 응용 업로드

multipart 하나로 보냅니다.

| 파트 | 필수 | 뜻 |
|---|---|---|
| `name` | 예 | 소문자·숫자·`_`·`-`, 63자까지. `.` 은 안 됨. 같은 이름이면 다음 버전 |
| `script` | 예 | 노드에서 파일들이 있는 폴더에서 실행할 스크립트. 텍스트 필드나 파일 파트로 한 번 |
| `files` | 아니오 | 스크립트가 쓰는 파일. 파일마다 파트 하나. 이름은 `.` 으로 시작할 수 없음, 256개까지 |
| `ports` | 아니오 | 이 응용이 노드에서 여는 TCP 포트. `3000,8080` 처럼 쉼표로, 또는 파트 여러 개. 16개까지. 배포 때 보안 그룹에 열림 |

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/apps" \
  -F name=open-webui -F script=@examples/apps/open-webui/deploy.sh \
  -F files=@examples/apps/open-webui/docker-compose.yml -F ports=3000
```

응답 `200` 은 등록된 버전(`version`, `script`, `files`, `ports`, `createdAt`)입니다. 상한(기본 256 MiB)을 넘으면 `413`, 이름·포트 규칙 위반은 `400` 입니다.

## 6. 배포 요청과 진행

```sh
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments" -d '{
  "type": "inference",
  "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"}],
  "inference": {"modelId": "qwen3-embedding-0.6b", "artifactId": "hf-bf16", "runtimeId": "vllm"}
}'
```

```sh
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments" -d '{
  "type": "app",
  "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm2"}],
  "app": {"name": "open-webui", "version": 1, "env": {"OLLAMA_BASE_URL": "http://203.0.113.12:11434"}}
}'
```

- `202` 이면 받은 것이고 본문은 `pending` 기록입니다. `id` 를 저장해 두고 `GET /aiapp/deployments/{id}/progress` 를 몇 초 간격으로
  읽어 `done` 이 `true` 가 될 때까지 진행을 보여 줍니다. 추론 엔진 배포(`type: inference`)와 응용 배포(`type: app`)가 같은 경로입니다.
  모델을 처음 받는 노드는 수십 분이 걸릴 수 있습니다.
- dry-run 이면 `200` 과 끝난 기록(`dry-run`)입니다.
- 요청 오류는 `400`/`404`/`422` 와 `{"message": "..."}` 입니다. 그 `message` 를 그대로 보여 주면 무엇이 틀렸는지 알 수 있습니다.

### 6-1. 진행과 완료 (`/progress`)

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/deployments/$id/progress"
```

진행 중:

```json
{
  "id": "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48", "type": "inference", "status": "deploying",
  "done": false, "running": false, "createdAt": "2026-11-03T12:00:00Z", "elapsedSeconds": 312,
  "count": {"total": 1, "inProgress": 1, "running": 0, "failed": 0, "dryRun": 0, "removed": 0},
  "nodes": [{
    "target": {"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"},
    "status": "deploying", "done": false, "phase": "compose-up", "phaseStartedAt": "2026-11-03T12:01:10Z",
    "step": {"playbook": "compose-up.yml", "taskId": 42, "status": "running",
             "currentTask": "Wait until the service answers", "startedAt": "2026-11-03T12:01:10Z"},
    "stepsDone": 1, "stepsTotal": 2, "host": "203.0.113.10", "serviceUrl": "http://203.0.113.10:8000/v1", "ports": [8000]
  }]
}
```

끝나면 `done: true` 이고, 성공이면 `running: true`·`status: running`·노드 `phase: done`·`stepsDone: 2` 입니다.
실패면 `running: false` 이고 노드는 실패한 `phase` 에 머물며 `nodes[].error` 에 이유가, `nodes[].step.currentTask` 에
멈춘 Ansible 태스크가 있습니다.

| 화면에 보여 줄 것 | 필드 |
|---|---|
| 끝났는지 (폴링 멈춤) | `done` |
| 서비스가 떴는지 | `running` (`false` 면 `status` 가 `failed`·`partial`·`interrupted`·`dry-run` 중 하나) |
| 노드 몇 개가 어떤 상태인지 | `count.total`·`inProgress`·`running`·`failed`·`dryRun`·`removed` |
| 노드가 지금 하는 일 | `nodes[].phase` ([README 7-1절](README.md) 의 표: `reading-node` → `staging` → `checking-ssh` → `install-docker` → `compose-up` 또는 `copy-and-run` → `opening-ports` → `done`) |
| 단계 진행 막대 | `nodes[].stepsDone` / `nodes[].stepsTotal` |
| 단계 안에서 하는 일 | `nodes[].step.currentTask` (Ansible 태스크 이름, 5초마다 갱신) |
| 경과 시간 | `elapsedSeconds`, 단계별로는 `nodes[].phaseStartedAt` |
| 끝난 뒤 쓸 주소 | `GET /aiapp/endpoints` (8절). 여기의 `nodes[].serviceUrl`·`nodes[].host` 는 배포할 때 접속한 IP 라, 노드를 정지했다 다시 켜서 IP 가 바뀌면 낡습니다 |

내리기(`DELETE`)가 `202` 면 같은 경로를 같은 방법으로 읽습니다. 그때 `phase` 는 `compose-down`, `stepsTotal` 은 1이고, 끝나면
`status: removed`, `running: false`, `done: true` 입니다.

### 6-2. 배포 기록 전체

`GET /aiapp/deployments/{id}` 는 compose 원문·노트·연 포트까지 든 전체 기록입니다. 노드마다 `phase`·`phaseStartedAt` 이 있고,
`steps[]` 의 각 단계는 시작되면 바로 `taskId`·`status` 가 채워지며 `currentTask` 가 있습니다.

끝난 기록(줄임):

```json
{
  "id": "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48",
  "type": "inference",
  "status": "running",
  "dryRun": false,
  "createdAt": "2026-11-03T12:00:00Z",
  "finishedAt": "2026-11-03T12:05:00Z",
  "request": {"type": "inference", "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"}],
              "inference": {"modelId": "qwen3-embedding-0.6b", "artifactId": "hf-bf16", "runtimeId": "vllm"}},
  "nodes": [{
    "target": {"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"},
    "status": "running",
    "node": {"csp": "aws", "region": "ap-northeast-2", "zone": "ap-northeast-2a", "host": "203.0.113.10",
             "port": 22, "user": "cb-user", "specId": "aws+ap-northeast-2+g4dn.xlarge"},
    "sshUser": "cb-user",
    "phase": "done", "phaseStartedAt": "2026-11-03T12:05:00Z",
    "gpu": true, "gpuSource": "spec",
    "workKey": "ms-vllm", "nodeDir": "/opt/aiapp/ms-vllm",
    "serviceUrl": "http://203.0.113.10:8000/v1",
    "apiKey": "3f1c9a7e5b2d4f6a8c0e1b3d5f7a9c2e",
    "ports": [8000],
    "openedPorts": [{"securityGroupId": "etri-shared-aws-ap-northeast-2", "port": 8000, "protocol": "TCP", "result": "added"}],
    "steps": [
      {"playbook": "install-docker.yml", "sshUser": "cb-user", "taskId": 41, "status": "success",
       "taskUrl": "http://203.0.113.5:3001/project/1/history?t=41"},
      {"playbook": "compose-up.yml", "sshUser": "cb-user", "taskId": 42, "status": "success",
       "taskUrl": "http://203.0.113.5:3001/project/1/history?t=42"}
    ]
  }]
}
```

| 화면에 보여 줄 것 | 필드 |
|---|---|
| 전체 상태 | `status` (README 7절의 표) |
| 노드별 상태와 실패 이유 | `nodes[].status`, `nodes[].error`, `nodes[].notes` |
| 노드에 접속한 SSH 사용자 | `nodes[].sshUser` (요청에 `targets[].sshUser` 를 주면 그 값, 아니면 CB-Tumblebug 의 `nodes[].node.user`) |
| 지금 어느 단계인지 | `nodes[].phase`, 단계 안에서는 `nodes[].steps[]` 마지막 항목의 `currentTask` |
| Semaphore 화면 링크 | `nodes[].steps[].taskUrl` (AI-App 에 `AIAPP_SEMAPHORE_UI_URL` 이 있을 때만) |
| 연 포트 | `nodes[].openedPorts[]`, 실패하면 `nodes[].portsError` |
| vLLM API 키 | `nodes[].apiKey` (vLLM 만). 8절의 `apiKey` 와 같은 값 |

## 7. 로그

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/deployments/$id/logs"
```

```json
{
  "id": "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48",
  "status": "running",
  "nodes": [{
    "target": {"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"},
    "status": "running",
    "steps": [{"playbook": "compose-up.yml", "sshUser": "cb-user", "taskId": 42, "status": "success",
               "taskUrl": "http://203.0.113.5:3001/project/1/history?t=42",
               "output": ["...태스크 로그 한 줄...", "..."]}]
  }]
}
```

- `output` 은 Semaphore 태스크 로그 한 줄씩이고 색 코드는 지웠습니다. 도는 중인 단계는 그때까지의 로그(5초마다 갱신)이고, 끝나면 전체 로그입니다.
- `taskUrl` 은 브라우저가 Semaphore 화면을 열 수 있는 곳에서만 쓸모가 있습니다. 열 수 없으면 `output` 만 보여 줍니다.

## 8. 지금 떠 있는 것 (주소)

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/endpoints?nsId=etri"
```

```json
{"endpoints": [
  {"nsId": "etri", "infraId": "travel", "nodeId": "vm1", "type": "inference", "workKey": "ms-vllm",
   "deploymentId": "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48",
   "nodeStatus": "Running", "available": true, "host": "203.0.113.10",
   "runtimeId": "vllm", "modelId": "qwen3-embedding-0.6b", "artifactId": "hf-bf16",
   "url": "http://203.0.113.10:8000/v1", "healthUrl": "http://203.0.113.10:8000/health",
   "apiKey": "3f1c9a7e5b2d4f6a8c0e1b3d5f7a9c2e",
   "ports": [8000], "portsOpened": true, "deployedAt": "2026-11-03T12:05:00Z"},
  {"nsId": "etri", "infraId": "travel", "nodeId": "vm2", "type": "app", "workKey": "app-open-webui",
   "deploymentId": "...", "nodeStatus": "Running", "available": true, "host": "203.0.113.12",
   "name": "open-webui", "version": 1,
   "urls": [{"port": 3000, "url": "http://203.0.113.12:3000"}], "ports": [3000], "portsOpened": true},
  {"nsId": "etri", "infraId": "travel", "nodeId": "vm3", "type": "app", "workKey": "app-langflow",
   "deploymentId": "...", "nodeStatus": "Suspended", "available": false,
   "name": "langflow", "version": 2, "ports": [7860], "portsOpened": true}
]}
```

- 무엇이 떠 있는지는 배포 기록에서 계산합니다. 컨테이너를 노드에 따로 묻지 않으므로, 배포 뒤에 누가 노드에서 컨테이너를 손으로 내렸으면 그것은 모릅니다.
- 주소는 부를 때마다 CB-Tumblebug 에서 노드를 읽어 지금 IP(공인 IP, 없으면 사설 IP)로 만듭니다. 노드를 정지했다 다시 켜서 IP 가 바뀌어도
  다시 배포할 필요가 없습니다.
- `nodeStatus` 는 CB-Tumblebug 이 준 노드 상태 그대로입니다(`Running`, `Suspended`, `Terminated`, `Failed` 등). **`Running` 일 때만
  `available: true` 이고 `host`·`url`·`healthUrl`·`urls` 가 있습니다.** 그 밖에는 주소를 비우므로 화면에서는 노드 상태를 보여 주고
  링크를 걸지 않습니다. CB-Tumblebug 을 못 읽으면 `unknown`, 노드가 없어졌으면 `not-found` 입니다. 주소 있는 것만 보려면 `?available=true`.
- 노드 하나·작업 키 하나(런타임당 하나, 응용 이름당 하나)에 항목 하나입니다. 다시 배포하면 바뀌고, 내리면 빠집니다.
- `portsOpened: false` 면 밖에서 안 닿을 수 있다고 표시합니다(`portsError`, `notes`).
- vLLM 항목의 `apiKey` 는 그 서비스를 부를 때 `Authorization: Bearer <apiKey>` 로 붙이는 키입니다. 가리지 않은 값이 그대로 오므로
  화면에 보여 줄 때는 가리고 복사만 하게 합니다. 노드가 `Running` 이 아니어도 키는 옵니다. Ollama 항목에는 없습니다(Ollama 는 인증이 없음).

## 9. 내리기와 노드 제어

```sh
curl -sS -H "$AUTH" -X DELETE "$AIAPP_URL/deployments/$id"
```

- `202`·`removing` 이면 `removed` 또는 `remove-failed` 가 될 때까지 읽습니다. 할 일이 없으면 바로 `200`·`removed` 입니다.
- 아직 `pending`·`deploying` 인 배포는 `409` 입니다. 끝난 뒤 다시 부릅니다.
- `remove-failed` 면 `DELETE` 를 다시 부르면 남은 노드만 다시 시도합니다.
- 컨테이너만 내리고 모델 가중치(named volume)와 연 포트는 남깁니다.

```sh
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments/$id/control" -d '{"action": "reboot"}'
```

```json
{"id": "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48", "action": "reboot",
 "nodes": [{"target": {"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"},
            "status": "done", "message": "Rebooting the node vm1"}]}
```

- `action` 은 `suspend`·`resume`·`reboot`·`terminate`. **`terminate` 는 VM 을 지웁니다.** 화면에서 확인을 받습니다.
- 그 배포의 노드 전부에 보냅니다. dry-run 이면 노드마다 `status: blocked` 이고 아무것도 보내지 않습니다.

## 10. 정해지지 않은 것

| 항목 | 지금 |
|---|---|
| 인증 연동 | bearer 토큰 하나. AI-Web 사용자와 기관 구분은 나중 |
| 배포 진행 알림 | 폴링만 있습니다(`GET /aiapp/deployments/{id}/progress`). 웹훅·이벤트 스트림은 없습니다 |
| 진행률 퍼센트 | 없습니다. 모델 다운로드 바이트 수는 모르고, `stepsDone`/`stepsTotal` 과 `phase` 로만 보여 줍니다 |
| 추천 | `POST /aiapp/recommendations/*` 는 지금 `501` 입니다. 화면에 넣지 않아도 됩니다 ([khu.md](khu.md)) |
