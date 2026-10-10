import sys, json
import os; sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from bench import Client
c = Client("11.11.11.20", 2053)
st, b, _ = c.req("GET", "/csrf-token")
st, b, _ = c.req("POST", "/login", body={"username": "admin", "password": "admin"}, headers={"X-CSRF-Token": b["obj"]}); print("login", st)
st, b, _ = c.req("GET", "/csrf-token"); c.h["X-CSRF-Token"] = b["obj"]
inb = {"enable": True, "remark": "hy2", "listen": "", "port": 443, "protocol": "hysteria", "expiryTime": 0, "total": 0,
       "settings": {"version": 2, "clients": []},
       "streamSettings": {"network": "hysteria", "security": "tls",
                          "tlsSettings": {"serverName": "", "certificates": [{"certificateFile": "/root/cert/cert.pem", "keyFile": "/root/cert/key.pem"}], "alpn": ["h3"]},
                          "hysteriaSettings": {"version": 2}},
       "sniffing": {"enabled": False, "destOverride": ["http", "tls"], "metadataOnly": False, "routeOnly": False}}
st, b, _ = c.req("POST", "/panel/api/inbounds/add", body=inb); print("inbound", st, json.dumps(b)[:700])
iid = b["obj"]["id"] if isinstance(b, dict) and b.get("obj") else None
if iid:
    st, b, _ = c.req("POST", "/panel/api/clients/add", body={"client": {"email": "user1", "enable": True, "auth": "bench-pass-user1-xxxxxxxx"}, "inboundIds": [iid]}); print("client", st, json.dumps(b)[:500])
    st, b, _ = c.req("GET", "/panel/api/clients/get/user1"); print(json.dumps(b)[:700])
