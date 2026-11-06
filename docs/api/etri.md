# ETRI: GPU VM 준비와 AI-App 배포

> 대상: CB-Tumblebug 으로 GPU VM 을 만들고 그 위에 추론 서비스와 응용을 올려 Travel Planner 포털에 붙이는 ETRI 개발자.
> 공통 내용(인증, 오류, 상태, 포트)은 [README.md](README.md) 에 있습니다.
> API 가 어디로 열려 있는지는 [README.md 1-1절](README.md#1-1-api-는-어디로-열려-있나), Semaphore 는 [3-1절](README.md#3-1-semaphore-가-무엇인가),
> 예시 응용 셋의 동작과 실제 화면은 [12절](README.md#12-예시-응용-셋은-어떻게-동작하나) 에 있습니다.

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
| OS | 아래 "지원하는 배포판" 표의 배포판. `install-docker` 가 `/etc/os-release` 의 `ID` 와 `ID_LIKE` 를 보고 설치 방법을 고름 | 그 밖의 배포판(Amazon Linux 2 포함)이면 노드 `failed`, 로그에 `install-docker supports only ubuntu, debian, rhel, rocky, almalinux, centos, ol, fedora, amzn (Amazon Linux 2023), found <ID> <VERSION_ID>` |
| docker | 없으면 AI-App 이 아래 표대로 깔고 docker 서비스를 켬. 이미 있는데 compose 플러그인(`docker compose`)이 없으면 노드의 저장소에 있는 `docker-compose-plugin` 이나 `moby-compose`(Azure 이미지)를 깖. dnf 를 쓰는 노드는 둘 다 없으면 compose 플러그인을 GitHub 에서 직접 받아 깖 | apt 를 쓰는 노드에서 둘 다 없으면 `failed`, 로그에 `no apt source offers docker-compose-plugin or moby-compose` |
| SSH 키 | 노드의 `sshKeyId` 가 가리키는 CB-Tumblebug SSH 키에 **개인키(`privateKey`)가 들어 있어야** 함. CB-Tumblebug 이 키를 만들게 하면 들어 있음. PEM 개인키 블록이어야 함 | 노드 `failed` (키를 읽거나 검사하는 단계에서) |
| SSH 사용자 | 노드 정보의 사용자(보통 `cb-user`)가 비밀번호 없이 `sudo` 가능. playbook 이 `become` 으로 돎. 노드에 `cb-user` 가 없으면(아래) AI-App 이 이미지 기본 사용자를 찾아 씀. 배포 요청의 `targets[].sshUser` 로 직접 정할 수도 있음 | playbook 실패 (접속되는 사용자가 없으면 접속 단계에서 `error`) |
| SSH 접속 | Semaphore 가 돌고 있는 호스트에서 노드의 공인 IP(없으면 사설 IP)와 SSH 포트로 닿아야 함 | 태스크 `error` (unreachable) |
| 보안 그룹 | 노드 정보에 `securityGroupIds` 가 있어야 AI-App 이 서빙 포트를 엶. 없으면 포트를 열지 않고 `notes` 에 남김 | 배포는 `running`, 밖에서 안 닿음 |
| GPU | 노드 스펙(`specId`, 네임스페이스 `system`)의 `acceleratorType` 이 `gpu` 면 GPU 노드로 봄. **NVIDIA 드라이버는 이미지에 들어 있어야** 하고 **550 이상**이어야 함(Ollama `0.40.13` 이 요구). AI-App 은 NVIDIA Container Toolkit 을 깔고, docker 에 nvidia 런타임이 없으면 등록함 | 스펙을 못 읽으면 GPU 없이 배포(`gpuSource: default`). 드라이버가 없거나 550 미만이면 런타임이 CPU 로 돌고, GPU 를 쓰기로 한 배포는 그 노드가 **`failed`** 가 됨(아래 3-2절) |
| 바깥 통신 | 노드에서 Docker Hub, `ghcr.io`, Hugging Face, `registry.ollama.ai`, `download.docker.com`, GitHub(Dify, 직접 받는 compose 플러그인), `nvidia.github.io` 로 나갈 수 있어야 함 | docker 설치, 이미지·모델 받기 실패 |
| 디스크 | 런타임 이미지와 모델 가중치가 들어갈 만큼. 모델 크기는 `GET /aiapp/models` 의 `sizeGiB` (모르면 비어 있음) | 받는 중 실패 |
| 응용이 쓰는 도구 | `install-docker` 는 docker 와 compose 플러그인만 깖. 예시 응용의 `deploy.sh` 가 쓰는 도구는 이미지에 있어야 함. Dify(vm1)는 `git` 이 꼭 있어야 함(Dify 공식 compose 를 노드에서 받아 띄움). `openssl` 은 있으면 비밀값을 만드는 데 쓰고 없으면 `/dev/urandom` 으로 대신함. Langflow 는 `MODEL_BASE_URL` 이나 `MODEL_API_KEY` 를 줄 때 `python3` 이 있어야 함(그 값을 Langflow 에 넣는 API 호출). Open WebUI 는 더 필요한 것이 없음 | `git` 이 없으면 Dify 배포 실패(`git is not installed on this node`). `python3` 이 없으면 Langflow 는 뜨지만 배포 실패(`python3 is not installed on this node`) |

지원하는 배포판과 `install-docker` 가 까는 방법입니다. `ID_LIKE` 가 이 중 하나를 가리키는 파생 배포판도 같은 방법으로 다룹니다.

| 배포판 (`ID`) | docker 와 compose 플러그인 | NVIDIA Container Toolkit (GPU 노드) |
|---|---|---|
| Ubuntu (`ubuntu`), Debian (`debian`) | Docker 의 apt 저장소(`download.docker.com/linux/ubuntu` 또는 `/linux/debian`, 배포판 코드명) | NVIDIA 의 apt 저장소 |
| RHEL (`rhel`), Rocky (`rocky`), AlmaLinux (`almalinux`), CentOS Stream (`centos`), Oracle Linux (`ol`), Fedora (`fedora`) | Docker 의 rpm 저장소(RHEL 은 `/linux/rhel`, Fedora 는 `/linux/fedora`, 나머지는 `/linux/centos`). docker 와 부딪히는 패키지(podman, runc)가 있으면 Docker 설치 안내대로 dnf 가 지우고 docker 쪽 패키지로 바꿈 | NVIDIA 의 rpm 저장소 |
| Amazon Linux 2023 (`amzn`) | 배포판 저장소의 `docker`. 이 저장소에는 compose 플러그인이 없어 Docker 문서의 수동 설치대로 GitHub 의 compose 릴리스 바이너리(playbook 에 고정한 버전, 노드 아키텍처 x86_64 또는 aarch64)를 받아 릴리스의 sha256 과 맞춰 본 뒤 `/usr/local/lib/docker/cli-plugins/docker-compose` 에 둠 | NVIDIA 의 rpm 저장소 |

GPU 사용을 직접 정하려면 배포 요청에 `"gpu": true` 를 넣습니다. 그러면 스펙을 보지 않습니다.

아래 이미지로 VM 을 만들어 AI-App 배포까지 확인했습니다.

| CSP | 이미지 | 스펙 | `install-docker` | 배포 | 계정 |
|---|---|---|---|---|---|
| aws | RHEL 9.8 (`ami-0938769ffbe276d3b`) | t3.medium | Docker rpm 저장소로 docker 29.9.3, compose 5.7.0 | Open WebUI `running`, 밖에서 HTTP 200 | `sshUser` 없이 자동으로 `ec2-user` (`sshUserSource: fallback`) |
| aws | Amazon Linux 2023 (`ami-0c051224e18c11f2e`) | t3.medium | 배포판 `docker` 25.0.18, compose 5.7.0 (GitHub, sha256 확인) | Open WebUI `running`, HTTP 200 | `sshUser: ec2-user` |
| aws | Deep Learning Base OSS Nvidia Driver GPU AMI (Amazon Linux 2023, `ami-0f328602208fc4f93`) | g4dn.xlarge (T4) | compose 5.7.0 추가, nvidia 런타임은 이미지에 있음 | Ollama + Qwen3-VL-8B `running`, GPU 확인 `library=CUDA` Tesla T4 | `sshUser: ec2-user` |
| azure | AlmaLinux 9 (`almalinux:almalinux-x86_64:9-gen2`) | Standard_B2s | docker 가 없는 상태에서 Docker rpm 저장소로 설치 | Open WebUI `running`, HTTP 200 | `cb-user` (지정 없음) |

CB-Tumblebug(cb-spider)은 aws 에 VM 을 만들 때 로그인 사용자 `cb-user` 를 `useradd -G sudo` 로 만듭니다. `sudo` 그룹이 없는 이미지(RHEL,
Rocky, AlmaLinux, Amazon Linux 는 `wheel` 을 씀)에서는 이것이 실패해, 노드 정보에는 `cb-user` 가 있는데 노드에는 그 사용자가 없습니다.
azure 는 VM 을 만들 때 Azure 가 관리자 사용자로 `cb-user` 를 만들어 이 문제가 없습니다(AlmaLinux 9 에서 확인).
aws 에서는 이미지의 기본 사용자(`ec2-user`)가 같은 키로 접속됩니다. AI-App 은 playbook 을 돌리기 전에 CB-Tumblebug 의 키로
`cb-user` 의 SSH 인증을 해 보고, 노드가 거절하면 이미지 OS 에 맞는 기본 사용자(RHEL·Amazon Linux 는 `ec2-user`)로 바꿔 씁니다.
그래서 배포 요청에 따로 줄 것이 없습니다. 이때 `nodes[].sshUserSource` 가 `fallback` 이고 `notes` 에 거절된 사용자와 쓴 사용자가 남습니다.
사용자를 직접 정하려면 대상에 `"sshUser": "ec2-user"` 처럼 주며, 그러면 인증을 시도하지 않습니다. 키는 어느 쪽이든 CB-Tumblebug 이
가진 키를 쓰고, 실제로 접속한 사용자는 `nodes[].sshUser` 에 남으며 같은 배포를 내릴 때도 그 사용자로 접속합니다([README](README.md) 10절).

azure 마켓플레이스에서 플랜(구매 조건)이 붙은 이미지(예: CIQ 의 Rocky Linux 이미지)는 지금 CB-Tumblebug 으로 만들 수 없습니다.
VM 생성이 `VMMarketplaceInvalidInput: ... requires Plan information in the request` 로 실패합니다. 플랜이 없는 이미지(AlmaLinux, RHEL 등)를 씁니다.

gcp 에서 GPU VM 을 만들 때 알아 둘 것이 둘 있습니다(vm3, `g2-standard-4` L4 로 확인).

- 존마다 GPU 재고가 다릅니다. `us-west1-a`·`us-west1-b` 는 `does not have enough resources available ... (state:STOCKOUT)` 로 실패했고
  `us-west1-c` 에서 만들어졌습니다. 재고가 없으면 같은 리전의 다른 존으로 다시 요청합니다.
- 다른 존으로 다시 요청할 때는 **노드 그룹 이름을 바꿉니다.** CB-Tumblebug 은 노드 그룹 이름으로 보안 그룹(`<infra>-<그룹>`)을 만들고,
  같은 이름이 이미 있으면 그것을 다시 씁니다. 처음 존의 vNet 에 만들어진 보안 그룹이 다른 존 vNet 의 VM 에는 적용되지 않아(GCP 방화벽은
  네트워크 단위) SSH(22)가 막힙니다. AI-App 의 배포는 `install-docker` 의 연결 대기에서 실패합니다.

### 2-1. 실제로 써 본 이미지

CB-Tumblebug 0.13.8 의 `POST /ns/system/resources/searchImage` 에 `"isGPUImage": true` 를 넣어 고른 이미지입니다.

| CSP·리전 | 스펙 | 이미지 | 드라이버 | 결과 |
|---|---|---|---|---|
| aws ap-northeast-1 | `g4dn.xlarge` (T4) | `ami-0e8e3e0f677c0f395` Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 22.04) | 595 | docker·compose·nvidia 런타임이 다 들어 있음. vLLM + Qwen3-Embedding 동작 |
| azure koreacentral | `Standard_NC4as_T4_v3` (T4) | `microsoft-dsvm:ubuntu-hpc:2204:22.04.2026100101` | 580 | compose 플러그인이 없어 `moby-compose` 를 깖. Ollama 가 GPU 사용(초당 약 43 토큰) |
| gcp us-west1 (`us-west1-c`) | `g2-standard-4` (L4) | `ubuntu-accelerator-2204-amd64-with-nvidia-595-v20261104a` (Canonical, Ubuntu 22.04). 시험한 VM 은 `-v20261021` 로 떴고, 그 이미지는 시험 뒤에 deprecated 되어 이것으로 대체됨 | 595 | docker 가 없어 AI-App 이 Docker apt 저장소로 설치(29.9.3, compose 5.7.0). vLLM + Ministral 3 8B 가 GPU 사용 |
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
| vm1 | `vllm` + `qwen3-embedding-0.6b` / `hf-bf16` (포트 8000) | `dify` (포트 80) | `VLLM_BASE_URL=http://<vm1>:8000/v1` (API 키는 Dify 화면에 넣음) |
| vm2 | `ollama` + `qwen3-vl-8b` / `ollama-gguf` (포트 11434) | `open-webui` (포트 3000) | `OLLAMA_BASE_URL=http://<vm2>:11434` (`/v1` 없이) |
| vm3 | `vllm` + `ministral-3-8b` / `hf-fp8` (포트 8000) | `langflow` (포트 7860) | `MODEL_BASE_URL=http://<vm3>:8000/v1`, `MODEL_API_KEY=<vm3 의 apiKey>` |

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

- 받으면 `202` 와 `{"id": "...", "status": "pending", ...}` 입니다. 그 `id` 로 `GET /aiapp/deployments/{id}/progress` 를 `done` 이
  `true` 가 될 때까지 읽습니다. `running` 이 `true`(`status: running`)면 됐습니다. 도는 동안 노드마다 `phase`(지금 하는 일)와
  `step.currentTask`(Ansible 태스크 이름)가 보입니다. 필드는 [README 7-1절](README.md) 입니다.
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
  `vllm serve` 뒤에 붙습니다. L4(24GB) 노드에서 이 인자 없이 띄웠더니 기본 최대 길이 262144 에 KV 캐시 34.0 GiB 가 필요한데 9.31 GiB 뿐이라는
  오류(`estimated maximum model length is 71760`)로 vLLM 이 뜨지 않았습니다. 다른 값을 쓰려면 `"args": ["--max-model-len", "16384"]` 처럼
  요청에 주면 카탈로그 인자 뒤에 붙어 덮어씁니다.
- 세 노드를 한 요청에 넣을 수도 있지만(`targets` 여러 개), 모델이 노드마다 달라 위처럼 따로 보냅니다.
- vm1·vm3 의 vLLM 은 API 키를 요구합니다. 위처럼 `apiKey` 를 빼면 AI-App 이 노드마다 키를 만들고, 같은 노드에 다시 배포하면 그 키를
  그대로 씁니다. 정한 키를 쓰려면 `"inference": {..., "apiKey": "<16~128자 [A-Za-z0-9._~-]>"}` 를 줍니다. 키는 3-3 의 `apiKey` 로
  읽습니다. vLLM 을 부를 때는 `Authorization: Bearer <apiKey>` 를 붙이고, 없으면 `/v1/*` 가 `401` 입니다. 헬스(`/health`)는 키 없이
  응답하므로 배포 확인은 키와 상관없습니다. vm2 의 Ollama 는 키 기능이 없어 `apiKey` 를 주면 `400` 입니다.

```sh
id=<위 응답의 id>
for i in $(seq 1 90); do
  p=$(curl -sS -H "$AUTH" "$AIAPP_URL/deployments/$id/progress")
  echo "$i $(echo "$p" | jq -r '[.status, (.nodes[] | "\(.target.nodeId):\(.phase) \(.step.currentTask // "")")] | join(" ")')"
  [ "$(echo "$p" | jq -r .done)" = true ] && break
  sleep 10
done
echo "$p" | jq '{status, running}'
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
      "csp": "aws", "region": "ap-northeast-2",
      "nodeStatus": "Running", "available": true, "host": "203.0.113.10",
      "type": "inference", "workKey": "ms-vllm",
      "deploymentId": "3f9b2c4e-8a51-4d27-9e0c-5b1f6a7d2e48",
      "runtimeId": "vllm", "modelId": "qwen3-embedding-0.6b", "artifactId": "hf-bf16",
      "url": "http://203.0.113.10:8000/v1", "healthUrl": "http://203.0.113.10:8000/health",
      "apiKey": "3f1c9a7e5b2d4f6a8c0e1b3d5f7a9c2e",
      "ports": [8000], "portsOpened": true,
      "openedPorts": [{"securityGroupId": "etri-shared-aws-ap-northeast-2", "port": 8000, "protocol": "TCP", "result": "added"}]
    }
  ]
}
```

(값은 Swagger 의 예시를 모은 것입니다. 노드 셋이면 항목이 셋입니다.)

### 3-4. 응용 배포

`env` 에 3-3 의 주소를 넣습니다. Open WebUI 는 Ollama 의 루트 주소를 받으므로 `url` 에서 `/v1` 을 뺍니다.
Langflow 에는 vm3 항목의 `apiKey` 를 `MODEL_API_KEY` 로 넘깁니다. Dify 는 키를 env 로 받지 않으므로 화면에서 넣습니다.

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
  "app": {"name": "langflow", "env": {"LANGFLOW_SUPERUSER_PASSWORD": "<관리자 비밀번호>",
                                     "MODEL_BASE_URL": "http://203.0.113.13:8000/v1", "MODEL_API_KEY": "<vm3 의 apiKey>"}}
}'
```

| 응용 | env | 끝난 뒤 |
|---|---|---|
| Dify | `VLLM_BASE_URL` (화면에 넣을 값을 로그에 찍기만 함), `DIFY_PORT`(기본 80) | `http://<vm1>/install` 에서 관리자 계정을 만들고, 설정 > Model Provider > OpenAI-API-compatible 에 `VLLM_BASE_URL` 을, API Key 칸에 vm1 의 `apiKey`(3-3)를 넣음. 스크립트는 키 값을 찍지 않음 |
| Open WebUI | `OLLAMA_BASE_URL` (필수), `OPEN_WEBUI_PORT`(기본 3000) | `http://<vm2>:3000` |
| Langflow | `LANGFLOW_SUPERUSER_PASSWORD`(필수), `MODEL_BASE_URL`, `MODEL_API_KEY`(vm3 의 `apiKey`, Credential 전역 변수로 들어감), `LANGFLOW_PORT`(기본 7860) | `http://<vm3>:7860`. OpenAI 컴포넌트의 `OpenAI API Base` 칸에서 전역 변수 `MODEL_BASE_URL`, `OpenAI API Key` 칸에서 `MODEL_API_KEY` 를 고름 ([README.md](README.md) 12-3절) |

