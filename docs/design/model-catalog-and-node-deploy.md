# AI 모델 카탈로그와 노드 배포 재구성 설계 (3차 개편)

> 작성 2026-10-29. 2차 개편([app-agent-split.md](app-agent-split.md))의 프로세스 구성(AI-App 서비스 + 자동화 엔진 +
> 협력 기관 모의)은 그대로 두고, 그 안의 **데이터 모델과 배포 실행 경로**를 다시 짠다.

## 1. 왜 다시 짜나

| 지금 | 문제 |
|------|------|
| 모델, 추론엔진, 설치 명령을 `AppSpec` 하나에 묶음 (`internal/model/app.go:130`) | 같은 모델을 다른 엔진·다른 포맷으로 쓰려면 항목을 통째로 복사해야 한다. 모델 정보만 보고 싶은 외부 모듈(경희대 모델 추천)이 설치 명령까지 받는다 |
| 모델 정보가 ID·포맷·크기 3개뿐 (`ModelRef`, `app.go:86`) | 용도(텍스트/이미지/임베딩), 출처, 정밀도, 호환 엔진을 담을 자리가 없다 |
| AI-App 이 CB-Tumblebug 으로 VM 을 직접 만들면서 설치 명령을 같이 실행 (`internal/deploy/service.go:171`, postCommands) | 설치는 VM 을 만들 때 한 번만 돈다. 이미 있는 VM 에 배포할 수 없고, 파일을 넘길 통로가 없고, 단계별 로그가 남지 않는다 |
| 응용은 설치 명령 문자열 | Dify 처럼 compose 파일·설정 파일 여러 개가 필요한 응용을 올릴 수 없다 |

## 2. 전체 흐름

```
외부 요청 ─▶ [경희대 Agent] ── GET /aiapp/models ──▶ [AI-App: 모델 카탈로그]        (모델 추천, 경희대 몫)
                   │ 고른 모델·아티팩트
                   ▼
            [인프라 추천 Agent, cm-beetle 형태] ─▶ Tumblebug Infra 요청 후보       (인프라 추천, 협력 기관 몫)
                   │
                   ▼
            [CB-Tumblebug] Infra 생성 (aws / azure / gcp 노드)
                   │ nsId / infraId / nodeId
                   ▼
[AI-App] ── 노드 정보·SSH 키 조회 ─▶ CB-Tumblebug
   │ 공유 저장소에 파일 배치, Semaphore 에 키·inventory·template 등록
   ▼
[Semaphore] ── SSH ─▶ 노드
   ├ 모델 서비스 배포: docker 설치 → 엔진+모델 compose → up → 헬스 확인
   └ 응용 배포: 업로드한 파일 복사 → script 실행
```

- **AI-App 은 VM 을 만들지 않는다.** CB-Tumblebug 이 만든 노드를 받아 그 위에 배포한다.
  VM 생성은 인프라 추천 쪽(cm-beetle 은 추천 결과를 그대로 `POST /migration/ns/{nsId}/infra` 로 넘겨 Tumblebug 을 직접
  부른다)이나 운영자가 한다. 노드 제어(정지·재개·재부팅·종료)는 AI-App 이 계속 Tumblebug 으로 한다.
- 모델 추천(정밀도·크기로 필요 자원을 계산하는 것)과 인프라 추천은 이 repo 의 담당이 아니다. AI-App 은 판단 재료가 되는
  메타데이터를 **원래 값 그대로** 내준다.

## 3. 데이터 모델

### 3.1 Model 과 Artifact

같은 모델이 출처마다 다른 형태로 배포된다(Qwen3-VL-8B 는 HF 에 BF16 safetensors 로, Ollama 에 GGUF 로). 그래서 포맷은
모델이 아니라 **아티팩트**에 둔다.

| Model 필드 | 뜻 | 예 |
|------------|----|----|
| `id` | 카탈로그 ID | `qwen3-vl-8b` |
| `name` | 표시 이름 | `Qwen3-VL-8B-Instruct` |
| `family`, `publisher` | 계열, 공개 주체 | `qwen3-vl`, `Qwen` |
| `parameters` | 파라미터 수 (원래 표기) | `8B` |
| `tasks` | 용도 | `chat`, `vision-chat`, `embedding` |
| `inputModalities` | 받는 입력 | `text`, `image` |
| `contextLength` | 문맥 길이 (모르면 비움) | `262144` |
| `license` | 라이선스 | `apache-2.0` |
| `artifacts[]` | 아래 표 | |

