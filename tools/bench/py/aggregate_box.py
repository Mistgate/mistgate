#!/usr/bin/env python3
"""Aggregate onebox.py results: median (min-max) per panel and metric. usage: aggregate_box.py DIR [DIR...]"""
import sys, os, json, glob, statistics, csv


def g(d, *path):
    for p in path:
        if d is None:
            return None
        d = d.get(p) if isinstance(d, dict) else None
    return d


def anon_kernel(r):
    a = g(r, "A_idle", "anon_mean_mib")
    k = g(r, "A_idle", "kernel_mean_mib") or 0
    return None if a is None else a + k


M = [
    ("A: private memory, anon+kernel (MiB)", anon_kernel, "%.0f"),
    ("A: anon only (MiB)", lambda r: g(r, "A_idle", "anon_mean_mib"), "%.0f"),
    ("A: memory.current (MiB, with page cache)", lambda r: g(r, "A_idle", "mem_mean_mib"), "%.0f"),
    ("A: CPU of the box (% of 1 vCPU)", lambda r: g(r, "A_idle", "cpu_pct_of_1vcpu"), "%.2f"),
    ("A: processes", lambda r: g(r, "A_idle", "procs_end"), "%.0f"),
    ("A: containers", lambda r: g(r, "A_idle", "containers_end"), "%.0f"),
    ("Setup of 1000 users (s)", lambda r: r.get("setup_s"), "%.0f"),
]
for s in ("1", "16", "64"):
    M += [
        ("B%s: throughput (Gbit/s)" % s, (lambda s: lambda r: g(r, "B", s, "gbit_s"))(s), "%.2f"),
        ("B%s: box CPU (%% of 1 vCPU)" % s, (lambda s: lambda r: g(r, "B", s, "cpu_pct_box"))(s), "%.0f"),
        ("B%s: Mbit/s per CPU-second" % s, (lambda s: lambda r: g(r, "B", s, "mbit_per_cpu_sec"))(s), "%.0f"),
        ("B%s: anon+kernel during run (MiB)" % s, (lambda s: lambda r: (g(r, "B", s, "box", "anon_mean_mib") or 0) + (g(r, "B", s, "box", "kernel_mean_mib") or 0))(s), "%.0f"),
        ("B%s: small request p95 through the tunnel (ms)" % s, (lambda s: lambda r: g(r, "B", s, "probe_ms_p95"))(s), "%.1f"),
        ("B%s: client CPU (%% of 1 vCPU)" % s, (lambda s: lambda r: g(r, "B", s, "client_cpu_pct"))(s), "%.0f"),
    ]
