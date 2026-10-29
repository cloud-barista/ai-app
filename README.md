# ai-agent-proto - AI-App 서비스와 AI 응용 관리 자동화 엔진 (이노그리드 담당 영역 프로토타입)

> AI-MCMP 과제에서 ㈜이노그리드가 맡은 영역의 프로토타입입니다.
>
> - **AI-App 서비스**: AI 모델 카탈로그(모델·아티팩트·엔진 프로필), AI 응용 등록과 버전 관리,
>   CB-Tumblebug 이 이미 띄운 노드에 모델 서비스·응용을 배포(Semaphore 경유), 노드 제어, 배포 기록과 로그
> - **AI-Ops AI 응용 관리 자동화 에이전트의 자동배포/자동제어 엔진**: 정해진 순서로 AI-App 기능을 부르는 엔진. **LLM 을 쓰지 않습니다.**
>
> **자연어 지원 에이전트(LLM)와 인프라 추천은 협력 기관 담당 영역입니다.** 이 저장소에는 연동을 확인하려고 둔
> **모의 구현**만 `mock/partner/` 아래에 있고, 이노그리드 프로토타입에 들지 않습니다.
>
> **AI-App 은 VM 을 만들지 않고 서빙 포트도 열지 않습니다** (3차 개편). Infra 생성과 포트 개방은 Infra 를 만드는 쪽
> (인프라 추천 또는 운영자)이 합니다. 자세한 것은 5-6 과 "개편 이력과 지워진 것" 에 있습니다.

## 구성

| 담당 | 프로세스 (바이너리) | 소스 | 포트 | 맡는 것 |
|---|---|---|---|---|
| 이노그리드 | `ai-app-server` | `cmd/ai-app-server` | `8090` | 모델 카탈로그(`/aiapp/models`, `/aiapp/engines`), 응용 등록·버전(`/aiapp/apps`), 노드 배포·제어(`/aiapp/deployments`), 도구 10개와 dry-run 게이트, **MCP 서버(`/aiapp/mcp`)**, 운영자 콘솔, 배포·제어 아카이브 |
| 이노그리드 | `ai-automation-engine` | `cmd/ai-automation-engine` | `8091` | 자동배포/자동제어 엔진. `deploy`·`heal`·`control`·`status` 워크플로, 엔진 도구 5개의 **MCP 서버(`/aiapp/engine/mcp`)**, 엔진 실행 기록, 엔진 카드 |
| 협력 기관 · **모의** | `mock-partner-recommender` | `mock/partner/recommender` | `8092` | 인프라 추천 API 계약(`POST /recommendations/specs`, `POST /recommendations/image`)의 모의 구현 |
| 협력 기관 · **모의** | `mock-partner-nl-agent` | `mock/partner/nlagent` | `8093` | 자연어 지원 에이전트의 모의 구현. LLM 이 지시를 해석해 엔진 MCP 도구를 부름 |
| 외부 (선택) | Semaphore UI `v2.12.17` | `docker-compose.yml` 의 `semaphore` (`deploy` 프로필) | `127.0.0.1:3001` | AI-App 이 등록한 노드에 `deploy/ansible` playbook 을 SSH 로 돌림. 노드 배포에만 필요 |

```
[협력 기관 · 모의] 자연어 지원 에이전트 (LLM)       mock/partner/nlagent       :8093
     │ MCP (엔진 도구 5개)
     ▼
[이노그리드] 자동배포/자동제어 엔진 (LLM 없음)      cmd/ai-automation-engine   :8091
     │ MCP (AI-App 도구 10개)
     ▼
[이노그리드] AI-App 서비스                          cmd/ai-app-server          :8090
     ├─▶ CB-Tumblebug     노드·SSH 키·스펙 조회, 노드 제어 (VM 생성은 하지 않음)
     └─▶ Semaphore ──SSH──▶ 노드   install-docker → compose-up / copy-and-run
```

- 이노그리드 바이너리 둘은 LLM 을 쓰지 않습니다. 자연어 지시가 없어도 엔진에 구조화된 요청
  (`POST /aiapp/engine/workflows/{workflow}`)을 보내면 배포부터 확인까지 전 과정이 돕니다.
- 콘솔과 운영자는 AI-App 주소(`8090`) 하나만 알면 됩니다. AI-App 이 `/aiapp/engine/*` 을 엔진(`AIAPP_ENGINE_URL`)으로,
  `/aiapp/partner/nl/*` 을 모의 자연어 에이전트(`AIAPP_MOCK_NL_URL`)로 경로 그대로 넘깁니다.
  `/aiapp/partner/rec/*` 은 앞의 `/aiapp/partner/rec` 를 떼고 모의 추천 API(`AIAPP_PARTNER_REC_URL`)로 넘깁니다.
  AI-App 자신은 추천 API 를 부르지 않고, 콘솔이 협력 기관 계약을 시험해 보는 길로만 둡니다.
- **dry-run 게이트는 AI-App 쪽에 있으므로** 엔진이든 모의 에이전트든 어떻게 부르든 게이트를 건너뛸 수 없습니다.
- 모의 구현은 같은 Go 모듈에 있지만 이노그리드 패키지(`cmd/`, `internal/`)는 `mock/` 를 import 하지 않습니다.
  `internal/boundary/boundary_test.go` 가 이것을 검사합니다.
- 모의 구현은 로그·응답·콘솔에 `[partner mock]` 또는 "협력 기관 담당 영역 · 모의 구현" 으로 표시됩니다.

설계와 그 근거는 `docs/design/model-catalog-and-node-deploy.md`(3차 개편, 지금 구조)와
`docs/design/app-agent-split.md`(2차 개편, 담당 경계)에 있습니다.

## 협력 기관이 할 일 (연동 지점)

이 저장소의 모의 구현은 아래 계약이 실제로 돌아가는지 확인하려는 것이고, 실제 구현은 협력 기관이 합니다.

| 연동 지점 | 지금 이 저장소에 있는 것 | 협력 기관이 실제로 할 것 |
|---|---|---|
| 모델 추천 | 판단 재료인 모델 메타데이터를 원래 값 그대로 내줌: `GET /aiapp/models` (필터 `task`, `modality`, `format`, `engine`, `source`) | 정밀도·크기로 필요 자원 계산, 모델 선택 (경희대) |
| 인프라 추천과 Infra 생성 | 제안 계약 `POST /recommendations/specs`, `POST /recommendations/image` (요청의 `app` 은 `mock/partner/recommender/app.go`). 모의 구현 `mock/partner/recommender`. AI-App 은 부르지 않고 `/aiapp/partner/rec/*` 로 넘기기만 함 | 요구 분석, 추천 알고리즘, 추천 근거, CB-Tumblebug 으로 Infra 생성 |
| 서빙 포트 개방 | 없음. AI-App 의 노드 배포는 보안 그룹 규칙을 건드리지 않음 (5-6) | Infra 를 만들 때 모델 서비스·응용 포트(예 vLLM `8000`, Ollama `11434`)를 연다 |
| 자연어 지원 에이전트 | 엔진 MCP 도구(`/aiapp/engine/mcp`)를 부르는 MCP 클라이언트 + Anthropic Messages 형식 LLM. 모의 구현 `mock/partner/nlagent` | LLM 선정·실행(OpsLLM), 자연어 해석 |
| 에이전트 등록 | 엔진이 `GET /aiapp/engine/card` 로 기술서를 내고, `AIAPP_ENGINE_REGISTRY_URL` 을 주면 기동할 때 `POST {registry}/agents` 로 등록 | 에이전트 등록 관리 체계 |
| 배포/운영 정보 기반 추론 최적화 | 입력 데이터인 엔진 실행 기록(JSON Lines, `GET /aiapp/engine/runs`)과 노드 배포 기록(`GET /aiapp/deployments`) | 추론 최적화 |

## 개편 이력과 지워진 것

1차년도 프로토타입은 한 프로세스였습니다. 2026-10 1차 개편에서 AI-App 서비스와 LLM 에이전트를 MCP 로 나눴고,
2차 개편에서 자연어 해석(LLM)과 인프라 추천을 협력 기관 담당 영역의 모의 구현으로 옮기고, 그 자리에 LLM 없는
자동배포/자동제어 엔진을 두었습니다. 3차 개편(2026-10-29, `docs/design/model-catalog-and-node-deploy.md`)에서
데이터 모델을 모델·아티팩트·엔진 프로필·응용으로 다시 나누고, 배포 경로를 "VM 을 만들면서 설치 명령 실행" 에서
"이미 있는 노드에 Semaphore 로 배포" 로 바꿨습니다. 아래 이름은 **지금 코드에 없습니다.**

| 지워진 것 | 지금 |
|---|---|
| `cmd/ai-agent` (자동화 에이전트, `8091`) | `8091` 은 `cmd/ai-automation-engine`. LLM 반복 루프는 `mock/partner/nlagent` (`8093`, 모의) |
| `internal/agent`, `internal/llm`, `internal/agentcard` | `mock/partner/nlagent/agent`, `.../llm`, `.../card` (모의) |
| `internal/deploy` 의 스펙·이미지 추천 로직 | `mock/partner/recommender/spec.go`, `image.go` (모의) |
| `POST /aiapp/ns/{nsId}/intents` | `POST /aiapp/partner/nl/ns/{nsId}/intents` (모의) |
| `/aiapp/agent/readyz`, `/aiapp/agent/card`, `/aiapp/agent/archive` | 엔진 `/aiapp/engine/readyz`·`card`·`runs`, 모의 에이전트 `/aiapp/partner/nl/readyz`·`card`·`runs` |
| `AIAPP_AGENT_PORT`, `AIAPP_AGENT_URL`, `AIAPP_AGENT_MAX_STEPS`, `AIAPP_AGENT_REGISTRY_URL`/`_TOKEN`, `AIAPP_AGENT_ARCHIVE_DIR` | `AIAPP_ENGINE_*` (엔진), `AIAPP_MOCK_NL_*` (모의 에이전트) |
| `AIAPP_RECOMMENDER=local\|remote`, `AIAPP_RECOMMENDER_URL`/`_TOKEN`, `AIAPP_PROVISIONER`/`_URL`/`_TOKEN`, Provisioner 계약 | 읽지 않습니다. AI-App 은 노드를 고르지도 만들지도 않습니다 |
| 옛 응용 카탈로그(`AppSpec`, `internal/catalog`, 시드 4개)와 도구 `list_ai_apps`·`find_ai_apps`·`get_ai_app` | 모델 카탈로그 `/aiapp/models`·`/aiapp/engines` (도구 `list_models`·`get_model`·`list_engines`), 응용 `/aiapp/apps` (도구 `list_apps`·`get_app`) |
| 규칙 매처(`internal/catalog/resolve.go`), `GET /aiapp/apps/search`, 콘솔 "자연어 매칭" 화면, 엔진 `list_applications` 의 `query` | 없습니다. `list_applications` 는 `task`·`modality`·`format`·`engine`·`source` 필터만 받습니다 |
| **VM 생성 경로**(`internal/deploy`): `/aiapp/ns/{nsId}/deployments...`, `infraDynamicReview`/`infraDynamic` 호출, `postCommands` 로 설치, `infraName` 중복 차단(409), 도구 `plan_deployment`·`review_deployment`·`deploy_ai_app`·`update_deployment`·`control_deployment`·`delete_deployment`·`list_deployments`·`get_deployment_status`·`recommend_accelerator_spec`·`resolve_node_image` | **AI-App 은 VM 을 만들지 않습니다.** CB-Tumblebug 이 이미 띄운 노드에 배포: `/aiapp/deployments...` (도구 `create_deployment`·`get_deployment`·`list_node_deployments`·`get_deployment_logs`·`control_node`) |
| **서빙 포트 개방**: 생성 직후 노드 보안 그룹에 `serving.port` 인바운드 규칙 추가 | **하지 않습니다.** 노드 배포 뒤 포트는 Infra 를 만든 쪽(인프라 추천 또는 운영자)이 열어야 합니다 (5-6) |
| **가속기 실측**: `GET .../{infraId}/accelerator`, 도구 `probe_accelerator`, PCI 버스 + 벤더 도구 7종 판독(`internal/deploy/accelerator*.go`) | **없습니다.** 노드 배포는 CB-Tumblebug 노드 스펙의 `acceleratorType` 으로 GPU 여부만 정합니다 (5-4) |
| **Infra 기준 헬스**: `GET .../{infraId}/health`, 도구 `check_serving_health`, `waitHealthy`, `POST /ns/{ns}/cmd/infra/{infra}` 로 노드 안 curl | **없습니다.** 모델 서비스 헬스는 `compose-up` playbook 이 노드 안에서 `health_url` 을 기다려 판정합니다 (5-5) |
| 엔진 `deploy` 입력 `appId`·`deploymentName`·`specId`·`imageId`, `requires` 로 모델 서비스 먼저 배포 | 입력이 노드 배포 요청 그대로입니다 (3-2) |

아래 10절(10-1 ~ 10-8)·12절의 실측 기록은 3차 개편 전, VM 을 만들던 시절에 잰 것이라 그대로 두었습니다.
그때의 배포 경로 `/aiapp/ns/{nsId}/deployments...` 는 지금 없습니다 (위 표).

## 1. 이게 뭘 하는 건가 - 한 문장

