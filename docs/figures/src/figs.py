"""Draws the figures of docs/api/README.md and docs/design/*.md.

Usage: python3 figs.py <out dir>, then embed_fonts.py and render.sh.
"""
import sys

from svglib import Fig

OUT = sys.argv[1]


def save(f, name):
    open(f"{OUT}/{name}.svg", "w", encoding="utf-8").write(f.svg())


def label(f, x, y, s, color="#334155", size=15, weight=700, anchor="middle"):
    f.text(x, y, s, size, color, weight, anchor)


# ------------------------------------------------------------------ 01 overview
f = Fig(1600, 1000, title="AI-App 전체 구조",
        subtitle="ETRI 가 VM 을 만들고, AI-Web 이 AI-App 을 불러 그 VM 에 추론 서비스와 응용을 올린다")
f.panel(40, 140, 300, 560, "협력 기관", "neutral")
f.box(65, 200, 250, 100, "ETRI", ["GPU VM 생성", "Travel Planner 포털"], "white")
f.box(65, 340, 250, 100, "BESP AI-Web", ["화면 · 배포 요청"], "blue")
f.box(65, 480, 250, 100, "경희대", ["모델 · 응용 추천 (나중)"], "white", dash="7 5")
f.panel(400, 140, 620, 560, "AI-App (이 repo)", "node")
f.box(430, 200, 260, 100, "카탈로그", ["/runtimes · /models · /apps"], "white")
f.box(730, 200, 260, 100, "추천 자리", ["/recommendations/*", "지금은 501"], "ghost", dash="7 5")
f.box(430, 350, 260, 100, "배포", ["/deployments"], "dark")
f.box(730, 350, 260, 100, "주소", ["/endpoints"], "white")
f.box(430, 520, 560, 90, "공유 저장소 stage", ["<배포 ID>/<csp>-<nodeId>/  compose 파일 · 응용 파일"], "store")
f.arrow("M560,300 L560,348", "blue")
f.arrow("M690,400 L728,400", "blue")
f.arrow("M690,250 L728,250", "gray", 2, "6 5")
f.arrow("M560,450 L560,518", "green")
label(f, 610, 492, "파일 배치", "#047857", 14, 400)

f.box(1100, 150, 460, 110, "CB-Tumblebug", ["VM · SSH 키 · 보안 그룹"], "warn")
f.box(1100, 380, 460, 110, "Semaphore", ["Ansible playbook 실행기"], "relay")
f.panel(660, 770, 900, 190, "Tumblebug Infra (노드)", "gpu")
for i, (n, sub) in enumerate([("vm1", "vLLM + Dify"), ("vm2", "Ollama + Open WebUI"), ("vm3", "vLLM + Langflow")]):
    f.box(690 + i * 290, 830, 250, 100, n, [sub], "white")

# 1 ETRI creates the VMs
f.arrow("M315,225 L365,225 L365,122 L1330,122 L1330,148", "amber", 3)
label(f, 1010, 114, "① VM 생성 (Tumblebug API 를 ETRI 가 직접 부름)", "#b45309")
f.arrow("M1560,205 L1585,205 L1585,865 L1562,865", "amber", 2.5, "7 5")
label(f, 1578, 540, "VM", "#b45309", 15, 700, "end")
# 2 and 7 AI-Web calls AI-App
f.arrow("M315,390 L398,390", "blue", 3)
label(f, 357, 372, "②⑦", "#1d4ed8", 16)
f.arrow("M315,530 L398,530", "gray", 2, "6 5")
label(f, 357, 512, "읽기", "#475569", 14, 400)
# 3 and 6 AI-App to Tumblebug
f.arrow("M650,348 L650,330 L1060,330 L1060,205 L1098,205", "amber", 3)
label(f, 860, 324, "③ 노드 · SSH 키 조회   ⑥ 포트 규칙 추가", "#b45309", 14)
# 4 AI-App to Semaphore
f.arrow("M650,450 L650,478 L1060,478 L1060,435 L1098,435", "orange", 3)
label(f, 860, 471, "④ 키 · inventory 등록, 태스크 실행", "#9a3412", 14)
# stage shared with Semaphore
f.add('<path d="M990,565 L1200,565 L1200,492" stroke="#059669" stroke-width="2.5" fill="none" stroke-dasharray="6 5"/>')
label(f, 1100, 590, "같은 볼륨 (Semaphore 가 읽음)", "#047857", 14, 400)
# 5 Semaphore to the nodes
f.arrow("M1400,490 L1400,768", "orange", 3)
label(f, 1388, 640, "⑤ SSH", "#9a3412", 16, 700, "end")
label(f, 1388, 664, "docker 설치 → compose up", "#9a3412", 14, 400, "end")
label(f, 1388, 686, "또는 파일 복사 → deploy.sh", "#9a3412", 14, 400, "end")

