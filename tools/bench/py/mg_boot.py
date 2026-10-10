"""Mistgate bootstrap over its Connect-JSON API: setup link -> owner (password+TOTP) -> API token -> profile, node, group, users.
Usage: mg_boot.py <admin_url> <setup_token> <users_n> <outdir>   (all invented data)"""
import sys, json, time, hmac, hashlib, struct, base64, urllib.request, urllib.error, secrets

def totp(secret, step):
    s = secret.upper(); s += "=" * ((8 - len(s) % 8) % 8)
    key = base64.b32decode(s)
    h = hmac.new(key, struct.pack(">Q", step), hashlib.sha1).digest()
    o = h[-1] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o+4])[0] & 0x7fffffff) % 1000000)

class MG:
    def __init__(self, admin): self.admin = admin; self.cookie = None; self.token = None; self.last_step = 0; self.totp = None
    def rpc(self, svc, method, body=None, raw=False):
        req = urllib.request.Request(self.admin + "api/mistgate.admin.v1.%s/%s" % (svc, method), data=json.dumps(body or {}).encode(), method="POST")
        req.add_header("Content-Type", "application/json")
        if self.token: req.add_header("Authorization", "Bearer " + self.token)
        elif self.cookie: req.add_header("Cookie", "__Host-sid=" + self.cookie)
        try:
            r = urllib.request.urlopen(req, timeout=60)
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read() or b"{}"), {}
        b = json.loads(r.read() or b"{}")
        return r.status, b, r.headers
    def stepup(self):
        step = int(time.time()) // 30
        while step <= self.last_step:
            time.sleep(0.5); step = int(time.time()) // 30
        st, b, _ = self.rpc("AuthService", "FinishStepUp", {"totpCode": totp(self.totp, step)})
        assert st == 200, (st, b); self.last_step = step

def main():
    admin, setup_token, n, outdir = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
    m = MG(admin)
    st, b, _ = m.rpc("AuthService", "BeginSetup", {"setupToken": setup_token, "displayName": "Bench owner", "method": "SETUP_METHOD_PASSWORD", "login": "benchowner", "password": "bench-" + secrets.token_hex(8)})
    assert st == 200, (st, b)
    m.totp = b["totpSecret"]; cer = b["ceremonyId"]
    step = int(time.time()) // 30
    req = urllib.request.Request(admin + "api/mistgate.admin.v1.AuthService/FinishSetup", data=json.dumps({"setupToken": setup_token, "ceremonyId": cer, "totpCode": totp(m.totp, step)}).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    r = urllib.request.urlopen(req, timeout=30)
    for c in r.headers.get_all("Set-Cookie") or []:
        if c.startswith("__Host-sid="): m.cookie = c.split("=", 1)[1].split(";")[0]
    assert m.cookie; m.last_step = step
    m.stepup()
    tok = ""
    # structure: profile + node + inbound + group (owner session; token profile cannot do these)
    st, b, _ = m.rpc("ProfileService", "ListProtocols"); print("protocols", [p["id"] for p in b.get("protocols", [])])
    proto = "hysteria2"
    defaults = [p for p in b["protocols"] if p["id"] == proto][0]["defaultSettingsJson"]
    print("defaults", defaults[:300])
    st, b, _ = m.rpc("ProfileService", "CreateProfile", {"protocol": proto, "name": "main", "settingsJson": defaults}); print("CreateProfile", st, str(b)[:300])
    pid = b["profile"]["id"]
    m.stepup()
    st, b, _ = m.rpc("NodeService", "CreateEnrollment", {"name": "bench1", "address": "node1.bench.test", "countryCode": "DE"}); print("CreateEnrollment", st, str(b)[:200])
    nid = b["node"]["id"]
    import os, re, subprocess
    R = os.environ.get("BENCH_ROOT", "/root/bench")
    cmd = b["installCommand"]
    a = {k: re.search(r"--%s (\S+)" % k, cmd).group(1) for k in ("panel", "sni", "ca-sha256", "token")}
    r = subprocess.run([R + "/bin/mistgate-node", "enroll", "--panel", a["panel"], "--sni", a["sni"], "--ca-sha256", a["ca-sha256"], "--token", a["token"], "--state-dir", R + "/mg-node-state"], capture_output=True, text=True)
    print("enroll rc", r.returncode, r.stdout[-200:], r.stderr[-300:])
    st, b, _ = m.rpc("ProfileService", "CreateInbound", {"profileId": pid, "nodeId": nid}); print("CreateInbound", st, str(b)[:400])
    st, b, _ = m.rpc("GroupService", "CreateGroup", {"name": "everyone", "profileIds": [pid]}); print("CreateGroup", st, str(b)[:200])
    gid = b["group"]["id"]
    json.dump({"token": tok, "group": gid, "profile": pid, "node": nid}, open(outdir + "/mg_state.json", "w"))
    t0 = time.time(); urls = []
    for i in range(1, n + 1):
        st, b, _ = m.rpc("UserService", "CreateUser", {"name": "user%d" % i, "groupId": gid})
        assert st == 200, (i, st, b)
        urls.append(b["subscriptionUrl"])
    print("created", n, "users in %.1fs" % (time.time() - t0))
    open(outdir + "/mg_urls.txt", "w").write("\n".join(urls) + "\n")
main()
