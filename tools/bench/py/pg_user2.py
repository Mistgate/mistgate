import sys, json, re, time
import os; sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from bench import Client, sh
key = re.search(r"Temp key: (\S+)", sh("docker exec bench_pasarguard_box pasarguard-cli generate-temp-key")).group(1)
c = Client("127.0.0.1", 8000)
pw = "Bn-resetPassw0rd-Aa1-zzzzzzzz"
st, b, _ = c.req("PATCH", "/api/setup/owner", body={"key": key, "password": pw}); print("reset", st); time.sleep(2.5)
st, b, _ = c.req("POST", "/api/admin/token", form={"username": "benchadmin", "password": pw}); print("token", st, str(b)[:80]); c.tok = b.get("access_token")
st, b, _ = c.req("GET", "/api/nodes"); print(json.dumps(b)[:300])
st, b, _ = c.req("POST", "/api/user", body={"username": "user2", "group_ids": [1], "data_limit": 0}); a2 = b["proxy_settings"]["hysteria"]["auth"]; print("user2", st)
time.sleep(3)
for name, a in (("user2 (created after node up)", a2), ("user1", json.load(open("/tmp/pg_user1.json"))["hy"])):
    out = sh("@R@/bin/hyload tp -server 11.11.0.41:443 -auth '%s' -sink 11.11.0.1:19000 -streams 4 -d 3s -probe 0" % a, check=False)
    print(name, out[:160])
print(sh("docker logs bench_pasarguard_node_box 2>&1 | tail -6 | cut -c1-200", check=False))
