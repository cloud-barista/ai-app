# ai-agent-proto - AI 응용 자동화 에이전트 (1차년도 프로토타입)

> AI-MCMP 과제 ㈜이노그리드 담당 1차년도 항목 중 두 블록의 프로토타입입니다.
>
> - **AI-App ④** AI반도체 기반 **AI 응용 배포 및 운용** (메타데이터 규격 / 등록·배포 / 라이프사이클)
> - **AI-Ops ⑥** **AI 응용 자동화 에이전트** (구조 설계 / 배포·제어 자동화 / 정보 아카이빙)

## 2026-10 구조 변경 - AI-App 서비스와 자동화 에이전트로 분리

1차년도 프로토타입은 한 프로세스였습니다. 지금은 두 프로세스이고, 둘은 **MCP** 로 연결됩니다.
설계와 그 근거는 `docs/design/app-agent-split.md` 에 있습니다.

| 프로세스 | 소스 | 포트 | 맡는 것 |
|---|---|---|---|
| **AI-App 서비스** | `cmd/ai-app-server` | `8090` | 응용 카탈로그, 배포·업데이트·라이프사이클, 도구 15개와 dry-run 게이트, **MCP 서버(`/aiapp/mcp`)**, 운영자 콘솔, 배포·제어 아카이브 |
| **AI 응용 관리 자동화 에이전트** | `cmd/ai-agent` | `8091` | 자연어 지시(`/intents`), LLM 반복 루프, 지시 실행 아카이브, 에이전트 카드(`/agent/card`) |

- 에이전트는 도구를 직접 갖고 있지 않습니다. 기동할 때 AI-App 의 MCP 서버(`AIAPP_APP_MCP_URL`)에서
  도구 목록과 등급, dry-run 상태를 받아 오고, 도구 호출도 MCP 로 합니다.
  **dry-run 게이트는 AI-App 쪽에 있으므로** 에이전트가 어떻게 부르든 게이트를 건너뛸 수 없습니다.
- 콘솔과 운영자는 AI-App 주소(`8090`) 하나만 알면 됩니다. AI-App 이 `POST /aiapp/ns/{nsId}/intents` 와
  `/aiapp/agent/*` 를 에이전트(`AIAPP_AGENT_URL`)로 넘깁니다.
- 아래 1절·10절·12절의 실측 기록은 한 프로세스 시절에 잰 것이라 그대로 두었습니다.
  REST 경로(`/aiapp/...`)는 분리 뒤에도 같습니다.

## 1. 이게 뭘 하는 건가 - 한 문장

> **AI 응용(vLLM 같은 추론 서버)의 메타데이터를 등록해 두고, 운영자가 자연어로 지시하면
> LLM 이 CB-Tumblebug API 호출을 결정해 AI반도체 VM 에 그 응용을 올리고 제어하며,
> 그 과정을 전부 아카이빙하는 Go 서비스.**

실제로 확인된 동작은 이렇습니다 (2026-09-01, 사내 CB-Tumblebug `<tumblebug-host>:1323`).

```
$ curl -X POST .../aiapp/ns/default/deployments -d '{"appId":"vllm-llama31-8b","infraName":"vllm-lab-01"}'

dryRun  : true
message : Dry run: vllm-lab-01 reviewed but not provisioned
spec    : aws+us-west-2+g6.2xlarge | recommended | gpu NVIDIA L4 x1 (24 GB)
image   : ami-0358c7fed3fc2fbd0    | resolved    | accelerator image: ships the vendor driver
estCost : $0.9776/hour
review  : creationViable=true, "All VMs can be created successfully"
```

**입력은 "GPU 24GiB 이상"이라는 응용의 요구사항 하나뿐입니다.**
어느 CSP 의 어느 리전에 어떤 인스턴스 타입을 쓸지, 어떤 이미지로 부팅할지는 이 서비스가 정합니다.

## 2. 왜 이렇게 만들었나 - 일곱 개의 설계 결정

근거는 전부 과제 산출물인 기술 조사 문서의 사례 조사에 있습니다. 그 문서는 이 저장소 밖에 있습니다.

| # | 결정 | 근거 |
|---|---|---|
| 1 | 계층을 넷으로 자름 (카탈로그 / 도구 / 에이전트 / 아카이브) | 컨소시엄 담당 경계가 아직 유동적이라, 어느 쪽이 넘어가도 계층 하나만 잘리게 |
| 2 | 한 번에 계획을 다 뽑지 않고 **반복 루프** | 배포는 체인이고 중간 결과가 다음 호출을 바꿈 (HolmesGPT 의 ReAct 구조) |
| 3 | **런타임 축과 가속기 축을 분리** | 국산 NPU 는 vLLM 플러그인으로 붙음. 합치면 가속기 늘 때마다 규격이 깨짐 |
| 4 | VM 생성과 응용 설치를 **한 호출로** | CB-Tumblebug `InfraDynamicReq.postCommands` 확인. 별도 SSH 채널 불필요 |
| 5 | **쓰기는 기본적으로 실행 안 됨** (dry-run 기본 ON) | AWS MCP 도 쓰기를 안 열었고, CB-Tumblebug MCP 는 스스로 PoC 경고 |
| 6 | **가속기 판독의 바닥을 PCI 버스로** | 벤더 도구는 드라이버가 붙어야 답하는데, 잡으려는 실패가 "드라이버가 안 붙음" 이다. 쿠버네티스 NFD 도 같은 방법을 쓴다 |
| 7 | **설정 없이 기동** | 클라우드에 안 닿는 기능이 대부분인데, 설정 하나 없다고 그 전부를 못 쓰게 할 이유가 없다 |

## 3. 구조

```
   자연어 지시 ─────────────────────────┐
                                       v
POST /aiapp/ns/{ns}/intents      [ internal/agent ]                cmd/ai-agent (8091)
                                  LLM 에 "무엇을 부를지" 묻는 반복 루프
                                       │  tool_use / tool_result
                                       v
                                 [ internal/mcpclient ]
                                       │
  - - - - - - - - - - - - - - - - - - -│- MCP (Streamable HTTP)  /aiapp/mcp - - - -
                                       v
                                 [ internal/mcpserver ]            cmd/ai-app-server (8090)
                                       │
                                       v
                                 [ internal/tools ]
                                  등급(read/review/write/destructive)별 게이트
                                       │
                    ┌──────────────────┼──────────────────┐
                    v                  v                  v
            [ internal/catalog ]  [ internal/deploy ]  [ internal/archive ]
             AI 응용 메타데이터    요구사항 -> 스펙/이미지   JSON Lines 레코드
                                  -> 배포 요청 조립
                                  -> 가속기 프로브
                                     (pci + 7종 벤더 도구)
                                  -> 서빙 헬스 확인
                                       │
                                       v
                              [ internal/tumblebug ]
                                       │
                                       v
                             CB-Tumblebug REST API
```

