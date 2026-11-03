# ETRI: GPU VM 준비와 AI-App 배포

> 대상: CB-Tumblebug 으로 GPU VM 을 만들고 그 위에 추론 서비스와 응용을 올려 Travel Planner 포털에 붙이는 ETRI 개발자.
> 공통 내용(인증, 오류, 상태, 포트)은 [README.md](README.md) 에 있습니다.

## 1. 나누는 일

| 단계 | 누가 | 무엇으로 |
|---|---|---|
| GPU VM 생성 | ETRI | CB-Tumblebug API (AI-App 은 VM 을 만들지 않음) |
| 추론 서비스·응용 배포 | AI-App (호출은 ETRI 또는 AI-Web) | `POST /aiapp/deployments` |
| 서빙 포트 열기 | AI-App | 배포가 끝나면 노드 보안 그룹에 규칙 추가 |
| 주소 확인 | 포털 | `GET /aiapp/endpoints` |
| VM 정지·재개·종료 | AI-App 또는 CB-Tumblebug | `POST /aiapp/deployments/{id}/control` 또는 CB-Tumblebug 직접 |

## 2. VM 이 갖춰야 할 것

AI-App 은 CB-Tumblebug 의 노드 정보(`GET /ns/{nsId}/infra/{infraId}/node/{nodeId}`)를 읽어 접속합니다. 그래서 아래가 맞아야 합니다.

| 항목 | 조건 | 안 맞으면 |
|---|---|---|
| OS | **Ubuntu**. `install-docker` 가 `/etc/os-release` 를 보고 Ubuntu 가 아니면 멈춤 | 노드 `failed`, 로그에 `install-docker supports Ubuntu only` |
| docker | 없으면 AI-App 이 Docker 저장소에서 깖. 이미 있는데 compose 플러그인(`docker compose`)이 없으면 노드의 apt 저장소에 있는 `docker-compose-plugin` 이나 `moby-compose`(Azure 이미지)를 깖 | 둘 다 없으면 `failed`, 로그에 `no apt source offers docker-compose-plugin or moby-compose` |
| SSH 키 | 노드의 `sshKeyId` 가 가리키는 CB-Tumblebug SSH 키에 **개인키(`privateKey`)가 들어 있어야** 함. CB-Tumblebug 이 키를 만들게 하면 들어 있음. PEM 개인키 블록이어야 함 | 노드 `failed` (키를 읽거나 검사하는 단계에서) |
| SSH 사용자 | 노드 정보의 사용자(보통 `cb-user`)가 비밀번호 없이 `sudo` 가능. playbook 이 `become` 으로 돎 | playbook 실패 |
| SSH 접속 | Semaphore 가 돌고 있는 호스트에서 노드의 공인 IP(없으면 사설 IP)와 SSH 포트로 닿아야 함 | 태스크 `error` (unreachable) |
| 보안 그룹 | 노드 정보에 `securityGroupIds` 가 있어야 AI-App 이 서빙 포트를 엶. 없으면 포트를 열지 않고 `notes` 에 남김 | 배포는 `succeeded`, 밖에서 안 닿음 |
| GPU | 노드 스펙(`specId`, 네임스페이스 `system`)의 `acceleratorType` 이 `gpu` 면 GPU 노드로 봄. **NVIDIA 드라이버는 이미지에 들어 있어야** 하고 **550 이상**이어야 함(Ollama `0.40.13` 이 요구). AI-App 은 NVIDIA Container Toolkit 을 깔고, docker 에 nvidia 런타임이 없으면 등록함 | 스펙을 못 읽으면 GPU 없이 배포(`gpuSource: default`). 드라이버가 없거나 550 미만이면 런타임이 CPU 로 돌고, GPU 를 쓰기로 한 배포는 그 노드가 **`failed`** 가 됨(아래 3-2절) |
| 바깥 통신 | 노드에서 Docker Hub, `ghcr.io`, Hugging Face, `registry.ollama.ai`, GitHub(Dify), `nvidia.github.io` 로 나갈 수 있어야 함 | 이미지·모델 받기 실패 |
| 디스크 | 런타임 이미지와 모델 가중치가 들어갈 만큼. 모델 크기는 `GET /aiapp/models` 의 `sizeGiB` (모르면 비어 있음) | 받는 중 실패 |
| git | Dify 를 올리는 노드(vm1)는 `git` 이 있어야 함. Dify 공식 compose 를 노드에서 받아 띄움 | Dify 배포 실패 |

