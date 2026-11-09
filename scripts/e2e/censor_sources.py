#!/usr/bin/env python3
"""Private, measured adapters for the production Globalping and Bsbord source clients."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ipaddress
import json
import subprocess
import urllib.parse

HOST = "198.51.100.100"
PROBES = [
    {"country": "RU", "asn": 0, "city": "region-a", "network": "home-a", "tags": ["eyeball-network"], "ns": "censor-home"},
    {"country": "RU", "asn": 0, "city": "region-b", "network": "home-b", "tags": ["eyeball-network"], "ns": "censor-panel"},
]
OPS = {
    "op-a|mobile|on": ("op-a", "censor-op-a", "region-a"),
    "op-b|mobile|on": ("op-b", "censor-op-b", "region-b"),
}
MEASUREMENTS = {}


def tcp_connect(namespace, host, port):
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        return False
    allowed = ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "192.0.2.0/24",
               "198.51.100.0/24", "203.0.113.0/24")
    if not any(address in ipaddress.ip_network(network) for network in allowed):
        return False
    code = "import socket,sys; s=socket.socket(); s.settimeout(1.5); s.connect((sys.argv[1],int(sys.argv[2]))); s.close()"
    try:
        result = subprocess.run(["ip", "netns", "exec", namespace, "python3", "-c", code, host, str(port)],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=3, check=False)
        return result.returncode == 0
    except subprocess.TimeoutExpired:
        return False


def send(handler, status, value):
    body = json.dumps(value, separators=(",", ":")).encode()
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def read_json(self):
        return json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))

    def do_GET(self):
        path = urllib.parse.urlsplit(self.path).path
        if self.server.server_port == 9001 and path == "/v1/probes":
            send(self, 200, [{"location": {k: p[k] for k in ("country", "asn", "city", "network")}, "tags": p["tags"]} for p in PROBES])
            return
        if self.server.server_port == 9001 and path.startswith("/v1/measurements/"):
            measurement = MEASUREMENTS.get(path.rsplit("/", 1)[-1])
            if not measurement:
                send(self, 404, {})
                return
            target, port, protocol, probes = measurement
            results = []
            for probe in probes:
                reached = protocol == "TCP" and tcp_connect(probe["ns"], target, port)
                result = {"status": "finished", "stats": {"rcv": int(reached), "avg": 1 if reached else None}}
                results.append({"probe": {k: probe[k] for k in ("city", "asn", "network", "country", "tags")}, "result": result})
            send(self, 200, {"id": path.rsplit("/", 1)[-1], "status": "finished", "probesCount": len(probes), "results": results})
            return
        if self.server.server_port == 9002 and path == "/v1/account":
            send(self, 200, {"balance_credits": 1000, "bonus_credits": 0, "balance_total": 1000,
                            "tier": "lab", "min_interval_sec": 0})
            return
        if self.server.server_port == 9002 and path == "/v1/operators":
            units = []
            for key, (operator, _, region) in OPS.items():
                units.append({"op_key": key, "operator": operator, "name": operator, "region": region,
                              "region_code": "lab", "dpi": "off", "channel_state": "online", "probeable": True})
            send(self, 200, {"units": units, "n_units": len(units), "n_probeable": len(units)})
            return
        send(self, 404, {})

    def do_POST(self):
        path = urllib.parse.urlsplit(self.path).path
        body = self.read_json()
        if self.server.server_port == 9001 and path == "/v1/measurements":
            locations = body.get("locations")
            if isinstance(locations, str):
                probes = MEASUREMENTS.get(locations, (None, None, None, []))[3]
            else:
                probes, picked = [], set()
                for location in locations or []:
                    matched, limit = 0, int(location.get("limit", 1))
                    for i, probe in enumerate(PROBES):
                        if location.get("country") and location["country"] != probe["country"]:
                            continue
                        if location.get("asn") and int(location["asn"]) != probe["asn"]:
                            continue
                        if location.get("city") and location["city"] != probe["city"]:
                            continue
                        if any(tag not in probe["tags"] for tag in location.get("tags", [])):
                            continue
                        if matched >= limit:
                            break
                        matched += 1
                        if i not in picked:
                            probes.append(probe)
                            picked.add(i)
            target = body.get("target", "")
            options = body.get("measurementOptions", {})
            try:
                host, port_text = target.rsplit(":", 1)
                port = int(options.get("port", port_text))
            except (ValueError, TypeError):
                host, port = target, int(options.get("port", 0))
            ident = "lab-%d" % (len(MEASUREMENTS) + 1)
            MEASUREMENTS[ident] = (host, port, options.get("protocol", ""), probes)
            send(self, 202, {"id": ident, "probesCount": len(probes)})
            return
        if self.server.server_port == 9002 and path == "/v1/probe/preview":
            send(self, 200, {"cost_credits": 1})
            return
        if self.server.server_port == 9002 and path == "/v1/probe":
            by_target = {}
            for target in body.get("targets", []):
                try:
                    host, port = target.rsplit(":", 1)
                    port = int(port)
                except ValueError:
                    continue
                legs = {}
                for key in body.get("operators", []):
                    if key not in OPS:
                        continue
                    operator, namespace, region = OPS[key]
                    reached = tcp_connect(namespace, host, port)
                    control_reached = tcp_connect(namespace, "198.51.100.10", 443)
                    # The local source reports DPI's whitelist result only after measuring both the node and a control
                    # address. The simulated TSPU allows the latter and drops the former for these source ranges.
                    dpi = "on" if not reached and control_reached else "off"
                    legs[key] = {"ok": True, "operator": operator, "region": region, "dpi": dpi,
                                 "error": None, "dns_fail": False, "tcp_is_tls": False,
                                 "icmp": {"ok": False}, "tcp": {"ok": reached}}
                by_target[target] = {"by_operator": legs}
            send(self, 200, {"outcome": "done", "cost_credits": 1, "refunded": 0, "by_target": by_target,
                             "skipped": [], "skipped_unavailable": [], "skipped_dpi_off": []})
            return
        send(self, 404, {})


def main():
    servers = [ThreadingHTTPServer((HOST, 9001), Handler), ThreadingHTTPServer((HOST, 9002), Handler)]
    for server in servers:
        import threading
        threading.Thread(target=server.serve_forever, daemon=True).start()
    import threading
    threading.Event().wait()


if __name__ == "__main__":
    main()