> **AI 모델(모델·아티팩트·추론 엔진)과 AI 응용(파일 + 스크립트)을 등록·버전 관리하고, CB-Tumblebug 이 이미 띄운
> 노드에 Semaphore 로 모델 서비스나 응용을 올리고, 자동배포/자동제어 엔진이 정해진 순서로 그 기능을 불러
> 배포·재배포·제어·상태 확인을 하며, 그 과정을 전부 기록하는 Go 서비스.**

```
외부 요청 ─▶ [모델 추천, 경희대 몫] ── GET /aiapp/models ──▶ [AI-App: 모델 카탈로그]
                   │ 고른 모델·아티팩트
                   ▼
            [인프라 추천, 협력 기관 몫] ─▶ Tumblebug Infra 요청 후보
                   │
                   ▼
            [CB-Tumblebug] Infra 생성 (노드)  + 서빙 포트 개방   <- AI-App 이 하지 않음
                   │ nsId / infraId / nodeId
                   ▼
[AI-App] POST /aiapp/deployments
   1. CB-Tumblebug 에서 노드 정보·SSH 키·스펙 조회
   2. 공유 저장소(stage)에 파일 배치. 모델 서비스는 compose 파일을 만들어 넣음
      ── dry-run 이면 여기서 멈춤 ──
   3. Semaphore 에 그 노드의 key·inventory 등록, playbook 태스크 실행
   4. 태스크 로그를 배포 기록에 남기고 key·inventory 삭제
                   │
                   ▼
[Semaphore] ── SSH ─▶ 노드 /opt/aiapp/<작업 키>
   ├ 모델 서비스: install-docker → compose-up (엔진 + 모델, 헬스 대기)
   └ 응용:       install-docker → copy-and-run (업로드한 파일 복사, script 실행)
```

## 2. 왜 이렇게 만들었나 - 설계 결정

1차년도 결정의 근거는 과제 산출물인 기술 조사 문서의 사례 조사에 있고, 그 문서는 이 저장소 밖에 있습니다.
3차 개편 결정의 근거는 `docs/design/model-catalog-and-node-deploy.md` 에 있습니다.

| # | 결정 | 근거 |
|---|---|---|
| 1 | 계층을 넷으로 자름 (카탈로그 / 도구 / 엔진 / 아카이브) | 컨소시엄 담당 경계가 아직 유동적이라, 어느 쪽이 넘어가도 계층 하나만 잘리게 |
| 2 | 엔진은 **정해진 순서(워크플로)** 로 돌고 다음 호출을 LLM 이 정하지 않음 | 배포는 체인이지만 분기(실패면 같은 요청으로 한 번 다시 배포)는 미리 정해 둘 수 있다. 자연어 해석과 LLM 반복 루프는 협력 기관 담당이라 모의 쪽에 있다 |
| 3 | **포맷·정밀도·호환 엔진은 모델이 아니라 아티팩트에** 둠 (3차) | 같은 모델이 출처마다 다른 형태로 배포된다(HF 의 BF16 safetensors, Ollama 의 GGUF). 모델에 두면 엔진·포맷마다 항목을 통째로 복사해야 한다 |
| 4 | **VM 을 만들지 않고 이미 있는 노드에 Semaphore 로 배포** (3차) | `postCommands` 는 VM 을 만들 때 한 번만 돌아 이미 있는 노드에 배포할 수 없고, 파일을 넘길 통로와 단계별 로그가 없었다 |
| 5 | **쓰기는 기본적으로 실행 안 됨** (dry-run 기본 ON) | AWS MCP 도 쓰기를 안 열었고, CB-Tumblebug MCP 는 스스로 PoC 경고 |
| 6 | **노드 위 폴더와 compose 프로젝트를 배포 ID 가 아니라 작업 키로** 고정 (3차) | 같은 노드에 같은 엔진·응용을 다시 배포하면 옛 컨테이너가 포트를 쥔 채 남아 새 컨테이너가 뜨지 못했다 (5-3) |
| 7 | **설정 없이 기동** | 클라우드에 안 닿는 기능이 대부분인데, 설정 하나 없다고 그 전부를 못 쓰게 할 이유가 없다 |
| 8 | **담당 경계대로 자름** (이노그리드 구현 / 협력 기관 모의) | 협력 기관 담당 영역을 이노그리드가 구현한 것처럼 보이지 않게. 모의 구현은 `mock/partner/` 에만 두고 이노그리드 코드가 import 하지 않음 |

## 3. 구조

```
   자연어 지시 (협력 기관 · 모의)
POST /aiapp/partner/nl/ns/{ns}/intents  [ mock/partner/nlagent ]          :8093
                                          LLM 이 엔진 도구를 고르는 반복 루프
                                               │
  - - - - - - - - - - - - - - - - - - - - - - -│- MCP  /aiapp/engine/mcp  (엔진 도구 5개) - -
                                               v
   구조화된 요청 (이노그리드)             [ internal/engine ]                cmd/ai-automation-engine :8091
POST /aiapp/engine/workflows/{workflow} ─▶  deploy · heal · control · status
                                          단계마다 도구·입력·결과·게이트 차단 여부를 기록
                                               │  [ internal/mcpclient ]
  - - - - - - - - - - - - - - - - - - - - - - -│- MCP  /aiapp/mcp  (AI-App 도구 10개) - - - -
                                               v
                                         [ internal/mcpserver ]            cmd/ai-app-server :8090
                                               v
                                         [ internal/tools ]
                                          등급(read/review/write/destructive)별 게이트
                                               │
        ┌──────────────────────┬───────────────┼─────────────────────┐
        v                      v               v                     v
[ internal/modelcat ]   [ internal/appstore ] [ internal/nodedeploy ] [ internal/archive ]
 모델·아티팩트·엔진 프로필  응용 이름·파일·script  노드 조회 -> stage 배치    JSON Lines 레코드
[ internal/compose ]     버전, 디스크 저장       -> Semaphore 실행
 모델 서비스 compose 생성                        -> 기록·로그
                                                │                │
                                                v                v
                                    [ internal/tumblebug ]  [ internal/semaphore ]
                                     노드·SSH 키·스펙 조회,    key·inventory 등록, 태스크
                                     노드 제어                 실행·폴링·로그, 정리
                                                │                │ playbook: deploy/ansible
                                                v                v
                                        CB-Tumblebug REST    Semaphore ── SSH ──▶ 노드
```

`internal/rest` 는 Echo 라우팅과 엔진·모의 에이전트·모의 추천 API 로 넘기는 프록시이고, `internal/registration` 은 엔진 카드를
에이전트 레지스트리에 등록합니다. `internal/config` 는 네 프로세스가 같이 쓰는 설정 로더, `internal/redact` 는 로그·응답에서
비밀값을 가리는 곳입니다. 점선은 프로세스 경계입니다.

모의 자연어 에이전트의 계획 모델은 `AIAPP_LLM_BASE_URL` 에 `/v1/messages` 를 붙여 부르므로 **Anthropic Messages
형식을 받는 엔드포인트면 무엇이든** 됩니다. Ollama 도 0.15 부터 `/v1/messages` 를 받아서 그대로 붙습니다
(2026-10-07, 1차 개편 때의 에이전트로 `glm-4.7-flash` 시연). `AIAPP_LLM_API_KEY` 가 비어 있으면 모의 에이전트가
계획 모델이 없다고 보고 지시 경로에 503 을 내므로, Ollama 처럼 키를 보지 않는 곳에도 아무 값이나 넣습니다.

### 3-1. 왜 도구 계층이 따로 있나

REST 로 직접 배포하든, 엔진이 MCP 로 부르든, 모의 에이전트가 엔진을 거쳐 부르든 **같은 서비스 계층**을 씁니다.
그래서 누가 부르든 동작과 게이트가 같습니다.
도구 목록은 `GET /aiapp/tools` 와 MCP 서버(`/aiapp/mcp`)로 노출되므로, 나중에 AI-Ops 프레임워크 골격이
협력 기관 쪽 것으로 정해져도 그 위에 같은 도구를 다시 얹을 수 있습니다.

### 3-2. 자동배포/자동제어 엔진의 워크플로

엔진(`internal/engine`)은 워크플로마다 정해진 단계 목록을 AI-App 도구 호출로 실행합니다. 모델을 쓰지 않습니다.

| 워크플로 | 입력 | 하는 일 |
|---|---|---|
| `deploy` | 노드 배포 요청 그대로 (`type`, `targets[]`, `modelService` 또는 `app`, 5-1) | 모델(`get_model`) 또는 응용(`get_app`)을 읽음 → `create_deployment` → 배포가 끝날 때까지 `get_deployment` 로 대기. 모든 노드가 `succeeded` 여야 성공이고, `partial` 은 실행 실패 |
| `heal` | `deploymentId` | 배포 기록을 읽어 `succeeded` 면 아무것도 안 함, `pending`·`running` 이면 실패로 끝냄. 그 밖(`failed`·`partial`·`interrupted`·`dry-run`)이면 기록된 요청을 **새 배포로 한 번 다시 실행**하고 끝날 때까지 대기 |
| `control` | `deploymentId`, `action` | `suspend`·`resume`·`reboot`·`terminate` 를 배포의 노드 전부에 (`control_node`) |
| `status` | `deploymentId` | 배포 기록(`get_deployment`)과 노드마다 마지막 태스크 출력 5줄(`get_deployment_logs`). CB-Tumblebug 노드 상태는 읽지 않음 |

- **헬스는 엔진 단계가 아닙니다.** 모델 서비스는 `compose-up` playbook 이 노드 안에서 엔진의 헬스 경로를 기다려,
  응답해야 그 노드가 `succeeded` 가 됩니다 (5-5). 엔진의 대기 단계는 그 결과를 읽습니다.
- 대기 단계는 `AIAPP_HEALTH_INTERVAL`(기본 `20s`) 간격으로 `AIAPP_HEALTH_TIMEOUT`(기본 `15m`)까지 묻습니다. 넘으면 그 단계가
  실패하고, 배포 자체는 AI-App 에서 계속 돕니다(`application_status` 로 이어서 봄).
- 단계 상태는 `succeeded`·`failed`·`blocked`·`waiting`·`skipped`, 실행 전체 상태는 `succeeded`·`failed`·`blocked` 입니다.
- 쓰기 단계가 dry-run 게이트에 막히면 그 단계를 `blocked` 로, 그 결과가 있어야 하는 다음 단계를 `waiting` 으로 남깁니다.
  실제로 실행한 것처럼 보이지 않게 하려는 것입니다.
- 단계가 실패한 실행도 `200` 으로 돌려주고 `status: failed` 를 답니다.
- 쿼리 `ns` 는 실행 기록의 `nsId` 에만 남습니다. 노드 배포의 네임스페이스는 `targets[].nsId` 입니다.
- 엔진은 같은 워크플로를 MCP 도구로도 엽니다(6-1). 모의 자연어 에이전트는 이 도구만 부릅니다.

### 3-3. 인프라 추천의 위치

- AI-App 은 추천을 **하지 않고 추천 API 도 부르지 않습니다.** 배포 요청에 CB-Tumblebug 이 이미 띄운 노드를 지정합니다.
- 1차년도에 쓰던 스펙·이미지 추천 로직은 `mock/partner/recommender` 로 옮겨 모의 추천 API 가 되었고, 협력 기관 계약 예시로 남아 있습니다.
  그 요청의 `app` 모양(`mock/partner/recommender/app.go`: `id`, `accelerator`, `resources`, `install`)은 지워진 옛 `AppSpec` 에서
  추천에 쓰던 필드만 남긴 것이고, 지금의 Model·App 과는 이어져 있지 않습니다.

## 4. 데이터 모델

정의는 `internal/model/aimodel.go`(Model, Artifact), `internal/model/engine.go`(EngineProfile), `internal/model/appbundle.go`(App),
`internal/model/nodedeploy.go`(배포 요청과 기록)에 있고, 필드 설명은 Swagger 에도 같이 실립니다.

### 4-1. Model 과 Artifact

같은 모델이 출처마다 다른 형태로 배포되므로 포맷은 모델이 아니라 **아티팩트**에 둡니다.

```json
{
  "id": "qwen3-vl-8b",
  "name": "Qwen3-VL-8B-Instruct",
  "family": "qwen3-vl",
  "publisher": "Qwen",
  "parameters": "8B",
  "tasks": ["vision-chat"],
  "inputModalities": ["text", "image"],
  "artifacts": [
    { "id": "hf-bf16", "source": "huggingface", "ref": "Qwen/Qwen3-VL-8B-Instruct",
      "format": "hf", "precision": "BF16", "engines": [{ "engine": "vllm" }] },
    { "id": "ollama-gguf", "source": "ollama", "ref": "qwen3-vl:8b",
      "format": "gguf", "engines": [{ "engine": "ollama" }] }
  ]
}
```

