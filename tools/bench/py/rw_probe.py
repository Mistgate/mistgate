import json, urllib.request, urllib.error
base = "http://127.0.0.1:3000"
FW = {"X-Forwarded-For": "127.0.0.1", "X-Forwarded-Proto": "https"}
tok = None
def call(method, path, body=None, extra=None):
    h = {"Content-Type": "application/json", **FW, **(extra or {})}
    if tok: h["Authorization"] = "Bearer " + tok
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None, method=method, headers=h)
    try:
        r = urllib.request.urlopen(req, timeout=30); return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()
print("status", call("GET", "/api/auth/status"))
r = call("POST", "/api/auth/register", {"username": "benchadmin", "password": "Bench-Passw0rd-x1Y2z3w4-Qq9Zz"}); print("register", r[0], r[1][:300])
d = json.loads(r[1]); tok = d.get("response", {}).get("accessToken")
print("tok?", bool(tok))
for p in ["/api/internal-squads", "/api/config-profiles", "/api/hosts", "/api/system/tools/x25519/generate"]:
    r = call("GET" if "x25519" not in p else "POST", p, {} if "x25519" in p else None); print(p, r[0], r[1][:700])
