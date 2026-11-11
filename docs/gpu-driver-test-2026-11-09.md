# GPU 드라이버 설치 시험 결과 (2026-11-09)

GPU 노드에 드라이버를 자동으로 까는 플레이북 `deploy/ansible/install-gpu-driver.yml` 을 만들면서 드라이버 구성별로 실제 VM 에서 돌려 본 결과입니다.
읽는 사람은 AI-App 개발자입니다.

## 왜 시험했나

Azure 의 AMD GPU VM 은 MS 가이드대로 하면 AMD 의 DKMS 드라이버(`amdgpu-dkms`)를 깝니다.
DKMS 는 노드에서 커널 모듈을 직접 빌드하는 방식이라, 그 모듈은 노드에서 만든 키로 서명됩니다.
Secure Boot 가 켜진 VM 은 이 키를 **MOK**(Machine Owner Key, 사용자가 추가하는 부트 신뢰 키)로 등록해야 모듈을 올립니다.
MOK 등록은 재부팅 중 부트 콘솔의 MokManager 화면에서 비밀번호를 쳐야 끝나서 자동화할 수 없습니다.
그래서 DKMS 없이 GPU 를 살리는 구성을 찾았습니다.

## 시험 환경

| 항목 | 값 |
|---|---|
| 리전, 스펙 | Azure eastus, `Standard_NV4ads_V710_v5` |
| GPU | AMD Radeon Pro V710 MxGPU. SR-IOV VF(GPU 하나를 나눠 VM 에 붙인 가상 장치), PCI `1002:7461`, gfx1101 |
| 이미지 | `Canonical:ubuntu-24_04-lts:ubuntu-pro:latest` (Ubuntu 24.04) |
| 커널 | `7.0.0-1017-azure` |
| VM 1 `ish-amd-test` | Azure 포털로 만듦. Trusted Launch, **Secure Boot 켜짐**. 처음에 MS 가이드대로 DKMS 를 깔고 MOK 를 등록해 썼음 |
| VM 2 `ish-amd-test2` | 이번에 cb-spider 로 새로 만듦. **Secure Boot 꺼짐** (`secureBootEnabled: false`). cb-spider 가 보안 설정을 넘기지 않아 일반 VM 으로 만들어짐 |

## AMD 결과

| # | 드라이버 구성 | VM | Secure Boot | 결과 |
|---|---|---|---|---|
| 1 | MS 가이드: `amdgpu-dkms` + `amdgpu-dkms-firmware` (31.50) | VM 1 | 켜짐 | 동작. 다만 MOK 등록(콘솔 입력)을 거친 뒤에만 모듈이 올라감 |
| 2 | 커널 기본 `amdgpu` + Ubuntu `linux-firmware` (20240318) | VM 1 (DKMS 제거 후) | 켜짐 | 동작. 재부팅 두 번 모두 자동 적재, `/dev/kfd`, `rocminfo` gfx1101 확인 |
| 3 | 커널 기본 `amdgpu` + Ubuntu `linux-firmware` | VM 2 (새 VM) | 꺼짐 | **실패**. `failed to load ucode RS64_PFP`, `PSP firmware loading failed`, `probe with driver amdgpu failed with error -22` |
| 4 | 커널 기본 `amdgpu` + AMD `amdgpu-dkms-firmware` 31.50 (손으로 설치) | VM 2 | 꺼짐 | 동작. `Initialized amdgpu 3.64.0`, `/dev/dri/renderD128` |
| 5 | 플레이북: 커널 기본 `amdgpu` + AMD `amdgpu-dkms-firmware` 최신(31.60) + `amd-smi` | VM 2 (드라이버 설치 전 상태로 되돌린 뒤) | 꺼짐 | 동작. 첫 실행 174초(재부팅 포함, changed=11), 두 번째 실행 90초(changed=0). `amd-smi` 가 `AMD Radeon Pro V710 MxGPU` 를 냄 |
| 6 | 플레이북 | VM 1 | 켜짐 | **시험 못 함**. VM 1 이 12:01(UTC)에 꺼져(deallocated) 있었음 |

정리하면 이렇습니다.

- **커널 기본 `amdgpu` 는 MOK 없이 올라갑니다.** 이 모듈은 Ubuntu 커널 빌드 키로 서명돼 있어서(구성 2, Secure Boot 켜짐) 노드에서 따로 서명할 것이 없습니다.
- **펌웨어는 Ubuntu 것으로는 부족합니다.** 새 VM 에서는 GPU 의 보안 프로세서(PSP)가 Ubuntu `linux-firmware` 의 펌웨어를 거절했습니다(구성 3).
  AMD 저장소의 `amdgpu-dkms-firmware` 는 의존성이 없는 펌웨어 파일 묶음이라(`/lib/firmware/updates/amdgpu`) DKMS 빌드 없이 깔립니다(구성 4, 5).
