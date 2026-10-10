#!/usr/bin/env python3
"""Panel resource benchmark harness (runs inside WSL as root).
usage: bench.py --panels mistgate,3xui,remnawave,pasarguard --reps 3 [--idle 300] [--load 60] [--users 200] [--tag run1]
All data is invented (user1..userN, node1.bench.test, 203.0.113.x); every service listens on 127.0.0.1 or on an isolated docker network."""
import os, sys, time, json, subprocess, threading, re, hmac, hashlib, struct, base64, argparse, secrets, http.client
import urllib.parse

ROOT = os.environ.get("BENCH_ROOT", "/root/bench"); BIN = ROOT + "/bin"; DATA = ROOT + "/data"
SLICE = "bench-panel.slice"; SL = "/sys/fs/cgroup/bench.slice/bench-panel.slice"
UA = "v2rayN/7.5.0"
FW = {"X-Forwarded-For": "127.0.0.1", "X-Forwarded-Proto": "https"}   # what a TLS reverse proxy in front of Remnawave sends


def sh(cmd, check=True, timeout=600):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=timeout)
    if check and r.returncode != 0:
        raise RuntimeError("cmd failed (%d): %s\n%s\n%s" % (r.returncode, cmd, r.stdout[-800:], r.stderr[-800:]))
    return r.stdout


def log(*a):
    print(time.strftime("%H:%M:%S"), *a, flush=True)


# ---------------------------------------------------------------- cgroup sampler
def _kv(path):
    d = {}
    try:
        for line in open(path):
            k, v = line.split()
            d[k] = int(v)
    except (FileNotFoundError, ValueError):
        pass
    return d


class Sampler(threading.Thread):
    """1 Hz sampling of the shared 1 vCPU / 1 GB cgroup: memory (current, anon, file, shmem, kernel), cpu usage, pids, processes, containers."""

    def __init__(self):
        super().__init__(daemon=True)
        self.rows = []
        self.stop_ev = threading.Event()

    def sample(self):
        t = time.time()
        try:
            cur = int(open(SL + "/memory.current").read())
        except (FileNotFoundError, OSError):
            return {"t": t, "mem": None}
        ms = _kv(SL + "/memory.stat")
        cs = _kv(SL + "/cpu.stat")
        procs = 0
        scopes = 0
        for dp, dn, fn in os.walk(SL):
            if "cgroup.procs" in fn:
                try:
                    procs += sum(1 for _ in open(dp + "/cgroup.procs"))
                except OSError:
                    pass
            scopes += sum(1 for d in dn if d.startswith("docker-"))
        try:
            pids = int(open(SL + "/pids.current").read())
        except OSError:
            pids = None
        return {"t": t, "mem": cur, "anon": ms.get("anon"), "file": ms.get("file"), "inactive_file": ms.get("inactive_file"),
                "shmem": ms.get("shmem"), "kernel": ms.get("kernel"), "cpu_us": cs.get("usage_usec"),
                "thr_n": cs.get("nr_throttled"), "thr_us": cs.get("throttled_usec"), "procs": procs, "containers": scopes, "tasks": pids}

    def run(self):
        nxt = time.time()
        while not self.stop_ev.is_set():
            self.rows.append(self.sample())
            nxt += 1.0
            self.stop_ev.wait(max(0, nxt - time.time()))

    def stop(self):
        self.stop_ev.set()
        self.join()

    def window(self, t0, t1):
        return [r for r in self.rows if r.get("mem") is not None and t0 <= r["t"] <= t1]