`internal/llm` 은 Anthropic Messages API 어댑터이고 `internal/rest` 는 Echo 라우팅입니다.
점선 위는 에이전트 프로세스, 아래는 AI-App 프로세스입니다.

계획 모델은 `AIAPP_LLM_BASE_URL` 에 `/v1/messages` 를 붙여 부르므로 **Anthropic Messages
형식을 받는 엔드포인트면 무엇이든** 됩니다. Ollama 도 0.15 부터 `/v1/messages` 를 받아서 그대로 붙습니다
(2026-10-07, `glm-4.7-flash` 로 시연). Ollama 는 키를 보지 않지만 `AIAPP_LLM_API_KEY` 가 비어 있으면
에이전트가 계획 모델이 없다고 보고 `/intents` 에 503 을 내므로, 아무 값이나 넣습니다.

### 3-1. 왜 도구 계층이 따로 있나

에이전트를 거치지 않고 REST 로 직접 배포할 수도 있습니다. 두 경로가 **같은 서비스 계층**을 쓰므로,
LLM 이 붙든 안 붙든 동작과 아카이브 형태가 같습니다.
그리고 도구 목록은 `GET /aiapp/tools` 로 노출되므로, 나중에 AI-Ops 프레임워크 골격이
BESP/ETRI 것으로 정해져도 그 위에 같은 도구를 다시 얹을 수 있습니다.
2026-10 분리 뒤에는 같은 도구가 MCP 서버(`/aiapp/mcp`)로도 나가고, 이 저장소의 에이전트도
그 MCP 서버를 통해서만 도구를 부릅니다.

## 4. AI 응용 메타데이터 규격 (1차년도 ④ 산출물)

`internal/model/app.go` 의 `AppSpec` 입니다. 핵심은 **런타임과 가속기가 서로 다른 필드**라는 점입니다.

```json
{
  "id": "vllm-llama31-8b",
  "name": "vLLM Llama 3.1 8B Instruct",
  "version": "0.1.0",
  "runtime": "vllm",                       // 엔진 축
  "runtimeVersion": "0.6.3",
  "model":  { "id": "meta-llama/Llama-3.1-8B-Instruct", "format": "safetensors", "sizeGiB": 16 },
  "accelerator": {                          // AI반도체 축 (엔진과 독립)
    "type": "gpu",
    "minCount": 1,
    "minMemoryGiB": 22
  },
  "resources": { "minVCPU": 8, "minMemoryGiB": 32, "rootDiskGiB": 120 },
  "serving":   { "port": 8000, "healthPath": "/health", "protocol": "http" },
  "install":   { "userName": "cb-user", "timeoutMinutes": 45, "commands": ["..."] },
  "deployTargets": ["vm"]
}
```

| 필드군 | 무엇에 쓰이나 |
|---|---|
| `runtime` / `runtimeVersion` | 배포 단위. 가속기가 GPU 에서 NPU 로 바뀌어도 이 값은 안 바뀜 |
| `accelerator` | **그대로 CB-Tumblebug 스펙 필터로 변환됨** (아래 5절) |
| `model` | ③ AI 모델 통합 관리와의 접점. 여기서는 참조만 하고 등록은 안 함 |
| `serving` | ⑤ AI 응용 모니터링과의 접점. 살아 있는지 확인할 포트와 경로. `healthPath`·`apiBasePath` 는 `/` 로 시작해야 하고 `@`·`#`·공백을 넣을 수 없음 (`@` 가 들어가면 노드의 헬스 확인 요청이 다른 호스트로 감) |
| `kind` / `requires` | `model-service`(모델을 올리고 추론 API 를 엶) 와 `application`(그 API 를 부르는 응용) 을 가름. `application` 은 `requires` 에 쓰는 모델 서비스와, 그 엔드포인트를 넘겨받을 환경변수 이름을 적음 |
| `install` | CB-Tumblebug `postCommands` 로 그대로 실림 |
| `deployTargets` | 1차년도 `vm`. 3차년도 `container` 가 들어와도 규격이 안 깨지게 |

기본 등록 항목 4개 (`internal/catalog/seed.go`): 모델 서비스 vLLM Llama 3.1 8B / Ollama Qwen2.5 7B / Triton ResNet-50,
응용 Open WebUI (Llama 3.1 8B 모델 서비스를 `requires` 로 씀).

## 5. 핵심 변환 - 응용 요구사항이 어떻게 인프라가 되나

이 프로토타입에서 실제로 새로 만든 부분이고, 사례 조사에서 아무도 안 하고 있던 부분입니다.

```
AppSpec.accelerator                     CB-Tumblebug POST /recommendSpec
  type: "gpu"           ──────────────>   metric acceleratorType   = gpu
  minCount: 1           ──────────────>   metric acceleratorCount  >= 1
  minMemoryGiB: 22      ──────────────>   metric acceleratorMemoryGB >= 22
AppSpec.resources
  minVCPU: 8            ──────────────>   metric vCPU              >= 8
  minMemoryGiB: 32      ──────────────>   metric memoryGiB         >= 32
                        priority: cost, 가장 싼 것부터
                                          |
                                          v
                        aws+us-west-2+g6.2xlarge (NVIDIA L4 24GB, $0.9776/h)
                                          |
                        POST /ns/system/resources/searchImage
                        matchedSpecId + osType, 같은 벤더의 GPU 이미지 우선
                                          v
                        ami-0358c7fed3fc2fbd0 (isGPUImage=true, 드라이버 포함)
                                          |
                                          v
                        InfraDynamicReq { nodeGroups + postCommands(install.commands) }
                                          |
                        POST /ns/{ns}/infraDynamicReview   <- 아무것도 안 만듦
                                          v
                        creationViable / estimatedCost
                                          |
                        [ dry-run 게이트 ]  <- 기본값에서 여기서 멈춤
                                          v
                        POST /ns/{ns}/infraDynamic
                                          |
                        POST /ns/{ns}/resources/securityGroup/{sg}/rules
                        serving.port 인바운드 개방
                                          |
                        POST /ns/{ns}/cmd/infra/{infra}
                        PCI 버스 재고 + 벤더 도구 7종 -> 카탈로그 약속과 대조
                        (gpu / npu / tpu, 출처를 값마다 표기)
                                          |
                        POST /ns/{ns}/cmd/infra/{infra}
                        노드 안에서 curl http://127.0.0.1:{port}{healthPath}
                        (waitHealthy=true 면 AIAPP_HEALTH_TIMEOUT 안에서 AIAPP_HEALTH_INTERVAL 간격으로 반복)
                                          v
                        http://<publicIP>:8000/health
```

