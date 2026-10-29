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
| `name` | 응용 이름. 같은 이름으로 다시 올리면 새 `version` |
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

### 4.5 dry-run

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

| 워크플로 | 바뀐 뒤 |
|----------|---------|
| `deploy` | 입력이 4.2 의 배포 요청. 대상 확인 → 계획(compose·파일 배치) → 실행 → 완료 대기 → 헬스 |
| `heal` | 헬스 확인 → 실패면 같은 배포 요청을 한 번 다시 실행 → 다시 확인 |
| `control` | 배포의 노드에 Tumblebug 노드 제어 |
| `status` | 노드 상태 + 배포 기록 + 헬스 |

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
