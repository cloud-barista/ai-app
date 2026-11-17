# ETRI 환경 구동 시험 (2026-11-18)

> 읽는 사람: AI-App 을 부르는 기관 개발자(ETRI, BESP, 경희대)와 이 repo 를 고치는 사람.
> ETRI 가 CB-Tumblebug 으로 만들어 둔 AWS·Azure·GCP GPU VM 3대에, AI-App API 만으로 추론 서비스 3개와 AI 응용 3개를 올린 절차와 결과입니다.
> 단계마다 예제 스크립트(`docs/api/examples/`)를 쓰고, 그 스크립트가 실제로 부르는 AI-App API 를 바로 아래에 적었습니다.
> IP 와 API 키는 문서용 값으로 바꿨습니다. 공인 IP 는 `203.0.113.0/24`(문서용 대역), 사설 IP 는 `198.51.100.0/24`(또 다른 문서용 대역), vLLM API 키는 끝 4자리만 남겼습니다.

## 1. 한 줄 요약

AI-App 을 `docker compose up -d` 로 띄운 뒤, 노드 3대에 추론 서비스(vLLM 2개, Ollama 1개)와 응용(Dify, Open WebUI, Langflow)을 올렸습니다. 6건 모두 `running` 으로 끝났고, 노드 보안 그룹에 서빙 포트가 열렸고, 바깥에서 응답했습니다. 응용 화면에서 Dify RAG(지식 검색 + LLM), Open WebUI 이미지 질의, Langflow 대화까지 확인했습니다(11절). 이번 시험에서 막힌 단계는 없었습니다.

| 노드 | 추론 서비스 | 걸린 시간 | 응용 | 걸린 시간 |
| --- | --- | --- | --- | --- |
| AWS `aws-2` | vLLM + `qwen3-embedding-0.6b` | 약 390초 | Dify (80) | 약 220초 |
| Azure `azure-1` | Ollama + `qwen3-vl-8b` | 약 190초 | Open WebUI (3000) | 약 200초 |
| GCP `gcp-us-central1-2` | vLLM + `ministral-3-8b` | 약 690초 | Langflow (7860) | 약 170초 |

걸린 시간은 배포 스크립트가 10초마다 찍은 진행 줄(`elapsedSeconds`)의 마지막 값입니다.

## 2. 환경

### 2-1. 관리 호스트

CB-Tumblebug 이 이미 돌고 있는 ETRI 호스트(이하 관리 호스트)에 AI-App 을 띄웠습니다. 이 문서의 명령은 전부 관리 호스트에서 실행합니다.

| 항목 | 값 |
| --- | --- |
| CB-Tumblebug | `cloudbaristaorg/cb-tumblebug:0.13.8` (cb-spider 0.13.20, cb-mapui 0.13.14, etcd, openbao, postgres 2개). 주소 `http://localhost:1323/tumblebug` |
| AI-App | 이 repo main, 커밋 `acbdb70`. `docker compose` 로 실행. API `http://localhost:8090/aiapp` |
| Semaphore | `semaphoreui/semaphore:v2.12.17`, AI-App compose 에 포함. 화면 `http://<관리 호스트>:3001` |
| 노드에서 바깥으로 | 노드가 Docker Hub, Hugging Face, registry.ollama.ai, GitHub 에 직접 나감 |

### 2-2. 노드

CB-Tumblebug 네임스페이스 `gpu-vm`, Infra `gpu-vm`, 상태 `Running:3 (R:3/3)`.

| 노드 ID | CSP·리전 | 스펙 | GPU | 이미지 (OS) | 공인 IP | 사설 IP | 접속 사용자 | 보안 그룹 (시작 때 열린 인바운드) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `aws-2` | aws us-east-1 | g6.2xlarge (8 vCPU, 32 GiB) | NVIDIA L4 24GB, 드라이버 595 | Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 24.04) | 203.0.113.11 | 198.51.100.11 | `cb-user` | `gpu-vm-aws` (TCP 22) |
| `azure-1` | azure eastus | Standard\_NC40ads\_H100\_v5 (40 vCPU, 320 GiB) | NVIDIA H100 NVL 94GB, 드라이버 580 | `microsoft-dsvm:ubuntu-hpc:2404` (Ubuntu 24.04) | 203.0.113.12 | 198.51.100.12 | `cb-user` | `gpu-vm-azure` (TCP 22) |
| `gcp-us-central1-2` | gcp us-central1 | g2-standard-8 (8 vCPU, 32 GiB) | NVIDIA L4 24GB, 드라이버 580 | Ubuntu 24.04 accelerator, NVIDIA 580 | 203.0.113.13 | 198.51.100.13 | `cb-user` | `gpu-vm-gcp-us-central1` (TCP 22) |

- 세 노드 모두 스펙의 `acceleratorType` 이 `gpu` 라서 AI-App 이 GPU 노드로 다룹니다. 배포 요청에 `gpu` 를 따로 줄 필요가 없습니다.
- 세 노드의 SSH 키 모두 CB-Tumblebug 에 개인키가 들어 있습니다. AI-App 은 이 키로 노드에 접속합니다.
- 드라이버는 셋 다 550 이상이라 Ollama `0.40.13` 의 요구를 맞춥니다.
- 시작 때는 22 번만 열려 있습니다. 서빙 포트(80, 3000, 7860, 8000, 11434)는 배포가 끝날 때 AI-App 이 보안 그룹에 엽니다.

### 2-3. 목표 구성

| VM | 노드 ID | 역할 | AI 응용 (포트) | 추론 엔진 (포트) | 모델 (`modelId` / `artifactId`) |
| --- | --- | --- | --- | --- | --- |
| AWS (VM 1) | `aws-2` | 문서 분석 (RAG) | Dify (80) | vLLM (8000) | `qwen3-embedding-0.6b` / `hf-bf16` |
| Azure (VM 2) | `azure-1` | 이미지 분석, 여행 계획 생성 | Open WebUI (3000) | Ollama (11434) | `qwen3-vl-8b` / `ollama-gguf` |
| GCP (VM 3) | `gcp-us-central1-2` | 검증, 알림 (Agent) | Langflow (7860) | vLLM (8000) | `ministral-3-8b` / `hf-fp8` |

세 모델은 AI-App 카탈로그의 기본 모델(seed)이라 따로 등록하지 않아도 됩니다. 가중치는 배포할 때 각 VM 이 Hugging Face 또는 Ollama 에서 직접 받습니다.

## 3. AI-App 띄우기

```sh
cd
git clone https://github.com/cloud-barista/ai-app.git
cd ~/ai-app
docker compose up -d
```

`.env` 없이 띄웠으므로 compose 기본값이 들어갑니다. 이 상태의 AI-App 은 아래와 같습니다.

| 항목 | 값 | 이 문서에서 할 일 |
| --- | --- | --- |
| 인증 | `AIAPP_AUTH_TOKEN` 이 비어 있어 꺼져 있음 | 토큰 없이 부름 |
| **dry-run** | **켜져 있음** (`AIAPP_TUMBLEBUG_DRY_RUN=true`) | **6절에서 끔** |
| 포트 열기 | 켜져 있음 (`openPorts: true`) | - |
| Semaphore | 닿음, 로그인 됨. playbook 템플릿 5개는 아직 없음 | 첫 실배포 때 AI-App 이 만듦 |
| 런타임 | `vllm` (`vllm/vllm-openai:v0.32.0`), `ollama` (`ollama/ollama:0.40.13`) | - |
| 올라간 응용 | 0개 | 7절에서 셋을 올림 |

dry-run 은 배포 요청을 받아 노드 정보를 읽고 compose 파일까지 만든 뒤, Semaphore 에 넘기기 전에 멈추는 모드입니다. 노드에는 아무것도 하지 않습니다.

AI-App compose 가 띄운 컨테이너:

| 컨테이너 | 이미지 | 바깥 포트 | 상태 |
| --- | --- | --- | --- |
| `ai-app-server` | `ai-agent-proto:local` | 8090 | healthy |
| `ai-automation-engine` | `ai-agent-proto:local` | 8091 | healthy |
| `mock-partner-recommender` | `ai-agent-proto:local` | 8092 | Up |
| `mock-partner-nl-agent` | `ai-agent-proto:local` | 8093 | healthy |
| `aiapp-semaphore` | `semaphoreui/semaphore:v2.12.17` | 3001 | Up |
| `aiapp-volume-init` | `ai-agent-proto:local` | - | Exited (0). `./container-volume` 아래 폴더를 만들고 소유자를 맞춘 뒤 끝나는 컨테이너 |

`docker logs ai-app-server` 의 시작 로그에서 볼 것:

```
{"level":"info","count":3,"message":"Seeded AI model catalog"}
{"level":"info","sources":["huggingface","ollama"],"interval":"24h0m0s","limit":100,"message":"Catalog sync is on"}
{"level":"info","url":"http://host.docker.internal:1323/tumblebug","source":"environment","message":"Using CB-Tumblebug"}
{"level":"warn","message":"Dry run is on: node deployments place their files and stop before Semaphore, and node control calls are blocked"}
{"level":"warn","message":"AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open"}
{"level":"info","port":8090,"basePath":"/aiapp","tools":13,"message":"Starting server"}
{"level":"info","source":"huggingface","fetched":300,"added":300,"message":"Catalog sync source finished"}
{"level":"info","source":"ollama","fetched":100,"added":100,"message":"Catalog sync source finished"}
{"level":"info","offline":false,"message":"Finished catalog sync"}
```