**CSP 이름도 인스턴스 타입도 코드에 없습니다.** 가속기 조건만 있습니다.
그래서 CSP 를 추가해도 이 코드는 안 바뀌고, 5차년도 목표인 "5개 이상 AI반도체 클라우드"가
카탈로그 항목 추가로 해결됩니다.

실측 예 (같은 서비스, 응용만 바꿈):

| 응용 | 가속기 요구 | 고른 스펙 | 시간당 |
|---|---|---|---|
| vLLM Llama 3.1 8B | gpu x1, >=22GiB, 8vCPU, 32GiB | `aws+us-west-2+g6.2xlarge` NVIDIA L4 24GB | $0.9776 |
| Ollama Qwen2.5 7B | gpu x1, >=8GiB, 4vCPU, 16GiB | `aws+us-west-2+g4ad.xlarge` AMD RADEON PRO V520 9GB | $0.3785 |

## 6. 안전장치

### 6-1. 도구 등급

`GET /aiapp/tools` 로 그대로 확인할 수 있습니다.

| 등급 | 도구 | dry-run 게이트 |
|---|---|---|
| `read` | `list_ai_apps`, `find_ai_apps`, `get_ai_app`, `recommend_accelerator_spec`, `resolve_node_image`, `plan_deployment`, `list_deployments`, `get_deployment_status`, `probe_accelerator`, `check_serving_health` | 해당 없음. 항상 실행 |
| `review` | `review_deployment` | 해당 없음. 아무것도 안 만듦 |
| `write` | `deploy_ai_app`, `update_deployment`, `control_deployment` | **걸림** |
| `destructive` | `delete_deployment` | **걸림** |

### 6-2. 실행 여부를 LLM 이 정하지 않는다

`AIAPP_TUMBLEBUG_DRY_RUN` 은 **서비스 설정**이지 요청 파라미터가 아닙니다.
LLM 이 "확실하니 실행하자"고 해도 `deploy_ai_app` 은 계획과 review 까지만 하고 멈춥니다.
시스템 프롬프트에는 게이트가 켜져 있다고 알려 주는데, 이것은 **지키라고 부탁하는 게 아니라**
운영자에게 "다 했습니다"라고 잘못 보고하지 않게 하려는 것입니다. 강제는 도구 계층에서 합니다.

### 6-3. 어느 AI 응용인지는 규칙이 먼저 좁힌다

`"라마 추론서버 올려줘"` 가 카탈로그의 어느 항목인지를 정하는 일은 원래 계획 모델에게
전부 맡겨져 있었습니다. 전체 AppSpec 을 통째로 보여주고 고르게 하는 방식인데,
**측정할 수 없고 회귀를 잡을 수 없습니다.**

지금은 규칙 기반 매처(`internal/catalog/resolve.go`)가 순위를 매겨 후보를 넘깁니다.
모델이 여전히 고르지만, 고를 대상이 좁혀지고 근거가 붙습니다.

- **구조화된 필드에 걸려야 후보가 됩니다.** id·별칭·모델명·런타임·포맷이 그것입니다.
  설명문 단어는 동점을 가르는 데만 쓰이고 혼자서는 후보를 만들지 못합니다.
  흔한 단어 하나로 확신에 찬 오답을 내는 것보다 "없다" 가 낫습니다.
- **별칭은 토큰이거나 단어 시작에서만 맞습니다.** 띄어쓰기 없이 쓰는 한국어(`라마모델`)를
  받으면서도 `ollama` 가 `llama` 별칭에 걸리지 않게 하는 선입니다.
- 후보가 없으면 빈 목록을 돌려주고, 시스템 프롬프트가 **가장 비슷한 것을 배포하지 말고
  없다고 답하라**고 지시합니다.

이것은 6-2 의 "실행 여부를 LLM 이 정하지 않는다" 를 대상 선정 단계에 적용한 것입니다.

### 6-4. 서빙 포트는 배포가 열어 준다

CB-Tumblebug 의 기본 보안그룹 템플릿 `sg-default` 는 **TCP/UDP 1-65535 를 전부 엽니다.**
템플릿 설명이 스스로 "development/testing use. For production, use a more restrictive
template" 이라고 적어 두었습니다. 즉 **기본값에서는 서빙 포트가 이미 열려 있고,
운영에서 제한적인 템플릿으로 바꾸는 순간 닫힙니다.**

그때 응용은 **아무도 닿을 수 없는 포트에서 응답**하고, 배포는 끝난 것처럼 보이는데
엔드포인트만 죽습니다. 그래서 생성 직후 노드의 보안그룹에 `serving.port` 인바운드 규칙을
명시적으로 넣습니다. 전부 열린 기본값에 기대지 않는 것이고, 제한적인 템플릿으로 바꿔도
배포가 안 깨지게 하는 것이 목적입니다.

결과의 `serving` 에 열렸는지·어디로 접속하는지를 담아 돌려주므로, 호출자는 엔드포인트
주소를 따로 조립하지 않아도 됩니다. 규칙 추가에 실패해도 배포 자체를 버리지 않습니다.
노드는 이미 떠서 과금 중이므로 **"포트가 안 열렸다"고 알려 주는 편이 완료된 배포를
폐기하는 것보다 낫습니다.**

현재 소스 범위는 `0.0.0.0/0` 입니다. 카탈로그 항목만으로는 호출자를 좁힐 근거가 없어서
접근 정책 항목으로 미루되, 열린 범위를 응답에 명시합니다.

### 6-5. 배포 성공과 가속기 사용 가능은 다른 상태다

노드가 `Running` 이라고 가속기가 쓸 수 있다는 뜻이 아닙니다. 벤더가 안 맞는 이미지를 붙이면
부팅은 되고 드라이버는 안 붙습니다. 스펙이 약속한 개수·메모리와 실제가 다를 수도 있습니다.
전부 **응용이 장치를 처음 만질 때가 되어서야** 드러납니다.

