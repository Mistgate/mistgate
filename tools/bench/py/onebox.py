#!/usr/bin/env python3
"""One-server scenario: panel + VPN node/core + Postgres/Valkey etc. all inside ONE slice (2 vCPU / 2 GB, no swap),
Hysteria2 for every panel, the same clients (hyload, built on the official Hysteria 2 client library) outside the slice.
Phases: A idle, B throughput 1/16/64 streams, C many clients, D bad day (B64 + subscription storm + admin churn), E panel restart under traffic.
usage: onebox.py --panels mistgate,3xui,remnawave,pasarguard --reps 3 --users 1000 [--vcpu 2 --mem 2G] [--tag box] [--phases ABCDE]
Everything is local: an internal docker network (no outside route), a sink HTTP server on the bridge address, invented data only."""
import os, sys, time, json, re, subprocess, threading, base64, argparse, random, urllib.parse, http.client, secrets
import os; sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bench
from bench import sh, log, Client, totp, ROOT, BIN, DATA, UA

# The docker network is --internal (no route out). 11.11.0.0/16 is an ordinary public unicast range, so the egress rules of the
# VPN cores (Mistgate refuses private ranges, Xray's default routing refuses geoip:private) let the tunnel reach the sink.
NET, SUBNET, GW = "bench-box", "11.11.0.0/16", "11.11.0.1"
SINK_ADDR = GW + ":19000"
HYLOAD = BIN + "/hyload"
BOXSLICE = "bench-box.slice"
BOXCG = "/sys/fs/cgroup/bench.slice/bench-box.slice"
CLICG = "/sys/fs/cgroup/bench.slice/bench-client.slice"
NSRC = 1200


