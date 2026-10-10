#!/usr/bin/env python3
"""Profiling pass for Mistgate only (never feeds the published numbers).
Builds with net/http/pprof (overlay, repository untouched): panel mistgate-prof (pprof 127.0.0.1:6060), node mistgate-node-prof (pprof 0.0.0.0:6061 inside its container on the isolated network),
GODEBUG=gctrace=1 for both. Phases:
  idle      panel + node idle, 200 users
  storm     subscription hammer as in the main benchmark (default build; one client address, X-Forwarded-For per link, 200 links) = the 429 storm
  nocache   same hammer on the limit-off / cache-off variant (the p99 tail)
  box       one server (2 vCPU/2 GB slice): B64 (30 s), C (400 clients), D (storm + churn + 64 streams)
usage: profile_run.py --phases idle,storm,nocache,box --out $BENCH_ROOT/out/profiles"""
import os, sys, time, json, re, argparse, threading, subprocess, urllib.request
import os; sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bench, onebox
from bench import sh, log, BIN, ROOT

PANEL_PPROF = "127.0.0.1:6060"
NODE_PPROF = "11.11.0.10:6061"


def grab(addr, name, outdir, what=("heap", "allocs", "goroutine", "mutex", "block"), cpu_s=0):
    os.makedirs(outdir, exist_ok=True)
    ths = []
    if cpu_s:
        def cpu():
            try:
                d = urllib.request.urlopen("http://%s/debug/pprof/profile?seconds=%d" % (addr, cpu_s), timeout=cpu_s + 30).read()
                open("%s/%s_cpu.pprof" % (outdir, name), "wb").write(d)
            except Exception as e:
                open("%s/%s_cpu.err" % (outdir, name), "w").write(repr(e))
        t = threading.Thread(target=cpu)
        t.start()
        ths.append(t)
    return ths


def grab_after(addr, name, outdir, what=("heap", "allocs", "goroutine", "mutex", "block")):
    for w in what:
        try:
            url = "http://%s/debug/pprof/%s%s" % (addr, w, "?gc=1" if w == "heap" else "")
            open("%s/%s_%s.pprof" % (outdir, name, w), "wb").write(urllib.request.urlopen(url, timeout=60).read())
        except Exception as e:
            open("%s/%s_%s.err" % (outdir, name, w), "w").write(repr(e))


def gctrace_panel(outdir, name):
    sh("journalctl -u bench-mg-prof -u bench-mg-box --no-pager -o cat | grep '^gc ' > %s/%s_panel_gctrace.txt" % (outdir, name), check=False)


def gctrace_node(outdir, name):
    sh("docker logs bench_mgnode_prof 2>&1 | grep -E '^gc [0-9]' > %s/%s_node_gctrace.txt" % (outdir, name), check=False)
    sh("docker logs bench_mgnode_box 2>&1 | grep -E '^gc [0-9]' >> %s/%s_node_gctrace.txt" % (outdir, name), check=False)