GPU 사용을 직접 정하려면 배포 요청에 `"gpu": true` 를 넣습니다. 그러면 스펙을 보지 않습니다.

### 2-1. 실제로 써 본 이미지 (2026-11-03)

CB-Tumblebug 0.13.8 의 `POST /ns/system/resources/searchImage` 에 `"isGPUImage": true` 를 넣어 고른 이미지입니다.

| CSP·리전 | 스펙 | 이미지 | 드라이버 | 결과 |
|---|---|---|---|---|
| aws ap-northeast-1 | `g4dn.xlarge` (T4) | `ami-0e8e3e0f677c0f395` Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 22.04) | 595 | docker·compose·nvidia 런타임이 다 들어 있음. vLLM + Qwen3-Embedding 동작 |
| azure koreacentral | `Standard_NC4as_T4_v3` (T4) | `microsoft-dsvm:ubuntu-hpc:2204:22.04.2026100101` | 580 | compose 플러그인이 없어 `moby-compose` 를 깖. Ollama 가 GPU 사용(초당 약 43 토큰) |
| azure koreacentral | `Standard_NC4as_T4_v3` | `microsoft-dsvm:ubuntu-2204:2204-gen2:25.06.18` | 535 | **쓰지 말 것.** 드라이버가 오래돼 Ollama 가 CPU 로 돎(초당 약 5 토큰) |

VM 이 Running 이 된 직후에는 cloud-init 이 접속 사용자를 아직 만들지 않았을 수 있습니다. `install-docker` 가 접속될 때까지 최대 10분 기다립니다.

## 3. 배포 (vm1, vm2, vm3)

아래 예시는 `nsId: etri`, `infraId: travel`, 노드 `vm1`·`vm2`·`vm3` 입니다. 실제 ID 로 바꿉니다.

```sh
export AIAPP_URL=http://<AI-App 호스트>:8090/aiapp
export AIAPP_AUTH_TOKEN=<받은 토큰>
AUTH="Authorization: Bearer $AIAPP_AUTH_TOKEN"
```

| 노드 | 추론 서비스 (`inference`) | 응용 (`app`) | 응용에 넘길 env |
|---|---|---|---|
| vm1 | `vllm` + `qwen3-embedding-0.6b` / `hf-bf16` (포트 8000) | `dify` (포트 80) | `VLLM_BASE_URL=http://<vm1>:8000/v1` |
| vm2 | `ollama` + `qwen3-vl-8b` / `ollama-gguf` (포트 11434) | `open-webui` (포트 3000) | `OLLAMA_BASE_URL=http://<vm2>:11434` (`/v1` 없이) |
| vm3 | `vllm` + `ministral-3-8b` / `hf-fp8` (포트 8000) | `langflow` (포트 7860) | `MODEL_BASE_URL=http://<vm3>:8000/v1` |

### 3-1. 응용 올리기 (한 번만)

응용 셋은 저장소의 `examples/apps/` 에 있습니다. 이미 올라가 있으면 건너뜁니다(`GET /aiapp/apps`).
`ports` 는 이 응용이 노드에서 여는 포트이고, 배포 때 AI-App 이 보안 그룹에 엽니다.

```sh
curl -sS -H "$AUTH" -F name=dify -F script=@examples/apps/dify/deploy.sh -F ports=80 \
  "$AIAPP_URL/apps"
curl -sS -H "$AUTH" -F name=open-webui -F script=@examples/apps/open-webui/deploy.sh \
  -F files=@examples/apps/open-webui/docker-compose.yml -F ports=3000 "$AIAPP_URL/apps"
curl -sS -H "$AUTH" -F name=langflow -F script=@examples/apps/langflow/deploy.sh \
  -F files=@examples/apps/langflow/docker-compose.yml -F ports=7860 "$AIAPP_URL/apps"
```

응답은 등록된 버전입니다. 같은 이름으로 다시 올리면 `version` 이 하나 올라갑니다.

```json
{"version": 1, "script": "...", "files": [{"name": "docker-compose.yml", "sizeBytes": 756, "sha256": "..."}],
 "ports": [3000], "createdAt": "2026-11-03T04:39:01Z"}
```

### 3-2. 추론 서비스 배포

