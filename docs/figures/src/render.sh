#!/usr/bin/env bash
# Draws every figure, embeds the font subset and renders a PNG next to each SVG.
# Needs python3 with fontTools, the Noto Sans CJK fonts and google-chrome.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=$(dirname "$here")
python3 "$here/figs.py" "$out"
python3 "$here/embed_fonts.py" "$out"/*.svg
for svg in "$out"/*.svg; do
	read -r w h < <(sed -n 's/.*viewBox="0 0 \([0-9]*\) \([0-9]*\)".*/\1 \2/p' "$svg" | head -1)
	timeout 60 google-chrome --headless=new --disable-gpu --no-sandbox --hide-scrollbars \
		--force-device-scale-factor=1 --window-size="$w,$h" \
		--screenshot="${svg%.svg}.png" "file://$svg" >/dev/null 2>&1
done
ls -la "$out"