기본 모델 3개가 들어가고, 카탈로그 동기화가 Hugging Face 300개, Ollama 100개를 더합니다(`time` 과 일부 필드는 줄였습니다).

## 4. 셸 준비

```sh
cd ~/ai-app/docs/api/examples
export AIAPP_URL=http://localhost:8090/aiapp
export POLL_MAX=180          # 배포 진행을 10초 간격으로 최대 30분 읽음 (기본 90 = 15분)
export NS=gpu-vm
export INFRA=gpu-vm
export AWS=aws-2
export AZURE=azure-1
export GCP=gcp-us-central1-2

# CB-Tumblebug 비밀번호. AI-App 이 쓰는 값을 컨테이너에서 읽어 변수에만 담음 (화면에 찍지 않음)
export TB_PW=$(docker inspect ai-app-server --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^AIAPP_TUMBLEBUG_PASSWORD=//p')
```

셸을 새로 열면 다시 넣습니다. 예제 스크립트는 `curl` 과 `jq` 를 씁니다.

| 스크립트 | 하는 일 | 쓰는 절 |
| --- | --- | --- |
| `00-status.sh` | AI-App, Semaphore, CB-Tumblebug, dry-run 상태 | 5-1 |
| `01-catalog.sh` | 런타임, 모델, 응용 목록 | 5-2, 7 |
| `02-upload-app.sh` | `examples/apps/<응용>` 폴더를 응용으로 올림 | 7 |
| `03-deploy-inference.sh` | 추론 서비스 배포 후 끝날 때까지 기다림 | 5-5, 8 |
| `04-deploy-app.sh` | 응용 배포 후 끝날 때까지 기다림 | 9-3 |
| `05-endpoints.sh` | 지금 떠 있는 주소와 vLLM API 키 | 9-1, 10 |
| `06-logs.sh` | 배포의 playbook 단계별 로그 | 12 |
| `07-remove.sh` | 배포 내리기 | 13 |

### 스크립트가 공통으로 쓰는 것 (`common.sh`)

모든 스크립트가 `common.sh` 를 읽습니다. 여기에 있는 함수 셋이 아래 절의 API 호출 구조를 만듭니다.

| 함수 | 하는 일 |
| --- | --- |
| `aiapp METHOD PATH [curl 인자]` | `curl -sS -X METHOD "$AIAPP_URL$PATH"` 로 부르고 `METHOD PATH -> HTTP 코드` 를 stderr 에 찍음. 2xx 가 아니면 응답 본문(`{"message": ...}`)을 stderr 에 찍고 실패. `AIAPP_AUTH_TOKEN` 이 있으면 `Authorization: Bearer <토큰>` 을 붙임 |
| `wait_deployment ID` | `GET /deployments/{id}/progress` 를 `POLL_INTERVAL`(기본 10초)마다 최대 `POLL_MAX` 번 읽음. 매번 노드별 `phase`, `stepsDone/stepsTotal`, 지금 도는 Ansible 태스크(`step.currentTask`)를 찍음. `done` 이 `true` 가 되면 `GET /deployments/{id}` 로 전체 기록을 한 번 읽어 넘김 |
| `summarize_deployment` | 배포 기록에서 노드마다 한 줄을 만듦: `status`, `phase`, `sshUser`, `serviceUrl`, `apiKey`(끝 4자리만), `ports`, `error`, `notes[]` |

배포를 기다리는 동안 한 번 읽는 진행 응답(`GET /deployments/{id}/progress`)에서 스크립트가 쓰는 필드:

```json
{
  "id": "<배포 ID>",
  "status": "deploying",
  "done": false,
  "elapsedSeconds": 320,
  "nodes": [
    {
      "target": {"kind": "node", "nsId": "gpu-vm", "infraId": "gpu-vm", "nodeId": "aws-2"},
      "status": "deploying",
      "phase": "compose-up",
      "stepsDone": 2,
      "stepsTotal": 3,
      "step": {"playbook": "compose-up.yml", "currentTask": "Wait until the service answers"}
    }
  ]
}
```

이번 시험에서 `phase` 는 `reading-node` → `install-gpu-driver`(GPU 노드) → `install-docker` → `compose-up`(추론) 또는 `copy-and-run`(응용) → `opening-ports` → `done` 순으로 바뀌었습니다. 그 사이에 `staging`, `checking-ssh`, `waiting-node-lock` 이 잠깐 나올 수 있습니다. `done` 은 `pending`·`deploying`·`removing` 동안 `false` 입니다.

## 5. 시작 전 확인

### 5-1. AI-App 상태

```sh
./00-status.sh
```

**부르는 API**

| 순서 | 호출 | 응답에서 읽는 것 |
| --- | --- | --- |
| 1 | `GET /readyz` | `{"message": "Service is ready"}`. 준비 전이면 503 |
| 2 | `GET /status` | `dryRun`, `openPorts`, `semaphore.{configured, reachable, authenticated, version, uiUrl, message, templates[].{name, exists}}`, `tumblebug.{url, reachable, message}`, `stage.{path, writable, message}` |

같은 호출을 curl 로:

```sh
curl -s $AIAPP_URL/readyz
curl -s $AIAPP_URL/status | jq .
```

기대 결과: `dryRun: true`, `semaphore.reachable: true`, `semaphore.authenticated: true`, `tumblebug.reachable: true`, `stage.writable: true`.

```
GET /readyz -> HTTP 200
{"message":"Service is ready"}

GET /status -> HTTP 200
{
  "dryRun": true,
  "openPorts": true,
  "semaphore": {
    "configured": true,
    "reachable": true,
    "authenticated": true,
    "version": "v2.12.17-5767d93-1741607759",
    "uiUrl": "http://127.0.0.1:3001",
    "message": "ready; project \"aiapp\" does not exist yet, the first deployment creates it; dry run is on, so deployments do not call Semaphore",
    "templates": [
      "aiapp-install-gpu-driver exists=false",
      "aiapp-install-docker exists=false",
      "aiapp-compose-up exists=false",
      "aiapp-copy-and-run exists=false",
      "aiapp-compose-down exists=false"
    ]
  },
  "tumblebug": {
    "url": "http://host.docker.internal:1323/tumblebug",
    "reachable": true,
    "message": "CB-Tumblebug is ready and initialized"
  },
  "stage": {
    "path": "/var/lib/aiapp/stage",
    "writable": true,
    "message": "writable"
  }
}
```

### 5-2. 카탈로그

```sh
./01-catalog.sh
```

**부르는 API**

| 순서 | 호출 | 보내는 것 | 응답에서 읽는 것 |
| --- | --- | --- | --- |
| 1 | `GET /runtimes` | - | `runtimes[].{id, image, port, formats}` |
| 2 | `GET /models` | 쿼리 `limit`(기본 100), `offset`(기본 0), `deployable`(`DEPLOYABLE` 를 줄 때만) | `total`, `offset`, `models[].{id, tasks, artifacts[].{id, runtimes[].runtimeId, source, ref, precision}}` |
| 3 | `GET /apps` | 쿼리 `limit`, `offset` | `total`, `offset`, `apps[].{name, versions[].{version, ports}}` |

같은 호출을 curl 로:

```sh
curl -s $AIAPP_URL/runtimes | jq .
curl -s "$AIAPP_URL/models?limit=100&offset=0" | jq .
curl -s "$AIAPP_URL/apps?limit=100&offset=0" | jq .
```

결과(모델 목록은 앞 몇 줄만 남겼습니다):

```
# runtimes
ollama  image=ollama/ollama:0.40.13 port=11434 formats=gguf
vllm  image=vllm/vllm-openai:v0.32.0 port=8000 formats=hf
# models (modelId / artifactId -> runtimeId)
100 of 403 models from offset 0
hf-baai--bge-base-en / hf -> vllm  tasks=embedding huggingface:BAAI/bge-base-en F32
hf-google--gemma-3-4b-it / hf -> vllm  tasks=vision-chat huggingface:google/gemma-3-4b-it BF16
hf-ai-forever--frida / hf ->   tasks=embedding huggingface:ai-forever/FRIDA F32
...
# apps (name version ports)
0 of 0 apps from offset 0
```

카탈로그에는 기본 모델 3개와 동기화로 들어온 400개, 합쳐서 403개가 있습니다. `-> ` 뒤가 비어 있는 모델은 이 AI-App 의 런타임으로 띄울 수 없는 것입니다. `DEPLOYABLE=true ./01-catalog.sh` 로 띄울 수 있는 것만 볼 수 있습니다.

### 5-3. CB-Tumblebug 에서 VM 확인

AI-App 은 배포할 때 CB-Tumblebug 에서 노드 정보, SSH 키, 스펙, 보안 그룹을 읽습니다. 같은 것을 CB-Tumblebug API 로 직접 불러 봅니다.
전부 `GET` 이라 아무것도 바꾸지 않습니다. API 는 CB-Tumblebug Swagger(`http://localhost:1323/tumblebug/api/index.html`, 아이디 `default` 와 `$TB_PW`)에 있는 그대로입니다.