class ProfPanel(bench.Mistgate):
    """main-benchmark style Mistgate (panel native on loopback, node transient) using the pprof builds"""

    def __init__(self, name, binary):
        super().__init__(name, binary)

    def start(self):
        if self.setup_s is None:
            self._setup()
        sh("systemd-run --unit=bench-mg-prof --slice=%s --quiet -E GODEBUG=gctrace=1 -E BENCH_PPROF_ADDR=%s %s serve --data-dir %s --listen 127.0.0.1:18080 --trusted-proxy 127.0.0.1 --agent-listen %s:18081 --agent-addr %s:18081"
           % (bench.SLICE, PANEL_PPROF, self.bin, self.D, self.GW, self.GW))
        return None

    def stop(self):
        sh("systemctl stop bench-mg-prof", check=False)

    def wipe(self):
        sh("systemctl stop bench-mg-prof 2>/dev/null; systemctl reset-failed bench-mg-prof 2>/dev/null; docker rm -f bench_mgnode_prof 2>/dev/null; rm -rf %s %s; mkdir -p %s" % (self.D, self.NODE_STATE, self.NODE_STATE), check=False)
        self.setup_s = None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--phases", default="idle,storm,nocache,box")
    ap.add_argument("--out", default=ROOT + "/out/profiles")
    ap.add_argument("--users", type=int, default=200)
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)
    ph = args.phases.split(",")
    onebox.prepare_infra()
    if "idle" in ph or "storm" in ph or "nocache" in ph:
        for name, binary in (("storm", "mistgate-prof"), ("nocache", "mistgate-nolimit-nocache-prof")):
            if name not in ph and not (name == "storm" and "idle" in ph):
                continue
            od = "%s/%s" % (args.out, name)
            os.makedirs(od, exist_ok=True)
            p = ProfPanel("mistgate-" + name, binary)
            p.prepare_once()
            p.wipe()
            sh("systemctl stop bench-panel.slice; systemctl start bench-panel.slice", check=False)
            bench.SL = "/sys/fs/cgroup/bench.slice/bench-panel.slice"
            p.start()
            t0 = time.time()
            while not p.ready() and time.time() - t0 < 30:
                time.sleep(0.05)
            # the node of the main benchmark must connect once; reuse its bootstrap (node container name differs: it is created by Mistgate.bootstrap)
            p.bootstrap()
            urls = p.create_users(args.users)
            open(ROOT + "/out/urls.txt", "w").write("\n".join(urls) + "\n")
            if name == "storm" and "idle" in ph:
                time.sleep(60)
                ths = grab(PANEL_PPROF, "panel_idle", od + "/idle", cpu_s=30)
                for t in ths:
                    t.join()
                grab_after(PANEL_PPROF, "panel_idle", od + "/idle")
            if name in ph:
                ths = grab(PANEL_PPROF, "panel_" + name, od, cpu_s=30)
                time.sleep(10)
                out = sh("%s/loadgen -urls %s/out/urls.txt -c 50 -d 60s -xff -ua '%s'" % (BIN, ROOT, bench.UA), timeout=200)
                open("%s/loadgen_%s.json" % (od, name), "w").write(out)
                for t in ths:
                    t.join()
                grab_after(PANEL_PPROF, "panel_" + name, od)
                gctrace_panel(od, name)
            p.stop()
            p.wipe()
    if "box" in ph:
        od = args.out + "/box"
        os.makedirs(od, exist_ok=True)

        class PBox(onebox.MistBox):
            NODE_IMAGE = "bench/mgnode-prof:0.1.32"
            NODE_ENV = "-e GODEBUG=gctrace=1 -e BENCH_PPROF_ADDR=0.0.0.0:6061"

            def start_panel(self):
                sh("systemd-run --unit=bench-mg-box --slice=%s --quiet -E GODEBUG=gctrace=1 -E BENCH_PPROF_ADDR=127.0.0.1:6060 %s/mistgate-prof serve --data-dir %s --listen %s:%d --agent-listen %s:18081 --agent-addr %s:18081"
                   % (onebox.BOXSLICE, BIN, self.D, self.PANEL[0], self.PANEL[1], onebox.GW, onebox.GW))

        box = PBox()
        box.wipe()
        onebox.box_limits(2, "2G")
        bench.SL = onebox.BOXCG
        sm = bench.Sampler()
        sm.start()
        onebox.start_sink()
        box.setup(1000)
        time.sleep(30)
        # B64
        ths = grab(PANEL_PPROF, "panel_B64", od, cpu_s=25) + grab(NODE_PPROF, "node_B64", od, cpu_s=25)
        time.sleep(3)
        r = onebox.phase_tp(box, sm, 64, 30)
        for t in ths:
            t.join()
        json.dump({k: v for k, v in r.items() if k != "mbit_s_per_second"}, open(od + "/B64.json", "w"), indent=1, default=str)
        grab_after(PANEL_PPROF, "panel_B64", od)
        grab_after(NODE_PPROF, "node_B64", od)
        # C
        open("/tmp/auths.txt", "w").write("\n".join(box.auths) + "\n")
        ths = grab(PANEL_PPROF, "panel_C", od, cpu_s=25) + grab(NODE_PPROF, "node_C", od, cpu_s=25)
        c = onebox.Run("many -server %s -auths /tmp/auths.txt -n 400 -d 60s -ramp 20s -every 2s -sink %s" % (box.server, onebox.SINK_ADDR), 60, sm).go()
        for t in ths:
            t.join()
        json.dump(c, open(od + "/C.json", "w"), indent=1, default=str)
        grab_after(PANEL_PPROF, "panel_C", od)
        grab_after(NODE_PPROF, "node_C", od)
        # D
        class A:  # args stand-in
            bad_dur = 120
            storm_rate = 300
        ths = grab(PANEL_PPROF, "panel_D", od, cpu_s=30) + grab(NODE_PPROF, "node_D", od, cpu_s=30)
        d = onebox.phase_D(box, sm, A, {})
        for t in ths:
            t.join()
        json.dump(d, open(od + "/D.json", "w"), indent=1, default=str)
        grab_after(PANEL_PPROF, "panel_D", od)
        grab_after(NODE_PPROF, "node_D", od)
        gctrace_panel(od, "box")
        gctrace_node(od, "box")
        sm.stop()
        onebox.stop_sink()
        box.wipe()
    log("profiling done")


if __name__ == "__main__":
    main()