Langflow 의 관리자 비밀번호는 **처음 배포할 때** Langflow DB 에 저장되고, 그 DB 는 볼륨에 남습니다. 같은 노드에 다시 배포할 때는
처음과 같은 `LANGFLOW_SUPERUSER_PASSWORD` 를 줍니다. 다른 값을 주면 `deploy.sh` 의 로그인이 거절되어 배포가 `failed`
(`Langflow refused the admin login: check LANGFLOW_SUPERUSER_PASSWORD`)가 됩니다. 비밀번호를 바꾸려면 Langflow 화면에서 바꾼 뒤 그 값으로 배포합니다.

포트를 기본값과 다르게 쓰면(`OPEN_WEBUI_PORT=8080` 처럼) 열 포트도 같이 바꿔야 합니다: `"app": {..., "ports": [8080]}`.

## 4. 포털이 주소를 찾는 법

포털은 `GET /aiapp/endpoints` 하나로 지금 떠 있는 것을 다 읽습니다. 한 번에 `limit`(기본 100, 최대 1000)개까지라, 응답의 `total` 이 더 크면 `offset` 을 늘려 다음 페이지를 읽습니다([README.md](README.md) 9-0절).

```sh
curl -sS -H "$AUTH" "$AIAPP_URL/endpoints?nsId=etri&infraId=travel"
```

