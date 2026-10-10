import json, urllib.request, urllib.error
base = "http://127.0.0.1:3000"
FW = {"X-Forwarded-For": "127.0.0.1", "X-Forwarded-Proto": "https", "x-remnawave-client-type": "browser"}
tok = None
def call(method, path, body=None, extra=None):
    h = {"Content-Type": "application/json", **FW, **(extra or {})}
    if tok: h["Authorization"] = "Bearer " + tok
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None, method=method, headers=h)
    try:
        r = urllib.request.urlopen(req, timeout=30); return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()
r = call("POST", "/api/auth/login", {"username": "benchadmin", "password": "Bench-Passw0rd-x1Y2z3w4-Qq9Zz"}); print("login", r[0])
tok = json.loads(r[1])["response"]["accessToken"]
r = call("POST", "/api/tokens", {"name": "bench", "expiresInDays": 7}); print("create tok", r[0], r[1][:700])
d = json.loads(r[1])
t2 = d.get("response", {}).get("token")
print("have token", bool(t2))
if t2:
    tok = t2; FW.pop("x-remnawave-client-type")
    r = call("POST", "/api/users", {"username": "user2", "expireAt": "2099-01-01T00:00:00.000Z", "activeInternalSquads": ["2be81895-e718-40eb-a49b-21c769e924e5"]}); print("user", r[0], r[1][:300])
    d = json.loads(r[1])["response"]; su = d["shortUuid"]
    import base64
    for ua in ["v2rayN/7.5.0"]:
        req = urllib.request.Request(base + "/api/sub/" + su, headers={**FW, "User-Agent": ua})
        rs = urllib.request.urlopen(req, timeout=30); b = rs.read(); print("SUB", ua, rs.status, dict(rs.headers), len(b)); print(base64.b64decode(b))