| Artifact 필드 | 뜻 | 예 |
|---------------|----|----|
| `id` | 모델 안에서의 ID | `hf-bf16`, `ollama-q4km` |
| `source` | 출처 | `huggingface`, `kaggle`, `ollama`, `docker-model-runner` |
| `ref` | 출처에서의 이름 | `Qwen/Qwen3-VL-8B-Instruct`, `qwen3-vl:8b`, `ai/qwen3-vl` |
| `format` | 파일 형식 | `hf`(safetensors), `gguf` |
| `precision` | 정밀도·양자화, **원래 값 그대로** | `BF16`, `FP8`, `Q4_K_M` |
| `sizeGiB` | 받을 크기 (모르면 0) | `6.2` |
| `engines[]` | 호환 엔진과 최소 버전 | `{engine: vllm, minVersion: 0.11.0}` |

- 호환 엔진은 포맷만으로 정하지 않고 명시한다. 같은 safetensors 라도 아키텍처 지원 여부가 엔진 버전마다 다르다.
- 정밀도로 VRAM 을 계산하는 등의 추천 로직은 넣지 않는다(경희대 몫).

### 3.2 Engine 프로필

| 필드 | 예 (vllm) | 예 (ollama) |
|------|-----------|-------------|
| `id` | `vllm` | `ollama` |
| `image` | `vllm/vllm-openai:<ver>` | `ollama/ollama:<ver>` |
| `formats` | `hf` | `gguf` |
| `port`, `healthPath`, `apiBasePath` | `8000`, `/health`, `/v1` | `11434`, `/`, `/v1` |
| compose 틀 | `--model <ref>` 로 기동, GPU 예약 | 기동 후 `ollama pull <ref>` |

모델 서비스 배포 요청이 오면 아티팩트의 `format`·`engines` 와 엔진 프로필이 맞는지 보고, 맞으면 compose 파일을 만든다.

### 3.3 App

| 필드 | 뜻 |
|------|----|
| `name` | 응용 이름. 소문자·숫자·`_`·`-` 만 쓴다(4.5). 같은 이름으로 다시 올리면 새 `version` |
| `files[]` | 업로드한 파일들 (compose, 설정, 데이터) |
| `script` | 노드에서 실행할 스크립트. 파일들이 있는 폴더에서 실행된다 |

등록은 multipart 하나로 받는다. 대상 노드와 다른 노드의 주소(env)는 등록이 아니라 **배포 요청**에서 받는다.

## 4. 배포

### 4.1 대상

CB-Tumblebug 구조 그대로 받는다.

```json
{ "kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1" }
```

- csp·region·zone 은 요청으로 받지 않고 노드 정보(`GET /ns/{nsId}/infra/{infraId}/node/{nodeId}` 의
  `connectionConfig.providerName`, `regionZoneInfo`)에서 읽는다.
- k8s(`k8sClusterId`, 노드 그룹)를 받을 자리(`kind: k8s`)만 정해 두고, 이번에는 거절한다(`422`).

### 4.2 요청

```json
{
  "type": "model-service",
  "targets": [{ "kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1" }],
  "modelService": { "modelId": "qwen3-embedding-0.6b", "artifactId": "hf-bf16", "engine": "vllm" }
}
```
```json
{
  "type": "app",
  "targets": [{ "kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm1" }],
  "app": { "name": "dify", "version": 1, "env": { "VLLM_BASE_URL": "http://10.0.0.5:8000/v1" } }
}
```

응답은 배포 ID(uuid)와 노드별 상태다.

### 4.3 공유 저장소

```
<AIAPP_STAGE_ROOT>/<배포 uuid>/<csp>-<nodeId>/
    docker-compose.yml        (모델 서비스) 또는 업로드 파일 + script (응용)
    .env                      (배포 요청의 env)
```

AI-App 컨테이너와 Semaphore 컨테이너가 같은 볼륨을 같은 경로로 마운트한다. playbook 은 이 폴더를 노드로 복사한다.

### 4.4 Semaphore 실행 (노드 하나 기준)

