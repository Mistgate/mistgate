"""Where does the p99 tail of the uncached subscription path come from?  Same setup as the main benchmark (1 vCPU / 1 GB slice, 200 users, 50 clients, X-Forwarded-For per link)
on the limit-off / cache-off variant, with loadgen reporting the seconds in which requests took longer than 500 ms. Variants: default GOMAXPROCS (the cgroup limit, 1), GOMAXPROCS=4."""
import sys, json, time
import os; sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bench
from bench import sh, ROOT, BIN

class P(bench.Mistgate):
    envs = ""
    def start(self):
        if self.setup_s is None:
            self._setup()
        sh("systemd-run --unit=bench-mg --slice=%s --quiet %s %s serve --data-dir %s --listen 127.0.0.1:18080 --trusted-proxy 127.0.0.1 --agent-listen %s:18081 --agent-addr %s:18081"
           % (bench.SLICE, self.envs, self.bin, self.D, self.GW, self.GW))

out = {}
for label, envs in (("default (GOMAXPROCS from cgroup)", ""), ("GOMAXPROCS=4", "-E GOMAXPROCS=4")):
    p = P("mistgate-nolimit-nocache", "mistgate-nolimit-nocache")
    p.envs = envs
    p.prepare_once(); p.wipe()
    sh("systemctl stop bench-panel.slice; systemctl start bench-panel.slice", check=False)
    p.start()
    t0 = time.time()
    while not p.ready() and time.time() - t0 < 30:
        time.sleep(0.05)
    p.bootstrap()
    urls = p.create_users(200)
    open(ROOT + "/out/urls.txt", "w").write("\n".join(urls) + "\n")
    time.sleep(5)
    r = json.loads(sh("%s/loadgen -urls %s/out/urls.txt -c 50 -d 60s -xff -ua '%s'" % (BIN, ROOT, bench.UA), timeout=200))
    out[label] = {k: r[k] for k in ("ok_rps", "lat_ms_p50", "lat_ms_p95", "lat_ms_p99", "lat_ms_max", "slow_requests_per_second_over_500ms")}
    print(label, json.dumps(out[label]), flush=True)
    p.stop(); p.wipe()
json.dump(out, open(ROOT + "/out/tail_diag.json", "w"), indent=1)
