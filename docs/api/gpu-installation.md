# CB-Tumblebug GPU VM과 AI-App 설치·시험

Ubuntu 관리 호스트에 CB-Tumblebug, AI-App과 Semaphore를 설치하고, AWS GPU VM 한 대에서 Ollama와 Qwen3-VL-8B를 실행하는 절차다. Open WebUI 연결, AI-Ops 연결과 시험 종료 절차를 포함한다. 관리 호스트와 GPU VM은 별도 서버이며, 관리 호스트는 GPU 없이 사용할 수 있다.

2026-11-11에 Ubuntu 22.04.5, Docker 29.7.2, Compose 5.4.0, CB-Tumblebug `b808b9a2`에서 GPU 배포와 추론 응답을 확인했다. AI-App은 `khu` 브랜치에서 이 문서와 스크립트, Semaphore 출력 설정을 포함한 체크아웃을 사용한다. 실제 검증 조합은 AWS 서울 `g4dn.xlarge`, NVIDIA T4 16GB, Ubuntu 22.04 NVIDIA 드라이버 이미지, Ollama `0.40.13`, `qwen3-vl:8b`다.

## 1. 호스트 준비

Docker 설치와 이미지·모델 다운로드에 사용할 인터넷 연결, AWS 자격 증명과 GPU VM을 실행할 수 있는 계정이 필요하다. CB-Tumblebug의 관리 호스트 권장 자원은 8코어, RAM 16GiB, 여유 디스크 50GiB다. 이 절차의 GPU VM에는 100GiB 디스크를 지정한다.

Ubuntu에서 기본 도구와 Docker를 설치한다. 기존 Docker를 사용하는 환경은 버전 확인부터 진행한다.

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl git jq make openssl python3
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
  -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
. /etc/os-release
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu %s stable\n' \
  "$(dpkg --print-architecture)" "$VERSION_CODENAME" \
  | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo usermod -aG docker "$USER"
```

다시 로그인해 Docker 그룹을 적용한다. 이어서 CB-Tumblebug 초기화에 쓰는 `uv`를 설치한다. [Docker 설치](https://docs.docker.com/engine/install/ubuntu/) · [uv 설치](https://docs.astral.sh/uv/getting-started/installation/)

```bash
curl -LsSf https://astral.sh/uv/install.sh -o /tmp/install-uv.sh
sh /tmp/install-uv.sh
export PATH="$HOME/.local/bin:$PATH"

docker version
docker compose version
uv --version
python3 --version
```

## 2. 저장소 준비

새 설치에서는 세 저장소를 같은 디렉터리 아래에 둔다.

```bash
mkdir -p ~/ai-mcmp
cd ~/ai-mcmp
git clone https://github.com/cloud-barista/cb-tumblebug.git
git clone --branch khu https://github.com/cloud-barista/ai-app.git
git clone https://github.com/cloud-barista/ai-ops.git
```

```text
~/ai-mcmp/
  cb-tumblebug/
  ai-app/
  ai-ops/