| # | 단계 | 비고 |
|---|------|------|
| 1 | Tumblebug 에서 노드 정보와 SSH 키(`GET /ns/{nsId}/resources/sshKey/{sshKeyId}` 의 `privateKey`) 조회 | 공인 IP 가 없으면 사설 IP |
| 2 | 공유 저장소에 파일 배치 | 모델 서비스는 compose 를 만들어 넣는다 |
| 3 | Semaphore 에 그 노드의 key(ssh)와 inventory 등록 | 배포마다 만들고 끝나면 지운다. template 은 playbook 마다 하나씩 고정으로 둔다 |
| 4 | 태스크 실행: `install-docker` → `compose-up`(모델 서비스) 또는 `copy-and-run`(응용). 태스크 요청에 3의 `inventory_id` 를 넣어 template 의 inventory 를 덮어쓴다 | GPU 노드면 nvidia-container-toolkit 도 설치 |
| 5 | 상태 폴링, 끝나면 `GET /api/project/{p}/tasks/{id}/output` 로그를 배포 기록에 남김 | 참고한 두 repo 는 로그를 가져오지 않는다 |
| 6 | 3에서 등록한 key·inventory 삭제 | 성공·실패와 무관하게 |

**배포마다 등록하는 방식을 고른 이유.** mc-observability·o11y-manager 는 고정 템플릿에 대상 정보를 env 로 넘긴다. 그 방식은
Semaphore 화면에서 대상이 `127.0.0.1` 로만 보이고 병렬 실행을 위해 템플릿을 여러 벌 돌려 쓴다. 배포마다 key·inventory 를
등록하면 노드별로 무엇이 돌았는지 보이는 대신, 키가 Semaphore DB 에 남지 않도록 지우는 단계(6)가 반드시 있어야 한다.

**template 을 고정으로 바꾼 이유 (2026-10-29 구현 중 실측).** Semaphore v2.12.17 은 template 을 지우면 그 template 의 태스크
이력과 로그를 같이 지운다. 처음 설계대로 배포마다 template 을 만들고 지우면 이력이 남지 않는다. 같은 실측에서 태스크 요청의
`inventory_id` 가 template 의 inventory 를 덮어쓰는 것을 확인해서, template 은 고정하고 inventory 만 배포마다 바꾼다.

### 4.5 노드 위의 배치

노드 위의 폴더와 compose 프로젝트는 배포 ID 가 아니라 작업 키로 정한다.

| 종류 | 작업 키 | 노드 폴더 | compose 프로젝트 |
|------|---------|-----------|------------------|
| 모델 서비스 | `ms-<engine>` (예 `ms-vllm`, `ms-ollama`) | `/opt/aiapp/<작업 키>` | `aiapp-<작업 키>` |
| 응용 | `app-<name>` | `/opt/aiapp/<작업 키>` | `aiapp-<작업 키>` |

- 노드 하나에 모델 서비스는 엔진당 하나, 응용은 이름당 하나다. 엔진이 다른 모델 서비스와 다른 응용은 서로 건드리지 않는다.
- 같은 작업 키로 다시 배포하면 폴더 안의 파일을 새 것으로 바꾼다. 이전 배포가 남긴 파일 목록(`.aiapp-files`)에 있고
  새 목록에 없는 파일은 지운다. 목록에 없는 파일(컨테이너가 bind mount 로 쓴 데이터)과 docker named volume 은 그대로 둔다.
- 모델 서비스는 `docker compose -p aiapp-<작업 키> up -d --remove-orphans --force-recreate` 로 컨테이너를 바꾼다. named volume 이
  프로젝트 이름에 묶여 있어 받아 둔 모델 가중치도 다음 배포가 다시 쓴다.
- `--force-recreate` 는 heal 때문에 넣었다. heal 은 같은 설정으로 다시 배포하는데, 설정이 같으면 compose 가 unhealthy 컨테이너를
  그대로 두기 때문이다.
- 응용은 script 가 compose 를 직접 부르므로 script 실행 전에 `COMPOSE_PROJECT_NAME=aiapp-<작업 키>` 를 export 한다.
  script 안의 `docker compose` 가 이전 배포와 같은 프로젝트를 쓴다.
- 응용은 script 실행 전에 `docker compose -p aiapp-<작업 키> down --remove-orphans` 로 이전 컨테이너를 내린다(`-v` 없이,
  named volume 은 남는다). 파일 하나를 bind mount 한 컨테이너는 파일을 새로 바꿔도 옛 파일을 계속 보고, script 가 컨테이너를
  다시 만든다는 보장이 없어서다. 프로젝트가 없으면 아무 일도 하지 않는다. 다시 배포하는 동안 응용이 잠깐 끊기는 것은 감수한다.