| 필드 | 쓰는 곳 |
|---|---|
| `nodeId`, `workKey` | 어느 노드의 무엇인지. 노드 하나·작업 키 하나에 항목 하나 |
| `nodeStatus`, `available` | 부를 때 CB-Tumblebug 에서 읽은 노드 상태(`Running`, `Suspended` 등). `Running` 일 때만 `available: true` 이고 주소가 있음 |
| `type: inference` 의 `url` | OpenAI 호환 API (`/v1/chat/completions`, `/v1/embeddings`) |
| `type: inference` 의 `apiKey` | vLLM 을 부를 때 `Authorization: Bearer <apiKey>`. 노드가 `Running` 이 아니어도 나옴. Ollama 항목에는 없음 |
| `type: app` 의 `urls[].url` | 응용 화면 (`http://<노드>:<포트>`) |
| `portsOpened` | `false` 면 보안 그룹에 포트가 안 열렸을 수 있음. `portsError`·`notes` 를 봄 |
| `deploymentId` | 이 주소를 만든 배포. 내릴 때 씀 |

- 다시 배포하면 항목의 `deploymentId` 와 주소가 새 것으로 바뀝니다. 내리면 항목이 빠집니다.
- 주소는 노드의 지금 IP(공인 IP, 없으면 사설 IP)로 만듭니다. 노드를 정지했다 다시 켜서 IP 가 바뀌어도 다시 배포하지 않고 그대로 읽으면 됩니다.
- 노드가 `Running` 이 아니면(정지·종료·실패) 항목은 남지만 `available: false` 이고 `host`·`url`·`urls` 가 없습니다. CB-Tumblebug 을 못 읽으면
  `nodeStatus: unknown`, 노드가 없어졌으면 `not-found` 입니다. 포털이 쓸 주소만 받으려면 `?available=true` 를 붙입니다.
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
| 응용 배포의 성공 시점 | 응용은 script 가 끝난 뒤 응용 포트가 HTTP 로 응답하고 compose 프로젝트의 컨테이너가 재시작 없이 돌아야 `running` 입니다([README](README.md) 7절). 2026-11-03 시험 때는 script 종료만 봐서 Open WebUI 가 성공 직후 잠깐 응답하지 않았고(약 20초 뒤 200), Dify 는 컨테이너 두 개가 계속 다시 시작하는데도 성공으로 기록됐습니다. 확인은 배포 때 한 번이라 그 뒤에 죽는 것은 보지 않습니다 |