```

기존 작업본에서는 각 저장소의 현재 브랜치와 로컬 설정을 사용한다. 아래 명령의 `cd` 경로는 이 배치를 기준으로 한다.

## 3. CB-Tumblebug 기동과 CSP 계정 등록

### 계정 값의 대응

| 값 | 사용하는 곳 |
| --- | --- |
| CB-Tumblebug `.env`의 `TB_API_USERNAME` / `TB_API_PASSWORD` | Tumblebug 관리 API 인증 |
| AI-App `.env`의 `AIAPP_TUMBLEBUG_USERNAME` / `AIAPP_TUMBLEBUG_PASSWORD` | 위 Tumblebug API 계정과 같은 값 |
| `~/.cloud-barista/credentials.yaml`의 AWS 키 | Tumblebug가 AWS 자원을 조회·생성할 때 사용 |
| `AIAPP_SEMAPHORE_PASSWORD` | 새 Semaphore 관리자에게 지정할 비밀번호 |
| `AIAPP_AUTH_TOKEN` | AI-App 관리 API의 Bearer 토큰 |

### 터미널 1에서 기동

```bash
cd ~/ai-mcmp/cb-tumblebug
cp -n .env.example .env
chmod 600 .env
nano .env
make up
```

`.env`에서 Tumblebug·Spider·Terrarium 등의 API 비밀번호를 정한다. 예시의 `TB_API_USERNAME`은 `default`이며, AI-App에도 같은 API 계정을 설정한다. 관리 포트를 외부에서 접근할 수 있는 호스트에서는 접근할 네트워크와 인증값을 설정한다.

현재 `make up`은 로그를 표시하면서 실행된다. 이 터미널을 유지하고 다음 작업은 새 터미널에서 진행한다.

### 터미널 2에서 초기화

```bash
cd ~/ai-mcmp/cb-tumblebug
make gen-cred
nano ~/.cloud-barista/credentials.yaml
make enc-cred
export TB_API_USERNAME=default
read -rsp 'Tumblebug API password ' TB_API_PASSWORD
printf '\n'
export TB_API_PASSWORD
make init
```

AWS 항목의 `aws_access_key_id`, `aws_secret_access_key`에 사용할 계정을 입력한다. 암호화 단계에서 엔터로 키를 생성했다면 `s`를 선택해 키를 저장할 수 있다. 직접 암호를 정했다면 초기화할 때 같은 암호를 입력한다. 위의 `TB_API_USERNAME`과 입력받는 `TB_API_PASSWORD`에는 Tumblebug `.env`의 API 계정을 사용한다. 사용자명을 변경했다면 `default`를 실제 값으로 바꾼다.

초기화 방식은 `a`를 선택해 제공된 카탈로그 백업을 복원하고, 진행 확인에 `y`를 입력한다. 마지막의 `Initialization completed successfully.`를 확인한다. 백업 카탈로그의 생성 시점과 현재 가용성은 이후 사전 검토에서 확인한다.

```bash
chmod 600 ~/.cloud-barista/credentials.yaml.enc
if test -f ~/.cloud-barista/.tmp_enc_key; then
  chmod 600 ~/.cloud-barista/.tmp_enc_key
fi

TB_URL=http://localhost:1323/tumblebug
TB_USER=default
curl -sS -u "$TB_USER" "$TB_URL/readyz" | jq
curl -sS -u "$TB_USER" "$TB_URL/connConfig" \
  | jq '[.connectionconfig[]? | select(.verified == true) | {configName, providerName}]'
```

`ready=true`, `initialized=true`와 사용할 리전의 연결을 확인한다. `curl -u "$TB_USER"`는 실행할 때 API 비밀번호를 입력받는다. `TB_USER`를 변경한 환경에서는 실제 사용자명으로 바꾼다.

## 4. GPU VM 한 대 생성

### GPU 스펙과 이미지 확인

아래는 AWS 서울의 T4 한 대를 선택한 예시다. 다른 리전·스펙을 사용할 때는 조회한 카탈로그 ID로 바꾼다.

```bash
SPEC_ID='aws+ap-northeast-2+g4dn.xlarge'
curl -sS -u "$TB_USER" "$TB_URL/ns/system/resources/spec/$SPEC_ID" \
  | jq '{id, vCPU, memoryGiB, acceleratorModel, acceleratorMemoryGB, availabilityStatus, availableZones, costPerHour}'

curl -sS -u "$TB_USER" \
  -H 'Content-Type: application/json' \
  -d '{"matchedSpecId":"aws+ap-northeast-2+g4dn.xlarge","isGPUImage":true,"isBasicGpuImage":true,"osType":"ubuntu 22.04","maxResults":10}' \
  "$TB_URL/ns/system/resources/searchImage" \
  | jq '.imageList[] | {id, osDistribution, imageStatus}'
