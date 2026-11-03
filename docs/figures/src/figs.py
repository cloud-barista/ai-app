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
for i, (n, sub) in enumerate([("aws vm1", "vLLM + Dify"), ("azure vm2", "Ollama + Open WebUI"), ("aws vm3", "vLLM + Langflow")]):
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
f = Fig(1600, 1270, title="모델 카탈로그와 응용 카탈로그에 담기는 것",
        subtitle="필드를 위에서 아래로 적었다. 파란 줄은 배열이고, 화살표가 그 배열에 담기는 항목을 가리킨다")

ROW = 32


def entity(x, y, w, name, rows, sub=None, style="white"):
    """rows: (field, type, example, kind) with kind None, "list" (array of another box) or "key" (looked up)."""
    top = 58 if sub else 50
    h = top + len(rows) * ROW + 14
    f.rect(x, y, w, h, style, rx=12, sw=2)
    f.add(f'<rect x="{x}" y="{y}" width="{w}" height="{top}" rx="12" fill="#1e3a8a"/>')
    f.add(f'<rect x="{x}" y="{y+top-14}" width="{w}" height="14" fill="#1e3a8a"/>')
    f.text(x + w / 2, y + (28 if sub else 33), name, 19, "#ffffff", 700, "middle")
    if sub:
        f.text(x + w / 2, y + 49, sub, 13, "#bfdbfe", 400, "middle")
    ys = {}
    col = x + 16 + max(len(r[0]) for r in rows) * 8.4 + 14
    for i, (fl, ty, ex, kind) in enumerate(rows):
        ry = y + top + 8 + i * ROW
        if kind == "list":
            f.add(f'<rect x="{x+6}" y="{ry}" width="{w-12}" height="{ROW-4}" rx="6" fill="#dbeafe"/>')
        if kind == "key":
            f.add(f'<rect x="{x+6}" y="{ry}" width="{w-12}" height="{ROW-4}" rx="6" fill="#ffedd5"/>')
        tc = "#1d4ed8" if kind == "list" else ("#9a3412" if kind == "key" else "#64748b")
        f.text(x + 16, ry + 21, fl, 15, "#0f172a", 700 if kind else 400)
        f.text(col, ry + 21, ty, 14, tc, 700 if kind else 400)
        if ex:
            f.text(x + w - 14, ry + 21, ex, 13, "#94a3b8", 400, "end")
        ys[fl] = ry + (ROW - 4) / 2
    return h, ys


label(f, 40, 140, "모델 카탈로그   GET /aiapp/models · GET /aiapp/runtimes   (type inference 배포가 가리킴)", "#1e3a8a", 18, 700, "start")
_, m = entity(40, 160, 350, "Model", [
    ("id", "string", "ministral-3-8b", None),
    ("name", "string", "표시 이름", None),
    ("family", "string", "ministral-3", None),
    ("publisher", "string", "mistralai", None),
    ("parameters", "string", "8B", None),
    ("tasks", "string[]", "chat, embedding", None),
    ("inputModalities", "string[]", "text, image", None),
    ("contextLength", "int", "모르면 비움", None),
    ("license", "string", "apache-2.0", None),
    ("artifacts", "Artifact[]", "1개 이상", "list")], "모델 하나")
_, a = entity(460, 160, 350, "Artifact", [
    ("id", "string", "hf-fp8", None),
    ("source", "string", "huggingface · ollama", None),
    ("ref", "string", "HF repo · Ollama 태그", None),
    ("format", "string", "hf · gguf", None),
    ("precision", "string", "BF16 · FP8 · Q4_K_M", None),
    ("sizeGiB", "number", "6.2", None),
    ("runtimes", "RuntimeCompat[]", "1개 이상", "list")], "받을 파일 한 벌")
_, c = entity(880, 160, 300, "RuntimeCompat", [
    ("runtimeId", "string", "vllm", "key"),
    ("minVersion", "string", "0.11.0", None),
    ("args", "string[]", "기본 인자", None)], "이 파일을 띄울 수 있는 런타임")