그래서 배포 직후 노드에 물어보고 카탈로그의 약속과 대조합니다
(`internal/deploy/accelerator.go`).

#### 벤더 도구가 바닥이면 안 된다

처음에는 `nvidia-smi` 하나만 돌렸습니다. **그 구조가 틀렸습니다.**

벤더 판독 도구는 전부 **드라이버가 이미 붙어야** 답합니다. 그런데 여기서 잡으려던 실패가 정확히
**드라이버가 안 붙은 상태**입니다. 그 상태에서 벤더 도구는 **가속기가 없을 때와 똑같이 침묵**합니다.

```
이전:  findings = ["serving-1: no NVIDIA driver tool on the node, ..."]
       ^ 카드 없는 노드 · 드라이버 안 붙은 노드 · AMD 카드 꽂힌 노드가 전부 이 한 줄
```

**지금은 PCI 버스가 바닥입니다** (`internal/deploy/acceleratorpci.go`).
아무것도 설치돼 있지 않아도 답합니다.

```
   /sys/bus/pci/devices          <- 항상 실행. 설치 불필요
      클래스 0x03(GPU) · 0x12(Processing accelerator) 만 채택
      벤더 ID -> 이름 · 종류(gpu/npu/tpu) · 모델명
            |
            v
     [ 장치 재고 ]  BDF · 커널 드라이버 · PF/VF
            ^
            |  BDF 로 조인 (도메인 자리수·대소문자 정규화)
   nvidia-smi · amd-smi · rocm-smi · rbln-stat · furiosa-smi · hl-smi · tpu-info
   있으면 메모리 · UUID · 드라이버 버전 · MIG 를 덧칠
```

**조인 방향이 요점입니다.** 벤더 목록을 바닥으로 삼으면 드라이버가 안 붙은 장치가 목록에서
빠지는데, 그게 바로 잡으려던 장치입니다.

이 방법은 지어낸 것이 아닙니다. **쿠버네티스 Node Feature Discovery 가 국산 NPU 를 판정하는
방법이 같습니다** - Rebellions 는 `pci-1eff.present`, FuriosaAI 는 `pci-1200_1ed2.present`
라벨입니다. `1200` 이 PCI 클래스 0x12, `1ed2` 가 벤더 ID 입니다.

#### 노드에서 도는 것

한 덩어리 셸이 섹션으로 나뉜 텍스트를 돌려주고, **파싱은 전부 Go 에서** 합니다
(`internal/deploy/acceleratorprobe.go`). 셸은 모으기만 합니다 - 셸은 나쁜 파서입니다.

```
===AIAPP-PROBE pci===
0000:01:00.0 0x030000 0x10de 0x2184 nvidia -
===AIAPP-PROBE nvidia-smi===
0, GPU-6f3a..., NVIDIA T4G, 15360, 595.71.05, N/A, 00000000:00:1E.0
===AIAPP-PROBE amd-smi===
===AIAPP-PROBE rocm-smi===
===AIAPP-PROBE rbln-stat===
===AIAPP-PROBE furiosa-smi===
===AIAPP-PROBE hl-smi===
===AIAPP-PROBE tpu-info===
===AIAPP-PROBE end===
```

**빈 섹션도 옵니다.** "도구가 없다" 와 "도구가 있는데 답이 없다" 를 가르기 위해서입니다.
마커가 아예 없는 출력은 이전 프로브를 쓰는 노드로 보고 nvidia 출력으로 읽습니다.

#### 판독 규칙

앞의 셋은 유지, 넷째가 이번에 추가됐습니다.

- **드라이버가 `N/A` 로 답한 값은 0 으로 적지 않습니다.** 0 은 측정값으로 읽힙니다.
  `memoryKnown` 같은 플래그로 "모른다" 와 "0 이라고 답했다" 를 가릅니다.
- **UUID 의 `GPU-` 접두사를 떼지 않습니다.** 떼면 한 카드가 두 개의 신원으로 갈라집니다.
- **`mig.mode.current` 를 읽습니다.** MIG 가 켜지면 드라이버가 장치 단위 사용률 카운터를 끄기
  때문에, 이걸 모르면 **놀고 있는 카드와 카운터가 꺼진 카드를 구분할 수 없습니다.**
- **(신규) `source` 로 출처를 적습니다.** 버스에서 읽은 것과 드라이버가 답한 것은 진실의 무게가
  다릅니다. 안 적고 합치면 **재고 목록이 측정값처럼 보입니다.**

**SR-IOV 가상 함수를 가릅니다.** Rebellions PCI ID 가 `1220`(PF)/`1221`(VF) 쌍이라,
안 가르면 가상화 호스트에서 **카드 한 장이 여러 장으로 보고됩니다.** 개수 대조는 PF 만 셉니다.

#### 드라이버가 안 붙은 노드의 응답

```json
{ "sources": ["pci"],
  "devices": [{ "kind": "npu", "vendor": "rebellions", "name": "RBLN-CA22",
                "source": "pci", "pciBusId": "0000:51:00.0",
                "kernelDriver": "", "memoryKnown": false }],
  "findings": ["RBLN-CA22 #0 is on the bus with no kernel driver bound, so the node is
                billing for a device nothing can use"] }
```

**이 한 줄이 이번 변경의 전부입니다.** 예전에는 이 노드와 가속기가 없는 노드가 구분되지 않았습니다.

**대조 결과는 findings 이지 에러가 아닙니다.** 노드는 이미 떠서 과금 중이므로,
배포를 무르는 것보다 무엇이 다른지 알려 주는 편이 낫습니다.
프로브가 아예 답하지 못해도 배포는 그대로 둡니다.

#### 어디까지 실측했나

| 경로 | 상태 |
|---|---|
| PCI 인벤토리 파싱 | **로컬 `/sys/bus/pci/devices` 실제 출력으로 단위테스트** |
| `nvidia-smi` 파서 | **실증 배포 출력으로 단위테스트** (NVIDIA T4G, 595.71.05) |
| `amd-smi` 파서 | AMD CLI 소스(`amdsmi_cli`)의 출력 형식. **실기기 미검증** |
| `rocm-smi` 파서 | 실제 AMD 장비에서 받은 출력 캡처(Telegraf 테스트 데이터, `internal/deploy/testdata/rocm-smi`)로 단위테스트. **이 프로토타입의 실기기 왕복은 미검증** |
| `rbln-stat`·`furiosa-smi`·`hl-smi`·`tpu-info` 파서 | 공개 문서 형식. **실기기 미검증 - 장비 없음** |
| 원격 실행 왕복 | NVIDIA 노드에서만 실증. 다벤더 스크립트는 **미검증** |