| # | API | 무엇을 보나 |
| --- | --- | --- |
| 1 | `GET /readyz` | CB-Tumblebug 이 떠 있는지 |
| 2 | `GET /ns?option=id` | 네임스페이스 |
| 3 | `GET /ns/{nsId}/infra?option=id` | Infra |
| 4 | `GET /ns/{nsId}/infra/{infraId}?option=status` | Infra 와 노드 상태, IP |
| 5 | `GET /ns/{nsId}/infra/{infraId}/node/{nodeId}` | 노드의 스펙, 접속 사용자, SSH 키, 보안 그룹 |
| 6 | `GET /ns/system/resources/spec/{specId}` | GPU 여부 |
| 7 | `GET /ns/{nsId}/resources/sshKey/{sshKeyId}` | 개인키가 들어 있는지 |
| 8 | `GET /ns/{nsId}/resources/securityGroup/{securityGroupId}` | 열린 인바운드 포트 |

**1~3. 준비 상태, 네임스페이스, Infra**

```sh
curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/readyz" | jq -c .
curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/ns?option=id" | jq -c .
curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/ns/$NS/infra?option=id" | jq -c .
```

```json
{"message":"CB-Tumblebug is ready and initialized","ready":true,"initialized":true}
{"output":["default","gpu-vm","system"]}
{"output":["gpu-vm"]}
```

**4. Infra 와 노드 상태**

```sh
curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/ns/$NS/infra/$INFRA?option=status" \
  | jq '{id: .status.id, status: .status.status, nodes: [.status.node[] | {id, status, publicIp, privateIp}]}'
```

```json
{
  "id": "gpu-vm",
  "status": "Running:3 (R:3/3)",
  "nodes": [
    {"id": "aws-2", "status": "Running", "publicIp": "203.0.113.11", "privateIp": "198.51.100.11"},
    {"id": "azure-1", "status": "Running", "publicIp": "203.0.113.12", "privateIp": "198.51.100.12"},
    {"id": "gcp-us-central1-2", "status": "Running", "publicIp": "203.0.113.13", "privateIp": "198.51.100.13"}
  ]
}
```

**5. 노드 정보** (`$AWS` 자리에 `$AZURE`, `$GCP` 를 넣어 셋 다 부름)

```sh
curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/ns/$NS/infra/$INFRA/node/$AWS" \
  | jq '{id, status, connectionName, specId, imageId, publicIP, privateIP, sshPort, nodeUserName, sshKeyId, securityGroupIds, os: .image.osType}'
```

```json
{
  "id": "aws-2",
  "status": "Running",
  "connectionName": "aws-us-east-1",
  "specId": "aws+us-east-1+g6.2xlarge",
  "imageId": "ami-04d7347cf1033bb39",
  "publicIP": "203.0.113.11",
  "privateIP": "198.51.100.11",
  "sshPort": 22,
  "nodeUserName": "cb-user",
  "sshKeyId": "gpu-vm-aws-us-east-1",
  "securityGroupIds": ["gpu-vm-aws"],
  "os": "Ubuntu 24.04"
}
```

| 노드 | `specId` | `imageId` | `sshKeyId` | `securityGroupIds` |
| --- | --- | --- | --- | --- |
| `aws-2` | `aws+us-east-1+g6.2xlarge` | `ami-04d7347cf1033bb39` | `gpu-vm-aws-us-east-1` | `gpu-vm-aws` |
| `azure-1` | `azure+eastus+standard_nc40ads_h100_v5` | `microsoft-dsvm:ubuntu-hpc:2404:24.04.2026092501` | `gpu-vm-azure-eastus` | `gpu-vm-azure` |
| `gcp-us-central1-2` | `gcp+us-central1+g2-standard-8` | `ubuntu-accelerator-2404-amd64-with-nvidia-580-v20261104a` | `gpu-vm-gcp-us-central1` | `gpu-vm-gcp-us-central1` |

6~8 은 이 표의 값을 넣어 부릅니다.

**6. 스펙** (스펙은 네임스페이스 `system` 에 있음. `+` 는 URL 에서 `%2B` 로 씀)

```sh
curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/ns/system/resources/spec/aws%2Bus-east-1%2Bg6.2xlarge" \
  | jq '{id, cspSpecName, vCPU, memoryGiB, acceleratorType, acceleratorModel, acceleratorCount, acceleratorMemoryGB, costPerHour}'
```

```json
{
  "id": "aws+us-east-1+g6.2xlarge",
  "cspSpecName": "g6.2xlarge",
  "vCPU": 8,
  "memoryGiB": 32,
  "acceleratorType": "gpu",
  "acceleratorModel": "NVIDIA L4",
  "acceleratorCount": 1,
  "acceleratorMemoryGB": 24,
  "costPerHour": 0.9776
}
```

| 노드 | `acceleratorModel` | `acceleratorMemoryGB` | `vCPU` / `memoryGiB` | `costPerHour` |
| --- | --- | --- | --- | --- |
| `aws-2` | NVIDIA L4 | 24 | 8 / 32 | 0.9776 |
| `azure-1` | NVIDIA H100 | 94 | 40 / 320 | 6.98 |
| `gcp-us-central1-2` | NVIDIA L4 | 24 | 8 / 32 | 0.853624 |

**7. SSH 키** (응답에 개인키가 들어 있으므로 jq 로 있는지만 찍음)

```sh
curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/ns/$NS/resources/sshKey/gpu-vm-aws-us-east-1" \
  | jq '{id, connectionName, hasPrivateKey: ((.privateKey // "") != "")}'
```

```json
{"id": "gpu-vm-aws-us-east-1", "connectionName": "aws-us-east-1", "hasPrivateKey": true}
```

**8. 보안 그룹**

```sh
curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/ns/$NS/resources/securityGroup/gpu-vm-aws" \
  | jq '{id, connectionName, inbound: [.firewallRules[] | select(.Direction == "inbound") | {Protocol, Port, CIDR}]}'
```

```json
{"id": "gpu-vm-aws", "connectionName": "aws-us-east-1", "inbound": [{"Protocol": "TCP", "Port": "22", "CIDR": "0.0.0.0/0"}]}
```

판정:

| 보는 것 | 기대 | 다를 때 |
| --- | --- | --- |
| 1 `ready` | `true` | CB-Tumblebug 이 안 떠 있음. AI-App 배포도 노드를 못 읽어 실패 |
| 4 `status` | `Running:3 (R:3/3)` | 노드가 `Suspended` 등이면 배포 전에 CB-Tumblebug 에서 켬 |
| 4 노드 ID | `aws-2`, `azure-1`, `gcp-us-central1-2` | 바뀌었으면 4절의 `AWS`·`AZURE`·`GCP` 변수를 고침 |
| 6 `acceleratorType` | 셋 다 `gpu` | `gpu` 가 아니면 AI-App 이 GPU 없이 배포함. 배포 요청에 `GPU=true` 를 붙임 |
| 7 `hasPrivateKey` | 셋 다 `true` | 없으면 AI-App 이 노드에 접속하지 못해 배포가 `failed` |
| 8 `inbound` | `TCP` `22` | 22 가 없으면 Semaphore 가 노드에 접속하지 못함. 서빙 포트는 배포가 끝날 때 AI-App 이 더함(10절에서 같은 호출로 확인) |

### 5-4. 노드 점검

예시 응용의 배포 스크립트가 노드에 있어야 하는 도구를 요구합니다. Dify 는 `git`, Langflow 는 `python3` 이 없으면 배포가 실패합니다.
노드에 다른 프로세스가 서빙 포트를 잡고 있으면 배포와 부딪히므로 같이 봅니다. 노드 키는 VM 을 만들 때 받은 것(`aws.pem`, `azure.pem`, `gcp.pem`)을 씁니다.

```sh
cd <노드 키 폴더>
for n in aws:203.0.113.11 azure:203.0.113.12 gcp:203.0.113.13; do
  echo "== ${n%%:*}"
  timeout 30 ssh -n -i ${n%%:*}.pem -o ConnectTimeout=10 cb-user@${n#*:} '
    . /etc/os-release; echo "OS: $PRETTY_NAME"
    for t in git python3 docker; do printf "%s: " $t; command -v $t || echo "없음"; done
    docker compose version 2>/dev/null || echo "docker compose: 없음"
    nvidia-smi --query-gpu=name,memory.used,memory.total,driver_version --format=csv,noheader
    df -h / | tail -1
    ss -ltn | grep -E ":(80|3000|7860|8000|11434) " || echo "서빙 포트: 비어 있음"'
done
cd ~/ai-app/docs/api/examples
```

결과(처음 접속할 때 나오는 호스트 키 확인 줄은 뺐습니다):

```
== aws
OS: Ubuntu 24.04.5 LTS
git: /usr/bin/git
python3: /usr/bin/python3
docker: /usr/bin/docker
Docker Compose version v5.7.0
NVIDIA L4, 0 MiB, 23034 MiB, 595.112.07
/dev/root       123G   48G   76G  39% /
서빙 포트: 비어 있음
== azure
OS: Ubuntu 24.04.5 LTS
git: /usr/bin/git
python3: /usr/bin/python3
docker: /usr/bin/docker
docker compose: 없음
NVIDIA H100 NVL, 0 MiB, 95830 MiB, 580.178.04
/dev/root       123G   44G   80G  36% /
서빙 포트: 비어 있음
== gcp
OS: Ubuntu 24.04.5 LTS
git: /usr/bin/git
python3: /usr/bin/python3
docker: 없음
docker compose: 없음
NVIDIA L4, 0 MiB, 23034 MiB, 580.178.04
/dev/root       123G  4.0G  120G   4% /
서빙 포트: 비어 있음
```