```sh
# vm1: vLLM + Qwen3-Embedding-0.6B
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments" -d '{
  "type": "inference",
  "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"}],
  "inference": {"modelId": "qwen3-embedding-0.6b", "artifactId": "hf-bf16", "runtimeId": "vllm"}
}'

# vm2: Ollama + Qwen3-VL-8B
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments" -d '{
  "type": "inference",
  "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm2"}],
  "inference": {"modelId": "qwen3-vl-8b", "artifactId": "ollama-gguf", "runtimeId": "ollama"}
}'

# vm3: vLLM + Ministral 3 8B
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments" -d '{
  "type": "inference",
  "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm3"}],
  "inference": {"modelId": "ministral-3-8b", "artifactId": "hf-fp8", "runtimeId": "vllm", "gpu": true}
}'
```

- 받으면 `202` 와 `{"id": "...", "status": "pending", ...}` 입니다. 그 `id` 로 `GET /aiapp/deployments/{id}` 를 `status` 가
  `pending`·`running` 이 아닐 때까지 읽습니다. `succeeded` 면 됐습니다.
- 접근 승인이 필요한 Hugging Face 모델이면 `"env": {"HF_TOKEN": "<토큰>"}` 을 `inference` 안에 넣습니다. 이 값은 배포 기록과
  노드의 `.env` 에 평문으로 남습니다.
- vm3 처럼 `"gpu": true` 를 주면 노드 스펙을 보지 않고 GPU 를 씁니다. 빼면 스펙의 `acceleratorType` 으로 정합니다(2절).
- GPU 를 쓰기로 한 노드는 `compose-up` 이 헬스 응답 뒤에 런타임이 정말 GPU 위에 있는지 봅니다. 아니면 노드가 `failed` 이고
  `error` 와 로그에 "The GPU was requested but the runtime is not running on it" 과 근거 줄이 남습니다. 성공하면 근거 줄이
  `nodes[].gpuCheck` 에 남습니다.

  | 런타임 | 보는 것 | GPU 위일 때 근거 줄 예 |
  |---|---|---|
  | vLLM | 호스트의 `nvidia-smi --query-compute-apps=pid,process_name,used_memory` 에 그 compose 프로젝트 컨테이너의 프로세스가 있는지 | `nvidia-smi: 3499, VLLM::EngineCore, 13524 MiB` |
  | Ollama | `engine` 서비스 로그의 `msg="inference compute"` 줄이 `library=CUDA`(또는 `cuda`·`ROCm`·`rocm`) 인지. Ollama 는 모델을 올리기 전에는 GPU 프로세스가 없어 `nvidia-smi` 로는 못 봄 | `log: ... msg="inference compute" id=0 library=CUDA compute=7.5 ... description="Tesla T4" total="15.6 GiB"` |

  Ollama 가 GPU 를 못 찾으면 로그가 `msg="inference compute" id=cpu library=cpu compute="" name=cpu description=cpu ...` 이고
  실패 메시지에 이 줄이 찍힙니다. 엔진이 헬스 응답보다 조금 늦게 GPU 를 잡을 수 있어 5초 간격으로 6번까지 봅니다.
- vm3 의 Ministral 은 `args` 를 주지 않아도 카탈로그 아티팩트의 `runtimes[].args`(`--max-model-len 32768` 과 mistral 형식 플래그)가
  `vllm serve` 뒤에 붙습니다. aws g6.xlarge(L4 24GB)에서 이 인자 없이 띄웠더니 기본 최대 길이 262144 에 KV 캐시 34.0 GiB 가 필요한데 9.31 GiB 뿐이라는
  오류(`estimated maximum model length is 71760`)로 vLLM 이 뜨지 않았습니다. 다른 값을 쓰려면 `"args": ["--max-model-len", "16384"]` 처럼
  요청에 주면 카탈로그 인자 뒤에 붙어 덮어씁니다.
- 세 노드를 한 요청에 넣을 수도 있지만(`targets` 여러 개), 모델이 노드마다 달라 위처럼 따로 보냅니다.

```sh
id=<위 응답의 id>
for i in $(seq 1 90); do
  status=$(curl -sS -H "$AUTH" "$AIAPP_URL/deployments/$id" | jq -r .status)
  echo "$i $status"
  case "$status" in pending|running) sleep 10 ;; *) break ;; esac
done
```