| 필드 | 뜻 |
|---|---|
| `id` | 카탈로그 ID. 소문자·숫자·`.`·`_`·`-`, 63자까지 |
| `tasks`, `inputModalities` | 용도(`chat`, `vision-chat`, `embedding`)와 받는 입력(`text`, `image`). 목록 필터 `task`, `modality` |
| `contextLength`, `license` | 모르면 비움. 0 은 "모름" 이지 "무제한" 이 아님 |
| `artifacts[].source`, `ref` | 출처(`huggingface`, `kaggle`, `ollama`, `docker-model-runner`)와 출처에서의 이름. `ref` 는 compose 명령줄에 들어가므로 공백·따옴표·셸 문자를 거절 |
| `artifacts[].format` | `hf`(safetensors) 또는 `gguf`. 엔진 프로필의 `formats` 와 맞아야 배포됨 |
| `artifacts[].precision` | `BF16`, `FP8`, `Q4_K_M` 처럼 **원래 값 그대로**. 정규화하지 않음 |
| `artifacts[].sizeGiB` | 받을 크기. 모르면 0 |
| `artifacts[].engines[]` | 호환 엔진과 `minVersion`. 포맷만으로 정하지 않고 명시. 첫 번째가 기본 엔진 |

정밀도로 VRAM 을 계산하는 등의 추천 로직은 넣지 않았습니다(모델 추천 쪽 몫).
`POST /aiapp/models` 는 같은 `id` 가 있으면 바꿔 씁니다.

기본 등록 모델 3개 (`internal/modelcat/seed.go`, ETRI 여행 포털 시나리오). `AIAPP_MODEL_STORE_FILE` 이 없을 때만 넣습니다.

| 모델 | 용도 | 아티팩트 | 엔진 |
|---|---|---|---|
| `qwen3-embedding-0.6b` | `embedding` | `hf-bf16` (`Qwen/Qwen3-Embedding-0.6B`, BF16) | `vllm` (`--runner pooling` 으로 기동) |
| `qwen3-vl-8b` | `vision-chat` | `hf-bf16` (`Qwen/Qwen3-VL-8B-Instruct`, BF16), `ollama-gguf` (`qwen3-vl:8b`) | `vllm`, `ollama` |
| `ministral-3-8b` | `chat` | `hf-fp8` (`mistralai/Ministral-3-8B-Instruct-2512`, FP8) | `vllm` |

### 4-2. Engine 프로필

내장이고 조회만 됩니다 (`internal/modelcat/engines.go`).

| `id` | `image` | `formats` | `port` | `healthPath` | `apiBasePath` | compose |
|---|---|---|---|---|---|---|
| `vllm` | `vllm/vllm-openai:v0.32.0` | `hf` | `8000` | `/health` | `/v1` | `--model <ref>` 로 기동, HF 캐시는 named volume, `HF_TOKEN` 은 env 나 `.env` 에서 |
| `ollama` | `ollama/ollama:0.40.13` | `gguf` | `11434` | `/` | `/v1` | 서버가 healthy 가 되면 한 번 도는 `model-pull` 컨테이너가 `ollama pull <ref>` 후 종료 |

GPU 를 쓰면 엔진 서비스에 NVIDIA GPU 전부를 예약하는 `deploy.resources.reservations.devices` 블록이 붙습니다.
`GET /aiapp/models/{modelId}/artifacts/{artifactId}/compose?engine=&gpu=` 로 노드에 갈 compose 를 미리 볼 수 있습니다.

### 4-3. App

AI 응용은 **이름 + 파일 + 노드에서 돌릴 script** 입니다 (`internal/appstore`).

| 필드 | 뜻 |
|---|---|
| `name` | 소문자·숫자·`_`·`-`, 63자까지. **`.` 은 안 됩니다** (노드의 compose 프로젝트 이름이 되는데 compose 가 `.` 을 거절). 같은 이름으로 다시 올리면 새 `version` |
| `versions[].script` | 노드에서 파일들이 있는 폴더에서 `bash` 로 실행. 64 KiB 까지 |
| `versions[].files[]` | 업로드한 파일(이름, 크기, SHA-256). 한 폴더에 평평하게 놓임. 이름은 `.` 으로 시작할 수 없고(`.env` 는 배포 요청이 씀) 256개까지 |

등록은 multipart 한 번입니다 (`name`, `script` 텍스트 또는 파일 파트, `files` 파트 여러 개). 대상 노드와 다른 노드의 주소(env)는
등록이 아니라 **배포 요청**에서 받습니다. 저장은 `AIAPP_APP_STORE_DIR/<name>/<version>/` 아래 `files/`, `script.sh`, `meta.json` 이고
기동 때 다시 읽습니다. **그 폴더에 이름 규칙에 안 맞는 디렉터리가 있으면 기동이 실패합니다** (`internal/appstore/appstore.go`).

예시 번들이 `examples/apps/` 에 있습니다 (ETRI 시나리오의 응용 계층).

| 번들 | 파일 | 배포 요청 `env` |
|---|---|---|
| `examples/apps/open-webui` | `deploy.sh`, `docker-compose.yml` (`ghcr.io/open-webui/open-webui:v0.12.4`) | `OLLAMA_BASE_URL` (필수), `OPEN_WEBUI_PORT` (기본 `3000`) |
| `examples/apps/langflow` | `deploy.sh`, `docker-compose.yml` (`langflowai/langflow:1.13.0`) | `MODEL_BASE_URL`, `LANGFLOW_PORT` (기본 `7860`) |
| `examples/apps/dify` | `deploy.sh` 하나. 노드에서 Dify `1.18.0` 공식 compose 를 `git clone` 해 띄움 (노드에 `git` 필요) | `DIFY_PORT` (기본 `80`), `VLLM_BASE_URL` (Dify 화면에 넣을 값을 출력만 함) |

```sh
curl -F name=open-webui -F script=@examples/apps/open-webui/deploy.sh \
     -F files=@examples/apps/open-webui/docker-compose.yml localhost:8090/aiapp/apps
```

## 5. 노드 배포

`internal/nodedeploy`. 요청을 받으면 모델·아티팩트·엔진 조합(또는 응용 버전)을 먼저 확인하고, 노드마다(한 배포에서 동시에 4개까지) 아래를 합니다.

### 5-1. 요청

```json
{
  "type": "model-service",
  "targets": [{ "kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm2" }],
  "modelService": { "modelId": "qwen3-vl-8b", "artifactId": "ollama-gguf", "engine": "ollama" }
}
```
```json
{
  "type": "app",
  "targets": [{ "kind": "node", "nsId": "etri", "infraId": "travel", "nodeId": "vm2" }],
  "app": { "name": "open-webui", "version": 1, "env": { "OLLAMA_BASE_URL": "http://10.0.0.5:11434" } }
}
```

- `targets` 는 CB-Tumblebug 구조 그대로이고 32개까지, 같은 노드는 한 번만. csp·region·zone 은 요청으로 받지 않고 노드 정보에서 읽습니다.
- `kind: k8s` 는 자리만 있고 `422` 로 거절합니다.
- `modelService.engine` 을 빼면 아티팩트의 첫 엔진, `app.version` 을 빼거나 `0` 이면 최신 버전입니다. 기록에는 고른 값이 채워집니다.
- `modelService.gpu` 를 빼면 노드 스펙에서 정합니다 (5-4).
- `env` 이름은 `[A-Z_][A-Z0-9_]*`, 값에는 줄바꿈과 작은따옴표를 넣을 수 없습니다. 모델 서비스는 엔진 컨테이너 환경과 `.env` 에,
  응용은 `.env` 에 쓰이고 script 실행 전에 export 됩니다.
- 응답: 받으면 `202` 와 `pending` 기록을 돌려주고 뒤에서 진행합니다. dry-run 이면 파일 배치까지 끝낸 기록을 `200` 으로 돌려줍니다.
  없는 모델·아티팩트·엔진·응용은 `404`, 엔진이 못 읽는 아티팩트는 `422` 입니다.

### 5-2. 노드 하나에서 하는 일

| # | 단계 | 비고 |
|---|---|---|
| 1 | CB-Tumblebug 에서 노드 정보와 SSH 키(`privateKey`) 조회 | 공인 IP 가 없으면 사설 IP. 키가 PEM 개인키 블록이 아니면 여기서 실패 (5-7) |
| 2 | GPU 여부 결정 (5-4) | |
| 3 | 공유 저장소에 파일 배치: `<AIAPP_STAGE_ROOT>/<배포 uuid>/<csp>-<nodeId>/` | 모델 서비스는 `docker-compose.yml` + `.env`, 응용은 업로드 파일 + `.aiapp-run.sh`(script) + `.env`. 파일 목록 `.aiapp-files` |
| | **dry-run 이면 여기서 끝** (`dry-run`) | Semaphore 에 아무것도 등록하지 않음 |
| 4 | Semaphore 에 노드의 key·inventory 등록, `install-docker.yml` 실행 | Ubuntu 만. docker 와 compose 플러그인이 있으면 그대로 둠, docker 는 있는데 compose 플러그인이 없으면 실패. GPU 면 NVIDIA Container Toolkit 도 설치 |
| 5 | `compose-up.yml`(모델 서비스) 또는 `copy-and-run.yml`(응용) 실행 | 5-3, 5-5 |
| 6 | 태스크 출력을 배포 기록에 남기고 key·inventory 삭제 | 성공·실패·취소와 무관하게. 태스크와 그 로그는 Semaphore 에 남음 |

- Semaphore 의 template 은 playbook 마다 하나(`aiapp-install-docker`, `aiapp-compose-up`, `aiapp-copy-and-run`)를 프로젝트(`AIAPP_SEMAPHORE_PROJECT`)에
  만들어 두고, 태스크 요청의 `inventory_id` 로 그 배포의 inventory 를 덮어써 실행합니다. template 을 지우면 Semaphore 가 그 태스크 이력과
  로그를 같이 지우기 때문입니다 (설계 4.4).
- 태스크 하나의 상한은 `AIAPP_SEMAPHORE_TIMEOUT`(기본 `30m`)이고, 넘으면 태스크를 멈추고 등록한 것을 지웁니다.
- 기록 상태: 배포는 `pending`·`running`·`succeeded`(전 노드)·`failed`(전 노드)·`partial`(일부)·`dry-run`·`interrupted`(서비스가 멈추거나 재기동),
  노드는 `partial` 을 뺀 같은 값입니다. 노드 결과에 접속 정보(키 제외), `gpu`·`gpuSource`, `stageDir`, `workKey`, `nodeDir`, 렌더한 compose,
  `serviceUrl`(모델 서비스: `http://<노드 IP>:<엔진 port><apiBasePath>`), 단계별 태스크 ID·상태가 남습니다.
- 태스크 출력은 `GET /aiapp/deployments/{id}/logs` 로만 나오고 기록 조회에서는 빠집니다.

### 5-3. 노드 위의 배치 (같은 노드에 다시 배포하면 바뀜)

노드 위의 폴더와 compose 프로젝트는 배포 ID 가 아니라 **작업 키**로 정합니다 (`internal/nodedeploy/placement.go`).

| 종류 | 작업 키 | 노드 폴더 | compose 프로젝트 |
|---|---|---|---|
| 모델 서비스 | `ms-<engine>` (예 `ms-vllm`, `ms-ollama`) | `/opt/aiapp/<작업 키>` | `aiapp-<작업 키>` |
| 응용 | `app-<name>` | `/opt/aiapp/<작업 키>` | `aiapp-<작업 키>` |

- 노드 하나에 모델 서비스는 **엔진당 하나**, 응용은 **이름당 하나**입니다. 같은 작업 키로 다시 배포하면 앞의 것을 바꿉니다.
  엔진이 다른 모델 서비스와 다른 응용은 서로 건드리지 않습니다.
- 파일: 이전 배포의 `.aiapp-files` 에 있고 새 목록에 없는 파일은 지웁니다. 목록에 없는 파일(컨테이너가 bind mount 로 쓴 데이터)과
  docker named volume 은 그대로 둡니다.
- 모델 서비스: `docker compose -p aiapp-<작업 키> up -d --remove-orphans --force-recreate`. named volume 이 프로젝트에 묶여 있어
  받아 둔 가중치를 다음 배포가 다시 씁니다. `--force-recreate` 는 같은 설정으로 다시 배포하는 `heal` 이 unhealthy 컨테이너를 바꾸게 하려는 것입니다.
- 응용: script 전에 `docker compose -p aiapp-<작업 키> down --remove-orphans`(`-v` 없음)로 이전 컨테이너를 내리고,
  `.env` 를 export 하고 `COMPOSE_PROJECT_NAME=aiapp-<작업 키>` 를 export 한 뒤 `bash ./.aiapp-run.sh` 를 실행합니다.
  다시 배포하는 동안 응용이 잠깐 끊깁니다.
- 같은 노드의 같은 작업 키는 서비스 안에서 한 배포씩 돕니다.

### 5-4. GPU 여부

`modelService.gpu` 를 주면 그 값(`gpuSource: request`), 안 주면 노드의 `specId` 를 `AIAPP_TUMBLEBUG_SPEC_NAMESPACE` 에서 읽어
`acceleratorType` 이 `gpu` 인지(`spec`), 스펙을 못 읽으면 `false`(`default`, 이유는 `notes`)입니다. GPU 면 `install-docker` 가
NVIDIA Container Toolkit 을 깔고 compose 에 GPU 예약이 붙습니다. 가속기 실측(PCI·벤더 도구 판독)은 3차 개편에서 지웠습니다.

### 5-5. 헬스

- 모델 서비스: `compose-up` 이 노드 안에서 `curl http://127.0.0.1:<port><healthPath>` 를 10초 간격 60번까지 시도하고, 응답해야 노드가 `succeeded` 입니다.
  Ollama 는 서버가 모델을 받기 전에 응답하므로 `model-pull` 컨테이너가 0 으로 끝나기를 먼저 기다립니다(`wait_timeout` 기본 1800초).
