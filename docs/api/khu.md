# 경희대: 모델·응용 추천을 넣을 자리

> 대상: 나중에 이 repo 에 모델·응용 추천을 구현할 경희대 개발자.
> 공통 내용(인증, 오류 형식)은 [README.md](README.md) 에 있습니다.
> API 가 어디로 열려 있는지는 [README.md 1-1절](README.md#1-1-api-는-어디로-열려-있나), Semaphore 는 [3-1절](README.md#3-1-semaphore-가-무엇인가),
> 예시 응용 셋의 동작과 실제 화면은 [12절](README.md#12-예시-응용-셋은-어떻게-동작하나) 에 있습니다.

## 1. 이번 시험에서는

**이번 2차 통합 시험(11월)에는 추천이 필요 없습니다.** 배포할 런타임·모델·응용이 정해져 있기 때문입니다([etri.md](etri.md) 3절).
그래서 AI-App 에는 추천 **자리**만 있습니다.

- `POST /aiapp/recommendations/models`, `POST /aiapp/recommendations/apps` 경로와 요청·응답 형식이 정해져 있습니다.
- 구현이 없어서 올바른 요청에는 `501` 을, 형식이 틀린 요청에는 `400` 을 답합니다.
- 구현은 Go 패키지 `internal/recommend` 의 인터페이스 하나를 채워 등록하면 붙습니다(3절).

```sh
curl -sS -X POST "$AIAPP_URL/recommendations/models" -H "Authorization: Bearer $AIAPP_AUTH_TOKEN" \
  -H 'Content-Type: application/json' -d '{"tasks":["embedding"]}'
# HTTP 501
# {"message":"no recommender is configured for models: set AIAPP_RECOMMENDER_MODEL to a registered model recommender (registered: none)"}
```

## 2. 추천이 받는 것과 돌려주는 것

### 2-1. 모델 추천 `POST /aiapp/recommendations/models`

요청 필드는 전부 선택입니다. 빈 본문 `{}` 은 "아무 모델이나" 입니다. 구현이 안 쓰는 필드는 무시하면 됩니다.

| 필드 | 형 | 뜻 |
|---|---|---|
| `tasks` | 문자열 배열 | 용도. 카탈로그 `tasks[]` 의 말: `chat`, `vision-chat`, `embedding` |
| `inputModalities` | 문자열 배열 | 받아야 할 입력. 카탈로그 `inputModalities[]` 의 말: `text`, `image` |
| `runtimeId` | 문자열 | 원하는 런타임. `GET /aiapp/runtimes` 에 있어야 함(없으면 `400`) |
| `gpuCount` | 정수 | 노드 하나의 GPU 수. `0` 은 GPU 없음, 없으면 모름. 64 까지 |
| `gpuMemoryGiB` | 숫자 | GPU 하나의 메모리 (GiB) |
| `maxSizeGiB` | 숫자 | 받아들일 수 있는 아티팩트 하나의 최대 다운로드 크기 (GiB) |
| `requirements` | 객체 | 그 밖의 무엇이든(비용 예산, 지연 목표, 언어 등). 키는 구현이 정하고 AI-App 은 읽지 않고 넘김 |
| `targetNodes[]` | `{nsId, infraId, nodeId}` | 올릴 노드. 32개까지. `nodeId` 를 주면 `infraId` 도 필요 |
| `maxCandidates` | 정수 | 후보 수 상한, 50 까지. `0` 이면 구현이 정함 |

응답:

```json
{
  "recommender": "khu",
  "candidates": [
    {"modelId": "qwen3-vl-8b", "artifactId": "hf-bf16", "runtimeId": "vllm", "score": 0.82,
     "reason": "Accepts text and image input and fits one 24 GiB GPU in BF16"}
  ]
}
```

후보의 `modelId`·`artifactId`·`runtimeId` 는 `POST /aiapp/deployments` 의 `inference` 에 그대로 넣을 수 있는 값입니다.
`score` 는 클수록 좋고 척도는 구현이 정합니다. `reason` 은 사람이 읽을 근거입니다.

### 2-2. 응용 추천 `POST /aiapp/recommendations/apps`

| 필드 | 뜻 |
|---|---|
| `tasks` | 응용 용도(예 `chat-ui`, `rag`, `travel-planner`). 업로드한 응용에는 태그가 없어서 말은 구현이 정함 |
| `inference[]` | 응용이 쓸 추론 서비스 `{modelId, artifactId, runtimeId}`. `modelId` 는 카탈로그에 있어야 함(없으면 `400`) |
| `gpuCount`, `gpuMemoryGiB`, `maxSizeGiB`, `requirements`, `targetNodes`, `maxCandidates` | 모델 추천과 같음 |

응답 후보는 `{name, version, score, reason}` 이고 `name`·`version` 은 `POST /aiapp/deployments` 의 `app` 에 그대로 들어갑니다.

## 3. 구현 넣는 법

### 3-1. 구조

```
internal/recommend/
    recommend.go     요청·응답 형식, ModelRecommender / AppRecommender 인터페이스, Catalog, 검증
    registry.go      RegisterModel / RegisterApp, 설정한 이름으로 구현을 고르는 Open
    khu/khu.go       (새로) 경희대 구현
cmd/ai-app-server/main.go   기동 때 recommend.Open(AIAPP_RECOMMENDER_MODEL, AIAPP_RECOMMENDER_APP, 카탈로그)
```

구현이 채울 인터페이스는 메서드 하나입니다(`internal/recommend/recommend.go`).

```go
type ModelRecommender interface {
	Recommend(ctx context.Context, req *ModelRecommendReq) (*ModelRecommendResp, error)
}
type AppRecommender interface {
	Recommend(ctx context.Context, req *AppRecommendReq) (*AppRecommendResp, error)
}
```

구현은 기동 때 한 번 `Catalog` 를 받습니다. 카탈로그를 읽는 통로입니다.

```go
type Catalog interface {
	Models(ctx context.Context) []model.Model        // GET /aiapp/models 와 같은 내용
	Runtimes() []model.RuntimeProfile                // GET /aiapp/runtimes
	Apps(ctx context.Context) []model.App            // GET /aiapp/apps
}
```

### 3-2. 단계

1. **패키지를 만듭니다:** `internal/recommend/khu/khu.go`.

   ```go
   package khu

   import (
   	"context"
   	"fmt"
   	"slices"

   	"github.com/ish-hcc/ai-agent-proto/internal/recommend"
   )

   func init() {
   	recommend.RegisterModel("khu", func(catalog recommend.Catalog) (recommend.ModelRecommender, error) {
   		return &khuModels{catalog: catalog}, nil
   	})
   }

   // khuModels recommends every artifact of the models that have one of the
   // asked tasks and fit the download bound, with the first runtime it lists.
   type khuModels struct{ catalog recommend.Catalog }

   func (k *khuModels) Recommend(ctx context.Context, req *recommend.ModelRecommendReq) (*recommend.ModelRecommendResp, error) {
   	resp := &recommend.ModelRecommendResp{}
   	for _, m := range k.catalog.Models(ctx) {
   		if len(req.Tasks) > 0 && !slices.ContainsFunc(req.Tasks, m.HasTask) {
   			continue
   		}
   		for _, a := range m.Artifacts {
   			if req.MaxSizeGiB != nil && a.SizeGiB > *req.MaxSizeGiB {
   				continue
   			}
   			resp.Candidates = append(resp.Candidates, recommend.ModelCandidate{
   				ModelID:    m.ID,
   				ArtifactID: a.ID,
   				RuntimeID:  a.Runtimes[0].RuntimeID,
   				Score:      1,
   				Reason:     fmt.Sprintf("%s serves %v", m.Name, m.Tasks),
   			})
   		}
   	}
   	return resp, nil
   }
   ```

   응용 추천은 같은 방식으로 `recommend.RegisterApp("khu", ...)` 와 `AppRecommender` 를 씁니다.
   이름은 소문자·숫자·`.`·`_`·`-`, 63자까지입니다. 같은 이름을 두 번 등록하면 기동 때 panic 합니다.

2. **연결합니다:** `cmd/ai-app-server/main.go` 의 import 에 한 줄을 넣습니다. 이 줄이 있어야 `init` 이 돌아 등록됩니다.

   ```go
   _ "github.com/ish-hcc/ai-agent-proto/internal/recommend/khu"
   ```

   `internal/recommend/khu.go` 처럼 `recommend` 패키지 안에 바로 둘 수도 있습니다(import 줄이 필요 없음). 다만 그러면
   `internal/recommend/recommend_test.go:49` 의 "등록된 모델 추천은 `nop` 하나" 검사가 `[khu nop]` 을 보고 실패하므로
   그 테스트도 고쳐야 합니다(2026-11-03 에 해 보고 확인). 하위 패키지로 두면 기존 테스트를 건드리지 않습니다.

3. **설정합니다:** AI-App 환경변수에 이름을 줍니다. 비우면 그 경로는 계속 `501` 입니다.

   | 환경변수 | 값 |
   |---|---|
   | `AIAPP_RECOMMENDER_MODEL` | 모델 추천 구현 이름 (예 `khu`) |
   | `AIAPP_RECOMMENDER_APP` | 응용 추천 구현 이름 |

   등록되지 않은 이름을 주면 AI-App 이 기동을 거부하고 등록된 이름을 알려 줍니다:
   `recommender not registered: AIAPP_RECOMMENDER_MODEL="khu", registered: none`.
   docker compose 로 띄우면 `.env` 에 넣습니다(`.env.example` 참고).

4. **빌드합니다:**

   ```sh
   make build      # bin/ 아래에 ai-app-server 와 나머지 바이너리
   make run-app    # go run ./cmd/ai-app-server, :8090
   make docker-up  # 이미지를 다시 빌드해 compose 로 띄움
   ```

5. **시험합니다:**

   ```sh
   make test       # go test ./...
   make verify     # gofmt + build + test + golangci-lint
   AIAPP_RECOMMENDER_MODEL=khu make run-app
   curl -sS -X POST localhost:8090/aiapp/recommendations/models -H 'Content-Type: application/json' \
     -d '{"tasks":["embedding"],"maxCandidates":3}'
   ```

   위 예시 구현으로 2026-11-03 에 돌린 결과(인증 끈 로컬 서버):

   ```
   HTTP 200
   {"recommender":"khu","candidates":[{"modelId":"qwen3-embedding-0.6b","artifactId":"hf-bf16","runtimeId":"vllm","score":1,"reason":"Qwen3-Embedding-0.6B serves [embedding]"}]}
   ```

### 3-3. 테스트 예

`internal/recommend/khu/khu_test.go`. 기본 모델 3개를 메모리 카탈로그에 넣고, 서비스가 하는 검증까지 거쳐 부릅니다.

```go
package khu

import (
	"context"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/recommend"
)

func TestKhuRecommendsEmbedding(t *testing.T) {
	ctx := context.Background()
	models, _, err := modelcat.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := modelcat.Seed(ctx, models); err != nil {
		t.Fatal(err)
	}
	svc, err := recommend.Open("khu", "", recommend.NewCatalog(models, nil))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := svc.RecommendModels(ctx, &recommend.ModelRecommendReq{Tasks: []string{"embedding"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Recommender != "khu" || len(resp.Candidates) != 1 || resp.Candidates[0].ModelID != "qwen3-embedding-0.6b" {
		t.Fatalf("got %+v", resp)
	}
}
```

REST 경로까지 묶어 시험하는 예는 `internal/rest/recommendations_test.go` 에 있습니다. 가짜 구현(`fakeModels`, `fakeApps`)을
`init` 에서 `fake` 라는 이름으로 등록하고, `recommend.Open("fake", "fake", ...)` 로 만든 서비스를 서버에 넣어 `200`·`501`·`500` 을 봅니다.

### 3-4. AI-App 이 대신 해 주는 것

- **요청 검증.** 구현에 넘기기 전에 범위를 봅니다(`gpuCount` 0~64, `maxCandidates` 0~50, `targetNodes` 32개, 없는 `runtimeId`,
  카탈로그에 없는 `inference[].modelId`). 틀리면 구현을 부르지 않고 `400` 입니다.
- **후보 검증.** 구현이 돌려준 후보를 카탈로그와 대조합니다. 없는 모델·아티팩트·런타임, 아티팩트가 그 런타임을 나열하지 않음,
  없는 응용이나 버전이면 요청 전체가 `500 {"message":"Model recommendation failed"}`(응용은 `Application recommendation failed`)이고
  호출자에게 그 후보가 가지 않습니다. 자세한 이유는 응답이 아니라 AI-App 로그에 남습니다
  (예 `recommender returned an invalid candidate: candidates[0]: model "nowhere" is not in the catalog`).
- **자르기와 이름.** 후보를 `maxCandidates` 개로 자르고 `recommender` 에 설정한 이름을 넣습니다. 후보는 좋은 것부터 돌려주면 됩니다.
- 구현이 `error` 를 돌려주면 같은 `500` 입니다. 구현의 오류 문구는 호출자에게 가지 않고 AI-App 로그에 남습니다. 요청이 틀렸다고 답하려면 `*model.ValidationError` 를 돌려주면 `400` 과 그 문구가 갑니다.

## 4. 한계 (추천 재료)

| 한계 | 뜻 |
|---|---|
| 응용 카탈로그는 이름·버전·파일·포트뿐 | `GET /aiapp/apps` 에 용도·설명·태그·필요 자원이 없습니다. 응용 추천은 이름이나 구현이 따로 가진 정보로 판단해야 합니다 |
| 노드 스펙은 카탈로그에 없음 | `targetNodes` 는 이름(`nsId`/`infraId`/`nodeId`)만 넘어갑니다. GPU 종류·메모리가 필요하면 구현이 CB-Tumblebug 에서 직접 읽어야 합니다(노드 `GET /ns/{nsId}/infra/{infraId}/node/{nodeId}` 의 `specId`, 스펙 `GET /ns/system/resources/spec/{specId}`). `Catalog` 는 노드를 읽지 않습니다 |
| 모델 메타데이터는 원래 값 그대로 | `precision`(`BF16`, `FP8`, `Q4_K_M`)은 출처 표기 그대로이고 정규화하지 않습니다. `sizeGiB`·`contextLength` 가 비어 있으면 "모름" 입니다. VRAM 계산은 구현 몫입니다 |
| 크기와 배포 가능 여부 | 아티팩트마다 `parameters`(출처 표기 그대로, 예 `8.2B`)와 `sizeGiB`(받을 크기)가 있습니다. 같은 모델도 태그·양자화마다 아티팩트가 따로라 크기가 다릅니다. `deployable` 이 false 인 모델·아티팩트는 지금 런타임으로 띄울 수 없으니 추천 후보에서 뺍니다(`GET /aiapp/models?deployable=true`) |
| 추천 결과로 배포하지 않음 | 추천 경로는 후보만 돌려줍니다. 배포는 호출자가 그 값으로 `POST /aiapp/deployments` 를 따로 부릅니다 |
| MCP 도구 없음 | 추천은 REST 경로만 있고 MCP 도구는 없습니다 |

## 5. 이 repo 밖에서 구현하고 싶다면

별도 서비스로 만들 수도 있습니다. 그때 카탈로그는 REST 나 MCP 로 읽습니다.

REST (토큰은 AI-App 운영자에게 받음):

```sh
curl -sS -H "Authorization: Bearer $AIAPP_AUTH_TOKEN" "$AIAPP_URL/models?task=chat"
curl -sS -H "Authorization: Bearer $AIAPP_AUTH_TOKEN" "$AIAPP_URL/runtimes"
curl -sS -H "Authorization: Bearer $AIAPP_AUTH_TOKEN" "$AIAPP_URL/apps"
```

MCP: `http://<AI-App 호스트>:8090/aiapp/mcp` (Streamable HTTP, 같은 `Authorization` 헤더). 카탈로그를 읽는 도구는
`list_models`(필터 `task`, `modality`, `format`, `runtimeId`, `source`, `origin`, `verified`, `deployable`, 페이지 `limit`·`offset`), `get_model`(`modelId`),
`list_runtimes`, `get_runtime`(`runtimeId`), `list_apps`(페이지 `limit`·`offset`), `get_app` 이고 모두 읽기 등급이라 dry-run 과 무관하게 실행됩니다. 도구 목록과 입력 형식은 `GET /aiapp/tools` 로 볼 수 있습니다.

다만 **AI-App 은 바깥 추천 서비스를 부르지 않습니다.** `POST /aiapp/recommendations/*` 가 그 서비스의 답을 내게 하려면 3절의 구현
안에서 그 서비스를 HTTP 로 부르는 얇은 어댑터를 넣어야 합니다. 그 어댑터가 받을 주소·토큰 설정은 아직 없어서 구현할 때 같이 정합니다.
