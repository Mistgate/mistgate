"""Merge: phase A (idle) from the first one-server pass (box1_first) with phases B-E from the corrected rerun (box2) for Mistgate and 3x-ui.
Writes results/onebox/<panel>_r<N>.json. The first pass B/C/D/E are kept untouched in results/box1_first (superseded: probe read to EOF, conntrack table filled by an unpaced storm)."""
import json, glob, os, sys
base = sys.argv[1]
out = os.path.join(base, "onebox"); os.makedirs(out, exist_ok=True)
for f in sorted(glob.glob(os.path.join(base, "box2", "*_r*.json"))):
    r2 = json.load(open(f)); name = os.path.basename(f)
    r1p = os.path.join(base, "box1_first", name)
    r = dict(r2)
    if os.path.exists(r1p):
        r1 = json.load(open(r1p))
        for k in ("A_idle", "setup_s", "smoke"):
            if k in r1: r["first_pass_" + k if k == "smoke" else k] = r1[k]
        r["merged_from"] = "A from box1_first, B-E from box2"
    json.dump(r, open(os.path.join(out, name), "w"), indent=1)
for f in sorted(glob.glob(os.path.join(base, "box3", "*_r*.json"))):
    open(os.path.join(out, os.path.basename(f)), "w").write(open(f).read())
print(sorted(os.listdir(out)))