- 응용: script 의 종료 코드가 전부입니다. 응용이 응답하는지는 확인하지 않습니다.
- Infra 기준 헬스 경로(`.../{infraId}/health`, `check_serving_health`)와 엔진의 별도 헬스 단계는 없습니다.

### 5-6. 서빙 포트는 열지 않는다

**노드 배포는 보안 그룹 규칙을 건드리지 않습니다.** 2차 개편까지는 VM 을 만든 직후 `serving.port` 인바운드 규칙을 넣었지만
(10-4, 10-5 의 실측), VM 생성 경로와 함께 지웠습니다. `serviceUrl` 은 노드 IP 와 엔진 포트로 조립한 주소일 뿐, 밖에서 닿는다는 뜻이 아닙니다.

- CB-Tumblebug 기본 보안 그룹 템플릿 `sg-default` 는 TCP/UDP 1-65535 를 전부 엽니다(12절, 2026-09-01 실측). 그 템플릿으로 만든 Infra 면 포트가 이미 열려 있습니다.
- 제한적인 템플릿으로 만든 Infra 면 **Infra 를 만든 쪽(인프라 추천 또는 운영자)이 모델 서비스·응용 포트를 열어야** 합니다. 열지 않으면 배포는
  `succeeded` 인데 밖에서는 닿지 않습니다(헬스는 노드 안에서 확인하기 때문).
- 누가 열지는 정해지지 않았습니다 (설계 8절).

### 5-7. SSH 키 취급

- 키는 배포 기록·응답·로그에 넣지 않고, 오류 문자열에서도 가립니다.
- Semaphore 에 올리기 전에 PEM 개인키 블록인지 봅니다. Semaphore `v2.12.17` 은 깨진 키를 등록할 때 거절하지 않고, 그 키를 쓰는 태스크에서
  프로세스 전체가 panic 으로 죽습니다 (`internal/semaphore/run.go` 의 `CheckPrivateKey` 주석).
- 등록한 key·inventory 는 실행이 끝나면 지웁니다. 지우지 못하면 그 단계의 `cleanupErrors` 에 남습니다. AI-App 을 멈추면 진행 중인 배포가
  태스크를 멈추고 키를 지우는 데 최대 70초를 기다립니다(compose 의 `stop_grace_period: 80s`).

### 5-8. dry-run

`AIAPP_TUMBLEBUG_DRY_RUN=true`(기본)면 5-2 의 1~3 까지만 합니다. 노드 조회와 파일 배치는 하므로 **CB-Tumblebug 에는 닿아야** 하고,
Semaphore 에는 아무것도 등록하지 않습니다. 기록 상태는 `dry-run` 이고, 노드 결과에 만든 compose 와 대상 노드 정보가 담깁니다.
노드 조회가 실패한 노드는 `failed` 이고 그러면 배포도 `failed` 입니다. 노드 제어는 노드마다 `blocked` 를 돌려줍니다.

## 6. 안전장치

### 6-1. 도구 등급

AI-App 도구 10개는 `GET /aiapp/tools` 와 MCP 서버 `/aiapp/mcp` 로 그대로 확인할 수 있습니다.

| 등급 | AI-App 도구 | dry-run 게이트 |
|---|---|---|
| `read` | `list_models`, `get_model`, `list_engines`, `list_apps`, `get_app`, `get_deployment`, `list_node_deployments`, `get_deployment_logs` | 해당 없음. 항상 실행 |
| `write` | `create_deployment`, `control_node` | **걸림**. `create_deployment` 는 dry-run 이면 공유 저장소에 파일만 두고 Semaphore 에 아무것도 안 올린 기록(`dry-run`)을 돌려주고, `control_node` 는 노드마다 `blocked` 를 돌려줌 |

`review`·`destructive` 등급은 도구 계층에 남아 있지만 지금 그 등급의 AI-App 도구는 없습니다.

엔진 도구 5개는 엔진의 MCP 서버 `/aiapp/engine/mcp` 로 나갑니다. 엔진은 이 중 8개 AI-App 도구를 불러 씁니다
(`list_engines`, `list_node_deployments` 는 부르지 않습니다. 엔진 카드의 `tools.uses`).

| 등급 | 엔진 도구 | 하는 일 |
|---|---|---|
| `write` | `deploy_application` | `deploy` 워크플로. 쓰기 단계는 AI-App 의 게이트를 거침 |
| `write` | `heal_application` | `heal` 워크플로. 같음 |
| `write` | `control_application` | `control` 워크플로. 같음 |
| `read` | `application_status` | `status` 워크플로. 아무것도 안 바꿈 |
| `read` | `list_applications` | 모델(아티팩트·엔진 포함)과 업로드한 응용(버전 번호)의 요약. 필터 `task`·`modality`·`format`·`engine`·`source` 는 모델에만 걸림. 워크플로를 돌리지 않음 |

### 6-2. 실행 여부를 호출자가 정하지 않는다

`AIAPP_TUMBLEBUG_DRY_RUN` 은 AI-App 의 **서비스 설정**이지 요청 파라미터가 아닙니다.
엔진이 `deploy` 를 돌려도, 모의 에이전트의 LLM 이 "확실하니 실행하자"고 해도 `create_deployment` 는 파일 배치까지만
하고 멈추고, 엔진은 그 단계를 `blocked` 로 남깁니다.
모의 에이전트의 시스템 프롬프트에는 게이트가 켜져 있다고 알려 주는데, 이것은 **지키라고 부탁하는 게 아니라**
운영자에게 "다 했습니다"라고 잘못 보고하지 않게 하려는 것입니다. 강제는 AI-App 의 도구 계층에서 합니다.

### 6-3. 노드에 넘어가는 값은 받을 때 거른다

노드에서 셸·compose 로 읽히는 값은 이스케이프하지 않고 받을 때 거절합니다.

| 값 | 규칙 | 이유 |
|---|---|---|
| 아티팩트 `ref` | `[A-Za-z0-9][A-Za-z0-9._:/@+-]*` | compose 명령줄에 들어감 |
| 응용 `name` | `[a-z0-9][a-z0-9_-]*`, 63자 | compose 프로젝트 이름(`aiapp-app-<name>`)이 됨 |
| 응용 파일 이름 | 경로 구분자 없음, `.` 으로 시작 안 함 | 노드 폴더에 평평하게 놓이고 `.env`·`.aiapp-*` 와 겹치지 않게 |
| `env` | 이름 `[A-Z_][A-Z0-9_]*`, 값에 줄바꿈·작은따옴표 없음 | `.env` 에 작은따옴표로 쓰여 compose 와 bash 가 그대로 읽음 |
| `targets[].nsId`·`infraId`·`nodeId` | `[A-Za-z0-9][A-Za-z0-9._-]*`, 128자 | 공유 저장소 폴더 이름이 됨 |

## 7. 아카이빙 (1차년도 ⑥ 산출물)

`runs.jsonl` 에 한 줄에 한 레코드씩 append 합니다. 아카이브는 프로세스마다 하나씩, 셋입니다.

| 아카이브 | 쓰는 쪽 | 위치 (기본값) | 무엇이 남나 | 조회 (AI-App 주소) |
|---|---|---|---|---|
| 배포·제어 | AI-App 서비스 | `AIAPP_ARCHIVE_DIR` (`./data/archive`) | 노드 배포가 끝날 때 한 줄(`kind`: `node-deployment`, `runId` 는 배포 ID, 입력은 배포 요청 전체)과 노드 제어 한 번에 한 줄(`node-control`). REST·MCP 어느 쪽으로 불러도 남음 | `GET /aiapp/archive` |
| 엔진 실행 기록 | 자동화 엔진 | `AIAPP_ENGINE_ARCHIVE_DIR` (`./data/engine-archive`) | 워크플로 한 번의 단계 전체 | `GET /aiapp/engine/runs` |
| 지시 실행 (모의) | 모의 자연어 에이전트 | `AIAPP_MOCK_NL_ARCHIVE_DIR` (`./data/partner-nl-archive`) | 자연어 지시 한 건의 반복 루프 전체 | `GET /aiapp/partner/nl/runs` |

노드 배포 기록 자체(노드별 단계·태스크 출력)는 아카이브가 아니라 `AIAPP_DEPLOYMENT_STORE_FILE` 에 있습니다 (8-1, 설정 표).

엔진 실행 기록에는 **최종 결과만이 아니라** 다음이 전부 들어갑니다 (`internal/engine/run.go`).

- 워크플로와 입력(`workflow`, `input`), 그때의 dry-run 상태(`dryRun`)
- 단계마다 부른 AI-App 도구와 **인자 전체**(`steps[].tool`, `steps[].input`)
- 도구 응답(`steps[].output`)과 실패 사유(`steps[].error`)
- **게이트가 막았는지**(`steps[].status` 가 `blocked`, 그 뒤 단계는 `waiting`)와 그 이유(`steps[].message`)
- 단계별 시작·끝 시각

중간 단계를 남기는 이유는 감사 때문만이 아닙니다.
2차년도 항목이 "배포 정보 수집 및 **추론 재적용**"이고, 협력 기관 담당인 배포/운영 정보 기반 추론 최적화의
입력이 이 레코드입니다. 결과만 남기면 왜 그 결정을 했는지가 사라져서 되먹일 수 없습니다.

배포 요청의 `env`(예 `HF_TOKEN`)는 배포 기록·아카이브·공유 저장소 `.env` 에 **평문으로** 남습니다 (설계 8절).

아래는 1차년도 한 프로세스 시절, LLM 에이전트가 이 저장소에 있던 때의 지시 실행 레코드입니다. 도구 이름은 지금 없는 것입니다.

```
run-dl3xxm60p7x7  intent   Llama 3.1 8B 추론 서버를 GPU 한 장짜리 노드에 올려줘
  1. list_ai_apps                 read   executed=true
  2. recommend_accelerator_spec   read   executed=true   {"appId":"vllm-llama31-8b","limit":2}
  3. deploy_ai_app                write  executed=false  <- 게이트가 막음
```

## 8. API

`GET /aiapp/api/index.html` 에서 Swagger UI 로 볼 수 있습니다.
엔진과 모의 자연어 에이전트의 경로도 AI-App 주소(`8090`)로 부르면 AI-App 이 경로 그대로 넘깁니다.
`AIAPP_AUTH_TOKEN` 을 설정하면 `/aiapp/readyz`, `/aiapp/ui`, `/aiapp/api/*`, `/aiapp/engine/readyz`,
`/aiapp/partner/nl/readyz` 를 뺀 전부가 `Authorization: Bearer <토큰>` 을 요구합니다.
넘긴 요청은 호출자의 `Authorization` 헤더를 그대로 달고 가므로 세 프로세스에 같은 토큰을 넣습니다.

### 8-1. AI-App 서비스 (`cmd/ai-app-server`, `8090`)

| 메서드 | 경로 | 하는 일 |
|---|---|---|
| GET | `/aiapp/ui` | **운영자 콘솔** (이 서비스의 API 를 실제로 호출하는 화면) |
| GET | `/aiapp/readyz` | 준비 상태 |
| GET | `/aiapp/api/*` | Swagger UI |
| GET | `/aiapp/models` | 모델 목록 (id 순). 필터 `task`, `modality`, `format`, `engine`, `source` |
| POST | `/aiapp/models` | 모델 등록. 같은 `id` 면 바꿔 씀 |
| GET | `/aiapp/models/{modelId}` | 모델 조회 |
| DELETE | `/aiapp/models/{modelId}` | 모델 삭제 |
| GET | `/aiapp/models/{modelId}/artifacts/{artifactId}/compose` | 그 아티팩트로 만들 모델 서비스 compose 미리 보기 (`engine`, `gpu` 쿼리). 엔진이 못 읽으면 `422` |
| GET | `/aiapp/engines` | 엔진 프로필 목록 |
| GET | `/aiapp/engines/{engineId}` | 엔진 프로필 조회 |
| POST | `/aiapp/apps` | AI 응용 업로드 (multipart: `name`, `script` 텍스트 또는 파일 파트, `files` 파트 여러 개). 같은 이름이면 다음 버전으로 추가. 상한 초과는 `413` |
| GET | `/aiapp/apps` | 업로드한 AI 응용 목록 (버전·script·파일 크기와 SHA-256 포함) |
| GET | `/aiapp/apps/{name}` | AI 응용 조회 (모든 버전) |
| GET | `/aiapp/apps/{name}/versions/{version}` | AI 응용 한 버전 조회 |
| DELETE | `/aiapp/apps/{name}` | AI 응용 삭제 (모든 버전과 저장된 파일) |
| POST | `/aiapp/deployments` | **이미 있는 CB-Tumblebug 노드에 배포** (5절). 받으면 `202`·`pending`, dry-run 이면 `200`·`dry-run`. `kind: k8s` 는 `422` |
| GET | `/aiapp/deployments` | 노드 배포 기록 목록 (최신순, 태스크 출력 제외) |
| GET | `/aiapp/deployments/{id}` | 노드 배포 기록 (5-2) |
| GET | `/aiapp/deployments/{id}/logs` | 노드·단계별 Semaphore 태스크 출력 |
| POST | `/aiapp/deployments/{id}/control` | 배포의 노드 전부에 CB-Tumblebug 노드 제어 (`suspend`/`resume`/`reboot`/`terminate`). dry-run 이거나 dry-run 배포면 `blocked` |
| GET | `/aiapp/tools` | AI-App 도구 10개와 등급, dry-run 상태 |
| (any) | `/aiapp/mcp` | **같은 도구의 MCP 서버** (Streamable HTTP). 엔진이 여기로 도구를 부름 |
| GET | `/aiapp/archive` | 배포·제어 아카이브 목록 (최신순, 기본 20건) |
| GET | `/aiapp/archive/{runId}` | 배포·제어 아카이브 단건 |
| (any) | `/aiapp/engine/*` | 엔진(`AIAPP_ENGINE_URL`)으로 넘김. 아래 8-2 |
| (any) | `/aiapp/partner/nl/*` | 모의 자연어 에이전트(`AIAPP_MOCK_NL_URL`)로 넘김. 아래 8-3 |
| (any) | `/aiapp/partner/rec/*` | 모의 추천 API(`AIAPP_PARTNER_REC_URL`)로 넘김. `/aiapp/partner/rec` 를 떼므로 `/aiapp/partner/rec/recommendations/specs` 는 `/recommendations/specs` 에 닿음 |