| 보는 것 | 기대 | 다를 때 |
| --- | --- | --- |
| `git` (aws) | 경로가 나옴 | 없으면 Dify 배포가 `git is not installed on this node` 로 실패함 |
| `python3` (gcp) | 경로가 나옴 | 없으면 Langflow 는 뜨지만 배포가 `python3 is not installed on this node` 로 실패함 |
| `docker`, `docker compose` | 있어도 되고 없어도 됨 | 없으면 AI-App 이 `install-docker` 단계에서 깖. 이번에는 azure 의 compose 플러그인, gcp 의 docker 를 AI-App 이 깔았음 |
| GPU `memory.used` | 수 MiB 정도 | 크면 다른 프로세스가 GPU 를 쓰고 있음 |
| 디스크 여유 | 수십 GB 이상 | 런타임 이미지와 가중치(8B 모델 기준 수 GB\~16 GB)가 들어가야 함 |
| 서빙 포트 | `비어 있음` | 무엇이 듣고 있는지 보고, 그 프로세스를 정리한 뒤 진행 |

### 5-5. 배포 요청 미리 보내 보기 (dry-run)

dry-run 이 켜진 지금 배포를 요청하면 AI-App 이 CB-Tumblebug 에서 노드·SSH 키·스펙을 읽고 compose 파일을 만든 뒤 **Semaphore 앞에서 멈춥니다.**
노드에는 아무것도 하지 않으니, 노드 ID·모델 ID 오타와 GPU 판정을 실배포 전에 걸러낼 수 있습니다.

```sh
./03-deploy-inference.sh $NS $INFRA $AWS   qwen3-embedding-0.6b hf-bf16 vllm
./03-deploy-inference.sh $NS $INFRA $AZURE qwen3-vl-8b ollama-gguf ollama
./03-deploy-inference.sh $NS $INFRA $GCP   ministral-3-8b hf-fp8 vllm
```

**부르는 API** (`03-deploy-inference.sh nsId infraId nodeId [modelId] [artifactId] [runtimeId]`)

| 순서 | 호출 | 보내는 것 | 응답에서 읽는 것 |
| --- | --- | --- | --- |
| 1 | `POST /deployments` | `Content-Type: application/json`, 아래 본문 | `id` |
| 2 | `GET /deployments/{id}/progress` (반복) | - | 4절 `wait_deployment` 의 필드. dry-run 이면 첫 번째 읽기에서 `done: true` |
| 3 | `GET /deployments/{id}` | - | 4절 `summarize_deployment` 의 필드 |

AWS 줄이 보내는 본문:

```json
{
  "type": "inference",
  "targets": [
    {"kind": "node", "nsId": "gpu-vm", "infraId": "gpu-vm", "nodeId": "aws-2"}
  ],
  "inference": {
    "modelId": "qwen3-embedding-0.6b",
    "artifactId": "hf-bf16",
    "runtimeId": "vllm"
  }
}
```

환경 변수를 주면 본문에 이것이 더해집니다. 이번 시험에서는 아무것도 주지 않았습니다.

| 환경 변수 | 들어가는 자리 | 뜻 |
| --- | --- | --- |
| `GPU=true\|false` | `inference.gpu` | 주지 않으면 CB-Tumblebug 스펙의 `acceleratorType` 으로 정함 |
| `HF_TOKEN` | `inference.env.HF_TOKEN` | 접근 승인이 필요한 Hugging Face 모델용. 배포 기록과 노드에 평문으로 남음 |
| `API_KEY` | `inference.apiKey` | vLLM 만. 주지 않으면 같은 노드의 이전 vLLM 키를 그대로 쓰거나 새로 만듦 |
| `SSH_USER` | `targets[].sshUser` | CB-Tumblebug 이 알려 준 사용자 대신 이 사용자로 playbook 을 돌림 |

같은 호출을 curl 로:

```sh
curl -s -X POST $AIAPP_URL/deployments -H 'Content-Type: application/json' -d '{
  "type": "inference",
  "targets": [{"kind": "node", "nsId": "gpu-vm", "infraId": "gpu-vm", "nodeId": "aws-2"}],
  "inference": {"modelId": "qwen3-embedding-0.6b", "artifactId": "hf-bf16", "runtimeId": "vllm"}
}' | jq '{id, status, dryRun}'
```

응답 코드는 dry-run 여부로 갈립니다.

| 코드 | 언제 |
| --- | --- |
| `200` | dry-run. 파일만 두고 끝난 기록(`status: dry-run`)이 바로 옴 |
| `202` | 실배포 접수. `status: pending` 인 기록이 오고 배포는 뒤에서 진행됨. `/progress` 를 `done: true` 까지 읽음 |
| `400` / `404` / `422` | 요청 형식 오류 / 모델·아티팩트·런타임·응용·노드 없음 / 런타임이 못 돌리는 아티팩트. 기록이 남지 않음 |
| `502` | 노드를 읽는 동안 CB-Tumblebug 이 실패하거나 답하지 않음 |

결과:

```
POST /deployments -> HTTP 200
deployment id: 50ef4bc3-24e6-4537-9b8f-dabfab3c021a
deployment 50ef4bc3-24e6-4537-9b8f-dabfab3c021a inference status=dry-run dryRun=true
  gpu-vm/gpu-vm/aws-2 dry-run phase=staging sshUser=cb-user serviceUrl=http://203.0.113.11:8000/v1 apiKey=****4ed4 ports=8000
POST /deployments -> HTTP 200
deployment id: 099b23c8-ddd9-4627-be54-9722882dba63
deployment 099b23c8-ddd9-4627-be54-9722882dba63 inference status=dry-run dryRun=true
  gpu-vm/gpu-vm/azure-1 dry-run phase=staging sshUser=cb-user serviceUrl=http://203.0.113.12:11434/v1 ports=11434
POST /deployments -> HTTP 200
deployment id: 06df36c8-0ac0-40b8-bfda-f6cdcd86f8c4
deployment 06df36c8-0ac0-40b8-bfda-f6cdcd86f8c4 inference status=dry-run dryRun=true
  gpu-vm/gpu-vm/gcp-us-central1-2 dry-run phase=staging sshUser=cb-user serviceUrl=http://203.0.113.13:8000/v1 apiKey=****4e05 ports=8000
```

이 기록은 배포 목록에 `dry-run` 으로 남고 `/endpoints` 에는 나오지 않습니다.

## 6. dry-run 끄기

**여기부터 노드가 바뀝니다.** `.env` 가 없으니 새로 만들고 AI-App 서버만 다시 만듭니다.
다른 설정(CB-Tumblebug·Semaphore 계정)은 compose 기본값 그대로라서 `.env` 에 이 한 줄만 넣으면 됩니다.

```sh
cd ~/ai-app
echo 'AIAPP_TUMBLEBUG_DRY_RUN=false' >> .env
docker compose up -d ai-app-server
for i in $(seq 1 30); do curl -sf localhost:8090/aiapp/readyz >/dev/null && break; sleep 2; done
curl -s localhost:8090/aiapp/status | jq .dryRun
cd ~/ai-app/docs/api/examples
```

```
[+] Running 2/2
 ✔ Container aiapp-volume-init  Exited
 ✔ Container ai-app-server      Started
false
```

서버가 다시 뜨면서 카탈로그 동기화가 한 번 돌지만, 카탈로그 파일이 이미 있어서 기본 모델 3개는 그대로입니다.

## 7. AI 응용 3종 등록

`examples/apps/` 의 응용 폴더를 AI-App 에 등록합니다. 한 번만 하면 됩니다.

```sh
./02-upload-app.sh ../../../examples/apps/dify
./02-upload-app.sh ../../../examples/apps/open-webui
./02-upload-app.sh ../../../examples/apps/langflow
./01-catalog.sh | sed -n '/# apps/,$p'
```

**부르는 API** (`02-upload-app.sh [폴더] [이름] [포트]`)

| 순서 | 호출 | 보내는 것 | 응답에서 읽는 것 |
| --- | --- | --- | --- |
| 1 | `POST /apps` | `multipart/form-data`. 아래 표의 필드 | `version`, `ports`, `files[].{name, sizeBytes}` |

| form 필드 | 값 |
| --- | --- |
| `name` | 둘째 인자, 없으면 폴더 이름 (`dify`, `open-webui`, `langflow`) |
| `script` | 폴더의 `deploy.sh` (필수. 없으면 스크립트가 요청 전에 멈춤) |
| `ports` | 셋째 인자, 없으면 이름별 기본값(`dify` 80, `open-webui` 3000, `langflow` 7860)을 `,` 로 이은 문자열 |
| `files` | `deploy.sh` 밖의 폴더 안 파일을 하나씩 반복해서 붙임 |

같은 호출을 curl 로 (Open WebUI):

```sh
curl -s -X POST $AIAPP_URL/apps \
  -F name=open-webui \
  -F script=@../../../examples/apps/open-webui/deploy.sh \
  -F files=@../../../examples/apps/open-webui/docker-compose.yml \
  -F ports=3000 | jq '{version, ports, files: [.files[] | "\(.name) \(.sizeBytes)B"]}'
```

같은 이름으로 다시 올리면 버전이 하나 오르고, 배포는 가장 높은 버전을 씁니다. Dify 폴더에는 `deploy.sh` 만 있어서 `files` 가 비어 있습니다(Dify 공식 compose 는 배포 때 노드에서 `git` 으로 받음).

결과:

