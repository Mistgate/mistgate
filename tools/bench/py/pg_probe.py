import json, urllib.request, urllib.error, urllib.parse, subprocess, sys
base = "http://127.0.0.1:8000"
tok = None
def call(method, path, body=None, form=None):
    h = {}
    data = None
    if body is not None: data = json.dumps(body).encode(); h["Content-Type"] = "application/json"
    if form is not None: data = urllib.parse.urlencode(form).encode(); h["Content-Type"] = "application/x-www-form-urlencoded"
    if tok: h["Authorization"] = "Bearer " + tok
    req = urllib.request.Request(base + path, data=data, method=method, headers=h)
    try:
        r = urllib.request.urlopen(req, timeout=30); return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()
PW = "Bench-Passw0rd-x1Y2z3w4"
r = call("POST", "/api/admin/token", form={"username": "benchadmin", "password": PW}); print("token", r[0])
tok = json.loads(r[1]).get("access_token")
r = call("POST", "/api/group", {"name": "everyone", "inbound_tags": ["Shadowsocks TCP"]}); print("group", r[0], r[1][:300]); gid = json.loads(r[1]).get("id")
r = call("POST", "/api/host/", {"remark": "bench-host", "address": ["node1.bench.test"], "inbound_tag": "Shadowsocks TCP", "port": 1080, "priority": 1}); print("host", r[0], r[1][:300])
r = call("POST", "/api/user", {"username": "user1", "group_ids": [gid], "data_limit": 0}); print("user", r[0], r[1][:1200])
d = json.loads(r[1]); su = d.get("subscription_url"); print("sub url", su)
import base64
for ua in ["v2rayN/7.5.0", "Mihomo/1.19.31"]:
    path = urllib.parse.urlparse(su).path
    rq = urllib.request.Request(base + path, headers={"User-Agent": ua})
    try:
        rs = urllib.request.urlopen(rq, timeout=30); b = rs.read(); print("SUB", ua, rs.status, dict(rs.headers), len(b), b[:300])
    except urllib.error.HTTPError as e:
        print("SUB", ua, e.code, e.read()[:300])
