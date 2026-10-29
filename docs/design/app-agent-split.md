# 이노그리드 담당 영역과 협력 기관 연동 지점 분리 설계 (2차 개편)

> **3차 개편([model-catalog-and-node-deploy.md](model-catalog-and-node-deploy.md))으로 데이터 모델과 배포 경로가 바뀜.** 이 문서의 담당 경계는 그대로지만, `AppSpec`·VM 생성·서빙 포트 개방·가속기 실측 설명은 지금 코드와 다르다.

> 작성 2026-10-13. 8·9절은 구현 후 실제 실행 결과로 채웠다. 1차 개편(AI-App 서비스와 LLM 에이전트를 MCP 로 분리, 커밋 `889f164`~`88547a0`)을
> 다시 나눈다. 1차 개편은 자연어 해석(LLM)과 인프라 추천까지 이 repo 의 담당 코드로 두어, 협력 기관 담당
> 영역까지 이노그리드가 구현한 것처럼 보였다.

## 1. 담당 구분

AI-MCMP 과제 역할 분담에 따른 구분이다. 이 repo 는 이노그리드 담당 영역의 프로토타입이고, 협력 기관 담당
영역은 연동을 확인하기 위한 **모의 구현**만 `mock/partner/` 아래에 둔다.

| 프레임워크 | 블록 | 담당 | 이 repo |
|------------|------|------|---------|
| AI-App | AI 모델 통합 관리 (모델 등록·버전, 배포·관리, 메타정보) | 이노그리드 | 구현 (`cmd/ai-app-server`) |
| AI-App | AI 응용 등록 및 버전 관리 | 이노그리드 | 구현 |
| AI-App | AI 응용 라이프사이클 관리 | 이노그리드 | 구현 |
| AI-App | AI 응용 모니터링 관리 (서빙 헬스, 가속기 실측) | 이노그리드 | 구현 |
| AI-Ops | AI 응용 관리 자동화 에이전트: 자동배포/자동제어 엔진 | 이노그리드 | 구현 (`cmd/ai-automation-engine`) |
| AI-App | AI 응용 요구 분석 및 인프라 추천 | 협력 기관 | 모의 (`mock/partner/recommender`) |
| AI-App | AI 응용 배포 자동화 및 업데이트 | 협력 기관 | 계약(Provisioner)만. 실행은 CB-Tumblebug 직접 호출을 기본으로 둔다 |
| AI-Ops | AI LLM 운영 관리: 자연어 지원 에이전트, OpsLLM 실행, 에이전트 등록 | 협력 기관 | 모의 (`mock/partner/nlagent`) |
| AI-Ops | AI 응용 관리 자동화 에이전트: 배포/운영 정보 기반 추론 최적화 | 협력 기관 | 없음 (엔진 실행 기록이 입력 데이터) |

## 2. 구조

```
[협력 기관 · 모의] 자연어 지원 에이전트 (LLM)          mock/partner/nlagent  :8093
     │ MCP (엔진의 상위 작업 도구)
     ▼
[이노그리드] AI 응용 관리 자동화 에이전트              cmd/ai-automation-engine :8091
            = 자동배포/자동제어 엔진 (LLM 없음, 정해진 순서로 실행)
     │ MCP (AI-App 의 기능 도구)
     ▼
[이노그리드] AI-App 서비스                              cmd/ai-app-server :8090
            모델·응용 등록·버전, 라이프사이클, 모니터링, 배포 실행, MCP 서버, 콘솔
     ├─▶ [협력 기관 · 모의] 인프라 추천 API             mock/partner/recommender :8092
     └─▶ CB-Tumblebug
```

- 이노그리드 바이너리 두 개는 LLM 을 쓰지 않는다. 자연어 지시가 없어도 엔진에 구조화된 요청을 보내 전 과정이 돈다.
- 콘솔과 외부 호출은 AI-App 한 주소로 들어오고, AI-App 이 `/aiapp/engine/*` 을 엔진으로,
  `/aiapp/partner/nl/*` 을 모의 자연어 에이전트로 넘긴다.
- 모의 구현은 같은 Go 모듈에 있지만 이노그리드 패키지가 모의 패키지를 import 하지 않는다(테스트로 검사).