```
POST /apps -> HTTP 200
{"version": 1, "ports": [80], "files": []}
POST /apps -> HTTP 200
{"version": 1, "ports": [3000], "files": ["docker-compose.yml 756B"]}
POST /apps -> HTTP 200
{"version": 1, "ports": [7860], "files": ["docker-compose.yml 1903B"]}
GET /runtimes -> HTTP 200
GET /models -> HTTP 200
GET /apps -> HTTP 200
# apps (name version ports)
3 of 3 apps from offset 0
dify v1 ports=80
langflow v1 ports=7860
open-webui v1 ports=3000
```

## 8. 추론 서비스 3개 배포

5-5 와 같은 명령이지만 이번에는 dry-run 이 꺼져 있어 노드에 실제로 올라갑니다. 부르는 API 는 5-5 와 같고, 응답이 `202` 라서 스크립트가 `/progress` 를 `done: true` 까지 10초마다 읽는 것만 다릅니다.
처음 배포는 docker·NVIDIA Container Toolkit 설치, 런타임 이미지와 가중치 다운로드가 들어가서 노드당 3\~12분 걸렸습니다. 셋은 서로 독립이니 터미널 세 개에서 동시에 돌려도 됩니다.

```sh
# AWS: vLLM + Qwen3-Embedding-0.6B
./03-deploy-inference.sh $NS $INFRA $AWS qwen3-embedding-0.6b hf-bf16 vllm

# Azure: Ollama + Qwen3-VL-8B
./03-deploy-inference.sh $NS $INFRA $AZURE qwen3-vl-8b ollama-gguf ollama

# GCP: vLLM + Ministral 3 8B
./03-deploy-inference.sh $NS $INFRA $GCP ministral-3-8b hf-fp8 vllm
```

| 노드 | 이 배포에서 일어나는 일 |
| --- | --- |
| 공통 | `install-gpu-driver` 로 드라이버 확인 → `install-docker` 로 docker·compose·NVIDIA Container Toolkit → `compose-up` 으로 런타임 기동 → 보안 그룹에 포트 열기 |
| AWS, GCP (vLLM) | vLLM 이 뜨면서 Hugging Face 에서 가중치를 받음. `/health` 가 응답하면 `nvidia-smi` 에 vLLM 프로세스가 있는지 확인 |
| GCP (Ministral) | 카탈로그의 인자 `--max-model-len 32768` 과 mistral 형식 플래그가 자동으로 붙음. L4 에서 이 인자 없이 띄우면 KV 캐시가 모자라 뜨지 않음 |
| Azure (Ollama) | 엔진이 뜬 뒤 `model-pull` 컨테이너가 `qwen3-vl:8b` 를 받음. 엔진 로그에서 `library=CUDA` 를 확인 |

결과(같은 태스크가 이어지는 줄은 줄였습니다):

```
POST /deployments -> HTTP 202
deployment id: 0eb3998a-991e-489d-97f0-bedb58b43f9f
[1/180] 0eb3998a-991e-489d-97f0-bedb58b43f9f deploying 0s
  aws-2 reading-node 0/2
[2/180] 0eb3998a-991e-489d-97f0-bedb58b43f9f deploying 10s
  aws-2 install-gpu-driver 0/3 (Read the vendors of the display controllers)
[3/180] 0eb3998a-991e-489d-97f0-bedb58b43f9f deploying 20s
  aws-2 install-docker 1/3 (Read the runtimes docker knows)
[5/180] 0eb3998a-991e-489d-97f0-bedb58b43f9f deploying 40s
  aws-2 compose-up 2/3 (Start the project, replacing what the previous deployment left)
  ...
[32/180] 0eb3998a-991e-489d-97f0-bedb58b43f9f deploying 310s
  aws-2 compose-up 2/3 (Wait until the service answers)
  ...
[40/180] 0eb3998a-991e-489d-97f0-bedb58b43f9f deploying 390s
  aws-2 compose-up 2/3 (Wait until the service answers)
deployment 0eb3998a-991e-489d-97f0-bedb58b43f9f inference status=running dryRun=false
  gpu-vm/gpu-vm/aws-2 running phase=done sshUser=cb-user serviceUrl=http://203.0.113.11:8000/v1 apiKey=****4ed4 ports=8000

POST /deployments -> HTTP 202
deployment id: 16704908-1c22-4397-b582-6d6475c36659
[1/180] 16704908-1c22-4397-b582-6d6475c36659 deploying 0s
  azure-1 reading-node 0/2
[3/180] 16704908-1c22-4397-b582-6d6475c36659 deploying 20s
  azure-1 install-docker 1/3 (Find a compose package in the node's apt sources)
[6/180] 16704908-1c22-4397-b582-6d6475c36659 deploying 50s
  azure-1 compose-up 2/3 (Start the project, replacing what the previous deployment left)
  ...
[15/180] 16704908-1c22-4397-b582-6d6475c36659 deploying 140s
  azure-1 compose-up 2/3 (Wait until the one-shot services exit)
  ...
[20/180] 16704908-1c22-4397-b582-6d6475c36659 deploying 190s
  azure-1 compose-up 2/3 (Wait until the one-shot services exit)
deployment 16704908-1c22-4397-b582-6d6475c36659 inference status=running dryRun=false
  gpu-vm/gpu-vm/azure-1 running phase=done sshUser=cb-user serviceUrl=http://203.0.113.12:11434/v1 ports=11434

POST /deployments -> HTTP 202
deployment id: a6f274a8-e83b-4cf1-8390-eacd915782d5
[1/180] a6f274a8-e83b-4cf1-8390-eacd915782d5 deploying 0s
  gcp-us-central1-2 reading-node 0/2
[2/180] a6f274a8-e83b-4cf1-8390-eacd915782d5 deploying 10s
  gcp-us-central1-2 install-gpu-driver 0/3 (Install pciutils)
[4/180] a6f274a8-e83b-4cf1-8390-eacd915782d5 deploying 30s
  gcp-us-central1-2 install-docker 1/3 (Install the repository prerequisites)
[11/180] a6f274a8-e83b-4cf1-8390-eacd915782d5 deploying 100s
  gcp-us-central1-2 install-docker 1/3 (Install Docker Engine and the compose plugin)
[21/180] a6f274a8-e83b-4cf1-8390-eacd915782d5 deploying 200s
  gcp-us-central1-2 install-docker 1/3 (Install the toolkit)
[24/180] a6f274a8-e83b-4cf1-8390-eacd915782d5 deploying 230s
  gcp-us-central1-2 compose-up 2/3 (Start the project, replacing what the previous deployment left)
  ...
[50/180] a6f274a8-e83b-4cf1-8390-eacd915782d5 deploying 491s
  gcp-us-central1-2 compose-up 2/3 (Wait until the service answers)
  ...
[70/180] a6f274a8-e83b-4cf1-8390-eacd915782d5 deploying 691s
  gcp-us-central1-2 opening-ports 3/3
deployment a6f274a8-e83b-4cf1-8390-eacd915782d5 inference status=running dryRun=false
  gpu-vm/gpu-vm/gcp-us-central1-2 running phase=done sshUser=cb-user serviceUrl=http://203.0.113.13:8000/v1 apiKey=****4e05 ports=8000
```

GCP 노드는 docker 가 없어서 `install-docker` 에 약 3분이 더 들었습니다. **배포 ID 는 13절(내리기)에 쓰니 적어 둡니다.**

GPU 위에서 도는지 확인한 근거 줄은 배포 기록의 `nodes[].gpuCheck` 에 남습니다.

```sh
curl -s $AIAPP_URL/deployments/<ID> | jq -r '.nodes[] | "\(.target.nodeId) \(.status) \(.gpuCheck // "-")"'
```

| 런타임 | `gpuCheck` 예 |
| --- | --- |
| vLLM | `nvidia-smi: 3499, VLLM::EngineCore, 13524 MiB` |
| Ollama | `log: ... msg="inference compute" id=0 library=CUDA ...` |

## 9. 주소와 키를 읽고 응용 3개 배포

### 9-1. 추론 서비스 주소와 키

```sh
SHOW_API_KEY=true TYPE=inference ./05-endpoints.sh $NS $INFRA
```

**부르는 API** (`05-endpoints.sh [nsId] [infraId] [nodeId]`)

| 순서 | 호출 | 보내는 것 | 응답에서 읽는 것 |
| --- | --- | --- | --- |
| 1 | `GET /endpoints` | 쿼리 `nsId`, `infraId`, `nodeId`(인자), `type`(`TYPE`), `available`(`AVAILABLE`), `limit`, `offset`. 값이 있는 것만 붙임 | `total`, `offset`, `endpoints[]` 의 아래 필드 |

| `endpoints[]` 필드 | 추론 서비스 | 응용 |
| --- | --- | --- |
| 공통 | `nsId`, `infraId`, `nodeId`, `workKey`(`ms-<런타임>` / `app-<이름>`), `deploymentId`, `nodeStatus`(CB-Tumblebug 노드 상태), `available`, `portsOpened`, `portsError` | 같음 |
| 종류별 | `runtimeId`, `modelId`, `artifactId`, `url`(OpenAI 호환 API), `healthUrl`, `apiKey`(vLLM 만), `host` | `name`, `version`, `urls[].url` |

