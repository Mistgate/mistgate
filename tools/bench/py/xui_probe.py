import json, urllib.request, http.cookiejar, sys
base = "http://127.0.0.1:2053"
cj = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))
def call(method, path, body=None, headers=None):
    h = {"Content-Type": "application/json", **(headers or {})}
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None, method=method, headers=h)
    try:
        r = op.open(req, timeout=30); return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()
st, b = call("GET", "/csrf-token"); csrf = json.loads(b)["obj"]; H = {"X-CSRF-Token": csrf}
print("login", call("POST", "/login", {"username": "admin", "password": "admin"}, H))
st, b = call("GET", "/csrf-token"); csrf = json.loads(b)["obj"]; H = {"X-CSRF-Token": csrf}
for p in ["/panel/api/inbounds/list", "/panel/api/server/getNewX25519Cert", "/panel/api/setting/all", "/panel/setting/all"]:
    for m in ("GET", "POST"):
        r = call(m, p, {} if m == "POST" else None, H)
        print(m, p, r[0], r[1][:600])
print("-----")
st, b = call("POST", "/panel/api/setting/all", {}, H)
o = json.loads(b)["obj"]
print({k: v for k, v in o.items() if k.lower().startswith("sub")})
kp = json.loads(call("GET", "/panel/api/server/getNewX25519Cert", None, H)[1])["obj"]
inb = {"enable": True, "remark": "bench", "listen": "", "port": 4443, "protocol": "vless", "expiryTime": 0, "total": 0,
  "settings": {"clients": [], "decryption": "none", "fallbacks": []},
  "streamSettings": {"network": "tcp", "security": "reality", "tcpSettings": {"acceptProxyProtocol": False, "header": {"type": "none"}},
     "realitySettings": {"show": False, "xver": 0, "target": "www.cloudflare.com:443", "serverNames": ["www.cloudflare.com"], "privateKey": kp["privateKey"], "minClientVer": "", "maxClientVer": "", "maxTimediff": 0, "shortIds": ["0123456789abcdef"], "settings": {"publicKey": kp["publicKey"], "fingerprint": "chrome", "serverName": "", "spiderX": "/"}}},
  "sniffing": {"enabled": True, "destOverride": ["http", "tls"], "metadataOnly": False, "routeOnly": False}}
r = call("POST", "/panel/api/inbounds/add", inb, H); print("inbound", r[0], r[1][:700])
iid = json.loads(r[1])["obj"]["id"]
r = call("POST", "/panel/api/clients/add", {"client": {"email": "user1", "enable": True, "totalGB": 0, "expiryTime": 0}, "inboundIds": [iid]}, H); print("client", r[0], r[1][:500])
r = call("GET", "/panel/api/clients/get/user1", None, H); print("get", r[1][:800])
sub = json.loads(r[1])["obj"]["client"]["subId"]
import urllib.request as U
rq = U.Request("http://127.0.0.1:2096" + o["subPath"] + sub, headers={"User-Agent": "v2rayN/7.5.0"})
rs = U.urlopen(rq); body = rs.read()
print("SUB", rs.status, dict(rs.headers), len(body)); 
import base64; print(base64.b64decode(body)[:300])