### 6-6. 멱등성

배포 이름(`infraName`)이 키입니다. 생성 전에 네임스페이스의 Infra 목록을 조회해
같은 이름이 있으면 409 로 거부합니다. 에이전트가 재시도해도 두 번 과금되지 않습니다.

> 목록 조회로 확인하는 이유: CB-Tumblebug 은 **없는 Infra 를 조회하면 404 가 아니라 500** 과
> `"The infra <id> does not exist."` 를 돌려줍니다 (2026-09-01 실측). 단건 조회로는
> "없음"과 "상류 장애"를 구분할 수 없습니다.

### 6-7. review 를 항상 먼저 한다

`deploy_ai_app` 은 내부적으로 `plan -> 중복확인 -> review -> (게이트) -> create` 순서가 고정입니다.
review 는 아무것도 안 만들고 `creationViable` 과 시간당 비용을 돌려줍니다.
**이 게이트가 실제로 일을 했습니다**: 초기 구현이 이미지 ID 를 `ubuntu22.04` 로 넣었는데
review 가 `"image 'ubuntu22.04' not found in DB or CSP"` 로 잡아냈고, 그래서 5절의
이미지 조회 단계를 넣었습니다. 추측한 값이 CSP 까지 안 갔습니다.

## 7. 아카이빙 (1차년도 ⑥ 산출물)

`runs.jsonl` 에 한 줄에 한 레코드씩 append 합니다. 2026-10 분리 뒤에는 아카이브가 둘입니다.

| 아카이브 | 쓰는 쪽 | 위치 (기본값) | 무엇이 남나 |
|---|---|---|---|
| 배포·제어 | AI-App 서비스 | `AIAPP_ARCHIVE_DIR` (`./data/archive`) | 에이전트를 거치지 않은 직접 REST 호출 |
| 지시 실행 | 자동화 에이전트 | `AIAPP_AGENT_ARCHIVE_DIR` (`./data/agent-archive`) | 자연어 지시 한 건의 반복 루프 전체 |

레코드에는 **최종 결과만이 아니라** 다음이 전부 들어갑니다.

- 운영자 지시(`intent`)
- LLM 이 고른 도구와 **인자 전체**(`steps[].input`)
- 도구 응답(`steps[].output`)
- **게이트가 막았는지**(`steps[].executed`)와 등급(`steps[].grade`)
- 소요 시간, 토큰 사용량, 실패 사유

중간 단계를 남기는 이유는 감사 때문만이 아닙니다.
2차년도 항목이 "배포 정보 수집 및 **추론 재적용**"이고, 그 입력이 이 레코드입니다.
결과만 남기면 왜 그 결정을 했는지가 사라져서 되먹일 수 없습니다.

에이전트를 거치지 않은 직접 REST 호출도 같은 형태로 남습니다 (`kind`: `deployment` / `control`).
조회는 AI-App 의 `GET /aiapp/archive` 가 배포·제어 아카이브를, `GET /aiapp/agent/archive` 가 지시 실행 아카이브를 돌려줍니다.

실측 예:

```
run-dl3xxm60p7x7  intent   Llama 3.1 8B 추론 서버를 GPU 한 장짜리 노드에 올려줘
  1. list_ai_apps                 read   executed=true
  2. recommend_accelerator_spec   read   executed=true   {"appId":"vllm-llama31-8b","limit":2}
  3. deploy_ai_app                write  executed=false  <- 게이트가 막음
```

## 8. API

`GET /aiapp/api/index.html` 에서 Swagger UI 로 볼 수 있습니다.
아래는 전부 AI-App 서비스(`8090`) 주소입니다. **(에이전트)** 라고 적은 것은 AI-App 이 에이전트로 넘깁니다.
`AIAPP_AUTH_TOKEN` 을 설정하면 `/readyz`, `/ui`, `/api/*`, `/agent/readyz` 를 뺀 전부가 `Authorization: Bearer <토큰>` 을 요구합니다.

| 메서드 | 경로 | 하는 일 |
|---|---|---|
| GET | `/aiapp/ui` | **운영자 콘솔** (이 서비스의 API 를 실제로 호출하는 화면) |
| GET | `/aiapp/readyz` | 준비 상태 |
| GET | `/aiapp/apps` | 등록된 AI 응용 목록 |
| POST | `/aiapp/apps` | AI 응용 등록 (같은 id 면 새 버전으로 교체) |
| GET | `/aiapp/apps/search?q=` | **자연어로 AI 응용 후보 순위 조회** (근거 신호 포함) |
| GET | `/aiapp/apps/{appId}` | AI 응용 조회 |
| DELETE | `/aiapp/apps/{appId}` | AI 응용 삭제 |
| GET | `/aiapp/apps/{appId}/specs` | 가속기 요구사항에 맞는 스펙 추천 |
| POST | `/aiapp/ns/{nsId}/deployments/plan` | 배포 계획만 만듦 (클라우드 미접촉) |
| POST | `/aiapp/ns/{nsId}/deployments` | 배포 (review 후, 게이트 통과 시 생성). `sgTemplateId` 로 보안그룹 템플릿 지정 가능. 생성 뒤 서빙 포트 응답을 한 번 확인해 `health` 에 담음. `?waitHealthy=true` 면 모든 노드가 응답하거나 `AIAPP_HEALTH_TIMEOUT` 이 지날 때까지 다시 확인 (응답이 없어도 배포는 실패로 안 바뀜) |
| GET | `/aiapp/ns/{nsId}/deployments` | 배포 목록 |
| GET | `/aiapp/ns/{nsId}/deployments/{infraId}/status` | 배포 상태 |
| GET | `/aiapp/ns/{nsId}/deployments/{infraId}/accelerator` | **노드의 실제 가속기 조회 + 카탈로그 약속과 대조** |
| GET | `/aiapp/ns/{nsId}/deployments/{infraId}/health` | 노드 안에서 `curl 127.0.0.1:{port}{healthPath}` 로 서빙 응답 확인. 배포 라벨에 응용이 없으면 `?appId=` 필요. 읽기 전용이라 dry-run 과 무관 |
| POST | `/aiapp/ns/{nsId}/deployments/{infraId}/update` | 등록된 응용의 현재 버전을 노드를 그대로 둔 채 다시 설치 (설치 명령 재실행). 게이트에 걸림 |
| POST | `/aiapp/ns/{nsId}/deployments/{infraId}/control` | suspend / resume / reboot / terminate |
| DELETE | `/aiapp/ns/{nsId}/deployments/{infraId}` | 배포 삭제 |
| POST | `/aiapp/ns/{nsId}/intents` | **자연어 지시 -> 에이전트 실행** (에이전트) |
| GET | `/aiapp/tools` | 에이전트가 부를 수 있는 도구 목록과 등급 |
| POST | `/aiapp/mcp` | **같은 도구의 MCP 서버** (Streamable HTTP). 에이전트가 여기로 도구를 부름 |
| GET | `/aiapp/archive` | 배포·제어 아카이브 목록 (최신순) |
| GET | `/aiapp/archive/{runId}` | 배포·제어 아카이브 단건 |
| GET | `/aiapp/agent/readyz` | 에이전트 준비 상태 (에이전트) |
| GET | `/aiapp/agent/card` | 에이전트 카드: 이름, 계획 모델, MCP 주소, 받은 도구, 레지스트리 등록 결과 (에이전트) |
| GET | `/aiapp/agent/archive` | 지시 실행 아카이브 목록 (에이전트) |
| GET | `/aiapp/agent/archive/{runId}` | 지시 실행 아카이브 단건 (에이전트) |

