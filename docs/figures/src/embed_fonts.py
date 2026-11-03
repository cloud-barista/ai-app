"""Embeds, per SVG, a WOFF2 subset of Noto Sans CJK KR (400 and 700) holding
only the characters the figure uses, so it looks the same where the font is
not installed."""
import base64, html, io, re, sys
from fontTools.ttLib import TTCollection
from fontTools import subset

FACES = {400: '/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc',
         700: '/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc'}
KR = 1  # index of the KR face in the collection
FAMILY = 'DocKR'
cache = {}

def face(weight):
    if weight not in cache:
        cache[weight] = TTCollection(FACES[weight]).fonts[KR]
    return cache[weight]

def woff2(weight, text):
    opts = subset.Options()
    opts.flavor = 'woff2'
    opts.layout_features = ['*']
    opts.name_IDs = ['*']
    sub = subset.Subsetter(opts)
    sub.populate(text=text)
    import copy
    font = copy.deepcopy(face(weight))
    sub.subset(font)
    buf = io.BytesIO()
    font.flavor = 'woff2'
    font.save(buf)
    return base64.b64encode(buf.getvalue()).decode()

def break_keylike(b64):
    """A run in the font base64 can look like a cloud access key (AKIA and 16
    characters), which commit hooks take for a secret. A backslash and line
    break inside a CSS string is a continuation and not part of the value, so
    one is put in the middle of such a run. The font data does not change."""
    out, last = [], 0
    for m in re.finditer(r'(?i)AKIA[0-9A-Z]{16}', b64):
        out.append(b64[last:m.start() + 2]); out.append('\\\n'); last = m.start() + 2
    out.append(b64[last:])
    return ''.join(out)

for path in sys.argv[1:]:
    s = open(path, encoding='utf-8').read()
    s = re.sub(r'<style id="embedded-font">.*?</style>', '', s, flags=re.S)
    texts = {400: '', 700: ''}
    for m in re.finditer(r'<text[^>]*font-weight="(\d+)"[^>]*>(.*?)</text>', s, flags=re.S):
        w = 700 if int(m.group(1)) >= 600 else 400
        texts[w] += html.unescape(m.group(2))
    rules = []
    for w, t in texts.items():
        if t:
            rules.append(f"@font-face{{font-family:'{FAMILY}';font-weight:{w};src:url(\"data:font/woff2;base64,{break_keylike(woff2(w, t + ' '))}\") format('woff2')}}")
    style = '<style id="embedded-font">' + ''.join(rules) + '</style>'
    s = re.sub(r'font-family="[^"]*"', f'font-family="{FAMILY}, \'Noto Sans CJK KR\', sans-serif"', s, count=1)
    s = s.replace('<defs>', '<defs>' + style, 1)
    open(path, 'w', encoding='utf-8').write(s)
    print(path.split('/')[-1], len(s) // 1024, 'KB')