_, r = entity(1250, 160, 310, "RuntimeProfile", [
    ("id", "string", "vllm · ollama", "key"),
    ("image", "string", "태그 고정", None),
    ("formats", "string[]", "hf · gguf", None),
    ("port", "int", "8000 · 11434", None),
    ("healthPath", "string", "/health", None),
    ("apiBasePath", "string", "/v1", None)], "/aiapp/runtimes 에 따로 있음", "relay")

# array row -> header of the item box
f.arrow(f"M384,{m['artifacts']} L425,{m['artifacts']} L425,190 L456,190", "blue", 2.5)
label(f, 425, 180, "여러 개", "#1d4ed8", 13, 700)
f.arrow(f"M804,{a['runtimes']} L845,{a['runtimes']} L845,190 L876,190", "blue", 2.5)
label(f, 845, 180, "여러 개", "#1d4ed8", 13, 700)
f.arrow(f"M1174,{c['runtimeId']} L1246,{r['id']}", "orange", 2.5, "6 5")
label(f, 1212, c['runtimeId'] + 26, "같은 id 를", "#9a3412", 13, 700)
label(f, 1212, c['runtimeId'] + 44, "찾아 씀", "#9a3412", 13, 700)

# a real entry, nested the same way
f.rect(880, 450, 680, 270, "ghost", rx=12, sw=1.5)
label(f, 900, 480, "실제 카탈로그 한 건 (시드의 Ministral, 줄임)", "#334155", 15, 700, "start")
js = ['{ "id": "ministral-3-8b", "tasks": ["chat"],',
      '  "artifacts": [',
      '    { "id": "hf-fp8", "source": "huggingface", "format": "hf",',
      '      "ref": "mistralai/Ministral-3-8B-Instruct-2512",',
      '      "precision": "FP8",',
      '      "runtimes": [',
      '        { "runtimeId": "vllm",',
      '          "args": ["--max-model-len", "32768", ...] } ] } ] }']
for i, t in enumerate(js):
    f.add(f'<text x="900" y="{512 + i * 26}" font-size="14" fill="#0f172a" font-family="DejaVu Sans Mono,monospace" '
          f'xml:space="preserve">{t.replace("&", "&amp;").replace("<", "&lt;")}</text>')

f.lines(40, 600, ["추론 배포(type inference)는 아티팩트 하나를 골라서 한다.",
                  "  POST /aiapp/deployments  { modelId, artifactId, runtimeId }",
                  "runtimeId 는 그 아티팩트의 runtimes[] 안에 있어야 하고,",
                  "그 런타임의 formats 에 아티팩트의 format 이 있어야 한다.",
                  "vLLM 인자는 런타임 기본 → RuntimeCompat.args → 요청 inference.args",
                  "순서로 붙는다. 같은 인자가 두 번 오면 뒤의 것이 이긴다."],
        15, "#334155", 400, "start", 26)

f.rect(40, 790, 1520, 2, "ghost", rx=0, sw=0)
label(f, 40, 840, "응용 카탈로그   POST·GET /aiapp/apps   (type app 배포가 가리킴. 처음에는 비어 있고 올린 것만 있음)", "#1e3a8a", 18, 700, "start")
_, ap = entity(40, 860, 350, "App", [
    ("name", "string", "open-webui", None),
    ("versions", "AppVersion[]", "올릴 때마다 추가", "list")], "응용 하나")
_, av = entity(460, 860, 350, "AppVersion", [
    ("version", "int", "1, 2, 3 …", None),
    ("script", "string", "deploy.sh", None),
    ("ports", "int[]", "3000", None),
    ("createdAt", "time", "", None),
    ("files", "AppFile[]", "업로드한 파일", "list")], "업로드 한 번")
_, af = entity(880, 860, 300, "AppFile", [
    ("name", "string", "docker-compose.yml", None),
    ("sizeBytes", "int", "812", None),
    ("sha256", "string", "", None)], "파일 하나")