- 같은 노드의 같은 작업 키는 서비스 안에서 한 배포씩 돈다. 앞 배포가 끝나야 다음 배포가 Semaphore 태스크를 시작한다.
- 공유 저장소 stage 폴더(`<uuid>/<csp>-<nodeId>/`)와 배포 기록은 지금처럼 배포 ID 별로 남는다. 배포 기록의 노드 결과에
  작업 키(`workKey`)와 노드 폴더(`nodeDir`)를 남긴다.
- docker compose 프로젝트 이름에는 `.` 을 쓸 수 없어서, 응용 이름에 `.` 을 쓸 수 없다. 업로드에서 거절하고, 배포 요청
  단계에서도 다시 거절한다.

**이렇게 정한 이유.** 처음에는 배포마다 `/opt/aiapp/<배포 ID>` 폴더와 별개 compose 프로젝트를 만들었다. 그러면 같은 노드에
같은 엔진을 다시 배포하거나(heal 이 그렇게 한다) 같은 응용을 다시 배포할 때 옛 컨테이너가 포트를 쥔 채로 남아 새 컨테이너가
포트 바인드에 실패한다. 작업 키로 폴더와 프로젝트를 고정하면 다시 배포가 옛 것을 바꾸는 동작이 된다.

### 4.6 dry-run

dry-run 이면 1·2 까지만 한다(조회와 파일 배치). Semaphore 에 아무것도 등록하지 않고, 만들어진 compose 와 대상 노드 정보를
결과로 돌려준다.

## 5. API

| 리소스 | 경로 | 비고 |
|--------|------|------|
| 모델 | `GET/POST /aiapp/models`, `GET/DELETE /aiapp/models/{modelId}` | 필터 `task`, `modality`, `format`, `engine`, `source` |
| 엔진 | `GET /aiapp/engines`, `GET /aiapp/engines/{engineId}` | 조회만 |
| 응용 | `POST /aiapp/apps` (multipart: name, files, script), `GET /aiapp/apps`, `GET /aiapp/apps/{name}`, `DELETE /aiapp/apps/{name}` | 버전 목록 포함 |
| 배포 | `POST /aiapp/deployments`, `GET /aiapp/deployments`, `GET /aiapp/deployments/{id}`, `GET /aiapp/deployments/{id}/logs` | |
| 노드 제어 | `POST /aiapp/deployments/{id}/control` | Tumblebug 노드 제어 |

요청·응답 필드는 Swagger 에 설명을 다 적는다. 다른 모듈(경희대 Agent, 인프라 추천 Agent, 포털)이 이 문서만 보고 붙을 수 있어야 한다.

MCP 도구도 같은 리소스 단위로 다시 묶는다. 등급(read·review·write·destructive)과 dry-run 게이트는 유지한다.

## 6. 자동화 엔진

워크플로 4개(`deploy`, `heal`, `control`, `status`)와 엔진 MCP 도구 구조는 유지하고, 입력과 단계만 새 도구에 맞춘다.

| 워크플로 | 입력 | 단계 (구현, `internal/engine`) |
|----------|------|-------------------------------|
| `deploy` | 4.2 의 배포 요청 그대로 | 모델(`get_model`) 또는 응용(`get_app`) 조회 → `create_deployment` → `get_deployment` 로 끝날 때까지 대기 |
| `heal` | `deploymentId` | 배포 기록 조회 → `succeeded` 면 끝, `pending`·`running` 이면 실패로 끝냄, 그 밖(`failed`·`partial`·`interrupted`·`dry-run`)이면 기록된 요청을 새 배포로 한 번 다시 실행하고 대기 |
| `control` | `deploymentId`, `action` | `control_node`. 노드마다 `done`·`blocked`·`failed` |
| `status` | `deploymentId` | 배포 기록(`get_deployment`) + 노드마다 마지막 태스크 출력 5줄(`get_deployment_logs`) |

설계 때와 달라진 것.

