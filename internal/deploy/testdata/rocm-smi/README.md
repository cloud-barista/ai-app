# rocm-smi 실제 출력 캡처

`rocm-smi --json` 을 실제 AMD 하드웨어에서 받은 출력입니다.
이 프로토타입에는 AMD 장비가 없어서, 파서를 검증할 수 있는 유일한 수단입니다.

## 출처

Telegraf 의 `amd_rocm_smi` 입력 플러그인 테스트 데이터에서 가져왔습니다.

- <https://github.com/influxdata/telegraf/tree/master/plugins/inputs/amd_rocm_smi/testdata>
- Copyright (c) 2015-2025 InfluxData Inc.
- SPDX-License-Identifier: MIT

내용을 고치지 않고 그대로 두었습니다. 고치면 실제 출력이 아니게 됩니다.

## 무엇을 덮는가

| 파일 | 장치 | ROCm | 카드 |
|---|---|---|---|
| `mi100_rocm571.json` | Instinct MI100 | 5.7.1 | 6 |
| `mi100_rocm602.json` | Instinct MI100 | 6.0.2 | 4 |
| `rx6700xt_rocm430.json` | Radeon RX 6700 XT | 4.3.0 | 1 |
| `rx6700xt_rocm571.json` | Radeon RX 6700 XT | 5.7.1 | 1 |
| `rx6700xt_rocm602.json` | Radeon RX 6700 XT | 6.0.2 | 1 |
| `rx6700xt_rocm612.json` | Radeon RX 6700 XT | 6.1.2 | 1 |
| `vega-10-XT.json` | Vega 10 XT | | 1 |
| `vega-20-WKS-GL-XE.json` | Vega 20 WKS | | 1 |

## 이 캡처들이 알려 준 것

- **릴리즈마다 키 이름이 바뀝니다.** `GPU ID` 가 6.02 에서 `Device ID` 가 되고,
  6.12 에서 `Card series` 가 `Card Series` 로 대소문자가 바뀌며 `Device Name` 이 새로 생깁니다.
  그래서 파서가 키를 대소문자 무시로 찾습니다.
- **드라이버 버전은 카드가 아니라 `system` 섹션에 있습니다.** 호스트당 하나입니다.
- **값이 전부 문자열입니다.** 숫자도 따옴표 안에 있습니다 (1,297개 값 전수 확인).
- **`Card Vendor` 에 쉼표가 들어갑니다** (`Advanced Micro Devices, Inc. [AMD/ATI]`).
  같은 도구의 CSV 출력을 쓸 때 칸이 밀리는 원인입니다.