def win_stats(rows):
    """mean/max memory (MiB) and mean CPU (% of one vCPU) over a sample window."""
    if len(rows) < 2:
        return {}

    def mb(k):
        return [r[k] / 1048576 for r in rows if r.get(k) is not None]

    def avg(k):
        v = mb(k)
        return sum(v) / len(v) if v else None

    cur = mb("mem")
    ws = [(r["mem"] - (r["inactive_file"] or 0)) / 1048576 for r in rows]
    dt = rows[-1]["t"] - rows[0]["t"]
    out = {"n": len(rows), "secs": dt, "mem_mean_mib": sum(cur) / len(cur), "mem_max_mib": max(cur), "mem_end_mib": cur[-1],
           "workset_mean_mib": sum(ws) / len(ws), "anon_mean_mib": avg("anon"), "file_mean_mib": avg("file"),
           "kernel_mean_mib": avg("kernel"), "procs_end": rows[-1]["procs"], "containers_end": rows[-1]["containers"],
           "tasks_end": rows[-1]["tasks"]}
    if rows[0].get("cpu_us") is not None and rows[-1].get("cpu_us") is not None and dt > 0:
        out["cpu_pct_of_1vcpu"] = (rows[-1]["cpu_us"] - rows[0]["cpu_us"]) / 1e6 / dt * 100
        out["throttled_periods"] = rows[-1]["thr_n"] - rows[0]["thr_n"]
        out["throttled_ms"] = (rows[-1]["thr_us"] - rows[0]["thr_us"]) / 1000
    return out


# ---------------------------------------------------------------- http helpers
class Client:
    """keep-alive JSON/form client with cookies, bearer token and extra headers."""

    def __init__(self, host, port, headers=None):
        self.host, self.port, self.h, self.cookies, self.tok = host, port, dict(headers or {}), {}, None
        self.conn = None

    def req(self, method, path, body=None, form=None, headers=None, raw=False):
        hdr = {**self.h, **(headers or {})}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            hdr["Content-Type"] = "application/json"
        if form is not None:
            data = urllib.parse.urlencode(form).encode()
            hdr["Content-Type"] = "application/x-www-form-urlencoded"
        if self.tok:
            hdr["Authorization"] = "Bearer " + self.tok
        if self.cookies:
            hdr["Cookie"] = "; ".join("%s=%s" % kv for kv in self.cookies.items())
        for attempt in (0, 1):
            try:
                if self.conn is None:
                    self.conn = http.client.HTTPConnection(self.host, self.port, timeout=60)
                self.conn.request(method, path, body=data, headers=hdr)
                r = self.conn.getresponse()
                b = r.read()
                break
            except (http.client.HTTPException, ConnectionError, OSError):
                self.conn = None
                if attempt:
                    raise
        if (r.getheader("Connection") or "").lower() == "close":
            self.conn = None
        for c in r.msg.get_all("Set-Cookie") or []:
            k, _, v = c.split(";")[0].partition("=")
            self.cookies[k] = v
        if raw:
            return r.status, b, r
        try:
            return r.status, (json.loads(b) if b else {}), r
        except ValueError:
            return r.status, b.decode("utf-8", "replace"), r


def http_ready(host, port, path="/", headers=None, ok=(200,)):
    try:
        c = http.client.HTTPConnection(host, port, timeout=2)
        c.request("GET", path, headers=headers or {})
        r = c.getresponse()
        r.read()
        c.close()
        return r.status in ok
    except Exception:
        return False


def totp(secret, step):
    s = secret.upper()
    s += "=" * ((8 - len(s) % 8) % 8)
    h = hmac.new(base64.b32decode(s), struct.pack(">Q", step), hashlib.sha1).digest()
    o = h[-1] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7fffffff) % 1000000)


# ---------------------------------------------------------------- panels
class Panel:
    name = "?"
    ready_host = "127.0.0.1"
    ready_port = 0
    ready_path = "/"
    ready_headers = None
    sub_headers = []      # extra headers the load tool must send

    def images(self): return []
    def data_dirs(self): return []
    def binaries(self): return []
    def prepare_once(self): pass
    def ready(self): return http_ready(self.ready_host, self.ready_port, self.ready_path, self.ready_headers)
    def versions(self): return {}