f.arrow(f"M384,{ap['versions']} L425,{ap['versions']} L425,890 L456,890", "blue", 2.5)
label(f, 425, 880, "여러 개", "#1d4ed8", 13, 700)
f.arrow(f"M804,{av['files']} L845,{av['files']} L845,890 L876,890", "blue", 2.5)
label(f, 845, 880, "여러 개", "#1d4ed8", 13, 700)
f.lines(1250, 900, ["같은 name 으로 다시 올리면", "version 이 하나 늘고", "옛 버전도 남는다.", "",
                    "응용 배포(type app) 요청은", "{ name, version }. version 을", "비우면 최신."], 15, "#334155", 400, "start", 27)
f.lines(40, 1130, ["대상 노드와 다른 노드의 주소(env)는 등록에 없다. 배포 요청에서 받는다.",
                   "파일 내용은 AI-App 이 보관하고, 배포할 때 노드의 /opt/aiapp/app-<name>/ 에 그대로 놓인다."],
        15, "#334155", 400, "start", 26)
f.legend([("blue", "배열이 담는 항목", None), ("orange", "id 로 찾아 씀", "6 5")], 40, 1230)
save(f, "06-catalog-model")


# ------------------------------------------------------------ 08 travel scenario
f = Fig(1600, 900, title="ETRI 여행 포털 시나리오의 노드 3대",
        subtitle="노드마다 런타임 + 모델(추론 서비스) 하나와 그 주소를 쓰는 응용 하나")