f.lines(70, 760, ["② 카탈로그 조회 · 배포 요청", "⑦ 주소 조회 (/endpoints)",
                  "추천은 경희대가 이 repo 에", "구현할 자리만 있음 (501)"], 15, "#334155", 400, "start", 26)
f.legend([("blue", "AI-App API 호출", None), ("amber", "CB-Tumblebug", None),
          ("orange", "Semaphore · SSH", None), ("gray", "나중 · 선택", "6 5")], 40, 985)
save(f, "01-overview")


# --------------------------------------------------- 02 one deployment, step by step
f = Fig(1600, 1180, title="배포 하나가 진행되는 순서",
        subtitle="POST /deployments 는 대상 노드를 확인한 뒤 202 를 돌려주고, 나머지는 노드마다 뒤에서 진행한다")
cols = [("호출자", "AI-Web · 스크립트", "blue"), ("AI-App", "배포 서비스", "node"),
        ("CB-Tumblebug", None, "warn"), ("공유 저장소", "stage", "store"),
        ("Semaphore", None, "relay"), ("노드", "VM", "gpu")]
X = [140, 400, 660, 920, 1180, 1440]
for (t, s, st), x in zip(cols, X):
    f.box(x - 110, 120, 220, 70, t, [s] if s else None, st, tsize=18)
    f.add(f'<line x1="{x}" y1="190" x2="{x}" y2="1120" stroke="#cbd5e1" stroke-width="2" stroke-dasharray="4 6"/>')


def msg(y, a, b, text, color="gray", dash=None, n=None):
    xa, xb = X[a], X[b]
    d = 8 if xb > xa else -8
    f.arrow(f"M{xa},{y} L{xb - d},{y}", color, 2.5, dash)
    label(f, min(xa, xb) + 16, y - 10, text, "#0f172a", 15, 400, "start")
    if n is not None:
        f.badge(xa - 24 if xb > xa else xa + 24, y, n, "#1e3a8a", 13)


def note(x, y, w, h, rows, style="ghost"):
    f.rect(x, y, w, h, style, rx=10, sw=1.5)
    f.lines(x + w / 2, y + 24, rows, 14, "#334155", 400, "middle", 21)


msg(240, 0, 1, "POST /deployments", "blue", n=1)
msg(290, 1, 2, "대상 노드가 있는지 확인. 없으면 404", "amber", n=2)
msg(340, 1, 0, "202 배포 기록 (status pending)", "blue", "6 5", n=3)
f.rect(250, 370, 1310, 700, "neutral", rx=16, sw=1.5, dash="8 6", fill="#f8fafc")
label(f, 270, 398, "노드마다 따로, 최대 AIAPP_DEPLOYMENT_MAX_PARALLEL 대까지 동시에", "#475569", 15, 700, "start")
msg(440, 1, 2, "노드 정보 (IP · 사용자 · sshKeyId · specId · 보안 그룹), SSH 키", "amber", n=4)
msg(500, 1, 3, "파일 배치 (추론: compose 생성 / 응용: 업로드 파일 + script)", "green", n=5)
note(1250, 470, 300, 60, ["dry-run 이면 여기서 끝", "(Semaphore · 보안 그룹은 안 건드림)"], "warn")
f.rect(280, 540, 1260, 380, "relay", rx=14, sw=1.5, dash="6 5", fill="#fffbf5")
label(f, 300, 566, "playbook 마다 반복: install-docker → compose-up (추론) 또는 copy-and-run (응용)", "#9a3412", 15, 700, "start")
msg(610, 1, 4, "SSH 키 · inventory 등록, 태스크 실행 (고정 template)", "orange", n=6)
msg(660, 4, 3, "배치된 파일 읽기", "green")
msg(710, 4, 5, "SSH 로 playbook 실행", "orange", n=7)
note(1310, 735, 220, 160, ["노드 안에서 확인", "추론: 헬스 경로,", "GPU 면 GPU 위인지", "응용: 포트 HTTP 응답,", "컨테이너 재시작 없음"], "gpu")
msg(800, 1, 4, "끝날 때까지 상태 확인, 로그를 받아 기록", "orange")
msg(860, 1, 4, "inventory · SSH 키 삭제 (성공 · 실패와 무관)", "orange", "6 5")
msg(970, 1, 2, "보안 그룹에 서빙 포트 규칙 추가 (없는 포트만)", "amber", n=8)
note(300, 1000, 500, 50, ["노드 결과가 모이면 배포 status 가 succeeded · partial · failed"])
msg(1100, 0, 1, "GET /deployments/{id} (끝날 때까지), GET /endpoints", "blue", n=9)
save(f, "02-deploy-sequence")


