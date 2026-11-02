"""Animated WebP of one filmed loop (loops forever) and the poster as a still WebP.
python encode.py <framesDir> <out.webp> <poster.png> <poster.webp> [quality] [fps]"""
import json
import sys
from pathlib import Path

from PIL import Image

frames, out, poster_png, poster_out = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3]), Path(sys.argv[4])
quality = int(sys.argv[5]) if len(sys.argv) > 5 else 80
film = json.loads((frames / "film.json").read_text())
src_fps = film["fps"]
fps = int(sys.argv[6]) if len(sys.argv) > 6 else src_fps
n = film["loopFrames"]
# Resample to the target rate by time, so the loop keeps its length
count = round(n * fps / src_fps)
picks = [min(n - 1, round(i * src_fps / fps)) for i in range(count)]
imgs = [Image.open(frames / f"f{i:04d}.png").convert("RGB") for i in picks]
imgs[0].save(out, save_all=True, append_images=imgs[1:], duration=round(1000 / fps), loop=0, quality=quality, method=6, minimize_size=True)
Image.open(poster_png).convert("RGB").save(poster_out, quality=90, method=6)
print(out, f"{out.stat().st_size / 1e6:.2f} MB", f"{count} frames at {fps} fps, {count / fps:.2f} s;", poster_out, f"{poster_out.stat().st_size / 1e3:.0f} kB")