API 응답에는 vLLM 키가 그대로 들어 있고, 스크립트가 끝 4자리만 남겨 찍습니다. `SHOW_API_KEY=true` 면 전체를 찍습니다. 주소는 노드의 지금 IP 로 만들어지므로 VM 을 껐다 켜서 IP 가 바뀌면 여기 값도 바뀝니다.

같은 호출을 curl 로:

```sh
curl -s "$AIAPP_URL/endpoints?nsId=$NS&infraId=$INFRA&type=inference" | jq .
```

결과(아래 키는 문서에서 끝 4자리만 남겼습니다. 실제로는 32자 전체가 나옴):

```
GET /endpoints -> HTTP 200
3 of 3 endpoints from offset 0
gpu-vm/gpu-vm/aws-2 ms-vllm deployment=0eb3998a-991e-489d-97f0-bedb58b43f9f node=Running available=true portsOpened=true
  vllm qwen3-embedding-0.6b/hf-bf16 url=http://203.0.113.11:8000/v1 health=http://203.0.113.11:8000/health apiKey=****4ed4
gpu-vm/gpu-vm/azure-1 ms-ollama deployment=16704908-1c22-4397-b582-6d6475c36659 node=Running available=true portsOpened=true
  ollama qwen3-vl-8b/ollama-gguf url=http://203.0.113.12:11434/v1 health=http://203.0.113.12:11434/
gpu-vm/gpu-vm/gcp-us-central1-2 ms-vllm deployment=a6f274a8-e83b-4cf1-8390-eacd915782d5 node=Running available=true portsOpened=true
  vllm ministral-3-8b/hf-fp8 url=http://203.0.113.13:8000/v1 health=http://203.0.113.13:8000/health apiKey=****4e05
```

응용 배포에 넘길 값을 같은 API 로 읽어 변수에 담습니다.

```sh
EP=$(curl -s "$AIAPP_URL/endpoints?nsId=$NS&infraId=$INFRA&type=inference")
pick() { jq -r --arg n "$1" --arg f "$2" '.endpoints[] | select(.nodeId == $n) | .[$f]' <<<"$EP"; }
AWS_URL=$(pick $AWS url)
AWS_KEY=$(pick $AWS apiKey)
AZURE_HOST=$(pick $AZURE host)
GCP_URL=$(pick $GCP url)
GCP_KEY=$(pick $GCP apiKey)
echo "AWS_URL=$AWS_URL AZURE_HOST=$AZURE_HOST GCP_URL=$GCP_URL"
```

```
AWS_URL=http://203.0.113.11:8000/v1 AZURE_HOST=203.0.113.12 GCP_URL=http://203.0.113.13:8000/v1
```

### 9-2. 추론 서비스 직접 불러 보기

응용을 붙이기 전에 모델이 실제로 답하는지 봅니다. 여기서부터는 AI-App 이 아니라 노드의 추론 서비스를 직접 부릅니다. vLLM 은 `Authorization: Bearer <apiKey>` 가 없으면 `/v1/*` 에 `401` 을 줍니다.

```sh
# AWS: 임베딩. 벡터 길이가 나옴
curl -s $AWS_URL/embeddings -H "Authorization: Bearer $AWS_KEY" -H 'Content-Type: application/json' \
  -d '{"model": "Qwen/Qwen3-Embedding-0.6B", "input": "서울 종로 맛집"}' | jq '.data[0].embedding | length'

# Azure: Ollama 생성
curl -s http://$AZURE_HOST:11434/api/generate \
  -d '{"model": "qwen3-vl:8b", "prompt": "2 + 2 는?", "stream": false}' | jq '{done, response, thinking: (.thinking // "" | .[0:80])}'

# GCP: 대화
curl -s $GCP_URL/chat/completions -H "Authorization: Bearer $GCP_KEY" -H 'Content-Type: application/json' \
  -d '{"model": "mistralai/Ministral-3-8B-Instruct-2512", "messages": [{"role": "user", "content": "대한민국의 수도는?"}], "max_tokens": 64}' \
  | jq -r '.choices[0].message.content'
```

| 노드 | 호출 | 기대 결과 | 받은 결과 |
| --- | --- | --- | --- |
| AWS | `POST /v1/embeddings` | `1024` | `1024` |
| Azure | `POST /api/generate` (Ollama 고유 API) | `done: true` | `done: true`, `response: "2 + 2 = **4** ..."`. `qwen3-vl:8b` 는 답 전에 생각을 길게 써서 `response` 가 비고 `thinking` 만 찰 때도 있음 |
| GCP | `POST /v1/chat/completions` | "서울특별시" 가 든 문장 | `대한민국의 수도는 **서울특별시**입니다.` |

vLLM 의 `model` 값은 카탈로그 artifact 의 `ref`(Hugging Face repo id) 그대로입니다.

### 9-3. 응용 배포

```sh
# AWS: Dify. VLLM_BASE_URL 은 화면에 넣을 값을 로그에 찍기만 함
./04-deploy-app.sh $NS $INFRA $AWS dify VLLM_BASE_URL=$AWS_URL

# Azure: Open WebUI. Ollama 루트 주소(/v1 없이)
./04-deploy-app.sh $NS $INFRA $AZURE open-webui OLLAMA_BASE_URL=http://$AZURE_HOST:11434

# GCP: Langflow. 관리자 비밀번호는 정해서 넣고, 다시 배포할 때도 같은 값을 씀
LANGFLOW_PW='<정한 비밀번호>'
./04-deploy-app.sh $NS $INFRA $GCP langflow \
  LANGFLOW_SUPERUSER_PASSWORD="$LANGFLOW_PW" MODEL_BASE_URL=$GCP_URL MODEL_API_KEY=$GCP_KEY
```

**부르는 API** (`04-deploy-app.sh nsId infraId nodeId appName [KEY=VALUE ...]`)

| 순서 | 호출 | 보내는 것 | 응답에서 읽는 것 |
| --- | --- | --- | --- |
| 1 | `POST /deployments` | `Content-Type: application/json`, 아래 본문 | `id` (응답 `202`) |
| 2 | `GET /deployments/{id}/progress` (반복) | - | 4절 `wait_deployment` 의 필드 |
| 3 | `GET /deployments/{id}` | - | 4절 `summarize_deployment` 의 필드 |

GCP 줄이 보내는 본문(`KEY=VALUE` 인자가 전부 `app.env` 로 들어감):

```json
{
  "type": "app",
  "targets": [
    {"kind": "node", "nsId": "gpu-vm", "infraId": "gpu-vm", "nodeId": "gcp-us-central1-2"}
  ],
  "app": {
    "name": "langflow",
    "env": {
      "LANGFLOW_SUPERUSER_PASSWORD": "<정한 비밀번호>",
      "MODEL_BASE_URL": "http://203.0.113.13:8000/v1",
      "MODEL_API_KEY": "<GCP vLLM 키>"
    }
  }
}
```

| 환경 변수 | 들어가는 자리 | 뜻 |
| --- | --- | --- |
| `APP_VERSION=N` | `app.version` | 주지 않으면 가장 높은 버전 |
| `APP_PORTS=a,b` | `app.ports` | 응용을 올릴 때 정한 포트 대신 이 포트를 엶 |
| `SSH_USER` | `targets[].sshUser` | 5-5 와 같음 |

같은 호출을 curl 로 (Azure):

```sh
curl -s -X POST $AIAPP_URL/deployments -H 'Content-Type: application/json' -d '{
  "type": "app",
  "targets": [{"kind": "node", "nsId": "gpu-vm", "infraId": "gpu-vm", "nodeId": "azure-1"}],
  "app": {"name": "open-webui", "env": {"OLLAMA_BASE_URL": "http://203.0.113.12:11434"}}
}' | jq '{id, status}'
```

`app.env` 는 노드에서 스크립트가 도는 폴더의 `.env` 로 쓰이고, 스크립트가 시작하기 전에 export 됩니다. 응용은 배포 스크립트가 끝난 뒤 포트가 HTTP 로 응답하고 컨테이너가 재시작 없이 돌아야 `running` 이 됩니다.

| 응용 | 넘긴 env | 주의 |
| --- | --- | --- |
| Dify | `VLLM_BASE_URL` | Dify 공식 compose 를 노드에서 `git` 으로 받아 띄움. 컨테이너가 많음 |
| Open WebUI | `OLLAMA_BASE_URL` | `/v1` 을 붙이면 모델 목록이 안 나옴 |
| Langflow | `LANGFLOW_SUPERUSER_PASSWORD`, `MODEL_BASE_URL`, `MODEL_API_KEY` | 비밀번호는 처음 배포 때 Langflow DB 에 저장됨. 다음 배포에 다른 값을 주면 `Langflow refused the admin login` 으로 실패. `MODEL_*` 는 Langflow 전역 변수로 들어감 |

결과(같은 태스크가 이어지는 줄은 줄였습니다):

