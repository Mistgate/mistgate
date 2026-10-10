import sys, json, time, secrets, subprocess
import os; sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from bench import Client, sh, FW
IP = "11.11.0.30"
sh("docker compose -p benchrembox -f @R@/remna/box.yml down -v 2>&1 | tail -1; rm -rf @R@/data/box-rw; mkdir -p @R@/data/box-rw/cert", check=False)
sh("openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout @R@/data/box-rw/cert/key.pem -out @R@/data/box-rw/cert/cert.pem -days 30 -subj /CN=bench.test 2>/dev/null; chmod 644 @R@/data/box-rw/cert/*")
sh("NODE_SECRET_KEY=placeholder docker compose -p benchrembox -f @R@/remna/box.yml up -d remnawave remnawave-db remnawave-redis 2>&1 | tail -3")
t0 = time.time()
import bench
while not bench.http_ready(IP, 3000, "/api/auth/status", FW) and time.time() - t0 < 120:
    time.sleep(0.5)
print("backend up after %.1fs" % (time.time() - t0))
c = Client(IP, 3000, FW)
pw = "Bn-" + secrets.token_hex(14) + "Aa1"
st, b, _ = c.req("POST", "/api/auth/register", body={"username": "benchadmin", "password": pw}); print("register", st)
c.tok = b["response"]["accessToken"]; c.h["x-remnawave-client-type"] = "browser"
st, b, _ = c.req("POST", "/api/tokens", body={"name": "bench", "expiresInDays": 7}); c.tok = b["response"]["token"]; c.h.pop("x-remnawave-client-type")
st, b, _ = c.req("GET", "/api/keygen"); print("keygen", st, json.dumps(b)[:200])
key = b["response"]["pubKey"]
open("/tmp/rw_key.txt", "w").write(key)
sh("NODE_SECRET_KEY='%s' docker compose -p benchrembox -f @R@/remna/box.yml up -d remnanode 2>&1 | tail -2" % key)
cfg = {"log": {"loglevel": "warning"},
       "inbounds": [{"tag": "hy2", "port": 443, "protocol": "hysteria", "settings": {"version": 2, "clients": []},
                     "streamSettings": {"network": "hysteria", "security": "tls",
                                        "tlsSettings": {"certificates": [{"certificateFile": "/certs/cert.pem", "keyFile": "/certs/key.pem"}], "alpn": ["h3"]},
                                        "hysteriaSettings": {"version": 2}}}],
       "outbounds": [{"tag": "DIRECT", "protocol": "freedom"}, {"tag": "BLOCK", "protocol": "blackhole"}],
       "routing": {"rules": []}}
st, b, _ = c.req("POST", "/api/config-profiles", body={"name": "hy2", "config": cfg}); print("profile", st, json.dumps(b)[:600])
prof = b["response"]; puid = prof["uuid"]; inb = prof["inbounds"][0]["uuid"]
st, b, _ = c.req("POST", "/api/nodes", body={"name": "box1", "address": "11.11.0.31", "port": 2222, "isTrafficTrackingActive": False, "configProfile": {"activeConfigProfileUuid": puid, "activeInbounds": [inb]}, "countryCode": "XX"}); print("node", st, json.dumps(b)[:500])
st, b, _ = c.req("GET", "/api/internal-squads"); sq = b["response"]["internalSquads"][0]; print("squad", sq["uuid"], len(sq["inbounds"]))
st, b, _ = c.req("PATCH", "/api/internal-squads", body={"uuid": sq["uuid"], "inbounds": [inb]}); print("squad patch", st, json.dumps(b)[:300])
st, b, _ = c.req("POST", "/api/hosts", body={"inbound": {"configProfileUuid": puid, "configProfileInboundUuid": inb}, "remark": "box-hy2", "address": "11.11.0.31", "port": 443, "sni": "bench.test", "allowInsecure": True}); print("host", st, json.dumps(b)[:400])
st, b, _ = c.req("POST", "/api/users", body={"username": "user1", "expireAt": "2099-01-01T00:00:00.000Z", "activeInternalSquads": [sq["uuid"]]}); print("user", st, json.dumps(b)[:700])
u = b["response"]
time.sleep(8)
st, b, _ = c.req("GET", "/api/nodes"); print("nodes", json.dumps(b)[:600])
import base64, urllib.request
req = urllib.request.Request("http://%s:3000/api/sub/%s" % (IP, u["shortUuid"]), headers={**FW, "User-Agent": "v2rayN/7.5.0"})
body = urllib.request.urlopen(req).read(); print("SUB", base64.b64decode(body)[:400])
json.dump({"trojan": u["trojanPassword"], "vless": u["vlessUuid"], "ss": u["ssPassword"], "short": u["shortUuid"]}, open("/tmp/rw_user1.json", "w"))