## 3. 이노그리드: 자동배포/자동제어 엔진

### 3.1 워크플로

| 워크플로 | 입력 | 하는 일 |
|----------|------|---------|
| `deploy` | appId, namespace, deploymentName, (specId, imageId 선택) | 응용이면 requires 의 모델 서비스가 떠 있는지 확인 → 없으면 모델 서비스 배포 단계를 먼저 넣음 → 각 단계마다 AI-App 의 계획(추천 포함) → 검토 → 배포 → 서빙 헬스 확인 |
| `heal` | deploymentId | 서빙 헬스 확인 → 실패면 정책에 따라 업데이트(설치 명령 재실행) 1회 → 다시 확인 → 결과 |
| `control` | deploymentId, action | 정지·재개·재부팅·종료 |
| `status` | deploymentId | 노드 상태 + 서빙 헬스 + 가속기 실측 요약 |

- 각 워크플로는 단계 목록으로 실행되고, 단계마다 호출한 AI-App 도구, 입력, 결과, 게이트 차단 여부를 기록한다
  (엔진 실행 기록, AI-Ops 의 "배포/제어 정보 아카이빙").
- 쓰기 단계가 dry-run 게이트에 막히면 그 단계를 "차단됨" 으로 남기고, 그 결과에 의존하는 다음 단계는
  "선행 단계 대기" 로 남긴다(실제로 실행한 것처럼 보이지 않게).
- 엔진은 상위 작업을 MCP 도구(`deploy_application`, `heal_application`, `control_application`,
  `application_status`, 카탈로그 조회 `list_applications`)와 REST(`/aiapp/engine/workflows`)로 연다.
  자연어 에이전트는 이 MCP 도구를 부른다. 쓰기 3개는 write, 조회 2개는 read 등급이다.
- 엔진이 부르는 AI-App 도구는 12개로 고정돼 있고(`internal/engine/workflows.go`), 엔진 카드에 그 목록만 싣는다.

### 3.2 AI-App 이 계속 맡는 것

1차 개편에서 만든 것 중 이노그리드 담당은 그대로 둔다: 응용 메타데이터(`kind`, `requires`), 등록·버전,
배포 실행 순서(중복 차단 → 검토 → dry-run 게이트 → 생성 → 포트 → 실측 → 헬스), 업데이트, 라이프사이클,
서빙 헬스체크, Bearer 인증, MCP 서버, 배포 호출 기록.

### 3.3 인프라 추천의 위치

- AI-App 은 추천을 **직접 하지 않는다.** `AIAPP_RECOMMENDER_URL` 의 추천 API(제안 계약)를 부른다.
- 추천 API 가 설정되지 않았으면, 요청에 specId·imageId 를 직접 지정해야 하고 아니면 계획이 실패한다
  (`422 no recommender configured: pin specId and imageId`).
- 1차년도에 쓰던 추천 로직(가속기 요구사항 → CB-Tumblebug 스펙 필터, 비용순, 벤더별 이미지)은
  `mock/partner/recommender` 로 옮겨 모의 추천 API 가 된다.

## 4. 협력 기관 연동 지점 (모의)

| 모의 | 계약 | 협력 기관이 실제로 할 것 |
|------|------|--------------------------|
| 인프라 추천 API | `POST /recommendations/specs`, `POST /recommendations/image` (요청에 이노그리드 AppSpec) | 요구 분석, 추천 알고리즘, 근거 |
| 자연어 지원 에이전트 | 엔진 MCP 도구를 부르는 MCP 클라이언트, Anthropic Messages 형식 LLM | LLM 선정·실행, 자연어 해석, 에이전트 등록 |
| 에이전트 등록 | 엔진이 `GET /aiapp/engine/card` 로 기술서 제공, 설정 시 `POST {registry}/agents` | 등록 관리 체계 |
| 배포 자동화·업데이트 | Provisioner 계약(`/provisions`, `/provisions/{id}/update`) | 실행 주체 확정 후 구현 |
| 추론 최적화 | 엔진 실행 기록(JSONL) | 배포/운영 정보 기반 추론 최적화 |

모의 구현은 화면·응답·로그에 "협력 기관 담당 영역 모의 구현" 으로 표시한다.

## 5. 콘솔