### 3-3. 추론 서비스 주소 읽기

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/endpoints?nsId=etri&infraId=travel&type=inference"
```

```json
{
  "endpoints": [
    {
      "nsId": "etri", "infraId": "travel", "nodeId": "vm1",
      "csp": "aws", "region": "ap-northeast-2", "host": "203.0.113.10",
      "type": "inference", "workKey": "ms-vllm",
      "deploymentId": "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48",
      "runtimeId": "vllm", "modelId": "qwen3-embedding-0.6b", "artifactId": "hf-bf16",
      "url": "http://203.0.113.10:8000/v1", "healthUrl": "http://203.0.113.10:8000/health",
      "ports": [8000], "portsOpened": true,
      "openedPorts": [{"securityGroupId": "etri-shared-aws-ap-northeast-2", "port": 8000, "protocol": "TCP", "result": "added"}]
    }
  ]
}
```

(값은 Swagger 의 예시를 모은 것입니다. 노드 셋이면 항목이 셋입니다.)

### 3-4. 응용 배포

`env` 에 3-3 의 주소를 넣습니다. Open WebUI 는 Ollama 의 루트 주소를 받으므로 `url` 에서 `/v1` 을 뺍니다.

```sh
# vm1: Dify
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments" -d '{
  "type": "app",
  "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1"}],
  "app": {"name": "dify", "env": {"VLLM_BASE_URL": "http://203.0.113.10:8000/v1"}}
}'

# vm2: Open WebUI
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments" -d '{
  "type": "app",
  "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm2"}],
  "app": {"name": "open-webui", "env": {"OLLAMA_BASE_URL": "http://203.0.113.12:11434"}}
}'

# vm3: Langflow
curl -sS -H "$AUTH" -H 'Content-Type: application/json' "$AIAPP_URL/deployments" -d '{
  "type": "app",
  "targets": [{"kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm3"}],
  "app": {"name": "langflow", "env": {"MODEL_BASE_URL": "http://203.0.113.13:8000/v1"}}
}'
```

| 응용 | env | 끝난 뒤 |
|---|---|---|
| Dify | `VLLM_BASE_URL` (화면에 넣을 값을 로그에 찍기만 함), `DIFY_PORT`(기본 80) | `http://<vm1>/install` 에서 관리자 계정을 만들고, 설정 > Model Provider > OpenAI-API-compatible 에 `VLLM_BASE_URL` 을 넣음 |
| Open WebUI | `OLLAMA_BASE_URL` (필수), `OPEN_WEBUI_PORT`(기본 3000) | `http://<vm2>:3000` |
| Langflow | `MODEL_BASE_URL`, `LANGFLOW_PORT`(기본 7860) | `http://<vm3>:7860`. 모델 컴포넌트의 base URL 에서 전역 변수 `MODEL_BASE_URL` 을 고름 |

포트를 기본값과 다르게 쓰면(`OPEN_WEBUI_PORT=8080` 처럼) 열 포트도 같이 바꿔야 합니다: `"app": {..., "ports": [8080]}`.

## 4. 포털이 주소를 찾는 법

포털은 `GET /aiapp/endpoints` 하나로 지금 떠 있는 것을 다 읽습니다.

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/endpoints?nsId=etri&infraId=travel"
```

| 필드 | 쓰는 곳 |
|---|---|
| `nodeId`, `workKey` | 어느 노드의 무엇인지. 노드 하나·작업 키 하나에 항목 하나 |
| `type: inference` 의 `url` | OpenAI 호환 API (`/v1/chat/completions`, `/v1/embeddings`) |
| `type: app` 의 `urls[].url` | 응용 화면 (`http://<노드>:<포트>`) |
| `portsOpened` | `false` 면 보안 그룹에 포트가 안 열렸을 수 있음. `portsError`·`notes` 를 봄 |
| `deploymentId` | 이 주소를 만든 배포. 내릴 때 씀 |

- 다시 배포하면 항목의 `deploymentId` 와 주소가 새 것으로 바뀝니다. 내리면 항목이 빠집니다.
- 배포 중이거나 실패한 것, dry-run 은 나오지 않습니다.
- 노드 하나만 볼 때는 `?nsId=etri&infraId=travel&nodeId=vm2` 처럼 거릅니다.