### 8-2. 자동화 엔진 (`cmd/ai-automation-engine`, `8091`)

| 메서드 | 경로 | 하는 일 |
|---|---|---|
| GET | `/aiapp/readyz`, `/aiapp/engine/readyz` | 준비 상태 |
| POST | `/aiapp/engine/workflows/{workflow}?ns={nsId}` | 워크플로(`deploy`·`heal`·`control`·`status`) 실행. 입력은 3-2 의 표. `ns` 를 비우면 `default` |
| GET | `/aiapp/engine/runs?limit=` | 엔진 실행 기록 목록 (최신순, 기본 20건) |
| GET | `/aiapp/engine/runs/{runId}` | 엔진 실행 기록 단건 |
| GET | `/aiapp/engine/card` | 엔진 카드: 이름, 담당(`owner`), LLM 사용 여부(`usesLLM`), 워크플로, AI-App MCP 연결 상태, 내보내는 도구(`tools.exposed`)와 부르는 AI-App 도구(`tools.uses`), dry-run 상태, 레지스트리 등록 결과 |
| (any) | `/aiapp/engine/mcp` | 엔진 도구 5개의 MCP 서버 (Streamable HTTP). 모의 자연어 에이전트가 여기로 부름 |

### 8-3. 협력 기관 연동 지점 (모의)

모의 자연어 에이전트 (`mock/partner/nlagent`, `8093`). AI-App 이 넘겨 주므로 AI-App 주소로 부릅니다.

| 메서드 | 경로 | 하는 일 |
|---|---|---|
| GET | `/aiapp/partner/nl/readyz` | 준비 상태 |
| POST | `/aiapp/partner/nl/ns/{nsId}/intents` | **자연어 지시 -> LLM -> 엔진 도구 호출**. 계획 모델이 없으면 `503` |
| GET | `/aiapp/partner/nl/runs` | 지시 실행 기록 목록 |
| GET | `/aiapp/partner/nl/runs/{runId}` | 지시 실행 기록 단건 |
| GET | `/aiapp/partner/nl/card` | 모의 에이전트 카드 |

모의 추천 API (`mock/partner/recommender`, `8092`). AI-App 은 부르지 않고 `/aiapp/partner/rec/*` 로 넘겨 주기만 합니다. 협력 기관 계약 예시입니다.
`AIAPP_MOCK_RECOMMENDER_TOKEN` 을 주면 모든 경로가 bearer 토큰을 요구합니다.

| 메서드 | 경로 | 하는 일 |
|---|---|---|
| POST | `/recommendations/specs` | 요청 `{app, limit}` -> `{candidates, reason}`. `app` 의 모양은 `mock/partner/recommender/app.go` |
| POST | `/recommendations/image` | 요청 `{app, specId}` -> `{imageId, source, reason}` |

## 9. 실행 방법

### 9-1. 설정 없이 바로

이노그리드 프로세스 둘에 모의 둘, 터미널 넷입니다. 엔진은 AI-App 의 MCP 서버에서, 모의 자연어 에이전트는 엔진의
MCP 서버에서 도구를 받아 오므로 이 순서로 띄웁니다. 먼저 뜬 쪽이 없어도 기동은 하고, 경고를 남긴 뒤 호출 때마다 다시 붙습니다.

```sh
make run-app                # AI-App 서비스, :8090
make run-engine             # 자동화 엔진, :8091 (다른 터미널)
make run-mock-recommender   # 협력 기관 추천 API 모의, :8092 (선택)
make run-mock-nl            # 협력 기관 자연어 에이전트 모의, :8093 (선택)
open http://localhost:8090/aiapp/ui
```

바이너리가 필요하면 `make build` 가 `bin/` 아래에 넷을 만듭니다:
`ai-app-server`, `ai-automation-engine`, `mock-partner-recommender`, `mock-partner-nl-agent`.

**끝입니다.** env 파일을 만들 필요가 없습니다. 모든 설정에 기본값이 있습니다.
아래는 설정 없이 AI-App 과 엔진을 띄웠을 때의 기동 로그입니다 (2026-10-29, JSON 로그를 줄여 옮김. 포트만 바꿔 띄워서 그 값은 기본값으로 고쳐 적음).
모의 둘의 로그는 2026-10-13 에 받은 것입니다.

```
# ai-app-server
INF Seeded AI model catalog  count=3
WRN AIAPP_MODEL_STORE_FILE is not set: the AI model catalog is kept in memory and registered
    models are lost on restart
INF Loaded AI application store  dir=.../data/apps apps=0 versions=0 maxUploadBytes=268435456
WRN AIAPP_DEPLOYMENT_STORE_FILE is not set: node deployment records are kept in memory and lost on restart
WRN AIAPP_SEMAPHORE_URL is not set: deployments onto nodes fail when they reach Semaphore, unless dry run is on
WRN CB-Tumblebug base URL was not configured, so deployment calls will not reach a real
    platform  url=http://localhost:1323/tumblebug source=default
WRN Dry run is on: node deployments place their files and stop before Semaphore, and node
    control calls are blocked
WRN AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open
INF Starting server port=8090 basePath=/aiapp tools=10 engineUrl=http://127.0.0.1:8091
    partnerNlUrl=http://127.0.0.1:8093 partnerRecUrl=http://127.0.0.1:8092

# ai-automation-engine
WRN AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open
INF Connected to the MCP server  endpoint=http://127.0.0.1:8090/aiapp/mcp tools=10 dryRun=true
INF AIAPP_ENGINE_REGISTRY_URL is not set: the engine card is not registered anywhere
INF Starting automation engine; it uses no LLM  port=8091 workflows=[deploy,heal,control,status] tools=5

# mock-partner-recommender (2026-10-13)
INF Partner institution recommender (mock implementation) starting; not part of the Innogrid prototype  port=8092
WRN CB-Tumblebug base URL was not configured, so recommendations will not reach a real platform

# mock-partner-nl-agent (2026-10-13)
WRN [partner mock] Planning model is not configured: the intent endpoint will report 503
WRN AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open
INF Connected to the MCP server  endpoint=http://127.0.0.1:8091/aiapp/engine/mcp tools=5 dryRun=true
INF AIAPP_MOCK_NL_REGISTRY_URL is not set: the agent card is not registered anywhere
INF [partner mock] Partner institution natural language agent (mock implementation) starting;
    not part of the Innogrid prototype  port=8093 prefix=/aiapp/partner/nl model=claude-opus-5
```

이 상태에서 **클라우드에 닿지 않는 것은 전부 동작합니다.**

```sh
open http://localhost:8090/aiapp/ui                       # 운영자 콘솔 (아래 9-5)
curl localhost:8090/aiapp/readyz                          # {"message":"Service is ready"}
curl localhost:8090/aiapp/engine/readyz                   # 엔진 준비 상태 (AI-App 이 넘겨줌)
curl localhost:8090/aiapp/partner/nl/readyz               # 모의 자연어 에이전트 준비 상태 (AI-App 이 넘겨줌)
curl localhost:8090/aiapp/models                          # 기본 모델 3개
curl 'localhost:8090/aiapp/models?modality=image'         # 이미지 입력을 받는 모델만
curl localhost:8090/aiapp/engines                         # 엔진 프로필 vllm, ollama
curl localhost:8090/aiapp/models/qwen3-vl-8b/artifacts/ollama-gguf/compose   # 노드에 갈 compose 미리 보기
curl localhost:8090/aiapp/apps                            # 업로드한 응용 (처음에는 비어 있음)
curl -F name=open-webui -F script=@examples/apps/open-webui/deploy.sh \
     -F files=@examples/apps/open-webui/docker-compose.yml localhost:8090/aiapp/apps   # 응용 업로드, 다시 올리면 version 2
curl localhost:8090/aiapp/tools                           # AI-App 도구 10개와 등급
curl localhost:8090/aiapp/engine/card                     # 엔진 카드
curl localhost:8090/aiapp/archive                         # 배포·제어 아카이브
curl localhost:8090/aiapp/engine/runs                     # 엔진 실행 기록
curl localhost:8090/aiapp/partner/nl/runs                 # 지시 실행 기록 (모의)
open http://localhost:8090/aiapp/api/index.html           # Swagger UI
```

CB-Tumblebug 에 닿는 경로와 계획 모델이 필요한 경로만 실패합니다 (2026-10-29 실측, 마지막 줄은 2026-10-13).
dry-run 배포도 노드 조회는 하므로 CB-Tumblebug 이 답하지 않으면 노드가 `failed` 입니다.

```sh
curl -X POST localhost:8090/aiapp/deployments -H 'Content-Type: application/json' \
     -d '{"type":"model-service","targets":[{"kind":"node","nsId":"etri","infraId":"travel","nodeId":"vm1"}],
          "modelService":{"modelId":"qwen3-vl-8b","artifactId":"ollama-gguf"}}'
# HTTP 200  {"status":"failed","dryRun":true, ... "nodes":[{"status":"failed","error":"failed to get node \"vm1\" of infra \"travel\": ..."}]}
curl -X POST localhost:8090/aiapp/deployments -H 'Content-Type: application/json' \
     -d '{"type":"app","targets":[{"kind":"k8s","nsId":"etri"}],"app":{"name":"open-webui"}}'
# HTTP 422  {"message":"unsupported deployment target: targets[0]: kind k8s is not deployed to yet; use kind node"}
curl -X POST 'localhost:8090/aiapp/engine/workflows/status?ns=default' -H 'Content-Type: application/json' -d '{"deploymentId":"none-01"}'
# HTTP 200  {"workflow":"status", ... "status":"failed","message":"deployment: deployment not found: none-01; output: deployment not found: none-01"}
curl -X POST localhost:8090/aiapp/partner/nl/ns/default/intents -H 'Content-Type: application/json' -d '{"intent":"x"}'
# HTTP 503  {"message":"[partner mock] Planning model is not configured"}
```

**기동 로그가 base URL 과 그 출처(`default`/`environment`)를 반드시 찍습니다.**
기본값을 주는 대신, 잘못된 곳을 가리키는 서비스가 첫 줄에서 그렇다고 말하게 했습니다.

### 9-2. 실제 CB-Tumblebug 과 Semaphore 에 붙일 때

```sh
cp conf/template-setup.env conf/setup.env
# conf/setup.env 를 채운다 (아래 설정 표 참고)
make run-app
make run-engine             # 다른 터미널
```

`conf/setup.env` 는 **있으면 읽고 없으면 넘어갑니다.** `source` 할 필요가 없습니다.
이미 환경변수로 설정된 값이 파일보다 우선합니다. 네 프로세스가 같은 파일을 읽고, 각자 자기에게 필요한 변수만 씁니다.

Semaphore 로 노드에 실제로 배포하려면 셋이 더 필요합니다.

- Semaphore 가 `deploy/ansible` 을 `AIAPP_SEMAPHORE_PLAYBOOK_DIR`(기본 `/ansible`)로 보고 있어야 합니다.
- AI-App 의 `AIAPP_STAGE_ROOT` 와 **같은 절대경로**를 Semaphore 도 봐야 합니다 (같은 볼륨을 같은 경로로 마운트).
  stage 파일은 Semaphore 컨테이너 사용자도 읽게 `0644`/`0755` 로 씁니다.
- `AIAPP_TUMBLEBUG_DRY_RUN=false`. 이러면 실제 노드에 docker 를 깔고 컨테이너를 바꿉니다.

가장 쉬운 길은 9-3 의 `deploy` 프로필입니다.

### 9-3. Docker

```sh
make docker-build   # docker build -t ai-agent-proto:local .
make docker-up      # docker compose up -d --build
make docker-logs
make docker-down
```

**이쪽도 설정 파일이 필요 없습니다.** `docker-compose.yml` 은 같은 이미지에서 네 서비스를 띄우고, 명령만 다릅니다.
노드 배포에 쓰는 Semaphore 는 `deploy` 프로필로 따로 띄웁니다.

```sh
cp .env.example .env        # AIAPP_SEMAPHORE_PASSWORD 와 AIAPP_TUMBLEBUG_* 를 채운다
docker compose --profile deploy up -d
```