- **헬스는 엔진 단계가 아니다.** 처음 설계는 `deploy` 끝에 "헬스", `heal` 앞에 "헬스 확인" 을 두었지만, 헬스는 노드 안에서만
  확인할 수 있고(서빙 포트를 열지 않으므로 밖에서는 닿는다는 보장이 없다) 그 일은 `compose-up` 이 한다. `compose-up` 이
  `health_url`(`http://127.0.0.1:<port><healthPath>`)을 기다려 응답해야 노드가 `succeeded` 가 되므로, 엔진의 대기 단계가
  읽는 노드 상태가 곧 헬스 결과다. Ollama 는 `model-pull` 이 끝나기를 먼저 기다린다. 응용은 script 종료 코드만 본다.
- **`status` 는 기록과 로그만 본다.** 처음 설계의 "노드 상태 + 헬스" 에서 CB-Tumblebug 노드 상태 조회와 헬스는 빠졌다.
  배포 뒤에 노드가 멈췄는지는 `status` 로 알 수 없다.
- **`partial` 은 실행 실패다.** 일부 노드만 성공한 배포는 `deploy` 실행을 `failed` 로 끝내고, `heal` 의 대상이 된다.
- dry-run 이면 `create_deployment` 단계가 `blocked`, 대기 단계가 `waiting` 이다.
- 대기 단계는 `AIAPP_HEALTH_INTERVAL`(20초) 간격으로 `AIAPP_HEALTH_TIMEOUT`(15분)까지 묻는다. 넘으면 단계가 실패하고 배포는
  AI-App 에서 계속 돈다(8절).
- `list_applications` 는 모델(아티팩트·엔진 포함)과 업로드한 응용의 요약을 돌려준다. 옛 규칙 매처의 `query` 는 없어졌다.

## 7. ETRI 시나리오 (시드)

ETRI 가 제시한 여행 계획 통합 포털 시나리오. 포털은 별도 노드에 ETRI 환경에만 있으므로 시드에 넣지 않는다.
포털은 아래 세 노드를 부르는 "여러 노드에 걸친 AI 응용" 의 예다.

| 노드 | 역할 | 모델 | 아티팩트 | 엔진 | 응용 |
|------|------|------|----------|------|------|
| aws vm1 | 관광 문서 분석 (RAG) | Qwen3-Embedding-0.6B | HF `Qwen/Qwen3-Embedding-0.6B`, BF16 | vLLM | Dify |
| azure vm2 | 이미지 분석·여행 계획 생성 | Qwen3-VL-8B | Ollama `qwen3-vl:8b` (GGUF, 6.2GB) | Ollama | Open WebUI |
| gcp vm3 | 일정 검증·외부 API·알림 (에이전트) | Ministral 3 8B | HF `mistralai/Ministral-3-8B-Instruct-2512`, FP8 | vLLM | Langflow |

HF 값은 2026-10-29 에 `https://huggingface.co/api/models/<id>` 로 확인했다(Qwen3-Embedding-0.6B `feature-extraction` BF16,
Qwen3-VL-8B-Instruct `image-text-to-text` BF16, Ministral-3-8B-Instruct-2512 FP8 가중치 + BF16 일부, 아키텍처
`Mistral3ForConditionalGeneration`). Ollama `qwen3-vl:8b` 크기 6.2GB 는 ollama.com 태그 페이지 값이다.

### 응용 계층 설명

| 응용 | 무엇인가 | 배포 형태 |
|------|----------|-----------|
| Dify | 화면에서 챗봇·워크플로·에이전트를 조립하는 LLM 앱 개발 플랫폼. 문서 업로드 → 분할 → 임베딩 → 벡터 DB 의 RAG 기능 내장 | 컨테이너 여러 개 (api, worker, web, postgres, redis, 벡터 DB, nginx) |
| Open WebUI | Ollama·OpenAI 호환 API 에 붙는 채팅 웹 화면. 첨부 이미지를 비전 모델로 넘김 | 컨테이너 1개 |
| Langflow | 노드를 이어 LangChain 기반 에이전트를 만드는 도구. 플로우를 API 로 열고 외부 API 호출을 노드로 붙임 | 컨테이너 1개 (+ DB 선택) |

## 8. 열린 문제