## 5. 로그

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/deployments/$id/logs"
```

노드마다 playbook 단계(`install-docker.yml`, `compose-up.yml` 또는 `copy-and-run.yml`)의 Semaphore 태스크 상태와 출력 줄이 옵니다.
AI-App 에 `AIAPP_SEMAPHORE_UI_URL` 이 설정돼 있으면 단계마다 `taskUrl`(Semaphore 화면의 그 태스크)도 붙습니다.
실패 원인은 보통 마지막 단계의 마지막 몇 줄에 있습니다. `docs/api/examples/06-logs.sh <id>` 가 단계별 마지막 20줄을 찍습니다.

## 6. 내리기

```sh
curl -sS -H "$AUTH" -X DELETE "$AIAPP_URL/deployments/$id"
```

- 노드에서 `docker compose down` 을 합니다. 받아 둔 모델 가중치(named volume)와 노드 폴더 `/opt/aiapp/<작업 키>` 는 남아서, 다음에
  같은 것을 배포하면 다시 받지 않습니다.
- 응답은 `202`·`removing` 이고, `removed` 가 될 때까지 읽습니다. 이미 다른 배포가 같은 자리를 바꿨으면 그 노드는 건드리지 않고
  `removed` 로 표시하며 `notes` 에 그 배포를 적습니다.
- 연 포트는 닫지 않습니다. VM 자체를 지우려면 CB-Tumblebug 에서 지우거나 `POST /aiapp/deployments/{id}/control` 에 `{"action":"terminate"}` 를 보냅니다.

## 7. 열린 문제

| 문제 | 상태 |
|---|---|
| vm3 의 Ministral 3 8B 는 FP8 가중치(`hf-fp8`)입니다 | FP8 을 지원하는 GPU 가 필요할 수 있습니다. 어느 GPU 에서 vLLM `v0.32.0` 이 이 모델을 띄우는지 **확인하지 않았습니다**. GPU 기종이 정해지면 먼저 한 번 띄워 봐야 합니다 |
| vm2 의 Qwen3-VL 은 이미지를 만들지 못합니다 | 이미지를 읽고 글을 쓰는 모델입니다. 이미지 생성이 필요하면 다른 모델과 런타임이 필요하고 지금 카탈로그에 없습니다 |
| vm1 에 글을 생성하는 LLM 이 없습니다 | Qwen3-Embedding 은 검색용 벡터만 만듭니다. Dify 로 답을 생성하려면 생성 모델이 있어야 합니다. vm1 에 생성 모델을 더 올리려면 런타임이 달라야 합니다(같은 노드에 `vllm` 은 하나) |
| 같은 노드 안에서 응용이 추론 서비스를 공인 IP 로 부릅니다 | `endpoints` 의 주소는 공인 IP 입니다. aws·azure 에서 응용 배포는 됐지만, 응용 안에서 그 주소로 모델을 실제로 불러 보지는 않았습니다. 안 되면 노드의 사설 IP 를 env 에 넣습니다 |
| 서빙 포트를 `0.0.0.0/0` 에 엽니다 | 시험 환경 기준입니다. 출처 제한은 정하지 않았습니다 |
| 실제 클라우드 VM 에서의 배포 | 자세한 결과는 [live-test-2026-11-03.md](live-test-2026-11-03.md). 2026-11-03 aws(도쿄 T4·L4)·azure(koreacentral T4) 에서 확인: vLLM 임베딩, Ollama 대화, Dify·Open WebUI 화면이 밖에서 응답, 보안 그룹 포트 열림, 주소 조회, 내리기. gcp 는 자격증명 권한(`compute.machineTypes.list` 403) 때문에 못 함. docker 를 처음부터 까는 분기(이미지에 docker 가 없는 경우)는 아직 실제로 돌지 않았습니다 |
| Qwen3-VL-8B 의 생각(thinking) 단계 | Ollama 의 `qwen3-vl:8b` 는 답 전에 생각을 길게 씁니다. `"think": false` 를 줘도 생각이 `message.thinking` 으로 나왔고, 1,500 토큰에서도 답(`content`)이 비어 있었습니다. 포털이 짧은 답을 바라면 시간 제한과 토큰 수를 넉넉히 두거나 모델을 바꿔야 합니다 |
| 응용 배포의 성공 시점 | 응용은 script 가 끝나면 `succeeded` 입니다. 서비스가 실제로 응답할 때까지는 기다리지 않아서, Open WebUI 는 성공 직후 잠깐 응답하지 않았습니다(약 20초 뒤 200) |