# ------------------------------------------------------------- 03 Semaphore layout
f = Fig(1600, 960, title="Semaphore 안에 무엇이 있나",
        subtitle="template 은 playbook 마다 하나로 고정, SSH 키와 inventory 는 실행마다 만들고 지운다")
f.box(40, 150, 260, 110, "AI-App", ["Semaphore API 로 등록 · 실행", "· 로그 · 삭제"], "node")
f.box(40, 380, 260, 140, "공유 볼륨 stage", ["/var/lib/aiapp/stage", "AI-App 이 쓰고", "Semaphore 가 읽음"], "store")
f.arrow("M170,260 L170,378", "green", 2.5)
label(f, 180, 325, "파일 배치", "#047857", 14, 400, "start")
f.panel(360, 130, 1200, 600, "Semaphore 프로젝트 aiapp  (AIAPP_SEMAPHORE_PROJECT)", "relay")
f.rect(390, 190, 560, 510, "white", rx=16, sw=1.5)
label(f, 410, 222, "처음 배포 때 만들고 계속 둠", "#9a3412", 17, 700, "start")
f.box(415, 245, 250, 80, "repository", ["aiapp-playbooks", "/ansible (deploy/ansible)"], "white", tsize=17, ssize=13)
f.box(680, 245, 250, 80, "environment", ["aiapp-empty"], "white", tsize=17, ssize=13)
f.box(415, 340, 515, 70, "inventory aiapp-no-hosts", ["template 에 묶어 두는 빈 자리"], "white", tsize=17, ssize=13)
f.rect(415, 430, 515, 250, "relay", rx=12, sw=1.5)
label(f, 435, 458, "template (고정, 지우지 않음)", "#9a3412", 16, 700, "start")
for i, t in enumerate(["aiapp-install-docker", "aiapp-compose-up", "aiapp-copy-and-run", "aiapp-compose-down"]):
    f.box(440 + (i % 2) * 245, 480 + (i // 2) * 95, 225, 75, t, [["install-docker.yml", "compose-up.yml", "copy-and-run.yml", "compose-down.yml"][i]], "white", tsize=15, ssize=13)
f.rect(990, 190, 540, 280, "warn", rx=16, sw=1.5, dash="8 6")
label(f, 1010, 222, "실행마다 등록 → 끝나면 삭제", "#92400e", 17, 700, "start")
f.box(1015, 245, 490, 80, "SSH 키", ["aiapp-<배포>-<csp>-<node>-<난수>"], "white", tsize=17, ssize=13)
f.box(1015, 360, 490, 80, "inventory", ["노드 주소 하나, 이 키를 씀"], "white", tsize=17, ssize=13)
f.arrow("M1260,325 L1260,358", "amber", 2)
f.box(990, 520, 540, 120, "태스크", ["template + 이번 inventory 로 실행", "이력 · 로그는 Semaphore 에 남음"], "relay")
f.arrow("M930,555 L988,555", "orange", 2.5)
f.arrow("M1260,470 L1260,518", "orange", 2.5)
f.arrow("M300,200 L358,200", "orange", 3)
f.add('<path d="M300,450 L340,450 L340,760 L1100,760 L1100,642" stroke="#059669" stroke-width="2.5" fill="none" stroke-dasharray="6 5"/>')
label(f, 720, 785, "태스크가 stage 의 파일을 읽음 (같은 볼륨)", "#047857", 15, 400)
f.box(1180, 800, 380, 120, "노드", ["/opt/aiapp/<작업 키>", "compose 프로젝트 aiapp-<작업 키>"], "gpu")
f.arrow("M1400,640 L1400,798", "orange", 3)
label(f, 1412, 775, "SSH", "#9a3412", 16, 700, "start")
f.box(40, 800, 560, 120, "Semaphore 화면", ["배포 기록 nodes[].steps[].taskUrl", "AIAPP_SEMAPHORE_UI_URL/project/<p>/history?t=<태스크>"], "white", dash="7 5")
f.arrow("M1060,642 L1060,860 L602,860", "gray", 2, "6 5")
save(f, "03-semaphore")


# ------------------------------------------------------------------ 04 call order
f = Fig(1600, 820, title="부르는 순서",
        subtitle="추론 서비스를 먼저 올리고, 그 주소를 env 로 넣어 응용을 올린다. 배포는 끝날 때까지 상태를 읽는다")
steps = [
    ("1", "카탈로그 읽기", ["GET /runtimes", "GET /models · /apps"], "white"),
    ("2", "응용 올리기", ["POST /apps", "(없을 때만, ports 포함)"], "white"),
    ("3", "추론 서비스 배포", ["POST /deployments", "type inference"], "dark"),
    ("4", "끝날 때까지 읽기", ["GET /deployments/{id}", "pending · running 동안"], "white"),
    ("5", "추론 주소 읽기", ["GET /endpoints", "http://노드:8000/v1"], "store"),
    ("6", "응용 배포", ["POST /deployments", "type app, env 에 5의 주소"], "dark"),
    ("7", "끝날 때까지 읽기", ["GET /deployments/{id}", ""], "white"),
    ("8", "응용 주소 읽기", ["GET /endpoints", "http://노드:포트"], "store"),
]
for i, (n, t, sub, st) in enumerate(steps):
    row, col = divmod(i, 4)
    x = 60 + col * 380 if row == 0 else 60 + (3 - col) * 380
    y = 170 + row * 260
    f.box(x, y, 320, 150, t, [s for s in sub if s], st, tsize=20, ssize=15)
    f.badge(x + 24, y + 24, n, "#ea580c" if st == "dark" else "#1e3a8a", 16)
    if i % 4 != 3:
        if row == 0:
            f.arrow(f"M{x+320},{y+75} L{x+378},{y+75}", "blue", 3)
        else:
            f.arrow(f"M{x},{y+75} L{x-58},{y+75}", "blue", 3)
f.arrow("M1360,320 L1360,428", "blue", 3)
f.rect(60, 650, 1460, 120, "ghost", rx=14, sw=1.5, dash="7 5")
label(f, 90, 690, "그 밖에", "#334155", 17, 700, "start")
f.lines(90, 725, ["실패했거나 궁금하면 GET /deployments/{id}/logs, 내릴 때는 DELETE /deployments/{id} (끝날 때까지 다시 읽기).",
                  "docs/api/examples/ 의 00~07 스크립트가 이 순서 그대로다."], 16, "#334155", 400, "start", 28)
save(f, "04-call-order")


# ------------------------------------------------------- 05 design: who asks whom
f = Fig(1600, 900, title="모델 카탈로그와 노드 배포가 놓이는 자리",
        subtitle="추천 Agent 는 카탈로그를 읽고 Infra 를 만들게 하며, AI-App 은 만들어진 노드에 배포만 한다")
f.panel(40, 140, 330, 430, "협력 기관 (이 repo 밖)", "neutral")
f.box(70, 200, 270, 110, "경희대 Agent", ["모델 추천"], "white")
f.box(70, 400, 270, 110, "인프라 추천 Agent", ["cm-beetle 형태"], "white")
f.panel(440, 140, 600, 560, "AI-App (이 repo)", "node")
f.box(470, 200, 250, 110, "모델 카탈로그", ["/models · /runtimes"], "white")
f.box(760, 200, 250, 110, "응용 저장소", ["/apps", "name · files · script"], "white")
f.box(470, 390, 540, 100, "노드 배포", ["/deployments"], "dark")
f.box(470, 560, 540, 100, "공유 저장소 stage", ["<uuid>/<csp>-<nodeId>/"], "store")
f.arrow("M595,310 L595,388", "blue")
f.arrow("M885,310 L885,388", "blue")
f.arrow("M740,490 L740,558", "green")
f.box(1120, 150, 440, 110, "CB-Tumblebug", ["VM 생성 · 노드 제어"], "warn")
f.box(1120, 400, 440, 110, "Semaphore", ["Ansible"], "relay")
f.panel(1120, 620, 440, 250, "Tumblebug Infra (노드)", "gpu")
for i, (n, sub) in enumerate([("aws vm1", "vLLM + Dify"), ("azure vm2", "Ollama + Open WebUI"), ("gcp vm3", "vLLM + Langflow")]):
    f.box(1145, 670 + i * 62, 390, 52, n + "   " + sub, None, "white", tsize=15)
f.arrow("M340,240 L468,240", "blue", 3)
label(f, 434, 230, "① 조회", "#1d4ed8", 14)
f.arrow("M205,310 L205,398", "gray", 3)
label(f, 215, 360, "② 고른 모델", "#334155", 14, 700, "start")
f.arrow("M340,455 L400,455 L400,122 L1340,122 L1340,148", "amber", 3)
label(f, 1000, 114, "③ Infra 생성 요청", "#b45309")
f.arrow("M1560,205 L1585,205 L1585,745 L1562,745", "amber", 2.5, "7 5")
f.arrow("M1010,420 L1060,420 L1060,205 L1118,205", "amber", 3)
label(f, 1066, 300, "④ 노드 · SSH 키", "#b45309", 14, 700, "start")
f.arrow("M1010,455 L1118,455", "orange", 3)
label(f, 1064, 478, "⑤ 실행", "#9a3412", 14)
f.add('<path d="M1010,610 L1240,610 L1240,512" stroke="#059669" stroke-width="2.5" fill="none" stroke-dasharray="6 5"/>')
label(f, 1125, 600, "같은 볼륨", "#047857", 14, 400)
f.arrow("M1440,510 L1440,618", "orange", 3)
label(f, 1450, 570, "⑥ SSH", "#9a3412", 15, 700, "start")
f.legend([("blue", "카탈로그 · 배포 API", None), ("amber", "CB-Tumblebug", None), ("orange", "Semaphore · SSH", None)], 40, 880)
save(f, "05-design-context")


# --------------------------------------------------------- 06 catalog data model
f = Fig(1600, 800, title="카탈로그에 담기는 것",
        subtitle="모델은 아티팩트(어디에 어떤 형식으로 있나)를 갖고, 아티팩트는 자신을 띄울 수 있는 런타임을 적는다")


def entity(x, y, w, name, fields, style="white", note=None):
    h = 56 + len(fields) * 30 + 16
    f.rect(x, y, w, h, style, rx=12, sw=2)
    f.add(f'<rect x="{x}" y="{y}" width="{w}" height="50" rx="12" fill="#1e3a8a"/>')
    f.add(f'<rect x="{x}" y="{y+36}" width="{w}" height="14" fill="#1e3a8a"/>')
    f.text(x + w / 2, y + 33, name, 19, "#ffffff", 700, "middle")
    for i, fl in enumerate(fields):
        f.text(x + 18, y + 82 + i * 30, fl, 15, "#0f172a", 400)
    return h


entity(40, 140, 330, "Model", ["id · name", "family · publisher · parameters", "tasks[]  chat, vision-chat, embedding",
                               "inputModalities[]  text, image", "contextLength · license"])
entity(450, 140, 360, "Artifact", ["id", "source  huggingface, ollama, …", "ref  (HF repo id, Ollama 태그)",
                                   "format  hf, gguf", "precision  BF16, FP8, Q4_K_M", "sizeGiB"])
entity(890, 140, 300, "RuntimeCompat", ["runtimeId", "minVersion", "args[]  이 아티팩트에", "필요한 런타임 인자"], "warn")
entity(1260, 140, 300, "RuntimeProfile", ["id  vllm, ollama", "image (태그 고정)", "formats[]",
                                         "port · healthPath", "apiBasePath"], "relay")
f.arrow("M370,200 L448,200", "gray", 2.5)
label(f, 409, 190, "1..*", "#475569", 14, 400)
f.arrow("M810,200 L888,200", "gray", 2.5)
label(f, 849, 190, "1..*", "#475569", 14, 400)
f.arrow("M1190,200 L1258,200", "orange", 2.5, "6 5")
label(f, 1224, 190, "id", "#9a3412", 14, 400)
f.rect(40, 440, 1520, 2, "ghost", rx=0, sw=0)
entity(40, 480, 330, "App", ["name"])
entity(450, 480, 360, "AppVersion", ["version  (올릴 때마다 1씩)", "script  (deploy.sh)", "ports[]", "createdAt"])
entity(890, 480, 300, "AppFile", ["name", "sizeBytes", "sha256"])
f.arrow("M370,540 L448,540", "gray", 2.5)
label(f, 409, 530, "1..*", "#475569", 14, 400)
f.arrow("M810,540 L888,540", "gray", 2.5)
label(f, 849, 530, "*", "#475569", 14, 400)
f.lines(1260, 530, ["배포는 아티팩트 단위다.", "추론 배포 = 아티팩트 + 런타임 하나.", "응용 배포 = 앱 이름 + 버전.",
                    "", "args 순서: 서비스 인자 →", "RuntimeCompat.args →", "요청 inference.args"], 16, "#334155", 400, "start", 28)
save(f, "06-catalog-model")


# ------------------------------------------------------------ 08 travel scenario
f = Fig(1600, 900, title="ETRI 여행 포털 시나리오의 노드 3대",
        subtitle="노드마다 런타임 + 모델(추론 서비스) 하나와 그 주소를 쓰는 응용 하나")
f.box(560, 130, 480, 90, "여행 계획 통합 포털", ["별도 노드, ETRI 환경에만 있음"], "dark")
nodes = [
    ("aws vm1", "관광 문서 분석 (RAG)", "Dify", "vLLM :8000", "Qwen3-Embedding-0.6B", "HF, BF16", "g4dn.xlarge T4"),
    ("azure vm2", "이미지 분석 · 여행 계획 생성", "Open WebUI", "Ollama :11434", "Qwen3-VL-8B", "Ollama qwen3-vl:8b, GGUF", "NC4as_T4_v3 T4"),
    ("gcp vm3", "일정 검증 · 외부 API · 알림", "Langflow", "vLLM :8000", "Ministral 3 8B", "HF, FP8", "g2-standard-4 L4"),
]
for i, (n, role, app, rt, m, mf, spec) in enumerate(nodes):
    x = 60 + i * 510
    f.panel(x, 290, 470, 560, n, "gpu")
    label(f, x + 446, 326, spec, "#6d28d9", 15, 400, "end")
    f.text(x + 235, 375, role, 17, "#5b21b6", 700, "middle", italic=True)
    f.box(x + 60, 410, 350, 100, app, ["응용 (app)"], "blue")
    f.box(x + 60, 570, 350, 100, rt, ["런타임"], "relay")
    f.box(x + 60, 720, 350, 100, m, [mf], "store")
    f.arrow(f"M{x+235},510 L{x+235},568", "blue", 3)
    label(f, x + 245, 545, "OpenAI 호환 API", "#1d4ed8", 14, 400, "start")
    f.add(f'<line x1="{x+235}" y1="670" x2="{x+235}" y2="720" stroke="#059669" stroke-width="3"/>')
    f.arrow(f"M800,220 L{x+235},288", "gray", 2.5)
label(f, 1575, 885, "vm3 는 gcp 권한이 정리될 때까지 aws g6.xlarge(L4)로 시험했다 (live-test-2026-11-03.md)", "#475569", 14, 400, "end")
save(f, "07-travel-nodes")