## 9. 실행 방법

### 9-1. 설정 없이 바로

프로세스가 둘이라 터미널도 둘입니다. 에이전트는 기동할 때 AI-App 의 MCP 서버에서 도구를 받아 오므로
`run-app` 을 먼저 띄웁니다.

```sh
make run-app      # AI-App 서비스, :8090
make run-agent    # 자동화 에이전트, :8091 (다른 터미널)
open http://localhost:8090/aiapp/ui
```

바이너리가 필요하면 `make build` 가 `bin/ai-app-server` 와 `bin/ai-agent` 둘을 만듭니다.

**끝입니다.** env 파일을 만들 필요가 없습니다. 모든 설정에 기본값이 있습니다.
아래는 설정 없이 띄웠을 때의 기동 로그입니다 (2026-10-13, JSON 로그를 줄여 옮김).

```
# ai-app-server
WRN CB-Tumblebug base URL was not configured, so deployment calls will not reach a real
    platform  url=http://localhost:1323/tumblebug source=default
INF Placement backends  recommender=local provisioner=local
WRN Dry run is on: deploy, control, update and delete calls are planned but never sent to the cloud;
    direct API calls are archived here, MCP calls by the calling agent
WRN AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open
INF Starting server port=8090 basePath=/aiapp tools=15 agentUrl=http://127.0.0.1:8091

# ai-agent
WRN Planning model is not configured: the intent endpoint will report 503
WRN AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open
INF Connected to the AI-App MCP server  endpoint=http://127.0.0.1:8090/aiapp/mcp tools=15 dryRun=true
INF AIAPP_AGENT_REGISTRY_URL is not set: the agent card is not registered anywhere
INF Starting agent port=8091 basePath=/aiapp model=claude-opus-5 mcpUrl=http://127.0.0.1:8090/aiapp/mcp
```

이 상태에서 **클라우드에 닿지 않는 것은 전부 동작합니다.**

```sh
open http://localhost:8090/aiapp/ui                       # 운영자 콘솔 (아래 9-5)
curl localhost:8090/aiapp/readyz                          # {"message":"Service is ready"}
curl localhost:8090/aiapp/agent/readyz                    # 에이전트 쪽 준비 상태 (AI-App 이 넘겨줌)
curl localhost:8090/aiapp/apps                            # 등록된 응용 4건
curl -G localhost:8090/aiapp/apps/search      --data-urlencode "q=라마 추론서버"                     # 후보 순위 + 판정 근거
curl localhost:8090/aiapp/tools                           # 도구 15개와 등급
curl localhost:8090/aiapp/agent/card                      # 에이전트 카드
curl localhost:8090/aiapp/archive                         # 배포·제어 아카이브
curl localhost:8090/aiapp/agent/archive                   # 지시 실행 아카이브
open http://localhost:8090/aiapp/api/index.html           # Swagger UI
```

클라우드에 닿는 경로와, 계획 모델이 필요한 경로만 실패합니다.

```sh
curl localhost:8090/aiapp/ns/default/deployments/none-01/accelerator
# HTTP 500  {"message":"Accelerator probe failed"}
curl -X POST localhost:8090/aiapp/ns/default/intents -H 'Content-Type: application/json' -d '{"intent":"x"}'
# HTTP 503  {"message":"Planning model is not configured"}
```

**기동 로그가 base URL 과 그 출처(`default`/`environment`)를 반드시 찍습니다.**
기본값을 주는 대신, 잘못된 곳을 가리키는 서비스가 첫 줄에서 그렇다고 말하게 했습니다.

### 9-2. 실제 CB-Tumblebug 에 붙일 때

```sh
cp conf/template-setup.env conf/setup.env
# conf/setup.env 를 채운다 (아래 설정 표 참고)
make run-app
make run-agent    # 다른 터미널
```

`conf/setup.env` 는 **있으면 읽고 없으면 넘어갑니다.** `source` 할 필요가 없어졌습니다.
이미 환경변수로 설정된 값이 파일보다 우선합니다. 두 프로세스가 같은 파일을 읽고, 각자 자기에게 필요한 변수만 씁니다.

### 9-3. Docker

```sh
make docker-up      # docker compose up -d --build
make docker-logs
make docker-down
```

**이쪽도 설정 파일이 필요 없습니다.** `docker-compose.yml` 은 같은 이미지에서 두 서비스를 띄웁니다.

| 서비스 | 포트 | 비고 |
|---|---|---|
| `ai-app-server` | `8090` | 아카이브는 named volume `archive` |
| `ai-agent` | `8091` | `ai-app-server` 가 healthy 가 된 뒤 뜸. MCP 주소는 `http://ai-app-server:8090/aiapp/mcp`, 아카이브는 named volume `agent-archive` |

아래 `docker ps` 출력은 한 프로세스 시절(1차년도)에 받은 것입니다.