| 서비스 | 포트 | 비고 |
|---|---|---|
| `ai-app-server` | `8090` | 아카이브와 노드 배포 기록(`/app/data/archive/node-deployments.json`)은 named volume `archive`, 배포 파일은 named volume `stage` (`/var/lib/aiapp/stage`). Semaphore 주소 기본값 `http://semaphore:3000`. 멈출 때 진행 중인 배포의 키를 지우려고 `stop_grace_period: 80s` |
| `ai-automation-engine` | `8091` | `ai-app-server` 가 healthy 가 된 뒤 뜸. MCP 주소는 `http://ai-app-server:8090/aiapp/mcp`, 실행 기록은 named volume `engine-archive` |
| `mock-partner-recommender` | `8092` | 협력 기관 · 모의. AI-App 은 부르지 않고 `/aiapp/partner/rec/*` 로 넘기기만 함(계약 예시). CB-Tumblebug 설정을 읽음 |
| `mock-partner-nl-agent` | `8093` | 협력 기관 · 모의. `ai-automation-engine` 이 healthy 가 된 뒤 뜸. MCP 주소는 `http://ai-automation-engine:8091/aiapp/engine/mcp`, 기록은 named volume `partner-nl-archive` |
| `semaphore` | `127.0.0.1:3001` (`AIAPP_SEMAPHORE_PORT`) | `semaphoreui/semaphore:v2.12.17`, DB 는 bolt. `deploy` 프로필이라 `docker compose --profile deploy up -d` 일 때만 뜸. `deploy/ansible` 을 `/ansible` 로(읽기 전용), named volume `stage` 를 `ai-app-server` 와 같은 `/var/lib/aiapp/stage` 로(읽기 전용) 마운트. 설정·DB 는 `semaphore-config`·`semaphore-data`. `AIAPP_SEMAPHORE_USERNAME`(기본 `admin`)·`AIAPP_SEMAPHORE_PASSWORD` 로 관리자를 만들고 AI-App 도 그것으로 로그인. **비밀번호가 없으면 기동을 거부함**(Semaphore 는 빈 비밀번호로도 로그인시킴) |

- 등록한 모델(`AIAPP_MODEL_STORE_FILE=/app/data/catalog/models.json`)과 업로드한 응용(`AIAPP_APP_STORE_DIR=/app/data/catalog/apps`)은
  named volume `catalog` 에 남습니다. 컨테이너를 지우고 다시 만들어도 그대로 읽힙니다(2026-10-29 에 이미지를 빌드해 확인).

아래 `docker ps` 출력은 한 프로세스 시절(1차년도)에 받은 것입니다.

```
$ docker compose up -d
$ docker ps
STATUS                  PORTS
Up 10 seconds (healthy) 0.0.0.0:8090->8090/tcp, [::]:8090->8090/tcp

$ curl localhost:8090/aiapp/readyz
{"message":"Service is ready"}
```

**compose 에 CB-Tumblebug 를 넣지 않았습니다.** cb-spider 와 DB 가 딸려오는 스택이라,
넣으면 `docker compose up` 이 "이 프로토타입을 띄우는 명령" 이 아니라 "멀티클라우드 플랫폼
전체를 띄우는 명령" 이 됩니다. 컨테이너 안에서 호스트의 Tumblebug 을 보도록
`host.docker.internal` 이 기본값이고, Linux 에서도 되게 `extra_hosts` 를 걸어 두었습니다.

컨테이너에서도 **dry-run 이 기본 `true`** 입니다. 실수로 띄운 컨테이너가 노드에 playbook 을 돌리거나 노드를 제어할 수 없습니다.
아카이브는 named volume(`archive`, `engine-archive`, `partner-nl-archive`)에 있어 컨테이너보다 오래 삽니다.
compose 는 `AIAPP_AUTH_TOKEN` 하나를 AI-App·엔진·모의 자연어 에이전트의 `AIAPP_AUTH_TOKEN` 과, 엔진의 `AIAPP_APP_TOKEN`,
모의 에이전트의 `AIAPP_MOCK_NL_ENGINE_TOKEN`, 모의 추천 API 의 `AIAPP_MOCK_RECOMMENDER_TOKEN`(따로 주지 않으면)에 같이 넣습니다.
AI-App 이 넘기는 요청은 호출자의 `Authorization` 헤더를 그대로 달고 가기 때문입니다.

### 9-4. 검증 명령

```sh
make verify     # gofmt + go build + go test + golangci-lint
make build      # bin/ 아래에 바이너리 넷
make test       # go test ./...
make fmt        # gofmt -w .
make lint       # golangci-lint run
make swag       # Swagger 재생성
```

### 9-5. 운영자 콘솔

```
http://localhost:8090/aiapp/ui
```

**이 서비스의 API 를 실제로 호출하는 화면입니다.** 녹화본이나 예시 데이터가 아니라,
지금 이 서버가 돌려준 응답을 그립니다. 사이드바가 "이노그리드 구현" 과 "협력 기관 연동 지점 · 모의" 두 구역으로 나뉘고,
모의 화면 머리에는 "협력 기관 담당 영역 · 연동 확인용 모의 구현 (이노그리드 프로토타입 아님)" 띠가 붙습니다.

| 구역 | 화면 | 부르는 것 | 무엇을 보여주나 |
|---|---|---|---|
| 이노그리드 구현 | 개요 | `GET /readyz` · `/tools` · `/engine/readyz` · `/engine/card` · `/partner/nl/readyz` · `/partner/nl/card` · `/models` · `/apps` · `/deployments` | 이노그리드 구현과 협력 기관 모의가 각각 답하는지, **dry-run 게이트**, 도구 등급 분포, 모델·응용·배포 수 |
| 이노그리드 구현 | 모델 카탈로그 | `GET/POST/DELETE /models` · `GET /models/{id}/artifacts/{a}/compose` · `GET /engines` | 모델과 아티팩트, 필터, 엔진 프로필, compose 미리 보기. 등록·삭제가 실제로 됩니다 |
| 이노그리드 구현 | 응용 | `GET/POST/DELETE /apps` · `GET /apps/{name}/versions/{v}` | 업로드, 버전, 파일 크기·SHA-256 |
| 이노그리드 구현 | 노드 배포와 제어 | `POST/GET /deployments` · `GET /deployments/{id}` · `.../logs` · `POST .../control` | 배포 요청, 목록, 노드별 결과와 태스크 출력, 노드 제어 |
| 이노그리드 구현 | 자동화 엔진 | `GET /engine/card` · `POST /engine/workflows/{workflow}` · `GET /engine/runs` · `GET /deployments` | 워크플로 실행과 단계 기록, **게이트가 막은 단계** |
| 이노그리드 구현 | 모니터링 | `GET /deployments` | 노드마다 지금 올라가 있는 배포와 그 배포가 어떻게 끝났는지 (배포 기록에서 읽은 요약. 노드에 따로 묻지 않음) |
| 이노그리드 구현 | 도구와 게이트 (MCP) | `GET /tools` · `/engine/card` | 엔진 도구와 AI-App 도구 두 층, 등급별 분류, 입력 스키마, 게이트에 걸리는 것 |
| 이노그리드 구현 | 아카이브 | `GET /archive` · `/engine/runs` · `/partner/nl/runs` | 노드 배포·제어, 엔진 실행, 모의 지시 실행 기록을 시각순으로 합쳐 봄 |
| 협력 기관 · 모의 | 자연어 지시 | `POST /partner/nl/ns/{ns}/intents` | 모의 에이전트가 고른 엔진 도구와 엔진 실행 ID |
| 협력 기관 · 모의 | 인프라 추천 | `POST /partner/rec/recommendations/specs` · `.../image` | 추천 API(모의)가 돌려준 스펙 후보와 이미지. AI-App 이 넘겨 주는 길로 직접 부름 |

설계에서 지킨 것 넷입니다.

- **모든 패널에 그 화면이 부른 요청과 받은 응답 원문을 접이식으로 답니다.** 콘솔이 무엇도
  덧붙이지 않는다는 것을 확인할 수 있게 하려는 것입니다.
- **실패를 감추지 않습니다.** 서버가 낸 메시지를 그대로 보여 주고, 콘솔이 지어낸 문구로
  바꾸지 않습니다. CB-Tumblebug 이 안 붙어 있으면 배포 기록의 노드 오류에 그 응답이 그대로 나옵니다.
  **그것도 동작의 일부입니다.**
- **쓰기 버튼은 확인을 거칩니다.** 배포 요청과 노드 제어 버튼은 dry-run 이 꺼져 있으면 실제로 노드가 바뀐다고 묻고,
  모델·응용 삭제도 확인 창을 띄웁니다. 엔진의 쓰기 단계는 AI-App 의 게이트를 거칩니다.
- **외부 리소스를 하나도 안 씁니다.** CDN·폰트·스크립트 전부 없어서 망이 끊긴 곳에서도
  그대로 뜹니다. 페이지(`web/index.html`)는 `//go:embed` 로 바이너리에 들어가므로 컨테이너에도 볼륨이 필요 없습니다.

콘솔은 API 와 **같은 오리진**에서 서빙됩니다. API 에 CORS 정책을 여는 것보다,
페이지를 같은 자리에서 내보내는 쪽이 낫다고 판단했습니다.
`AIAPP_AUTH_TOKEN` 을 켜면 콘솔 페이지 자체는 열려 있고, 운영자가 넣은 토큰을 매 호출에 실어 보냅니다.

### 설정

**필수 변수는 없습니다.** 전부 기본값이 있습니다.
AI-App·엔진·모의 추천 API 는 같은 설정 로더(`internal/config`)를 쓰므로 그 값이 잘못되면 셋 다 기동을 거부합니다.
모의 자연어 에이전트는 자기 설정을 따로 읽습니다(`mock/partner/nlagent/cmd/nl-agent/main.go`).
"읽는 프로세스" 는 그 값을 실제로 쓰는 프로세스입니다
(App = `ai-app-server`, Engine = `ai-automation-engine`, Rec = `mock-partner-recommender`, NL = `mock-partner-nl-agent`).

