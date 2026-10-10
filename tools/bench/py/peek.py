#!/usr/bin/env python3
# Quick look at finished runs: body sizes, status mix, anon vs file memory under load, what ran in the slice.
import csv, glob, json, os
for f in sorted(glob.glob(os.environ.get("BENCH_ROOT", "/root/bench") + "/out/main/*_r?.json")):
    r = json.load(open(f))
    name = os.path.basename(f)[:-5]
    w, lg = r.get("warmup", {}), r.get("load", {})
    print("==", name)
    print("  warmup:", {k: w.get(k) for k in ("mean_body_bytes", "status", "codes", "error_rate", "bytes_min", "bytes_max") if k in w})
    print("  load keys:", sorted(lg.keys())[:30])
    print("  load codes:", lg.get("codes") or lg.get("status"))
    rows = list(csv.DictReader(open(f[:-5] + "_samples.csv")))
    mib = lambda v: round(int(v) / 1048576, 1) if v not in (None, "", "None") else None
    for key in ("mem", "anon", "file", "kernel"):
        vals = [mib(x[key]) for x in rows if mib(x.get(key)) is not None]
        print("  %-6s max %s" % (key, max(vals) if vals else None))
    print("  sanity:", str(r.get("sanity") or r.get("sub_sample") or "")[:300])
    print("  setup:", str(r.get("setup") or r.get("panel_info") or "")[:300])
