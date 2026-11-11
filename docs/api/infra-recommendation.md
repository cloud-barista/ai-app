# 응용 요구 분석과 인프라 추천

경희대 추천 서비스는 AI-App 8090의 기존 REST 경로를 사용해 응용 요구를 CB-Tumblebug의 스펙·이미지 카탈로그와 대조한다. API가 반환한 조합은 CB-Tumblebug에서 사전 검토한다. VM 준비가 끝나면 기존 배포 API에 `nsId/infraId/nodeId`를 전달한다. [노드와 배포 계약](etri.md)

## 연결 설정

기본 구성은 `mock/partner/recommender`의 기존 8092 추천기를 사용한다. 경희대 추천을 사용하려면 아래 두 명령을 별도 터미널에서 실행한다. AI-App은 `AIAPP_PARTNER_REC_URL`로 추천 경로와 Authorization 헤더를 전달한다.

```sh
go run ./cmd/khu-infra-recommender
AIAPP_PARTNER_REC_URL=http://127.0.0.1:8094 go run ./cmd/ai-app-server
```

경희대 추천기는 `AIAPP_TUMBLEBUG_BASE_URL`, 인증과 카탈로그 설정을 읽는다. `AIAPP_TUMBLEBUG_SPEC_NAMESPACE`와 `AIAPP_TUMBLEBUG_IMAGE_NAMESPACE`의 기본값은 `system`이고 `AIAPP_TUMBLEBUG_DEFAULT_OS_TYPE`은 `ubuntu 22.04`다. 인증을 사용하는 경우 AI-App과 추천기에 같은 `AIAPP_AUTH_TOKEN`을 지정한다. 컨테이너 환경에서는 `AIAPP_KHU_REC_ADDR`과 `AIAPP_PARTNER_REC_URL`에 컨테이너가 접속할 수 있는 주소를 사용한다.

## 스펙 후보

`POST /aiapp/partner/rec/recommendations/specs`에 기존 콘솔과 같은 `{app, limit}`을 보낸다. `limit`을 생략하거나 0 이하로 보내면 후보를 최대 5건 요청한다. 가속기 유형을 지정하고 수량을 생략하면 한 장을 요구한다.

```sh
AIAPP_URL=http://127.0.0.1:8090/aiapp
curl -fsS "$AIAPP_URL/partner/rec/recommendations/specs" \
  -H "Authorization: Bearer $AIAPP_AUTH_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "app": {
      "id": "text-generation",
      "accelerator": {"type": "gpu", "minCount": 1, "minMemoryGiB": 22},
      "resources": {"minVCPU": 8, "minMemoryGiB": 32, "minStorageGiB": 60},
      "install": {"osType": "ubuntu 22.04"},
      "slo": {"latencyP95MsMax": 1000, "throughputRpsMin": 5}
    },
    "limit": 5
  }'
```

`app` 대신 `profile`에 AI-Ops의 `ApplicationProfile`을 보낼 수 있다. `app_id`, `requirements.compute`, `requirements.accelerator`, `requirements.slo`, `requirements.cost`와 `workload.expected_rps`를 읽는다. RAM과 가속기 메모리는 MiB를 GiB로 변환하고, GPU 메모리는 장치 한 개의 값으로 비교한다. 상한 비용을 사용하면 `maxCostPerHour` 또는 `requirements.cost.cost_per_hour_max`에 USD 단가를 보낸다. 가격이 확인된 후보에만 비용 상한을 적용할 수 있다.

응답은 `{candidates, reason}`이다. 후보의 `availabilityStatus`로 `Available`, `Unverified`, `SoldOut`, `QuotaExceeded`, `Unsupported`를 구분한다. 확인된 후보, 미확인 후보, 사용 불가 후보 순으로 보여 주며 그룹 안에서는 CB-Tumblebug의 비용순을 유지한다. 가격이 0 이하인 후보는 가격 미확인이다. 저장공간 요구는 프로비저닝 검토에서 확인하고 지연·처리량 SLO는 실제 부하에서 확인한다. 추천 사유에 두 조건의 확인 필요 여부가 나온다. CB-Tumblebug에서 받은 가용성 확인 시각이 있으면 `availabilityCheckedAt`에 담는다.

`accelerator.type`에 `gpu`만 지정하면 카탈로그에서 여러 벤더의 후보를 찾는다. GPU 후보의 `gpuRuntimeVendorStatus`는 현재 AI-App GPU 추론 런타임에 대한 벤더 호환 상태다. 카탈로그에서 NVIDIA로 확인하면 `Compatible`, 다른 벤더로 확인하면 `Unsupported`, 벤더를 확인할 수 없으면 `Unverified`를 반환한다. 후보를 제외하거나 재정렬하는 데 이 필드를 사용하지 않는다. 드라이버와 CSP 가용성은 각각 이미지 응답과 `availabilityStatus`에서 확인한다. 특정 모델만 필요하면 `accelerator.model`에 후보의 정확한 `acceleratorModel`을 지정할 수 있다.