| 문제 | 근거 |
|------|------|
| vm1 에 글을 생성하는 LLM 이 없다 | 임베딩 모델은 검색용 벡터만 만든다. "관광지·맛집 정보 추출" 은 생성 LLM 이 필요하다. vm1 에 생성 모델을 더 두거나 vm2 를 불러야 한다 |
| vm2 의 이미지 생성 | Qwen3-VL 은 이미지를 읽고 글을 쓰는 모델이라 이미지를 만들지 못한다. 이미지 생성 모델(예: Stable Diffusion·FLUX + ComfyUI)이 따로 필요하다 |
| vm3 모델의 입력 | ETRI 는 "LLM" 으로 소개했지만 HF 아키텍처가 `Mistral3ForConditionalGeneration` 이라 이미지 입력도 받는 것으로 보인다. 시드에는 `text`, `image` 로 넣고 확인이 필요하다고 남긴다 |
| 서빙 포트를 열지 않는다. 누가 여나 | VM 생성 경로와 함께 보안 그룹 규칙 추가도 지웠다. 노드 배포는 포트를 건드리지 않고, 헬스는 노드 안에서 확인하므로 제한적인 보안 그룹 템플릿으로 만든 Infra 면 배포가 `succeeded` 인데 밖에서 닿지 않는다. Infra 를 만든 쪽(인프라 추천 또는 운영자)이 여는 것으로 README 에 적었지만 합의된 것은 아니다 |
| Semaphore 는 깨진 키에 panic 한다 | Semaphore v2.12.17 은 형식이 깨진 SSH 키를 등록할 때 거절하지 않고, 그 키를 쓰는 태스크에서 프로세스 전체가 panic("close of nil channel", `pkg/ssh/agent.go`)으로 죽는다. 우리는 PEM 개인키 블록인지 먼저 검사해 막는다(`internal/semaphore/run.go` `CheckPrivateKey`). PEM 블록 안의 키 내용까지는 검사하지 않는다 |
| 큰 모델은 시간 상한에 걸릴 수 있다 | 태스크 하나의 상한이 `AIAPP_SEMAPHORE_TIMEOUT` 기본 30분이고, Ollama 모델 pull 대기 `wait_timeout` 기본 1800초도 30분이다. vLLM 은 `/health` 가 가중치를 다 받은 뒤 응답하는데 `compose-up` 의 헬스 대기는 10초 간격 60번(약 10분)이다. 큰 모델을 처음 받는 노드는 이 상한들에 걸릴 수 있다 |
| compose env 가 평문으로 남는다 | 배포 요청의 `env`(예 `HF_TOKEN`)가 배포 기록(`AIAPP_DEPLOYMENT_STORE_FILE`), 배포·제어 아카이브, 공유 저장소의 `.env`, 노드의 `.env` 에 평문으로 남는다. 비밀값을 가리거나 따로 넘기는 경로가 없다 |
| 실측하지 못한 경로 | `install-docker` 의 실제 설치 분기(docker 가 없는 Ubuntu 노드), GPU 분기(NVIDIA Container Toolkit 설치, compose GPU 예약), vLLM 기동은 로컬 검증에서 돌지 않았다(11절) |
| 엔진 대기 상한이 AI-App 쪽 상한보다 짧다 | 엔진의 대기 단계는 `AIAPP_HEALTH_TIMEOUT` 기본 15분에서 멈추는데, AI-App 은 노드마다 `install-docker` 와 `compose-up` 을 각각 최대 30분(`AIAPP_SEMAPHORE_TIMEOUT`)까지 돌린다. 엔진 실행은 실패로 끝났는데 배포는 나중에 성공하는 일이 생긴다 |
| 응용 저장소에 이름 규칙 위반 디렉터리가 있으면 기동 실패 | AI-App 은 기동 때 `AIAPP_APP_STORE_DIR` 을 다시 읽고, 응용 이름 규칙(`[a-z0-9][a-z0-9_-]*`)에 안 맞는 디렉터리나 깨진 버전이 있으면 기동을 거부한다(`internal/appstore/appstore.go`). 손으로 넣은 디렉터리나, `.` 을 이름에 허용하던 때 올린 응용이 남아 있으면 서비스가 뜨지 않는다 |

## 9. 지금 코드와의 관계

| 유지 | 교체·삭제 |
|------|-----------|
| 두 프로세스, MCP 서버·클라이언트, 도구 등급과 dry-run 게이트, 아카이브, Bearer 인증, 협력 기관 모의 두 개 | `AppSpec`·카탈로그(`internal/catalog`) → Model / Engine / App. VM 생성 경로(plan·review·infraDynamic·postCommands·추천 API 호출) 삭제. 서빙 포트 개방, 가속기 실측은 노드 기준으로 옮길 수 있으면 옮기고, 아니면 삭제하고 그렇게 적는다 |