- 구성 2 가 왜 Ubuntu 펌웨어로 됐는지는 **확인하지 못했습니다.** 그 VM 이 앞서 AMD 펌웨어로 돌았고 deallocate 없이 재부팅만 했기 때문에, 호스트 쪽 GPU 상태가 남아 있었을 수 있다고 짐작만 합니다.

### 플레이북이 AMD 노드에서 하는 일

1. `/etc/modprobe.d` 의 `blacklist amdgpu` 줄을 지웁니다. Azure V710 이미지는 `blacklist-radeon-instinct.conf` 로 amdgpu 를 막아 둡니다.
2. `repo.radeon.com/amdgpu/latest` 를 추가하고 `amdgpu-dkms-firmware` 를 최신으로 깝니다(`state: latest`, 실행할 때마다 새 판이 있으면 올림).
   이 저장소에는 Ubuntu 보다 새 libdrm, Mesa, DKMS 드라이버도 있어서, apt 우선순위로 펌웨어 패키지만 받게 막았습니다. 시험 VM 에서 `apt-get -s upgrade` 가 이 저장소에서 받는 패키지는 0개였습니다.
3. 바뀐 것이 있으면 initramfs 를 다시 만들고 한 번 재부팅합니다.
4. GPU 가 `amdgpu` 에 연결됐는지(`/sys/bus/pci/drivers/amdgpu/<PCI 주소>`)와 `/dev/dri/renderD*` 로 판정합니다.
   `/dev/kfd` 는 드라이버 초기화가 실패해도 생겨서 판정에 쓰면 안 됩니다. 처음 만든 플레이북은 이것 때문에 구성 3 의 실패를 성공으로 넘겼습니다.
5. AMD ROCm 저장소에서 `amdrocm-amdsmi10.0` 만 깔고 `/usr/bin/amd-smi` 를 `update-alternatives` 로 등록합니다. 이 패키지는 `/opt/rocm/core-10.0/bin` 에만 깔려 PATH 에 없습니다.
6. 이미 MS 가이드대로 `amdgpu-dkms` 를 깐 노드는 그 드라이버와 펌웨어를 그대로 둡니다.

### amd-smi 로 볼 수 있는 것

| 지표 | DKMS 드라이버 | 커널 기본 드라이버 |
|---|---|---|
| 제품명, VRAM 크기·사용량 | 나옴 | 나옴 |
| 전력, 온도, 클럭, GFX 사용률 | N/A | N/A |

V710 VF 에서는 드라이버와 무관하게 VRAM 만 나옵니다. `amd-smi` 는 root 나 `render` 그룹이어야 읽습니다.

## NVIDIA 결과

| 구성 | 결과 |
|---|---|
| `ubuntu-drivers install --gpgpu` (플레이북의 NVIDIA 갈래) | **실제 VM 에서 시험 못 함** |

설계상 근거만 적습니다. Ubuntu 는 커널마다 Canonical 이 빌드하고 서명한 `linux-modules-nvidia-*` 패키지를 내므로, 그것이 깔리면 MOK 가 필요 없습니다.
그 패키지가 없어 DKMS 로 빠졌는데 Secure Boot 가 켜져 있으면, 플레이북은 MOK 가 필요하다는 메시지를 내고 실패합니다.
드라이버가 이미 응답하는 노드(`nvidia-smi -L`)는 건드리지 않습니다.
MS 문서상 Azure 의 NV A10 GRID 드라이버는 Secure Boot 를 꺼야 하고 apt 패키지가 없어서 플레이북 대상이 아닙니다.

## Secure Boot 관련 확인 사항

| 질문 | 답 | 근거 |
|---|---|---|
| VM 이 켜진 채 Secure Boot 를 끌 수 있나 | 없음. 정지(deallocate) 후 바꿔야 함 | MS Trusted Launch 문서 |
| cb-spider 로 만든 Azure VM 은 | Secure Boot, vTPM 모두 꺼진 채 만들어짐 | VM 2 의 `securityProfile` |
| MOK 등록·삭제를 자동화할 수 있나 | 없음. `mokutil --import`, `--delete` 모두 재부팅 중 콘솔 입력이 필요 | VM 1 에서 등록할 때 직접 겪음 |

## 남은 것

| 무엇 | 언제 |
|---|---|
| Secure Boot 켜진 VM 에서 플레이북 실행(구성 6) | VM 1 을 다시 켤지 정한 뒤 |
| NVIDIA 갈래를 실제 VM 에서 실행 | NVIDIA GPU VM 을 만들 때 |
| AMD 노드에서 모델 실행. 지금은 `install-docker` 가 NVIDIA Container Toolkit 을 깔고 compose 파일이 `nvidia` 장치를 요구함 | 별도 작업 |
| Ubuntu 밖의 배포판(RHEL 계열, Debian, Amazon Linux) | 별도 작업. 지금은 메시지만 내고 건너뜀 |
