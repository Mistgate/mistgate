#!/usr/bin/env python3
"""Run `go tool pprof -top` on every .pprof under DIR and write <file>.top.txt next to it (top 15 by default).
usage: pprof_report.py DIR [nodecount]"""
import sys, os, subprocess, glob
d = sys.argv[1]
n = sys.argv[2] if len(sys.argv) > 2 else "15"
for f in sorted(glob.glob(os.path.join(d, "**", "*.pprof"), recursive=True)):
    base = os.path.basename(f)
    views = [("", [])]
    if "heap" in base:
        views = [("inuse", ["-sample_index=inuse_space"]), ("alloc", ["-sample_index=alloc_space"]), ("allocobj", ["-sample_index=alloc_objects"])]
    elif "allocs" in base:
        views = [("alloc", ["-sample_index=alloc_space"]), ("allocobj", ["-sample_index=alloc_objects"])]
    for tag, extra in views:
        out = f + (".%s" % tag if tag else "") + ".top.txt"
        r = subprocess.run(["go", "tool", "pprof", "-top", "-nodecount=" + n] + extra + [f], capture_output=True, text=True)
        open(out, "w", encoding="utf-8").write(r.stdout + r.stderr)
        print(out)