M += [
    ("C: clients connected", lambda r: g(r, "C", "connected"), "%.0f"),
    ("C: handshake failures", lambda r: g(r, "C", "handshake_fail"), "%.0f"),
    ("C: handshake p50 (ms)", lambda r: g(r, "C", "handshake_ms_p50"), "%.0f"),
    ("C: handshake p95 (ms)", lambda r: g(r, "C", "handshake_ms_p95"), "%.0f"),
    ("C: request failures", lambda r: g(r, "C", "requests_fail"), "%.0f"),
    ("C: request p95 (ms)", lambda r: g(r, "C", "request_ms_p95"), "%.1f"),
    ("C: box CPU (% of 1 vCPU)", lambda r: g(r, "C", "box", "cpu_pct_of_1vcpu"), "%.0f"),
    ("C: anon+kernel (MiB)", lambda r: (g(r, "C", "box", "anon_mean_mib") or 0) + (g(r, "C", "box", "kernel_mean_mib") or 0), "%.0f"),
    ("D: tunnel (Gbit/s, 64 streams)", lambda r: g(r, "D", "tp", "gbit_s"), "%.2f"),
    ("D: tunnel drop vs B64 (%)", lambda r: None if not g(r, "B", "64", "gbit_s") or g(r, "D", "tp", "gbit_s") is None else (1 - g(r, "D", "tp", "gbit_s") / g(r, "B", "64", "gbit_s")) * 100, "%.0f"),
    ("D: tunnel streams dropped", lambda r: g(r, "D", "tp", "streams_dropped"), "%.0f"),
    ("D: tunnel reconnects", lambda r: g(r, "D", "tp", "reconnects"), "%.0f"),
    ("D: small request p95 (ms)", lambda r: g(r, "D", "tp", "probe_ms_p95"), "%.1f"),
    ("D: subscription storm (requests/s, all responses)", lambda r: g(r, "D", "storm", "rps"), "%.0f"),
    ("D: subscription storm 200 responses/s", lambda r: g(r, "D", "storm", "ok_rps"), "%.0f"),
    ("D: subscription p95 (ms, 200 only)", lambda r: g(r, "D", "storm", "lat_ms_p95"), "%.0f"),
    ("D: subscription non-200 (%)", lambda r: None if g(r, "D", "storm", "error_rate") is None else g(r, "D", "storm", "error_rate") * 100, "%.1f"),
    ("D: admin add-user p95 (ms)", lambda r: g(r, "D", "admin_add_ms", "p95"), "%.0f"),
    ("D: admin remove-user p95 (ms)", lambda r: g(r, "D", "admin_del_ms", "p95"), "%.0f"),
    ("D: admin list call p95 (ms)", lambda r: g(r, "D", "admin_call_ms", "p95"), "%.0f"),
    ("D: admin API errors", lambda r: g(r, "D", "admin_error_count"), "%.0f"),
    ("D: box CPU (% of 1 vCPU)", lambda r: g(r, "D", "tp", "box", "cpu_pct_of_1vcpu"), "%.0f"),
    ("D: anon+kernel (MiB)", lambda r: (g(r, "D", "tp", "box", "anon_mean_mib") or 0) + (g(r, "D", "tp", "box", "kernel_mean_mib") or 0), "%.0f"),
    ("E: admin HTTP answers again after (s)", lambda r: g(r, "E", "admin_http_back_s"), "%.1f"),
    ("E: admin API answers again after (s)", lambda r: g(r, "E", "admin_api_back_s"), "%.1f"),
    ("E: seconds with zero tunnel traffic (of 60)", lambda r: g(r, "E", "zero_seconds"), "%.0f"),
    ("E: streams dropped", lambda r: g(r, "E", "streams_dropped"), "%.0f"),
    ("E: tunnel reconnects", lambda r: g(r, "E", "reconnects"), "%.0f"),
    ("E: throughput over the run (Gbit/s)", lambda r: g(r, "E", "tp", "gbit_s"), "%.2f"),
]


def main():
    runs = {}
    for d in sys.argv[1:]:
        for f in sorted(glob.glob(os.path.join(d, "*_r*.json"))):
            r = json.load(open(f))
            runs.setdefault(r["panel"], []).append(r)
    panels = list(runs)
    print("| Metric | " + " | ".join("%s (n=%d)" % (p, len([r for r in runs[p] if r.get("ok")])) for p in panels) + " |")
    print("|---|" + "---|" * len(panels))
    rows = []
    for label, fn, fmt in M:
        cells = []
        for p in panels:
            vals = []
            for r in runs[p]:
                if not r.get("ok") and not ("A_idle" in r or "B" in r):
                    continue
                try:
                    v = fn(r)
                except Exception:
                    v = None
                if v is not None:
                    vals.append(v)
                    rows.append([p, r["rep"], label, v])
            cells.append("n/a" if not vals else (fmt + " (" + fmt + "-" + fmt + ")") % (statistics.median(vals), min(vals), max(vals)))
        print("| %s | %s |" % (label, " | ".join(cells)))
    for p in panels:
        for r in runs[p]:
            if not r.get("ok"):
                print("\nrun %s_r%d: %s" % (p, r["rep"], r.get("error")))
    with open(os.path.join(sys.argv[1], "per_rep.csv"), "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["panel", "rep", "metric", "value"])
        w.writerows(rows)


if __name__ == "__main__":
    main()