| 환경변수 | 기본값 | 읽는 프로세스 | 설명 |
|---|---|---|---|
| `AIAPP_SERVER_PORT` | `8090` | App | AI-App 리스닝 포트 |
| `AIAPP_ENGINE_PORT` | `8091` | Engine | 엔진 리스닝 포트 |
| `AIAPP_ENGINE_URL` | `http://127.0.0.1:8091` | App | `/aiapp/engine/*` 을 넘길 엔진 주소 |
| `AIAPP_MOCK_NL_URL` | `http://127.0.0.1:8093` | App | `/aiapp/partner/nl/*` 을 넘길 자연어 에이전트 주소. 지금은 모의 |
| `AIAPP_PARTNER_REC_URL` | `http://127.0.0.1:8092` | App | `/aiapp/partner/rec/*` 을 넘길 추천 API 주소. 지금은 모의 |
| `AIAPP_APP_MCP_URL` | `http://127.0.0.1:8090/aiapp/mcp` | Engine | 도구를 받아 올 AI-App MCP 서버 |
| `AIAPP_AUTH_TOKEN` | | App, Engine, NL | 설정하면 준비 상태·콘솔·Swagger 를 뺀 전부가 이 bearer 토큰을 요구 (8절). 비우면 인증 꺼짐(기동 로그가 경고). 넘긴 요청은 헤더를 그대로 달고 가므로 세 프로세스에 같은 값을 넣음 |
| `AIAPP_APP_TOKEN` | | Engine, NL | 엔진이 AI-App MCP 서버에 보내는 bearer 토큰. NL 은 `AIAPP_MOCK_NL_ENGINE_TOKEN` 이 비었을 때 이것을 엔진에 보냄 |
| `AIAPP_TUMBLEBUG_BASE_URL` | `http://localhost:1323/tumblebug` | App, Rec | 예 `http://<host>:1323/tumblebug`. **없어도 기동합니다.** 기동 로그가 출처를 찍습니다 |
| `AIAPP_TUMBLEBUG_USERNAME` / `_PASSWORD` | | App, Rec | basic auth |
| `AIAPP_TUMBLEBUG_CREDENTIAL_HOLDER` | | App, Rec | `x-credential-holder` 헤더 값. 비우면 CB-Tumblebug 기본 holder |
| `AIAPP_TUMBLEBUG_TIMEOUT` | `20m` | App, Rec | CB-Tumblebug 호출 하나의 상한 |
| `AIAPP_TUMBLEBUG_IMAGE_NAMESPACE` | `system` | Rec | 이미지 카탈로그가 있는 네임스페이스 |
| `AIAPP_TUMBLEBUG_SPEC_NAMESPACE` | `system` | App, Rec | 스펙 카탈로그가 있는 네임스페이스. 노드 배포는 노드의 스펙을 여기서 읽어 GPU 여부를 판단 |
| `AIAPP_TUMBLEBUG_DEFAULT_OS_TYPE` | `ubuntu 22.04` | Rec | 요청이 OS 를 안 정했을 때 |
| **`AIAPP_TUMBLEBUG_DRY_RUN`** | **`true`** | App | **쓰기 게이트.** 노드 배포는 파일 배치까지만 하고 Semaphore 에 안 올림, 노드 제어는 `blocked`. `false` 로 바꾸면 실제 노드가 바뀜 |
| `AIAPP_HEALTH_TIMEOUT` | `15m` | Engine | 엔진 워크플로의 대기 단계가 배포가 끝나기를 기다리는 상한. 이름과 달리 헬스를 직접 확인하지 않음 (3-2) |
| `AIAPP_HEALTH_INTERVAL` | `20s` | Engine | 그 사이 다시 묻는 간격 |
| `AIAPP_ARCHIVE_DIR` | `./data/archive` | App | 배포·제어 아카이브 위치 |
| `AIAPP_MODEL_STORE_FILE` | | App | 모델 카탈로그를 쓰고 기동 때 다시 읽는 JSON 파일. 파일이 아직 없을 때만 시드 3개를 넣음. 비우면 메모리에만 둠(재기동하면 시드부터 다시) |
| `AIAPP_APP_STORE_DIR` | `./data/apps` | App | 업로드한 응용 저장 위치. `<name>/<version>/` 아래 `files/`, `script.sh`, `meta.json`. 기동 때 다시 읽음 |
| `AIAPP_APP_UPLOAD_MAX_BYTES` | `268435456` (256 MiB) | App | 업로드 한 번의 script 와 파일 합계 상한 (바이트) |
| `AIAPP_SEMAPHORE_URL` | | App | 노드에 playbook 을 돌릴 Semaphore 주소(`/api` 없이). 비우면 dry-run 이 아닐 때 노드 배포만 실패 |
| `AIAPP_SEMAPHORE_TOKEN` | | App | Semaphore API 토큰. 있으면 사용자·비밀번호 대신 씀 |
| `AIAPP_SEMAPHORE_USERNAME` / `_PASSWORD` | | App | Semaphore 로그인. 토큰도 이것도 없으면 배포가 Semaphore 에 닿을 때 오류 |
| `AIAPP_SEMAPHORE_PROJECT` | `aiapp` | App | 배포에 쓰는 프로젝트. 없으면 만듦. template 은 playbook 마다 하나(`aiapp-<playbook>`)를 만들어 두고 태스크 이력을 남김. 노드의 key·inventory 는 실행마다 등록하고 끝나면 지움 |
| `AIAPP_SEMAPHORE_PLAYBOOK_DIR` | `/ansible` | App | Semaphore 컨테이너 안의 playbook 폴더. 프로젝트 repository 로 등록됨 |
| `AIAPP_SEMAPHORE_POLL_INTERVAL` | `5s` | App | 태스크 상태 확인 간격 |
| `AIAPP_SEMAPHORE_TIMEOUT` | `30m` | App | 태스크 하나의 상한. 넘으면 태스크를 멈추고 등록한 것을 지움 |
| `AIAPP_STAGE_ROOT` | `/var/lib/aiapp/stage` | App | 배포 파일을 두는 곳. Semaphore 가 같은 절대경로로 봐야 하므로 절대경로만 받음 |
| `AIAPP_DEPLOYMENT_STORE_FILE` | | App | `POST /aiapp/deployments` 기록을 쓰고 기동 때 다시 읽는 JSON 파일. 비우면 메모리에만 둠(재기동하면 사라짐). 이전 기동에서 진행 중이던 기록은 `interrupted` 로 바뀜. docker compose 는 아카이브 볼륨의 `/app/data/archive/node-deployments.json` 으로 고정 |
| `AIAPP_ENGINE_ARCHIVE_DIR` | `./data/engine-archive` | Engine | 엔진 실행 기록 위치 |
| `AIAPP_ENGINE_REGISTRY_URL` / `_TOKEN` | | Engine | 기동할 때 엔진 카드를 등록할 레지스트리(`POST <url>/agents`)와 그 bearer 토큰. 비우면 등록 안 함 |
| `AIAPP_MOCK_RECOMMENDER_PORT` | `8092` | Rec | 모의 추천 API 리스닝 포트 |
| `AIAPP_MOCK_RECOMMENDER_TOKEN` | | Rec | 설정하면 모의 추천 API 의 모든 경로가 이 bearer 토큰을 요구 |
| `AIAPP_MOCK_NL_PORT` | `8093` | NL | 모의 자연어 에이전트 리스닝 포트 |
| `AIAPP_MOCK_NL_ENGINE_MCP_URL` | `http://127.0.0.1:8091/aiapp/engine/mcp` | NL | 도구를 받아 올 엔진 MCP 서버 |
| `AIAPP_MOCK_NL_ENGINE_TOKEN` | | NL | 엔진 MCP 서버에 보내는 bearer 토큰. 비우면 `AIAPP_APP_TOKEN` |
| `AIAPP_MOCK_NL_ARCHIVE_DIR` | `./data/partner-nl-archive` | NL | 지시 실행 기록 위치 |
| `AIAPP_MOCK_NL_MAX_STEPS` | `12` | NL | 도구 호출 반복 상한 |
| `AIAPP_MOCK_NL_REGISTRY_URL` / `_TOKEN` | | NL | 기동할 때 모의 에이전트 카드를 등록할 레지스트리와 그 bearer 토큰. 비우면 등록 안 함 |
| `AIAPP_LLM_BASE_URL` | `https://api.anthropic.com` | NL | Messages API 엔드포인트. `/v1/messages` 를 받는 곳이면 됨 (3절) |
| `AIAPP_LLM_API_KEY` | | NL | 비어 있으면 지시 경로만 503, 나머지는 정상 동작 |
| `AIAPP_LLM_MODEL` | `claude-opus-5` | NL | 계획 모델 |
| `AIAPP_LLM_MAX_TOKENS` | `16000` | NL | 모델 호출 한 번의 최대 출력 토큰 |
| `AIAPP_LLM_TIMEOUT` | `5m` | NL | 모델 호출 한 번의 시간 상한 |

`AIAPP_SEMAPHORE_PORT`(기본 `3001`)는 프로세스가 읽지 않고 `docker-compose.yml` 이 Semaphore 를 호스트에 내놓는 포트로만 씁니다.
`AIAPP_SERVER_PORT`·`AIAPP_ENGINE_PORT`·`AIAPP_MOCK_NL_PORT`·`AIAPP_MOCK_RECOMMENDER_PORT` 도 compose 에서는 호스트 쪽 포트입니다.

## 10. 검증 결과

### 10-0. 3차 개편 (2026-10-29, 로컬)

실제 클라우드 노드에는 배포하지 않았습니다. 로컬의 실 Semaphore `v2.12.17` + docker-in-docker 컨테이너(노드 대신) + 가짜 CB-Tumblebug 으로
app 배포, Ollama 모델 서비스 배포, 같은 노드 재배포 교체, dry-run 시 Semaphore 변화 없음, 실행 뒤 남은 key·inventory 0 을 확인했습니다.
vLLM GPU 기동, `install-docker` 의 실제 설치 분기, MCP 를 거친 `create_deployment` 끝까지의 배포는 확인하지 못했습니다.
작업별 커밋과 확인한 것·못 한 것은 `docs/design/model-catalog-and-node-deploy.md` 11절에 있습니다.

아래 10-1 ~ 10-8 은 **3차 개편 전 기록**입니다. 2026-09 에 한 프로세스 시절 코드로 잰 것이고, 그때의 VM 생성 경로·서빙 포트 개방·
가속기 프로브·규칙 매처는 지금 코드에 없습니다. 본문의 절 번호(6-4, 6-5 등)는 그때 README 의 번호입니다.

**실제 VM 생성부터 삭제까지 (2026-09-02)**

`AIAPP_TUMBLEBUG_BASE_URL=http://<tumblebug-host>:1323/tumblebug`, `DRY_RUN=false` 로
**AWS 에 실제 GPU VM 을 만들고, terminate 로 지우고, CB-Spider 에서 사라진 것까지 확인**했습니다.

| 항목 | 결과 | 근거 |
|---|---|---|
| `gofmt` / `go build` / `golangci-lint` | 통과 (0 issues) | |
| 응용 카탈로그 등록·조회·검증 | 통과 | 필수 필드 누락 시 `{"message":"version required"}` |
| 가속기 요구 -> 스펙 추천 | 통과 | GPU x1 >=8GiB 조건으로 6건, 최저가 $0.37853/h |
| 이미지 자동 조회 (같은 벤더의 GPU 이미지 우선) | 통과 | arm64 NVIDIA 스펙에 `ami-0fc9719148d3c8367` (ARM64 NVIDIA GPU AMI) 선택 |
| 배포 사전 검증(`infraDynamicReview`) | 통과 | `creationViable=true`, `$0.4200/hour` |
| **실제 VM 생성** | **통과** | `aiapp-live-03` 생성 2m28s, `Running:1 (R:1/1)`, EC2 `i-xxxxxxxxxxxxxxxxx` |
| **응용 설치(`postCommands`) 실행** | **통과** | 노드에서 `nvidia-smi` -> `NVIDIA T4G, 15360 MiB, 595.71.05`, `AI-MCMP-PROBE-OK` |
| **멱등성 (같은 이름 재요청)** | **통과** | HTTP 409, `infra already exists: aiapp-live-03` |
| **terminate 삭제** (force 아님) | **통과** | `[Done] Node: serving-1` / `NodeGroup: serving` / `Infra: aiapp-live-03`, 5m39s |
| **CB-Spider 에서 삭제 확인** | **통과** | us-west-2 / us-east-1 양쪽 vm·vpc·securitygroup·keypair 전부 `count = 0` |
| **서빙 포트 개방 후 외부 접속** | **통과** | `curl http://<public-ip>:8000/index.html` -> `AI-MCMP-SERVING-OK`, HTTP 200 |
| 아카이빙 | 통과 | 실패 5건 + 성공 1건 + 삭제 4건 전부 레코드로 남음 |
| dry-run 게이트 | 통과 (별도 회차) | 배포·제어·삭제 셋 다 `dryRun:true`, 클라우드 미접촉 |

### 10-1. 성공한 회차의 실제 값

```
POST /aiapp/ns/default/deployments
  {"appId":"gpu-probe-live","infraName":"aiapp-live-03","specId":"aws+us-west-2+g5g.xlarge"}

message : aiapp-live-03 deployed (2m28s)
image   : ami-0fc9719148d3c8367 | resolved | accelerator image: ships the vendor driver
cost    : $0.4200/hour
node    : serving-1  Running  <public-ip>

postCommand stdout:
  [0] name, memory.total [MiB], driver_version
      NVIDIA T4G, 15360 MiB, 595.71.05
  [1] Linux ip-<private-ip> 6.8.0-1060-aws ... aarch64 GNU/Linux
  [2] AI-MCMP-PROBE-OK
```

`nvidia-smi` 가 도는 것이 이 프로토타입의 주장을 실증합니다.
**가속기 요구사항만 주면 드라이버가 든 이미지로 GPU 노드가 뜨고 응용 설치까지 한 호출에 끝납니다.**

### 10-2. 삭제 확인 (CB-Spider 직접 조회)

Tumblebug 기록이 아니라 **Spider 를 통해 CSP 실체**를 봤습니다.

| 시점 | vm | vpc | securitygroup | keypair |
|---|---|---|---|---|
| 생성 전 | 0 | 0 | 0 | 0 |
| VM 기동 중 | **1** (`i-xxxxxxxxxxxxxxxxx`) | 1 | 2 | 2 |
| terminate 직후 | **0** | 1 | 2 | 2 |
| 공유자원 해제 후 | 0 | **0** | **0** | **0** |

us-east-1 (실패 회차 잔여분)도 최종적으로 전부 `0` 입니다.

### 10-3. 실패한 회차와 원인

성공까지 5번 실패했습니다. 전부 프로토타입 밖의 원인이거나, 이번에 고친 것입니다.

| # | 대상 | 실패 원인 | 성격 |
|---|---|---|---|
| 1 | us-west-2 g4ad | `aws-us-east-1: does not exist!` - **Spider 에 자격증명 0건** | 노드 설정. `init.sh --credentials-only` 로 해소 |
| 2 | us-west-2 g4ad | `InvalidBlockDeviceMapping: Volume of size 50GB is smaller than snapshot, expect >= 75GB` | **프로토타입 결함.** 아래 수정 |
| 3 | us-west-2 g4ad | Tumblebug 위험도 모델이 #2 이력(1/1 실패)으로 차단 | 상류 안전장치가 정상 동작 |
| 4 | us-west-2 g6.2xlarge | `InsufficientInstanceCapacity` (us-west-2b) | AWS 용량 |
| 5 | us-east-1 g6.2xlarge | `VcpuLimitExceeded: current vCPU limit of 0` | AWS 계정 G 인스턴스 쿼터 0 |

**#2 로 고친 것**: `defaultRootDiskGiB = 60` 이라는 근거 없는 기본값을 넣고 있었습니다.
응용이 요구하지도 않은 루트 디스크 크기를 지어내서 보내다가, 스냅샷이 75GB 인 GPU AMI 에서 거부당했습니다.
`ImageInfo.osDiskSizeGB` 로 최소치를 알아내려 했으나 이 카탈로그에서는 전부 `-1` 이라 API 로는 알 수 없습니다.
그래서 **응용이 크기를 지정하지 않으면 `rootDiskSize` 를 아예 보내지 않고 이미지 자체 크기를 쓰도록** 고쳤습니다
(`internal/deploy/service.go`). 우회가 아니라, 근거 없는 값을 빼는 수정입니다.

