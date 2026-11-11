# 추론 배포 Revision 관측

`get_inference_observation`은 단일 GPU 노드에 적용한 승인 Revision의 엔진 Run·배포 ID·설정과 vLLM·DCGM 지표를 연결하는 읽기 도구다. 최적화 모듈은 결과 JSON을 입력으로 사용한다. [승인 배포 계약](approved-deployment.md)

## 설정과 호출

엔진에 `AIAPP_ENGINE_APPLY_ENABLED=true`와 `AIAPP_OBSERVATION_PROMETHEUS_URL`을 설정한다. Prometheus가 인증을 요구하면 조회 권한이 있는 `AIAPP_OBSERVATION_PROMETHEUS_TOKEN`을 함께 설정한다. MCP 클라이언트는 `:8090/aiapp/engine/automation/mcp`에 승인 전용 bearer와 `X-AIMCMP-Namespace`를 보내 도구를 호출한다.

```json
{
  "correlationId": "flow-1",
  "revision": 2,
  "window": {
    "start": "2026-11-10T08:00:00Z",
    "end": "2026-11-10T08:05:00Z"
  }
}
```

창은 초 단위의 종료된 2~30분으로 지정한다. 시작은 적용 영수증의 `updatedAt` 이후여야 한다. AI-App은 해당 Revision의 추론·응용 Run을 아카이브에서 확인하고, 두 배포가 여전히 해당 노드의 활성 엔드포인트인지 대조한다. 같은 Revision과 창은 같은 `observationId`를 사용한다. 후속 Revision 적용 전에 JSON을 수집해 두면 교체 뒤 학습·비교에 다시 사용할 수 있다.

## Prometheus 연결

대상 등록 시 아래 라벨을 스크레이프 설정에 넣는다. CB-Tumblebug 식별자는 대소문자를 포함해 배포 레코드의 값과 일치해야 한다. 새 런타임이 같은 주소에서 시작하면 vLLM 대상의 `deploymentId`를 새 배포 ID로 갱신한다. 주소 변경으로 한 창에 같은 장치의 시계열이 중복되면 창을 나눠 읽는다.

| 스크레이프 대상 | 라벨 | 관측값 |
| --- | --- | --- |
| vLLM `/metrics` | `job="vllm"`, `nsId`, `infraId`, `nodeId`, `deploymentId` | 요청 지연 p95, 종료 요청/s, 생성 토큰/s, 평균 대기 요청 수, 엔진 오류/s, 클라이언트 중단/s |
| DCGM exporter | `job="dcgm"`, `nsId`, `infraId`, `nodeId`, exporter의 `UUID` | GPU별 평균 사용률, 사용 VRAM 최대값, 여유 VRAM 최소값 |

Prometheus의 range vector에서 스크레이프 시각을 읽는다. 시작과 끝의 표본이 각각 창 경계에서 45초 이내이고, 중간 스크레이프 간격도 45초 이하여야 한다. 실측 간격을 기준으로 카운터 증가율을 구하고, vLLM 히스토그램 버킷으로 p95를 추정한다. 요청이 없는 창의 p95, 감소한 카운터, 범위를 벗어난 값, 충돌하는 시계열은 품질 사유와 함께 값이 비어 있다. [Prometheus 응답 형식](https://prometheus.io/docs/prometheus/latest/querying/api/#range-vectors) · [vLLM v0.32.0 지표](https://github.com/vllm-project/vllm/blob/v0.32.0/vllm/v1/metrics/loggers.py) · [DCGM 지표](https://github.com/NVIDIA/dcgm-exporter/blob/main/etc/default-counters.csv)

`metrics.complete`는 위 계측이 모두 유효한 경우에만 참이다. 서빙 지표의 오류는 vLLM 엔진에서 오류로 종료한 요청을 센다. 응용 전체 HTTP 오류, 부하 발생기의 입력 요청/s와 토큰 길이, SLO를 충족한 요청/s는 실측 시 별도로 기록한다. 응답에는 엔진 Run의 환경변수, 서비스 주소, API 키를 싣지 않는다.