```
POST /deployments -> HTTP 202
deployment id: d7f87c47-e69c-4c97-b6a3-d0f3b6905086
[1/180] d7f87c47-e69c-4c97-b6a3-d0f3b6905086 deploying 0s
  aws-2 reading-node 0/2
[4/180] d7f87c47-e69c-4c97-b6a3-d0f3b6905086 deploying 30s
  aws-2 copy-and-run 2/3 (Copy the staged files)
[5/180] d7f87c47-e69c-4c97-b6a3-d0f3b6905086 deploying 40s
  aws-2 copy-and-run 2/3 (Run the script)
  ...
[20/180] d7f87c47-e69c-4c97-b6a3-d0f3b6905086 deploying 190s
  aws-2 copy-and-run 2/3 (Let the containers settle)
[23/180] d7f87c47-e69c-4c97-b6a3-d0f3b6905086 deploying 220s
  aws-2 copy-and-run 2/3 (Check that the containers of the project are stable)
deployment d7f87c47-e69c-4c97-b6a3-d0f3b6905086 app status=running dryRun=false
  gpu-vm/gpu-vm/aws-2 running phase=done sshUser=cb-user ports=80

POST /deployments -> HTTP 202
deployment id: 78d5d7c7-a8d1-4831-896b-91742310d6e9
[1/180] 78d5d7c7-a8d1-4831-896b-91742310d6e9 deploying 0s
  azure-1 reading-node 0/2
[4/180] 78d5d7c7-a8d1-4831-896b-91742310d6e9 deploying 30s
  azure-1 copy-and-run 2/3 (Place the files)
[5/180] 78d5d7c7-a8d1-4831-896b-91742310d6e9 deploying 40s
  azure-1 copy-and-run 2/3 (Run the script)
  ...
[15/180] 78d5d7c7-a8d1-4831-896b-91742310d6e9 deploying 140s
  azure-1 copy-and-run 2/3 (Wait until the application ports answer)
  ...
[21/180] 78d5d7c7-a8d1-4831-896b-91742310d6e9 deploying 200s
  azure-1 copy-and-run 2/3 (Let the containers settle)
deployment 78d5d7c7-a8d1-4831-896b-91742310d6e9 app status=running dryRun=false
  gpu-vm/gpu-vm/azure-1 running phase=done sshUser=cb-user ports=3000

POST /deployments -> HTTP 202
deployment id: f633fe00-18b5-44df-a932-c88ac27ef735
[1/180] f633fe00-18b5-44df-a932-c88ac27ef735 deploying 0s
  gcp-us-central1-2 reading-node 0/2
[5/180] f633fe00-18b5-44df-a932-c88ac27ef735 deploying 40s
  gcp-us-central1-2 copy-and-run 2/3 (Copy the staged files)
[6/180] f633fe00-18b5-44df-a932-c88ac27ef735 deploying 50s
  gcp-us-central1-2 copy-and-run 2/3 (Run the script)
  ...
[17/180] f633fe00-18b5-44df-a932-c88ac27ef735 deploying 160s
  gcp-us-central1-2 copy-and-run 2/3 (Check that the containers of the project are stable)
[18/180] f633fe00-18b5-44df-a932-c88ac27ef735 deploying 170s
  gcp-us-central1-2 opening-ports 3/3
deployment f633fe00-18b5-44df-a932-c88ac27ef735 app status=running dryRun=false
  gpu-vm/gpu-vm/gcp-us-central1-2 running phase=done sshUser=cb-user ports=7860
```

**배포 ID 를 적어 둡니다.**

## 10. Endpoint 확인

```sh
./05-endpoints.sh $NS $INFRA
```

부르는 API 는 9-1 과 같습니다(`type` 쿼리 없이 부르므로 응용 항목도 나옴).

```
GET /endpoints -> HTTP 200
6 of 6 endpoints from offset 0
gpu-vm/gpu-vm/aws-2 app-dify deployment=d7f87c47-e69c-4c97-b6a3-d0f3b6905086 node=Running available=true portsOpened=true
  dify v1 http://203.0.113.11:80
gpu-vm/gpu-vm/aws-2 ms-vllm deployment=0eb3998a-991e-489d-97f0-bedb58b43f9f node=Running available=true portsOpened=true
  vllm qwen3-embedding-0.6b/hf-bf16 url=http://203.0.113.11:8000/v1 health=http://203.0.113.11:8000/health apiKey=****4ed4
gpu-vm/gpu-vm/azure-1 app-open-webui deployment=78d5d7c7-a8d1-4831-896b-91742310d6e9 node=Running available=true portsOpened=true
  open-webui v1 http://203.0.113.12:3000
gpu-vm/gpu-vm/azure-1 ms-ollama deployment=16704908-1c22-4397-b582-6d6475c36659 node=Running available=true portsOpened=true
  ollama qwen3-vl-8b/ollama-gguf url=http://203.0.113.12:11434/v1 health=http://203.0.113.12:11434/
gpu-vm/gpu-vm/gcp-us-central1-2 app-langflow deployment=f633fe00-18b5-44df-a932-c88ac27ef735 node=Running available=true portsOpened=true
  langflow v1 http://203.0.113.13:7860
gpu-vm/gpu-vm/gcp-us-central1-2 ms-vllm deployment=a6f274a8-e83b-4cf1-8390-eacd915782d5 node=Running available=true portsOpened=true
  vllm ministral-3-8b/hf-fp8 url=http://203.0.113.13:8000/v1 health=http://203.0.113.13:8000/health apiKey=****4e05
```

| 응용 | 주소 | 처음 할 일 |
| --- | --- | --- |
| Dify | `http://203.0.113.11/install` | 관리자 계정을 만듦. 플러그인 `langgenius/openai_api_compatible` 을 설치하고, 모델 공급자 OpenAI-API-compatible 에 Text Embedding 모델 `Qwen/Qwen3-Embedding-0.6B`, API endpoint `http://203.0.113.11:8000/v1`, API Key `$AWS_KEY` 를 등록 |
| Open WebUI | `http://203.0.113.12:3000` | 처음 가입한 계정이 관리자. 모델 목록에 `qwen3-vl:8b` 가 보이면 연결된 것 |
| Langflow | `http://203.0.113.13:7860` | `admin` / 정한 비밀번호로 로그인. OpenAI 컴포넌트의 `OpenAI API Base` 칸에서 전역 변수 `MODEL_BASE_URL`, `OpenAI API Key` 칸에서 `MODEL_API_KEY`, 모델 이름 `mistralai/Ministral-3-8B-Instruct-2512` |

AI-App 이 보안 그룹에 서빙 포트를 열었는지 CB-Tumblebug 에서 직접 봅니다(5-3 의 8번 호출을 보안 그룹 셋에 대해).

```sh
for g in gpu-vm-aws gpu-vm-azure gpu-vm-gcp-us-central1; do
  curl -s -u "default:$TB_PW" "http://localhost:1323/tumblebug/ns/$NS/resources/securityGroup/$g" \
    | jq -c '{id, inbound: [.firewallRules[] | select(.Direction == "inbound") | "\(.Protocol)/\(.Port) \(.CIDR)"]}'
done
```

| 보안 그룹 | 기대 (22 에 더해 이 포트 번호들이 보임. 규칙 표기는 CB-Tumblebug 이 정함) |
| --- | --- |
| `gpu-vm-aws` | 8000, 80 |
| `gpu-vm-azure` | 11434, 3000 |
| `gpu-vm-gcp-us-central1` | 8000, 7860 |

이번 시험에서는 6건 모두 `portsOpened=true` 였고 응용 주소가 바깥에서 열렸습니다. 포트가 없으면 `./05-endpoints.sh` 의 `portsOpened`·`portsError` 와 배포 기록의 `notes` 를 봅니다.

## 11. 화면 확인

AI 추론 엔진과 AI 응용이 정상으로 도는지 화면에서 확인했습니다. 캡처 안의 노드 IP 는 문서용 값으로 바꿨습니다.

### 11-1. AWS Dify + vLLM 임베딩, Azure Ollama LLM

Dify 에 vLLM 임베딩 모델과 Azure 의 Ollama LLM 을 붙여, 서울시 관광 명소 데이터로 지식(RAG)을 만들고 답이 달라지는지 봤습니다.

1. Dify 주소로 들어가 관리자 계정을 만듭니다.

    <img src="etri-env-test-2026-11-18/01-dify-install.png" alt="Dify 관리자 계정 설정" width="100%">

2. 통합(연동) 메뉴에서 OpenAI-API-compatible 플러그인을 설치합니다.

    <img src="etri-env-test-2026-11-18/02-dify-openai-compatible.png" alt="OpenAI-API-compatible 설치" width="100%">

3. AWS vLLM 의 `qwen3-embedding-0.6b` 를 Text Embedding 모델로 등록합니다. API endpoint 는 9-1 의 `AWS_URL`, API Key 는 `AWS_KEY`, 모델 이름은 `Qwen/Qwen3-Embedding-0.6B` 입니다.

    <img src="etri-env-test-2026-11-18/03-dify-embedding-model.png" alt="임베딩 모델 등록" width="100%">