8090의 기존 추천 화면은 `reason`의 GPU 벤더 상태 집계와 요청·응답 원문을 표시한다. 화면의 출처 라벨은 기본 모의 추천기 이름으로 표시되므로 실제 추천 서버는 `AIAPP_PARTNER_REC_URL` 설정에서 확인한다.

## 호환 이미지

`POST /aiapp/partner/rec/recommendations/image`에 같은 `app` 또는 `profile`과 위 응답에서 선택한 `specId`를 보낸다. 응용 요구를 스펙과 다시 대조하고, CB-Tumblebug `searchImage`의 `matchedSpecId`로 호환 이미지를 찾는다.

```json
{
  "app": {
    "id": "text-generation",
    "accelerator": {"type": "gpu", "minCount": 1, "minMemoryGiB": 22}
  },
  "specId": "<스펙 후보 ID>"
}
```

응답의 `imageId`, `source`, `reason`과 `driverStatus`를 확인한다. `matched`는 카탈로그에 가속기와 같은 벤더의 드라이버 정보가 있다는 뜻이다. `review_required`이면 드라이버 설치 또는 호환 여부를 확인한다. 가속기 요구가 없는 응용은 `not_required`로 표시한다. GPU 요청의 `gpuRuntimeVendorStatus`는 선택한 스펙의 벤더 호환 상태이며, `Unsupported`와 `Unverified`는 `reason`에도 표시한다. AMD 장치와 ROCm 이미지가 매칭된 경우에도 현재 AI-App GPU 추론 런타임의 벤더 상태는 `Unsupported`다.

| 응답 | 확인할 내용 |
| --- | --- |
| `200` · `candidates: []` | 가속기 종류·수량·장치당 메모리 또는 비용 등 요청 필터에 맞는 후보가 없음 |
| `400` | 응용 식별자, 수치 요구 조건 또는 이미지의 스펙 ID 확인 |
| `401` | 8090 bearer 확인 |
| `404` | 스펙 ID 또는 해당 스펙에 호환되는 이미지 확인 |
| `409` | 선택한 스펙의 VRAM·수량·CPU·RAM·비용 등 요구 미충족 항목 확인 |
| `502` | CB-Tumblebug 또는 설정한 외부 추천 서버 연결 확인 |

CB-Tumblebug가 스펙 부재를 전용 메시지와 함께 `500`으로 반환하는 경우에도 이 API는 `404`로 응답한다. 같은 상태 코드의 DB 조회 실패와 연결 오류는 `502`로 분류한다.

## 사전 검토와 배포 대상 대조

인프라 운영자가 기존 CB-Tumblebug `infraDynamicReview`에서 선택한 노드 그룹이 `canCreate: true`이면 검토 내용을 승인 요청에 함께 보낼 수 있다. `specValidation.resourceId`와 `imageValidation.resourceId`를 각각 `specId`, `imageId`에 담는다. 노드 그룹의 `status`, 스펙·이미지의 `status`, 그룹의 `estimatedCost`를 복사한다. `reviewedAt`은 검토 응답을 받은 UTC 시각이다. 이 가격과 가용성은 검토 당시 기록이다. [CB-Tumblebug 사전 검토 계약](https://github.com/cloud-barista/cb-tumblebug/blob/b808b9a2/docs/feature_guide/ko/spec-image-recommendation.md#L260)

GPU VM이 준비되면 AI-Ops의 `/api/v1/agent-control/flows/{correlation_id}/aiapp/prepare`에 기존 모델·아티팩트와 함께 아래처럼 보낸다.

```json
{
  "model_id": "ministral-3-8b",
  "artifact_id": "hf-fp8",
  "placement_review": {
    "nsId": "etri",
    "specId": "aws+ap-northeast-2+g4dn.xlarge",
    "imageId": "aws+ap-northeast-2+ami-example",
    "status": "Ready",
    "specStatus": "Available",
    "imageStatus": "Available",
    "estimatedCost": "$0.50/hour",
    "reviewedAt": "2026-11-10T08:00:00Z"
  }
}
```

준비 응답의 두 배포 본문에 `placementReview`가 들어간다. 추론 dry-run과 후속 Revision에 같은 내용을 전달한다. AI-App은 배포 접수와 실행 직전의 기존 노드 조회에서 `specId`·`imageId`와 `Running` 상태를 확인한다. 조건이 맞으면 설치 단계로 넘어간다. 검토 기록이 없는 경우에는 기존 입력 형식을 사용한다. [승인 배포 절차](approved-deployment.md)
