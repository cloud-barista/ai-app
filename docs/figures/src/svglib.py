"""Small helper that draws the document figures as SVG in one look."""
from xml.sax.saxutils import escape

FONT = "'Noto Sans CJK KR','Noto Sans KR','Malgun Gothic',sans-serif"

STYLE = {
    "neutral": ("#f1f5f9", "#94a3b8", "#0f172a", "#475569"),
    "white":   ("#ffffff", "#64748b", "#0f172a", "#475569"),
    "node":    ("#eff6ff", "#3b82f6", "#1d4ed8", "#1e40af"),
    "blue":    ("#ffffff", "#3b82f6", "#0f172a", "#475569"),
    "relay":   ("#fff7ed", "#ea580c", "#9a3412", "#9a3412"),
    "store":   ("#ecfdf5", "#059669", "#065f46", "#047857"),
    "gpu":     ("#f5f3ff", "#7c3aed", "#5b21b6", "#6d28d9"),
    "bad":     ("#fef2f2", "#dc2626", "#991b1b", "#b91c1c"),
    "warn":    ("#fffbeb", "#d97706", "#92400e", "#b45309"),
    "dark":    ("#1e3a8a", "#1e3a8a", "#ffffff", "#bfdbfe"),
    "ghost":   ("#f8fafc", "#cbd5e1", "#64748b", "#94a3b8"),
}
LINE = {"gray": "#475569", "blue": "#2563eb", "orange": "#ea580c", "green": "#059669",
        "red": "#dc2626", "purple": "#7c3aed", "amber": "#d97706"}


class Fig:
    def __init__(self, w=1600, h=900, title="", subtitle=""):
        self.w, self.h = w, h
        self.parts = []
        self.title, self.subtitle = title, subtitle

    def add(self, s):
        self.parts.append(s)

    def text(self, x, y, s, size=18, color="#0f172a", weight=400, anchor="start", italic=False):
        st = ' font-style="italic"' if italic else ""
        self.add(f'<text x="{x}" y="{y}" font-size="{size}" fill="{color}" font-weight="{weight}" '
                 f'text-anchor="{anchor}"{st}>{escape(s.replace("   ", "\u00a0\u00a0\u00a0"))}</text>')

    def lines(self, x, y, rows, size=16, color="#475569", weight=400, anchor="middle", gap=None):
        gap = gap or int(size * 1.45)
        for i, r in enumerate(rows):
            self.text(x, y + i * gap, r, size, color, weight, anchor)

    def rect(self, x, y, w, h, style="neutral", rx=14, sw=2, dash=None, fill=None):
        f, s, _, _ = STYLE[style]
        d = f' stroke-dasharray="{dash}"' if dash else ""
        self.add(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill or f}" '
                 f'stroke="{s}" stroke-width="{sw}"{d}/>')

    def box(self, x, y, w, h, title, sub=None, style="neutral", tsize=19, ssize=14, rx=14, dash=None, sw=2):
        self.rect(x, y, w, h, style, rx, sw, dash)
        _, _, tc, sc = STYLE[style]
        subs = sub if isinstance(sub, list) else ([sub] if sub else [])
        total = tsize + len(subs) * (ssize * 1.45)
        top = y + (h - total) / 2 + tsize * 0.85
        self.text(x + w / 2, top, title, tsize, tc, 700, "middle")
        for i, s in enumerate(subs):
            self.text(x + w / 2, top + (i + 1) * ssize * 1.45 + 2, s, ssize, sc, 400, "middle")

    def panel(self, x, y, w, h, label, style="node", size=21):
        self.rect(x, y, w, h, style, rx=22, sw=2)
        _, _, tc, _ = STYLE[style]
        self.text(x + 24, y + 36, label, size, tc, 700)

    def arrow(self, d, color="gray", width=2.5, dash=None, head=True, both=False):
        c = LINE.get(color, color)
        da = f' stroke-dasharray="{dash}"' if dash else ""
        mk = f' marker-end="url(#m-{color})"' if head else ""
        if both:
            mk += f' marker-start="url(#s-{color})"'
        self.add(f'<path d="{d}" stroke="{c}" stroke-width="{width}" fill="none"{da}{mk}/>')

    def badge(self, x, y, n, color="#dc2626", r=16):
        self.add(f'<circle cx="{x}" cy="{y}" r="{r}" fill="{color}"/>')
        self.text(x, y + 6, str(n), 17, "#ffffff", 700, "middle")

    def pill(self, x, y, w, h, title, sub=None, style="dark"):
        self.box(x, y, w, h, title, sub, style, tsize=24, ssize=14, rx=h / 2)

    def cross(self, x, y, s=14):
        self.add(f'<path d="M{x-s},{y-s}L{x+s},{y+s}M{x+s},{y-s}L{x-s},{y+s}" stroke="#dc2626" stroke-width="5"/>')

    def legend(self, items, x=40, y=None):
        y = y or self.h - 26
        cx = x
        for color, label, dash in items:
            c = LINE.get(color, color)
            d = f' stroke-dasharray="{dash}"' if dash else ""
            self.add(f'<line x1="{cx}" y1="{y-5}" x2="{cx+46}" y2="{y-5}" stroke="{c}" stroke-width="3"{d}/>')
            self.text(cx + 56, y, label, 15, "#334155")
            cx += 56 + len(label) * 15 + 40

    def svg(self):
        defs = []
        for name, c in LINE.items():
            defs.append(f'<marker id="m-{name}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
                        f'orient="auto-start-reverse"><path d="M0,0L10,5L0,10z" fill="{c}"/></marker>')
            defs.append(f'<marker id="s-{name}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
                        f'orient="auto-start-reverse"><path d="M0,0L10,5L0,10z" fill="{c}"/></marker>')
        head = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {self.w} {self.h}" width="{self.w}" '
                f'height="{self.h}" font-family="{FONT}">',
                "<defs>" + "".join(defs) + "</defs>",
                f'<rect width="{self.w}" height="{self.h}" fill="#ffffff"/>']
        if self.title:
            head.append(f'<text x="40" y="58" font-size="32" font-weight="700" fill="#0f172a">{escape(self.title)}</text>')
        if self.subtitle:
            head.append(f'<text x="40" y="92" font-size="18" fill="#475569">{escape(self.subtitle)}</text>')
        return "\n".join(head + self.parts + ["</svg>"])