4. 서울 열린데이터광장의 [서울시 관광 명소](https://data.seoul.go.kr/dataList/OA-21050/S/1/datasetView.do) CSV 를 받습니다.

    <img src="etri-env-test-2026-11-18/04-seoul-attractions-data.png" alt="서울시 관광 명소 CSV" width="100%">

5. 지식 메뉴에서 받은 CSV 를 올리고, 3에서 등록한 임베딩 모델을 고릅니다.

    <img src="etri-env-test-2026-11-18/05-dify-knowledge-upload.png" alt="CSV 업로드" width="100%">

    <img src="etri-env-test-2026-11-18/06-dify-embedding-select.png" alt="임베딩 모델 선택" width="100%">

6. 임베딩이 끝나면 문서가 `사용 가능` 이 됩니다.

    <img src="etri-env-test-2026-11-18/07-dify-knowledge-processing.png" alt="임베딩 처리 중" width="100%">

    <img src="etri-env-test-2026-11-18/08-dify-knowledge-done.png" alt="임베딩 완료" width="100%">

    <img src="etri-env-test-2026-11-18/09-dify-knowledge-list.png" alt="지식 문서 목록" width="100%">

7. 검색 테스트에서 "을지로" 로 찾으면 을지로에 있는 명소 청크가 나옵니다.

    <img src="etri-env-test-2026-11-18/10-dify-retrieval-test.png" alt="지식 검색 테스트" width="100%">

8. Azure Ollama 의 `qwen3-vl:8b` 를 LLM 으로 등록합니다. API endpoint 는 `http://<Azure 노드>:11434/v1`, 키는 비워 둡니다.

    <img src="etri-env-test-2026-11-18/11-dify-ollama-llm.png" alt="Ollama LLM 등록" width="100%">

9. 지식 없이 LLM 만으로 "을지로에서 갈만한 곳을 추천해줘" 를 물으면 첫 답은 홍대 위주였고, 두 번째도 을지로 밖까지 넓게 답했습니다.

    <img src="etri-env-test-2026-11-18/12-dify-llm-only-1.png" alt="지식 없이 LLM 1" width="100%">

    <img src="etri-env-test-2026-11-18/13-dify-llm-only-2.png" alt="지식 없이 LLM 2" width="100%">

10. 지식 검색 노드를 앞에 넣고 같은 질문을 하면 두 번 모두 을지로에 있는 장소를 답했습니다.

    <img src="etri-env-test-2026-11-18/14-dify-llm-knowledge-1.png" alt="지식과 LLM 1" width="100%">

    <img src="etri-env-test-2026-11-18/15-dify-llm-knowledge-2.png" alt="지식과 LLM 2" width="100%">

### 11-2. Azure Open WebUI + Ollama

처음 가입한 계정이 관리자가 되고, 모델 목록에 `qwen3-vl:8b` 가 바로 보입니다. 글 질의와 사진 질의(남산타워 사진)가 모두 답했습니다. Open WebUI 0.12.4, Ollama 0.40.13 입니다.

<img src="etri-env-test-2026-11-18/16-open-webui-start.png" alt="Open WebUI 첫 화면" width="100%">

<img src="etri-env-test-2026-11-18/17-open-webui-admin.png" alt="Open WebUI 관리자 계정 생성" width="100%">

<img src="etri-env-test-2026-11-18/18-open-webui-chat.png" alt="Open WebUI 질의" width="100%">

<img src="etri-env-test-2026-11-18/19-open-webui-image.png" alt="Open WebUI 이미지 질의" width="100%">

<img src="etri-env-test-2026-11-18/20-open-webui-about.png" alt="Open WebUI 버전 정보" width="100%">

### 11-3. GCP Langflow + vLLM

`admin` 과 배포 때 정한 비밀번호로 로그인하고, OpenAI Compatible 공급자에 GCP vLLM 주소와 키를 넣은 뒤, Chat Input → Language Model(`mistralai/Ministral-3-8B-Instruct-2512`) → Chat Output 흐름을 Playground 에서 돌렸습니다.

<img src="etri-env-test-2026-11-18/21-langflow-login.png" alt="Langflow 로그인" width="100%">

<img src="etri-env-test-2026-11-18/22-langflow-provider.png" alt="Langflow OpenAI Compatible 설정" width="100%">

<img src="etri-env-test-2026-11-18/23-langflow-flow.png" alt="Langflow 흐름" width="100%">

<img src="etri-env-test-2026-11-18/24-langflow-playground.png" alt="Langflow Playground 응답" width="100%">

## 12. 막혔을 때

먼저 로그를 봅니다. 노드마다 playbook 단계별 Semaphore 태스크 상태와 마지막 줄이 나옵니다. 실패 원인은 보통 마지막 단계의 마지막 몇 줄에 있습니다.

```sh
./06-logs.sh <배포 ID> 40
```

**부르는 API** (`06-logs.sh deploymentId [lines]`)

| 순서 | 호출 | 응답에서 읽는 것 |
| --- | --- | --- |
| 1 | `GET /deployments/{id}/logs` | `id`, `status`, `nodes[].{target, status, steps[].{playbook, taskId, status, taskUrl, error, output[]}}`. `output` 은 끝에서 `lines`(기본 20)줄만 찍음 |

같은 호출을 curl 로:

```sh
curl -s $AIAPP_URL/deployments/<배포 ID>/logs | jq '.nodes[] | {node: .target.nodeId, steps: [.steps[] | {playbook, status, taskUrl, tail: .output[-5:]}]}'
```

`taskUrl` 은 그 태스크의 Semaphore 화면으로, `/status` 의 `semaphore.uiUrl` 뒤에 `/project/<번호>/history?t=<태스크>` 를 붙인 주소입니다. 이번 환경은 `uiUrl` 이 `http://127.0.0.1:3001` 이라서 다른 PC 에서 열 때는 앞부분을 `http://<관리 호스트>:3001` 로 바꿉니다. Semaphore 계정은 `admin` 이고 비밀번호는 compose 기본값입니다.

| 증상 | 원인 | 할 일 |
| --- | --- | --- |
| `install-docker` 의 연결 대기에서 실패 | 관리 호스트에서 노드 22 번으로 못 닿음 | 5-4 의 SSH 점검이 되는지 다시 봄 |
| vLLM 노드가 `/health` 대기 중 실패 | 첫 가중치 다운로드가 길거나 vLLM 이 GPU 메모리 부족으로 죽음 | 로그 마지막 줄에서 `out of memory`, `KV cache`, `401`(접근 승인 필요 모델) 을 찾음. Hugging Face 가 `401`·`403` 이면 `HF_TOKEN=<토큰> ./03-deploy-inference.sh ...` 로 다시 배포 |
| `The GPU was requested but the runtime is not running on it` | 런타임이 CPU 로 뜸 | 근거 줄이 에러에 같이 나옴. 드라이버(`nvidia-smi`)와 docker 의 nvidia 런타임을 노드에서 확인 |
| Ollama `model-pull` 대기 시간 초과 | 노드의 바깥 통신이 느리거나 막힘 | 노드에서 `registry.ollama.ai` 로 나가는지 확인 |
| Dify 실패 `git is not installed on this node` | 노드에 `git` 없음 | 노드에 `git` 을 깔고 같은 명령을 다시 실행 |
| Langflow 실패 `python3 is not installed on this node` | 노드에 `python3` 없음 | 노드에 `python3` 을 깔고 다시 실행 |
| 배포는 `running` 인데 밖에서 안 열림 | 보안 그룹에 포트가 안 열림 | `./05-endpoints.sh` 의 `portsOpened`, `portsError` 를 봄 |
| 응용 안에서 모델 호출 실패 | 같은 노드 안에서 공인 IP 로 부르는 것이 막힘 | 응용 env 의 주소를 노드의 사설 IP(5-3 의 4번 응답 `privateIp`)로 바꿔 다시 배포 |

같은 명령을 다시 실행하면 같은 자리(노드·런타임)의 이전 배포를 바꿉니다. 받아 둔 가중치는 노드 볼륨에 남아서 다시 받지 않습니다.

## 13. 내리기와 dry-run 되돌리기

이번 시험에서는 배포한 응용을 더 살펴보려고 내리지 않았습니다. 내릴 때는 응용, 추론 순서로 내립니다. 배포 ID 를 잊었으면 목록에서 찾습니다.

```sh
curl -s "$AIAPP_URL/deployments?limit=1000" | jq -r '.deployments[] | select(.status == "running") | "\(.id) \(.type) \(.nodes[0].target.nodeId)"'
./07-remove.sh <배포 ID>      # 6건 각각
```

**부르는 API** (`07-remove.sh deploymentId`)

| 순서 | 호출 | 응답에서 읽는 것 |
| --- | --- | --- |
| 1 | `DELETE /deployments/{id}` | `status`. `202` 와 `removing` 이면 노드에서 `compose-down` 이 뒤에서 돎 |
| 2 | `GET /deployments/{id}/progress` (반복, 1이 `removing` 일 때만) | 4절 `wait_deployment` 의 필드. `removed` 또는 `remove-failed` 가 되면 끝 |
| 3 | `GET /deployments/{id}` | 4절 `summarize_deployment` 의 필드 |

| 코드 | 언제 |
| --- | --- |
| `200` | 노드에서 돌릴 것이 없음(이미 내림, dry-run 기록, 같은 자리에 더 나중 배포가 있음) |
| `202` | 접수. `status: removing` |
| `409` | 아직 `pending`·`deploying` 이거나 이미 내리는 중 |

- 노드에서 `docker compose -p aiapp-<작업 키> down --remove-orphans` 를 합니다. 가중치 볼륨과 노드 폴더 `/opt/aiapp/<작업 키>` 는 남습니다.
- 연 포트는 닫지 않습니다. VM 자체도 그대로입니다. VM 을 지우는 것은 CB-Tumblebug 쪽 작업입니다.

AI-App 을 다시 dry-run 으로 돌립니다.

```sh
cd ~/ai-app
sed -i 's/^AIAPP_TUMBLEBUG_DRY_RUN=false$/AIAPP_TUMBLEBUG_DRY_RUN=true/' .env
docker compose up -d ai-app-server
for i in $(seq 1 30); do curl -sf localhost:8090/aiapp/readyz >/dev/null && break; sleep 2; done
curl -s localhost:8090/aiapp/status | jq .dryRun     # true
```
