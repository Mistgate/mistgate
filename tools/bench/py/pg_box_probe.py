import sys, json, time, secrets, uuid, subprocess, re
import os; sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bench
from bench import Client, sh
D = bench.DATA
sh("docker compose -p benchpgbox -f @R@/pasar/box.yml down 2>&1 | tail -1; rm -rf %s/box-pasar %s/box-pasar-node %s/box-pasar-cert; mkdir -p %s/box-pasar %s/box-pasar-node/certs %s/box-pasar-cert" % ((D,) * 6), check=False)
sh("sed -e 's|^UVICORN_HOST = .*|UVICORN_HOST = \"127.0.0.1\"|' @R@/pasar/.env > @R@/pasar/box.env; sed -i 's|^SQLALCHEMY_DATABASE_URL = .*|SQLALCHEMY_DATABASE_URL = \"sqlite+aiosqlite:////var/lib/pasarguard/db.sqlite3\"|' @R@/pasar/box.env")
# node server certificate (SAN = its address) and the TLS cert of the Hysteria2 inbound
sh("openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout %s/box-pasar-node/certs/ssl_key.pem -out %s/box-pasar-node/certs/ssl_cert.pem -days 30 -subj /CN=11.11.0.41 -addext subjectAltName=IP:11.11.0.41 2>/dev/null" % (D, D))
sh("openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout %s/box-pasar-cert/key.pem -out %s/box-pasar-cert/cert.pem -days 30 -subj /CN=bench.test 2>/dev/null; chmod 644 %s/box-pasar-cert/*" % (D, D, D))
apikey = str(uuid.uuid4())
sh("PG_NODE_API_KEY=%s docker compose -p benchpgbox -f @R@/pasar/box.yml up -d 2>&1 | tail -3" % apikey)
t0 = time.time()
while not bench.http_ready("127.0.0.1", 8000, "/") and time.time() - t0 < 90:
    time.sleep(0.5)
print("panel up after %.1fs" % (time.time() - t0))
key = re.search(r"Temp key: (\S+)", sh("docker exec bench_pasarguard_box pasarguard-cli generate-temp-key")).group(1)
c = Client("127.0.0.1", 8000)
pw = "Bn-" + secrets.token_hex(12) + "Aa1"
st, b, _ = c.req("POST", "/api/setup/owner", body={"key": key, "username": "benchadmin", "password": pw}); print("owner", st)
st, b, _ = c.req("POST", "/api/admin/token", form={"username": "benchadmin", "password": pw}); c.tok = b["access_token"]
st, b, _ = c.req("GET", "/api/cores"); core = b["cores"][0]; print("core", core["id"], core["name"])
cfg = core["config"]
cfg["inbounds"] = [{"tag": "Hysteria2 UDP", "listen": "0.0.0.0", "port": 443, "protocol": "hysteria", "settings": {"version": 2, "clients": []},
                    "streamSettings": {"network": "hysteria", "security": "tls",
                                       "tlsSettings": {"certificates": [{"certificateFile": "/certs/cert.pem", "keyFile": "/certs/key.pem"}], "alpn": ["h3"]},
                                       "hysteriaSettings": {"version": 2}}}]
cfg["routing"] = {"rules": []}                    # the default core config blocks geoip:private; the sink is not private, but keep the same (empty) rules as the other panels
st, b, _ = c.req("PUT", "/api/core/%d?restart_nodes=true" % core["id"], body={"name": core["name"], "config": cfg, "exclude_inbound_tags": [], "fallbacks_inbound_tags": []}); print("core put", st, json.dumps(b)[:200])
ca = open("%s/box-pasar-node/certs/ssl_cert.pem" % D).read()
st, b, _ = c.req("POST", "/api/node", body={"name": "box1", "address": "11.11.0.41", "port": 62050, "usage_coefficient": 1, "server_ca": ca, "connection_type": "grpc", "keep_alive": 60, "core_config_id": core["id"], "api_key": apikey}); print("node", st, json.dumps(b)[:300])
st, b, _ = c.req("POST", "/api/group", body={"name": "everyone", "inbound_tags": ["Hysteria2 UDP"]}); print("group", st, json.dumps(b)[:200]); gid = b["id"]
st, b, _ = c.req("POST", "/api/host/", body={"remark": "box-hy2", "address": ["11.11.0.41"], "inbound_tag": "Hysteria2 UDP", "port": 443, "sni": ["bench.test"], "allowinsecure": True, "priority": 1}); print("host", st, json.dumps(b)[:300])
st, b, _ = c.req("POST", "/api/user", body={"username": "user1", "group_ids": [gid], "data_limit": 0}); print("user", st, json.dumps(b)[:500])
u = b
time.sleep(8)
st, b, _ = c.req("GET", "/api/nodes"); print("nodes", json.dumps(b)[:500])
st, body, r = c.req("GET", u["subscription_url"], headers={"User-Agent": "v2rayN/7.5.0"}, raw=True)
import base64
print("SUB", st, base64.b64decode(body)[:400])
json.dump({"hy": u["proxy_settings"]["hysteria"]["auth"]}, open("/tmp/pg_user1.json", "w"))
print(sh("docker logs bench_pasarguard_node_box 2>&1 | tail -5 | cut -c1-200", check=False))