f.box(560, 130, 480, 90, "여행 계획 통합 포털", ["별도 노드, ETRI 환경에만 있음"], "dark")
nodes = [
    ("aws vm1", "관광 문서 분석 (RAG)", "Dify", "vLLM :8000", "Qwen3-Embedding-0.6B", "HF, BF16", "g4dn.xlarge T4"),
    ("azure vm2", "이미지 분석 · 여행 계획 생성", "Open WebUI", "Ollama :11434", "Qwen3-VL-8B", "Ollama qwen3-vl:8b, GGUF", "NC4as_T4_v3 T4"),
    ("aws vm3", "일정 검증 · 외부 API · 알림", "Langflow", "vLLM :8000", "Ministral 3 8B", "HF, FP8", "g6.xlarge L4"),
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
    label(f, x + 245, 545, "Ollama API" if rt.startswith("Ollama") else "OpenAI 호환 API", "#1d4ed8", 14, 400, "start")
    f.add(f'<line x1="{x+235}" y1="670" x2="{x+235}" y2="720" stroke="#059669" stroke-width="3"/>')
    f.arrow(f"M800,220 L{x+235},288", "gray", 2.5)
label(f, 1575, 885, "vm3 는 계획상 gcp g2-standard-4(L4) 이지만 gcp 권한 문제로 일단 aws g6.xlarge(L4) 로 진행했다", "#475569", 14, 400, "end")
save(f, "07-travel-nodes")


# ---------------------------------------------------------- 08 API exposure
f = Fig(1600, 1000, title="API 는 어디로 열려 있나",
        subtitle="관리 요청은 AI-App 으로 가고, 추론·응용 요청은 AI-App 을 거치지 않고 노드로 바로 간다")
f.panel(40, 140, 300, 700, "부르는 쪽", "neutral")
f.box(65, 200, 250, 90, "BESP AI-Web", ["배포 화면"], "blue")
f.box(65, 320, 250, 90, "ETRI 포털 · 스크립트", ["curl, 배치 작업"], "white")
f.box(65, 440, 250, 90, "에이전트", ["MCP 클라이언트"], "white")
f.box(65, 600, 250, 110, "최종 사용자", ["브라우저 · 앱", "(포털 뒤에서)"], "white")

f.panel(420, 140, 520, 560, "AI-App 서버   :8090", "node")
f.box(450, 200, 460, 100, "/aiapp/*  REST", ["Authorization: Bearer <토큰>", "카탈로그 · 배포 · 내리기 · /endpoints"], "dark")
f.box(450, 330, 220, 90, "/aiapp/api/", ["Swagger UI", "토큰 없이"], "white")
f.box(690, 330, 220, 90, "/aiapp/ui", ["운영자 콘솔"], "white")
f.box(450, 450, 220, 90, "/aiapp/mcp", ["같은 기능의 MCP 도구"], "white")
f.box(690, 450, 220, 90, "/aiapp/readyz", ["살아 있나 (토큰 없이)"], "white")
f.box(450, 580, 220, 90, "CB-Tumblebug", ["노드 조회 · 보안 그룹"], "warn", dash="6 4")
f.box(690, 580, 220, 90, "Semaphore", ["SSH 로 노드 작업"], "relay", dash="6 4")
label(f, 680, 725, "점선 상자는 AI-App 뒤에 있어 협력 기관이 직접 부르지 않는다", "#64748b", 14, 400)

f.panel(1020, 140, 540, 700, "노드 (GPU VM)   보안 그룹 TCP 인바운드", "gpu")
f.box(1050, 200, 480, 110, "vLLM   :8000/v1", ["OpenAI 호환: /v1/models, /v1/chat/completions,", "/v1/embeddings   헬스 /health"], "relay")
f.box(1050, 340, 480, 110, "Ollama   :11434", ["Ollama API /api/*  ·  OpenAI 호환 /v1/*", "헬스 /"], "relay")
f.box(1050, 480, 480, 130, "응용 포트", ["업로드 때 준 ports (배포 요청 app.ports 가 우선)", "Dify 80 · Open WebUI 3000 · Langflow 7860"], "blue")
f.lines(1050, 660, ["배포가 성공하면 AI-App 이 이 포트들을 노드의", "보안 그룹에 0.0.0.0/0 으로 연다 (이미 있으면 그대로).",
                    "내릴 때는 닫지 않는다."], 15, "#5b21b6", 400, "start", 26)

f.arrow("M315,245 L448,245", "blue", 3)
f.arrow("M315,365 L395,365 L395,270 L448,270", "blue", 3)
f.arrow("M315,485 L448,495", "blue", 2.5)
f.badge(380, 225, 1, "#2563eb")
label(f, 380, 200, "배포 · 주소 조회", "#1d4ed8", 14, 700)
f.arrow("M910,625 L1016,625", "orange", 2.5, "6 5")
label(f, 963, 650, "SSH", "#9a3412", 14, 700)
f.arrow("M315,395 L350,395 L350,880 L975,880 L975,255 L1048,255", "green", 3)
f.arrow("M190,710 L190,900 L995,900 L995,545 L1048,545", "green", 3)
f.badge(660, 880, 2, "#059669")
label(f, 690, 935, "② GET /aiapp/endpoints 가 준 url 로 바로 부른다. 이 길에는 AI-App 토큰이 없다", "#065f46", 15, 700, "start")
f.legend([("blue", "AI-App API (토큰)", None), ("green", "노드 서비스 (직접)", None), ("orange", "AI-App 이 노드에 하는 작업", "6 5")], 40, 975)
save(f, "08-api-exposure")


# ---------------------------------------------------------- 09 deployment states
f = Fig(1600, 760, title="배포 기록의 상태",
        subtitle="POST 가 기록을 만들고, 뒤에서 노드마다 진행해 끝 상태로 간다. DELETE 는 끝난 기록만 받는다")
def st(x, y, name, sub, style, w=200, h=84):
    f.box(x, y, w, h, name, [sub], style, tsize=20, ssize=13)
st(40, 300, "POST", "/aiapp/deployments", "dark", 210)
st(330, 300, "pending", "받음 · 202", "white")
st(610, 300, "running", "노드에서 진행", "node")
st(900, 160, "succeeded", "모든 노드 성공", "store")
st(900, 300, "partial", "일부만 성공", "warn")
st(900, 440, "failed", "모든 노드 실패", "bad")
st(610, 520, "interrupted", "AI-App 재기동 등", "ghost")
st(330, 140, "dry-run", "파일 배치까지 · 200", "ghost")
st(1240, 300, "removing", "DELETE · 202", "relay")
st(1240, 160, "removed", "모든 노드에서 내림", "neutral")
st(1240, 440, "remove-failed", "일부 실패", "bad")
f.arrow("M250,342 L328,342", "gray", 2.5)
f.arrow("M145,300 L145,182 L328,182", "gray", 2.5, "6 5")
label(f, 150, 240, "dry-run 설정", "#64748b", 13, 400, "start")
f.arrow("M530,342 L608,342", "gray", 2.5)
f.arrow("M810,330 L898,202", "green", 2.5)
f.arrow("M810,342 L898,342", "amber", 2.5)
f.arrow("M810,354 L898,482", "red", 2.5)
f.arrow("M710,384 L710,518", "gray", 2, "6 5")
f.arrow("M1100,202 L1170,202 L1170,330 L1238,330", "orange", 2.5)
f.arrow("M1100,342 L1238,342", "orange", 2.5)
f.arrow("M1100,482 L1170,482 L1170,356 L1238,356", "orange", 2.5)
label(f, 1170, 150, "DELETE", "#9a3412", 14, 700)
f.arrow("M1340,300 L1340,246", "gray", 2.5)
f.arrow("M1340,384 L1340,438", "red", 2.5)
f.arrow("M1440,482 L1500,482 L1500,342 L1442,342", "orange", 2, "6 5")
label(f, 1510, 420, "DELETE 다시", "#9a3412", 13, 700, "start")
f.lines(40, 640, ["pending · running · removing 인 기록에 DELETE 를 부르면 409. 끝날 때까지 GET /aiapp/deployments/{id} 를 읽는다.",
                  "노드 상태 nodes[].status 도 같은 값을 쓴다 (partial 만 없음). 실패한 노드의 이유는 nodes[].error 와 GET .../logs 에 있다.",
                  "succeeded 인 노드의 주소는 GET /aiapp/endpoints 로 받는다. 같은 노드에 같은 런타임·같은 응용을 다시 배포하면 이전 배포는 주소 목록에서 빠진다."],
        15, "#334155", 400, "start", 28)
save(f, "09-deploy-states")


# ---------------------------------------------------------- 10 node layout
f = Fig(1600, 800, title="노드 위에 놓이는 것",
        subtitle="폴더와 compose 프로젝트는 배포 ID 가 아니라 작업 키(ms-<런타임>, app-<이름>)로 정해진다")
f.panel(40, 140, 1520, 560, "노드 하나 (예: aws vm1)", "gpu")
cols = [
    ("추론 서비스", "ms-vllm", ["docker-compose.yml", ".env", ".aiapp-files"], ["engine  (vllm/vllm-openai)", "컨테이너 이름 aiapp-ms-vllm-engine-1"],
     "볼륨: 받은 모델 가중치", ":8000", "relay"),
    ("응용", "app-dify", ["deploy.sh (.aiapp-run.sh)", ".env", "dify-src/ ...", ".aiapp-files"],
     ["nginx · api · worker · web", "db_postgres · redis · weaviate", "sandbox · plugin_daemon ..."],
     "볼륨: DB · 업로드 데이터", ":80", "blue"),
]
for i, (kind, key, files, conts, vol, port, sty) in enumerate(cols):
    x = 80 + i * 740
    label(f, x, 210, f"{kind}   작업 키 {key}", "#5b21b6", 18, 700, "start")
    f.rect(x, 230, 320, 220, "white", rx=12)
    label(f, x + 20, 262, f"/opt/aiapp/{key}/", "#0f172a", 16, 700, "start")
    for j, fl in enumerate(files):
        f.text(x + 40, 300 + j * 32, fl, 15, "#475569")
    f.rect(x + 350, 230, 330, 220, sty, rx=12)
    label(f, x + 370, 262, f"compose 프로젝트 aiapp-{key}", "#0f172a", 16, 700, "start")
    for j, c in enumerate(conts):
        f.text(x + 370, 300 + j * 32, c, 15, "#334155")
    f.text(x + 370, 430, vol, 14, "#047857", 700)
    f.arrow(f"M{x+320},340 L{x+348},340", "gray", 2)
    f.pill(x + 450, 480, 130, 50, port)
    f.arrow(f"M{x+515},450 L{x+515},478", "gray", 2)
f.lines(80, 590, ["같은 작업 키로 다시 배포하면: 폴더의 파일을 새 것으로 바꾸고(이전 목록에만 있던 파일은 지움), 컨테이너를 새로 만든다. named volume 은 남는다.",
                  "그래서 노드 하나에 추론 서비스는 런타임마다 하나, 응용은 이름마다 하나다. 서로 다른 작업 키는 서로 건드리지 않는다.",
                  "DELETE 는 docker compose down 만 한다. 폴더와 볼륨은 남아 다음 배포가 다시 쓴다."], 15, "#334155", 400, "start", 30)
label(f, 40, 750, "응용 스크립트 안의 docker compose 는 COMPOSE_PROJECT_NAME=aiapp-<작업 키> 를 받으므로 같은 프로젝트로 묶인다.", "#475569", 15, 400, "start")
save(f, "10-node-layout")


# ---------------------------------------------------------- 11 success checks
f = Fig(1600, 800, title="노드가 succeeded 가 되기까지",
        subtitle="Semaphore 태스크가 노드에서 차례로 확인한다. 하나라도 안 되면 그 노드는 failed 이고 이유가 기록에 남는다")
def lane(y, title, steps):
    label(f, 40, y - 20, title, "#1e3a8a", 18, 700, "start")
    x = 40
    for i, (t, sub, kind, check) in enumerate(steps):
        w = 188
        f.box(x, y, w, 110, t, sub, kind, tsize=17, ssize=13)
        if check:
            f.arrow(f"M{x+w/2},{y+110} L{x+w/2},{y+142}", "red", 2, "5 4")
            label(f, x + w / 2, y + 162, "안 되면 failed", "#b91c1c", 13, 700)
        if i < len(steps) - 1:
            f.arrow(f"M{x+w},{y+55} L{x+w+20},{y+55}", "gray", 2.5)
        x += w + 20
    return x
end = lane(170, "추론 서비스 (Semaphore 템플릿 compose-up)", [
    ("docker 준비", ["install-docker", "GPU 면 nvidia toolkit"], "white", True),
    ("파일 놓기 · 기동", ["compose up", "모델 받기 (처음은 수십 분)"], "white", True),
    ("헬스 응답", ["vLLM /health", "Ollama /"], "node", True),
    ("GPU 위인가", ["vLLM: nvidia-smi 프로세스", "Ollama: 로그의 library"], "gpu", True),
    ("playbook 성공", ["gpuCheck 에 근거 줄"], "store", False),
])
lane(440, "응용 (Semaphore 템플릿 copy-and-run)", [
    ("docker 준비", ["install-docker"], "white", True),
    ("파일 놓기", ["/opt/aiapp/app-<이름>", "이전 컨테이너 내림"], "white", False),
    ("스크립트 실행", ["deploy.sh 가 0 으로", "끝나야 함"], "white", True),
    ("포트 응답", ["curl 127.0.0.1:<port>/", "10초 간격 60번까지"], "node", True),
    ("30초 뒤 안정", ["재시작 0 번", "비정상 종료 0 개"], "gpu", True),
    ("playbook 성공", [""], "store", False),
])
f.box(1300, 170, 260, 380, "포트 열기", ["AI-App 이 CB-Tumblebug 으로", "노드 보안 그룹에", "TCP 인바운드 추가", "", "실패해도 succeeded,", "portsError 에 이유"], "warn", tsize=19, ssize=14)
f.arrow("M1060,225 L1298,225", "gray", 2.5)
f.arrow("M1268,495 L1298,495", "gray", 2.5)
f.pill(1330, 580, 200, 56, "succeeded")
f.arrow("M1430,550 L1430,578", "gray", 2.5)
f.lines(40, 680, ["HTTP 응답이면 상태 코드와 상관없이(404, 500 도) 응답으로 본다. 빈 응답·연결 거절은 아니다. docker-proxy 가 TCP 연결은 늘 받아 주기 때문이다.",
                  "GPU 를 요청했는데 CPU 로 돌면(드라이버가 오래됨 등) failed 이고, error 에 근거 줄(Ollama 의 library=cpu 줄 등)이 남는다.",
                  "확인은 배포 때 한 번이다. 그 뒤에 컨테이너가 죽는지는 보지 않는다."], 15, "#334155", 400, "start", 30)
save(f, "11-success-checks")


# ---------------------------------------------------------- 12 example apps
f = Fig(1600, 960, title="예시 응용 셋은 모델에 어떻게 닿나",
        subtitle="배포 요청의 env 가 노드의 .env 가 되고, 응용은 그 주소로 추론 서비스를 부른다")
apps = [
    ("Dify", "80", "LLM 앱 · RAG 워크플로 빌더",
     ["nginx · web · api · worker", "postgres · redis · weaviate", "sandbox · plugin_daemon (16개)"],
     "VLLM_BASE_URL", "화면에서 모델 공급자로 등록", ["Settings → Model Provider →", "OpenAI-API-compatible 에", "주소를 넣는다 (스크립트가 안내만 함)"],
     ["/install 에서 관리자 만들기", "모델 공급자 등록", "지식(문서) 올리고 앱 만들기"]),
    ("Open WebUI", "3000", "ChatGPT 같은 대화 화면",
     ["open-webui (1개)", "대화 기록은 볼륨에"],
     "OLLAMA_BASE_URL", "env 만으로 연결됨", ["기동할 때 그 Ollama 를 붙인다.", "모델 목록이 바로 보인다", "(/v1 없이 :11434 까지만)"],
     ["첫 계정이 관리자", "모델 고르고 대화", "이미지도 올릴 수 있음 (VL 모델)"]),
    ("Langflow", "7860", "노드를 이어 흐름을 만드는 빌더",
     ["langflow (1개)", "SQLite DB 는 볼륨에 (흐름 보존)"],
     "MODEL_BASE_URL", "deploy.sh 가 Generic 전역 변수로 만듦", ["OpenAI 컴포넌트의 OpenAI API Base 칸에서", "MODEL_BASE_URL 을 고른다.", "그 호스트는 http 허용 목록에 들어감"],
     ["관리자로 로그인", "흐름에 모델 컴포넌트 연결", "Playground 에서 실행 · API 로 공개"]),
]
for i, (name, port, what, conts, env, how, howsub, user) in enumerate(apps):
    x = 40 + i * 515
    f.panel(x, 140, 495, 760, f"{name}   :{port}", "node")
    f.text(x + 24, 200, what, 16, "#1e40af", 700)
    f.box(x + 24, 225, 447, 130, "컨테이너", conts, "white", tsize=16, ssize=14)
    f.box(x + 24, 390, 447, 70, f"env  {env}", [how], "warn", tsize=16, ssize=14)
    f.box(x + 24, 495, 447, 140, "모델에 붙는 방법", howsub, "relay", tsize=16, ssize=14)
    f.box(x + 24, 670, 447, 130, "사용자가 하는 일", user, "store", tsize=16, ssize=14)
    f.arrow(f"M{x+247},355 L{x+247},388", "gray", 2)
    f.arrow(f"M{x+247},460 L{x+247},493", "gray", 2)
    f.arrow(f"M{x+247},635 L{x+247},668", "gray", 2)
    label(f, x + 247, 870, "ETRI 예: 같은 노드의 vLLM :8000/v1" if name != "Open WebUI" else "ETRI 예: 같은 노드의 Ollama :11434", "#9a3412", 15, 700)
label(f, 40, 940, "env 값은 배포 기록(GET /aiapp/deployments/{id})과 노드의 .env 에 평문으로 남는다. 비밀번호를 넣을 때는 이 점을 알고 넣는다.", "#475569", 15, 400, "start")
save(f, "12-example-apps")