```
$ docker compose up -d
$ docker ps
STATUS                  PORTS
Up 10 seconds (healthy) 0.0.0.0:8090->8090/tcp, [::]:8090->8090/tcp

$ curl localhost:8090/aiapp/readyz
{"message":"Service is ready"}
```

실제 CB-Tumblebug 을 쓰려면 `.env` 만 만듭니다.

```sh
cp .env.example .env
# AIAPP_TUMBLEBUG_BASE_URL 등을 채운다
docker compose up -d
```

**compose 에 CB-Tumblebug 를 넣지 않았습니다.** cb-spider 와 DB 가 딸려오는 스택이라,
넣으면 `docker compose up` 이 "이 프로토타입을 띄우는 명령" 이 아니라 "멀티클라우드 플랫폼
전체를 띄우는 명령" 이 됩니다. 컨테이너 안에서 호스트의 Tumblebug 을 보도록
`host.docker.internal` 이 기본값이고, Linux 에서도 되게 `extra_hosts` 를 걸어 두었습니다.

컨테이너에서도 **dry-run 이 기본 `true`** 입니다. 실수로 띄운 컨테이너가 과금되는 자원을
만들 수 없습니다. 아카이브는 named volume(`archive`, `agent-archive`)에 있어 컨테이너보다 오래 삽니다.
compose 는 `AIAPP_AUTH_TOKEN` 하나를 AI-App 의 `AIAPP_AUTH_TOKEN` 과 에이전트의 `AIAPP_AUTH_TOKEN`·`AIAPP_APP_TOKEN` 에
같이 넣습니다. AI-App 이 넘기는 요청은 호출자의 `Authorization` 헤더를 그대로 달고 가기 때문입니다.

### 9-4. 검증 명령

```sh
make verify     # gofmt + go build + go test + golangci-lint
make test       # go test ./...
make swag       # Swagger 재생성
```

### 9-5. 운영자 콘솔

```
http://localhost:8090/aiapp/ui
```

**이 서비스의 API 를 실제로 호출하는 화면입니다.** 녹화본이나 예시 데이터가 아니라,
지금 이 서버가 돌려준 응답을 그립니다.

| 화면 | 부르는 것 | 무엇을 보여주나 |
|---|---|---|
| 개요 | `GET /readyz` · `/tools` · `/agent/readyz` · `/agent/card` | 두 프로세스의 준비 상태, **dry-run 게이트**, 도구 15개의 등급 분포, 에이전트 카드 |
| 응용 카탈로그 | `GET/POST/DELETE /apps` | 메타데이터 규격을 실물로. 등록·삭제가 실제로 됩니다 |
| **자연어 매칭** | `GET /apps/search?q=` | **가장 볼 만한 화면.** 후보 순위와 **왜 골랐는지**(`alias:라마`) |
| 도구와 게이트 | `GET /tools` | 등급별 분류, 입력 스키마, 게이트에 걸리는 것 |
| 자연어 지시 | `POST /ns/{ns}/intents` | 에이전트가 고른 도구와 **게이트가 막았는지** |
| 배포와 라이프사이클 | `plan` · `specs` · `deployments` · `accelerator` · `health` | 계획·스펙 추천·가속기 판독·서빙 응답 확인 |
| 아카이브 | `GET /archive` · `/agent/archive` | 배포·제어 기록과 지시 실행 기록 |

설계에서 지킨 것 넷입니다.

- **모든 패널에 그 화면이 부른 요청과 받은 응답 원문을 접이식으로 답니다.** 콘솔이 무엇도
  덧붙이지 않는다는 것을 확인할 수 있게 하려는 것입니다.
- **실패를 감추지 않습니다.** 서버가 낸 메시지를 그대로 보여 주고, 콘솔이 지어낸 문구로
  바꾸지 않습니다. 이종 클라우드 관리 계층이 안 붙어 있으면 배포 화면은
  `HTTP 500 Deployment lookup failed` 를 그대로 띄웁니다. **그것도 동작의 일부입니다.**
- **쓰기 버튼을 넣지 않았습니다.** 실제 생성·제어·삭제 버튼이 없습니다. dry-run 이 켜져 있어도
  마찬가지입니다. 되돌릴 수 없고 과금되는 행동을 화면의 버튼 하나에 걸지 않는 것이
  이 프로토타입의 안전장치와 같은 취지입니다. 카탈로그 등록·삭제(메모리)만 예외입니다.
- **외부 리소스를 하나도 안 씁니다.** CDN·폰트·스크립트 전부 없어서 망이 끊긴 곳에서도
  그대로 뜹니다. 페이지는 `//go:embed` 로 바이너리에 들어가므로 컨테이너에도 볼륨이 필요 없습니다.

콘솔은 API 와 **같은 오리진**에서 서빙됩니다. API 에 CORS 정책을 여는 것보다,
페이지를 같은 자리에서 내보내는 쪽이 낫다고 판단했습니다.
`AIAPP_AUTH_TOKEN` 을 켜면 콘솔 페이지 자체는 열려 있고, 운영자가 넣은 토큰을 매 호출에 실어 보냅니다.

### 설정

**필수 변수는 없습니다.** 전부 기본값이 있고, `AIAPP_RECOMMENDER=remote`·`AIAPP_PROVISIONER=remote` 일 때만
그 `_URL` 이 필수가 됩니다. 두 프로세스가 같은 설정 로더를 쓰므로 값이 잘못되면 둘 다 기동을 거부합니다.
"쓰는 쪽" 은 그 값을 실제로 쓰는 프로세스입니다 (App = `ai-app-server`, Agent = `ai-agent`).