def src_ip(k):
    """source address of simulated user k: an alias on the bridge, so every user has its own address (per-IP limits behave as in production)"""
    return "11.11.%d.%d" % (k // 250 + 1, k % 250 + 1)


PIN = ""            # e.g. "14-15": pin the box to these host CPUs (and the clients to the rest)
CLIENT_PREFIX = ""


def box_limits(vcpu, mem):
    sh("systemctl stop %s 2>/dev/null; true" % BOXSLICE, check=False)
    unit = "[Unit]\nDescription=bench one-server box\n[Slice]\nCPUQuota=%d%%\nMemoryMax=%s\nMemoryHigh=infinity\nMemorySwapMax=0\nTasksMax=infinity\n" % (vcpu * 100, mem)
    if PIN:
        unit += "AllowedCPUs=%s\n" % PIN
    open("/etc/systemd/system/%s" % BOXSLICE, "w").write(unit)
    sh("systemctl daemon-reload; systemctl start %s" % BOXSLICE)


def cpu_usec(path):
    try:
        for line in open(path + "/cpu.stat"):
            if line.startswith("usage_usec"):
                return int(line.split()[1])
    except OSError:
        pass
    return 0


def pct(v, p):
    v = sorted(v)
    return v[int((len(v) - 1) * p)] if v else None


def host_stat():
    f = open("/proc/stat").readline().split()[1:]
    v = [int(x) for x in f]
    return sum(v), v[3] + v[4]            # total jiffies, idle+iowait


def host_busy(h0):
    t, i = host_stat()
    return 100.0 * (1 - (i - h0[1]) / max(1, (t - h0[0])))   # whole VM (16 vCPUs), % busy


class Run:
    """one hyload invocation in the client slice (outside the box); box CPU/memory sampled around it"""

    def __init__(self, args, dur, sampler):
        self.args, self.dur, self.sm = args, dur, sampler

    def go(self):
        c0, t0, h0 = cpu_usec(CLICG), time.time(), host_stat()
        out = sh("systemd-run --quiet --scope --slice=bench-client.slice %s%s %s" % (CLIENT_PREFIX, HYLOAD, self.args), timeout=self.dur + 240)
        t1 = time.time()
        r = json.loads(out.strip().splitlines()[-1])
        r["client_cpu_pct"] = (cpu_usec(CLICG) - c0) / 1e6 / (t1 - t0) * 100
        r["host_cpu_busy_pct"] = host_busy(h0)
        r["box"] = bench.win_stats(self.sm.window(t0 + 2, t1 - 1))
        r["window"] = [t0, t1]
        return r


class Box:
    name = "?"
    server = None            # host:port of the Hysteria2 listener
    core_restart_on_user_change = "?"
    protocol_note = ""

    def prepare_once(self): pass
    def wipe(self): raise NotImplementedError
    def setup(self, n): raise NotImplementedError          # sets self.auths (list), self.sub_urls (list), self.server
    def restart_panel_only(self): raise NotImplementedError
    def admin_ok(self): raise NotImplementedError
    def admin_call(self): raise NotImplementedError        # one authenticated, cheap admin API call -> http status
    def api_add_user(self, i): raise NotImplementedError
    def api_del_user(self, i): raise NotImplementedError
    def sub_headers(self): return []
    def versions(self): return {}


def parse_hy2(line):
    u = urllib.parse.urlsplit(line.strip())
    auth = urllib.parse.unquote(u.netloc.rsplit("@", 1)[0])
    return auth, u.hostname, u.port, dict(urllib.parse.parse_qsl(u.query))


def fetch_sub(url, k, headers=None, src=None):
    u = urllib.parse.urlsplit(url)
    c = http.client.HTTPConnection(u.hostname, u.port, timeout=30, source_address=(src or src_ip(k), 0))
    c.request("GET", u.path + ("?" + u.query if u.query else ""), headers={"User-Agent": UA, **(headers or {})})
    r = c.getresponse()
    b = r.read()
    c.close()
    return r.status, b


def collect_auths(box, sub_headers=None):
    """read every user's hysteria2:// link from its subscription (own source address per user) -> auth strings"""
    auths = []
    for k, u in enumerate(box.sub_urls):
        code, body = fetch_sub(u, k, sub_headers)
        assert code == 200 and body, (k, code)
        lines = base64.b64decode(body).decode().strip().splitlines()
        hy = [l for l in lines if l.startswith("hysteria2://") or l.startswith("hy2://")]
        assert hy, "no hysteria2 link in subscription of user %d: %r" % (k, lines[:2])
        auth, host, port, q = parse_hy2(hy[0])
        auths.append(auth)
        box.link_query = q
        box.link_host = (host, port)
    return auths


# ---------------------------------------------------------------- Mistgate
class MistBox(Box):
    name = "mistgate"
    NODE_IP = "11.11.0.10"
    PANEL = ("11.11.0.1", 18080)
    D = DATA + "/box-mg"
    NODE_STATE = DATA + "/box-mg-node"
    NODE_IMAGE = "bench/mgnode:0.1.32"
    NODE_ENV = ""
    core_restart_on_user_change = "no: the node agent adds and removes users in the running Hysteria2 server"

    def wipe(self):
        sh("systemctl stop bench-mg-box 2>/dev/null; systemctl reset-failed bench-mg-box 2>/dev/null; docker rm -f bench_mgnode_box 2>/dev/null; rm -rf %s %s; mkdir -p %s" % (self.D, self.NODE_STATE, self.NODE_STATE), check=False)

    def start_panel(self):
        sh("systemd-run --unit=bench-mg-box --slice=%s --quiet %s/mistgate serve --data-dir %s --listen %s:%d --agent-listen %s:18081 --agent-addr %s:18081"
           % (BOXSLICE, BIN, self.D, self.PANEL[0], self.PANEL[1], GW, GW))

    def rpc(self, svc, method, body=None):
        for _ in range(120):
            st, b, r = self.c.req("POST", self.admin_path + "api/mistgate.admin.v1.%s/%s" % (svc, method), body=body or {})
            if st != 429:
                break
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

    def setup(self, n):
        out = sh("%s/mistgate setup --data-dir %s --public-url http://localhost:18080" % (BIN, self.D))
        tok = re.search(r"Setup link:\s+\S+#(\S+)", out).group(1)
        self.admin_path = urllib.parse.urlparse(re.search(r"Admin URL:\s+(\S+)", out).group(1)).path
        self.start_panel()
        t0 = time.time()
        while not bench.http_ready(self.PANEL[0], self.PANEL[1], "/") and time.time() - t0 < 30:
            time.sleep(0.1)
        c = Client(*self.PANEL)
        self.c = c
        st, b, _ = self.rpc("AuthService", "BeginSetup", {"setupToken": tok, "displayName": "Bench owner", "method": "SETUP_METHOD_PASSWORD", "login": "benchowner", "password": "bn-" + secrets.token_hex(10)})
        assert st == 200, (st, b)
        self.totp = b["totpSecret"]
        step = int(time.time()) // 30
        st, b, _ = self.rpc("AuthService", "FinishSetup", {"setupToken": tok, "ceremonyId": b["ceremonyId"], "totpCode": totp(self.totp, step)})
        assert st == 200, (st, b)
        c.cookies = {"__Host-sid": c.cookies["__Host-sid"]}
        self.last_step = step
        self._stepup()
        st, b, _ = self.rpc("ProfileService", "ListProtocols")
        d = json.loads([p for p in b["protocols"] if p["id"] == "hysteria2"][0]["defaultSettingsJson"])
        d["tls_mode"] = "self_signed"
        d["obfs"] = {"type": "none"}
        st, b, _ = self.rpc("ProfileService", "CreateProfile", {"protocol": "hysteria2", "name": "main", "settingsJson": json.dumps(d)})
        assert st == 200, (st, b)
        pid = b["profile"]["id"]
        st, b, _ = self.rpc("NodeService", "CreateEnrollment", {"name": "box1", "address": self.NODE_IP, "countryCode": "DE"})
        assert st == 200, (st, b)
        nid = b["node"]["id"]
        a = {k: re.search(r"--%s (\S+)" % k, b["installCommand"]).group(1) for k in ("panel", "sni", "ca-sha256", "token")}
        sh("docker run --rm --network %s -v %s:/state %s enroll --panel %s --sni %s --ca-sha256 %s --token %s --state-dir /state" % (NET, self.NODE_STATE, self.NODE_IMAGE, a["panel"], a["sni"], a["ca-sha256"], a["token"]))
        sh("docker run -d --name bench_mgnode_box --network %s --ip %s --cgroup-parent %s --cap-add NET_ADMIN %s -v %s:/state %s run --state-dir /state" % (NET, self.NODE_IP, BOXSLICE, self.NODE_ENV, self.NODE_STATE, self.NODE_IMAGE))
        st, b, _ = self.rpc("ProfileService", "CreateInbound", {"profileId": pid, "nodeId": nid})
        assert st == 200, (st, b)
        st, b, _ = self.rpc("GroupService", "CreateGroup", {"name": "everyone", "profileIds": [pid]})
        assert st == 200, (st, b)
        self.gid = b["group"]["id"]
        self.users = {}
        urls = []
        for i in range(1, n + 1):
            st, b, _ = self.rpc("UserService", "CreateUser", {"name": "user%d" % i, "groupId": self.gid})
            assert st == 200, (i, st, b)
            self.users[i] = b["user"]["id"]
            urls.append("http://%s:%d%s" % (self.PANEL[0], self.PANEL[1], urllib.parse.urlparse(b["subscriptionUrl"]).path))
        self.sub_urls = urls
        for i in range(180):                                   # the node connects, starts the self-signed Hysteria2 server and reports its pin
            code, body = fetch_sub(urls[0], 0)
            if code == 200 and body:
                log("mistgate box: node up, subscription complete after %ds" % i)
                break
            time.sleep(1)
        else:
            raise RuntimeError("mistgate node never served: " + sh("docker logs bench_mgnode_box 2>&1 | tail -20", check=False))
        self.auths = collect_auths(self)
        self.server = "%s:%d" % self.link_host

    def restart_panel_only(self):
        sh("systemctl restart bench-mg-box")

    def admin_ok(self):
        return bench.http_ready(self.PANEL[0], self.PANEL[1], "/")

    def admin_call(self):
        st, b, _ = self.c.req("POST", self.admin_path + "api/mistgate.admin.v1.UserService/ListUsers", body={"pageSize": 1})
        return st

    def api_add_user(self, i):
        st, b, _ = self.c.req("POST", self.admin_path + "api/mistgate.admin.v1.UserService/CreateUser", body={"name": "churn%d" % i, "groupId": self.gid})
        assert st == 200, (st, b)
        self.users["c%d" % i] = b["user"]["id"]

    def api_del_user(self, i):
        st, b, _ = self.c.req("POST", self.admin_path + "api/mistgate.admin.v1.UserService/DeleteUsers", body={"userIds": [self.users.pop("c%d" % i)]})
        assert st == 200, (st, b)

    def versions(self):
        return {"mistgate": sh(BIN + "/mistgate version", check=False).strip().replace("\n", " | ")}


# ---------------------------------------------------------------- 3x-ui
class XuiBox(Box):
    name = "3xui"
    IP = "11.11.0.20"
    P, F = "benchbox3x", ROOT + "/3xui/box.yml"
    DD = DATA + "/box-3xui"
    core_restart_on_user_change = "to be measured (3x-ui restarts Xray on client changes)"

    def wipe(self):
        sh("docker compose -p %s -f %s down -v 2>&1 | tail -1; rm -rf %s; mkdir -p %s/cert" % (self.P, self.F, self.DD, self.DD), check=False)
        sh("openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout %s/cert/key.pem -out %s/cert/cert.pem -days 30 -subj /CN=bench.test 2>/dev/null" % (self.DD, self.DD), check=False)

    def _login(self):
        c = Client(self.IP, 2053)
        st, b, _ = c.req("GET", "/csrf-token")
        st, b, _ = c.req("POST", "/login", body={"username": "admin", "password": "admin"}, headers={"X-CSRF-Token": b["obj"]})
        assert st == 200 and b.get("success"), (st, b)
        st, b, _ = c.req("GET", "/csrf-token")
        c.h["X-CSRF-Token"] = b["obj"]
        self.c = c

    def setup(self, n):
        sh("docker compose -p %s -f %s up -d" % (self.P, self.F))
        t0 = time.time()
        while not bench.http_ready(self.IP, 2053, "/") and time.time() - t0 < 60:
            time.sleep(0.2)
        self._login()
        c = self.c
        inb = {"enable": True, "remark": "hy2", "listen": "", "port": 443, "protocol": "hysteria", "expiryTime": 0, "total": 0,
               "settings": {"version": 2, "clients": []},
               "streamSettings": {"network": "hysteria", "security": "tls",
                                  "tlsSettings": {"serverName": "", "certificates": [{"certificateFile": "/root/cert/cert.pem", "keyFile": "/root/cert/key.pem"}], "alpn": ["h3"]},
                                  "hysteriaSettings": {"version": 2}},
               "sniffing": {"enabled": False, "destOverride": ["http", "tls"], "metadataOnly": False, "routeOnly": False}}
        st, b, _ = c.req("POST", "/panel/api/inbounds/add", body=inb)
        assert st == 200 and b.get("success"), (st, b)
        self.iid = b["obj"]["id"]
        self.auths = []
        for i in range(1, n + 1):
            a = "pw%d-%s" % (i, secrets.token_hex(8))
            st, b, _ = c.req("POST", "/panel/api/clients/add", body={"client": {"email": "user%d" % i, "enable": True, "auth": a, "totalGB": 0, "expiryTime": 0}, "inboundIds": [self.iid]})
            assert st == 200 and b.get("success"), (i, st, b)
            self.auths.append(a)
        st, b, _ = c.req("POST", "/panel/api/setting/all", body={})
        sub_path, sub_port = b["obj"]["subPath"], b["obj"]["subPort"]
        st, b, _ = c.req("GET", "/panel/api/clients/list")
        ids = {x["email"]: x["subId"] for x in b["obj"]}
        self.sub_urls = ["http://%s:%d%s%s" % (self.IP, sub_port, sub_path, ids["user%d" % i]) for i in range(1, n + 1)]
        self.server = "%s:443" % self.IP
        # the subscription must carry a Hysteria2 link for the user (same protocol as the tunnel)
        code, body = fetch_sub(self.sub_urls[0], 0)
        lines = base64.b64decode(body).decode().strip().splitlines()
        self.sub_sample = lines[0][:60] if lines else ""
        assert any(l.startswith("hysteria2://") for l in lines), lines[:2]

    def restart_panel_only(self):
        sh("docker restart bench_3xui_box")          # one container: x-ui and the Xray core it supervises

    def admin_ok(self):
        return bench.http_ready(self.IP, 2053, "/")

    def admin_call(self):
        st, b, _ = self.c.req("GET", "/panel/api/inbounds/list/slim")
        if st in (401, 403, 404):                     # session lost after a restart: log in again
            self._login()
            st, b, _ = self.c.req("GET", "/panel/api/inbounds/list/slim")
        return st

    def api_add_user(self, i):
        st, b, _ = self.c.req("POST", "/panel/api/clients/add", body={"client": {"email": "churn%d" % i, "enable": True, "auth": "pwc%d-%s" % (i, secrets.token_hex(6)), "totalGB": 0, "expiryTime": 0}, "inboundIds": [self.iid]})
        assert st == 200 and b.get("success"), (st, b)

    def api_del_user(self, i):
        st, b, _ = self.c.req("POST", "/panel/api/clients/del/churn%d" % i, body={})
        assert st == 200 and b.get("success"), (st, b)

    def versions(self):
        return {"3x-ui": "v3.9.0", "xray": "26.9.30 (as logged by the container)"}


# ---------------------------------------------------------------- Remnawave
class RemBox(Box):
    name = "remnawave"
    IP, NODE_IP = "11.11.0.30", "11.11.0.31"
    P, F = "benchrembox", ROOT + "/remna/box.yml"
    DD = DATA + "/box-rw"
    FWH = {"X-Forwarded-For": "127.0.0.1", "X-Forwarded-Proto": "https"}      # what the TLS reverse proxy in front of the backend sends
    core_restart_on_user_change = "to be measured (users are added to the running Xray of the node)"

    def sub_headers(self):
        return ["X-Forwarded-For: 127.0.0.1", "X-Forwarded-Proto: https"]

    def wipe(self):
        sh("docker compose -p %s -f %s down -v 2>&1 | tail -1; rm -rf %s; mkdir -p %s/cert" % (self.P, self.F, self.DD, self.DD), check=False)
        sh("openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout %s/cert/key.pem -out %s/cert/cert.pem -days 30 -subj /CN=bench.test 2>/dev/null; chmod 644 %s/cert/*" % (self.DD, self.DD, self.DD), check=False)

    def compose(self, what, key="placeholder"):
        return sh("NODE_SECRET_KEY='%s' docker compose -p %s -f %s %s" % (key, self.P, self.F, what))

    def setup(self, n):
        self.compose("up -d remnawave remnawave-db remnawave-redis")
        t0 = time.time()
        while not bench.http_ready(self.IP, 3000, "/api/auth/status", self.FWH) and time.time() - t0 < 120:
            time.sleep(0.3)
        c = Client(self.IP, 3000, self.FWH)
        pw = "Bn-" + secrets.token_hex(14) + "Aa1"
        st, b, _ = c.req("POST", "/api/auth/register", body={"username": "benchadmin", "password": pw})
        assert st == 201, (st, b)
        c.tok = b["response"]["accessToken"]
        c.h["x-remnawave-client-type"] = "browser"
        st, b, _ = c.req("POST", "/api/tokens", body={"name": "bench", "expiresInDays": 7})
        assert st == 201, (st, b)
        c.tok = b["response"]["token"]
        c.h.pop("x-remnawave-client-type")
        self.c = c
        st, b, _ = c.req("GET", "/api/keygen")
        key = b["response"]["pubKey"]
        self.compose("up -d remnanode", key)
        cfg = {"log": {"loglevel": "warning"},
               "inbounds": [{"tag": "hy2", "port": 443, "protocol": "hysteria", "settings": {"version": 2, "clients": []},
                             "streamSettings": {"network": "hysteria", "security": "tls",
                                                "tlsSettings": {"certificates": [{"certificateFile": "/certs/cert.pem", "keyFile": "/certs/key.pem"}], "alpn": ["h3"]},
                                                "hysteriaSettings": {"version": 2}}}],
               "outbounds": [{"tag": "DIRECT", "protocol": "freedom"}, {"tag": "BLOCK", "protocol": "blackhole"}],
               "routing": {"rules": []}}
        st, b, _ = c.req("POST", "/api/config-profiles", body={"name": "hy2", "config": cfg})
        assert st == 201, (st, b)
        puid, inb = b["response"]["uuid"], b["response"]["inbounds"][0]["uuid"]
        st, b, _ = c.req("POST", "/api/nodes", body={"name": "box1", "address": self.NODE_IP, "port": 2222, "isTrafficTrackingActive": False,
                                                      "configProfile": {"activeConfigProfileUuid": puid, "activeInbounds": [inb]}, "countryCode": "XX"})
        assert st == 201, (st, b)
        st, b, _ = c.req("GET", "/api/internal-squads")
        self.squad = b["response"]["internalSquads"][0]["uuid"]
        st, b, _ = c.req("PATCH", "/api/internal-squads", body={"uuid": self.squad, "inbounds": [inb]})
        assert st == 200, (st, b)
        st, b, _ = c.req("POST", "/api/hosts", body={"inbound": {"configProfileUuid": puid, "configProfileInboundUuid": inb}, "remark": "box-hy2", "address": self.NODE_IP, "port": 443, "sni": "bench.test", "allowInsecure": True})
        assert st == 201, (st, b)
        for i in range(120):                                   # wait until the node is connected and its Xray runs
            st, b, _ = c.req("GET", "/api/nodes")
            if st == 200 and b["response"] and b["response"][0].get("isConnected"):
                break
            time.sleep(1)
        else:
            raise RuntimeError("remnawave node never connected")
        self.auths, self.sub_urls, self.uuids = [], [], {}
        for i in range(1, n + 1):
            st, b, _ = c.req("POST", "/api/users", body={"username": "user%d" % i, "expireAt": "2099-01-01T00:00:00.000Z", "activeInternalSquads": [self.squad]})
            assert st == 201, (i, st, b)
            u = b["response"]
            self.auths.append(u["vlessUuid"])                 # the Hysteria2 password the panel puts in the link
            self.sub_urls.append("http://%s:3000/api/sub/%s" % (self.IP, u["shortUuid"]))
        self.server = "%s:443" % self.NODE_IP
        code, body = fetch_sub(self.sub_urls[0], 0, self.FWH)
        lines = base64.b64decode(body).decode().strip().splitlines()
        assert lines and lines[0].startswith("hysteria2://") and parse_hy2(lines[0])[0] == self.auths[0], lines[:2]

    def restart_panel_only(self):
        sh("docker restart remnawave")                 # the backend only; Postgres, Valkey and the node keep running

    def admin_ok(self):
        return bench.http_ready(self.IP, 3000, "/api/auth/status", self.FWH)

    def admin_call(self):
        st, b, _ = self.c.req("GET", "/api/users?size=1")
        return st

    def api_add_user(self, i):
        st, b, _ = self.c.req("POST", "/api/users", body={"username": "churn%d" % i, "expireAt": "2099-01-01T00:00:00.000Z", "activeInternalSquads": [self.squad]})
        assert st == 201, (st, b)
        self.uuids[i] = b["response"]

    def api_del_user(self, i):
        u = self.uuids.pop(i)
        tries = [p for p in ("/api/users/%s" % u.get("uuid"), "/api/users/by-id/%s" % u.get("id"), "/api/users/%s" % u.get("id")) if "None" not in p]
        for path in tries:
            st, b, _ = self.c.req("DELETE", path)
            if st in (200, 204):
                return
        raise AssertionError((st, b, tries))

    def versions(self):
        return {"remnawave backend": "3.4.5", "remnawave node": "3.4.2 (Xray 26.7.28)", "postgres": "18.4", "valkey": "9-alpine"}


# ---------------------------------------------------------------- PasarGuard
class PgBox(Box):
    name = "pasarguard"
    NODE_IP = "11.11.0.41"
    P, F = "benchpgbox", ROOT + "/pasar/box.yml"
    DD = DATA
    bind_prefix = "127.1"                                   # the panel container uses host networking (as shipped) and listens on 127.0.0.1:8000
    core_restart_on_user_change = "to be measured (users are synced to the node's running Xray)"

    def src(self, k):
        return "127.1.%d.%d" % (k // 250 + 1, k % 250 + 1)

    def wipe(self):
        D = self.DD
        sh("docker compose -p %s -f %s down 2>&1 | tail -1; rm -rf %s/box-pasar %s/box-pasar-node %s/box-pasar-cert; mkdir -p %s/box-pasar %s/box-pasar-node/certs %s/box-pasar-cert" % ((self.P, self.F) + (D,) * 6), check=False)

    def setup(self, n):
        D = self.DD
        sh("sed -e 's|^UVICORN_HOST = .*|UVICORN_HOST = \"127.0.0.1\"|' @R@/pasar/.env > @R@/pasar/box.env")
        sh("openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout %s/box-pasar-node/certs/ssl_key.pem -out %s/box-pasar-node/certs/ssl_cert.pem -days 30 -subj /CN=%s -addext subjectAltName=IP:%s 2>/dev/null" % (D, D, self.NODE_IP, self.NODE_IP))
        sh("openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout %s/box-pasar-cert/key.pem -out %s/box-pasar-cert/cert.pem -days 30 -subj /CN=bench.test 2>/dev/null; chmod 644 %s/box-pasar-cert/*" % (D, D, D))
        import uuid
        apikey = str(uuid.uuid4())
        sh("PG_NODE_API_KEY=%s docker compose -p %s -f %s up -d" % (apikey, self.P, self.F))
        t0 = time.time()
        while not bench.http_ready("127.0.0.1", 8000, "/") and time.time() - t0 < 120:
            time.sleep(0.3)
        key = re.search(r"Temp key: (\S+)", sh("docker exec bench_pasarguard_box pasarguard-cli generate-temp-key")).group(1)
        c = Client("127.0.0.1", 8000)
        self.pw = "Bn-" + secrets.token_hex(12) + "Aa1"
        st, b, _ = c.req("POST", "/api/setup/owner", body={"key": key, "username": "benchadmin", "password": self.pw})
        assert st == 201, (st, b)
        st, b, _ = c.req("POST", "/api/admin/token", form={"username": "benchadmin", "password": self.pw})
        c.tok = b["access_token"]
        self.c = c
        st, b, _ = c.req("GET", "/api/cores")
        core = b["cores"][0]
        cfg = core["config"]
        cfg["inbounds"] = [{"tag": "Hysteria2 UDP", "listen": "0.0.0.0", "port": 443, "protocol": "hysteria", "settings": {"version": 2, "clients": []},
                            "streamSettings": {"network": "hysteria", "security": "tls",
                                               "tlsSettings": {"certificates": [{"certificateFile": "/certs/cert.pem", "keyFile": "/certs/key.pem"}], "alpn": ["h3"]},
                                               "hysteriaSettings": {"version": 2}}}]
        cfg["routing"] = {"rules": []}
        st, b, _ = c.req("PUT", "/api/core/%d?restart_nodes=true" % core["id"], body={"name": core["name"], "config": cfg, "exclude_inbound_tags": [], "fallbacks_inbound_tags": []})
        assert st == 200, (st, b)
        ca = open("%s/box-pasar-node/certs/ssl_cert.pem" % D).read()
        st, b, _ = c.req("POST", "/api/node", body={"name": "box1", "address": self.NODE_IP, "port": 62050, "usage_coefficient": 1, "server_ca": ca, "connection_type": "grpc", "keep_alive": 60, "core_config_id": core["id"], "api_key": apikey})
        assert st == 201, (st, b)
        st, b, _ = c.req("POST", "/api/group", body={"name": "everyone", "inbound_tags": ["Hysteria2 UDP"]})
        assert st == 201, (st, b)
        self.gid = b["id"]
        st, b, _ = c.req("POST", "/api/host/", body={"remark": "box-hy2", "address": [self.NODE_IP], "inbound_tag": "Hysteria2 UDP", "port": 443, "sni": ["bench.test"], "allowinsecure": True, "priority": 1})
        assert st == 201, (st, b)
        time.sleep(10)
        self.auths, self.sub_urls = [], []
        for i in range(1, n + 1):
            st, b, _ = c.req("POST", "/api/user", body={"username": "user%d" % i, "group_ids": [self.gid], "data_limit": 0})
            assert st == 201, (i, st, b)
            self.auths.append(b["proxy_settings"]["hysteria"]["auth"])
            self.sub_urls.append("http://127.0.0.1:8000" + b["subscription_url"])
        self.server = "%s:443" % self.NODE_IP
        code, body = fetch_sub(self.sub_urls[0], 0, None, self.src(0))
        lines = base64.b64decode(body).decode().strip().splitlines()
        assert lines and lines[0].startswith("hysteria2://"), lines[:2]
        # the panel pushes new users to the node in batches (SyncUser); wait until the last user can really connect
        t0 = time.time()
        while time.time() - t0 < 300:
            out = sh("%s tp -server %s -auth '%s' -sink %s -streams 1 -d 2s -probe 0" % (HYLOAD, self.server, self.auths[-1], SINK_ADDR), check=False)
            if '"gbit_s":0,' not in out and '"error"' not in out:
                break
            time.sleep(2)
        self.user_sync_wait_s = time.time() - t0
        log("pasarguard box: last user can connect after %.0fs" % self.user_sync_wait_s)

    def restart_panel_only(self):
        sh("docker restart bench_pasarguard_box")      # the panel only; the node container (with its Xray) keeps running

    def admin_ok(self):
        return bench.http_ready("127.0.0.1", 8000, "/")

    def admin_call(self):
        st, b, _ = self.c.req("GET", "/api/users?limit=1")
        if st == 401:                                   # token survives a restart; log in again just in case
            st, b, _ = self.c.req("POST", "/api/admin/token", form={"username": "benchadmin", "password": self.pw})
            self.c.tok = b.get("access_token")
            st, b, _ = self.c.req("GET", "/api/users?limit=1")
        return st

    def api_add_user(self, i):
        st, b, _ = self.c.req("POST", "/api/user", body={"username": "churn%d" % i, "group_ids": [self.gid], "data_limit": 0})
        assert st == 201, (st, b)

    def api_del_user(self, i):
        st, b, _ = self.c.req("DELETE", "/api/user/churn%d" % i)
        assert st in (200, 204), (st, b)

    def versions(self):
        return {"pasarguard panel": "v5.4.1 (SQLite)", "pasarguard node": "v0.5.4 (Xray 26.3.27)"}


# ---------------------------------------------------------------- driver
BOXES = {"mistgate": MistBox, "3xui": XuiBox, "remnawave": RemBox, "pasarguard": PgBox}


def prepare_infra(first=True):
    nets = sh("docker network ls --format '{{.Name}}'")
    if NET in nets:
        sub = sh("docker network inspect %s -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}'" % NET).strip()
        if sub != SUBNET:
            sh("docker network rm %s" % NET)
            nets = ""
    if NET not in nets:
        sh("docker network create --internal --subnet %s --gateway %s %s" % (SUBNET, GW, NET))
    nid = sh("docker network inspect %s -f '{{.Id}}'" % NET).strip()
    br = "br-" + nid[:12]
    existing = sh("ip -o addr show dev %s" % br)
    if "11.11.5." not in existing:
        batch = "\n".join("addr add %s/32 dev %s" % (src_ip(k), br) for k in range(NSRC)) + "\n"
        open("/tmp/ipbatch.txt", "w").write(batch)
        sh("ip -batch /tmp/ipbatch.txt", check=False)
    sh("systemctl start bench-client.slice 2>/dev/null; true", check=False)


def start_sink():
    sh("systemctl stop bench-sink 2>/dev/null; systemd-run --unit=bench-sink --slice=bench-client.slice --quiet %s%s sink -listen %s" % (CLIENT_PREFIX, HYLOAD, SINK_ADDR))
    time.sleep(0.5)


def stop_sink():
    sh("systemctl stop bench-sink", check=False)


def phase_tp(box, sm, streams, dur, user=0):
    r = Run("tp -server %s -auth '%s' -sink %s -streams %d -d %ds -probe 100ms" % (box.server, box.auths[user], SINK_ADDR, streams, dur), dur, sm).go()
    r["cpu_pct_box"] = r["box"].get("cpu_pct_of_1vcpu")
    if r.get("gbit_s") is not None and r["cpu_pct_box"]:
        r["mbit_per_cpu_sec"] = r["gbit_s"] * 1000 / (r["cpu_pct_box"] / 100)
    return r


def phase_D(box, sm, args, res):
    """bad day: 64 streams + subscription storm from 1000 source addresses + admin churn (one add and one remove per second), together"""
    dur = args.bad_dur
    open("/tmp/suburls.txt", "w").write("\n".join(box.sub_urls) + "\n")
    out = {}
    stop = threading.Event()
    lat_add, lat_del, lat_call, errs = [], [], [], []

    def churn():
        i = 0
        nxt = time.time()
        while not stop.is_set():
            i += 1
            try:
                t = time.time()
                box.api_add_user(i)
                lat_add.append((time.time() - t) * 1000)
                if i > 1:
                    t = time.time()
                    box.api_del_user(i - 1)
                    lat_del.append((time.time() - t) * 1000)
                t = time.time()
                st = box.admin_call()
                lat_call.append((time.time() - t) * 1000)
                if st != 200:
                    errs.append("call %s" % st)
            except Exception as e:
                errs.append(repr(e)[:120])
            nxt += 1.0
            stop.wait(max(0, nxt - time.time()))
        try:
            box.api_del_user(i)
        except Exception:
            pass

    def storm():
        hargs = " ".join("-H '%s'" % h for h in box.sub_headers())
        rate = "-rate %d" % args.storm_rate if args.storm_rate > 0 else ""
        o = sh("systemd-run --quiet --scope --slice=bench-client.slice %s%s/loadgen -urls /tmp/suburls.txt -c 50 -d %ds -bind -bind-prefix %s %s -ua '%s' %s" % (CLIENT_PREFIX, BIN, dur, getattr(box, "bind_prefix", "11.11"), rate, UA, hargs), timeout=dur + 120)
        out["storm"] = json.loads(o)

    def conntrack():
        mx = 0
        while not stop.is_set():
            try:
                mx = max(mx, int(open("/proc/sys/net/netfilter/nf_conntrack_count").read()))
            except OSError:
                pass
            stop.wait(1.0)
        out["conntrack_max_seen"] = mx
        out["conntrack_limit"] = int(open("/proc/sys/net/netfilter/nf_conntrack_max").read())

    def tunnel():
        out["tp"] = Run("tp -server %s -auth '%s' -sink %s -streams 64 -d %ds -probe 100ms" % (box.server, box.auths[1], SINK_ADDR, dur), dur, sm).go()

    full0 = sh("dmesg | grep -c 'table full'", check=False).strip() or "0"
    ths = [threading.Thread(target=f) for f in (tunnel, storm, churn, conntrack)]
    for t in ths:
        t.start()
    ths[0].join()
    ths[1].join()
    stop.set()
    ths[2].join()
    ths[3].join()
    out["conntrack_table_full_messages"] = int((sh("dmesg | grep -c 'table full'", check=False).strip() or "0")) - int(full0)
    out["admin_add_ms"] = {"n": len(lat_add), "p50": pct(lat_add, .5), "p95": pct(lat_add, .95), "max": max(lat_add or [0])}
    out["admin_del_ms"] = {"n": len(lat_del), "p50": pct(lat_del, .5), "p95": pct(lat_del, .95), "max": max(lat_del or [0])}
    out["admin_call_ms"] = {"n": len(lat_call), "p50": pct(lat_call, .5), "p95": pct(lat_call, .95), "max": max(lat_call or [0])}
    out["admin_errors"] = errs[:10]
    out["admin_error_count"] = len(errs)
    return out


def phase_E(box, sm, args):
    """restart only the panel (process/container) while 64 streams run; does the tunnel survive, how long until the admin answers"""
    dur = args.restart_dur
    res = {}
    done = threading.Event()

    def restarter():
        time.sleep(dur / 3)
        t0 = time.time()
        box.restart_panel_only()
        t_cmd = time.time() - t0
        while not box.admin_ok():
            time.sleep(0.05)
            if time.time() - t0 > 120:
                break
        res["restart_command_s"] = t_cmd
        res["admin_http_back_s"] = time.time() - t0
        res["restart_at_s"] = dur / 3
        try:
            for _ in range(200):
                if box.admin_call() == 200:
                    break
                time.sleep(0.1)
            res["admin_api_back_s"] = time.time() - t0
        except Exception as e:
            res["admin_api_error"] = repr(e)[:100]
        done.set()

    th = threading.Thread(target=restarter)
    th.start()
    r = Run("tp -server %s -auth '%s' -sink %s -streams 64 -d %ds -probe 100ms" % (box.server, box.auths[2], SINK_ADDR, dur), dur, sm).go()
    th.join()
    res["tp"] = r
    series = r.get("mbit_s_per_second") or []
    res["zero_seconds"] = sum(1 for x in series if x < 1.0)
    res["streams_dropped"] = r.get("streams_dropped")
    res["reconnects"] = r.get("reconnects")
    res["reconnect_ms"] = r.get("reconnect_ms")
    return res


def run_one(box, rep, args, outdir):
    tag = "%s_r%d" % (box.name, rep)
    res = {"panel": box.name, "rep": rep, "vcpu": args.vcpu, "mem": args.mem, "users": args.users, "started": time.strftime("%Y-%m-%d %H:%M:%S"),
           "core_restart_on_user_change": box.core_restart_on_user_change}
    log("=== onebox %s" % tag)
    box.wipe()
    time.sleep(3)
    box_limits(args.vcpu, args.mem)
    bench.SL = BOXCG
    res["background"] = bench.bg_snapshot()
    sm = bench.Sampler()
    sm.start()
    ph = args.phases
    try:
        t0 = time.time()
        box.setup(args.users)
        res["setup_s"] = time.time() - t0
        log("setup %.0fs; server %s, %d auths" % (res["setup_s"], box.server, len(box.auths)))
        open("%s/auths_%s.txt" % (outdir, box.name), "w").write(chr(10).join(box.auths[:20]) + chr(10))
        smoke = phase_tp(box, sm, 1, 5)
        res["smoke"] = {k: smoke.get(k) for k in ("gbit_s", "first_error", "streams_dropped")}
        assert smoke.get("gbit_s", 0) > 0, smoke
        if "A" in ph:
            time.sleep(args.settle)
            t1 = time.time()
            time.sleep(args.idle)
            res["A_idle"] = bench.win_stats(sm.window(t1, time.time()))
            res["A_idle"]["procs_detail"] = bench.proc_detail()
            log("A idle: anon %.0f MiB, cpu %.2f%%" % (res["A_idle"]["anon_mean_mib"], res["A_idle"].get("cpu_pct_of_1vcpu", -1)))
        if "B" in ph:
            res["B"] = {}
            for s in (1, 16, 64):
                r = phase_tp(box, sm, s, args.tp_dur)
                res["B"][str(s)] = r
                log("B %d streams: %.2f Gbit/s, box cpu %.0f%%, client cpu %.0f%%, probe p95 %.1f ms" % (s, r.get("gbit_s", 0), r.get("cpu_pct_box") or 0, r.get("client_cpu_pct") or 0, r.get("probe_ms_p95") or 0))
        if "C" in ph:
            open("/tmp/auths.txt", "w").write("\n".join(box.auths) + "\n")
            c = Run("many -server %s -auths /tmp/auths.txt -n %d -d %ds -ramp 20s -every 2s -sink %s" % (box.server, args.many, args.many_dur, SINK_ADDR), args.many_dur, sm).go()
            res["C"] = c
            log("C %d clients: handshake p95 %.0f ms, fail %s, req fail %s" % (args.many, c.get("handshake_ms_p95") or 0, c.get("handshake_fail"), c.get("requests_fail")))
        if "D" in ph:
            res["D"] = phase_D(box, sm, args, res)
            d = res["D"]
            log("D tunnel %.2f Gbit/s (dropped %s, reconnects %s); storm %.0f rps p95 %.0f ms err %.1f%%; admin add p95 %s ms" % (
                d["tp"].get("gbit_s", 0), d["tp"].get("streams_dropped"), d["tp"].get("reconnects"), d["storm"].get("rps", 0), d["storm"].get("lat_ms_p95", 0), 100 * d["storm"].get("error_rate", 0), d["admin_add_ms"]["p95"]))
        if "E" in ph:
            res["E"] = phase_E(box, sm, args)
            e = res["E"]
            log("E restart: admin http back %.1fs, api back %s, dropped %s, reconnects %s, zero seconds %s" % (e.get("admin_http_back_s", -1), e.get("admin_api_back_s"), e["streams_dropped"], e["reconnects"], e["zero_seconds"]))
        res["versions"] = box.versions()
        res["ok"] = True
    except Exception as e:
        res["ok"] = False
        res["error"] = repr(e)[:1500]
        log("FAILED:", repr(e)[:800])
    finally:
        sm.stop()
        keys = ["t", "mem", "anon", "file", "inactive_file", "shmem", "kernel", "cpu_us", "thr_n", "thr_us", "procs", "containers", "tasks"]
        with open("%s/%s_samples.csv" % (outdir, tag), "w") as f:
            f.write(",".join(keys) + "\n")
            for r in sm.rows:
                f.write(",".join("" if r.get(k) is None else str(r.get(k)) for k in keys) + "\n")
        json.dump(res, open("%s/%s.json" % (outdir, tag), "w"), indent=1, default=str)
        open(outdir + "/CURRENT", "w").close()
        os.remove(outdir + "/CURRENT")
        if not args.keep:
            box.wipe()
    return res


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--panels", default="mistgate")
    ap.add_argument("--reps", type=int, default=3)
    ap.add_argument("--users", type=int, default=1000)
    ap.add_argument("--vcpu", type=int, default=2)
    ap.add_argument("--mem", default="2G")
    ap.add_argument("--phases", default="ABCDE")
    ap.add_argument("--settle", type=int, default=60)
    ap.add_argument("--idle", type=int, default=120)
    ap.add_argument("--tp-dur", type=int, default=30, dest="tp_dur")
    ap.add_argument("--many", type=int, default=400)
    ap.add_argument("--many-dur", type=int, default=60, dest="many_dur")
    ap.add_argument("--bad-dur", type=int, default=180, dest="bad_dur")
    ap.add_argument("--storm-rate", type=int, default=300, dest="storm_rate", help="subscription storm, requests/s over all users (open loop); 0 = as fast as possible")
    ap.add_argument("--restart-dur", type=int, default=60, dest="restart_dur")
    ap.add_argument("--tag", default="box")
    ap.add_argument("--pin", default="", help="host CPUs for the box, e.g. 14-15 (sensitivity check); clients then run on 0-13")
    ap.add_argument("--keep", action="store_true", help="debug: leave the box running")
    ap.add_argument("--skip-done", action="store_true")
    args = ap.parse_args()
    global PIN, CLIENT_PREFIX
    PIN = args.pin
    if PIN:
        CLIENT_PREFIX = "taskset -c 0-13 "
    outdir = "%s/out/%s" % (ROOT, args.tag)
    os.makedirs(outdir, exist_ok=True)
    prepare_infra()
    start_sink()
    names = args.panels.split(",")
    boxes = {n: BOXES[n]() for n in names}
    for b in boxes.values():
        b.prepare_once()
    try:
        for rep in range(1, args.reps + 1):
            order = names[:]
            random.Random(rep).shuffle(order)
            for n in order:
                if args.skip_done and os.path.exists("%s/%s_r%d.json" % (outdir, n, rep)):
                    log("skip %s_r%d (done)" % (n, rep))
                    continue
                open(outdir + "/CURRENT", "w").write("%s_r%d\n" % (n, rep))
                run_one(boxes[n], rep, args, outdir)
    finally:
        if not args.keep:
            stop_sink()
    log("all done")


if __name__ == "__main__":
    main()