```

검증에서는 `Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 22.04) 20261027` 이미지로 드라이버 `595.112.07`을 확인했다. 사용할 이미지 ID는 현재 조회 결과에서 선택한다. GPU 노드는 NVIDIA 드라이버 550 이상과 외부 이미지·모델 저장소 연결이 필요하다.

### 생성 요청과 사전 검토

`IMAGE_ID`에 선택한 이미지의 `id`를 넣는다.

```bash
NS_ID=default
INFRA_ID=aiapp-gpu-test
IMAGE_ID='<선택한 이미지 ID>'

jq -n --arg name "$INFRA_ID" --arg spec "$SPEC_ID" --arg image "$IMAGE_ID" '{
  name: $name,
  nodeGroups: [{
    name: "gpu",
    connectionName: "aws-ap-northeast-2",
    specId: $spec,
    imageId: $image,
    nodeGroupSize: 1,
    rootDiskType: "gp3",
    rootDiskSize: 100
  }]
}' > /tmp/aiapp-gpu-request.json

curl -sS -u "$TB_USER" -H 'Content-Type: application/json' \
  -d @/tmp/aiapp-gpu-request.json \
  "$TB_URL/ns/$NS_ID/infraDynamicReview" | jq
```

`overallStatus=Ready`, `creationViable=true`, `nodeReviews[].canCreate=true`와 `estimatedCost`를 확인한다. 생성 시점의 CSP 재고나 계정 쿼터 때문에 실제 요청이 실패할 수 있다. `estimatedCost`는 Tumblebug가 계산한 VM 비용이며, 선택한 디스크와 트래픽 등의 사용료도 발생한다. [Tumblebug 생성 절차](https://github.com/cloud-barista/cb-tumblebug/blob/main/docs/feature_guide/ko/infra-provisioning.md)

다음 명령으로 실제 VM을 생성한다.

```bash
curl -sS -u "$TB_USER" -H 'Content-Type: application/json' \
  -d @/tmp/aiapp-gpu-request.json \
  "$TB_URL/ns/$NS_ID/infraDynamic" | jq

curl -sS -u "$TB_USER" \
  "$TB_URL/ns/$NS_ID/infra/$INFRA_ID?option=status" | jq

curl -sS -u "$TB_USER" \
  "$TB_URL/ns/$NS_ID/infra/$INFRA_ID?option=id" | jq '.output'
```

`Running` 상태와 노드 ID를 확인한다. 이 요청의 노드 ID는 보통 `gpu-1`이며, 실제 응답 값을 사용한다. ID 목록 API의 JSON 필드는 `output`이다.

```bash
NODE_ID=gpu-1
```

이제 배포 대상은 `$NS_ID/$INFRA_ID/$NODE_ID`다.

## 5. AI-App과 Semaphore 설치

```bash
cd ~/ai-mcmp/ai-app
cp -n .env.example .env
chmod 600 .env
nano .env
```

아래 항목을 활성 설정으로 작성한다. `<...>` 부분은 실제 값으로 바꾼다. 토큰과 새 비밀번호는 `openssl rand -hex 32`로 생성할 수 있다.

```dotenv
AIAPP_TUMBLEBUG_BASE_URL=http://host.docker.internal:1323/tumblebug
AIAPP_TUMBLEBUG_USERNAME=default
AIAPP_TUMBLEBUG_PASSWORD='<Tumblebug API 비밀번호>'
AIAPP_SEMAPHORE_PASSWORD='<Semaphore 관리자 비밀번호>'
AIAPP_AUTH_TOKEN='<AI-App Bearer 토큰>'
AIAPP_TUMBLEBUG_DRY_RUN=true
```

`host.docker.internal`은 AI-App 컨테이너가 같은 호스트의 Tumblebug에 접근할 때 쓰는 주소다. Compose에 Linux 호스트 매핑이 포함돼 있다. Tumblebug가 다른 서버에 있으면 그 서버의 접근 가능한 주소를 사용한다.

Compose의 Semaphore에는 다음 출력 설정이 포함돼 있다. `community.general` 12.0.0에서 제거된 `yaml` 플러그인에 의존하던 실행 오류를 해결한다. [Ansible 출력 설정](https://docs.ansible.com/projects/ansible/latest/collections/ansible/builtin/default_callback.html)

```yaml
ANSIBLE_STDOUT_CALLBACK: ansible.builtin.default
ANSIBLE_CALLBACK_RESULT_FORMAT: yaml
```

```bash
./scripts/gpu-test.sh up
export AIAPP_AUTH_TOKEN="$(docker compose config --format json \
  | jq -r '.services["ai-app-server"].environment.AIAPP_AUTH_TOKEN // ""')"