### 10-4. 서빙 포트 실증 (별도 회차)

`serving-probe` 응용으로 노드에서 8000 포트에 실제 리스너를 띄우고 밖에서 호출했습니다.

```
POST /aiapp/ns/default/deployments
  {"appId":"serving-probe","infraName":"aiapp-e2e-01","specId":"aws+us-west-2+g5g.xlarge"}

message : aiapp-e2e-01 deployed (2m22s)
serving : opened=true, port=8000, sourceIp=0.0.0.0/0,
          securityGroupIds=["aiapp-e2e-01-serving"],
          endpoints=["http://<public-ip>:8000/index.html"]

$ curl http://<public-ip>:8000/index.html
AI-MCMP-SERVING-OK          HTTP:200  time:0.335s
```

Spider 로 본 CSP 규칙에 `inbound TCP 8000 - 8000 0.0.0.0/0` 이 들어가 있습니다.

이 회차만으로는 규칙이 접속의 원인이라고 말할 수 없었습니다. 같은 보안그룹에 기본
템플릿(`sg-default`)이 넣은 `inbound TCP 1-65535` 가 이미 있었기 때문입니다 (6-4절).
그래서 아래 대조 실험을 따로 했습니다.

### 10-5. 대조 실험 - 규칙이 원인인지 (제한 템플릿)

`sgTemplateId: sg-usecase-web` 로 배포했습니다. 이 템플릿은 22, 80, 443, 8080, 8443, ICMP 만
열고 **8000 은 열지 않습니다.** 응용은 8000(서빙 포트)과 8080(대조군)에 동시에 리스너를 띄웁니다.

생성 직후 CSP 보안그룹 규칙 - 템플릿 규칙 + **프로토타입이 넣은 8000** 뿐이고 전체 개방이 없습니다.

```
inbound TCP 22   inbound TCP 80   inbound TCP 443
inbound TCP 8080 inbound TCP 8443 inbound ICMP
inbound TCP 8000      <- 프로토타입이 추가
```

같은 VM, 같은 리스너에서 **규칙만 넣었다 뺐다** 했습니다.

| 단계 | 8000 (서빙 포트) | 8080 (대조군) |
|---|---|---|
| A. 규칙 있음 (배포 직후) | `AI-MCMP-PORT-8000` **HTTP 200** | `AI-MCMP-PORT-8080` HTTP 200 |
| B. 8000 규칙만 삭제 | **HTTP 000 (접속 실패)** | `AI-MCMP-PORT-8080` HTTP 200 |
| C. 8000 규칙 재추가 | `AI-MCMP-PORT-8000` **HTTP 200** | `AI-MCMP-PORT-8080` HTTP 200 |

B 에서 8080 이 계속 살아 있으므로 VM 이나 리스너가 죽은 것이 아닙니다.
**제한 템플릿에서는 이 규칙이 있어야만 서빙 포트가 열립니다.**

### 10-6. AI 응용 판별 정확도 (2026-09-03)

운영자가 쓰는 말로 지시 19건을 만들어 top-1 정확도를 쟀습니다
(`internal/catalog/resolve_test.go`). 등록 id, 영문 모델명, 한국어 음차, 런타임명,
기능 표현, 그리고 **등록된 적 없는 모델 요청**을 섞었습니다.

| | top-1 정확도 |
|---|---|
| 이전 (id·이름 부분 문자열) | **3 / 19** |
| 지금 (규칙 매처) | **19 / 19** |

`list_ai_apps` 응답도 전체 스펙에서 요약으로 바꿨습니다. 앱 3개 기준 2,568 -> 949 bytes (64% 감소).
빠진 것은 대부분 `install.commands` 로, 어느 응용인지 고르는 데 쓰이지 않습니다.

**이 숫자는 규칙 매처의 정확도이지 에이전트 전체의 정확도가 아닙니다.**
최종 선택은 여전히 LLM 이 하고, 이 환경에 API 키가 없어 그 부분은 재지 못했습니다.
측정 대상은 "LLM 에게 무엇을 보여주는가" 까지입니다.
(측정 당시에는 LLM 에이전트가 이 저장소에 있었습니다. 지금 LLM 은 협력 기관 모의 자연어 에이전트 쪽에 있고,
매처는 AI-App 과 엔진의 `list_applications` 에 그대로 있습니다.)

지시 세트를 만들면서 매처를 같이 손봤으므로 **과적합 위험이 있습니다.**
그래서 절대 수치보다 기준선 대비 차이를 같이 적었고, 판정 근거(`alias:라마`)를 응답에 실어
왜 그렇게 골랐는지 사람이 확인할 수 있게 했습니다.

### 10-7. 아직 확인 못 한 것

| 항목 | 왜 |
|---|---|
| 자연어 intent 의 실제 Messages API 호출 | 이 환경에 API 키가 없음. 루프 자체는 스텁으로만 검증. 이후 2026-10-07 에 Ollama(`glm-4.7-flash`)로 dry-run 시연을 했고, 아카이브 레코드상 도구 선택부터 `review_deployment` 까지 돌았음. 이 경로는 지금 협력 기관 모의 자연어 에이전트(`/aiapp/partner/nl/ns/{nsId}/intents`)에 있음 |
| **가속기 프로브의 원격 실행** | 파서는 실측 출력으로 단위테스트했지만, `POST /ns/{ns}/cmd/infra` 왕복은 VM 없이 확인 못 함 |
| **NPU·TPU 실기기 판독** | **경로는 만들었습니다** (6-5). 파서는 공개 문서 예시로만 단위테스트했고 **실기기가 없습니다** |
| **다벤더 프로브의 원격 실행 왕복** | 섹션 스크립트가 실제 노드에서 어떻게 도는지는 NVIDIA 노드에서만 봤습니다 |
| 컨테이너 배포 | 3차년도 항목 |
| 다중 노드(`nodeCount` > 1) 배포 | 비용 때문에 1대로만 검증 |
| AWS 외 CSP 에서의 생성 | 비용·시간 때문에 AWS 만 |

### 10-8. 실증에서 나온 결함 둘을 더 고쳤습니다

**(1) 서빙 포트를 아무도 열지 않았습니다.**
`serving.port` 가 검증에만 쓰이고 배포에는 안 쓰였습니다. `sg-default` 가 전부 열어 두는
덕에 드러나지 않았을 뿐, 운영용 제한 템플릿으로 바꾸는 순간 배포는 성공을 보고하고
엔드포인트만 죽습니다. 10-5 의 대조 실험이 이것을 그대로 보여 줍니다.
이제 노드의 보안그룹에 명시적으로 규칙을 넣고 결과에 엔드포인트를 담습니다 (6-4절).

**(2) 가속기 벤더와 이미지 벤더를 대조하지 않았습니다.**
`ResolveImage` 가 `isGPUImage=true` 만 보고 골라서, AMD GPU 인스턴스(`g4ad`)에
NVIDIA Deep Learning AMI 를 붙였습니다. 부팅은 되지만 드라이버가 바인딩되지 않고,
응용이 장치를 쓰려 할 때가 되어서야 드러납니다. 노드가 멀쩡해 보이는 만큼 일반 이미지보다 나쁩니다.

이제 스펙의 `acceleratorModel` 에서 벤더를 뽑아, **같은 벤더의 가속기 이미지일 때만** 우선합니다.
없으면 일반 이미지로 내려가되 이유를 남깁니다. 벤더를 알 수 없으면 종전대로 동작합니다.

실측 (같은 응용, 스펙만 바꿈):

| 스펙 | 가속기 | 고른 이미지 | 이유 |
|---|---|---|---|
| `aws+us-west-2+g4ad.xlarge` | AMD RADEON PRO V520 | `ami-0b29ba40f35aad99d` (일반) | `no amd accelerator image is registered for this spec ...` |
| `aws+us-west-2+g5g.xlarge` | NVIDIA T4G | `ami-0fc9719148d3c8367` (ARM64 GPU) | `accelerator image: ships the vendor driver` |
| `aws+us-west-2+g6.2xlarge` | NVIDIA L4 | `ami-0358c7fed3fc2fbd0` (GPU) | `accelerator image: ships the vendor driver` |

## 11. 안 넣은 것 (빠뜨린 게 아니라 뺀 것)

| 안 넣은 것 | 왜 |
|---|---|
| VM(Infra) 생성 | 3차 개편에서 지움. CB-Tumblebug 이 만든 노드를 받아 배포함. 생성은 인프라 추천 쪽이나 운영자 몫 |
| 서빙 포트 개방 (보안 그룹 규칙) | VM 생성 경로와 함께 지움. Infra 를 만든 쪽이 열어야 함 (5-6) |
| 가속기 실측 | 3차 개편에서 지움. 노드 기준으로 다시 만들려면 노드 원격 명령 경로부터 정해야 함 |
| 쿠버네티스 대상 배포 | `kind: k8s` 자리만 있고 `422`. 계획서상 3차년도 항목 |
| 모델 추천·인프라 추천 로직 | 경희대·협력 기관 몫. AI-App 은 메타데이터를 원래 값 그대로 내줌 |
| 모델 파일 저장 | 노드에서 엔진이 출처(HF, Ollama)에서 직접 받음 |
| Windows 노드, Ubuntu 외 배포판 | `install-docker` 가 Ubuntu 만 다룸 |
| 승인 대기 상태 저장소 | 아카이브 레코드로 대체. 2차년도에 붙일 때 구조가 안 바뀜 |
| `go-playground/validator` | 신규 의존성 승인 전. `validate` 태그는 규격 문서용으로 두고 검증은 명시적으로 함 |
| 사용자·역할 단위 인가 | 프레임워크 골격(협력 기관) 확정 전. 지금은 AI-App·엔진·모의 자연어 에이전트가 같이 쓰는 bearer 토큰 하나(`AIAPP_AUTH_TOKEN`)만 있음 |
| 협력 기관 실제 API 연동 (인프라 추천, 자연어 지원 에이전트) | 계약 합의 전. 지금은 제안 계약과 모의 구현(`mock/partner/`)까지. 추천 API 는 `AIAPP_PARTNER_REC_URL`, 자연어 에이전트는 `AIAPP_MOCK_NL_URL` 만 바꾸면 AI-App 이 그쪽으로 넘김 |

## 12. 이 프로토타입이 확인해 준 CB-Tumblebug 사실

> **3차 개편 전 기록.** VM 을 만들던 시절(2026-09-01)에 확인한 것이라 지금 코드가 쓰지 않는 API(`infraDynamic`, `postCommands`,
> 보안 그룹 규칙 등)가 섞여 있습니다. CB-Tumblebug 쪽 사실이라 그대로 둡니다.

다른 팀도 겪을 것들이라 적어 둡니다. 전부 2026-09-01 실측입니다.

| 사실 | 영향 |
|---|---|
| 리소스 이름이 `mci` 가 아니라 **`infra`** | 옛 문서 기준으로 짜면 전부 404 |
| 없는 Infra 조회 시 **404 가 아니라 500** | 존재 확인을 단건 조회로 하면 안 됨 |
| `recommendSpec` 의 `limit` 은 **정수** | 문자열이면 400 |
| 이미지는 **`system` 네임스페이스**에 있음 | `default` 에서 검색하면 0건 |
| `searchImage` 의 `matchedSpecId` 로 스펙 호환 이미지를 바로 찾음 | 프로바이더별 이미지 표를 손으로 안 만들어도 됨 |
| `InfraDynamicReq.postCommands` 로 생성과 설치를 한 호출에 | 별도 배포 채널 불필요 |
| `infraDynamicReview` 가 생성 없이 검증 + 비용 추정 | plan/apply 분리가 이미 가능 |
| Tumblebug 의 `connConfig` **`verified: true` 는 Spider 등록을 뜻하지 않음** | 117건 verified 인데 Spider 는 0건이라 아무것도 생성 못 했음 |
| Spider 자원 조회는 쿼리 파라미터 `?ConnectionName=` (헤더 아님) | 헤더로 보내면 `ConnectionName is empty!` |
| Tumblebug 은 **spec+image 프로비저닝 실패 이력**으로 이후 요청을 차단함 | 한 번 실패한 조합은 review 가 `creationViable=false` 로 막음 |
| `ImageInfo.osDiskSizeGB` 가 이 카탈로그에서는 전부 `-1` | 이미지 최소 디스크 크기를 API 로 알 수 없음 |
| `DELETE /ns/{ns}/sharedResources` 는 Infra 삭제 후에만 동작 | 순서를 지켜야 CSP 의존성 오류가 안 남 |
| 기본 보안그룹 템플릿 `sg-default` 는 **TCP/UDP 1-65535 를 전부 개방** | 기본값으로 검증하면 포트 개방 여부를 확인할 수 없다. 템플릿 자체가 dev/test 용이라고 적혀 있음 |
| **`DELETE /ns/{ns}/sharedResources` 는 네임스페이스 전체를 지운다** | 공용 네임스페이스에서 쓰면 남의 자원까지 삭제를 시도한다. 자기 자원만 이름으로 지울 것 |
| 노드의 보안그룹은 `NodeInfo.securityGroupIds` 로 알 수 있음 | 이름 규칙에 의존하지 않고 서빙 포트를 열 수 있음 |
| 규칙 추가 요청은 `Ports`, 응답은 `Port` | 요청·응답 타입을 따로 둬야 함 |
