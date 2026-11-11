#!/usr/bin/env python3
"""Local, deterministic stand-in for GitHub release downloads in e2e runs."""

import http.server
import os
import re
import ssl
import subprocess
import sys
import time
import urllib.parse

root, port, log, flags = os.path.realpath(sys.argv[1]), int(sys.argv[2]), sys.argv[3], sys.argv[4]
tls_cert, tls_key = (sys.argv[5], sys.argv[6]) if len(sys.argv) >= 7 else ("", "")
CUT, HOLD = 1 << 20, 10


def make_tls_certificate(cert, key):
    os.makedirs(os.path.dirname(os.path.realpath(cert)), exist_ok=True)
    os.makedirs(os.path.dirname(os.path.realpath(key)), exist_ok=True)
    try:
        subprocess.run(
            [
                "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "3",
                "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1",
                "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign",
                "-addext", "extendedKeyUsage=serverAuth", "-keyout", key, "-out", cert,
            ],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
    except (OSError, subprocess.CalledProcessError):
        raise SystemExit("could not create the fake release TLS certificate")


def flag(name):
    return os.path.exists(os.path.join(flags, name))


def take(name):  # one-shot flag: true for exactly one caller
    try:
        os.remove(os.path.join(flags, name))
        return True
    except OSError:
        return False


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def note(self, text):
        with open(log, "a", encoding="utf-8") as output:
            output.write("%d %s\n" % (int(time.time()), text))

    def answer(self, code, extra, headers=()):
        self.note('"%s" %d -%s' % (self.requestline, code, extra))
        self.send_response(code)
        for key, value in headers:
            self.send_header(key, value)
        if code >= 300 and code not in (304, 206):
            self.send_header("Content-Length", "0")
        self.end_headers()

    def do_HEAD(self):
        self.serve(False)

    def do_GET(self):
        self.serve(True)

    def serve(self, with_body):
        path = urllib.parse.unquote(urllib.parse.urlsplit(self.path).path)
        request_range = self.headers.get("Range", "")
        extra = (" range=" + request_range if request_range else "") + "".join(
            " %s=present" % header.lower()
            for header in ("Authorization", "Cookie")
            if self.headers.get(header)
        )
        cdn = path.startswith("/cdn/")
        if cdn:
            path = path[len("/cdn") :]
        full = os.path.realpath(os.path.join(root, path.lstrip("/")))
        if not full.startswith(root + os.sep) or not os.path.isfile(full):
            return self.answer(404, extra)
        if not cdn and flag("redirect"):
            return self.answer(
                302,
                extra,
                [("Location", "%s://%s/cdn%s" % (self.server.scheme, self.headers.get("Host", "127.0.0.1:%d" % port), path))],
            )

        size = os.path.getsize(full)
        start, end, code = 0, size - 1, 200
        if request_range:
            match = re.fullmatch(r"bytes=(\d+)-(\d*)", request_range)
            if not match or int(match.group(1)) >= size:
                return self.answer(416, extra, [("Content-Range", "bytes */%d" % size)])
            start, code = int(match.group(1)), 206
            end = min(int(match.group(2)), size - 1) if match.group(2) else size - 1
        length = end - start + 1
        content_type = "application/json" if full.endswith(".json") else "application/octet-stream"
        headers = [("Content-Type", content_type), ("Accept-Ranges", "bytes"), ("Content-Length", str(length))]
        if code == 206:
            headers.append(("Content-Range", "bytes %d-%d/%d" % (start, end, size)))
        cut = with_body and start == 0 and os.path.basename(full).startswith("mistgate-node-") and flag("cut") and take("cut")
        self.answer(code, extra, headers)
        if not with_body:
            return

        with open(full, "rb") as source:
            source.seek(start)
            if cut:
                self.wfile.write(source.read(CUT))
                self.wfile.flush()
                self.note("cut %s after %d of %d bytes (silent for %d s first)" % (path, CUT, length, HOLD))
                time.sleep(HOLD)
                self.close_connection = True
                return
            left = length
            while left > 0:
                chunk = source.read(min(left, 1 << 16))
                if not chunk:
                    break
                self.wfile.write(chunk)
                left -= len(chunk)
                if flag("slow"):
                    self.wfile.flush()
                    time.sleep(0.02)


class Server(http.server.ThreadingHTTPServer):
    def handle_error(self, request, client_address):  # cancelled downloads are expected in update tests
        if not isinstance(sys.exc_info()[1], ConnectionError):
            super().handle_error(request, client_address)


if tls_cert or tls_key:
    if not tls_cert or not tls_key:
        raise SystemExit("both TLS certificate output paths are required")
    make_tls_certificate(tls_cert, tls_key)

server = Server(("127.0.0.1", port), Handler)
server.scheme = "https" if tls_cert else "http"
if tls_cert:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(tls_cert, tls_key)
    server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