| 구역 | 화면 |
|------|------|
| 이노그리드 구현 | 개요, 응용 카탈로그(등록·버전), 자동화 엔진(워크플로 실행·단계 기록), 배포와 라이프사이클, 모니터링(헬스·가속기), 아카이브 |
| 협력 기관 연동 지점 (모의) | 자연어 지시(모의 자연어 에이전트 경유), 인프라 추천 API |

두 구역은 사이드바에서 나뉘고, 모의 화면에는 머리에 "협력 기관 담당 영역 · 모의 구현" 띠를 둔다.

## 6. 바뀌는 코드

| 지금 | 바뀐 뒤 |
|------|---------|
| `cmd/ai-agent`, `internal/agent`, `internal/llm`, `internal/agentcard` | `mock/partner/nlagent` 로 이동. 도구 공급원은 엔진 MCP |
| `internal/deploy/spec.go`, `image.go` (추천 로직) | `mock/partner/recommender` 로 이동. AI-App 은 remote 만 |
| 없음 | `cmd/ai-automation-engine`, `internal/engine` (워크플로, 기록, MCP 서버, REST), 엔진 카드 |
| `internal/rest/proxy.go` (에이전트 프록시) | `/engine/*` → 엔진, `/partner/nl/*` → 모의 자연어 에이전트 |

## 7. 하지 않는 것

- 협력 기관 실제 API 연동(계약 합의 전), 사용자·역할 인가, 컨테이너 배포 대상, AI-HA 연동.
- 실제 클라우드 자원 생성. 시연은 dry-run 으로 찍는다. 따라서 `deploy` 워크플로에서 응용 단계는
  "모델 서비스가 실제로 뜬 뒤 진행" 으로 남는다.

## 8. 동작 과정

### 8.1 자동화 엔진의 deploy (이노그리드)

`POST /aiapp/engine/workflows/deploy?ns=default {"appId":"open-webui-llama31","deploymentName":"demo-webui"}`

| # | 단계 | 엔진이 부르는 AI-App 도구 | dry-run 일 때 |
|---|------|---------------------------|---------------|
| 1 | 응용 읽기 | `get_ai_app` | succeeded |
| 2 | 필요한 모델 서비스가 떠 있는지 | `list_deployments` | succeeded (vllm 없음 → 먼저 배포) |
| 3 | 모델 서비스 계획 (추천 API 경유) | `plan_deployment` | succeeded |
| 4 | 모델 서비스 검토 | `review_deployment` | succeeded |
| 5 | 모델 서비스 배포 | `deploy_ai_app` | **blocked** (dry-run 게이트) |
| 6 | 모델 서비스 서빙 헬스 | `check_serving_health` | waiting |
| 7~10 | 응용 계획·검토·배포·헬스 | 같은 도구 | waiting (모델 서비스가 떠야 엔드포인트를 주입할 수 있음) |

dry-run 이 꺼져 있으면 5 에서 VM 이 생기고, 6 에서 서빙이 답할 때까지 기다린 뒤, 7 의 계획에서 AI-App 이
모델 서비스 엔드포인트를 응용 설치 명령에 `OPENAI_API_BASE_URL` 로 넣는다.

### 8.2 heal / control / status (이노그리드)

- heal: 헬스 확인 → 답하면 끝 → 아니면 업데이트(설치 명령 재실행) 1회 → 다시 확인. 업데이트가 노드 0대에만
  적용되면 실패로 남기고 재확인은 건너뛴다.
- control: 정지·재개·재부팅·종료. dry-run 이면 blocked.
- status: 노드 상태, 서빙 헬스, 가속기 실측을 각각 독립으로 묻고 요약한다.

### 8.3 자연어 지시 (협력 기관 모의 → 이노그리드 엔진)

```
운영자 ─ POST /aiapp/partner/nl/ns/default/intents ─▶ AI-App ─프록시─▶ 모의 자연어 에이전트 (LLM)
모의 에이전트 ─ MCP ─▶ 엔진: list_applications(read) → deploy_application(write) ...
엔진 ─ MCP ─▶ AI-App 도구 (8.1 의 단계)
응답: [partner mock] 답 + 엔진 실행 ID(engineRunId) 와 상태
```