| 환경변수 | 기본값 | 쓰는 쪽 | 설명 |
|---|---|---|---|
| `AIAPP_SERVER_PORT` | `8090` | App | AI-App 리스닝 포트 |
| `AIAPP_AGENT_PORT` | `8091` | Agent | 에이전트 리스닝 포트 |
| `AIAPP_AGENT_URL` | `http://127.0.0.1:8091` | App | `/intents` 와 `/agent/*` 를 넘길 에이전트 주소 |
| `AIAPP_APP_MCP_URL` | `http://127.0.0.1:8090/aiapp/mcp` | Agent | 도구를 받아 올 AI-App MCP 서버 |
| `AIAPP_AUTH_TOKEN` | | App, Agent | 설정하면 `/readyz`·`/ui`·`/api/*`·`/agent/readyz` 를 뺀 전부가 이 bearer 토큰을 요구. 비우면 인증 꺼짐(기동 로그가 경고). 넘긴 요청은 헤더를 그대로 달고 가므로 두 프로세스에 같은 값을 넣음 |
| `AIAPP_APP_TOKEN` | | Agent | 에이전트가 AI-App MCP 서버에 보내는 bearer 토큰. AI-App 의 `AIAPP_AUTH_TOKEN` 과 같은 값 |
| `AIAPP_TUMBLEBUG_BASE_URL` | `http://localhost:1323/tumblebug` | App | 예 `http://<host>:1323/tumblebug`. **없어도 기동합니다.** 기동 로그가 출처를 찍습니다 |
| `AIAPP_TUMBLEBUG_USERNAME` / `_PASSWORD` | | App | basic auth |
| `AIAPP_TUMBLEBUG_CREDENTIAL_HOLDER` | | App | `x-credential-holder` 헤더 값. 비우면 CB-Tumblebug 기본 holder |
| `AIAPP_TUMBLEBUG_TIMEOUT` | `20m` | App | Infra 생성이 동기라 넉넉해야 함. `AIAPP_PROVISIONER=remote` 호출에도 같은 값을 씀 |
| `AIAPP_TUMBLEBUG_IMAGE_NAMESPACE` | `system` | App | 이미지 카탈로그가 있는 네임스페이스 |
| `AIAPP_TUMBLEBUG_SPEC_NAMESPACE` | `system` | App | 스펙 카탈로그가 있는 네임스페이스 |
| `AIAPP_TUMBLEBUG_DEFAULT_OS_TYPE` | `ubuntu 22.04` | App | 응용이 OS 를 안 정했을 때 |
| **`AIAPP_TUMBLEBUG_DRY_RUN`** | **`true`** | App | **쓰기 게이트. 생성·업데이트·제어·삭제를 막음. `false` 로 바꾸면 실제로 과금됨.** `probe_accelerator`·`check_serving_health` 의 읽기 전용 원격 명령은 막지 않음 |
| `AIAPP_HEALTH_TIMEOUT` | `15m` | App | `waitHealthy=true` 배포가 서빙 응답을 기다리는 상한 |
| `AIAPP_HEALTH_INTERVAL` | `20s` | App | 그 사이 다시 확인하는 간격 |
| `AIAPP_RECOMMENDER` | `local` | App | 스펙·이미지를 누가 고르나. `local` = CB-Tumblebug 카탈로그, `remote` = 외부 추천 서비스 |
| `AIAPP_RECOMMENDER_URL` / `_TOKEN` | | App | `remote` 일 때 주소(필수)와 bearer 토큰 |
| `AIAPP_PROVISIONER` | `local` | App | Infra 검토·생성·업데이트를 누가 하나. `local` = CB-Tumblebug 직접, `remote` = 외부 배포 서비스 |
| `AIAPP_PROVISIONER_URL` / `_TOKEN` | | App | `remote` 일 때 주소(필수)와 bearer 토큰 |
| `AIAPP_LLM_BASE_URL` | `https://api.anthropic.com` | Agent | Messages API 엔드포인트. `/v1/messages` 를 받는 곳이면 됨 (3절) |
| `AIAPP_LLM_API_KEY` | | Agent | 비어 있으면 `/intents` 만 503, 나머지는 정상 동작 |
| `AIAPP_LLM_MODEL` | `claude-opus-5` | Agent | 계획 수립 모델 |
| `AIAPP_LLM_MAX_TOKENS` | `16000` | Agent | 모델 호출 한 번의 최대 출력 토큰 |
| `AIAPP_LLM_TIMEOUT` | `5m` | Agent | 모델 호출 한 번의 시간 상한 |
| `AIAPP_AGENT_MAX_STEPS` | `12` | Agent | 도구 호출 반복 상한 |
| `AIAPP_AGENT_REGISTRY_URL` / `_TOKEN` | | Agent | 기동할 때 에이전트 카드를 등록할 레지스트리와 그 bearer 토큰. 비우면 등록 안 함 |
| `AIAPP_ARCHIVE_DIR` | `./data/archive` | App | 배포·제어 아카이브 위치 |
| `AIAPP_AGENT_ARCHIVE_DIR` | `./data/agent-archive` | Agent | 지시 실행 아카이브 위치 |

## 10. 검증 결과 - 실제 VM 생성부터 삭제까지 (2026-09-02)

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
  [1] Linux ip-10-49-6-99 6.8.0-1060-aws ... aarch64 GNU/Linux
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
템플릿(`sg-default`)이 넣은 `inbound TCP 1-65535` 가 이미 있었기 때문입니다 (6-3절).
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

지시 세트를 만들면서 매처를 같이 손봤으므로 **과적합 위험이 있습니다.**
그래서 절대 수치보다 기준선 대비 차이를 같이 적었고, 판정 근거(`alias:라마`)를 응답에 실어
왜 그렇게 골랐는지 사람이 확인할 수 있게 했습니다.

### 10-7. 아직 확인 못 한 것

| 항목 | 왜 |
|---|---|
| 자연어 intent 의 실제 Messages API 호출 | 이 환경에 API 키가 없음. 루프 자체는 스텁으로만 검증. 이후 2026-10-07 에 Ollama(`glm-4.7-flash`)로 dry-run 시연을 했고, 아카이브 레코드상 도구 선택부터 `review_deployment` 까지 돌았음 |
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
이제 노드의 보안그룹에 명시적으로 규칙을 넣고 결과에 엔드포인트를 담습니다 (6-3절).

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
| 쿠버네티스 / 컨테이너 배포 | 계획서상 3차년도 항목 |
| 승인 대기 상태 저장소 | 아카이브 레코드로 대체. 2차년도에 붙일 때 구조가 안 바뀜 |
| 카탈로그 영속화 | 2차년도 "대규모 AI 응용 통합 저장소" 항목. 지금은 메모리 |
| `go-playground/validator` | 신규 의존성 승인 전. `validate` 태그는 규격 문서용으로 두고 검증은 명시적으로 함 |
| 사용자·역할 단위 인가 | 프레임워크 골격(BESP/경희대) 확정 전. 지금은 두 프로세스가 같이 쓰는 bearer 토큰 하나(`AIAPP_AUTH_TOKEN`)만 있음 |
| ETRI 인프라 추천 연동 | 출력 스키마 미확정. `recommend_accelerator_spec` 도구 하나만 교체하면 됨 |

## 12. 이 프로토타입이 확인해 준 CB-Tumblebug 사실

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
