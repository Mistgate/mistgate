#!/usr/bin/env python3
"""Aggregate bench.py results: median and spread (min-max) per panel and metric, over the repetitions.
usage: aggregate.py DIR [DIR...]   (each DIR holds <panel>_r<N>.json) -> prints markdown tables, writes summary.csv and per_rep.csv next to the first DIR"""
import sys, os, json, glob, statistics, csv

MB = 1e6
MIB = 1


def g(d, *path):
    for p in path:
        if d is None:
            return None
        d = d.get(p) if isinstance(d, dict) else None
    return d


def disk_mb(r):
    return (r.get("images_bytes") or 0) / MB


METRICS = [
    # key, label, extractor, format
    ("disk_images", "Disk: images / binary (MB)", lambda r: disk_mb(r), "%.0f"),
    ("disk_data0", "Disk: data dir, 0 users (MB)", lambda r: (g(r, "disk_idle0", "data_bytes") or 0) / MB, "%.1f"),
    ("disk_data200", "Disk: data dir, 200 users (MB)", lambda r: (g(r, "disk_idle200", "data_bytes") or 0) / MB, "%.1f"),
    ("idle0_mem", "Idle 0 users: memory.current (MiB)", lambda r: g(r, "idle0", "mem_mean_last30_mib"), "%.0f"),
    ("idle0_anon", "Idle 0 users: anon (MiB)", lambda r: g(r, "idle0", "anon_mean_mib"), "%.0f"),
    ("idle0_ws", "Idle 0 users: working set (MiB)", lambda r: g(r, "idle0", "workset_mean_mib"), "%.0f"),
    ("idle0_cpu", "Idle 0 users: CPU (% of 1 vCPU, 5 min mean)", lambda r: g(r, "idle0", "cpu_pct_whole_window"), "%.2f"),
    ("idle200_mem", "Idle 200 users: memory.current (MiB)", lambda r: g(r, "idle200", "mem_mean_last30_mib"), "%.0f"),
    ("idle200_anon", "Idle 200 users: anon (MiB)", lambda r: g(r, "idle200", "anon_mean_mib"), "%.0f"),
    ("idle200_ws", "Idle 200 users: working set (MiB)", lambda r: g(r, "idle200", "workset_mean_mib"), "%.0f"),
    ("idle200_cpu", "Idle 200 users: CPU (% of 1 vCPU, 5 min mean)", lambda r: g(r, "idle200", "cpu_pct_whole_window"), "%.2f"),
    ("procs", "Processes (idle, 200 users)", lambda r: g(r, "idle200", "procs_end"), "%.0f"),
    ("containers", "Containers (idle, 200 users)", lambda r: g(r, "idle200", "containers_end"), "%.0f"),
    ("create_s", "Creating 200 users via API (s)", lambda r: r.get("create_users_s"), "%.1f"),
    ("rps", "Load: requests/s (all responses)", lambda r: g(r, "load", "rps"), "%.0f"),
    ("ok_rps", "Load: successful (200) requests/s", lambda r: g(r, "load", "ok_rps"), "%.0f"),
    ("p50", "Load: p50 latency (ms, 200 only)", lambda r: g(r, "load", "lat_ms_p50"), "%.1f"),
    ("p95", "Load: p95 latency (ms, 200 only)", lambda r: g(r, "load", "lat_ms_p95"), "%.1f"),
    ("p99", "Load: p99 latency (ms, 200 only)", lambda r: g(r, "load", "lat_ms_p99"), "%.1f"),
    ("err", "Load: error rate (non-200 + transport, %)", lambda r: (g(r, "load", "error_rate") or 0) * 100 if g(r, "load", "error_rate") is not None else None, "%.2f"),
    ("load_cpu", "Load: mean CPU (% of 1 vCPU)", lambda r: g(r, "load_res", "cpu_pct_of_1vcpu"), "%.0f"),
    ("load_thr", "Load: CFS throttled time (ms)", lambda r: g(r, "load_res", "throttled_ms"), "%.0f"),
    ("load_peak", "Load: peak memory.current (MiB, 1 Hz samples)", lambda r: g(r, "load_res", "mem_max_mib"), "%.0f"),
    ("load_peak_anon", "Load: mean anon during run (MiB)", lambda r: g(r, "load_res", "anon_mean_mib"), "%.0f"),
    ("slice_peak", "Peak memory.current since start (MiB, kernel memory.peak)", lambda r: r.get("slice_memory_peak_mib"), "%.0f"),
    ("after_load", "30 s after load: anon (MiB)", lambda r: g(r, "after_load", "anon_mean_mib"), "%.0f"),
    ("cold_first", "Cold start, empty data (s)", lambda r: r.get("cold_first_s"), "%.2f"),
    ("cold_restart", "Cold start, 200 users on disk (s)", lambda r: r.get("cold_restart_s"), "%.2f"),
]


def load(dirs):
    runs = {}
    for d in dirs:
        for f in sorted(glob.glob(os.path.join(d, "*_r*.json"))):
            r = json.load(open(f))
            runs.setdefault(r["panel"], []).append(r)
    return runs


def main():
    dirs = sys.argv[1:]
    runs = load(dirs)
    panels = list(runs)
    out_summary, out_rep = [], []
    print("| Metric | " + " | ".join("%s (n=%d)" % (p, len([r for r in runs[p] if (r.get("ok") or ("load" in r and "cold_restart_s" in r))])) for p in panels) + " |")
    print("|---|" + "---|" * len(panels))
    for key, label, fn, fmt in METRICS:
        cells = []
        for p in panels:
            vals = []
            for r in runs[p]:
                if not (r.get("ok") or ("load" in r and "cold_restart_s" in r)):
                    continue
                try:
                    v = fn(r)
                except Exception:
                    v = None
                if v is not None:
                    vals.append(v)
                    out_rep.append([p, r["rep"], key, v])
            if not vals:
                cells.append("n/a")
                out_summary.append([p, key, "", "", ""])
                continue
            med, lo, hi = statistics.median(vals), min(vals), max(vals)
            cells.append((fmt + " (" + fmt + "-" + fmt + ")") % (med, lo, hi))
            out_summary.append([p, key, med, lo, hi])
        print("| %s | %s |" % (label, " | ".join(cells)))
    failed = [(p, r["rep"], r.get("error")) for p in panels for r in runs[p] if not r.get("ok")]
    if failed:
        print("\nFailed runs:")
        for f in failed:
            print(" -", f)
    base = dirs[0]
    with open(os.path.join(base, "summary.csv"), "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["panel", "metric", "median", "min", "max"])
        w.writerows(out_summary)
    with open(os.path.join(base, "per_rep.csv"), "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["panel", "rep", "metric", "value"])
        w.writerows(out_rep)


if __name__ == "__main__":
    main()