class ComposePanel(Panel):
    P = F = ""

    def start(self):
        return subprocess.Popen("docker compose -p %s -f %s up -d" % (self.P, self.F), shell=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def stop(self):
        sh("docker compose -p %s -f %s down" % (self.P, self.F), check=False)


class XUI(ComposePanel):
    name = "3xui"
    ready_port = 2053
    P, F = "bench3xui", ROOT + "/3xui/compose.yml"

    def images(self): return ["ghcr.io/mhsanaei/3x-ui:v3.9.0"]
    def data_dirs(self): return [DATA + "/3xui"]

    def wipe(self):
        sh("docker compose -p %s -f %s down -v 2>&1 | tail -1; rm -rf %s/3xui; mkdir -p %s/3xui" % (self.P, self.F, DATA, DATA), check=False)

    def bootstrap(self):
        c = Client("127.0.0.1", 2053)
        st, b, _ = c.req("GET", "/csrf-token")
        st, b, _ = c.req("POST", "/login", body={"username": "admin", "password": "admin"}, headers={"X-CSRF-Token": b["obj"]})
        assert st == 200 and b.get("success"), (st, b)
        st, b, _ = c.req("GET", "/csrf-token")
        c.h["X-CSRF-Token"] = b["obj"]
        self.c = c
        kp = c.req("GET", "/panel/api/server/getNewX25519Cert")[1]["obj"]
        inb = {"enable": True, "remark": "bench", "listen": "", "port": 4443, "protocol": "vless", "expiryTime": 0, "total": 0,
               "settings": {"clients": [], "decryption": "none", "fallbacks": []},
               "streamSettings": {"network": "tcp", "security": "reality", "tcpSettings": {"acceptProxyProtocol": False, "header": {"type": "none"}},
                                  "realitySettings": {"show": False, "xver": 0, "target": "www.cloudflare.com:443", "serverNames": ["www.cloudflare.com"],
                                                      "privateKey": kp["privateKey"], "minClientVer": "", "maxClientVer": "", "maxTimediff": 0,
                                                      "shortIds": ["0123456789abcdef"],
                                                      "settings": {"publicKey": kp["publicKey"], "fingerprint": "chrome", "serverName": "", "spiderX": "/"}}},
               "sniffing": {"enabled": True, "destOverride": ["http", "tls"], "metadataOnly": False, "routeOnly": False}}
        st, b, _ = c.req("POST", "/panel/api/inbounds/add", body=inb)
        assert st == 200 and b.get("success"), (st, b)
        self.iid = b["obj"]["id"]

    def create_users(self, n):
        c = self.c
        for i in range(1, n + 1):
            st, b, _ = c.req("POST", "/panel/api/clients/add", body={"client": {"email": "user%d" % i, "enable": True, "totalGB": 0, "expiryTime": 0}, "inboundIds": [self.iid]})
            assert st == 200 and b.get("success"), (i, st, b)
        st, b, _ = c.req("POST", "/panel/api/setting/all", body={})
        sub_path, sub_port = b["obj"]["subPath"], b["obj"]["subPort"]
        st, b, _ = c.req("GET", "/panel/api/clients/list")
        ids = {x["email"]: x["subId"] for x in b["obj"]}
        return ["http://127.0.0.1:%d%s%s" % (sub_port, sub_path, ids["user%d" % i]) for i in range(1, n + 1)]

    def versions(self):
        return {"3x-ui": "v3.9.0 (ghcr.io/mhsanaei/3x-ui:v3.9.0)"}


class Remna(ComposePanel):
    name = "remnawave"
    ready_port = 3000
    ready_path = "/api/auth/status"
    ready_headers = FW
    sub_headers = ["X-Forwarded-For: 127.0.0.1", "X-Forwarded-Proto: https"]
    P, F = "benchremna", ROOT + "/remna/compose.yml"

    def images(self): return ["remnawave/backend:3.4.5", "postgres:18.4", "valkey/valkey:9-alpine"]
    def data_dirs(self): return ["/var/lib/docker/volumes/remnawave-db-data", "/var/lib/docker/volumes/valkey-socket"]

    def wipe(self):
        sh("docker compose -p %s -f %s down -v 2>&1 | tail -1" % (self.P, self.F), check=False)

    def bootstrap(self):
        c = Client("127.0.0.1", 3000, FW)
        pw = "Bn-" + secrets.token_hex(14) + "Aa1"
        st, b, _ = c.req("POST", "/api/auth/register", body={"username": "benchadmin", "password": pw})
        assert st == 201, (st, b)
        c.tok = b["response"]["accessToken"]
        c.h["x-remnawave-client-type"] = "browser"      # the dashboard's own header; API tokens are minted from a dashboard session
        st, b, _ = c.req("POST", "/api/tokens", body={"name": "bench", "expiresInDays": 7})
        assert st == 201, (st, b)
        c.tok = b["response"]["token"]
        c.h.pop("x-remnawave-client-type")
        self.c = c
        st, b, _ = c.req("GET", "/api/internal-squads")
        sq = b["response"]["internalSquads"][0]
        self.squad = sq["uuid"]
        ib = sq["inbounds"][0]
        st, b, _ = c.req("POST", "/api/hosts", body={"inbound": {"configProfileUuid": ib["profileUuid"], "configProfileInboundUuid": ib["uuid"]},
                                                      "remark": "bench-host", "address": "node1.bench.test", "port": 1234})
        assert st == 201, (st, b)

    def create_users(self, n):
        c = self.c
        out = []
        for i in range(1, n + 1):
            st, b, _ = c.req("POST", "/api/users", body={"username": "user%d" % i, "expireAt": "2099-01-01T00:00:00.000Z", "activeInternalSquads": [self.squad]})
            assert st == 201, (i, st, b)
            out.append("http://127.0.0.1:3000/api/sub/" + b["response"]["shortUuid"])
        return out

    def versions(self):
        return {"remnawave backend": "3.4.5", "postgres": "18.4", "valkey": "9-alpine"}


class Pasar(ComposePanel):
    name = "pasarguard"
    ready_port = 8000
    P, F = "benchpasar", ROOT + "/pasar/compose.yml"

    def images(self): return ["pasarguard/panel:v5.4.1"]
    def data_dirs(self): return [DATA + "/pasar"]

    def wipe(self):
        sh("docker compose -p %s -f %s down -v 2>&1 | tail -1; rm -rf %s/pasar; mkdir -p %s/pasar" % (self.P, self.F, DATA, DATA), check=False)

    def bootstrap(self):
        out = sh("docker exec bench_pasarguard pasarguard-cli generate-temp-key")
        key = re.search(r"Temp key: (\S+)", out).group(1)
        c = Client("127.0.0.1", 8000)
        pw = "Bn-" + secrets.token_hex(12) + "Aa1"
        st, b, _ = c.req("POST", "/api/setup/owner", body={"key": key, "username": "benchadmin", "password": pw})
        assert st == 201, (st, b)
        st, b, _ = c.req("POST", "/api/admin/token", form={"username": "benchadmin", "password": pw})
        assert st == 200, (st, b)
        c.tok = b["access_token"]
        self.c = c
        st, b, _ = c.req("POST", "/api/group", body={"name": "everyone", "inbound_tags": ["Shadowsocks TCP"]})
        assert st == 201, (st, b)
        self.gid = b["id"]
        st, b, _ = c.req("POST", "/api/host/", body={"remark": "bench-host", "address": ["node1.bench.test"], "inbound_tag": "Shadowsocks TCP", "port": 1080, "priority": 1})
        assert st == 201, (st, b)

    def create_users(self, n):
        c = self.c
        out = []
        for i in range(1, n + 1):
            st, b, _ = c.req("POST", "/api/user", body={"username": "user%d" % i, "group_ids": [self.gid], "data_limit": 0})
            assert st == 201, (i, st, b)
            out.append("http://127.0.0.1:8000" + b["subscription_url"])
        return out

    def versions(self):
        return {"pasarguard panel": "v5.4.1 (database: SQLite, the installer default)"}


class Mistgate(Panel):
    ready_port = 18080
    D = DATA + "/mg"
    NODE_STATE = DATA + "/mg-node"
    NET, SUBNET, GW = "bench-iso", "10.200.0.0/24", "10.200.0.1"

    def __init__(self, name="mistgate", binary="mistgate"):
        self.name, self.bin = name, BIN + "/" + binary

    def data_dirs(self): return [self.D]
    def binaries(self): return [self.bin]

    def prepare_once(self):
        if "bench/mgnode" not in sh("docker images --format '{{.Repository}}:{{.Tag}}'"):
            sh("rm -rf /tmp/mgn && mkdir /tmp/mgn && cp %s/mistgate-node /tmp/mgn/ && tar -c -C /tmp/mgn mistgate-node | "
               "docker import --change 'ENTRYPOINT [\"/mistgate-node\"]' - bench/mgnode:0.1.32 && rm -rf /tmp/mgn" % BIN)
        if self.NET not in sh("docker network ls --format '{{.Name}}'"):
            sh("docker network create --internal --subnet %s --gateway %s %s" % (self.SUBNET, self.GW, self.NET))

    def wipe(self):
        sh("systemctl stop bench-mg 2>/dev/null; systemctl reset-failed bench-mg 2>/dev/null; docker rm -f bench_mgnode 2>/dev/null; "
           "rm -rf %s %s; mkdir -p %s" % (self.D, self.NODE_STATE, self.NODE_STATE), check=False)
        self.setup_s = None

    def _setup(self):
        t = time.time()
        out = sh("%s setup --data-dir %s --public-url http://localhost:18080" % (self.bin, self.D))
        self.setup_s = time.time() - t
        self.admin = re.search(r"Admin URL:\s+(\S+)", out).group(1).replace("localhost", "127.0.0.1")
        self.setup_token = re.search(r"Setup link:\s+\S+#(\S+)", out).group(1)

    def start(self):
        if self.setup_s is None:
            self._setup()                                  # `mistgate setup` is the documented step before `serve`; timed separately
        sh("systemd-run --unit=bench-mg --slice=%s --quiet %s serve --data-dir %s --listen 127.0.0.1:18080 --trusted-proxy 127.0.0.1 --agent-listen %s:18081 --agent-addr %s:18081"
           % (SLICE, self.bin, self.D, self.GW, self.GW))
        return None

    def stop(self):
        sh("systemctl stop bench-mg", check=False)

    def rpc(self, svc, method, body=None):
        for _ in range(60):                               # the admin listener allows 30 requests/s per client (burst 200): wait out a 429
            st, b, r = self.c.req("POST", self.admin_path + "api/mistgate.admin.v1.%s/%s" % (svc, method), body=body or {})
            if st != 429:
                break
            self.rate_limited = getattr(self, "rate_limited", 0) + 1
            time.sleep(float(r.getheader("Retry-After") or 1))
        return st, b, r

    def _stepup(self):
        step = int(time.time()) // 30
        while step <= self.last_step:
            time.sleep(0.5)
            step = int(time.time()) // 30
        st, b, _ = self.rpc("AuthService", "FinishStepUp", {"totpCode": totp(self.totp, step)})
        assert st == 200, (st, b)
        self.last_step = step

    def bootstrap(self):
        c = Client("127.0.0.1", 18080)
        self.c = c
        self.admin_path = urllib.parse.urlparse(self.admin).path
        st, b, _ = self.rpc("AuthService", "BeginSetup", {"setupToken": self.setup_token, "displayName": "Bench owner", "method": "SETUP_METHOD_PASSWORD",
                                                           "login": "benchowner", "password": "bn-" + secrets.token_hex(10)})
        assert st == 200, (st, b)
        self.totp = b["totpSecret"]
        step = int(time.time()) // 30
        st, b, _ = self.rpc("AuthService", "FinishSetup", {"setupToken": self.setup_token, "ceremonyId": b["ceremonyId"], "totpCode": totp(self.totp, step)})
        assert st == 200, (st, b)
        assert "__Host-sid" in c.cookies
        c.cookies = {"__Host-sid": c.cookies["__Host-sid"]}
        self.last_step = step
        self._stepup()
        st, b, _ = self.rpc("ProfileService", "ListProtocols")
        d = json.loads([p for p in b["protocols"] if p["id"] == "hysteria2"][0]["defaultSettingsJson"])
        d["tls_mode"] = "self_signed"
        st, b, _ = self.rpc("ProfileService", "CreateProfile", {"protocol": "hysteria2", "name": "main", "settingsJson": json.dumps(d)})
        assert st == 200, (st, b)
        pid = b["profile"]["id"]
        st, b, _ = self.rpc("NodeService", "CreateEnrollment", {"name": "bench1", "address": "203.0.113.10", "countryCode": "DE"})
        assert st == 200, (st, b)
        nid = b["node"]["id"]
        cmd = b["installCommand"]
        a = {k: re.search(r"--%s (\S+)" % k, cmd).group(1) for k in ("panel", "sni", "ca-sha256", "token")}
        sh("docker run --rm --network %s -v %s:/state bench/mgnode:0.1.32 enroll --panel %s --sni %s --ca-sha256 %s --token %s --state-dir /state"
           % (self.NET, self.NODE_STATE, a["panel"], a["sni"], a["ca-sha256"], a["token"]))
        sh("docker run -d --name bench_mgnode --network %s --cpus 2 --memory 2g --memory-swap 2g -v %s:/state bench/mgnode:0.1.32 run --state-dir /state"
           % (self.NET, self.NODE_STATE))
        st, b, _ = self.rpc("ProfileService", "CreateInbound", {"profileId": pid, "nodeId": nid})
        assert st == 200, (st, b)
        st, b, _ = self.rpc("GroupService", "CreateGroup", {"name": "everyone", "profileIds": [pid]})
        assert st == 200, (st, b)
        self.gid = b["group"]["id"]
        st, b, _ = self.rpc("UserService", "CreateUser", {"name": "probe", "groupId": self.gid})
        assert st == 200, (st, b)
        probe = urllib.parse.urlparse(b["subscriptionUrl"]).path
        # the node must connect once (state 'active', self-signed pin reported) before a subscription carries a server line
        for i in range(120):
            code, body, _ = Client("127.0.0.1", 18080).req("GET", probe, headers={"User-Agent": UA}, raw=True)
            if code == 200 and body:
                log("mistgate: node connected, subscription has a server line after %ds" % i)
                break
            time.sleep(1)
        else:
            raise RuntimeError("mistgate node never became active: " + sh("docker logs bench_mgnode 2>&1 | tail -20", check=False))
        sh("docker rm -f bench_mgnode")                    # panel-only measurement from here on: the agent is gone, the node record stays
        self.probe_id = None

    def create_users(self, n):
        out = []
        for i in range(1, n + 1):
            st, b, _ = self.rpc("UserService", "CreateUser", {"name": "user%d" % i, "groupId": self.gid})
            assert st == 200, (i, st, b)
            out.append("http://127.0.0.1:18080" + urllib.parse.urlparse(b["subscriptionUrl"]).path)
        return out

    def versions(self):
        return {"mistgate": sh(BIN + "/mistgate version", check=False).strip().replace("\n", " | ")}


PANELS = {"mistgate": lambda: Mistgate("mistgate", "mistgate"),
          "mistgate-nolimit": lambda: Mistgate("mistgate-nolimit", "mistgate-nolimit"),
          "mistgate-nolimit-nocache": lambda: Mistgate("mistgate-nolimit-nocache", "mistgate-nolimit-nocache"),
          "3xui": XUI, "remnawave": Remna, "pasarguard": Pasar}


# ---------------------------------------------------------------- one repetition
CPUMODE = "quota"      # quota: CPUQuota=100% only (the specified setup); pin: the same quota plus the slice pinned to one host CPU (AllowedCPUs=15)
LOADGEN_PREFIX = ""


def reset_slice():
    sh("systemctl stop bench-panel.slice; systemctl start bench-panel.slice", check=False)
    if CPUMODE == "pin":
        sh("systemctl set-property --runtime bench-panel.slice AllowedCPUs=15", check=False)


def wait_ready(p, t0, limit=180):
    while time.time() - t0 < limit:
        if p.ready():
            return time.time() - t0
        time.sleep(0.05)
    raise RuntimeError("%s not ready within %ds" % (p.name, limit))


def bg_snapshot():
    top = sh("top -bn1 -w 160 | head -14", check=False)
    return {"loadavg": open("/proc/loadavg").read().strip(), "psi_cpu": open("/proc/pressure/cpu").read().strip().splitlines()[0],
            "meminfo_available_mib": int(re.search(r"MemAvailable:\s+(\d+)", open("/proc/meminfo").read()).group(1)) // 1024, "top": top}


def du(paths):
    total = 0
    for p in paths:
        if os.path.exists(p):
            total += int(sh("du -sb %s | cut -f1" % p, check=False) or 0)
    return total


def image_bytes(p):
    t = 0
    out = sh("docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}'", check=False)
    for im in p.images():
        for line in out.splitlines():
            ref, size = line.rsplit(" ", 1)
            if ref == im:
                m = re.match(r"([\d.]+)\s*([kMG]?B)", size)
                t += float(m.group(1)) * {"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9}[m.group(2)]
    for b in p.binaries():
        t += os.path.getsize(b)
    return int(t)


def run_rep(p, rep, args, outdir):
    tag = "%s_r%d" % (p.name, rep)
    res = {"panel": p.name, "cpumode": CPUMODE, "rep": rep, "started": time.strftime("%Y-%m-%d %H:%M:%S"), "idle_s": args.idle, "load_s": args.load, "users": args.users, "conc": args.conc}
    log("=== %s" % tag)
    p.wipe()
    time.sleep(3)
    reset_slice()
    time.sleep(2)
    res["background"] = bg_snapshot()
    log("load:", res["background"]["loadavg"], res["background"]["psi_cpu"])
    sm = Sampler()
    sm.start()
    ev = {}
    try:
        # 1. first start (empty data)
        t0 = time.time()
        proc = p.start()
        res["cold_first_s"] = wait_ready(p, t0)
        log("first start ready in %.2fs" % res["cold_first_s"])
        if proc:
            proc.wait()
        res["cold_first_command_returned_s"] = time.time() - t0
        if getattr(p, "setup_s", None):
            res["setup_s"] = p.setup_s
        # 2. bootstrap (admin + base config; not measured)
        tb = time.time()
        p.bootstrap()
        res["bootstrap_s"] = time.time() - tb
        log("bootstrap %.1fs" % res["bootstrap_s"])
        # 3. idle, 0 users
        ev["idle0_start"] = time.time()
        time.sleep(args.idle)
        ev["idle0_end"] = time.time()
        res["idle0"] = win_stats(sm.window(ev["idle0_end"] - 60, ev["idle0_end"]))
        res["idle0"]["mem_mean_last30_mib"] = win_stats(sm.window(ev["idle0_end"] - 30, ev["idle0_end"])).get("mem_mean_mib")
        res["idle0"]["cpu_pct_whole_window"] = win_stats(sm.window(ev["idle0_start"], ev["idle0_end"])).get("cpu_pct_of_1vcpu")
        res["disk_idle0"] = {"data_bytes": du(p.data_dirs())}
        log("idle0:", {k: round(v, 1) for k, v in res["idle0"].items() if isinstance(v, float)})
        # 4. create users through the API
        ev["users_start"] = time.time()
        urls = p.create_users(args.users)
        ev["users_end"] = time.time()
        res["create_users_s"] = ev["users_end"] - ev["users_start"]
        res["create_users_429_waits"] = getattr(p, "rate_limited", 0)
        ws = win_stats(sm.window(ev["users_start"], ev["users_end"]))
        res["users_cpu_pct"], res["users_mem_max_mib"] = ws.get("cpu_pct_of_1vcpu"), ws.get("mem_max_mib")
        log("created %d users in %.1fs" % (len(urls), res["create_users_s"]))
        # 5. idle, with users
        time.sleep(args.idle)
        ev["idle200_end"] = time.time()
        res["idle200"] = win_stats(sm.window(ev["idle200_end"] - 60, ev["idle200_end"]))
        res["idle200"]["mem_mean_last30_mib"] = win_stats(sm.window(ev["idle200_end"] - 30, ev["idle200_end"])).get("mem_mean_mib")
        res["idle200"]["cpu_pct_whole_window"] = win_stats(sm.window(ev["users_end"], ev["idle200_end"])).get("cpu_pct_of_1vcpu")
        res["disk_idle200"] = {"data_bytes": du(p.data_dirs())}
        log("idle200:", {k: round(v, 1) for k, v in res["idle200"].items() if isinstance(v, float)})
        # 6. subscription sanity check, warm-up pass, load
        open(ROOT + "/out/urls.txt", "w").write("\n".join(urls) + "\n")
        hargs = " ".join("-H '%s'" % h for h in p.sub_headers)
        pu = urllib.parse.urlparse(urls[0])
        hd = {"User-Agent": UA, "X-Forwarded-For": "10.0.0.7", **{k.strip(): v.strip() for k, v in (h.split(":", 1) for h in p.sub_headers)}}
        hd["X-Forwarded-For"] = "10.0.0.7"
        code, body, r = Client(pu.hostname, pu.port).req("GET", pu.path, headers=hd, raw=True)
        res["sub_sample"] = {"status": code, "bytes": len(body), "content_type": r.getheader("Content-Type"), "headers": sorted(k.lower() for k, _ in r.getheaders())}
        try:
            res["sub_sample"]["decoded_lines"] = len(base64.b64decode(body).decode().strip().splitlines())
        except Exception:
            res["sub_sample"]["decoded_lines"] = None
        assert code == 200 and len(body) > 0, res["sub_sample"]
        res["warmup"] = json.loads(sh("%s%s/loadgen -urls %s/out/urls.txt -once -xff -ua '%s' %s" % (LOADGEN_PREFIX, BIN, ROOT, UA, hargs)))
        time.sleep(5)
        ev["load_start"] = time.time()
        lg = json.loads(sh("%s%s/loadgen -urls %s/out/urls.txt -c %d -d %ds -xff -ua '%s' %s" % (LOADGEN_PREFIX, BIN, ROOT, args.conc, args.load, UA, hargs), timeout=args.load + 120))
        ev["load_end"] = time.time()
        res["load"] = lg
        res["load_res"] = win_stats(sm.window(ev["load_start"], ev["load_end"]))
        log("load:", {k: round(v, 2) for k, v in lg.items() if isinstance(v, float)}, "cpu%%=%.0f memmax=%.0f" % (res["load_res"].get("cpu_pct_of_1vcpu", -1), res["load_res"].get("mem_max_mib", -1)))
        time.sleep(30)
        res["after_load"] = win_stats(sm.window(time.time() - 25, time.time()))
        res["slice_memory_peak_mib"] = int(open(SL + "/memory.peak").read()) / 1048576 if os.path.exists(SL + "/memory.peak") else None
        res["images_bytes"] = image_bytes(p)
        res["versions"] = p.versions()
        res["oom_events"] = _kv(SL + "/memory.events")
        # 7. restart with 200 users on disk
        p.stop()
        time.sleep(3)
        t0 = time.time()
        proc = p.start()
        res["cold_restart_s"] = wait_ready(p, t0)
        log("restart ready in %.2fs" % res["cold_restart_s"])
        if proc:
            proc.wait()
        code, body, _ = Client(pu.hostname, pu.port).req("GET", pu.path, headers=hd, raw=True)
        res["restart_sub_ok"] = bool(code == 200 and body)
        time.sleep(20)
        res["after_restart"] = win_stats(sm.window(time.time() - 15, time.time()))
        res["ok"] = True
    except Exception as e:
        res["ok"] = False
        res["error"] = repr(e)[:1500]
        log("FAILED:", repr(e)[:600])
        res["oom_events"] = _kv(SL + "/memory.events")
        res["container_state"] = sh("docker ps -a --format '{{.Names}}: {{.Status}}' | head -20", check=False)
    finally:
        sm.stop()
        res["events"] = ev
        keys = ["t", "mem", "anon", "file", "inactive_file", "shmem", "kernel", "cpu_us", "thr_n", "thr_us", "procs", "containers", "tasks"]
        with open("%s/%s_samples.csv" % (outdir, tag), "w") as f:
            f.write(",".join(keys) + "\n")
            for r in sm.rows:
                f.write(",".join("" if r.get(k) is None else str(r.get(k)) for k in keys) + "\n")
        json.dump(res, open("%s/%s.json" % (outdir, tag), "w"), indent=1, default=str)
        try:
            p.stop()
        except Exception:
            pass
        p.wipe()
    return res


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--panels", default="mistgate,3xui,remnawave,pasarguard")
    ap.add_argument("--reps", type=int, default=3)
    ap.add_argument("--idle", type=int, default=300)
    ap.add_argument("--load", type=int, default=60)
    ap.add_argument("--users", type=int, default=200)
    ap.add_argument("--conc", type=int, default=50)
    ap.add_argument("--tag", default="run")
    ap.add_argument("--cpumode", default="quota", choices=["quota", "pin"])
    args = ap.parse_args()
    global CPUMODE, LOADGEN_PREFIX
    CPUMODE = args.cpumode
    if CPUMODE == "pin":
        LOADGEN_PREFIX = "taskset -c 0-7 "
    outdir = "%s/out/%s" % (ROOT, args.tag)
    os.makedirs(outdir, exist_ok=True)
    names = args.panels.split(",")
    panels = {n: PANELS[n]() for n in names}
    for p in panels.values():
        p.prepare_once()
    for rep in range(1, args.reps + 1):
        k = (rep - 1) % len(names)
        for n in names[k:] + names[:k]:                 # rotate the panel order every repetition
            run_rep(panels[n], rep, args, outdir)
    log("all done")


if __name__ == "__main__":
    main()
