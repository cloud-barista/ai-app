# vLLM 설정 최적화 Shadow 시험

AI-App의 `optimization/policy.py`는 관측 레코드와 부하 시험 결과로 `--max-num-seqs`의 변경 효과를 예측한다. Go 서비스에서는 관측 레코드를 만들고, Python 명령으로 학습과 제안을 실행한다. [관측 도구](inference-observation.md)

## 실측 자료

하나의 데이터셋에서는 준비된 노드, GPU UUID, 모델·아티팩트, 런타임 이미지, 응용 버전과 요청 분포를 고정한다. 초기 AI-Ops 배포 준비의 `max_num_seqs`로 기준값을 지정하고, 후속 후보는 승인된 업데이트에서 `--max-num-seqs`로 적용한다. 각 설정에서 워밍업 뒤 부하를 발생시킨다. 최소 두 부하 수준과 두 설정을 시험하고, 그 조합 전체를 시간순으로 다섯 번 이상 반복한다. 각 반복에서는 조합별 관측 창이 겹치지 않도록 한다. 새 Revision을 적용하기 전에 현재 창의 관측 JSON을 받는다. [AI-Ops 초기 설정과 승인](../../../ai-ops/deployment_agent/docs/aiapp-intent-approval.md#추론-최적화-제안-승인)

데이터셋은 한 줄에 시험 하나를 담은 JSONL 파일이다. 각 행의 `observation`에는 MCP가 반환한 관측 레코드 전체를 넣는다. `load`에는 같은 창에서 부하 발생기가 기록한 아래 값을 넣는다. `withinSLO`에는 지정한 지연 기준을 충족한 성공 요청만 센다. 실패와 타임아웃은 `failed`에 포함한다. `workloadId`는 요청 본문과 길이 분포를 고정한 실험 구성의 버전이다.

```json
{
  "observationId": "<observationId from MCP>",
  "workloadId": "text-generation-workload-v1",
  "runtimeImage": "vllm/vllm-openai@sha256:<observed-digest>",
  "window": {
    "start": "2026-11-10T08:00:00Z",
    "end": "2026-11-10T08:03:00Z"
  },
  "promptTokensP50": 128,
  "maxOutputTokens": 64,
  "offeredRps": 4,
  "sloSeconds": 0.5,
  "attempted": 720,
  "failed": 2,
  "withinSLO": 660
}
```

```bash
jq -nc --arg run repeat-1 --slurpfile obs observation.json --slurpfile load load.json \
  '{experimentId:$run, observation:$obs[0], load:$load[0]}' >> observations.jsonl
```

### vLLM 부하 결과 수집

`optimization/capture_load.py`로 활성 Revision의 vLLM `/v1/completions`에 일정한 속도로 요청을 보낸다. 운영자는 워밍업을 마친 시험 노드의 서빙 주소, Flow와 Revision, 실제 배포한 이미지 다이제스트를 지정한다. 같은 모델의 토크나이저로 만든 입력 토큰 배열과 출력 상한을 프로필에 넣고, 설정별 시험에서 같은 프로필을 사용한다. 아래 토큰과 다이제스트는 형식 예시다. [vLLM v0.32.0 Completion 요청](https://github.com/vllm-project/vllm/blob/v0.32.0/vllm/entrypoints/openai/completion/protocol.py)

```json
{
  "model": "served-model-name",
  "runtimeImage": "vllm/vllm-openai@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "promptTokenIds": [[128, 256, 512], [128, 768, 1024]],
  "maxOutputTokens": 64
}
```

```sh
python3 optimization/capture_load.py workload.json \
  --endpoint "$VLLM_URL/v1/completions" --flow "$FLOW_ID" --revision "$REVISION" \
  --rps 4 --seconds 180 --slo-seconds 0.5 > load-capture.json
jq '{correlationId, revision, window:.load.window}' load-capture.json
```

필요하면 `AIAPP_BENCHMARK_TOKEN`으로 vLLM 인증을 전달한다. 수집 명령은 첫 요청부터 마지막 응답까지 포함한 UTC 창을 초 단위로 기록하고 창이 닫힌 뒤 결과를 출력한다. 소켓 대기 제한은 15초다. `--rps`에는 0.5 같은 소수도 쓸 수 있다. 설정한 속도와 시간으로 입력 묶음이 정수 번 반복돼야 하며, 속도를 유지한 시험에서 결과를 출력한다. 결과에는 요청 수, 실패 수, SLO 이내 성공 수와 요청 구성 해시가 들어간다.

출력한 창의 마지막 스크레이프가 들어온 뒤 같은 Flow·Revision에 `get_inference_observation`을 호출한다. 응답의 관측 객체를 `observation.json`에 저장한다. 다음 명령으로 대상을 대조하고 학습·Shadow 입력 한 건을 만든다.

```sh
jq -nce --arg run repeat-1 --slurpfile obs observation.json --slurpfile sample load-capture.json '
  $obs[0] as $o | $sample[0] as $s |
  if ($o.correlationId == $s.correlationId and $o.revision == $s.revision
      and $o.window == $s.load.window and $o.metrics.complete == true
      and ($o.observationId | test("^[0-9a-f]{64}$")))
  then {experimentId:$run, observation:$o, load:($s.load + {observationId:$o.observationId})}
  else error("incomplete observation or mismatched load window")
  end
' > current-sample.json
```

검증된 샘플을 반복 ID별로 모아 JSONL 데이터셋을 만든다. 적용 후 비교에는 새 샘플의 `load`를 사용한다.

## 학습과 제안

시험 전에 현재 기준값과 한계를 `guard.json`에 정한다. 아래 값은 입력 예시다.

```json
{
  "dataKind": "measurement",
  "baselineMaxNumSeqs": 16,
  "p95LimitSeconds": 0.5,
  "peakVRAMLimitMiB": 12000,
  "errorRateLimit": 0.02,
  "minimumGainFraction": 0.10,
  "evaluationHorizonSeconds": 1800,
  "maxUpdateDowntimeSeconds": 90
}
```

```bash
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r optimization/requirements.txt
python optimization/policy.py train observations.jsonl guard.json artifacts/vllm-policy-1
python optimization/policy.py preview current-sample.json artifacts/vllm-policy-1 > shadow.json
```

`current-sample.json`은 위 JSONL과 같은 구조의 객체 한 건이다. Python 3.11 이상에서 운영자 학습 작업이 생성한 아티팩트를 읽는다. 재생용 데이터는 `dataKind: "fixture"`로 표시한다.

v2 Random Forest는 같은 반복·요청률에서 기준 설정과 후보 설정의 관측을 짝짓는다. 요청률, 후보 설정, 기준 설정의 실측 p95·SLO 충족 요청/s·장치당 최대 사용 VRAM·오류율을 입력으로 후보의 네 지표를 예측한다. 현재 설정의 비교값에는 현재 창의 실측을 사용한다. 학습 기준 관측에서 요청률별 지표의 최솟값과 최댓값을 저장하고, 현재 실측이 이 범위를 벗어나면 `ABSTAIN`을 반환한다.

반복 시험을 시간순으로 학습·검증·시험에 나눈다. 검증에서 구한 최대 오차를 후보 비교에 적용한다. Shadow와 시험은 평가 기간에서 최대 업데이트 중단 시간을 차감해 유효 처리량을 구한다. 시험에서는 이 값으로 기존 설정 유지와 학습 실측 중앙값 기준을 비교하며, 원시 처리량도 함께 기록한다. SLO나 최소 개선 폭을 충족하지 못한 변경안은 보류한다. 기존 v1 아티팩트는 같은 scikit-learn 버전에서 v1 입력으로 재생할 수 있다.

`preview`는 `KEEP`, `UPDATE`, `ABSTAIN`과 해당 관측·배포·Revision, 모델·데이터 해시를 반환한다. 측정 범위 밖의 부하·구성, 누락 지표, 기준 설정 불일치, 시험 기준 미달은 `ABSTAIN`이다. 중단 예산을 반영하기 전에 학습한 실측 정책은 재평가 사유를 반환한다. 유효한 관측을 받으면 보류했을 때도 같은 창의 현재 설정 실측을 `baseline`에 담는다. `UPDATE`에는 예상값과 `inferenceArgs`가 포함된다.

## 제안 게시와 승인

자동화 엔진에 `AIAPP_ENGINE_APPLY_ENABLED=true`, `AIAPP_OBSERVATION_PROMETHEUS_URL`, `AIAPP_OPTIMIZATION_PROPOSAL_DIR`을 설정한다. 제안 디렉터리는 학습·게시 작업만 쓰고 엔진이 읽을 수 있게 한다. 다음은 엔진도 호스트에서 실행할 때의 게시 명령이다. [Compose에서 호스트 제안 읽기](#compose에서-호스트-제안-읽기)

```bash
python optimization/policy.py preview current-sample.json artifacts/vllm-policy-1 \
  --transition-evidence transition.json \
  --publish-dir "$AIAPP_OPTIMIZATION_PROPOSAL_DIR"
```

게시 명령은 원본 관측, 부하 결과, Guard, 선택한 후보, 검증 오차와 첨부한 전환 계측을 담은 JSON을 쓰고 그 SHA-256을 `proposalId`로 반환한다. 동일한 제안은 같은 ID를 사용한다. 승인 MCP의 `get_inference_optimization`에 `proposalId`와 `checkCurrent: true`를 보내면 현재 배포·Run·관측 창과 과거 전환 기록을 대조한다.

전환 계측에는 같은 노드에서 기준 설정을 후보 설정으로 바꾼 승인 기록을 사용한다. 두 응용 배포 ID는 각 Revision의 성공 영수증에서 가져온다. 기존 응용의 마지막 정상 추론 시각과 후보 Revision 적용 완료 뒤의 첫 정상 추론 시각을 기록한다. `/health`는 별도로 계측한다. `runtimeImage`는 시험에 사용한 이미지 다이제스트이고 `gpuUUIDs`는 해당 노드 관측의 장치 UUID다.

```json
{
  "correlationId": "earlier-transition",
  "previousRevision": 1,
  "revision": 2,
  "baselineApplicationDeploymentId": "<baseline app deployment ID>",
  "candidateApplicationDeploymentId": "<candidate app deployment ID>",
  "runtimeImage": "vllm/vllm-openai@sha256:<observed-digest>",
  "gpuUUIDs": ["<GPU UUID>"],
  "lastBaselineResponseAt": "2026-11-10T08:00:00Z",
  "firstCandidateResponseAt": "2026-11-10T08:01:00Z"
}
```

적용 후보는 실측 자료로 검증한 `UPDATE`이며 관측 창 종료 후 30분 이내여야 한다. 엔진은 과거 전환의 두 Revision과 Run이 현재와 같은 노드·기준 구성·후보 설정인지 확인한다. 전환은 현재 관측 창 시작 전에 끝나야 한다. 계측이 빠지면 `transition_evidence_required`, 기록이 맞지 않으면 `transition_evidence_invalid`, 두 응용 응답 사이의 공백이 상한을 넘으면 `transition_downtime_exceeds_budget`을 반환한다. 보수적 유효 처리량은 최대 업데이트 중단 시간과 평가 기간을 기준으로 계산하고, 예측 오차를 반영한 뒤에도 최소 개선 폭과 SLO를 충족해야 한다. `fixture`, `KEEP`, `ABSTAIN`은 조회용 제안이다. 이미 적용한 이전 제안의 사후 비교에는 당시 게시물과 실제 중단 시간을 사용한다.

AI-Ops에서 `/api/v1/agent-control/flows/{correlation_id}/aiapp/optimizations/{proposal_id}`의 `GET`으로 제안과 다음 Revision을 읽는다. `/approve`에 `{"digest":"<proposalId>"}`를 보내 승인한다. AI-Ops는 승인 직전에 근거를 다시 확인하고, 이전 구성에서 `--max-num-seqs`만 바꾼 다음 Revision을 기존 적용 API에 제출한다. [AI-Ops 권한과 요청 형식](../../../ai-ops/deployment_agent/docs/aiapp-intent-approval.md#추론-최적화-제안-승인) · [승인 적용](approved-deployment.md)

### Compose에서 호스트 제안 읽기

Linux에서 호스트의 게시 계정이 속한 제안 전용 그룹의 숫자 ID를 `AIAPP_PROPOSAL_GID`에 지정한다. `AIAPP_PROPOSAL_HOST_DIR`은 호스트에서 제안을 쓸 절대 경로다. 두 값을 셸 환경에 내보낸 뒤 디렉터리를 만들고 자동화 엔진에 읽기 전용으로 연결한다. 엔진은 이미지의 UID `10001`과 지정한 추가 그룹으로 파일을 읽는다.

```sh
install -d -m 2750 -g "$AIAPP_PROPOSAL_GID" "$AIAPP_PROPOSAL_HOST_DIR"
```

Compose override에 아래 설정을 적용한다.

```yaml
services:
  ai-automation-engine:
    group_add: ["${AIAPP_PROPOSAL_GID}"]
    environment:
      AIAPP_OPTIMIZATION_PROPOSAL_DIR: /app/data/optimization/proposals
    volumes:
      - "${AIAPP_PROPOSAL_HOST_DIR}:/app/data/optimization/proposals:ro"
```

호스트에서 게시한 파일의 기본 권한은 `0600`이다. 게시자가 선택한 제안의 권한을 `0640`으로 바꾼 뒤 ID를 전달한다.

```sh
proposal_id=$(python optimization/policy.py preview current-sample.json artifacts/vllm-policy-1 \
  --transition-evidence transition.json --publish-dir "$AIAPP_PROPOSAL_HOST_DIR" \
  | jq -er '.proposalId | select(test("^[0-9a-f]{64}$"))') &&
  chmod 0640 -- "$AIAPP_PROPOSAL_HOST_DIR/$proposal_id.json" &&
  printf '%s\n' "$proposal_id"
```

## 적용 후 비교

다음 Revision이 활성화된 뒤 같은 부하를 재현한다. `get_inference_observation`으로 새 창의 ID를 얻고, AI-Ops의 `/compare`에 이 창의 `load`와 이번 적용의 `transitionEvidence`를 보낸다. 전환 근거는 앞의 게시 예시와 같은 형식이며, 이번 Flow와 두 Revision의 응용 배포 ID를 담는다. AI-App의 `compare_inference_optimization`이 노드·GPU·모델·런타임·응용 버전·부하 조건과 적용한 인수를 확인한다. 과거 전환 근거를 포함하지 않은 이전 게시물에는 기존 `downtimeSeconds` 입력을 사용할 수 있다.

전환 중에는 응용 경로에 같은 확인 요청을 일정 간격으로 보낸다. 기준 관측 창이 닫힌 뒤 기존 배포의 마지막 정상 응답을 기록한다. 다음 Revision의 `succeeded` 이후 첫 정상 추론 응답 시각을 기록하고, 워밍업이 끝난 뒤 새 관측 창을 시작한다. 런타임 `/health` 복구 시각은 별도로 기록한다. 엔진은 응용 응답 시각을 두 배포 영수증과 관측 창에 대조해 중단 시간을 계산한다.

비교 결과에는 확인한 `transitionEvidence`, 계산한 `downtimeSeconds`, 전후 p95·오류율·최대 VRAM·SLO 충족 요청/s와 중단 시간을 반영한 처리량이 들어간다. 개선 기준과 한계를 만족하면 `KEEP`을 기록한다. 중단 예산 초과 등 기준 미달이면 `ROLLBACK_REVIEW`와 이전 설정으로 복구할 다음 Revision 초안을 받는다. 복구 초안은 운영자가 기존 적용 API에서 승인한다.