- 모의 인프라 추천(`mock/partner/recommender`)은 AI-App 이 더 부르지 않는다. 협력 기관 계약 예시로 남긴다.
- 모의 자연어 에이전트는 엔진 MCP 도구의 새 입력에 맞춘다.

**되돌리기 어려운 것:** REST·MCP 계약이 바뀐다. 협력 기관 모의는 이 repo 안에 있어 같이 고친다. 외부에서 이미 붙여 둔 곳이
있는지는 확인하지 못했다.

## 10. 하지 않는 것

- 모델 추천과 인프라 추천 로직(경희대·협력 기관 몫), 모델 파일 저장(노드에서 엔진이 출처에서 직접 받는다).
- k8s 대상 배포(자리만 정함), Windows 노드, 포털 배포.
- 실제 클라우드 노드에 배포(이번 작업의 검증은 로컬 컨테이너를 노드 대신 써서 한다).

## 11. 구현 결과 (2026-10-29)

작업별 검증 근거는 계획 진행 기록(`.claude/plan-progress.md`, 저장소에 커밋하지 않는 작업 문서)에서 옮겼다. "구현자" 는 그 작업을
구현한 쪽이 돌려서 보고한 것이고, 팀장이 다시 돌린 것은 따로 적었다.

### 11.1 작업과 커밋

| 커밋 | 작업 | 확인한 것 |
|------|------|-----------|
| `6e6546b` | 이 설계 | |
| `b8d7152` | 모델 카탈로그, 엔진 프로필, compose 생성, REST `/models`·`/engines`, MCP 도구 | build·vet·test 통과. 서버를 띄워 시드 3건, `?modality=image` 필터, 등록 영속, 삭제, 엔진 2건, 못 읽는 조합의 compose `422` |
| `c8d81d9` | 응용 등록(name·files·script, 버전, 디스크 저장), 옛 `/apps` REST 제거 | 서버를 띄워 업로드 v1 → 재업로드 v2, 디스크 저장, 재기동 뒤 다시 읽기, 삭제, 경로 이탈 파일 이름 `400`, 상한 초과 `413` |
| `18280a0` | Tumblebug 노드·SSH 키 조회, Semaphore 클라이언트, playbook 3종 | 실 Semaphore v2.12.17 + sshd 컨테이너로 `copy-and-run` 끝까지 성공, 실행 뒤 남은 key·inventory 0 (구현자) |
| `07eaeb8` | 4.4 갱신: template 은 playbook 마다 고정, key·inventory 는 배포마다 | 같은 실측에서 template 삭제가 그 태스크 이력·로그를 같이 지우는 것, 태스크 요청의 `inventory_id` 가 template 의 inventory 를 덮어쓰는 것 확인 |
| `ec4c302` | 노드 배포 서비스, REST `/deployments`, MCP 도구, dry-run, 공유 저장소 배치, 로그, 노드 제어 | 실 Semaphore + docker-in-docker 노드 컨테이너 + 가짜 CB-Tumblebug 으로 app 배포 `succeeded`, Ollama model-service 배포 `succeeded`(모델 pull 이 끝난 뒤 헬스 통과), dry-run 때 Semaphore 변화 0, 실행 뒤 남은 key·inventory 0 (구현자). 검토 지적(csp 경로 이탈, 종료 순서)과 PEM 검사·Ollama 대기를 고친 뒤 팀장이 build·test·race 재확인 |
| `0027b9b` | 엔진 워크플로를 새 도구로 전환, 모의 자연어 에이전트 입력 맞춤 | 엔진 테스트 22개 race 통과. 로컬 dry-run 으로 `deploy`·`heal`·`status`·`control` 단계 기록 확인, 엔진 카드가 부르는 AI-App 도구 8개 |
| `2ecec08` | 옛 경로 삭제(`AppSpec`·`internal/catalog`·VM 생성·추천 호출·`postCommands`·서빙 포트 개방·가속기 실측·Infra 기준 헬스) | 진행 기록에는 검증 칸이 비어 있다. 2026-10-29 문서 작업 때 `go build ./...`·`go vet ./...`·`go test ./...` 통과와, 테스트를 뺀 Go 코드에서 옛 이름(`AppSpec`, `infraDynamic`, `postCommands`, `internal/catalog`, `probe_accelerator`, `check_serving_health`, `AIAPP_RECOMMENDER_URL`, `AIAPP_PROVISIONER`) grep 0건을 다시 확인했다 |
| `9fc85a0` | 같은 노드 재배포 교체(작업 키로 폴더·compose 프로젝트 고정, 4.5), 응용 이름에서 `.` 거절 | docker-in-docker e2e 로 같은 응용 v1 → v2 재배포 시 v2 응답·컨테이너 1벌, Ollama 재배포 시 컨테이너 1벌 (구현자). 커밋 기준 검토 통과(place-files 경로 이탈 실측 포함) |
| `d5738f1` | 콘솔 화면(모델·엔진·응용·배포), `/aiapp/partner/rec/*` 프록시 | headless Chrome 스크린샷 14장, JS 예외 0, 콘솔이 부르는 경로와 라우트 전수 대조 불일치 0. 진행 기록상 검토는 진행 중이었다 |