모의 에이전트는 엔진의 상위 도구 5개만 본다. 무엇을 어떤 순서로 실행할지는 엔진이 정하고, 기록도 엔진에 남는다.

### 8.4 인프라 추천 (협력 기관 모의)

AI-App 의 계획 단계가 `POST {AIAPP_RECOMMENDER_URL}/recommendations/specs`, `/image` 를 부르고, 결과의 출처를
`recommended:remote`, `resolved:remote` 로 남긴다. 추천 API 가 없으면 specId·imageId 를 직접 지정해야 한다(422).

## 9. 검증 결과 (2026-10-13)

### 9.1 작업별

| 작업 | 커밋 | 확인한 것 |
|------|------|-----------|
| 추천 로직을 모의로 이동 | `6def661` | 실제 CB-Tumblebug 으로 모의 추천 API 가 후보 5건(g6.2xlarge 등)을 주고 계획 출처가 `recommended:remote`. 추천 API 없으면 422 |
| 공용화 리팩터링 | `b225d4f` | 바꾸기 전과 같은 테스트 184개가 이름·결과 모두 같음 |
| 자동배포/자동제어 엔진 | `836ddc9`, `00ce932` | 가짜 AI-App 으로 워크플로 테스트, 실제 CB-Tumblebug 으로 deploy 10단계. 검토에서 나온 판정 결함(헬스 단계 누락, 노드 0대 업데이트를 성공으로 기록, 카드의 사용 도구 과다)을 `00ce932` 로 고침 |
| LLM 에이전트를 모의로 이동 | `a110a5b` | 이노그리드 바이너리 두 개의 의존 목록에 LLM·모의 패키지 0건(`go list -deps`). 경계 테스트에 일부러 위반 파일을 넣으면 실패 |
| 콘솔 구역 분리 | `5f05a1e`, `d34dd54`, `dc21415` | 화면별 스크린샷 확인. 녹화 중 발견한 표시 결함(엔진 도구 미표시, 답변 마크다운 원문, 목록 번호)을 고침. 답변 렌더러는 escape 를 먼저 해 스크립트·이미지 태그가 글자로만 보임 |
| README | `96c477f` | make 타깃, 환경변수 41개, 경로 37개, 도구 20개를 코드와 대조해 불일치 0 |

전체: `go build ./... && go vet ./... && go test -count=1 ./...` 통과, `gofmt -l .` 빈 출력.

### 9.2 시연 (영상 `ai-agent-proto-2차-시연.mp4`, 4분 38초)

조건: 실제 CB-Tumblebug, 모의 자연어 에이전트의 LLM 은 사내 Ollama `glm-4.7-flash`, dry-run 켬, Bearer 토큰 켬.

| 장면 | 관측 |
|------|------|
| 개요 | 네 프로세스 응답, 엔진 LLM 없음, 이노그리드 구현과 협력 기관 모의가 색으로 구분 |
| 자동화 엔진 deploy (Open WebUI) | 1~4 succeeded(vLLM 계획: aws+us-west-2+g6.2xlarge, 검토 $0.9776/h), 5 blocked, 6~10 waiting |
| 도구 | 엔진이 여는 도구 5개(read 2, write 3), AI-App 기능 도구 15개 중 엔진이 쓰는 12개 표시 |
| 인프라 추천 (모의) | 후보 5건, 계약 요약, 협력 기관이 할 일 |
| 자연어 지시 (모의) | 실제 197초. `list_applications`(read) → `deploy_application`(write, 차단), 엔진 실행 `eng-dm3oeazjjgh0` 이 같은 10단계 |
| 아카이브 | 엔진 실행 2건, 자연어 지시 1건(blocked). AI-App 직접 배포 호출은 이 시연에서 0건 |

### 9.3 확인하지 못한 것

- 실배포(dry-run 끔): VM 생성, 응용 엔드포인트 주입, 원격 헬스체크, heal 의 실제 재설치.
- 협력 기관 실제 API, 실제 에이전트 레지스트리.
- 답변 렌더러의 하위 목록 중첩은 시연 답변에 하위 목록이 없어 화면으로는 보지 못했다(단위 확인만).
- docker compose 로 네 서비스를 실제로 띄우는 것(설정 검증만).