./scripts/gpu-test.sh status
```

`semaphore.authenticated=true`, `tumblebug.reachable=true`, Tumblebug의 ready·initialized 메시지, `stage.writable=true`, `dryRun=true`를 확인한다. Semaphore 프로젝트와 템플릿은 첫 실제 배포 때 생성된다. [기존 상태 확인 예제](examples/00-status.sh)

### 관리 주소

| 서비스 | 관리 호스트에서 접근하는 주소 |
| --- | --- |
| Tumblebug API | `http://localhost:1323/tumblebug/api` |
| AI-App 콘솔 | `http://localhost:8090/aiapp/ui` |
| AI-App Swagger | `http://localhost:8090/aiapp/api/index.html` |
| Semaphore | `http://localhost:3001` |

실제 배포의 `AIAPP_DEPLOYMENT_OPEN_PORTS=true`는 서빙 포트를 `0.0.0.0/0`에 연다. Ollama에는 기본 인증이 없으므로 접속 범위를 운영 환경에 맞춰 설정한다. 시험망에서 직접 규칙을 관리할 때는 `AIAPP_DEPLOYMENT_OPEN_PORTS=false`를 지정하고 필요한 출처·포트를 보안 그룹에 설정한다. [포트 동작](README.md#8-서빙-포트-열기)

## 6. Dry-run

```bash
./scripts/gpu-test.sh dry-run "$NS_ID" "$INFRA_ID" "$NODE_ID"
```

`status=dry-run`, `dryRun=true`, 대상 노드와 준비된 `serviceUrl`을 확인한다. 이 단계는 노드 조회와 파일 준비를 검증한다. 스크립트는 서버의 dry-run 설정을 먼저 확인한다.

## 7. 실제 추론 배포와 응답 확인

`.env`의 설정을 `AIAPP_TUMBLEBUG_DRY_RUN=false`로 바꾸고 반영한다.

```bash
docker compose --profile deploy up -d ai-app-server semaphore
./scripts/gpu-test.sh status
./scripts/gpu-test.sh deploy "$NS_ID" "$INFRA_ID" "$NODE_ID"
```

스크립트는 새 배포 ID를 출력하고, Docker 설치와 Compose 실행이 끝날 때까지 기본 최대 30분 동안 확인한다. 이어서 배포 기록의 `running`, GPU 확인 근거와 공개된 엔드포인트를 확인하고 실제 질문을 보낸다. 초기 모델 적재를 포함한 추론 요청의 제한 시간은 300초다.

성공한 출력에는 배포의 `status=running`과 모델 응답의 `done=true`, 비어 있지 않은 `response`, 양수의 `eval_count`가 포함된다. 실패·부분 완료·중단 상태는 스크립트가 오류로 종료한다. 출력된 새 배포 ID는 기록해 둔다.

실행 중인 서비스의 응답만 다시 확인할 때는 다음 명령을 쓴다.

```bash
./scripts/gpu-test.sh test "$NS_ID" "$INFRA_ID" "$NODE_ID"
./docs/api/examples/05-endpoints.sh "$NS_ID" "$INFRA_ID" "$NODE_ID"
```

직접 호출할 때는 조회된 서비스 주소에서 `/v1`을 뺀 주소를 사용한다.

```bash
OLLAMA_URL='http://<조회된 노드 IP>:11434'
curl -sS --max-time 300 "$OLLAMA_URL/api/generate" \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3-vl:8b","prompt":"What is 2 + 2? Reply with the number only.","stream":false,"think":false,"options":{"num_predict":512,"temperature":0}}' \
  | jq '{model, done, response, eval_count}'
```

2026-11-11 시험에서는 `response="4"`, `done=true`와 Tesla T4의 CUDA 사용을 확인했다. 모델 호출 주소는 GPU 노드가 직접 제공한다. [Ollama API](https://docs.ollama.com/api/generate)

## 8. Open WebUI 연결

응용 카탈로그와 엔드포인트에서 기존 등록·배포 상태를 확인한다. 새로 등록하면 같은 이름의 다음 버전이 만들어진다.

```bash
./docs/api/examples/01-catalog.sh
./docs/api/examples/02-upload-app.sh
```

Tumblebug에서 GPU 노드의 사설 IP를 조회해 Open WebUI에 전달한다.

```bash
NODE_PRIVATE_IP="$(curl -sS -u "$TB_USER" \
  "$TB_URL/ns/$NS_ID/infra/$INFRA_ID/node/$NODE_ID" | jq -er '.privateIP | select(length > 0)')"

./docs/api/examples/04-deploy-app.sh "$NS_ID" "$INFRA_ID" "$NODE_ID" \
  open-webui "OLLAMA_BASE_URL=http://$NODE_PRIVATE_IP:11434"
./docs/api/examples/05-endpoints.sh "$NS_ID" "$INFRA_ID" "$NODE_ID"
```

응용 배포가 `running`이면 출력된 Open WebUI 주소를 브라우저에서 연다. 사용자 등록 후 `qwen3-vl:8b`를 선택해 질문한다. 모델 주소의 `OLLAMA_BASE_URL`은 `/v1` 없이 지정한다. [Open WebUI 예제](../../examples/apps/open-webui/)

## 9. AI-Ops 연결

AI-Ops의 Go 모듈을 호스트에서 실행하려면 Go 1.25 이상과 `GOTOOLCHAIN=auto`를 준비한다. 사용할 LLM의 후보 설정에는 `candidate_id`, `llm_type`, `actual_model`, OpenAI 호환 `endpoint`, `enabled=true`가 필요하다. 실행 환경에서 접근 가능한 모델로 설정한다. 후보 예시는 `llmop/config/application_profile_llm_candidates.json`에 있다. [OpsLLM 설정](../../../ai-ops/llmop/go/service-control-api/README.md)

앞에서 배포한 GPU 모델을 OpsLLM 자연어 시험에 사용하려면 다음처럼 후보를 작성한다. `OLLAMA_URL`은 7절에서 확인한 노드의 Ollama 주소다.

```bash
cd ~/ai-mcmp/ai-ops
umask 077
mkdir -p output/local
jq -n --arg endpoint "$OLLAMA_URL/v1/chat/completions" '{
  version: "1",
  benchmark_status: "not_executed",
  candidates: [{
    candidate_id: "gpu-qwen3-vl",
    llm_type: "self_hosted",
    role_label: "AI-App operations interpretation",
    provider: "openai-compatible",
    actual_model: "qwen3-vl:8b",
    endpoint: $endpoint,
    enabled: true,
    json_mode: true,
    reasoning_effort: "none",
    timeout_seconds: 120
  }]
}' > output/local/gpu-opsllm-candidates.json
```

아래 환경변수를 AI-Ops 두 서비스를 실행할 각 터미널에 설정한다. `AIOPS_OPS_LLM_TOKEN`은 LLM Op와 에이전트가 같은 값을 사용한다. `AIOPS_AIAPP_TOKEN`은 AI-App의 Bearer 토큰과 맞춘다. 엔진 등록 토큰에는 별도로 생성한 값을 사용하고 AI-App의 동명 설정과 맞춘다.

```bash
export AIOPS_AIAPP_BASE_URL=http://127.0.0.1:8090
export AIOPS_AIAPP_NAMESPACE=default
export AIOPS_AIAPP_TOKEN='<AIAPP_AUTH_TOKEN과 같은 값>'
export AIOPS_AIAPP_ENGINE_REGISTRY_TOKEN='<엔진 등록 공통 토큰>'
export AIOPS_LLMOP_BASE_URL=http://127.0.0.1:18110
export AIOPS_OPS_LLM_TOKEN='<OpsLLM 공통 토큰>'
export AIOPS_LLM_CANDIDATES_PATH="$HOME/ai-mcmp/ai-ops/output/local/gpu-opsllm-candidates.json"
export AIOPS_LLMOP_CANDIDATE_ID=gpu-qwen3-vl
export AIOPS_AGENT_EXECUTION_TIMEOUT_SECONDS=120
```

터미널을 나눠 실행한다. 이 예제의 LLM Op는 `18110`, 에이전트는 `18131` 포트를 사용한다.

```bash
cd ~/ai-mcmp/ai-ops
PORT=18110 go -C llmop/go/service-control-api run ./cmd/llmop-api
```

```bash
cd ~/ai-mcmp/ai-ops
AIOPS_BIND_ADDRESS=0.0.0.0 PORT=18131 \
  go -C app_automation_agent/go/service-control-api run ./cmd/service-control-api
```

AI-App `.env`에 다음을 추가한다. `0.0.0.0` 리스너는 같은 호스트의 컨테이너에서 AI-Ops에 접근할 수 있게 한다. 호스트의 `18131` 접근 범위를 운영 환경에 맞춰 설정한다.

```dotenv
AIAPP_MOCK_NL_URL=http://host.docker.internal:18131
AIAPP_ENGINE_REGISTRY_URL=http://host.docker.internal:18131/api/v1/integrations/ai-app
AIAPP_ENGINE_REGISTRY_TOKEN='<엔진 등록 공통 토큰>'
```

```bash
cd ~/ai-mcmp/ai-app
docker compose --profile deploy up -d ai-app-server ai-automation-engine
curl -sS http://localhost:18110/healthz | jq
curl -sS http://localhost:18131/healthz | jq
curl -sS http://localhost:8090/aiapp/partner/nl/readyz | jq

curl -sS -H "Authorization: Bearer $AIAPP_AUTH_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"intent":"구축형 LLM으로 배포 가능한 응용을 보여줘","llmType":"self_hosted"}' \
  http://localhost:8090/aiapp/partner/nl/ns/default/intents | jq
```

응답의 OpsLLM 실행 ID와 실제 모델, 엔진 호출 결과를 확인한다. AI-App 콘솔에서도 같은 자연어 요청을 보낼 수 있다. 승인된 배포·업데이트와 추론 최적화의 입력은 해당 API 계약에 맞춰 준비한다. [AI-Ops 실행 설정](../../../ai-ops/docs/getting-started.md) · [승인 배포](approved-deployment.md) · [추론 최적화](inference-optimization.md)

기동 후에는 AI-Ops의 통합 시험 스크립트로 자연어 카탈로그 조회, 실제 배포 상태 조회, REST·MCP 실행 기록의 일치를 확인한다. 현재 사용 중인 추론 배포 ID를 지정한다.

```bash
cd ~/ai-mcmp/ai-ops
AIAPP_URL=http://localhost:8090/aiapp \
  ./platform/scripts/verify-aiapp-integration.sh "$NS_ID" '<실제 추론 배포 ID>' self_hosted
```

2026-11-11에는 GPU의 `qwen3-vl:8b`가 자연어를 해석하고, 등록된 `ai-mcmp-app-automation-engine`이 MCP로 배포의 `running` 상태를 조회하는 흐름을 확인했다. 상태 요청에는 AI-Ops·OpsLLM·엔진의 각 실행 ID가 기록된다.

같은 시험에서 REST로 생성한 실행 기록의 MCP 조회와 MCP로 생성한 기록의 REST 조회, 잘못된 토큰의 `401`, 범위 밖 네임스페이스의 `403`을 확인했다. LLM Op와 에이전트를 재시작한 뒤에도 OpsLLM·자연어 실행 기록과 엔진 등록이 복구됐다. 업데이트 지시는 제안과 승인 대기 상태로 기록됐다.

## 10. 장애 확인

```bash
./scripts/gpu-test.sh status
./docs/api/examples/06-logs.sh '<이번 배포 ID>'
docker compose logs --tail 100 ai-app-server semaphore
```

| 증상 | 확인과 조치 |
| --- | --- |
| `initialized=false` | 초기화 선택·진행 확인과 마지막 성공 메시지를 확인하고 Tumblebug 연결·카탈로그를 조회한다. |
| Tumblebug 인증 실패 | Tumblebug `.env`의 API 계정과 AI-App의 대응 값을 맞추고 해당 컨테이너에 반영한다. |
| 컨테이너에서 Tumblebug 접속 실패 | 같은 호스트라면 `AIAPP_TUMBLEBUG_BASE_URL`에 `host.docker.internal` 주소를 사용한다. |
| `community.general.yaml has been removed` | Compose의 Semaphore 출력 설정을 확인하고 `docker compose --profile deploy up -d --no-deps semaphore`로 반영한다. |
| SSH 접속 실패 | Semaphore에서 노드의 SSH 포트에 접근 가능한지 확인한다. 이미지 사용자 자동 선택 결과는 `nodes[].sshUser`, `sshUserSource`, `notes`에 기록된다. |
| GPU 확인 실패 | 드라이버 버전, NVIDIA Container Toolkit과 `gpuCheck` 로그를 확인한다. |
| 배포는 실행 중인데 스크립트 대기 종료 | 출력된 배포 ID의 `/progress`와 로그를 조회한다. `POLL_MAX`와 `POLL_INTERVAL`로 대기 범위를 설정할 수 있다. |
| 모델 응답 실패 | `05-endpoints.sh`의 URL과 포트 접근, 모델 적재 결과를 확인한다. Ollama 최초 호출에는 모델 적재 시간이 포함된다. |
| AI-Ops 자연어 준비 실패 | 후보의 활성화·호출 주소·실제 모델, 공통 토큰, 엔진 카드 등록과 네임스페이스를 확인한다. |

## 11. 시험 종료

응용과 추론 서비스 각각의 실제 배포 ID로 제거한다. 예제 스크립트가 `removed`까지 확인한다.

```bash
./docs/api/examples/07-remove.sh '<응용 배포 ID>'
./docs/api/examples/07-remove.sh '<추론 배포 ID>'
```

GPU VM을 종료·삭제할 때는 Tumblebug의 `terminate` 방식으로 Infra를 삭제한다.

```bash
curl -sS -u "$TB_USER" -X DELETE \
  "$TB_URL/ns/$NS_ID/infra/$INFRA_ID?option=terminate" | jq
curl -sS -u "$TB_USER" "$TB_URL/ns/$NS_ID/infra?option=id" | jq '.output'
```

CSP 콘솔에서도 VM 종료와 남은 디스크·네트워크 자원을 확인한다. AI-App의 서비스 제거는 모델 볼륨·폴더와 열린 포트를 보존하며, VM 사용료는 VM이 실행 중인 동안 이어진다. Infra 삭제 후 공유 네트워크 등이 남을 수 있다. [공유 자원 정리](../../../cb-tumblebug/docs/feature_guide/ko/shared-resource-management.md)

관리 서비스를 정지한다.

```bash
cd ~/ai-mcmp/ai-app
docker compose --profile deploy down
cd ~/ai-mcmp/cb-tumblebug
make down
```

기본 종료 명령은 카탈로그·배포 기록이 있는 볼륨을 보존한다. 다음 기동에서 같은 데이터로 복구할 수 있다.

## 12. 자동 시험 스크립트 검증

```bash
cd ~/ai-mcmp/ai-app
bash -n scripts/gpu-test.sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/tests -v
```

회귀 시험은 모의 HTTP 응답으로 모드 확인, 정상·실패 배포와 추론 응답 검증을 확인한다. 실제 서버에서는 `status`와 `test` 명령으로 연결과 응답을 재확인한다.