### 11.2 확인한 것

로컬의 실 Semaphore v2.12.17 + docker-in-docker 컨테이너(노드 대신) + 가짜 CB-Tumblebug 으로 아래를 확인했다(구현자 실행).

- app 배포: 업로드한 파일 복사, script 실행, `succeeded`
- Ollama model-service 배포: compose 생성 → `up` → `model-pull` 종료 대기 → 헬스 통과 → `succeeded`
- 같은 노드 재배포: 앞 배포를 바꾸고 컨테이너는 1벌
- dry-run: 노드 조회와 파일 배치만 하고 Semaphore 에 등록된 것이 늘지 않음
- 실행 뒤 Semaphore 에 남은 key·inventory 0, 태스크 이력과 로그는 남음

### 11.3 확인하지 못한 것

| 항목 | 왜 |
|------|----|
| 실제 클라우드 노드(CB-Tumblebug 이 만든 aws·azure·gcp VM)에 배포 | 사용자 지시로 이번 작업에서 하지 않았다. 노드 정보·SSH 키 조회는 가짜 CB-Tumblebug 으로만 확인했다 |
| vLLM 기동과 GPU 분기 | 로컬에 GPU 가 없다. `install-docker` 의 NVIDIA Container Toolkit 설치, compose GPU 예약, vLLM 모델 로드와 `/health` 는 돌지 않았다 |
| `install-docker` 의 실제 설치 분기 | 검증 노드가 docker 를 가진 docker-in-docker 컨테이너였고, 진행 기록에 docker 가 없는 Ubuntu 노드에서 설치 분기를 돌린 근거가 없다 |
| MCP 를 거친 `create_deployment` 끝까지의 배포 | 진행 기록에 MCP(엔진 워크플로 포함)로 실제 배포를 끝까지 돌린 근거가 없다. 엔진 워크플로는 dry-run 단계 기록까지만 확인했다 |
| 콘솔 인프라 추천 화면의 정상 추천 후보 | 후보는 모의 추천 API 가 CB-Tumblebug 스펙·이미지 카탈로그를 읽어야 나오는데, 진행 기록에 후보가 나온 화면의 근거가 없다 |

### 11.4 설계와 달라진 것

- 6절: 헬스를 엔진 단계에서 빼고 `compose-up` 의 `health_url` 대기로 판정, `status` 에서 노드 상태·헬스를 뺐다.
- 7절의 Ollama 아티팩트는 시드에서 ID 가 `ollama-gguf` 이고 `precision`·`sizeGiB` 를 비웠다. 태그 페이지에 양자화가 안 적혀 있고,
  6.2GB 는 반올림한 10진 값이라 GiB 로 바꾸면 없는 정밀도가 생기기 때문이다(`internal/modelcat/seed.go`).
- 9절에서 "노드 기준으로 옮길 수 있으면 옮긴다" 고 한 서빙 포트 개방과 가속기 실측은 옮기지 않고 지웠다. 서빙 포트는 8절의 열린 문제로 남겼다.
- docker compose 는 등록한 모델과 업로드한 응용을 named volume `catalog`(`/app/data/catalog`)에 둔다. 컨테이너를 다시 만든 뒤에도 남는 것을 이미지를 빌드해 확인했다.
