"""Contact sheet of a filmed loop: 12 frames around the story's marks, 3 x 4, each labelled with its time.
python sheet.py <framesDir> <out.png> [frame ...]   (explicit frame numbers override the picks)"""
import json
import sys
from pathlib import Path

from PIL import Image, ImageDraw

frames = Path(sys.argv[1])
out = Path(sys.argv[2])
film = json.loads((frames / "film.json").read_text())
fps = film["fps"]
at = {m["mark"]: m["frame"] for m in film["marks"]}

if len(sys.argv) > 3:
    picks = [int(x) for x in sys.argv[3:]]
else:
    picks = [
        0,
        at["add"] + 14,
        at["add"] + 34,
        at["add"] + 64,
        at["install"] + 6,
        at["row"] + 6,
        at["row"] + 30,
        at["online"] + 4,
        at["phone"] + 6,
        at["payoff"] + 20,
        at["exit"] + 8,
        film["loopFrames"] - 1,
    ]

scale = 0.5
tw, th = int(1100 * scale), int(620 * scale)
cols = 3
rows = (len(picks) + cols - 1) // cols
pad = 10
label = 22
sheet = Image.new("RGB", (cols * tw + (cols + 1) * pad, rows * (th + label) + (rows + 1) * pad), (40, 40, 44))
draw = ImageDraw.Draw(sheet)
for i, f in enumerate(picks):
    f = min(f, film["loopFrames"] - 1)
    img = Image.open(frames / f"f{f:04d}.png").convert("RGB").resize((tw, th), Image.LANCZOS)
    x = pad + (i % cols) * (tw + pad)
    y = pad + (i // cols) * (th + label + pad)
    sheet.paste(img, (x, y + label))
    draw.text((x + 2, y + 4), f"#{f}  {f / fps:.2f}s", fill=(230, 230, 235))
sheet.save(out)
print(out, sheet.size)
